<script setup lang="ts">
// ── 阅读导览 · 前端 B（渲染与管理台）────────────────────
// 上一站：前端 A 客户端协议
import { onMounted, ref } from 'vue'
import { api, type AdminUser, type Me } from '../api'

defineProps<{ me: Me }>()

// 六块数据各一个 ref：统计、账号、会话、检索词、检索结果、提示语。
const stats = ref<{ counts: Record<string, number>; outbox: Record<string, number>; ws_conns: number } | null>(null)
const users = ref<AdminUser[]>([])
const conversations = ref<Array<{ id: number; kind: string; members: number; messages: number; next_seq: number }>>([])
const query = ref('')
const results = ref<Array<{ conversation_id: number; seq: number; sender: string; body: string }>>([])
const flash = ref('')

// 提示语四秒后自动消失：管理台的操作反馈不需要点确认。
function say(msg: string) {
  flash.value = msg
  setTimeout(() => (flash.value = ''), 4000)
}

async function load() {
  // 三个接口并发拉取：首屏只等最慢的那一个，而不是三个相加。
  const [s, u, c] = await Promise.all([api.adminStats(), api.adminUsers(), api.adminConversations()])
  stats.value = s
  users.value = u.users
  conversations.value = c.conversations
}

async function setStatus(id: number, status: string) {
  const r = await api.adminSetStatus(id, status)
  say((status === 'banned' ? '已封禁' : '已解封') + '，踢下线 ' + r.kicked + ' 个连接')
  // 操作完立刻重拉一次，界面上的数字才和刚才那一下对得上。
  await load()
}

async function kick(id: number) {
  const r = await api.adminKick(id)
  say('踢下线 ' + r.kicked + ' 个连接')
}

// 搜索在按下按钮时跑一次：管理台的检索是点查，不是实时过滤。
async function search() {
  const q = query.value.trim()
  if (!q) return
  results.value = (await api.adminSearch(q)).messages
}

// 进页面加载一次；之后每次改动都会自己重拉。
onMounted(load)
</script>

