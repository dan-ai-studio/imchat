package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateRoomIn creates a room inside one community. A room is a conversation
// with is_room set: it reuses the message table and the sequence counter, but
// its messages are broadcast instead of being delivered per recipient.
func (d *DB) CreateRoomIn(ctx context.Context, communityID, ownerID int64, title string) (*Conversation, error) {
	now := time.Now().UnixMilli()
	c := &Conversation{CommunityID: communityID, IsRoom: true, Kind: "room", Title: title, OwnerID: ownerID, CreatedAt: now}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO conversations (community_id, kind, is_room, title, owner_id, next_seq, created_at) VALUES (?, 'group', 1, ?, ?, 0, ?)",
			communityID, title, ownerID, now)
		if err != nil {
			return err
		}
		// 房主自己也要进 members：发消息、看历史都走同一套会话成员校验。
		c.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		// 房主行与房间行同事务：开出来的房间一定有人管。
		_, err = tx.ExecContext(ctx,
			"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, 'owner', ?)",
			c.ID, ownerID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// EnterRoom marks one user present in a room and makes them a member of the
// underlying conversation, so the normal send and history checks keep working.
func (d *DB) EnterRoom(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		// 在场是瞬时事实：重复进场只刷新时间，不报错。
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO room_presence (conversation_id, user_id, joined_at, last_seen_at) VALUES (?, ?, ?, ?) ON CONFLICT(conversation_id, user_id) DO UPDATE SET last_seen_at = excluded.last_seen_at",
			roomID, userID, now, now); err != nil {
			return err
		}
		// 进房即成为会话成员：这样发送与历史接口不需要第二套权限判断。
		_, err := tx.ExecContext(ctx,
			"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?) ON CONFLICT(conversation_id, user_id) DO NOTHING",
			roomID, userID, now)
		return err
	})
}

// LeaveRoom removes the presence row. Membership stays: leaving a room is not
// the same as being expelled from the conversation behind it.
func (d *DB) LeaveRoom(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM room_presence WHERE conversation_id = ? AND user_id = ?", roomID, userID)
		return err
	})
}

// RoomOccupants returns the ids of everyone currently present in a room. It is
// the recipient list for a broadcast: no presence row, no push.
func (d *DB) RoomOccupants(ctx context.Context, roomID int64) ([]int64, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id FROM room_presence WHERE conversation_id = ? ORDER BY user_id", roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	// 结果很小，直接收成 id 切片；调用方只关心「发给谁」。
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CloseRoom ends a room: it stops appearing in the list and nobody can enter.
// Messages are kept — closing is not deleting.
func (d *DB) CloseRoom(ctx context.Context, roomID, ownerID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 只有房主能关：关房是房间级动作，不是社群级治理。
		var owner int64
		var isRoom int64
		if err := tx.QueryRowContext(ctx,
			"SELECT owner_id, is_room FROM conversations WHERE id = ?", roomID).Scan(&owner, &isRoom); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 不是房间、或者不是房主，都回同一个错误：关房不解释「为什么不行」。
		if isRoom != 1 || owner != ownerID {
			return ErrNotRoom
		}
		// 关房同时清掉在场：房间结束，没有「还在里面的人」。
		if _, err := tx.ExecContext(ctx,
			"UPDATE conversations SET closed_at = ? WHERE id = ?", time.Now().UnixMilli(), roomID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM room_presence WHERE conversation_id = ?", roomID)
		return err
	})
}

// RoomGate reports whether a user may enter a room. The gate is a level
// threshold, nothing else: it never grants or removes any管理权.
func (d *DB) RoomGate(ctx context.Context, roomID, userID int64) (int, error) {
	var minLevel int
	if err := d.rdb.QueryRowContext(ctx,
		"SELECT min_level FROM conversations WHERE id = ? AND is_room = 1", roomID).Scan(&minLevel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return minLevel, nil
}

// SetRoomMinLevel changes the entry threshold of one room.
func (d *DB) SetRoomMinLevel(ctx context.Context, roomID, ownerID int64, minLevel int) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 只有房主能改门槛：这是房间级动作，不是社群级治理。
		res, err := tx.ExecContext(ctx,
			"UPDATE conversations SET min_level = ? WHERE id = ? AND owner_id = ? AND is_room = 1", minLevel, roomID, ownerID)
		if err != nil {
			return err
		}
		// 影响行数为 0 有两种可能：不是房间，或者不是房主。两者回同一个错误。
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotRoom
		}
		return nil
	})
}

// RoomsOf lists the open rooms of one community, newest first.
func (d *DB) RoomsOf(ctx context.Context, communityID int64) ([]*Conversation, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT id, community_id, is_room, kind, title, owner_id, next_seq, created_at FROM conversations WHERE community_id = ? AND is_room = 1 AND closed_at = 0 ORDER BY id DESC",
		communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Conversation
	// 逐行扫成结构体；房间的 kind 在这里被翻成 room。
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrNotRoom is returned when a room-only operation targets a normal
// conversation.
var ErrNotRoom = errors.New("store: conversation is not a room")
