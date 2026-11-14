package auth

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

type Manager struct {
	db  *store.DB
	ttl time.Duration
}

func NewManager(db *store.DB, ttl time.Duration) *Manager {
	return &Manager{db: db, ttl: ttl}
}

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

func (m *Manager) Login(ctx context.Context, username, password string) (string, *store.User, error) {
	u, hash, err := m.db.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, ErrBadCredentials
		}
		return "", nil, err
	}
	if u.Status != "active" {
		return "", nil, ErrDisabled
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", nil, ErrBadCredentials
	}
	token := newToken()
	if err := m.db.CreateSession(ctx, token, u.ID, time.Now()); err != nil {
		return "", nil, err
	}
	return token, u, nil
}

func (m *Manager) Resolve(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, ErrBadCredentials
	}
	return m.db.SessionUser(ctx, token, time.Now(), m.ttl)
}

func (m *Manager) Logout(ctx context.Context, token string) error {
	return m.db.DeleteSession(ctx, token)
}

func (m *Manager) TTL() time.Duration { return m.ttl }

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
