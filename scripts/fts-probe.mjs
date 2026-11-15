const BASE = 'http://127.0.0.1:18091';
const r = await fetch(BASE + '/api/login', {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'alice', password: 'password123' }),
});
const token = r.headers.getSetCookie()[0].split(';')[0].split('=')[1];
const H = { Cookie: 'imchat_session=' + token, 'Content-Type': 'application/json' };

await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST', headers: H,
  body: JSON.stringify({ client_msg_id: 't-1', body: 'deploy the 疼痛 registry tonight' }),
});
await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST', headers: H,
  body: JSON.stringify({ client_msg_id: 't-2', body: 'unrelated english sentence here' }),
});

for (const q of ['疼痛', 'registry', '完全不存在的词']) {
  const res = await fetch(BASE + '/api/admin/messages?q=' + encodeURIComponent(q), { headers: { Cookie: H.Cookie } });
  const body = await res.text();
  console.log(q, '->', res.status, body.slice(0, 260));
}
