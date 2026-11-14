package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (d *DB) CreateSession(ctx context.Context, token string, userID int64, now time.Time) error {
	ts := now.UnixMilli()
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO sessions (token, user_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
			token, userID, ts, ts)
		return err
	})
}

func (d *DB) SessionUser(ctx context.Context, token string, now time.Time, ttl time.Duration) (*User, error) {
	cutoff := now.Add(-ttl).UnixMilli()
	var (
		u     User
		admin int
	)
	err := d.rdb.QueryRowContext(ctx,
		"SELECT u.id, u.username, u.display_name, u.is_admin, u.status, u.created_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token = ? AND s.last_seen_at >= ?",
		token, cutoff).Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.Status != "active" {
		return nil, ErrNotFound
	}
	u.IsAdmin = admin == 1
	_ = d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token = ?", now.UnixMilli(), token)
		return e
	})
	return &u, nil
}

func (d *DB) DeleteSession(ctx context.Context, token string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token = ?", token)
		return err
	})
}
