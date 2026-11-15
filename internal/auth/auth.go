package auth

// ── 阅读导览 · 第 2 站 ────────────────────────────────────
// 上一站：站1 进程装配
// 下一站：站3 帧协议

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	"imchat/internal/store"
)

var (
	ErrBadCredentials = errors.New("auth: bad credentials")
	ErrDisabled       = errors.New("auth: account disabled")
	ErrWeakInput      = errors.New("auth: username or password too short")
)

// Manager turns a password into a session token, and a token back into a user.
// It owns the one answer the rest of the server must be able to trust: who is
// this connection.
type Manager struct {
	db  *store.DB
	ttl time.Duration
}

// NewManager binds the session store and the session lifetime together.
func NewManager(db *store.DB, ttl time.Duration) *Manager {
	return &Manager{db: db, ttl: ttl}
}

// Register hashes the password and creates the account. bcrypt is deliberately
// slow: that cost is what makes a leaked hash useless.
func (m *Manager) Register(ctx context.Context, username, display, password string, admin bool) (*store.User, error) {
	if len(username) < 2 || len(password) < 6 {
		return nil, ErrWeakInput
	}
	if display == "" {
		display = username
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return m.db.CreateUser(ctx, username, display, string(hash), admin)
}

// Login verifies the password and mints a fresh token. Unknown user and wrong
// password collapse into one error on purpose: a login form must not reveal
// which usernames exist. A disabled account is a separate error, because the
// operator needs to tell "you are banned" apart from "you typed it wrong".
func (m *Manager) Login(ctx context.Context, username, password string) (string, *store.User, error) {
	// 第一步：把人找出来，连同他的密码哈希一起拿回来。
	u, hash, err := m.db.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, ErrBadCredentials
		}
		return "", nil, err
	}
	// 账号被禁用就直接拒绝，连密码都不用比——封禁要的是立刻生效。
	if u.Status != "active" {
		return "", nil, ErrDisabled
	}
	// 第二步：比密码。这里故意很慢（bcrypt），慢才是安全的来源。
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", nil, ErrBadCredentials
	}
	// 第三步：发一张随机的、服务端记住的令牌。它不是 JWT，因为签名令牌撤销不掉。
	token := newToken()
	if err := m.db.CreateSession(ctx, token, u.ID, time.Now()); err != nil {
		return "", nil, err
	}
	return token, u, nil
}

// Resolve turns a token into the user it belongs to. An empty token is not an
// error worth a database round trip.
func (m *Manager) Resolve(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, ErrBadCredentials
	}
	return m.db.SessionUser(ctx, token, time.Now(), m.ttl)
}

// Logout deletes the session row. Deleting is the whole point: a stolen token
// stops working the moment its row is gone.
func (m *Manager) Logout(ctx context.Context, token string) error {
	return m.db.DeleteSession(ctx, token)
}

// TTL is the session lifetime, exposed so the HTTP layer can set the cookie's
// expiry to match what the server will actually enforce.
func (m *Manager) TTL() time.Duration { return m.ttl }

// newToken returns 32 random bytes as hex. Opaque by design: only the server
// knows what a token means, which is what makes revocation possible at all.
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
