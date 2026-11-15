package store

import (
	"context"
	"testing"
	"time"
)

// TestGroupLifecycleAndMute proves the whole conversation-role lifecycle: create a
// group, add and remove members, mute with a deadline, and change a role.
func TestGroupLifecycleAndMute(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// 三个账号：一个群主、两个待加入的成员。
	owner, _ := db.CreateUser(ctx, "own", "Owner", "h", false)
	a, _ := db.CreateUser(ctx, "aaa", "A", "h", false)
	b, _ := db.CreateUser(ctx, "bbb", "B", "h", false)

	// 建群：群主自动成为 owner，另外两人以 member 加入。
	g, err := db.CreateGroup(ctx, owner.ID, "team", []int64{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if g.Kind != "group" || g.Title != "team" || g.OwnerID != owner.ID {
		t.Fatalf("unexpected group: %+v", g)
	}

	// 群主 + 两个被邀请的人 = 3 个成员。
	members, err := db.MembersOf(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("want 3 members, got %d", len(members))
	}
	// 第一个成员是群主：排序按 joined_at，建群时 owner 先落库。
	if members[0].Role != "owner" {
		t.Fatalf("first member should be the owner, got %q", members[0].Role)
	}

	ids, err := db.Members(ctx, g.ID)
	if err != nil || len(ids) != 3 {
		t.Fatalf("recipient list should cover every member, got %v (%v)", ids, err)
	}

	// 接着移人：消息不受影响，只是这个人不再是成员。
	if err := db.RemoveMember(ctx, g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := db.Members(ctx, g.ID)
	if len(after) != 2 {
		t.Fatalf("want 2 members after removal, got %d", len(after))
	}

	// 再禁言：写到未来的一个时间点，读回来必须还在未来。
	if err := db.SetMuted(ctx, g.ID, a.ID, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// 读回来验证：禁言到期时间必须还在未来。
	until, err := db.MutedUntil(ctx, g.ID, a.ID)
	if err != nil || until <= time.Now().UnixMilli() {
		t.Fatalf("mute should be in the future, got %d (%v)", until, err)
	}

	if err := db.SetRole(ctx, g.ID, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	role, err := db.RoleOf(ctx, g.ID, a.ID)
	if err != nil || role != "admin" {
		t.Fatalf("want admin role, got %q (%v)", role, err)
	}
}
