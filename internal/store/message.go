package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type SendParams struct {
	ConversationID int64
	SenderID       int64
	ClientMsgID    string
	Kind           string
	Body           string
	Meta           string
	Recipients     []int64
}

// SendMessage allocates the sequence, inserts the message and fans the outbox
// entries out in one transaction. Either all three happen or none do, which is
// why a rolled-back send leaves no hole in the conversation sequence.
func (d *DB) SendMessage(ctx context.Context, p SendParams) (*Message, error) {
	msg := &Message{
		ConversationID: p.ConversationID,
		SenderID:       p.SenderID,
		ClientMsgID:    p.ClientMsgID,
		Kind:           p.Kind,
		Body:           p.Body,
		Meta:           p.Meta,
		CreatedAt:      time.Now().UnixMilli(),
	}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if p.ClientMsgID != "" {
			var (
				existing Message
				client   sql.NullString
			)
			err := tx.QueryRowContext(ctx,
				"SELECT conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, created_at FROM messages WHERE client_msg_id = ?",
				p.ClientMsgID).Scan(&existing.ConversationID, &existing.Seq, &existing.ID, &client,
				&existing.SenderID, &existing.Kind, &existing.Body, &existing.Meta, &existing.CreatedAt)
			switch {
			case err == nil:
				existing.ClientMsgID = client.String
				*msg = existing
				return nil
			case errors.Is(err, sql.ErrNoRows):
				// fall through and create it
			default:
				return err
			}
		}

		var seq int64
		if err := tx.QueryRowContext(ctx,
			"UPDATE conversations SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq",
			p.ConversationID).Scan(&seq); err != nil {
			return err
		}
		msg.Seq = seq
		msg.ID = NewID()

		if _, err := tx.ExecContext(ctx,
			"INSERT INTO messages (conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			msg.ConversationID, msg.Seq, msg.ID, nullString(msg.ClientMsgID),
			msg.SenderID, msg.Kind, msg.Body, msg.Meta, msg.CreatedAt); err != nil {
			return err
		}

		now := time.Now().UnixMilli()
		for _, r := range p.Recipients {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO outbox (message_id, conversation_id, seq, recipient_id, state, next_at, created_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)",
				msg.ID, msg.ConversationID, msg.Seq, r, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// MessagesAfter returns messages with seq > afterSeq, oldest first.
func (d *DB) MessagesAfter(ctx context.Context, conversationID, afterSeq int64, limit int) ([]*Message, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, created_at FROM messages WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?",
		conversationID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m := &Message{}
		var client sql.NullString
		if err := rows.Scan(&m.ConversationID, &m.Seq, &m.ID, &client, &m.SenderID,
			&m.Kind, &m.Body, &m.Meta, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ClientMsgID = client.String
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
