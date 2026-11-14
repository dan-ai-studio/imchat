<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, uuid, type Conversation, type Me, type Message } from '../api'

const props = defineProps<{ me: Me }>()
const emit = defineEmits<{ (e: 'logout'): void }>()

const conversations = ref<Conversation[]>([])
const activeId = ref(0)
const messages = ref<Message[]>([])
const draft = ref('')
const status = ref<'offline' | 'connecting' | 'online'>('offline')
const gapNotice = ref('')
const stats = ref<Record<string, number>>({})
const scroller = ref<HTMLElement | null>(null)

const members = ref<Array<{ user_id: number; username: string; display_name: string; role: string; muted: boolean }>>([])
const showMembers = ref(false)
const showCreate = ref(false)
const newTitle = ref('')
const people = ref<Array<{ id: number; username: string; display_name: string }>>([])
const picked = ref<number[]>([])

let ws: WebSocket | null = null
let retry = 0
let retryTimer: number | undefined
let manualDown = false
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
      messages.value
        .filter((m) => m.kind === 'recall' && (m.recall_of ?? 0) > 0)
        .map((m) => m.recall_of as number),
    ),
)

function merge(list: Message[]) {
  const merged = [...messages.value]
  for (const m of list) {
    if (!merged.some((x) => x.conversation_id === m.conversation_id && x.seq === m.seq)) merged.push(m)
  }
  merged.sort((a, b) => a.seq - b.seq)
  messages.value = merged
}

