// ── 阅读导览 · 前端 A（客户端协议）──────────────────────
// 主线见 docs/READING.md。这一站回答：客户端怎么兑现服务端的承诺。
// 下一站：前端 B 渲染与管理台
//
// 与后端共享的线上形状：字段名就是 JSON 键名，改一个就断一个客户端。
// 这几个 interface 只取前端要用的字段，不是 store 层结构体的完整镜像。

// 当前登录者。is_admin 是平台管理员标记，与任何会话内角色无关。
export interface Me {
  id: number
  username: string
  display_name: string
  is_admin: boolean
}

// 会话。next_seq 是序号水位：前端靠它判断自己缺不缺消息。
export interface Conversation {
  id: number
  kind: string
  title: string
  next_seq: number
}

// 社群：隔离与治理的边界。role 是「我」在这个社群里的身份。
export interface Community {
  id: number
  name: string
  description: string
  role: string
}

// 一条消息。client_msg_id 只在发送方存在，是重发的幂等键。
export interface Message {
  conversation_id: number
  seq: number
  id: string
  client_msg_id?: string
  // 发送者与创建时间都由服务端填，前端只读。
  sender_id: number
  kind: string
  body: string
  meta?: string
  // 只有墓碑才带它：指向被作废的那个序号。
  recall_of?: number
  created_at: number
}

// 管理台的账号视图：比 /api/me 多出会话数与在线态。
export interface AdminUser {
  id: number
  username: string
  display_name: string
  is_admin: boolean
  status: string
  created_at: number
  // 后两个是管理台才算得出来的：会话数与在线态。
  conversations: number
  online: boolean
}

// 所有请求都走这一个函数：统一带 cookie、统一把后端的错误变成异常。
async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...init })
  const text = await res.text()
  const data = text ? JSON.parse(text) : {}
  // 后端把错误放在 body 的 message 里；没有 body 就退回状态码。
  if (!res.ok) {
    throw new Error(data.message || data.error || 'request failed: ' + res.status)
  }
  return data as T
}

