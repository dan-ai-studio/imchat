package store

// ── 阅读导览 · 第 5 站 ────────────────────────────────────
// 上一站：站4 发送的前半
// 下一站：站6 投递器

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

// NewID returns 16 random bytes as hex. Message ids are opaque and unguessable
// on purpose: they are the handle other rows point at.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// SendParams is everything the write path needs: the message itself, who sent
// it, and the recipients whose delivery obligations must be created alongside it.
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
	// 先按调用方给的参数把消息对象拼出来；序号与 id 稍后才填，
	// 因为它们分别由数据库和随机源决定，不能由调用方指定。
	msg := &Message{
		ConversationID: p.ConversationID,
		SenderID:       p.SenderID,
		ClientMsgID:    p.ClientMsgID,
		Kind:           kind,
		Body:           body,
		Meta:           meta,
		RecallOf:       recallOf,
		// 时间戳在这里取、由插入语句写进去：同一个事务里所有行的「现在」是同一个现在。
		CreatedAt: time.Now().UnixMilli(),
	}

	// 取号与落库必须在同一个事务里。下面这一句就是全项目的第一性论点：
	// UPDATE 把 next_seq 加一，RETURNING 把它读回来——同一个事务，
	// 所以回滚不会留下序号空洞，并发也不会撞号。
	var seq int64
	if err := tx.QueryRowContext(ctx,
		"UPDATE conversations SET next_seq = next_seq + 1 WHERE id = ? RETURNING next_seq",
		p.ConversationID).Scan(&seq); err != nil {
		return nil, err
	}
	// 序号来自数据库、id 来自随机源，两者此刻才定下来。
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

	// 每个接收者一条待投递记录。它们与消息行同事务写入，所以不存在
	// 「消息在、义务没了」或「义务在、消息没了」这两种半截状态。
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

// SendMessage is the store's only write path for messages. If the caller
// supplied a client_msg_id that is already known, the existing message is
// returned instead of a second one being written.
func (d *DB) SendMessage(ctx context.Context, p SendParams) (*Message, error) {
	var out *Message
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 幂等的落点：先按 client_msg_id 查一次。查到就原样返回，绝不写第二条。
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
		// 先把被撤回的那条读出来：确认它存在、是你的、而且还没被撤回过。
		err := tx.QueryRowContext(ctx,
			"SELECT sender_id, kind FROM messages WHERE conversation_id = ? AND seq = ?",
			convID, targetSeq).Scan(&owner, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 三道拒绝，顺序有意义：不存在 → 不是你的 → 已经被撤回过。
		if owner != senderID {
			return ErrNotSender
		}
		// 撤回一条撤回消息没有意义，直接拒绝。
		if kind == "recall" {
			return ErrAlreadyRecalled
		}
		// 第二道防线：同一条消息只能被撤回一次。查库比维护内存状态可靠。
		var count int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND kind = 'recall' AND recall_of = ?",
			convID, targetSeq).Scan(&count); err != nil {
			return err
		}
		// 已经有墓碑了，就不再追加第二条。
		if count > 0 {
			return ErrAlreadyRecalled
		}
		// 墓碑就是一条普通消息：kind=recall、body 空、recall_of 指向被作废的序号。
		// 它因此复用同一套取号、投递与补齐 —— 撤回不需要第二条投递通道。
		m, err := d.appendInTx(ctx, tx, SendParams{
			ConversationID: convID,
			SenderID:       senderID,
			Recipients:     recipients,
		}, "recall", "", "{}", targetSeq)
		// 闭包只把 err 抛给 Write；结果通过外面那个变量交接。
		out = m
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// findByClientID is the dedupe lookup: one client_msg_id must resolve to the
// message that was already written, never to a second copy.
func findByClientID(ctx context.Context, tx *sql.Tx, clientMsgID string) (*Message, error) {
	var (
		m      Message
		client sql.NullString
	)
	// client_msg_id 在库里可能是 NULL（部分索引只约束非空值），
	// 所以读进 sql.NullString，再决定要不要赋回结构体。
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
	// 走只读连接：拉历史不该跟唯一的写者去抢那把写锁。
	//
	// 这就是补齐路径：客户端带着自己的最大序号回来，服务端从它的下一条开始给。
	// 缺口只能补、不能跳——跳过的那一段不会被任何机制重发，等于永久丢失。
	// 而「看起来像恰好一次」是三件事叠加出来的假象：服务端按 client_msg_id 幂等写入、
	// 客户端按 (会话, 序号) 去重、序号单调且只补不跳。缺任何一件，假象就破了。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT conversation_id, seq, id, client_msg_id, sender_id, kind, body, meta, recall_of, created_at FROM messages WHERE conversation_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?",
		conversationID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		// 每行扫回一个 Message；client_msg_id 可能为 NULL，先过一遍 NullString。
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

// nullString turns the empty string into SQL NULL, so the partial unique index
// on client_msg_id only ever covers real ids.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
