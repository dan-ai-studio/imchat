package store

import (
	"context"
	"database/sql"
	"errors"
)

// ConversationsOf lists the conversations one user belongs to, oldest id first.
func (d *DB) ConversationsOf(ctx context.Context, userID int64) ([]*Conversation, error) {
	// 用 members 做连接条件，比先查会话再过滤少一次往返，也顺带保证了「只返回你所在的」。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.community_id, c.is_room, c.kind, c.title, c.owner_id, c.next_seq, c.created_at FROM conversations c JOIN members m ON m.conversation_id = c.id WHERE m.user_id = ? ORDER BY c.id",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conversation
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY c.id 决定，不在这里排。
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationsOfIn lists one user's conversations inside one community. The
// community filter is part of the join, not a post-filter: a conversation from
// another community never enters the result set at all.
func (d *DB) ConversationsOfIn(ctx context.Context, communityID, userID int64) ([]*Conversation, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.community_id, c.is_room, c.kind, c.title, c.owner_id, c.next_seq, c.created_at FROM conversations c JOIN members m ON m.conversation_id = c.id WHERE c.community_id = ? AND m.user_id = ? ORDER BY c.id",
		communityID, userID)
	// 社群写在 WHERE 里：别的社群的会话不会进入结果集，不靠事后剔。
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conversation
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定，不在这里排。
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanConversation turns one row into a Conversation. The room flag is folded
// into Kind on the way out, so callers only ever compare one field.
func scanConversation(rows *sql.Rows) (*Conversation, error) {
	c := &Conversation{}
	var isRoom int64
	if err := rows.Scan(&c.ID, &c.CommunityID, &isRoom, &c.Kind, &c.Title, &c.OwnerID, &c.NextSeq, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.IsRoom = isRoom == 1
	// 对外只有一种区分方式：kind。房间在库里是 group + is_room，读出来翻成 room。
	if c.IsRoom {
		c.Kind = "room"
	}
	return c, nil
}

// ConversationByID fetches one conversation, or ErrNotFound. It performs no
// membership check: that belongs to the layer that knows who is asking.
func (d *DB) ConversationByID(ctx context.Context, id int64) (*Conversation, error) {
	c := &Conversation{}
	var isRoom int64
	// 单行查询：没有就是 ErrNotFound，调用方据此回 404，不必自己判空。
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, community_id, is_room, kind, title, owner_id, next_seq, created_at FROM conversations WHERE id = ?", id).
		Scan(&c.ID, &c.CommunityID, &isRoom, &c.Kind, &c.Title, &c.OwnerID, &c.NextSeq, &c.CreatedAt)
	// 单行查询：没有就是 ErrNotFound，调用方据此回 404，不必自己判空。
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.IsRoom = isRoom == 1
	if c.IsRoom {
		c.Kind = "room"
	}
	return c, nil
}

// SetReadCursor advances one device's read position. It never moves backwards:
// MAX keeps an out-of-order update from un-reading what was already read.
func (d *DB) SetReadCursor(ctx context.Context, conversationID, userID int64, deviceID string, seq int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 冲突时取 MAX：已读只能前进，不能因为一条乱序的旧请求而回退。
		_, err := tx.ExecContext(ctx,
			"INSERT INTO read_cursors (conversation_id, user_id, device_id, seq, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(conversation_id, user_id, device_id) DO UPDATE SET seq = MAX(seq, excluded.seq), updated_at = excluded.updated_at",
			conversationID, userID, deviceID, seq, nowMillis())
		return err
	})
}
