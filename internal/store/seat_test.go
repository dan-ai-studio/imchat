package store

import (
	"context"
	"errors"
	"testing"
)

// TestSeatFlow proves the seat lifecycle: a hand raise grants nothing, the
// owner promotes the person, and leaving removes the row.
func TestSeatFlow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// 房主与听众各一个：麦位是「谁在说话」，不是「谁在房间」。
	owner, err := db.CreateUser(ctx, "owner", "Owner", "h", false)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	guest, err := db.CreateUser(ctx, "guest", "Guest", "h", false)
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	// 房间必须建在某个社群里：麦位是房间的属性，不是全局的。
	club, err := db.CreateCommunity(ctx, owner.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	room, err := db.CreateRoomIn(ctx, club.ID, owner.ID, "夜谈")
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	// 举手只写一行 requested，不代表已经在麦上。
	if err := db.RequestSeat(ctx, room.ID, guest.ID); err != nil {
		t.Fatalf("request: %v", err)
	}
	seats, err := db.SeatsOf(ctx, room.ID)
	if err != nil {
		t.Fatalf("seats: %v", err)
	}
	if len(seats) != 1 || seats[0].State != "requested" {
		t.Fatalf("after request: %+v", seats)
	}
	// 批准之后变成 on_mic，并且拿到 0 号位。
	if err := db.TakeSeat(ctx, room.ID, guest.ID); err != nil {
		t.Fatalf("take: %v", err)
	}
	seats, err = db.SeatsOf(ctx, room.ID)
	if err != nil {
		t.Fatalf("seats: %v", err)
	}
	if len(seats) != 1 || seats[0].State != "on_mic" || seats[0].Position != 0 {
		t.Fatalf("after take: %+v", seats)
	}
	// 下麦把行删掉：麦位是资源，不留历史。
	if err := db.LeaveSeat(ctx, room.ID, guest.ID); err != nil {
		t.Fatalf("leave: %v", err)
	}
	seats, err = db.SeatsOf(ctx, room.ID)
	if err != nil {
		t.Fatalf("seats: %v", err)
	}
	if len(seats) != 0 {
		t.Fatalf("after leave: %+v", seats)
	}
}

// TestSeatsFull proves the seventeenth person is refused, and that the cap is
// enforced in the same transaction that assigns a position.
func TestSeatsFull(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	owner, err := db.CreateUser(ctx, "owner", "Owner", "h", false)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	// 一间房、十六个位置：后面的循环要把它填满。
	club, err := db.CreateCommunity(ctx, owner.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	room, err := db.CreateRoomIn(ctx, club.ID, owner.ID, "夜谈")
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	// 16 个人依次上麦，每个人都应该拿到不同的位置。
	for i := 0; i < MaxSeats; i++ {
		u, err := db.CreateUser(ctx, "u"+string(rune('a'+i)), "U", "h", false)
		if err != nil {
			t.Fatalf("user %d: %v", i, err)
		}
		if err := db.TakeSeat(ctx, room.ID, u.ID); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	// 第 17 个必须被拒：满员是可预期的业务状态，不是内部错误。
	extra, err := db.CreateUser(ctx, "extra", "Extra", "h", false)
	if err != nil {
		t.Fatalf("extra: %v", err)
	}
	if err := db.TakeSeat(ctx, room.ID, extra.ID); !errors.Is(err, ErrSeatsFull) {
		t.Fatalf("17th seat: got %v, want ErrSeatsFull", err)
	}
}
