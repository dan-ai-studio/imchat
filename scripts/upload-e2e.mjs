const BASE = 'http://127.0.0.1:18090';
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');

const loginRes = await fetch(BASE + '/api/login', {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: 'alice', password: 'password123' }),
});
const token = loginRes.headers.getSetCookie()[0].split(';')[0].split('=')[1];
const H = { Cookie: 'imchat_session=' + token };

const fd = new FormData();
fd.append('file', new Blob([PNG], { type: 'image/png' }), 'dot.png');
const up = await fetch(BASE + '/api/upload', { method: 'POST', body: fd, headers: H });
const upBody = await up.json();
console.log('upload:', up.status, JSON.stringify(upBody));

const fileRes = await fetch(BASE + upBody.url, { headers: H });
console.log('fetch blob:', fileRes.status, fileRes.headers.get('content-type'), (await fileRes.arrayBuffer()).byteLength, 'bytes');

const noAuth = await fetch(BASE + upBody.url);
console.log('blob without session:', noAuth.status);

const send = await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST', headers: { ...H, 'Content-Type': 'application/json' },
  body: JSON.stringify({ client_msg_id: 'img-1', kind: 'image', body: upBody.id }),
});
console.log('send image:', send.status, JSON.stringify(await send.json()));

const text = await fetch(BASE + '/api/conversations/1/send', {
  method: 'POST', headers: { ...H, 'Content-Type': 'application/json' },
  body: JSON.stringify({ client_msg_id: 'txt-1', body: 'deploy the 疼痛 registry tonight' }),
});
console.log('send text:', text.status);

const hit = await fetch(BASE + '/api/admin/messages?q=' + encodeURIComponent('疼痛'), { headers: H });
const hitBody = await hit.json();
console.log('fts hit:', JSON.stringify(hitBody.messages?.map((m) => m.body)));

const miss = await fetch(BASE + '/api/admin/messages?q=' + encodeURIComponent('完全不存在的词'), { headers: H });
console.log('fts miss count:', (await miss.json()).messages?.length);

const hist = await fetch(BASE + '/api/conversations/1/messages?after=0&limit=10', { headers: H });
console.log('history kinds:', JSON.stringify((await hist.json()).messages.map((m) => m.kind + ':' + m.seq)));
