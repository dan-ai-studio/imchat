export interface Me {
  id: number
  username: string
  display_name: string
  is_admin: boolean
}

export interface Conversation {
  id: number
  kind: string
  title: string
  next_seq: number
}

export interface Message {
  conversation_id: number
  seq: number
  id: string
  client_msg_id?: string
  sender_id: number
  kind: string
  body: string
  meta?: string
  recall_of?: number
  created_at: number
}

export interface AdminUser {
  id: number
  username: string
  display_name: string
  is_admin: boolean
  status: string
  created_at: number
  conversations: number
  online: boolean
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...init })
  const text = await res.text()
  const data = text ? JSON.parse(text) : {}
  if (!res.ok) {
    throw new Error(data.message || data.error || 'request failed: ' + res.status)
  }
  return data as T
}

function json(body: unknown): RequestInit {
  return { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

export const api = {
  me: () => req<Me>('/api/me'),
  login: (username: string, password: string) => req<Me>('/api/login', json({ username, password })),
  logout: () => req<{ ok: boolean }>('/api/logout', { method: 'POST' }),
  users: () =>
    req<{ users: Array<{ id: number; username: string; display_name: string }> }>('/api/users'),
  createGroup: (title: string, members: number[]) =>
    req<{ id: number; title: string }>('/api/conversations', json({ kind: 'group', title, members })),
  members: (id: number) =>
    req<{ members: Array<{ user_id: number; username: string; display_name: string; role: string; muted: boolean }> }>(
      '/api/conversations/' + id + '/members',
    ),
  muteMember: (id: number, uid: number, minutes: number) =>
    req<{ ok: boolean }>('/api/conversations/' + id + '/members/' + uid + '/mute', json({ minutes })),
  removeMember: (id: number, uid: number) =>
    req<{ ok: boolean }>('/api/conversations/' + id + '/members/' + uid, { method: 'DELETE' }),
  reads: (id: number) =>
    req<{ reads: Array<{ user_id: number; seq: number }> }>('/api/conversations/' + id + '/reads'),
  conversations: () => req<{ conversations: Conversation[] }>('/api/conversations'),
  messages: (id: number, after = 0) =>
    req<{ messages: Message[] }>('/api/conversations/' + id + '/messages?after=' + after + '&limit=300'),
  send: (id: number, body: string, clientMsgId: string, kind = 'text') =>
    req<{ seq: number; id: string }>(
      '/api/conversations/' + id + '/send',
      json({ client_msg_id: clientMsgId, body, kind }),
    ),
  upload: async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    const res = await fetch('/api/upload', { method: 'POST', body: form, credentials: 'same-origin' })
    const text = await res.text()
    const data = text ? JSON.parse(text) : {}
    if (!res.ok) throw new Error(data.message || 'upload failed')
    return data as { id: string; url: string; bytes: number }
  },
  recall: (id: number, seq: number) =>
    req<{ seq: number; recall_of: number }>('/api/conversations/' + id + '/recall', json({ seq })),
  stats: () => req<{ outbox: Record<string, number>; ws_conns: number }>('/api/stats'),

  adminStats: () =>
    req<{ counts: Record<string, number>; outbox: Record<string, number>; ws_conns: number }>('/api/admin/stats'),
  adminUsers: () => req<{ users: AdminUser[] }>('/api/admin/users'),
  adminSetStatus: (id: number, status: string) =>
    req<{ ok: boolean; kicked: number }>('/api/admin/users/' + id + '/status', json({ status })),
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

export function uuid(): string {
  if (crypto.randomUUID) return crypto.randomUUID()
  return Math.random().toString(16).slice(2) + Date.now().toString(16)
}
