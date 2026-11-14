package store

import (
	"context"
	"database/sql"
)

type DueEntry struct {
	OutboxID       int64
	ConversationID int64
	Seq            int64
	RecipientID    int64
	MessageID      string
	SenderID       int64
	Kind           string
	Body           string
	RecallOf       int64
	CreatedAt      int64
}

func (d *DB) DueOutbox(ctx context.Context, now int64, limit int) ([]*DueEntry, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT o.id, o.conversation_id, o.seq, o.recipient_id, o.message_id, m.sender_id, m.kind, m.body, m.recall_of, m.created_at FROM outbox o JOIN messages m ON m.conversation_id = o.conversation_id AND m.seq = o.seq WHERE o.state = 'pending' AND o.next_at <= ? ORDER BY o.id LIMIT ?",
		now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DueEntry
	for rows.Next() {
		e := &DueEntry{}
		if err := rows.Scan(&e.OutboxID, &e.ConversationID, &e.Seq, &e.RecipientID,
			&e.MessageID, &e.SenderID, &e.Kind, &e.Body, &e.RecallOf, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) MarkOutboxInflight(ctx context.Context, ids []int64, nextAt int64) error {
	if len(ids) == 0 {
		return nil
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				"UPDATE outbox SET state = 'inflight', attempts = attempts + 1, next_at = ? WHERE id = ?",
				nextAt, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AckOutbox resolves the delivery obligation. This is the only place a pending
// delivery becomes done, which is why the ack frame matters.
func (d *DB) AckOutbox(ctx context.Context, conversationID, seq, recipientID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE outbox SET state = 'done' WHERE conversation_id = ? AND seq = ? AND recipient_id = ? AND state <> 'done'",
			conversationID, seq, recipientID)
		return err
	})
}

// RepumpStale turns deliveries that were handed to the bus but never acked back
// into pending. This is what makes the guarantee at-least-once rather than
// at-most-once.
func (d *DB) RepumpStale(ctx context.Context, now int64) (int64, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE outbox SET state = 'pending' WHERE state = 'inflight' AND next_at <= ?", now)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

func (d *DB) OutboxStats(ctx context.Context) (map[string]int64, error) {
	rows, err := d.rdb.QueryContext(ctx, "SELECT state, COUNT(*) FROM outbox GROUP BY state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			s string
			c int64
		)
		if err := rows.Scan(&s, &c); err != nil {
			return nil, err
		}
		out[s] = c
	}
	return out, rows.Err()
}
