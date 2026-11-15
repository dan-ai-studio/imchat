<script setup lang="ts">
// ── 阅读导览 · 前端 A 与 B 都住在这份文件里 ──────────────
// 协议部分（连接、重发、补齐、已读）属前端 A；
// 模板与样式属前端 B。主线见 docs/READING.md。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, uuid, type Community, type Conversation, type Me, type Message } from '../api'

const props = defineProps<{ me: Me }>()
const emit = defineEmits<{ (e: 'logout'): void }>()

const communities = ref<Community[]>([])
const activeCommunity = ref(0)
const conversations = ref<Conversation[]>([])
const activeId = ref(0)
const messages = ref<Message[]>([])
// 草稿与连接状态是这一屏的两块活数据；下面还有缺口提示与统计数字。
const draft = ref('')
const status = ref<'offline' | 'connecting' | 'online'>('offline')
const gapNotice = ref('')
const stats = ref<Record<string, number>>({})
// 消息列表的滚动容器：新消息进来要自动滚到底。
const scroller = ref<HTMLElement | null>(null)

const members = ref<Array<{ user_id: number; username: string; display_name: string; role: string; muted: boolean }>>([])
const showMembers = ref(false)
const showCreate = ref(false)
// 加入社群与审批：邀请码是加入凭证，审批列表只对社群管理员开放。
const showJoin = ref(false)
const joinCode = ref('')
const showRequests = ref(false)
// 房间面板：列表、建房、进场。房间不在会话列表里，进场后才补进去。
const showRooms = ref(false)
const rooms = ref<Array<{ id: number; title: string; owner_id: number; online: number }>>([])
const newRoomTitle = ref('')
// 麦位面板：只在房间里出现，展示谁在麦上、谁举手。
const seats = ref<Array<{ user_id: number; display_name: string; position: number; state: string }>>([])
// 自己的等级：侧栏展示用，不参与任何权限判断。
const myLevel = ref<{ level: number; exp: number; next_at: number } | null>(null)
const joinRequests = ref<Array<{ id: number; user_id: number; username: string; display_name: string }>>([])
// 建群用的标题输入，与消息草稿分开：它们是两件不同的事。
const newTitle = ref('')
const people = ref<Array<{ id: number; username: string; display_name: string }>>([])
const picked = ref<number[]>([])

let ws: WebSocket | null = null
// 连接、重试、退出手动关闭标记都是模块级变量，不是响应式的：它们由事件驱动。
let retry = 0
let retryTimer: number | undefined
let manualDown = false
// 下面这几个 let 是「连接生命周期」的状态，只在事件回调里被改。
let statsTimer: number | undefined

const active = computed(() => conversations.value.find((c) => c.id === activeId.value))
const isGroup = computed(() => active.value?.kind === 'group')
const myRole = computed(() => members.value.find((m) => m.user_id === props.me.id)?.role ?? '')
const canGovern = computed(() => myRole.value === 'owner' || myRole.value === 'admin')
const lastSeq = computed(() => (messages.value.length ? messages.value[messages.value.length - 1].seq : 0))
const statusText = computed(() =>
  status.value === 'online' ? '已连接' : status.value === 'connecting' ? '连接中' : '已断开',
)

// A recall is itself a message: the tombstone points at the sequence it voids,
// so rendering only has to translate "there is a tombstone for seq N".
const recalledSeqs = computed(
  () =>
    new Set(
      // 先挑出所有墓碑，再把它们指向的序号收成集合。
      messages.value
        .filter((m) => m.kind === 'recall' && (m.recall_of ?? 0) > 0)
        .map((m) => m.recall_of as number),
    ),
)

// 合并去重的落点：按 (会话, 序号) 判重，与后端的去重口径一致。
function merge(list: Message[]) {
  const merged = [...messages.value]
  for (const m of list) {
    if (!merged.some((x) => x.conversation_id === m.conversation_id && x.seq === m.seq)) merged.push(m)
  }
  // 排一次序：本地刚发的那条 seq 可能比别人推来的小。
  merged.sort((a, b) => a.seq - b.seq)
  messages.value = merged
}

async function scrollToEnd() {
  await nextTick()
  // 等 Vue 渲染完新节点再滚，否则滚到的是旧的底部。
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

// 补缺口：带上自己的最大序号去问，服务端只回比它大的那些。
async function fillGap(fromSeq: number) {
  if (!activeId.value) return
  try {
    // 只问比 fromSeq 大的：服务端按 seq > after 过滤，回的就是缺的那段。
    const res = await api.messages(activeId.value, fromSeq)
    const missing = res.messages.filter((m) => m.seq > fromSeq)
    // 有缺口才提示；补完之后四秒，提示自己消失。
    if (missing.length) {
      merge(missing)
      gapNotice.value =
        '已补齐 ' + missing.length + ' 条缺口消息（seq ' + fromSeq + '→' + lastSeq.value + '）'
      setTimeout(() => (gapNotice.value = ''), 4000)
    }
    await scrollToEnd()
  } catch {
    /* the next frame will retry */
  }
}

function ack(conversationId: number, seq: number) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'ack', conversation_id: conversationId, seq }))
  }
}

