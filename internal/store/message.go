package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrNotSender       = errors.New("store: not the sender of this message")
	ErrAlreadyRecalled = errors.New("store: message already recalled")
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

// appendInTx is the single place a message row and its delivery obligations are
// created. Sequence allocation, the message insert and the outbox rows all live
// inside the caller's transaction, so a rollback leaves no hole in the sequence.
func (d *DB) appendInTx(ctx context.Context, tx *sql.Tx, p SendParams, kind, body, meta string, recallOf int64) (*Message, error) {
	msg := &Message{
		ConversationID: p.ConversationID,
		SenderID:       p.SenderID,
		ClientMsgID:    p.ClientMsgID,
		Kind:           kind,
		Body:           body,
		Meta:           meta,
		RecallOf:       recallOf,
		CreatedAt:      time.Now().UnixMilli(),
	}

	var seq int64
	if err := tx.QueryRowContext(ctx,
		"UPDATE conversations SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq",
		p.ConversationID).Scan(&seq); err != nil {
		return nil, err
	}
	msg.Seq = seq
	msg.ID = NewID()

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO messages (conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, recall_of, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		msg.ConversationID, msg.Seq, msg.ID, nullString(msg.ClientMsgID),
		msg.SenderID, msg.Kind, msg.Body, msg.Meta, msg.RecallOf, msg.CreatedAt); err != nil {
		return nil, err
	}

	// Only human-readable bodies are searchable; an image message carries a
	// file name in body and must not pollute the text index.
	if kind == "text" {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO messages_fts (body, conv, seq) VALUES (?, ?, ?)",
			body, p.ConversationID, msg.Seq); err != nil {
			return nil, err
		}
	}

	now := time.Now().UnixMilli()
	for _, r := range p.Recipients {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO outbox (message_id, conversation_id, seq, recipient_id, state, next_at, created_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)",
			msg.ID, msg.ConversationID, msg.Seq, r, now, now); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

func (d *DB) SendMessage(ctx context.Context, p SendParams) (*Message, error) {
	var out *Message
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if p.ClientMsgID != "" {
			existing, err := findByClientID(ctx, tx, p.ClientMsgID)
			switch {
			case err == nil:
				out = existing
				return nil
			case errors.Is(err, sql.ErrNoRows):
				// fall through and create it
			default:
				return err
			}
		}
		m, err := d.appendInTx(ctx, tx, p, p.Kind, p.Body, p.Meta, 0)
		out = m
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecallMessage appends a tombstone rather than mutating the target row, so the
// messages table stays append-only (see ADR-0005).
func (d *DB) RecallMessage(ctx context.Context, convID, senderID, targetSeq int64, recipients []int64) (*Message, error) {
	var out *Message
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var (
			owner int64
			kind  string
		)
		err := tx.QueryRowContext(ctx,
			"SELECT sender_id, kind FROM messages WHERE conversation_id = ? AND seq = ?",
			convID, targetSeq).Scan(&owner, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if owner != senderID {
			return ErrNotSender
		}
		if kind == "recall" {
			return ErrAlreadyRecalled
		}
		var count int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND kind = 'recall' AND recall_of = ?",
			convID, targetSeq).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrAlreadyRecalled
		}
		m, err := d.appendInTx(ctx, tx, SendParams{
			ConversationID: convID,
			SenderID:       senderID,
			Recipients:     recipients,
		}, "recall", "", "{}", targetSeq)
		out = m
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func findByClientID(ctx context.Context, tx *sql.Tx, clientMsgID string) (*Message, error) {
	var (
		m      Message
		client sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		"SELECT conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, recall_of, created_at FROM messages WHERE client_msg_id = ?",
		clientMsgID).Scan(&m.ConversationID, &m.Seq, &m.ID, &client, &m.SenderID,
		&m.Kind, &m.Body, &m.Meta, &m.RecallOf, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	m.ClientMsgID = client.String
	return &m, nil
}

// MessagesAfter returns messages with seq > afterSeq, oldest first.
func (d *DB) MessagesAfter(ctx context.Context, conversationID, afterSeq int64, limit int) ([]*Message, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, recall_of, created_at FROM messages WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?",
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
			&m.Kind, &m.Body, &m.Meta, &m.RecallOf, &m.CreatedAt); err != nil {
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
