// End-to-end verification: two real WebSocket clients, one shared dm,
// one message, two deliveries, one ack. Run against a live server.
const BASE = 'http://127.0.0.1:18081';

async function login(username, password = 'password123') {
  const res = await fetch(BASE + '/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) throw new Error('login failed for ' + username + ': ' + res.status);
  const body = await res.json();
  const raw = res.headers.getSetCookie()[0];
  const token = raw.split(';')[0].split('=')[1];
  return { body, token };
}

function open(token, label) {
  return new Promise((resolve, reject) => {
    const w = new WebSocket('ws://' + BASE.slice(7) + '/ws?token=' + token);
    const frames = [];
    w.onmessage = (ev) => { try { frames.push(JSON.parse(ev.data)); } catch {} };
    w.onerror = () => reject(new Error(label + ': websocket error'));
    w.onopen = () => setTimeout(() => resolve({ ws: w, frames }), 250);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const alice = await login('alice');
const bob = await login('bob');
console.log('login ok:', alice.body.username, '/', bob.body.username);

const A = await open(alice.token, 'alice');
const B = await open(bob.token, 'bob');
console.log('ws ready:', A.frames[0]?.type, B.frames[0]?.type);

B.ws.send(JSON.stringify({
  type: 'send', client_msg_id: 'ws-1', conversation_id: 1, kind: 'text', body: 'hello over websocket',
}));
await sleep(1000);

const bobMsgs = B.frames.filter((f) => f.type === 'msg');
const aliceMsgs = A.frames.filter((f) => f.type === 'msg');
console.log('bob msg frames:', bobMsgs.map((m) => m.seq + ':' + m.body).join(','));
console.log('alice echo frames:', aliceMsgs.map((m) => m.seq + ':' + m.body).join(','));
console.log('bob send receipts:', B.frames.filter((f) => f.type === 'sent').length);

for (const m of bobMsgs) {
  B.ws.send(JSON.stringify({ type: 'ack', conversation_id: m.conversation_id, seq: m.seq }));
}
await sleep(600);

const stats = await (await fetch(BASE + '/api/stats', {
  headers: { Cookie: 'imchat_session=' + alice.token },
})).json();
console.log('outbox after bob acked:', JSON.stringify(stats));

const hist = await (await fetch(BASE + '/api/conversations/1/messages?after=0&limit=10', {
  headers: { Cookie: 'imchat_session=' + alice.token },
})).json();
console.log('history:', JSON.stringify(hist.messages.map((m) => ({ seq: m.seq, body: m.body }))));

A.ws.close();
B.ws.close();

const pass = bobMsgs.length === 1 && bobMsgs[0].seq === 1 && aliceMsgs.length === 1;
console.log(pass ? 'E2E_PASS' : 'E2E_FAIL');
process.exit(pass ? 0 : 1);
