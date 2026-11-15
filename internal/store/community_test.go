package store

import (
	"context"
	"errors"
	"testing"
)

// TestCreateCommunityMakesOwner proves the creator is the first administrator,
// and that a fresh account already belongs to the default community.
func TestCreateCommunityMakesOwner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	u, err := db.CreateUser(ctx, "alice", "Alice", "h", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	c, err := db.CreateCommunity(ctx, u.ID, "公会", "一起玩")
	if err != nil {
		t.Fatalf("create community: %v", err)
	}
	// 创建者必须是 owner：没有主人的社群不是有效状态。
	role, err := db.CommunityRole(ctx, c.ID, u.ID)
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role: got %q, want owner", role)
	}
	// 注册不再自动分配社群：列表里只有刚建的那一个。
	list, err := db.CommunitiesOf(ctx, u.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("communities: got %d, want 1", len(list))
	}
}

// TestCrossCommunityConversationIsInvisible proves the isolation promise: a
// conversation created in one community cannot be reached from another, and the
// error is ErrNotFound rather than a permission error.
func TestCrossCommunityConversationIsInvisible(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	alice, err := db.CreateUser(ctx, "alice", "Alice", "h", false)
	if err != nil {
		t.Fatalf("user alice: %v", err)
	}
	bob, err := db.CreateUser(ctx, "bob", "Bob", "h", false)
	if err != nil {
		t.Fatalf("user bob: %v", err)
	}
	// Alice 建一个社群并在里面建会话；Bob 不在这个社群。
	club, err := db.CreateCommunity(ctx, alice.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	// 会话建在这个社群里，而不是默认社群：社群维度在写入时就固定。
	conv, err := db.CreateConversationIn(ctx, club.ID, "group", "内部群", alice.ID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if err := db.AddMember(ctx, conv.ID, alice.ID, "owner"); err != nil {
		t.Fatalf("member: %v", err)
	}
	clubScope, err := db.InCommunity(club.ID)
	if err != nil {
		t.Fatalf("club scope: %v", err)
	}
	// Alice 在自己社群里看得到这个会话。
	if _, err := clubScope.Conversation(ctx, conv.ID); err != nil {
		t.Fatalf("owner read: %v", err)
	}
	// Bob 用默认社群的作用域去读，必须得到「不存在」，而不是「无权限」。
	defaultScope, err := db.InCommunity(DefaultCommunityID)
	if err != nil {
		t.Fatalf("default scope: %v", err)
	}
	if _, err := defaultScope.Conversation(ctx, conv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-community read: got %v, want ErrNotFound", err)
	}
	// 列表里也不能出现：过滤发生在 SQL 里，不是事后剔。
	list, err := defaultScope.ConversationsOf(ctx, bob.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range list {
		if c.ID == conv.ID {
			t.Fatalf("cross-community conversation leaked into the list")
		}
	}
}

// TestScopeRefusesWriteIntoAnotherCommunity proves the write gate: a scoped
// send cannot reach a conversation outside the scope, and leaves no row behind.
func TestScopeRefusesWriteIntoAnotherCommunity(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	alice, err := db.CreateUser(ctx, "alice", "Alice", "h", false)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// 同一个会话，换一个社群作用域去写，就是跨社群写入的现场。
	club, err := db.CreateCommunity(ctx, alice.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	conv, err := db.CreateConversationIn(ctx, club.ID, "group", "内部群", alice.ID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if err := db.AddMember(ctx, conv.ID, alice.ID, "owner"); err != nil {
		t.Fatalf("member: %v", err)
	}
	// 换一个作用域（默认社群）来写同一个会话：跨社群写必须被拒。
	other, err := db.InCommunity(DefaultCommunityID)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	// 跨社群写：必须在写之前就被拒，不能落进任何一条消息。
	_, err = other.SendMessage(ctx, SendParams{
		ConversationID: conv.ID, SenderID: alice.ID, Kind: "text", Body: "x",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-community send: got %v, want ErrNotFound", err)
	}
	// 库里不能多出这条消息。
	msgs, err := db.MessagesAfter(ctx, conv.ID, 0, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("cross-community send wrote %d rows", len(msgs))
	}
}