// 收到一帧：先解析、再按 type 分派。解析失败就丢，坏帧不该影响连接。
function onFrame(raw: MessageEvent) {
  let f: any
  try {
    f = JSON.parse(raw.data)
  } catch {
    return
  }
  // 连上之后第一帧是 ready，从那以后才开始认别的帧。
  if (f.type === 'ready') {
    status.value = 'online'
    retry = 0
    return
  }
  if (f.type === 'read') {
    // 已读位点只能前进：取较大值，防止一条乱序帧把它拉回去。
    reads.value[f.user_id] = Math.max(reads.value[f.user_id] ?? 0, f.seq)
    return
  }
  if (f.type === 'rtc') {
    // 服务端重协商：加轨之后它主动 offer，我们答完再应用本地描述。
    void answerRTC(f.signal)
    return
  }
  if (f.type !== 'msg') return

  if (f.conversation_id !== activeId.value) {
    // 不是当前会话：只更新它的水位并立刻 ack，不往当前列表里塞。
    const c = conversations.value.find((x) => x.id === f.conversation_id)
    if (c && f.seq > c.next_seq) c.next_seq = f.seq
    ack(f.conversation_id, f.seq)
    return
  }
  // 重投会带来重复帧：序号不比最后一条大就直接认掉，不重复入列。
  const prev = messages.value[messages.value.length - 1]
  if (prev && f.seq <= prev.seq) {
    ack(f.conversation_id, f.seq)
    return
  }
  // 中间断了就先补；补完再入列，保证列表里不出现空洞。
  if (prev && f.seq > prev.seq + 1) fillGap(prev.seq)
  merge([f as Message])
  ack(f.conversation_id, f.seq)
  // 收尾两步：滚到底、把已读位点报上去。
  scrollToEnd()
  sendRead()
}

function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return
  status.value = 'connecting'
  // https 页面必须用 wss，否则浏览器会直接拒绝这条连接。
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  ws = new WebSocket(proto + '//' + location.host + '/ws')

  ws.onopen = () => {
    status.value = 'online'
    retry = 0
    flushLocal()
    // 连上之后补两件事：补齐缺失的消息、重报一次已读。
    if (activeId.value) {
      fillGap(lastSeq.value)
      sendRead()
    }
  }
  ws.onmessage = onFrame
  // 断线自动重连：指数退避，最多五档；主动退出时不再重连。
  ws.onclose = () => {
    status.value = 'offline'
    ws = null
    if (manualDown) return
    retry = Math.min(retry + 1, 5)
    retryTimer = window.setTimeout(connect, Math.round(400 * Math.pow(1.7, retry)))
  }
  ws.onerror = () => {}
}

// 演示按钮：主动断开并保持断开，好让人看清补齐过程。
function simulateDrop() {
  manualDown = true
  ws?.close()
  gapNotice.value = '已断开。让对面发几条消息，重连后会自动补齐。'
  // 六秒后自己接回去：够对面发几条消息，也够看清提示。
  window.setTimeout(() => {
    manualDown = false
    gapNotice.value = '正在重连…'
    connect()
  }, 6000)
}

// 成员表给「群设置」用：角色、禁言状态都从这一份来。
async function loadMembers() {
  if (!activeId.value) return
  try {
    members.value = (await api.members(activeId.value)).members
  } catch {
    members.value = []
  }
}

