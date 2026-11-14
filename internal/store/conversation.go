package store

import (
	"context"
	"database/sql"
	"errors"
)

func (d *DB) ConversationsOf(ctx context.Context, userID int64) ([]*Conversation, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.kind, c.title, c.owner_id, c.next_seq, c.created_at FROM conversations c JOIN members m ON m.conversation_id = c.id WHERE m.user_id = ? ORDER BY c.id",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conversation
	for rows.Next() {
		c := &Conversation{}
		if err := rows.Scan(&c.ID, &c.Kind, &c.Title, &c.OwnerID, &c.NextSeq, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) ConversationByID(ctx context.Context, id int64) (*Conversation, error) {
	c := &Conversation{}
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, kind, title, owner_id, next_seq, created_at FROM conversations WHERE id = ?", id).
		Scan(&c.ID, &c.Kind, &c.Title, &c.OwnerID, &c.NextSeq, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (d *DB) SetReadCursor(ctx context.Context, conversationID, userID int64, deviceID string, seq int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO read_cursors (conversation_id, user_id, device_id, seq, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(conversation_id, user_id, device_id) DO UPDATE SET seq = MAX(seq, excluded.seq), updated_at = excluded.updated_at",
			conversationID, userID, deviceID, seq, nowMillis())
		return err
	})
}
