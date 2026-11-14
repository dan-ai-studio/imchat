package store

import (
	"context"
	"database/sql"
	"time"
)

type Member struct {
	UserID      int64
	Username    string
	DisplayName string
	Role        string
	MutedUntil  int64
	JoinedAt    int64
}

// CreateGroup creates a group conversation and its membership in one
// transaction: a conversation without an owner is not a valid state.
func (d *DB) CreateGroup(ctx context.Context, ownerID int64, title string, memberIDs []int64) (*Conversation, error) {
	c := &Conversation{Kind: "group", Title: title, OwnerID: ownerID, CreatedAt: time.Now().UnixMilli()}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO conversations (kind, title, owner_id, next_seq, created_at) VALUES ('group', ?, ?, 0, ?)",
			title, ownerID, c.CreatedAt)
		if err != nil {
			return err
		}
		c.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, 'owner', ?)",
			c.ID, ownerID, c.CreatedAt); err != nil {
			return err
		}
		for _, id := range memberIDs {
			if id == ownerID {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?) ON CONFLICT(conversation_id, user_id) DO NOTHING",
				c.ID, id, c.CreatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (d *DB) MembersOf(ctx context.Context, conversationID int64) ([]*Member, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.user_id, u.username, u.display_name, m.role, m.muted_until, m.joined_at FROM members m JOIN users u ON u.id = m.user_id WHERE m.conversation_id = ? ORDER BY m.joined_at, m.user_id",
		conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Member
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Role, &m.MutedUntil, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) RemoveMember(ctx context.Context, conversationID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM members WHERE conversation_id = ? AND user_id = ?", conversationID, userID)
		return err
	})
}

func (d *DB) SetMuted(ctx context.Context, conversationID, userID, until int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE members SET muted_until = ? WHERE conversation_id = ? AND user_id = ?",
			until, conversationID, userID)
		return err
	})
}

func (d *DB) MutedUntil(ctx context.Context, conversationID, userID int64) (int64, error) {
	var until int64
	err := d.rdb.QueryRowContext(ctx,
		"SELECT muted_until FROM members WHERE conversation_id = ? AND user_id = ?",
		conversationID, userID).Scan(&until)
	if err != nil {
		return 0, err
	}
	return until, nil
}

func (d *DB) SetRole(ctx context.Context, conversationID, userID int64, role string) error {
	if role != "owner" && role != "admin" && role != "member" {
		return ErrBadInput
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE members SET role = ? WHERE conversation_id = ? AND user_id = ?",
			role, conversationID, userID)
		return err
	})
}
