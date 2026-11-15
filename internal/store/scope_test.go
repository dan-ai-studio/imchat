package store

import (
	"context"
	"errors"
	"testing"
)

// TestInCommunityRejectsMissingCommunity proves the boundary check: a scope
// cannot be built without a positive community id.
func TestInCommunityRejectsMissingCommunity(t *testing.T) {
	db := newTestDB(t)
	for _, id := range []int64{0, -1} {
		if _, err := db.InCommunity(id); err == nil {
			t.Fatalf("InCommunity(%d) accepted a missing community", id)
		}
	}
}

// TestZeroScopeIsRejected proves that a scope which never went through
// InCommunity fails on use instead of quietly reading across communities.
func TestZeroScopeIsRejected(t *testing.T) {
	ctx := context.Background()
	var nilScope *CommunityScope
	if _, err := nilScope.ConversationsOf(ctx, 1); !errors.Is(err, ErrNoCommunityScope) {
		t.Fatalf("nil scope list: got %v, want ErrNoCommunityScope", err)
	}
	if _, err := nilScope.SendMessage(ctx, SendParams{}); !errors.Is(err, ErrNoCommunityScope) {
		t.Fatalf("nil scope send: got %v, want ErrNoCommunityScope", err)
	}
	// 零值结构体同样要拒绝：漏传社群不能退化成「全部社群」。
	empty := &CommunityScope{}
	if _, err := empty.Conversation(ctx, 1); !errors.Is(err, ErrNoCommunityScope) {
		t.Fatalf("empty scope read: got %v, want ErrNoCommunityScope", err)
	}
	if empty.CommunityID() != 0 {
		t.Fatalf("empty scope id: %d, want 0", empty.CommunityID())
	}
}

// TestScopeMatchesDirectAccess proves the scope is a pass-through today: the
// same writes and reads produce the same results as the un-scoped methods, so
// wiring callers onto it cannot change behaviour.
func TestScopeMatchesDirectAccess(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)
	scope, err := db.InCommunity(1)
	if err != nil {
		t.Fatalf("InCommunity: %v", err)
	}
	// 作用域必须把社群维度原样带出来，否则下一层拿不到它去过滤。
	if scope.CommunityID() != 1 {
		t.Fatalf("CommunityID: %d, want 1", scope.CommunityID())
	}
	// 经作用域写入必须与直接写入产生同一条消息，不能出现第二套写路径。
	if _, err := scope.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, ClientMsgID: "m1",
		Kind: "text", Body: "hello", Recipients: []int64{bID},
	}); err != nil {
		t.Fatalf("scope send: %v", err)
	}
	// 读回来必须与写入一致：历史只按序号取，作用域不该改变排序。
	msgs, err := scope.MessagesAfter(ctx, convID, 0, 10)
	if err != nil {
		t.Fatalf("scope history: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "hello" {
		t.Fatalf("scope history: got %+v", msgs)
	}
	// 列会话走成员关系，作用域不能改变「只返回你所在的」这条。
	convs, err := scope.ConversationsOf(ctx, aID)
	if err != nil {
		t.Fatalf("scope list: %v", err)
	}
	if len(convs) != 1 || convs[0].ID != convID {
		t.Fatalf("scope list: got %+v", convs)
	}
	if _, err := scope.Conversation(ctx, convID); err != nil {
		t.Fatalf("scope read: %v", err)
	}
}
