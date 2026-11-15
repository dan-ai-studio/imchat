package store

import (
	"context"
	"errors"
	"testing"
)

// TestRegisterLeavesNoCommunity proves registration does not silently place a
// new account into any community: joining is always an explicit act.
func TestRegisterLeavesNoCommunity(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	u, err := db.CreateUser(ctx, "carol", "Carol", "h", false)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// 注册后没有任何社群归属：加入永远是显式动作。
	list, err := db.CommunitiesOf(ctx, u.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("new account already belongs to %d communities", len(list))
	}
}

// TestInviteJoinsOnce proves an invite is single-use: the second attempt fails
// and the first one actually granted membership.
func TestInviteJoinsOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	owner, err := db.CreateUser(ctx, "owner", "Owner", "h", false)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	guest, err := db.CreateUser(ctx, "guest", "Guest", "h", false)
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	// 房主建社群并发一张一次性邀请码。
	club, err := db.CreateCommunity(ctx, owner.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	code, err := db.CreateInvite(ctx, club.ID, owner.ID)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	// 第一次：成功入群。
	got, err := db.JoinByInvite(ctx, code, guest.ID)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	if got != club.ID {
		t.Fatalf("joined community %d, want %d", got, club.ID)
	}
	// 第二次：码已经用掉，必须是「不可用」，不是「不存在」。
	if _, err := db.JoinByInvite(ctx, code, guest.ID); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("second join: got %v, want ErrInviteUnusable", err)
	}
	// 陌生人用同一个码也失败。
	stranger, err := db.CreateUser(ctx, "stranger", "Stranger", "h", false)
	if err != nil {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := db.JoinByInvite(ctx, code, stranger.ID); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("stranger join: got %v, want ErrInviteUnusable", err)
	}
}

// TestRequestJoinNeedsApproval proves the second door: a request grants nothing
// until an administrator approves it, and approval adds the membership.
func TestRequestJoinNeedsApproval(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	owner, err := db.CreateUser(ctx, "owner", "Owner", "h", false)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	applicant, err := db.CreateUser(ctx, "applicant", "Applicant", "h", false)
	if err != nil {
		t.Fatalf("applicant: %v", err)
	}
	// 申请人没有码，只能提交申请；批准前不构成成员。
	club, err := db.CreateCommunity(ctx, owner.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	if err := db.RequestJoin(ctx, club.ID, applicant.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	// 申请还没批：不是成员。
	if _, err := db.CommunityRole(ctx, club.ID, applicant.ID); !errors.Is(err, ErrNotCommunityMember) {
		t.Fatalf("before approval: got %v, want ErrNotCommunityMember", err)
	}
	list, err := db.JoinRequests(ctx, club.ID)
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("requests: got %d, want 1", len(list))
	}
	// 批准：同事务写入成员行。
	if err := db.DecideJoinRequest(ctx, list[0].ID, owner.ID, true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	role, err := db.CommunityRole(ctx, club.ID, applicant.ID)
	if err != nil {
		t.Fatalf("after approval: %v", err)
	}
	if role != "member" {
		t.Fatalf("role: got %q, want member", role)
	}
	// 同一个申请不能批第二次。
	if err := db.DecideJoinRequest(ctx, list[0].ID, owner.ID, true); !errors.Is(err, ErrRequestAlreadyDecided) {
		t.Fatalf("second decision: got %v, want ErrRequestAlreadyDecided", err)
	}
}