async function scrollToEnd() {
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

async function fillGap(fromSeq: number) {
  if (!activeId.value) return
  try {
    const res = await api.messages(activeId.value, fromSeq)
    const missing = res.messages.filter((m) => m.seq > fromSeq)
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

function onFrame(raw: MessageEvent) {
  let f: any
  try {
    f = JSON.parse(raw.data)
  } catch {
    return
  }
  if (f.type === 'ready') {
    status.value = 'online'
    retry = 0
    return
  }
  if (f.type !== 'msg') return

  if (f.conversation_id !== activeId.value) {
    const c = conversations.value.find((x) => x.id === f.conversation_id)
    if (c && f.seq > c.next_seq) c.next_seq = f.seq
    ack(f.conversation_id, f.seq)
    return
  }
  const prev = messages.value[messages.value.length - 1]
  if (prev && f.seq <= prev.seq) {
    ack(f.conversation_id, f.seq)
    return
  }
  if (prev && f.seq > prev.seq + 1) fillGap(prev.seq)
  merge([f as Message])
  ack(f.conversation_id, f.seq)
  scrollToEnd()
}

function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return
  status.value = 'connecting'
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  ws = new WebSocket(proto + '//' + location.host + '/ws')

  ws.onopen = () => {
    status.value = 'online'
    retry = 0
    if (activeId.value) fillGap(lastSeq.value)
  }
  ws.onmessage = onFrame
  ws.onclose = () => {
    status.value = 'offline'
    ws = null
    if (manualDown) return
    retry = Math.min(retry + 1, 5)
    retryTimer = window.setTimeout(connect, Math.round(400 * Math.pow(1.7, retry)))
  }
  ws.onerror = () => {}
}

function simulateDrop() {
  manualDown = true
  ws?.close()
  gapNotice.value = '已断开。让对面发几条消息，重连后会自动补齐。'
  window.setTimeout(() => {
    manualDown = false
    gapNotice.value = '正在重连…'
    connect()
  }, 6000)
}

async function loadMembers() {
  if (!activeId.value) return
  try {
    members.value = (await api.members(activeId.value)).members
  } catch {
    members.value = []
  }
}

async function select(id: number) {
  activeId.value = id
  messages.value = []
  gapNotice.value = ''
  showMembers.value = false
  try {
    const res = await api.messages(id, 0)
    messages.value = res.messages
    await scrollToEnd()
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
  await loadMembers()
}

async function send() {
  const body = draft.value.trim()
  if (!body || !activeId.value) return
  draft.value = ''
  const clientMsgId = uuid()
  merge([
    {
      conversation_id: activeId.value,
      seq: -1,
      id: 'local-' + clientMsgId,
      client_msg_id: clientMsgId,
      sender_id: props.me.id,
      kind: 'text',
      body,
      created_at: Date.now(),
    },
  ])
  await scrollToEnd()
  try {
    await api.send(activeId.value, body, clientMsgId)
  } catch (e) {
    gapNotice.value = '发送失败：' + (e as Error).message
  }
}

async function recallMessage(m: Message) {
  if (m.seq <= 0) return
  try {
    await api.recall(m.conversation_id, m.seq)
  } catch (e) {
    gapNotice.value = '撤回失败：' + (e as Error).message
  }
}

async function openCreate() {
  picked.value = []
  newTitle.value = ''
  try {
    people.value = (await api.users()).users.filter((p) => p.id !== props.me.id)
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
  showCreate.value = true
}

async function createGroup() {
  const title = newTitle.value.trim() || '新群聊'
  try {
    const c = await api.createGroup(title, picked.value)
    showCreate.value = false
    conversations.value = (await api.conversations()).conversations
    await select(c.id)
  } catch (e) {
    gapNotice.value = '创建失败：' + (e as Error).message
  }
}

async function mute(uid: number, minutes: number) {
  if (!activeId.value) return
  await api.muteMember(activeId.value, uid, minutes)
  await loadMembers()
}

async function removeMember(uid: number) {
  if (!activeId.value) return
  await api.removeMember(activeId.value, uid)
  await loadMembers()
  conversations.value = (await api.conversations()).conversations
}

function openAdmin() {
  location.hash = '#/admin'
}

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

onMounted(async () => {
  const res = await api.conversations()
  conversations.value = res.conversations
  connect()
  if (res.conversations.length) await select(res.conversations[0].id)
  statsTimer = window.setInterval(refreshStats, 2000)
  refreshStats()
})

onBeforeUnmount(() => {
  manualDown = true
  clearTimeout(retryTimer)
  clearInterval(statsTimer)
  ws?.close()
})

watch(activeId, () => (gapNotice.value = ''))
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="dot" :class="status" />
        <strong>imchat</strong>
        <span class="who">{{ me.display_name }}</span>
      </div>

      <div class="list">
        <button
          v-for="c in conversations"
          :key="c.id"
          class="conv"
          :class="{ on: c.id === activeId }"
          @click="select(c.id)"
        >
          <span class="kind">{{ c.kind === 'group' ? '群' : '私' }}</span>
          <span class="name">{{ c.title || '会话 #' + c.id }}</span>
          <span class="meta">{{ c.next_seq }}</span>
        </button>
        <button class="newgroup" @click="openCreate">+ 新建群聊</button>
      </div>

      <div class="foot">
        <div class="stat">连接 <b>{{ statusText }}</b></div>
        <div class="stat">
          待投递
          <b>{{ Object.entries(stats).map(([k, v]) => k + ':' + v).join(' ') || '—' }}</b>
        </div>
        <button class="ghost" @click="simulateDrop">模拟断网</button>
        <button v-if="me.is_admin" class="ghost" @click="openAdmin">管理台</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <main class="main">
      <header>
        <h2>{{ active ? active.title || '会话 #' + active.id : '未选择会话' }}</h2>
        <button v-if="isGroup" class="ghost small" @click="showMembers = !showMembers">
          成员 {{ members.length }}
        </button>
        <span class="seq">last seq {{ lastSeq }}</span>
      </header>

      <div ref="scroller" class="stream">
        <template v-for="m in messages" :key="m.conversation_id + ':' + m.seq">
          <div v-if="m.kind === 'recall'" class="sys">撤回了一条消息</div>
          <div v-else class="row" :class="{ mine: m.sender_id === me.id }">
            <div class="bubble" :class="{ pending: m.seq < 0, revoked: recalledSeqs.has(m.seq) }">
              <div class="body">{{ recalledSeqs.has(m.seq) ? '该消息已撤回' : m.body }}</div>
              <div class="meta">
                <span
                  v-if="m.sender_id === me.id && m.seq > 0 && !recalledSeqs.has(m.seq)"
                  class="act"
                  @click="recallMessage(m)"
                  >撤回</span
                >
                #{{ m.seq }}
              </div>
            </div>
          </div>
        </template>
        <p v-if="!messages.length" class="empty">还没有消息。</p>
      </div>

      <p v-if="gapNotice" class="notice">{{ gapNotice }}</p>

      <footer>
        <input v-model="draft" placeholder="输入消息，回车发送" @keydown.enter.prevent="send" />
        <button class="primary" :disabled="!draft.trim()" @click="send">发送</button>
      </footer>
    </main>

    <aside v-if="showMembers && isGroup" class="drawer">
      <div class="drawer-head">
        <strong>成员</strong>
        <button class="ghost small" @click="showMembers = false">关闭</button>
      </div>
      <ul>
        <li v-for="m in members" :key="m.user_id">
          <div class="ml">
            <b>{{ m.display_name }}</b>
            <span class="role" :class="m.role">{{ m.role }}</span>
            <span v-if="m.muted" class="muted-flag">禁言中</span>
          </div>
          <div v-if="canGovern && m.user_id !== me.id" class="mops">
            <button class="tiny" @click="mute(m.user_id, m.muted ? 0 : 30)">
              {{ m.muted ? '解除' : '禁言30分' }}
            </button>
            <button v-if="m.role !== 'owner'" class="tiny" @click="removeMember(m.user_id)">移出</button>
          </div>
        </li>
      </ul>
    </aside>

    <div v-if="showCreate" class="modal">
      <div class="sheet">
        <h3>新建群聊</h3>
        <input v-model="newTitle" placeholder="群名称" />
        <ul class="picks">
          <li v-for="p in people" :key="p.id">
            <label>
              <input type="checkbox" :value="p.id" v-model="picked" />
              {{ p.display_name }} <span class="muted">@{{ p.username }}</span>
            </label>
          </li>
        </ul>
        <div class="row-actions">
          <button class="ghost" @click="showCreate = false">取消</button>
          <button class="primary" @click="createGroup">创建</button>
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
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
.dot.online { background: var(--ok); box-shadow: 0 0 0 3px rgba(53,192,138,.15); }
.dot.connecting { background: var(--warn); }
.dot.offline { background: var(--bad); }

.list { flex: 1; overflow-y: auto; padding: 8px; display: grid; gap: 4px; align-content: start; }
.conv {
  display: flex; align-items: center; gap: 8px; text-align: left;
  background: transparent; border-color: transparent; padding: 10px 12px; border-radius: 10px;
}
.conv:hover { background: var(--panel-2); }
.conv.on { background: var(--accent-soft); border-color: #2b3a63; }
.kind { font-size: 10px; color: var(--muted); border: 1px solid var(--line); border-radius: 5px; padding: 1px 5px; }
.conv .name { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conv .meta { margin-left: auto; color: var(--muted); font-size: 11px; }
.newgroup { background: transparent; border: 1px dashed var(--line); color: var(--muted); margin-top: 4px; }

.foot { padding: 12px; border-top: 1px solid var(--line); display: grid; gap: 8px; }
.stat { font-size: 12px; color: var(--muted); display: flex; gap: 6px; }
.stat b { color: var(--text); font-weight: 500; }

.main { display: flex; flex-direction: column; min-width: 0; }
.main header { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--line); }
.main header h2 { margin: 0; font-size: 15px; font-weight: 600; }
.seq { margin-left: auto; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.small { padding: 4px 10px; font-size: 12px; }

.stream { flex: 1; overflow-y: auto; padding: 20px; display: flex; flex-direction: column; gap: 10px; }
.row { display: flex; }
.row.mine { justify-content: flex-end; }
.bubble { max-width: min(560px, 72%); background: var(--panel-2); border: 1px solid var(--line); border-radius: 14px; padding: 9px 13px; }
.row.mine .bubble { background: var(--accent); border-color: var(--accent); color: #fff; }
.bubble.pending { opacity: .55; }
.bubble.revoked { opacity: .5; font-style: italic; }
.bubble .body { white-space: pre-wrap; word-break: break-word; }
.bubble .meta { font-size: 10px; opacity: .6; margin-top: 3px; text-align: right; font-variant-numeric: tabular-nums; }
.sys { text-align: center; color: var(--muted); font-size: 12px; padding: 2px 0; }
.act { cursor: pointer; margin-right: 8px; text-decoration: underline; opacity: .75; }
.act:hover { opacity: 1; }
.empty { color: var(--muted); text-align: center; margin-top: 40px; }

.notice { margin: 0; padding: 9px 20px; font-size: 12px; background: var(--accent-soft); color: #b9c8ff; border-top: 1px solid #2b3a63; }

.main footer { display: flex; gap: 10px; padding: 14px 20px; border-top: 1px solid var(--line); background: var(--panel); }
.main footer input { flex: 1; }

.drawer { width: 240px; border-left: 1px solid var(--line); background: var(--panel); display: flex; flex-direction: column; }
.drawer-head { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--line); }
.drawer ul { list-style: none; margin: 0; padding: 8px; overflow-y: auto; display: grid; gap: 4px; }
.drawer li { padding: 8px 10px; border-radius: 9px; display: grid; gap: 6px; }
.drawer li:hover { background: var(--panel-2); }
.ml { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.role { font-size: 10px; color: var(--muted); border: 1px solid var(--line); border-radius: 5px; padding: 1px 5px; }
.role.owner { color: #ffd479; border-color: #5a4a20; }
.role.admin { color: #9bd07a; border-color: #2f4526; }
.muted-flag { font-size: 10px; color: var(--bad); }
.mops { display: flex; gap: 6px; }
.tiny { padding: 3px 8px; font-size: 11px; }

.modal { position: fixed; inset: 0; background: rgba(6,8,12,.66); display: grid; place-items: center; padding: 24px; }
.sheet { width: 360px; background: var(--panel); border: 1px solid var(--line); border-radius: 14px; padding: 22px; display: grid; gap: 14px; }
.sheet h3 { margin: 0; font-size: 16px; }
.picks { list-style: none; margin: 0; padding: 0; max-height: 220px; overflow-y: auto; display: grid; gap: 6px; }
.picks label { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.picks input { width: auto; }
.row-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