// 切会话：清空消息与提示、收起成员面板，再重新拉历史与已读。
async function select(id: number) {
  activeId.value = id
  messages.value = []
  gapNotice.value = ''
  // 切过去先拉全量历史（after = 0），再滚到底。
  showMembers.value = false
  try {
    const res = await api.messages(id, 0)
    messages.value = res.messages
    await scrollToEnd()
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
  // 成员与已读各拉一次；已读转成 userId → seq 的映射，模板里直接用。
  await loadMembers()
  try {
    const rs = await api.reads(id)
    reads.value = Object.fromEntries(rs.reads.map((r) => [r.user_id, r.seq]))
  } catch {
    reads.value = {}
  }
  sendRead()
}

// 发送：先清草稿、生成 client_msg_id，再走「先上屏、后确认」那条路。
async function send() {
  const body = draft.value.trim()
  if (!body || !activeId.value) return
  draft.value = ''
  const clientMsgId = uuid()
  // 先插一条 seq = -1 的本地消息：界面上立刻能看到，等回执再替换。
  merge([
    {
      conversation_id: activeId.value,
      seq: -1,
      id: 'local-' + clientMsgId,
      client_msg_id: clientMsgId,
      // 本地这条的字段要和真消息一致，回执到达时才能原地替换。
      sender_id: props.me.id,
      kind: 'text',
      body,
      // seq 用负数：它比任何真实序号都小，用来标记「还没被服务端确认」。
      created_at: Date.now(),
    },
  ])
  // 立刻滚到底，让人看见自己刚发的那条。
  await scrollToEnd()
  try {
    await api.send(activeId.value, body, clientMsgId)
  } catch (e) {
    localQueue.value.push({ convId: activeId.value, body, clientMsgId, kind: 'text' })
    gapNotice.value = '发送失败，已进入本地重试队列：' + (e as Error).message
  }
}

// 撤回：先请求服务端，再等广播回来的帧更新列表——本地不抢先改。
async function recallMessage(m: Message) {
  if (m.seq <= 0) return
  try {
    await api.recall(m.conversation_id, m.seq)
  } catch (e) {
    gapNotice.value = '撤回失败：' + (e as Error).message
  }
}

// 设备号只存在本地：同一个账号在两台机器上就是两份已读位点。
const deviceId = (() => {
  const key = 'imchat.device'
  let d = localStorage.getItem(key)
  if (!d) {
    d = uuid()
    localStorage.setItem(key, d)
  }
  return d
})()

// 已读表：userId → 最大序号。peerRead 取别人的最大值，用来显示「已读至」。
const reads = ref<Record<number, number>>({})
const peerRead = computed(() => {
  let max = 0
  for (const [uid, seq] of Object.entries(reads.value)) {
    if (Number(uid) !== props.me.id) max = Math.max(max, seq)
  }
  return max
})

// A failed send is not dropped: it waits here and replays on reconnect. Replay
// is safe because the server dedupes on client_msg_id.
const localQueue = ref<Array<{ convId: number; body: string; clientMsgId: string; kind: string }>>([])

async function flushLocal() {
  if (!localQueue.value.length) return
  const queued = [...localQueue.value]
  localQueue.value = []
  // 一条条重发；每条都带原来的 client_msg_id，所以重复到达也无害。
  for (const item of queued) {
    try {
      // 失败的那条会在 catch 里放回队列，等下一次重连再试。
      await api.send(item.convId, item.body, item.clientMsgId, item.kind)
    } catch {
      localQueue.value.push(item)
    }
  }
}

// 已读上报：一个用户一份位点；设备号只用来区分这份位点是从哪台机器报上去的。
function sendRead() {
  if (!ws || ws.readyState !== WebSocket.OPEN || !activeId.value) return
  const seq = lastSeq.value
  if (seq <= 0) return
  ws.send(JSON.stringify({ type: 'read', conversation_id: activeId.value, seq, device: deviceId }))
}

// 选文件用一个隐藏的 input，点按钮时代它点一下。
const fileInput = ref<HTMLInputElement | null>(null)

function pickFile() {
  fileInput.value?.click()
}

async function onFile(e: Event) {
  // 取完立刻清空 input，同一个文件才能被再选一次。
  const input = e.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = ''
  if (!f || !activeId.value) return
  try {
    // 先上传拿文件名，再发一条 kind=image 的消息；两步都成功才算发出去了。
    const up = await api.upload(f)
    await api.send(activeId.value, up.id, uuid(), 'image')
  } catch (err) {
    gapNotice.value = '上传失败：' + (err as Error).message
  }
}

// Mentions live in meta, so the wire format needs no new field for them.
function mentionsMe(m: Message): boolean {
  if (!m.meta) return false
  try {
    // meta 是服务端解析 @ 后写进去的；这里只读，不改。
    const parsed = JSON.parse(m.meta)
    return Array.isArray(parsed.mentions) && parsed.mentions.includes(props.me.id)
  } catch {
    return false
  }
}

// 建群弹窗：先清空已选，再拉一次可选的人（排除自己）。
async function openCreate() {
  // 拉全站用户再排除自己：建群时不该把自己列进候选。
  picked.value = []
  newTitle.value = ''
  try {
    people.value = (await api.users()).users.filter((p) => p.id !== props.me.id)
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
  showCreate.value = true
}

// 会话列表只属于当前社群：切社群时整份换掉，不做合并。
async function loadConversations() {
  if (!activeCommunity.value) {
    conversations.value = []
    return
  }
  const res = await api.communityConversations(activeCommunity.value)
  conversations.value = res.conversations
}

// 换社群：先清空选中与消息，再换列表——否则上一个社群的消息会挂在新会话下面。
async function onCommunityChange() {
  activeId.value = 0
  messages.value = []
  members.value = []
  await loadConversations()
  if (conversations.value.length) await select(conversations.value[0].id)
}

// 房间语音：一条 PeerConnection 负责发布自己的麦克风并接收别人的音轨。
let pc: RTCPeerConnection | null = null

// 进房后建连：offer 由客户端发起，answer 里带着房间里已有的音轨。
async function joinAudio(roomId: number) {
  try {
    // 浏览器要求麦克风必须由用户手势触发；进房按钮就是那个手势。
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    pc = new RTCPeerConnection()
    for (const t of stream.getTracks()) pc.addTrack(t, stream)
    // 每来一路远端音轨就挂一个 <audio>，由浏览器负责播放。
    pc.ontrack = (e) => {
      const audio = new Audio()
      audio.srcObject = e.streams[0]
      audio.play().catch(() => {})
    }
    // 客户端先出 offer：把麦克风轨道发布出去，服务端在 answer 里回房间已有的音轨。
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    const res = await api.rtcOffer(roomId, offer.sdp ?? '')
    await pc.setRemoteDescription({ type: 'answer', sdp: res.sdp })
  } catch (e) {
    gapNotice.value = '语音连接失败：' + (e as Error).message
  }
}

// 服务端重协商的应答：先应用远端描述，再造 answer 回传。
async function answerRTC(signal: { type: string; sdp: string; room_id: number }) {
  if (!pc || signal.type !== 'offer') return
  await pc.setRemoteDescription({ type: 'offer', sdp: signal.sdp })
  const answer = await pc.createAnswer()
  await pc.setLocalDescription(answer)
  await api.rtcAnswer(signal.room_id, answer.sdp ?? '')
}

// 打开房间面板：每次现拉，在线人数是瞬时值。
async function openRooms() {
  try {
    rooms.value = (await api.rooms(activeCommunity.value)).rooms
    showRooms.value = true
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
}

// 建房：建完立刻进去，房主默认在场。
async function createRoom() {
  const title = newRoomTitle.value.trim() || '新房间'
  try {
    const r = await api.createRoom(activeCommunity.value, title)
    newRoomTitle.value = ''
    await enterRoom(r)
  } catch (e) {
    gapNotice.value = '建房失败：' + (e as Error).message
  }
}

// 开摄像头：向已有连接追加一条视频轨，然后重新协商。
// 1v1 视频与多人视频走同一条路径，区别只在房间里坐了几个人。
async function enableVideo() {
  if (!pc) return
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ video: true })
    // 视频轨加进同一条连接：不新建 PeerConnection，也不重进房间。
    for (const t of stream.getTracks()) pc.addTrack(t, stream)
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    const res = await api.rtcRenegotiate(activeId.value, offer.sdp ?? '')
    await pc.setRemoteDescription({ type: 'answer', sdp: res.sdp })
    gapNotice.value = '摄像头已开启'
  } catch (e) {
    gapNotice.value = '开摄像头失败：' + (e as Error).message
  }
}

