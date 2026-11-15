package store

import (
	"context"
	"database/sql"
)

// DueEntry is one delivery that is ready to go out, joined with the message it
// carries so the dispatcher never has to make a second query.
type DueEntry struct {
	OutboxID       int64
	ConversationID int64
	Seq            int64
	RecipientID    int64
	MessageID      string
	SenderID       int64
	Kind           string
	Body           string
	Meta           string
	RecallOf       int64
	CreatedAt      int64
}

// DueOutbox returns pending obligations whose next attempt is due, oldest first.
// The limit is the dispatcher's back-pressure valve, not a throughput promise.
func (d *DB) DueOutbox(ctx context.Context, now int64, limit int) ([]*DueEntry, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT o.id, o.conversation_id, o.seq, o.recipient_id, o.message_id, m.sender_id, m.kind, m.body, m.meta, m.recall_of, m.created_at FROM outbox o JOIN messages m ON m.conversation_id = o.conversation_id AND m.seq = o.seq WHERE o.state = 'pending' AND o.next_at <= ? ORDER BY o.id LIMIT ?",
		now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 一次 join 把消息内容也带回来：每次投递只查一次库，不回头补。
	var out []*DueEntry
	for rows.Next() {
		// 字段顺序与 SELECT 一一对应，改任意一边都要同步另一边。
		e := &DueEntry{}
		if err := rows.Scan(&e.OutboxID, &e.ConversationID, &e.Seq, &e.RecipientID,
			&e.MessageID, &e.SenderID, &e.Kind, &e.Body, &e.Meta, &e.RecallOf, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkOutboxInflight stamps the deliveries that were just handed to the bus, so
// the next tick will not pick them up again while clients are acking.
func (d *DB) MarkOutboxInflight(ctx context.Context, ids []int64, nextAt int64) error {
	// 空批次直接返回：一次空写事务不值得开。
	if len(ids) == 0 {
		return nil
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// attempts 在这里自增：它是重试计数，也是排障时唯一的线索。
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
	// 把「发出去了但没等到确认」的打回待投——这一句就是至少一次的最后一道保险。
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE outbox SET state = 'pending' WHERE state = 'inflight' AND next_at <= ?", now)
		if err != nil {
			return err
		}
		// 把影响行数带出去：调用方据此判断这一轮救回了多少条。
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// OutboxStats counts obligations per state: pending, inflight, done. It is the
// one number an operator should watch.
func (d *DB) OutboxStats(ctx context.Context) (map[string]int64, error) {
	rows, err := d.rdb.QueryContext(ctx, "SELECT state, COUNT(*) FROM outbox GROUP BY state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 按状态分组聚合：pending 涨起来就是有人在丢消息。
	out := map[string]int64{}
	for rows.Next() {
		var (
			s string
			c int64
		)
		if err := rows.Scan(&s, &c); err != nil {
			return nil, err
		}
		// 状态名直接当键，别处不再维护一份枚举。
		out[s] = c
	}
	return out, rows.Err()
}
