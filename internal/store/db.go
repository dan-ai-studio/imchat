package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 5

//go:embed schema.sql
var schemaFS embed.FS

// upgrades maps "current version" to the statements that move it up one step.
// A fresh database applies schema.sql (already at the latest shape) and skips
// this entirely; an existing database walks these in order.
var upgrades = map[int][]string{
	1: {"ALTER TABLE messages ADD COLUMN recall_of INTEGER NOT NULL DEFAULT 0"},
	2: {
		"CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(body, conv UNINDEXED, seq UNINDEXED, tokenize='unicode61')",
		"INSERT INTO messages_fts (body, conv, seq) SELECT body, conversation_id, seq FROM messages",
	},
	// 3 → 4：把「一个实例一个世界」改成「一个实例多个社群」。
	// 旧数据全部落进默认社群（id=1），所以升级后行为不变。
	3: {
		"CREATE TABLE IF NOT EXISTS communities (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS community_members (community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')), joined_at INTEGER NOT NULL, PRIMARY KEY (community_id, user_id))",
		"CREATE INDEX IF NOT EXISTS idx_community_members_user ON community_members(user_id)",
		"ALTER TABLE conversations ADD COLUMN community_id INTEGER NOT NULL DEFAULT 1",
		"CREATE INDEX IF NOT EXISTS idx_conversations_community ON conversations(community_id, id)",
	},
	// 4 → 5：加入社群的两条路——一次性邀请码，或提交申请由管理员审批。
	4: {
		"CREATE TABLE IF NOT EXISTS community_invites (code TEXT PRIMARY KEY, community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE, created_by INTEGER NOT NULL REFERENCES users(id), created_at INTEGER NOT NULL, used_by INTEGER NOT NULL DEFAULT 0, used_at INTEGER NOT NULL DEFAULT 0)",
		"CREATE TABLE IF NOT EXISTS community_join_requests (id INTEGER PRIMARY KEY AUTOINCREMENT, community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')), created_at INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0, decided_by INTEGER NOT NULL DEFAULT 0)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_join_pending ON community_join_requests(community_id, user_id) WHERE status = 'pending'",
	},
}

// writeReq is one queued write: the closure to run, plus the channel that
// carries its error back to the caller.
type writeReq struct {
	ctx context.Context
	fn  func(context.Context, *sql.Tx) error
	res chan error
}

// DB holds two handles on the same SQLite file:
//   - rdb: a pool of readers. WAL lets them run concurrently with the writer.
//   - wdb: exactly one connection, and every write goes through a single
//     goroutine that owns it. This is the physical basis of the guarantee that
//     "allocating a conversation sequence and inserting the message happen
//     atomically", so a message sequence can never contain a hole.
type DB struct {
	rdb    *sql.DB
	wdb    *sql.DB
	writes chan writeReq
	done   chan struct{}
	once   sync.Once
}

// Open builds the two handles: a small reader pool and a single writer
// connection. Every pragma here is load-bearing; see the comments inline.
func Open(path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	// 四个 pragma 各有理由：忙等 5 秒、WAL、外键打开、fsync 降一档。
	rdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open reader: %w", err)
	}
	rdb.SetMaxOpenConns(8)
	rdb.SetMaxIdleConns(8)

	// _txlock=immediate makes every write transaction take the write lock up
	// front instead of failing on a deferred upgrade.
	wdb, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("open writer: %w", err)
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	// 写队列给 256 格：够吸收一次突发，又能在真堵住时把压力传回去。
	d := &DB{
		rdb:    rdb,
		wdb:    wdb,
		writes: make(chan writeReq, 256),
		done:   make(chan struct{}),
	}
	go d.writeLoop()
	return d, nil
}

// Read returns the reader pool. Queries must not write.
func (d *DB) Read() *sql.DB { return d.rdb }

// Write runs fn inside a transaction owned by the single writer goroutine.
// Write hands fn to the single writer goroutine and waits for its error. The two
// selects are the interesting part: either half can lose the race to shutdown.
func (d *DB) Write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	// 先排队；排队阶段就要能被取消，不然关机会卡在一个排队中的写上。
	req := writeReq{ctx: ctx, fn: fn, res: make(chan error, 1)}
	select {
	case d.writes <- req:
	case <-d.done:
		return sql.ErrConnDone
	case <-ctx.Done():
		return ctx.Err()
	}
	// 再等结果；同样允许在关机和取消时放弃等待。
	select {
	case err := <-req.res:
		return err
	case <-d.done:
		return sql.ErrConnDone
	}
}

// writeLoop is the only goroutine that ever runs a write transaction, so writes
// are serialised by construction rather than by a mutex.
func (d *DB) writeLoop() {
	for {
		select {
		case req := <-d.writes:
			req.res <- d.exec(req)
		case <-d.done:
			return
		}
	}
}

// exec runs one queued write inside its own transaction and reports the outcome
// back on the request's channel.
func (d *DB) exec(req writeReq) error {
	tx, err := d.wdb.BeginTx(req.ctx, nil)
	if err != nil {
		return err
	}
	if err := req.fn(req.ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close shuts the readers down first, then checkpoints and truncates the WAL,
// then closes the writer last — so a clean exit leaves exactly one .db file.
func (d *DB) Close() error {
	d.once.Do(func() { close(d.done) })
	// Close the readers first, then checkpoint and truncate the write-ahead log,
	// then close the writer last. SQLite removes -wal/-shm when the final
	// connection closes, so this leaves a single .db file behind.
	// 顺序是刻意的：读者先走，WAL 才能被完整 checkpoint 掉。
	errR := d.rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = d.wdb.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	cancel()
	if err := d.wdb.Close(); err != nil {
		return err
	}
	return errR
}

// Backup writes a consistent snapshot with VACUUM INTO. Copying the .db file
// while the server is running is unsafe under WAL: this is the supported way.
func (d *DB) Backup(ctx context.Context, path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	_, err := d.wdb.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// Migrate brings the file up to SchemaVersion: a fresh file gets schema.sql,
// and the numbered steps above handle anything older.
func (d *DB) Migrate(ctx context.Context) error {
	// user_version 是 SQLite 自带的一个整数槽，正好用来记数据库版本。
	var v int
	if err := d.rdb.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v >= SchemaVersion {
		return nil
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 版本 0 表示空库：直接把 schema.sql 整份执行，一步到位。
		cur := v
		if cur == 0 {
			body, err := schemaFS.ReadFile("schema.sql")
			if err != nil {
				return err
			}
			// 整份 schema.sql 一次执行：里面全是 IF NOT EXISTS，重复跑也安全。
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			cur = SchemaVersion
		}
		// 然后按版本号逐级补：每一步只跑一次，因为 user_version 会跟着往前走。
		for ; cur < SchemaVersion; cur++ {
			for _, stmt := range upgrades[cur] {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
		}
		// 默认社群是旧数据与演示数据的落点。新建与升级都必须有它，
		// 否则历史会话会挂在一个不存在的社群上。
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO communities (id, name, description, created_at) VALUES (1, '默认社群', '', ?)", now); err != nil {
			return err
		}
		// 还没有社群归属的账号全部进默认社群；平台管理员在其中担任管理员。
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO community_members (community_id, user_id, role, joined_at) SELECT 1, id, CASE WHEN is_admin = 1 THEN 'admin' ELSE 'member' END, ? FROM users", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion))
		return err
	})
}
