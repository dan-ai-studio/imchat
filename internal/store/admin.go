package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrBadInput = errors.New("store: invalid input")

// AdminUser is an account as the platform-admin screens see it: identity, plus
// how many conversations it is in. Nothing conversation-specific.
type AdminUser struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	Status      string
	CreatedAt   int64
	Convs       int64
}

// ListUsers returns accounts with a per-user conversation count. The count is a
// correlated subquery on purpose: one round trip instead of N+1.
func (d *DB) ListUsers(ctx context.Context, limit int) ([]*AdminUser, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT u.id, u.username, u.display_name, u.is_admin, u.status, u.created_at, (SELECT COUNT(*) FROM members m WHERE m.user_id = u.id) FROM users u ORDER BY u.id LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 逐行扫；会话数是子查询算好的，这里只负责搬进结构体。
	var out []*AdminUser
	for rows.Next() {
		u := &AdminUser{}
		var admin int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt, &u.Convs); err != nil {
			return nil, err
		}
		// 整数换 bool 的地方，只此一处。
		u.IsAdmin = admin == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserStatus is the only platform-level mutation of an account. Banned users
// stop resolving through sessions, so existing cookies die on the next request.
func (d *DB) SetUserStatus(ctx context.Context, userID int64, status string) error {
	if status != "active" && status != "banned" {
		return ErrBadInput
	}
	// 状态与「删会话」写在同一个事务里：不会出现「已封禁但旧会话还能用」的空档。
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET status = ? WHERE id = ?", status, userID); err != nil {
			return err
		}
		// 封禁顺手删掉全部会话：这就是「下一次请求就是未授权」的实现方式。
		if status == "banned" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
				return err
			}
		}
		return nil
	})
}

// AdminConversation is a conversation as an operator sees it: size, sequence
// watermark, and when it was created.
type AdminConversation struct {
	ID        int64
	Kind      string
	Title     string
	Members   int64
	Messages  int64
	NextSeq   int64
	CreatedAt int64
}

// AdminConversations lists conversations newest-first with their member and
// message counts. Both counts are correlated subqueries.
func (d *DB) AdminConversations(ctx context.Context, limit int) ([]*AdminConversation, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.kind, c.title, (SELECT COUNT(*) FROM members m WHERE m.conversation_id = c.id), (SELECT COUNT(*) FROM messages x WHERE x.conversation_id = c.id), c.next_seq, c.created_at FROM conversations c ORDER BY c.id DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 成员数与消息数都是子查询算出来的，这里只收结果。
	var out []*AdminConversation
	for rows.Next() {
		c := &AdminConversation{}
		if err := rows.Scan(&c.ID, &c.Kind, &c.Title, &c.Members, &c.Messages, &c.NextSeq, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AdminMessage is one search hit: enough to read the message and know where it
// came from, without exposing the whole row.
type AdminMessage struct {
	ConversationID int64
	Seq            int64
	SenderID       int64
	Sender         string
	Body           string
	CreatedAt      int64
}

// SearchMessages is the LIKE fallback: no index, substring match. The indexed
// path lives in fts.go and is what the HTTP surface actually calls.
func (d *DB) SearchMessages(ctx context.Context, q string, limit int) ([]*AdminMessage, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.conversation_id, m.seq, m.sender_id, u.display_name, m.body, m.created_at FROM messages m JOIN users u ON u.id = m.sender_id WHERE m.body LIKE ? ORDER BY m.created_at DESC LIMIT ?",
		"%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 逐行扫成结构体；正文只读不回写，这是只读路径。
	var out []*AdminMessage
	for rows.Next() {
		m := &AdminMessage{}
		if err := rows.Scan(&m.ConversationID, &m.Seq, &m.SenderID, &m.Sender, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Counts returns the four row counts an operator checks first. Four separate
// queries on purpose: each one is exact and cheap on SQLite.
func (d *DB) Counts(ctx context.Context) (map[string]int64, error) {
	queries := map[string]string{
		"users":         "SELECT COUNT(*) FROM users",
		"conversations": "SELECT COUNT(*) FROM conversations",
		"messages":      "SELECT COUNT(*) FROM messages",
		"sessions":      "SELECT COUNT(*) FROM sessions",
	}
	// 一条一条查，结果按名字收进 map；名字就是这里的键，改键等于改接口。
	out := map[string]int64{}
	for name, q := range queries {
		var n int64
		if err := d.rdb.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, err
		}
		// 任一查询失败就整体失败：半个仪表盘比没有更误导人。
		out[name] = n
	}
	return out, nil
}
