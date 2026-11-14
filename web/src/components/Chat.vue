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

let ws: WebSocket | null = null
let retry = 0
let retryTimer: number | undefined
let manualDown = false

const active = computed(() => conversations.value.find((c) => c.id === activeId.value))
const lastSeq = computed(() => (messages.value.length ? messages.value[messages.value.length - 1].seq : 0))
const statusText = computed(() =>
  status.value === 'online' ? '已连接' : status.value === 'connecting' ? '连接中' : '已断开',
)

function merge(list: Message[]) {
  const seen = new Set<string>()
  const merged = [...messages.value]
  for (const m of list) {
    const key = m.conversation_id + ':' + m.seq
    if (seen.has(key)) continue
    seen.add(key)
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
      gapNotice.value = '已补齐 ' + missing.length + ' 条缺口消息（seq ' + fromSeq + '→' + lastSeq.value + '）'
      setTimeout(() => (gapNotice.value = ''), 4000)
    }
    await scrollToEnd()
  } catch {
    /* next tick will retry */
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
  if (f.type === 'error') return
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
  if (prev && f.seq > prev.seq + 1) {
    fillGap(prev.seq)
  }
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
    const delay = Math.round(400 * Math.pow(1.7, retry))
    retryTimer = window.setTimeout(connect, delay)
  }
  ws.onerror = () => {}
}

function simulateDrop() {
  manualDown = true
  ws?.close()
  gapNotice.value = '已断开。对面现在发消息，重连后会自动补齐。'
  window.setTimeout(() => {
    manualDown = false
    gapNotice.value = '正在重连…'
    connect()
  }, 6000)
}

async function select(id: number) {
  activeId.value = id
  messages.value = []
  gapNotice.value = ''
  try {
    const res = await api.messages(id, 0)
    messages.value = res.messages
    await scrollToEnd()
  } catch (e) {
    gapNotice.value = (e as Error).message
  }
}

async function send() {
  const body = draft.value.trim()
  if (!body || !activeId.value) return
  draft.value = ''
  const clientMsgId = uuid()
  const optimistic: Message = {
    conversation_id: activeId.value,
    seq: -1,
    id: 'local-' + clientMsgId,
    client_msg_id: clientMsgId,
    sender_id: props.me.id,
    kind: 'text',
    body,
    created_at: Date.now(),
  }
  merge([optimistic])
  await scrollToEnd()
  try {
    await api.send(activeId.value, body, clientMsgId)
  } catch (e) {
    gapNotice.value = '发送失败：' + (e as Error).message
  }
}

async function refreshStats() {
  try {
    stats.value = (await api.stats()).outbox
  } catch {
    /* ignore */
  }
}

function openAdmin() {
  location.hash = '#/admin'
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
  setInterval(refreshStats, 2000)
  refreshStats()
})

onBeforeUnmount(() => {
  manualDown = true
  clearTimeout(retryTimer)
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
          <span class="name">{{ c.title || (c.kind === 'dm' ? '私聊 #' + c.id : '群聊 #' + c.id) }}</span>
          <span class="meta">seq {{ c.next_seq }}</span>
        </button>
      </div>

      <div class="foot">
        <div class="stat">连接 <b>{{ statusText }}</b></div>
        <div class="stat">待投递 <b>{{ Object.entries(stats).map(([k, v]) => k + ':' + v).join(' ') || '—' }}</b></div>
        <button class="ghost" @click="simulateDrop">模拟断网</button>
        <button v-if="me.is_admin" class="ghost" @click="openAdmin">管理台</button>
        <button class="ghost" @click="logout">退出</button>
      </div>
    </aside>

    <main class="main">
      <header>
        <h2>{{ active ? active.title || '会话 #' + active.id : '未选择会话' }}</h2>
        <span class="seq">last seq {{ lastSeq }}</span>
      </header>

      <div ref="scroller" class="stream">
        <div v-for="m in messages" :key="m.conversation_id + ':' + m.seq" class="row" :class="{ mine: m.sender_id === me.id }">
          <div class="bubble" :class="{ pending: m.seq < 0 }">
            <div class="body">{{ m.body }}</div>
            <div class="meta">#{{ m.seq }}</div>
          </div>
        </div>
        <p v-if="!messages.length" class="empty">还没有消息。</p>
      </div>

      <p v-if="gapNotice" class="notice">{{ gapNotice }}</p>

      <footer>
        <input v-model="draft" placeholder="输入消息，回车发送" @keydown.enter.prevent="send" />
        <button class="primary" :disabled="!draft.trim()" @click="send">发送</button>
      </footer>
    </main>
  </div>
</template>

<style scoped>
.shell { display: grid; grid-template-columns: 260px 1fr; height: 100%; }

.sidebar {
  display: flex; flex-direction: column;
  background: var(--panel); border-right: 1px solid var(--line);
}
.brand {
  display: flex; align-items: center; gap: 8px;
  padding: 16px; border-bottom: 1px solid var(--line);
}
.brand strong { letter-spacing: -.02em; }
.who { margin-left: auto; color: var(--muted); font-size: 12px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
.dot.online { background: var(--ok); box-shadow: 0 0 0 3px rgba(53,192,138,.15); }
.dot.connecting { background: var(--warn); }
.dot.offline { background: var(--bad); }

.list { flex: 1; overflow-y: auto; padding: 8px; display: grid; gap: 4px; align-content: start; }
.conv {
  display: flex; align-items: center; gap: 8px;
  text-align: left; background: transparent; border-color: transparent;
  padding: 10px 12px; border-radius: 10px;
}
.conv:hover { background: var(--panel-2); }
.conv.on { background: var(--accent-soft); border-color: #2b3a63; }
.conv .name { font-weight: 500; }
.conv .meta { margin-left: auto; color: var(--muted); font-size: 11px; }

.foot { padding: 12px; border-top: 1px solid var(--line); display: grid; gap: 8px; }
.stat { font-size: 12px; color: var(--muted); display: flex; gap: 6px; }
.stat b { color: var(--text); font-weight: 500; }

.main { display: flex; flex-direction: column; min-width: 0; }
.main header {
  display: flex; align-items: baseline; gap: 12px;
  padding: 16px 20px; border-bottom: 1px solid var(--line);
}
.main header h2 { margin: 0; font-size: 15px; font-weight: 600; }
.seq { margin-left: auto; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }

.stream { flex: 1; overflow-y: auto; padding: 20px; display: flex; flex-direction: column; gap: 10px; }
.row { display: flex; }
.row.mine { justify-content: flex-end; }
.bubble {
  max-width: min(560px, 72%);
  background: var(--panel-2); border: 1px solid var(--line);
  border-radius: 14px; padding: 9px 13px;
}
.row.mine .bubble { background: var(--accent); border-color: var(--accent); color: #fff; }
.bubble.pending { opacity: .55; }
.bubble .body { white-space: pre-wrap; word-break: break-word; }
.bubble .meta { font-size: 10px; opacity: .6; margin-top: 3px; text-align: right; font-variant-numeric: tabular-nums; }

.empty { color: var(--muted); text-align: center; margin-top: 40px; }

.notice {
  margin: 0; padding: 9px 20px; font-size: 12px;
  background: var(--accent-soft); color: #b9c8ff; border-top: 1px solid #2b3a63;
}

.main footer {
  display: flex; gap: 10px; padding: 14px 20px;
  border-top: 1px solid var(--line); background: var(--panel);
}
.main footer input { flex: 1; }
</style>
