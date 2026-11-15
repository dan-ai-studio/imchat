package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateSession inserts one session row. The caller supplies the token and the
// clock, which is what lets tests pin both.
func (d *DB) CreateSession(ctx context.Context, token string, userID int64, now time.Time) error {
	ts := now.UnixMilli()
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO sessions (token, user_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
			token, userID, ts, ts)
		return err
	})
}

// SessionUser resolves a token to its user, enforcing both the idle timeout and
// the account status. A disabled account resolves to ErrNotFound, not to an
// error the caller might special-case.
func (d *DB) SessionUser(ctx context.Context, token string, now time.Time, ttl time.Duration) (*User, error) {
	// 过期的会话直接当作不存在：查询里就带上时间条件，不需要定时清理任务。
	cutoff := now.Add(-ttl).UnixMilli()
	var (
		u     User
		admin int
	)
	// 一次查询同时拿到身份与会话有效性：时间条件写在 WHERE 里，过期的直接查不到。
	err := d.rdb.QueryRowContext(ctx,
		"SELECT u.id, u.username, u.display_name, u.is_admin, u.status, u.created_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token = ? AND s.last_seen_at >= ?",
		token, cutoff).Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt)
	// 没有这行、或时间条件没过，都归为「查不到」：调用方只该知道这一种结果。
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.Status != "active" {
		return nil, ErrNotFound
	}
	// SQLite 没有布尔类型，读出来是整数，在这里改回 bool。
	u.IsAdmin = admin == 1
	_ = d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token = ?", now.UnixMilli(), token)
		return e
	})
	return &u, nil
}

// DeleteSession removes one session row. Deleting is the revocation mechanism:
// the next request with that token resolves to nobody.
func (d *DB) DeleteSession(ctx context.Context, token string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token = ?", token)
		return err
	})
}
