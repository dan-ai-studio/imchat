package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DefaultCommunityID is where pre-community data and demo data land. Migrate
// creates it and nothing deletes it.
const DefaultCommunityID int64 = 1

// ErrNotCommunityMember is returned when a caller asks for something inside a
// community they do not belong to. It is shaped like a missing row on purpose:
// a caller must not be able to tell "exists but forbidden" from "does not exist".
var ErrNotCommunityMember = errors.New("store: not a member of this community")

// Community is the isolation and governance boundary. Every conversation and
// every room belongs to exactly one community.
type Community struct {
	ID          int64
	Name        string
	Description string
	CreatedAt   int64
	Role        string // the asking user's role in this community
}

// CreateCommunity creates a community and makes its creator the owner in one
// transaction: a community without an owner is not a valid state.
func (d *DB) CreateCommunity(ctx context.Context, ownerID int64, name, description string) (*Community, error) {
	now := time.Now().UnixMilli()
	c := &Community{Name: name, Description: description, CreatedAt: now, Role: "owner"}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO communities (name, description, created_at) VALUES (?, ?, ?)",
			c.Name, c.Description, c.CreatedAt)
		if err != nil {
			return err
		}
		// id 回填给社群行，紧接着的成员行要用它。
		c.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		// 创建者同时是 owner：社群与第一个管理员必须同事务落地。
		_, err = tx.ExecContext(ctx,
			"INSERT INTO community_members (community_id, user_id, role, joined_at) VALUES (?, ?, 'owner', ?)",
			c.ID, ownerID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CommunitiesOf lists every community the user belongs to, oldest first, with
// the user's role in each.
func (d *DB) CommunitiesOf(ctx context.Context, userID int64) ([]*Community, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT c.id, c.name, c.description, c.created_at, m.role FROM communities c JOIN community_members m ON m.community_id = c.id WHERE m.user_id = ? ORDER BY c.id",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Community
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定，不在这里排。
	for rows.Next() {
		c := &Community{}
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CommunityByID fetches one community, or ErrNotFound.
func (d *DB) CommunityByID(ctx context.Context, id int64) (*Community, error) {
	c := &Community{}
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, name, description, created_at FROM communities WHERE id = ?", id).
		Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt)
	// 单行查询：没有就是 ErrNotFound，调用方据此回 404，不必自己判空。
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CommunityRole returns the user's role in one community, or
// ErrNotCommunityMember when they do not belong to it.
func (d *DB) CommunityRole(ctx context.Context, communityID, userID int64) (string, error) {
	var role string
	err := d.rdb.QueryRowContext(ctx,
		"SELECT role FROM community_members WHERE community_id = ? AND user_id = ?",
		communityID, userID).Scan(&role)
	// 不在社群里与社群不存在共用同一个错误形状，避免反推出存在性。
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotCommunityMember
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// ConversationInUserCommunity reports whether a conversation lives in a
// community the user belongs to. It is the existence gate for the API: when it
// is false the caller must answer "not found", so a response never reveals that
// an object exists in a community the caller cannot see.
func (d *DB) ConversationInUserCommunity(ctx context.Context, conversationID, userID int64) (bool, error) {
	var n int
	err := d.rdb.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM conversations c JOIN community_members m ON m.community_id = c.community_id WHERE c.id = ? AND m.user_id = ?",
		conversationID, userID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AddCommunityMember is idempotent: joining twice keeps the original role.
func (d *DB) AddCommunityMember(ctx context.Context, communityID, userID int64, role string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO community_members (community_id, user_id, role, joined_at) VALUES (?, ?, ?, ?) ON CONFLICT(community_id, user_id) DO NOTHING",
			communityID, userID, role, time.Now().UnixMilli())
		return err
	})
}