// 拉自己的等级：进页面与每次发言后刷新一次。
async function loadLevel() {
  try {
    myLevel.value = await api.level(me.id)
  } catch {
    myLevel.value = null
  }
}

// 拉麦位列表：进房与每次操作后都要刷新。
async function loadSeats() {
  if (!activeId.value) return
  try {
    seats.value = (await api.seats(activeId.value)).seats
  } catch {
    seats.value = []
  }
}

// 举手申请上麦。
async function requestSeat() {
  await api.requestSeat(activeId.value)
  await loadSeats()
}

// 房主批准某人上麦。
async function approveSeat(uid: number) {
  await api.approveSeat(activeId.value, uid)
  await loadSeats()
}

// 自己下麦，或房主把人抱下麦。
async function leaveSeat(uid: number) {
  await api.leaveSeat(activeId.value, uid)
  await loadSeats()
}

// 进房：先登记在场，再把房间补进本地会话列表并选中它。
async function enterRoom(room: { id: number; title: string }) {
  await api.enterRoom(room.id)
  if (!conversations.value.some((c) => c.id === room.id)) {
    conversations.value = [...conversations.value, { id: room.id, kind: 'room', title: room.title, next_seq: 0 }]
  }
  showRooms.value = false
  await select(room.id)
  // 进房即开麦：用户点「进入」就是浏览器要求的那个手势。
  await joinAudio(room.id)
  await loadSeats()
}

// 关房：只有房主能关；关掉后从房间列表消失。
async function closeRoom(id: number) {
  await api.closeRoom(id)
  await openRooms()
}

// 当前社群里「我」是不是管理员：决定邀请与审批按钮出不出现。
const isCommunityAdmin = computed(() => {
  const c = communities.value.find((x) => x.id === activeCommunity.value)
  return c?.role === 'owner' || c?.role === 'admin'
})

// 生成一张一次性邀请码：码只显示一次，复制出去就是加入凭证。
async function createInvite() {
  try {
    const res = await api.createInvite(activeCommunity.value)
    gapNotice.value = '邀请码：' + res.code + '（一次性，复制发给对方）'
  } catch (e) {
    gapNotice.value = '生成邀请码失败：' + (e as Error).message
  }
}

// 凭码加入：成功后重拉社群列表，并切到刚加入的那个。
async function joinCommunity() {
  const code = joinCode.value.trim()
  if (!code) return
  try {
    // 先入群再重拉列表：本地不知道新社群叫什么，必须问服务端。
    const res = await api.joinByInvite(code)
    communities.value = (await api.communities()).communities
    showJoin.value = false
    joinCode.value = ''
    activeCommunity.value = res.community_id
    await onCommunityChange()
  } catch (e) {
    gapNotice.value = '加入失败：' + (e as Error).message
  }
}

