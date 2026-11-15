package store

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoCommunityScope is returned by every CommunityScope method whose receiver
// carries no community. A missing scope must fail loudly: falling back to "all
// communities" is exactly the cross-community leak this type exists to prevent.
var ErrNoCommunityScope = errors.New("store: no community scope")

// CommunityScope is the sanctioned entry point for reading and writing data
// that belongs to a community. It carries the community dimension every such
// query filters on; callers never pass a community id to the underlying methods.
//
// The zero value is invalid on purpose. Obtain one through DB.InCommunity.
type CommunityScope struct {
	db          *DB
	communityID int64
}

// InCommunity binds storage access to one community. A non-positive id is
// rejected at the boundary, so "I forgot to pass a community" cannot degrade
// into a query that spans every community.
func (d *DB) InCommunity(communityID int64) (*CommunityScope, error) {
	if d == nil {
		return nil, ErrNoCommunityScope
	}
	if communityID <= 0 {
		return nil, fmt.Errorf("store: community scope requires a positive community id, got %d", communityID)
	}
	return &CommunityScope{db: d, communityID: communityID}, nil
}

// valid rejects a scope that never went through InCommunity. It is called by
// every exported method so that misuse fails at the call site, not in a query.
func (s *CommunityScope) valid() error {
	if s == nil || s.db == nil || s.communityID <= 0 {
		return ErrNoCommunityScope
	}
	return nil
}

// CommunityID returns the community this scope is bound to, or zero for an
// invalid scope. It never fails; use valid() when the answer must be enforced.
func (s *CommunityScope) CommunityID() int64 {
	if s == nil {
		return 0
	}
	return s.communityID
}

// ConversationsOf lists one user's conversations inside this community. The
// community filter is applied in the query, not in the caller.
func (s *CommunityScope) ConversationsOf(ctx context.Context, userID int64) ([]*Conversation, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	return s.db.ConversationsOfIn(ctx, s.communityID, userID)
}

// Conversation fetches one conversation inside this community. A conversation
// that belongs to another community is reported as ErrNotFound, never as a
// permission error: the caller must not learn that it exists.
func (s *CommunityScope) Conversation(ctx context.Context, id int64) (*Conversation, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	c, err := s.db.ConversationByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// 跨社群一律当作不存在：错误码不能反推出「它存在但你看不到」。
	if c.CommunityID != s.communityID {
		return nil, ErrNotFound
	}
	return c, nil
}

// MessagesAfter reads history inside this community, strictly after afterSeq.
func (s *CommunityScope) MessagesAfter(ctx context.Context, conversationID, afterSeq int64, limit int) ([]*Message, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	// 先确认这个会话属于本社群，再放行读；顺序不能反。
	if _, err := s.Conversation(ctx, conversationID); err != nil {
		return nil, err
	}
	return s.db.MessagesAfter(ctx, conversationID, afterSeq, limit)
}

// SendMessage is the write path inside this community. Idempotency and sequence
// allocation stay in the underlying implementation, so the scope adds no second
// write path — only the community gate.
func (s *CommunityScope) SendMessage(ctx context.Context, p SendParams) (*Message, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	// 写入前先过社群这道门：跨社群的会话不能被写进任何一条消息。
	if _, err := s.Conversation(ctx, p.ConversationID); err != nil {
		return nil, err
	}
	return s.db.SendMessage(ctx, p)
}
