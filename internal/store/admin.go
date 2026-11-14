package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrBadInput = errors.New("store: invalid input")

type AdminUser struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	Status      string
	CreatedAt   int64
	Convs       int64
}

func (d *DB) ListUsers(ctx context.Context, limit int) ([]*AdminUser, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT u.id, u.username, u.display_name, u.is_admin, u.status, u.created_at, (SELECT COUNT(*) FROM members m WHERE m.user_id = u.id) FROM users u ORDER BY u.id LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AdminUser
	for rows.Next() {
		u := &AdminUser{}
		var admin int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt, &u.Convs); err != nil {
			return nil, err
		}
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
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET status = ? WHERE id = ?", status, userID); err != nil {
			return err
		}
		if status == "banned" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
				return err
			}
		}
		return nil
	})
}

type AdminConversation struct {
	ID        int64
	Kind      string
	Title     string
	Members   int64
	Messages  int64
	NextSeq   int64
	CreatedAt int64
}

func (d *DB) AdminConversations(ctx context.Context, limit int) ([]*AdminConversation, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.kind, c.title, (SELECT COUNT(*) FROM members m WHERE m.conversation_id = c.id), (SELECT COUNT(*) FROM messages x WHERE x.conversation_id = c.id), c.next_seq, c.created_at FROM conversations c ORDER BY c.id DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

type AdminMessage struct {
	ConversationID int64
	Seq            int64
	SenderID       int64
	Sender         string
	Body           string
	CreatedAt      int64
}

func (d *DB) SearchMessages(ctx context.Context, q string, limit int) ([]*AdminMessage, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.conversation_id, m.seq, m.sender_id, u.display_name, m.body, m.created_at FROM messages m JOIN users u ON u.id = m.sender_id WHERE m.body LIKE ? ORDER BY m.created_at DESC LIMIT ?",
		"%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func (d *DB) Counts(ctx context.Context) (map[string]int64, error) {
	queries := map[string]string{
		"users":         "SELECT COUNT(*) FROM users",
		"conversations": "SELECT COUNT(*) FROM conversations",
		"messages":      "SELECT COUNT(*) FROM messages",
		"sessions":      "SELECT COUNT(*) FROM sessions",
	}
	out := map[string]int64{}
	for name, q := range queries {
		var n int64
		if err := d.rdb.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, nil
}