// 拉待审批列表：只有社群管理员能拿到。
async function loadJoinRequests() {
  try {
    joinRequests.value = (await api.joinRequests(activeCommunity.value)).requests
    showRequests.value = true
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
}

// 批准或拒绝一条申请；批准后对方立刻成为成员。
async function decideJoin(rid: number, approve: boolean) {
  await api.decideJoin(activeCommunity.value, rid, approve)
  await loadJoinRequests()
}

// 建群：没填标题就给个默认的，然后把人一次加进去。
async function createGroup() {
  const title = newTitle.value.trim() || '新群聊'
  try {
    const c = await api.createCommunityGroup(activeCommunity.value, title, picked.value)
    // 成功后三步：关弹窗、重拉列表、切到新群。
    showCreate.value = false
    await loadConversations()
    await select(c.id)
  } catch (e) {
    gapNotice.value = '创建失败：' + (e as Error).message
  }
}

// 禁言与解除走同一个接口：minutes = 0 就是解除。
async function mute(uid: number, minutes: number) {
  if (!activeId.value) return
  await api.muteMember(activeId.value, uid, minutes)
  await loadMembers()
}

// 移出成员：写完重拉成员表，再重拉会话列表——成员数变了。
async function removeMember(uid: number) {
  if (!activeId.value) return
  await api.removeMember(activeId.value, uid)
  await loadMembers()
  await loadConversations()
}

function openAdmin() {
  location.hash = '#/admin'
}

// 统计两秒一拉：它是运维窗口，不影响消息收发。
async function refreshStats() {
  try {
    stats.value = (await api.stats()).outbox
  } catch {
    /* ignore */
  }
}

async function logout() {
  manualDown = true
  ws?.close()
  await api.logout().catch(() => {})
  emit('logout')
}

// 进页面第一件事：先拉社群，再拉该社群的会话，最后连上第一条。
onMounted(async () => {
  try {
    communities.value = (await api.communities()).communities
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
  if (communities.value.length) {
    activeCommunity.value = communities.value[0].id
    await loadConversations()
  }
  // 连接与社群无关：先把长连接建起来，再决定选中哪个会话。
  connect()
  if (conversations.value.length) await select(conversations.value[0].id)
  // 等级是展示值，进页面拉一次就够。
  await loadLevel()
  // 统计数字两秒刷一次：它是运维视角，不需要实时。
  statsTimer = window.setInterval(refreshStats, 2000)
  refreshStats()
})

onBeforeUnmount(() => {
  manualDown = true
  // 卸载时把定时器和连接都收掉，否则热更新会叠出旧连接。
  clearTimeout(retryTimer)
  clearInterval(statsTimer)
  ws?.close()
})

// 切会话时把上一条缺口提示清掉：它只对那个会话有意义。
watch(activeId, () => (gapNotice.value = ''))
</script>

<!-- Chat 模板三栏：左侧会话列表、中间消息流、底部输入框。 -->
<template>
  <div class="shell">
    <!-- 左栏：会话列表。每一项显示标题与最后一条的序号。 -->
    <aside class="sidebar">
      <div class="brand">
        <span class="dot" :class="status" />
        <strong>imchat</strong>
        <span class="who">{{ me.display_name }}<template v-if="myLevel"> · Lv.{{ myLevel.level }}</template></span>
      </div>

      <!-- 社群切换器：会话列表属于当前社群，换社群等于换一份列表。 -->
      <div class="comm">
        <select v-model.number="activeCommunity" @change="onCommunityChange">
          <option v-for="c in communities" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
      </div>

      <!-- 连接状态点：offline / connecting / online，由回调维护。 -->
      <div class="list">
        <!-- 每个会话一行：类型、标题、序号水位。 -->
        <button
          v-for="c in conversations"
          :key="c.id"
          class="conv"
          :class="{ on: c.id === activeId }"
          @click="select(c.id)"
        >
          <!-- 三个 span：类型徽标、标题、序号水位。 -->
          <span class="kind">{{ c.kind === 'group' ? '群' : '私' }}</span>
          <span class="name">{{ c.title || '会话 #' + c.id }}</span>
          <span class="meta">{{ c.next_seq }}</span>
        </button>
        <button class="newgroup" @click="openCreate">+ 新建群聊</button>
        <!-- 没有码的人走「加入」弹窗；有码的人一步进群。 -->
        <button class="newgroup" @click="showJoin = true">+ 加入社群</button>
        <!-- 房间是独立入口：它不在会话列表里，进场后才补进去。 -->
        <button class="newgroup" @click="openRooms">房间</button>
      </div>

      <!-- 底部状态条：连接状态与待投递队列深度。 -->
      <div class="foot">
        <div class="stat">连接 <b>{{ statusText }}</b></div>
        <div class="stat">
          待投递
          <b>{{ Object.entries(stats).map(([k, v]) => k + ':' + v).join(' ') || '—' }}</b>
        </div>
        <!-- 三个按钮：模拟断网（演示补齐）、管理台（仅管理员）、退出。 -->
        <button class="ghost" @click="simulateDrop">模拟断网</button>
        <!-- 社群管理员才看得到：邀请码与待审批。 -->
        <button v-if="isCommunityAdmin" class="ghost" @click="createInvite">邀请</button>
        <button v-if="isCommunityAdmin" class="ghost" @click="loadJoinRequests">待审批</button>
        <button v-if="me.is_admin" class="ghost" @click="openAdmin">管理台</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <!-- 主区：会话标题栏、消息流、输入框。 -->
    <main class="main">
      <header>
        <h2>{{ active ? active.title || '会话 #' + active.id : '未选择会话' }}</h2>
        <button v-if="isGroup" class="ghost small" @click="showMembers = !showMembers">
          成员 {{ members.length }}
        </button>
        <span class="seq">last seq {{ lastSeq }} · 对方已读至 {{ peerRead }}</span>
      </header>

      <!-- 消息流：每条一行；撤回的消息渲染成一行系统提示。 -->
      <div ref="scroller" class="stream">
        <template v-for="m in messages" :key="m.conversation_id + ':' + m.seq">
          <div v-if="m.kind === 'recall'" class="sys">撤回了一条消息</div>
          <!-- 非撤回消息：一条气泡，自己发的靠右。 -->
          <div v-else class="row" :class="{ mine: m.sender_id === me.id }">
            <div
              class="bubble"
              :class="{ pending: m.seq < 0, revoked: recalledSeqs.has(m.seq), mentioned: mentionsMe(m) }"
            >
              <!-- 图片消息：正文就是上传返回的文件名，路径由 /files/ 拼出来。 -->
              <img
                v-if="!recalledSeqs.has(m.seq) && m.kind === 'image'"
                class="pic"
                :src="'/files/' + m.body"
                alt="图片消息"
              />
              <div v-else class="body">{{ recalledSeqs.has(m.seq) ? '该消息已撤回' : m.body }}</div>
              <!-- 底部一行：时间与操作；撤回只对自己发的消息显示。 -->
              <div class="meta">
                <!-- 撤回按钮只在自己发的、已确认、且未撤回的消息上出现。 -->
                <span
                  v-if="m.sender_id === me.id && m.seq > 0 && !recalledSeqs.has(m.seq)"
                  class="act"
                  @click="recallMessage(m)"
                  >撤回</span
                >
                <!-- 序号跟在后面：排查时一眼能对上服务端的记录。 -->
                #{{ m.seq }}
              </div>
            </div>
          </div>
        </template>
        <!-- 空态：一句提示，不画占位骨架。 -->
        <p v-if="!messages.length" class="empty">还没有消息。</p>
      </div>

      <p v-if="gapNotice" class="notice">{{ gapNotice }}</p>

      <!-- 输入区：图片按钮、文本框、发送按钮。 -->
      <footer>
        <input ref="fileInput" class="file" type="file" accept="image/*" @change="onFile" />
        <button class="ghost" title="发送图片" @click="pickFile">图片</button>
        <input
          v-model="draft"
          placeholder="输入消息，回车发送。用 @用户名 提醒对方"
          @keydown.enter.prevent="send"
        />
        <!-- 没输入时按钮不可点：空消息发不出去，也就没机会报错。 -->
        <button class="primary" :disabled="!draft.trim()" @click="send">发送</button>
      </footer>
    </main>

    <!-- 成员抽屉：只在群聊且点了「成员」时出现。 -->
    <aside v-if="showMembers && isGroup" class="drawer">
      <div class="drawer-head">
        <strong>成员</strong>
        <button class="ghost small" @click="showMembers = false">关闭</button>
      </div>
      <!-- 成员列表：名字、角色徽标、禁言标记；有治理权的人还多一行操作。 -->
      <ul>
        <li v-for="m in members" :key="m.user_id">
          <div class="ml">
            <b>{{ m.display_name }}</b>
            <span class="role" :class="m.role">{{ m.role }}</span>
            <!-- 禁言中的成员多一个标记；解除之后这一行就消失。 -->
            <span v-if="m.muted" class="muted-flag">禁言中</span>
          </div>
          <div v-if="canGovern && m.user_id !== me.id" class="mops">
            <button class="tiny" @click="mute(m.user_id, m.muted ? 0 : 30)">
              {{ m.muted ? '解除' : '禁言30分' }}
            </button>
            <!-- 群主不能被移出，所以他对这一列按钮不可见。 -->
            <button v-if="m.role !== 'owner'" class="tiny" @click="removeMember(m.user_id)">移出</button>
          </div>
        </li>
      </ul>
    </aside>

    <!-- 建群弹窗：标题输入 + 成员勾选。 -->
    <div v-if="showCreate" class="modal">
      <div class="sheet">
        <h3>新建群聊</h3>
        <input v-model="newTitle" placeholder="群名称" />
        <!-- 成员勾选：候选来自 /api/users，已经排除自己。 -->
        <ul class="picks">
          <li v-for="p in people" :key="p.id">
            <label>
              <input type="checkbox" :value="p.id" v-model="picked" />
              {{ p.display_name }} <span class="muted">@{{ p.username }}</span>
            </label>
          </li>
        </ul>
        <!-- 底部两个按钮：取消只关弹窗，创建会真的建群并切过去。 -->
        <div class="row-actions">
          <button class="ghost" @click="showCreate = false">取消</button>
          <!-- 创建：走 /api/conversations，成功后关弹窗并切到新群。 -->
          <button class="primary" @click="createGroup">创建</button>
        </div>
      </div>
    </div>

    <!-- 房间面板：列表带在线人数，房主多一个「关房」。 -->
    <div v-if="showRooms" class="modal">
      <div class="sheet">
        <h3>房间</h3>
        <!-- 每行一间房：在线人数是进场时拉到的瞬时值。 -->
        <ul class="picks">
          <li v-for="r in rooms" :key="r.id">
            <span>{{ r.title }} <span class="muted">在线 {{ r.online }}</span></span>
            <span class="mops">
              <button class="tiny" @click="enterRoom(r)">进入</button>
              <button v-if="r.owner_id === me.id" class="tiny" @click="closeRoom(r.id)">关房</button>
            </span>
          </li>
        </ul>
        <!-- 麦位：谁在麦上、谁举手；房主能批准，本人能下麦。 -->
        <div v-if="activeId" class="seats">
          <div class="seats-head">
            <strong>麦位</strong>
            <button class="tiny" @click="requestSeat">举手</button>
            <button class="tiny" @click="enableVideo">开视频</button>
          </div>
          <!-- 每行一个麦位：位置编号、名字、状态。 -->
          <ul>
            <li v-for="s in seats" :key="s.user_id">
              <span>{{ s.position + 1 }}. {{ s.display_name }}</span>
              <span class="muted">{{ s.state }}</span>
              <span class="mops">
                <button v-if="s.state !== 'on_mic'" class="tiny" @click="approveSeat(s.user_id)">批准</button>
                <button v-if="s.user_id === me.id || s.state === 'on_mic'" class="tiny" @click="leaveSeat(s.user_id)">下麦</button>
              </span>
            </li>
          </ul>
        </div>
        <!-- 开房只需要一个名字：房主建完立刻在场。 -->
        <input v-model="newRoomTitle" placeholder="新房间名称" />
        <div class="row-actions">
          <button class="ghost" @click="showRooms = false">关闭</button>
          <button class="primary" @click="createRoom">开房</button>
        </div>
      </div>
    </div>

    <!-- 加入社群弹窗：粘贴邀请码即可，码是一次性的。 -->
    <div v-if="showJoin" class="modal">
      <div class="sheet">
        <h3>加入社群</h3>
        <input v-model="joinCode" placeholder="粘贴邀请码" />
        <div class="row-actions">
          <button class="ghost" @click="showJoin = false">取消</button>
          <button class="primary" @click="joinCommunity">加入</button>
        </div>
      </div>
    </div>

    <!-- 审批弹窗：批准会同时把人加进社群，拒绝只改状态。 -->
    <div v-if="showRequests" class="modal">
      <div class="sheet">
        <h3>待审批（{{ joinRequests.length }}）</h3>
        <!-- 每行两个人名与两个动作：批准会真的把人加进来。 -->
        <ul class="picks">
          <li v-for="r in joinRequests" :key="r.id">
            <span>{{ r.display_name }} <span class="muted">@{{ r.username }}</span></span>
            <span class="mops">
              <button class="tiny" @click="decideJoin(r.id, true)">批准</button>
              <button class="tiny" @click="decideJoin(r.id, false)">拒绝</button>
            </span>
          </li>
        </ul>
        <!-- 关闭只是收起面板，不改任何一条申请的状态。 -->
        <div class="row-actions">
          <button class="ghost" @click="showRequests = false">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.shell { display: grid; grid-template-columns: 260px 1fr; height: 100%; }

.sidebar { display: flex; flex-direction: column; background: var(--panel); border-right: 1px solid var(--line); }
.brand { display: flex; align-items: center; gap: 8px; padding: 16px; border-bottom: 1px solid var(--line); }
.brand strong { letter-spacing: -.02em; }
.who { margin-left: auto; color: var(--muted); font-size: 12px; }
/* 连接状态点：灰=未知、绿=在线、黄=连接中、红=离线。 */
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
.dot.online { background: var(--ok); box-shadow: 0 0 0 3px rgba(53,192,138,.15); }
.dot.connecting { background: var(--warn); }
.dot.offline { background: var(--bad); }

/* 会话列表：可滚动，列表项之间留 4px 缝。 */
.list { flex: 1; overflow-y: auto; padding: 8px; display: grid; gap: 4px; align-content: start; }
/* 会话项：整行可点，选中态用浅蓝底。 */
.conv {
  display: flex; align-items: center; gap: 8px; text-align: left;
  background: transparent; border-color: transparent; padding: 10px 12px; border-radius: 10px;
}
.conv:hover { background: var(--panel-2); }
.conv.on { background: var(--accent-soft); border-color: #2b3a63; }
/* 类型徽标：群 / 私。 */
.kind { font-size: 10px; color: var(--muted); border: 1px solid var(--line); border-radius: 5px; padding: 1px 5px; }
.conv .name { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conv .meta { margin-left: auto; color: var(--muted); font-size: 11px; }
.newgroup { background: transparent; border: 1px dashed var(--line); color: var(--muted); margin-top: 4px; }

/* 侧栏底部：连接状态、待投递数字，三个按钮。 */
.foot { padding: 12px; border-top: 1px solid var(--line); display: grid; gap: 8px; }
.stat { font-size: 12px; color: var(--muted); display: flex; gap: 6px; }
.stat b { color: var(--text); font-weight: 500; }

/* 主区：标题栏固定、消息流占满、输入区固定在底部。 */
.main { display: flex; flex-direction: column; min-width: 0; }
.main header { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--line); }
.main header h2 { margin: 0; font-size: 15px; font-weight: 600; }
.seq { margin-left: auto; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.small { padding: 4px 10px; font-size: 12px; }

/* 消息流：竖向弹性布局，新消息在底部。 */
.stream { flex: 1; overflow-y: auto; padding: 20px; display: flex; flex-direction: column; gap: 10px; }
.row { display: flex; }
.row.mine { justify-content: flex-end; }
/* 气泡：最宽 560px 或 72%，自己发的换配色。 */
.bubble { max-width: min(560px, 72%); background: var(--panel-2); border: 1px solid var(--line); border-radius: 14px; padding: 9px 13px; }
.row.mine .bubble { background: var(--accent); border-color: var(--accent); color: #fff; }
/* 三种气泡状态：待确认（半透明）、已撤回（斜体）、被 @（金色描边）。 */
.bubble.pending { opacity: .55; }
.bubble.revoked { opacity: .5; font-style: italic; }
.bubble.mentioned { box-shadow: 0 0 0 2px #d9a441; }
.bubble .pic { max-width: 260px; max-height: 260px; border-radius: 10px; display: block; }
.file { display: none; }
.bubble .body { white-space: pre-wrap; word-break: break-word; }
.bubble .meta { font-size: 10px; opacity: .6; margin-top: 3px; text-align: right; font-variant-numeric: tabular-nums; }
/* 撤回的系统提示：居中灰字，与正常消息区分开。 */
.sys { text-align: center; color: var(--muted); font-size: 12px; padding: 2px 0; }
.act { cursor: pointer; margin-right: 8px; text-decoration: underline; opacity: .75; }
.act:hover { opacity: 1; }
.empty { color: var(--muted); text-align: center; margin-top: 40px; }

/* 缺口提示条：出现在消息流上方，四秒后消失。 */
.notice { margin: 0; padding: 9px 20px; font-size: 12px; background: var(--accent-soft); color: #b9c8ff; border-top: 1px solid #2b3a63; }

/* 输入区：一条输入框加发送按钮。 */
.main footer { display: flex; gap: 10px; padding: 14px 20px; border-top: 1px solid var(--line); background: var(--panel); }
.main footer input { flex: 1; }

/* 成员抽屉：右栏 240px，头固定、列表可滚动。 */
.drawer { width: 240px; border-left: 1px solid var(--line); background: var(--panel); display: flex; flex-direction: column; }
.drawer-head { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--line); }
/* 抽屉成员列表：可滚动。 */
.drawer ul { list-style: none; margin: 0; padding: 8px; overflow-y: auto; display: grid; gap: 4px; }
.drawer li { padding: 8px 10px; border-radius: 9px; display: grid; gap: 6px; }
.drawer li:hover { background: var(--panel-2); }
.ml { display: flex; align-items: center; gap: 6px; font-size: 13px; }
/* 角色徽标：owner 金色、admin 绿色、member 灰色。 */
.role { font-size: 10px; color: var(--muted); border: 1px solid var(--line); border-radius: 5px; padding: 1px 5px; }
.role.owner { color: #ffd479; border-color: #5a4a20; }
.role.admin { color: #9bd07a; border-color: #2f4526; }
.muted-flag { font-size: 10px; color: var(--bad); }
.mops { display: flex; gap: 6px; }
.tiny { padding: 3px 8px; font-size: 11px; }

/* 建群弹窗：整页遮罩 + 居中卡片。 */
.modal { position: fixed; inset: 0; background: rgba(6,8,12,.66); display: grid; place-items: center; padding: 24px; }
/* 弹窗卡片：固定 360px 宽，内容竖向排。 */
.sheet { width: 360px; background: var(--panel); border: 1px solid var(--line); border-radius: 14px; padding: 22px; display: grid; gap: 14px; }
.sheet h3 { margin: 0; font-size: 16px; }
.picks { list-style: none; margin: 0; padding: 0; max-height: 220px; overflow-y: auto; display: grid; gap: 6px; }
.picks label { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.picks input { width: auto; }
.row-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
