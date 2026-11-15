-- ── 阅读导览 · 第 5 站（先读这一份）──────────────────────
-- 上一站：站4 发送的前半
-- 下一站：站6 投递器
--
-- 数据形状：一个会话一条序号，一条消息一条待投递记录。
--
-- 七张表分三组：
--   身份   users / sessions
--   会话   conversations（持有序号水位）/ members（会话内角色与禁言）/ messages（只追加）
--   投递   outbox（未送达的义务）/ read_cursors（读到哪了）/ messages_fts（检索影子表）
--
-- 只记住一句就够：序号在 conversations.next_seq 上分配，并与消息行、待投递记录
-- 写在同一个事务里——所以会话序号不可能出现空洞。

-- 身份：一个账号一行。password_hash 存的是 bcrypt；status 是封禁开关，
-- 不是 'active' 就一律拒绝，这就是「封禁立刻生效」的全部机制。
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT    NOT NULL UNIQUE,
  display_name  TEXT    NOT NULL,
  password_hash TEXT    NOT NULL,
  is_admin      INTEGER NOT NULL DEFAULT 0,
  status        TEXT    NOT NULL DEFAULT 'active',
  created_at    INTEGER NOT NULL
);

-- 服务端会话：令牌当主键。删掉一行就等于立刻登出，
-- 这是签名令牌（JWT）做不到、而本项目需要的能力。
CREATE TABLE IF NOT EXISTS sessions (
  token        TEXT    PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- 社群：隔离与治理的边界。一套部署服务多个社群，同一个人可以在多个社群
-- 拥有不同身份，但账号只有一个（users 表不重复）。
CREATE TABLE IF NOT EXISTS communities (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

-- 谁在哪个社群里、是什么角色。owner/admin/member 是社群级角色，
-- 与会话内角色（members 表）是两套正交的主体，互不推导。
CREATE TABLE IF NOT EXISTS community_members (
  community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role         TEXT    NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  joined_at    INTEGER NOT NULL,
  PRIMARY KEY (community_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_community_members_user ON community_members(user_id);

-- 邀请码：凭码直接加入社群。码一次性，用过即废——所以它没有「剩余次数」，
-- 只有一个 used_by；这样「码被谁用了」也能回答。
CREATE TABLE IF NOT EXISTS community_invites (
  code         TEXT    PRIMARY KEY,
  community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
  created_by   INTEGER NOT NULL REFERENCES users(id),
  created_at   INTEGER NOT NULL,
  used_by      INTEGER NOT NULL DEFAULT 0,
  used_at      INTEGER NOT NULL DEFAULT 0
);

-- 加入申请：没有邀请码的人提交申请，由社群管理员审批。
-- 部分唯一索引保证同一个人对同一个社群只有一条待审记录。
CREATE TABLE IF NOT EXISTS community_join_requests (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status       TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
  created_at   INTEGER NOT NULL,
  decided_at   INTEGER NOT NULL DEFAULT 0,
  decided_by   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_join_pending ON community_join_requests(community_id, user_id) WHERE status = 'pending';

-- 会话本身。next_seq 是序号水位：取号就是
-- UPDATE next_seq = next_seq + 1 ... RETURNING，且与消息行同事务。
CREATE TABLE IF NOT EXISTS conversations (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  community_id INTEGER NOT NULL DEFAULT 1 REFERENCES communities(id),
  kind         TEXT    NOT NULL CHECK (kind IN ('dm','group')),
  title      TEXT    NOT NULL DEFAULT '',
  owner_id   INTEGER NOT NULL DEFAULT 0,
  next_seq   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- 会话按社群取列表时走的索引：维度在前，顺序在后。
CREATE INDEX IF NOT EXISTS idx_conversations_community ON conversations(community_id, id);

-- 谁在哪个会话里、是什么角色、被禁言到什么时候。
-- 这一张表就是「会话内角色」这套权力的全部来源；
-- 它与 users.is_admin 代表的平台管理员是两套正交的主体，互不推导。
CREATE TABLE IF NOT EXISTS members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role            TEXT    NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  muted_until     INTEGER NOT NULL DEFAULT 0,
  joined_at       INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_members_user ON members(user_id);

-- 一条消息一行，只追加、不改写。主键是 (conversation_id, seq)：
-- 序号是会话内的物理顺序，不是装饰；它同时是客户端去重与补齐的依据。
-- recall_of 指向被撤回的序号——撤回是追加一条新消息，不是改这一行。
CREATE TABLE IF NOT EXISTS messages (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  seq             INTEGER NOT NULL,
  id              TEXT    NOT NULL UNIQUE,
  client_msg_id   TEXT,
  sender_id       INTEGER NOT NULL REFERENCES users(id),
  kind            TEXT    NOT NULL DEFAULT 'text',
  body            TEXT    NOT NULL DEFAULT '',
  meta            TEXT    NOT NULL DEFAULT '{}',
  recall_of       INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, seq)
);
-- 「不重」的来源：同一个 client_msg_id 重发时，唯一索引让写入拿回原来那条，
-- 而不是插出第二条。注意它是部分索引——没给 client_msg_id 的历史行不受约束。
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_client ON messages(client_msg_id) WHERE client_msg_id IS NOT NULL;

-- 已读是位点，不是逐条标记，所以每个终端一行，
-- 主键是 (会话, 用户, 设备)。用户级的已读取各终端的最大值。
CREATE TABLE IF NOT EXISTS read_cursors (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id       TEXT    NOT NULL DEFAULT '',
  seq             INTEGER NOT NULL DEFAULT 0,
  updated_at      INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, user_id, device_id)
);

-- 一条「消息 × 接收者」的待投递记录。这就是「不丢」的载体：
-- 它与消息同事务写入，只有收到 ack 才消解，没人 ack 就一直重投。
-- attempts / next_at 是重试计数与下次尝试时间，last_error 只用于排障。
CREATE TABLE IF NOT EXISTS outbox (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id      TEXT    NOT NULL,
  conversation_id INTEGER NOT NULL,
  seq             INTEGER NOT NULL,
  recipient_id    INTEGER NOT NULL,
  state           TEXT    NOT NULL DEFAULT 'pending',
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_at         INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT    NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL
);
-- 投递器每 100 毫秒扫的就是这个索引：(state, next_at) 直接对应「到期该投的」。
CREATE INDEX IF NOT EXISTS idx_outbox_due ON outbox(state, next_at);
CREATE INDEX IF NOT EXISTS idx_outbox_recipient ON outbox(recipient_id, state);
-- 只服务检索的影子表：正文进索引，conv 与 seq 原样带着，命中后据此回表。
-- unicode61 分词对中文按字切分，长句精度不如专门的 CJK 分词器——已知限制。
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(body, conv UNINDEXED, seq UNINDEXED, tokenize='unicode61');