<!-- 管理台模板自上而下四块：统计卡片、账号表、会话表、检索框。 -->
<template>
  <div class="admin">
    <header>
      <a class="back" href="#">← 聊天</a>
      <h1>平台管理</h1>
      <span class="muted">{{ me.display_name }}</span>
    </header>

    <!-- 第一块：四个计数，来自 /api/admin/stats。 -->
    <section class="cards" v-if="stats">
      <div class="card"><b>{{ stats.counts.users }}</b><span>用户</span></div>
      <div class="card"><b>{{ stats.counts.conversations }}</b><span>会话</span></div>
      <div class="card"><b>{{ stats.counts.messages }}</b><span>消息</span></div>
      <div class="card"><b>{{ stats.ws_conns }}</b><span>实时连接</span></div>
      <!-- 队列深度单独占一行：它涨起来就说明有人在丢消息。 -->
      <div class="card wide">
        <b>{{ Object.entries(stats.outbox).map(([k, v]) => k + ' ' + v).join(' · ') || '空' }}</b>
        <span>投递队列</span>
      </div>
    </section>

    <!-- 操作反馈一行，四秒后自动消失。 -->
    <p v-if="flash" class="flash">{{ flash }}</p>

    <section>
      <h2>用户</h2>
      <!-- 账号表：ID、账号、状态、在线、会话数，最后是操作列。 -->
      <table>
        <thead>
          <tr><th>ID</th><th>账号</th><th>状态</th><th>在线</th><th>会话</th><th>操作</th></tr>
        </thead>
        <tbody>
          <!-- 每行一个账号；在线列来自连接中心，是瞬时值，不进数据库。 -->
          <tr v-for="u in users" :key="u.id">
            <td class="num">{{ u.id }}</td>
            <td>{{ u.display_name }} <span class="muted">@{{ u.username }}</span></td>
            <td><span class="pill" :class="u.status">{{ u.status }}</span></td>
            <td>{{ u.online ? '●' : '—' }}</td>
            <td class="num">{{ u.conversations }}</td>
            <!-- 操作列：封禁/解封二选一，再加一个只管连接的踢下线。 -->
            <td class="ops">
              <button v-if="u.status === 'active'" @click="setStatus(u.id, 'banned')">封禁</button>
              <button v-else @click="setStatus(u.id, 'active')">解封</button>
              <!-- 封禁会连带清掉会话；踢下线只断当前连接。 -->
              <button @click="kick(u.id)">踢下线</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- 第二块：会话表，带成员数、消息数与序号水位。 -->
    <section>
      <h2>会话</h2>
      <table>
        <thead><tr><th>ID</th><th>类型</th><th>成员</th><th>消息</th><th>next_seq</th></tr></thead>
        <tbody>
          <!-- 每行一个会话；next_seq 与消息数对不上就说明这个会话有问题。 -->
          <tr v-for="c in conversations" :key="c.id">
            <td class="num">{{ c.id }}</td>
            <td>{{ c.kind }}</td>
            <td class="num">{{ c.members }}</td>
            <td class="num">{{ c.messages }}</td>
            <!-- next_seq 是序号水位；和消息数一起看就知道这个会话有没有卡住。 -->
            <td class="num">{{ c.next_seq }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <section>
      <!-- 第三块：按内容检索，走 fts5 索引。 -->
      <h2>消息检索</h2>
      <div class="searchbar">
        <input v-model="query" placeholder="按内容搜索" @keydown.enter.prevent="search" />
        <button class="primary" @click="search">搜索</button>
      </div>
      <!-- 命中结果一行一条：会话/序号、发送者、正文。 -->
      <ul class="hits">
        <li v-for="m in results" :key="m.conversation_id + ':' + m.seq">
          <span class="tag">#{{ m.conversation_id }}/{{ m.seq }}</span>
          <b>{{ m.sender }}</b>
          <span>{{ m.body }}</span>
        </li>
      </ul>
      <!-- 一条都没搜到时给一句提示，不留空白。 -->
      <p v-if="query && !results.length" class="muted">无结果。</p>
    </section>
  </div>
</template>

<style scoped>
/* 页面容器：最宽 1040px、居中，底部多留一点空。 */
.admin { max-width: 1040px; margin: 0 auto; padding: 28px 24px 60px; }
header { display: flex; align-items: center; gap: 14px; margin-bottom: 22px; }
header h1 { margin: 0; font-size: 19px; letter-spacing: -.02em; }
header .muted { margin-left: auto; }
.back { color: var(--muted); text-decoration: none; font-size: 13px; }
.back:hover { color: var(--text); }

.cards { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-bottom: 20px; }
/* 卡片本体：数值在上、文字在下，数字用等宽衬线对齐。 */
.card {
  background: var(--panel); border: 1px solid var(--line); border-radius: 12px;
  padding: 14px 16px; display: grid; gap: 2px;
}
.card b { font-size: 20px; font-variant-numeric: tabular-nums; }
.card span { color: var(--muted); font-size: 12px; }
.card.wide { grid-column: span 4; }
.card.wide b { font-size: 14px; }

/* 操作反馈条：四秒后由脚本清空，不占固定位置。 */
.flash {
  background: var(--accent-soft); border: 1px solid #2b3a63; color: #b9c8ff;
  padding: 9px 13px; border-radius: 10px; font-size: 13px; margin: 0 0 18px;
}

/* 三个区块共用的间距与标题样式。 */
section { margin-bottom: 26px; }
h2 { font-size: 14px; margin: 0 0 10px; color: var(--muted); font-weight: 500; }

/* 表格：只有底部细线，不用竖线，读起来更安静。 */
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th {
  text-align: left; font-weight: 500; color: var(--muted); font-size: 12px;
  padding: 8px 10px; border-bottom: 1px solid var(--line);
}
td { padding: 9px 10px; border-bottom: 1px solid #1e222c; }
tr:last-child td { border-bottom: none; }
.num { font-variant-numeric: tabular-nums; }

/* 状态标签：正常绿、封禁红。 */
.pill { font-size: 11px; padding: 2px 8px; border-radius: 999px; background: #24301f; color: #9bd07a; }
.pill.banned { background: #33201f; color: #f0908f; }

/* 操作列：三个小按钮横排。 */
.ops { display: flex; gap: 6px; }
.ops button { padding: 4px 10px; font-size: 12px; }

/* 检索框与命中列表。 */
.searchbar { display: flex; gap: 8px; margin-bottom: 12px; max-width: 520px; }
.hits { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.hits li {
  display: flex; gap: 10px; align-items: baseline;
  background: var(--panel); border: 1px solid var(--line); border-radius: 9px; padding: 9px 12px;
  font-size: 13px;
}
.tag { color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; }
</style>
