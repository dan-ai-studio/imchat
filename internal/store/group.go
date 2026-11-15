package store

import (
	"context"
	"database/sql"
	"time"
)

// Member is one participant as the group screens need them: identity plus the
// role and the mute deadline that this conversation gives them.
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
	// 旧调用方没有社群概念：一律落进默认社群。
	return d.CreateGroupIn(ctx, DefaultCommunityID, ownerID, title, memberIDs)
}

// CreateGroupIn creates a group conversation inside one community, together
// with its membership. The community is fixed at creation and never changes.
func (d *DB) CreateGroupIn(ctx context.Context, communityID, ownerID int64, title string, memberIDs []int64) (*Conversation, error) {
	// 会话行与 owner 成员行必须同一个事务：没有群主的群不是有效状态。
	c := &Conversation{CommunityID: communityID, Kind: "group", Title: title, OwnerID: ownerID, CreatedAt: time.Now().UnixMilli()}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO conversations (community_id, kind, title, owner_id, next_seq, created_at) VALUES (?, 'group', ?, ?, 0, ?)",
			communityID, title, ownerID, c.CreatedAt)
		if err != nil {
			return err
		}
		// 拿到自增 id：后面写成员行要用它，所以顺序不能换。
		c.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		// 群主的成员行必须在同一个事务里落地，否则会出现「群没人管」的中间态。
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, 'owner', ?)",
			c.ID, ownerID, c.CreatedAt); err != nil {
			return err
		}
		// 其余成员一次写进去；重复邀请由 ON CONFLICT 吃掉。
		for _, id := range memberIDs {
			if id == ownerID {
				continue
			}
			// 幂等写入：同一个人被邀请两次只会有一行，角色保持第一次的。
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
	// 群与成员一起落地；任何一步失败，整个事务回滚。
	return c, nil
}

// MembersOf returns everyone in a conversation with their profile, role and mute
// state, ordered by when they joined.
func (d *DB) MembersOf(ctx context.Context, conversationID int64) ([]*Member, error) {
	// 连 users 表拿显示名：成员列表要给人看，光有 id 没用。

	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.user_id, u.username, u.display_name, m.role, m.muted_until, m.joined_at FROM members m JOIN users u ON u.id = m.user_id WHERE m.conversation_id = ? ORDER BY m.joined_at, m.user_id",
		conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 逐行扫成结构体；排序由 SQL 的 ORDER BY joined_at 决定。
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

// RemoveMember deletes one membership row. Nothing else changes: their messages
// stay, because history is not a membership record.
func (d *DB) RemoveMember(ctx context.Context, conversationID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM members WHERE conversation_id = ? AND user_id = ?", conversationID, userID)
		return err
	})
}

// SetMuted writes the instant a participant may speak again. Zero lifts the
// mute; there is no separate "unmute" state to keep in sync.
func (d *DB) SetMuted(ctx context.Context, conversationID, userID, until int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE members SET muted_until = ? WHERE conversation_id = ? AND user_id = ?",
			until, conversationID, userID)
		return err
	})
}

// MutedUntil reads that instant back. Callers compare it with the clock, which
// is why the send path can enforce a mute without a background sweeper.
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

// SetRole changes one participant's conversation role. The value is validated
// here as well as by the CHECK constraint, so a typo fails before the write.
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
