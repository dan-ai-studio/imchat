package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MaxSeats is the hard cap on speaking positions in one room.
const MaxSeats = 16

// ErrSeatsFull is returned when a seventeenth person tries to take the mic.
var ErrSeatsFull = errors.New("store: all seats are taken")

// ErrNotOnMic is returned when an operation expects a person to be on the mic
// and they are not.
var ErrNotOnMic = errors.New("store: user is not on the mic")

// Seat is one row of the seat table, joined with the profile so the room screen
// needs no second query.
type Seat struct {
	UserID      int64
	Username    string
	DisplayName string
	Position    int
	State       string
}

// RequestSeat records a raised hand. It does not put anyone on the mic: a room
// owner approves it first.
func (d *DB) RequestSeat(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO room_seats (conversation_id, user_id, position, state, updated_at) VALUES (?, ?, 0, 'requested', ?) ON CONFLICT(conversation_id, user_id) DO UPDATE SET state = 'requested', updated_at = excluded.updated_at",
			roomID, userID, time.Now().UnixMilli())
		return err
	})
}

// InviteSeat puts an invitation on the table. The invitee still has to accept.
func (d *DB) InviteSeat(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO room_seats (conversation_id, user_id, position, state, updated_at) VALUES (?, ?, 0, 'invited', ?) ON CONFLICT(conversation_id, user_id) DO UPDATE SET state = 'invited', updated_at = excluded.updated_at",
			roomID, userID, time.Now().UnixMilli())
		return err
	})
}

// TakeSeat promotes a requested or invited person onto the mic. The seat count
// is checked inside the same transaction that assigns the position, so two
// people cannot grab the last seat at once.
func (d *DB) TakeSeat(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 先数满员，再找空位：两步在同一个事务里，最后一把椅子不会被两个人同时抢到。
		var taken int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM room_seats WHERE conversation_id = ? AND state = 'on_mic'", roomID).Scan(&taken); err != nil {
			return err
		}
		if taken >= MaxSeats {
			return ErrSeatsFull
		}
		// 位置取第一个空位：麦位是编号的，前端靠它画格子。
		var pos int
		for p := 0; p < MaxSeats; p++ {
			var n int
			if err := tx.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM room_seats WHERE conversation_id = ? AND position = ? AND state = 'on_mic'", roomID, p).Scan(&n); err != nil {
				return err
			}
			// 第一个没被占用的编号就是新位置。
			if n == 0 {
				pos = p
				break
			}
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO room_seats (conversation_id, user_id, position, state, updated_at) VALUES (?, ?, ?, 'on_mic', ?) ON CONFLICT(conversation_id, user_id) DO UPDATE SET position = excluded.position, state = 'on_mic', updated_at = excluded.updated_at",
			roomID, userID, pos, time.Now().UnixMilli())
		return err
	})
}

// LeaveSeat removes one person from the seat table entirely. Leaving the mic
// and being taken off it are the same state change.
func (d *DB) LeaveSeat(ctx context.Context, roomID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM room_seats WHERE conversation_id = ? AND user_id = ?", roomID, userID)
		return err
	})
}

// SeatsOf lists everyone who has a seat row, on mic first.
func (d *DB) SeatsOf(ctx context.Context, roomID int64) ([]*Seat, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT s.user_id, u.username, u.display_name, s.position, s.state FROM room_seats s JOIN users u ON u.id = s.user_id WHERE s.conversation_id = ? ORDER BY CASE s.state WHEN 'on_mic' THEN 0 WHEN 'invited' THEN 1 ELSE 2 END, s.position, s.user_id",
		roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Seat
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定。
	for rows.Next() {
		s := &Seat{}
		if err := rows.Scan(&s.UserID, &s.Username, &s.DisplayName, &s.Position, &s.State); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