// POST 的样板：JSON 头 + 序列化好的体。
function json(body: unknown): RequestInit {
  return { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

export const api = {
  me: () => req<Me>('/api/me'),
  login: (username: string, password: string) => req<Me>('/api/login', json({ username, password })),
  logout: () => req<{ ok: boolean }>('/api/logout', { method: 'POST' }),
  // 从这里往下是各组接口：会话、成员、消息、上传、撤回、统计、管理台。
  users: () =>
    req<{ users: Array<{ id: number; username: string; display_name: string }> }>('/api/users'),
  createGroup: (title: string, members: number[]) =>
    req<{ id: number; title: string }>('/api/conversations', json({ kind: 'group', title, members })),
  members: (id: number) =>
    req<{ members: Array<{ user_id: number; username: string; display_name: string; role: string; muted: boolean }> }>(
      '/api/conversations/' + id + '/members',
    ),
  // 成员管理三件：禁言、移出、加人。都是会话内动作，与平台管理员无关。
  muteMember: (id: number, uid: number, minutes: number) =>
    req<{ ok: boolean }>('/api/conversations/' + id + '/members/' + uid + '/mute', json({ minutes })),
  // 移出成员用 DELETE，请求体都不用。
  removeMember: (id: number, uid: number) =>
    req<{ ok: boolean }>('/api/conversations/' + id + '/members/' + uid, { method: 'DELETE' }),
  reads: (id: number) =>
    req<{ reads: Array<{ user_id: number; seq: number }> }>('/api/conversations/' + id + '/reads'),
  // 社群：列表只返回我加入的；会话列表按社群取，隔离由服务端保证。
  communities: () => req<{ communities: Community[] }>('/api/communities'),
  createCommunity: (name: string, description = '') =>
    req<{ id: number; name: string }>('/api/communities', json({ name, description })),
  communityConversations: (id: number) =>
    req<{ conversations: Conversation[] }>('/api/communities/' + id + '/conversations'),
  // 建群必须指定社群：会话的社群维度在创建时固定，之后不改。
  createCommunityGroup: (communityId: number, title: string, members: number[]) =>
    req<{ id: number; title: string }>(
      '/api/communities/' + communityId + '/conversations',
      json({ kind: 'group', title, members }),
    ),
  // 房间：以在场为核心的会话。消息走广播，不进待投递队列。
  rooms: (communityId: number) =>
    req<{ rooms: Array<{ id: number; kind: string; title: string; owner_id: number; online: number }> }>(
      '/api/communities/' + communityId + '/rooms',
    ),
  createRoom: (communityId: number, title: string) =>
    req<{ id: number; title: string }>('/api/communities/' + communityId + '/rooms', json({ title })),
  enterRoom: (id: number) => req<{ id: number; online: number }>('/api/rooms/' + id + '/enter', { method: 'POST' }),
  leaveRoom: (id: number) => req<{ ok: boolean }>('/api/rooms/' + id + '/leave', { method: 'POST' }),
  closeRoom: (id: number) => req<{ ok: boolean }>('/api/rooms/' + id + '/close', { method: 'POST' }),
  // 媒体信令：一次 offer/answer 建连；重协商的 answer 也走同一个形状。
  rtcOffer: (roomId: number, sdp: string) =>
    req<{ type: string; sdp: string }>('/api/rooms/' + roomId + '/rtc/offer', json({ sdp })),
  rtcAnswer: (roomId: number, sdp: string) =>
    req<{ ok: boolean }>('/api/rooms/' + roomId + '/rtc/answer', json({ sdp })),
  rtcLeave: (roomId: number) => req<{ ok: boolean }>('/api/rooms/' + roomId + '/rtc/leave', { method: 'POST' }),
  // 麦位：举手、上麦、下麦。麦位是资源，不是权限。
  seats: (roomId: number) =>
    req<{ seats: Array<{ user_id: number; display_name: string; position: number; state: string }>; max: number }>(
      '/api/rooms/' + roomId + '/seats',
    ),
  requestSeat: (roomId: number) => req<{ ok: boolean }>('/api/rooms/' + roomId + '/seats/request', { method: 'POST' }),
  approveSeat: (roomId: number, uid: number) =>
    req<{ ok: boolean }>('/api/rooms/' + roomId + '/seats/' + uid + '/approve', { method: 'POST' }),
  leaveSeat: (roomId: number, uid: number) =>
    req<{ ok: boolean }>('/api/rooms/' + roomId + '/seats/' + uid, { method: 'DELETE' }),
  // 加入社群的两条路：邀请码直接加入；没有码就提交申请，等管理员审批。
  createInvite: (communityId: number) =>
    req<{ code: string; community_id: number }>('/api/communities/' + communityId + '/invites', { method: 'POST' }),
  joinByInvite: (code: string) =>
    req<{ community_id: number }>('/api/join/' + encodeURIComponent(code), { method: 'POST' }),
  requestJoin: (communityId: number) =>
    req<{ status: string }>('/api/communities/' + communityId + '/requests', { method: 'POST' }),
  // 待审批列表只有社群管理员拿得到，服务端会再校验一次。
  joinRequests: (communityId: number) =>
    req<{ requests: Array<{ id: number; user_id: number; username: string; display_name: string; created_at: number }> }>(
      '/api/communities/' + communityId + '/requests',
    ),
  decideJoin: (communityId: number, rid: number, approve: boolean) =>
    req<{ ok: boolean }>('/api/communities/' + communityId + '/requests/' + rid, json({ approve })),
  // 会话与消息：列表、历史、发送。历史接口的 after 参数就是断线补齐的入口。
  conversations: () => req<{ conversations: Conversation[] }>('/api/conversations'),
  messages: (id: number, after = 0) =>
    req<{ messages: Message[] }>('/api/conversations/' + id + '/messages?after=' + after + '&limit=300'),
  send: (id: number, body: string, clientMsgId: string, kind = 'text') =>
    req<{ seq: number; id: string }>(
      '/api/conversations/' + id + '/send',
      json({ client_msg_id: clientMsgId, body, kind }),
    ),
  // 上传走 multipart，这一条自己写 fetch，不复用 req。
  upload: async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    // 手动写 fetch：FormData 自带 boundary，不能加 JSON 头。
    const res = await fetch('/api/upload', { method: 'POST', body: form, credentials: 'same-origin' })
    const text = await res.text()
    const data = text ? JSON.parse(text) : {}
    if (!res.ok) throw new Error(data.message || 'upload failed')
    return data as { id: string; url: string; bytes: number }
  },
  // 撤回的参数是要作废的那个序号，不是消息 id。
  recall: (id: number, seq: number) =>
    req<{ seq: number; recall_of: number }>('/api/conversations/' + id + '/recall', json({ seq })),
  stats: () => req<{ outbox: Record<string, number>; ws_conns: number }>('/api/stats'),

  // ── 以下都要求平台管理员身份 ──────────────────────────
  adminStats: () =>
    req<{ counts: Record<string, number>; outbox: Record<string, number>; ws_conns: number }>('/api/admin/stats'),
  adminUsers: () => req<{ users: AdminUser[] }>('/api/admin/users'),
  adminSetStatus: (id: number, status: string) =>
    req<{ ok: boolean; kicked: number }>('/api/admin/users/' + id + '/status', json({ status })),
  // 踢下线只断连接、不改账号状态——封禁才是改状态的那一个。
  adminKick: (id: number) =>
    req<{ ok: boolean; kicked: number }>('/api/admin/users/' + id + '/kick', { method: 'POST' }),
  adminConversations: () =>
    req<{ conversations: Array<{ id: number; kind: string; title: string; members: number; messages: number; next_seq: number }> }>(
      '/api/admin/conversations',
    ),
  adminSearch: (q: string) =>
    req<{ messages: Array<{ conversation_id: number; seq: number; sender: string; body: string }> }>(
      '/api/admin/messages?q=' + encodeURIComponent(q),
    ),
}

// 生成 client_msg_id：优先用浏览器自带的 UUID，没有就退回随机串 + 时间戳。
export function uuid(): string {
  if (crypto.randomUUID) return crypto.randomUUID()
  return Math.random().toString(16).slice(2) + Date.now().toString(16)
}
