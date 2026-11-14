package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("store: not found")

func (d *DB) CreateUser(ctx context.Context, username, displayName, passwordHash string, isAdmin bool) (*User, error) {
	admin := 0
	if isAdmin {
		admin = 1
	}
	u := &User{
		Username:    username,
		DisplayName: displayName,
		IsAdmin:     isAdmin,
		Status:      "active",
		CreatedAt:   time.Now().UnixMilli(),
	}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO users (username, display_name, password_hash, is_admin, status, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			u.Username, u.DisplayName, passwordHash, admin, u.Status, u.CreatedAt)
		if err != nil {
			return err
		}
		u.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *DB) UserByUsername(ctx context.Context, username string) (*User, string, error) {
	var (
		u     User
		hash  string
		admin int
	)
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, username, display_name, password_hash, is_admin, status, created_at FROM users WHERE username = ?",
		username).Scan(&u.ID, &u.Username, &u.DisplayName, &hash, &admin, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	u.IsAdmin = admin == 1
	return &u, hash, nil
}

func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	var (
		u     User
		admin int
	)
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, username, display_name, is_admin, status, created_at FROM users WHERE id = ?",
		id).Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = admin == 1
	return &u, nil
}

func (d *DB) CreateConversation(ctx context.Context, kind, title string, ownerID int64) (*Conversation, error) {
	c := &Conversation{Kind: kind, Title: title, OwnerID: ownerID, CreatedAt: time.Now().UnixMilli()}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO conversations (kind, title, owner_id, next_seq, created_at) VALUES (?, ?, ?, 0, ?)",
			c.Kind, c.Title, c.OwnerID, c.CreatedAt)
		if err != nil {
			return err
		}
		c.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// AddMember is idempotent: joining twice keeps the original role.
func (d *DB) AddMember(ctx context.Context, conversationID, userID int64, role string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO members (conversation_id, user_id, role, joined_at) VALUES (?, ?, ?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING",
			conversationID, userID, role, time.Now().UnixMilli())
		return err
	})
}

func (d *DB) Members(ctx context.Context, conversationID int64) ([]int64, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id FROM members WHERE conversation_id = ? ORDER BY user_id", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) RoleOf(ctx context.Context, conversationID, userID int64) (string, error) {
	var role string
	err := d.rdb.QueryRowContext(ctx,
		"SELECT role FROM members WHERE conversation_id = ? AND user_id = ?",
		conversationID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return role, nil
}
