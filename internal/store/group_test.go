package store

import (
	"context"
	"testing"
	"time"
)

func TestGroupLifecycleAndMute(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	owner, _ := db.CreateUser(ctx, "own", "Owner", "h", false)
	a, _ := db.CreateUser(ctx, "aaa", "A", "h", false)
	b, _ := db.CreateUser(ctx, "bbb", "B", "h", false)

	g, err := db.CreateGroup(ctx, owner.ID, "team", []int64{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if g.Kind != "group" || g.Title != "team" || g.OwnerID != owner.ID {
		t.Fatalf("unexpected group: %+v", g)
	}

	members, err := db.MembersOf(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("want 3 members, got %d", len(members))
	}
	if members[0].Role != "owner" {
		t.Fatalf("first member should be the owner, got %q", members[0].Role)
	}

	ids, err := db.Members(ctx, g.ID)
	if err != nil || len(ids) != 3 {
		t.Fatalf("recipient list should cover every member, got %v (%v)", ids, err)
	}

	if err := db.RemoveMember(ctx, g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := db.Members(ctx, g.ID)
	if len(after) != 2 {
		t.Fatalf("want 2 members after removal, got %d", len(after))
	}

	if err := db.SetMuted(ctx, g.ID, a.ID, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
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
