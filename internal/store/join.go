package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// ErrInviteUnusable is returned when a code is unknown or already consumed.
// The two cases share one error on purpose: a caller must not be able to tell
// whether a code ever existed.
var ErrInviteUnusable = errors.New("store: invite code is not usable")

// ErrRequestAlreadyDecided is returned when someone tries to decide a request
// that has already been approved or rejected.
var ErrRequestAlreadyDecided = errors.New("store: join request already decided")

// JoinRequest is one pending request to join a community, joined with the
// applicant's profile so the approval screen needs no second query.
type JoinRequest struct {
	ID          int64
	CommunityID int64
	UserID      int64
	Username    string
	DisplayName string
	Status      string
	CreatedAt   int64
}

// NewInviteCode returns 8 random bytes as hex: short enough to paste, long
// enough that guessing is not a strategy.
func NewInviteCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CreateInvite mints a one-time code that lets exactly one person join.
func (d *DB) CreateInvite(ctx context.Context, communityID, createdBy int64) (string, error) {
	code := NewInviteCode()
	// 码在写事务外先生成：随机源与数据库无关，失败也不会留下半条记录。
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO community_invites (code, community_id, created_by, created_at) VALUES (?, ?, ?, ?)",
			code, communityID, createdBy, time.Now().UnixMilli())
		return err
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// JoinByInvite consumes one code and adds the caller to its community in the
// same transaction. Consuming and joining must not be separable: a crash
// between them would burn the code without granting membership.
func (d *DB) JoinByInvite(ctx context.Context, code string, userID int64) (int64, error) {
	var communityID int64
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		// 抢占式消费：只有 used_by 仍为 0 的行才会被更新，并发下只有一个人成功。
		res, err := tx.ExecContext(ctx,
			"UPDATE community_invites SET used_by = ?, used_at = ? WHERE code = ? AND used_by = 0",
			userID, now, code)
		if err != nil {
			return err
		}
		// 影响行数是「这个码还能不能用」的唯一判据。
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInviteUnusable
		}
		// 消费成功后才读社群 id，并顺手写入成员行。
		if err := tx.QueryRowContext(ctx,
			"SELECT community_id FROM community_invites WHERE code = ?", code).Scan(&communityID); err != nil {
			return err
		}
		// 入群与消费同事务：码被用掉就一定换来成员行，不会两头落空。
		_, err = tx.ExecContext(ctx,
			"INSERT INTO community_members (community_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?) ON CONFLICT(community_id, user_id) DO NOTHING",
			communityID, userID, now)
		return err
	})
	if err != nil {
		return 0, err
	}
	return communityID, nil
}

// RequestJoin records a pending request; a second request is a no-op thanks to
// the partial unique index on pending rows.
func (d *DB) RequestJoin(ctx context.Context, communityID, userID int64) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO community_join_requests (community_id, user_id, status, created_at) VALUES (?, ?, 'pending', ?)",
			communityID, userID, time.Now().UnixMilli())
		return err
	})
}

// JoinRequests lists the pending requests of one community, oldest first.
func (d *DB) JoinRequests(ctx context.Context, communityID int64) ([]*JoinRequest, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT r.id, r.community_id, r.user_id, u.username, u.display_name, r.status, r.created_at FROM community_join_requests r JOIN users u ON u.id = r.user_id WHERE r.community_id = ? AND r.status = 'pending' ORDER BY r.id",
		communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JoinRequest
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定。
	for rows.Next() {
		j := &JoinRequest{}
		if err := rows.Scan(&j.ID, &j.CommunityID, &j.UserID, &j.Username, &j.DisplayName, &j.Status, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DecideJoinRequest approves or rejects one pending request. Approval and the
// membership row are written together, so a request can never be approved
// without the person actually joining.
func (d *DB) DecideJoinRequest(ctx context.Context, requestID, decidedBy int64, approve bool) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var (
			communityID int64
			userID      int64
			status      string
		)
		// 先读出申请指向的社群与人：批准时要用它们写成员行。
		if err := tx.QueryRowContext(ctx,
			"SELECT community_id, user_id, status FROM community_join_requests WHERE id = ?", requestID).
			Scan(&communityID, &userID, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "pending" {
			return ErrRequestAlreadyDecided
		}
		// 只有 pending 能走到这里：已决的请求在上面就被挡掉了。
		next := "rejected"
		if approve {
			next = "approved"
		}
		// 先落决定，再（在批准时）落成员行：两步同一个事务。
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			"UPDATE community_join_requests SET status = ?, decided_at = ?, decided_by = ? WHERE id = ?",
			next, now, decidedBy, requestID); err != nil {
			return err
		}
		if !approve {
			return nil
		}
		// 批准与入群同事务：不能出现「申请批了但人没进来」。
		_, err := tx.ExecContext(ctx,
			"INSERT INTO community_members (community_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?) ON CONFLICT(community_id, user_id) DO NOTHING",
			communityID, userID, now)
		return err
	})
}
