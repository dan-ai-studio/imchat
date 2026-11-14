CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT    NOT NULL UNIQUE,
  display_name  TEXT    NOT NULL,
  password_hash TEXT    NOT NULL,
  is_admin      INTEGER NOT NULL DEFAULT 0,
  status        TEXT    NOT NULL DEFAULT 'active',
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token        TEXT    PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS conversations (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT    NOT NULL CHECK (kind IN ('dm','group')),
  title      TEXT    NOT NULL DEFAULT '',
  owner_id   INTEGER NOT NULL DEFAULT 0,
  next_seq   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role            TEXT    NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  muted_until     INTEGER NOT NULL DEFAULT 0,
  joined_at       INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_members_user ON members(user_id);

CREATE TABLE IF NOT EXISTS messages (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  seq             INTEGER NOT NULL,
  id              TEXT    NOT NULL UNIQUE,
  client_msg_id   TEXT,
  sender_id       INTEGER NOT NULL REFERENCES users(id),
  kind            TEXT    NOT NULL DEFAULT 'text',
  body            TEXT    NOT NULL DEFAULT '',
  meta            TEXT    NOT NULL DEFAULT '{}',
  created_at      INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, seq)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_client ON messages(client_msg_id) WHERE client_msg_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS read_cursors (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id       TEXT    NOT NULL DEFAULT '',
  seq             INTEGER NOT NULL DEFAULT 0,
  updated_at      INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, user_id, device_id)
);

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
CREATE INDEX IF NOT EXISTS idx_outbox_due ON outbox(state, next_at);
CREATE INDEX IF NOT EXISTS idx_outbox_recipient ON outbox(recipient_id, state);
