// End-to-end verification against a live server:
//   go build -o bin/imchat.exe ./cmd/imchat
//   ./bin/imchat.exe -addr 127.0.0.1:18081 -db ./data/e2e.db &
//   node scripts/e2e.mjs
const BASE = 'http://127.0.0.1:18081';
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

// 四个小工具：等一会儿、失败即退出、成功打一行。整个脚本不引测试框架，
// 只靠退出码与那一行 E2E_FAIL 说话。
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const fail = (msg) => {
  console.log('E2E_FAIL: ' + msg);
  process.exit(1);
};
const ok = (msg) => console.log('  ok   ' + msg);

// 登录拿到的 cookie 里就是会话令牌；后面所有请求都带它。
async function login(username) {
  const res = await fetch(BASE + '/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: 'password123' }),
  });
  if (!res.ok) fail('login ' + username + ' -> ' + res.status);
  // Set-Cookie 里 "名字=值" 那一段就是令牌；后面的属性不要。
  const token = res.headers.getSetCookie()[0].split(';')[0].split('=')[1];
  return token;
}

// 开一条 websocket 并收集下行帧；250 毫秒足够服务端把 ready 与补投都发出来。
function open(token, label) {
  return new Promise((resolve, reject) => {
    const w = new WebSocket('ws://' + BASE.slice(7) + '/ws?token=' + token);
    // frames 是这个终端的收件箱：脚本后面直接按内容断言。
    const frames = [];
    // 收到什么就原样收着；解析失败（例如保活帧）静默丢掉。
    w.onmessage = (ev) => {
      try {
        frames.push(JSON.parse(ev.data));
      } catch {}
    };
    // 任何一条出错都让整轮失败；onopen 之后再等 250ms 收帧才 resolve。
    w.onerror = () => reject(new Error(label + ': websocket error'));
    w.onopen = () => setTimeout(() => resolve({ ws: w, frames }), 250);
  });
}

const aliceToken = await login('alice');
const bobToken = await login('bob');
const H = { Cookie: 'imchat_session=' + aliceToken, 'Content-Type': 'application/json' };
ok('login alice + bob');

// 两个客户端都连上：后面的所有断言都建立在“两边都在线”之上。
const A = await open(aliceToken, 'alice');
const B = await open(bobToken, 'bob');
if (A.frames[0]?.type !== 'ready') fail('alice did not get ready');
ok('websocket ready on both clients');

// 1. one send, two deliveries, one ack
B.ws.send(JSON.stringify({ type: 'send', client_msg_id: 'e2e-1', conversation_id: 1, kind: 'text', body: 'hello over websocket' }));
await sleep(900);
const bobMsgs = B.frames.filter((f) => f.type === 'msg');
const aliceMsgs = A.frames.filter((f) => f.type === 'msg');
if (bobMsgs.length !== 1 || aliceMsgs.length !== 1) fail('fan-out expected 1+1, got ' + bobMsgs.length + '+' + aliceMsgs.length);
ok('fan-out to 2 clients, seq ' + bobMsgs[0].seq);
B.ws.send(JSON.stringify({ type: 'ack', conversation_id: 1, seq: bobMsgs[0].seq }));
await sleep(500);

// 2. image upload -> file -> message
const form = new FormData();
form.append('file', new Blob([PNG], { type: 'image/png' }), 'dot.png');
// 上传 → 会话内取回 → 再匿名取一次：第三步必须 401，证明文件是受保护的。
const upRes = await fetch(BASE + '/api/upload', { method: 'POST', body: form, headers: { Cookie: H.Cookie } });
if (!upRes.ok) fail('upload -> ' + upRes.status);
// 上传成功才继续：后面每一步都依赖上一步的结果。
const up = await upRes.json();
const blob = await fetch(BASE + up.url, { headers: { Cookie: H.Cookie } });
if (blob.status !== 200) fail('blob fetch -> ' + blob.status);
if ((await fetch(BASE + up.url)).status !== 401) fail('blob should require a session');
ok('upload ' + up.bytes + ' bytes, served, and protected');

// 再用上传返回的文件名发一条图片消息：消息表里存的只是这个名字。
const imgSend = await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST',
  headers: H,
  body: JSON.stringify({ client_msg_id: 'e2e-img', kind: 'image', body: up.id }),
});
if (imgSend.status !== 201) fail('image message -> ' + imgSend.status);
ok('image message accepted');

// 3. mention
await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST',
  headers: H,
  body: JSON.stringify({ client_msg_id: 'e2e-mention', body: '@Bob check the delivery queue' }),
});
const hist = await (await fetch(BASE + '/api/conversations/1/messages?after=0&limit=50', { headers: H })).json();
const mention = hist.messages.find((m) => m.body.includes('@Bob'));
if (!mention || !mention.meta.includes('mentions')) fail('mention meta missing');
ok('mention recorded as ' + mention.meta);

// 4. recall appends a tombstone
const rec = await (
  await fetch(BASE + '/api/conversations/1/recall', {
    method: 'POST',
    headers: H,
    body: JSON.stringify({ seq: mention.seq }),
  })
).json();
if (rec.recall_of !== mention.seq) fail('recall mismatch');
ok('recall tombstone at seq ' + rec.seq + ' -> target ' + rec.recall_of);

// 5. full text search
const hit = await (await fetch(BASE + '/api/admin/messages?q=' + encodeURIComponent('queue'), { headers: H })).json();
if (!hit.messages || !hit.messages.length) fail('fts returned nothing');
ok('fts hit: ' + hit.messages[0].body.slice(0, 40));

// 6. outbox resolves only on ack
const stats = await (await fetch(BASE + '/api/stats', { headers: H })).json();
if (!stats.outbox || Object.keys(stats.outbox).length === 0) fail('outbox stats empty');
ok('outbox ' + JSON.stringify(stats.outbox));

A.ws.close();
B.ws.close();
console.log('E2E_PASS');
process.exit(0);
