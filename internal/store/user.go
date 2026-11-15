package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("store: not found")

// CreateUser inserts one account. The admin flag is stored as 0/1 because SQLite
// has no boolean type; it is converted back on the way out.
func (d *DB) CreateUser(ctx context.Context, username, displayName, passwordHash string, isAdmin bool) (*User, error) {
	// SQLite 没有布尔：这里换个整数存，读出来再换回 bool。
	admin := 0
	if isAdmin {
		admin = 1
	}
	// 先把结构体拼好，插入语句只认这几个字段；id 由数据库回填。
	u := &User{
		Username:    username,
		DisplayName: displayName,
		IsAdmin:     isAdmin,
		Status:      "active",
		CreatedAt:   time.Now().UnixMilli(),
	}
	// 真正写库：id 由 LastInsertId 回填，前面拼好的结构体稍后补上它。
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO users (username, display_name, password_hash, is_admin, status, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			u.Username, u.DisplayName, passwordHash, admin, u.Status, u.CreatedAt)
		// 插入失败直接回滚；账号与默认社群成员必须一起成立。
		if err != nil {
			return err
		}
		u.ID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		// 新账号默认进默认社群：一个人至少要有一个可去的社群，
		// 否则登录后是一片空白。
		_, err = tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO community_members (community_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?)",
			DefaultCommunityID, u.ID, u.CreatedAt)
		return err
	})
	// 事务错误直接上抛：这一层不重试，也不把错误翻译成别的形状。
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UserByUsername returns the account together with its password hash. The hash
// is returned separately on purpose: every caller has to name it to get it.
func (d *DB) UserByUsername(ctx context.Context, username string) (*User, string, error) {
	// 哈希单独返回，是为了让调用方必须显式说明「我要用密码」。

	var (
		u     User
		hash  string
		admin int
	)
	// 单行查询：查不到就 ErrNotFound，调用方据此回「凭据不对」，不区分用户是否存在。
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, username, display_name, password_hash, is_admin, status, created_at FROM users WHERE username = ?",
		username).Scan(&u.ID, &u.Username, &u.DisplayName, &hash, &admin, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	// 读失败与「没有这个人」是两回事，前者原样上抛。
	if err != nil {
		return nil, "", err
	}
	u.IsAdmin = admin == 1
	return &u, hash, nil
}

// UserByID fetches one account without the password hash. Nothing that only
// needs identity should ever be handed a credential.
func (d *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	// 与按用户名查的唯一区别：不取哈希——只需要身份的路径不该拿到它。

	var (
		u     User
		admin int
	)
	// 与按用户名查同构，少了哈希那一列。
	err := d.rdb.QueryRowContext(ctx,
		"SELECT id, username, display_name, is_admin, status, created_at FROM users WHERE id = ?",
		id).Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// 读出来的是整数，换成 bool 再交出去。
	u.IsAdmin = admin == 1
	return &u, nil
}

// CreateConversation inserts a conversation with next_seq at zero; only the
// write path is allowed to advance that counter.
func (d *DB) CreateConversation(ctx context.Context, kind, title string, ownerID int64) (*Conversation, error) {
	// 旧调用方没有社群概念：一律落进默认社群，行为与升级前一致。
	return d.CreateConversationIn(ctx, DefaultCommunityID, kind, title, ownerID)
}

// CreateConversationIn inserts a conversation inside one community. The
// community dimension is written in the same row as the sequence counter, so a
// conversation can never be read from the wrong community by accident.
func (d *DB) CreateConversationIn(ctx context.Context, communityID int64, kind, title string, ownerID int64) (*Conversation, error) {
	// 新会话的序号水位从 0 起，第一条消息拿到的就是 1。

	c := &Conversation{CommunityID: communityID, Kind: kind, Title: title, OwnerID: ownerID, CreatedAt: time.Now().UnixMilli()}
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			// next_seq 显式写 0：水位从这里开始，只由写路径递增。
			"INSERT INTO conversations (community_id, kind, title, owner_id, next_seq, created_at) VALUES (?, ?, ?, ?, 0, ?)",
			c.CommunityID, c.Kind, c.Title, c.OwnerID, c.CreatedAt)
		if err != nil {
			return err
		}
		c.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	// id 由 LastInsertId 回填，调用方拿到的就是完整行。
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

// Members returns the recipient ids of one conversation, in a stable order so
// that delivery rows are created in a predictable sequence.
func (d *DB) Members(ctx context.Context, conversationID int64) ([]int64, error) {
	// 稳定排序让待投递记录的创建顺序可重复，日志与测试都靠这一点。

	// 只要 id，不要资料：这个结果直接变成待投递记录的接收者名单。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id FROM members WHERE conversation_id = ? ORDER BY user_id", conversationID)
	if err != nil {
		return nil, err
	}
	// 结果很小，直接收成一个 id 切片；调用方只关心「发给谁」。
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	// rows.Err 把迭代中途的错误带出来，不能只看循环有没有跑完。
	return out, rows.Err()
}

// RoleOf answers the conversation-role question at the storage layer. It never
// consults users.is_admin: that flag is a different power altogether.
func (d *DB) RoleOf(ctx context.Context, conversationID, userID int64) (string, error) {
	// 查不到就 ErrNotFound：调用方据此判定「不是成员」，而不是「没有角色」。

	// 一次点查：会话内角色只存在 members 表里，别处没有副本。
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
	// 返回角色字符串本身：owner / admin / member 就是三种权力。
	return role, nil
}
