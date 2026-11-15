package store

import (
	"context"
	"testing"
)

// TestRoomBroadcastSkipsOutbox proves the core promise difference: a room
// message is stored once and creates no per-recipient delivery obligation.
func TestRoomBroadcastSkipsOutbox(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	owner, err := db.CreateUser(ctx, "owner", "Owner", "h", false)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// 第二个账号只当听众：房间的接收者是「在场的人」，不是「会话成员」。
	listener, err := db.CreateUser(ctx, "listener", "Listener", "h", false)
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	club, err := db.CreateCommunity(ctx, owner.ID, "俱乐部", "")
	if err != nil {
		t.Fatalf("community: %v", err)
	}
	// 房间是会话的一种：复用消息与序号，但投递语义是广播。
	room, err := db.CreateRoomIn(ctx, club.ID, owner.ID, "夜谈")
	if err != nil {
		t.Fatalf("room: %v", err)
	}
	// 两个人都进场：房间的接收者就是此刻在场的人。
	if err := db.EnterRoom(ctx, room.ID, owner.ID); err != nil {
		t.Fatalf("enter owner: %v", err)
	}
	if err := db.EnterRoom(ctx, room.ID, listener.ID); err != nil {
		t.Fatalf("enter listener: %v", err)
	}
	// 在场名单就是广播的收件人名单。
	occupants, err := db.RoomOccupants(ctx, room.ID)
	if err != nil {
		t.Fatalf("occupants: %v", err)
	}
	if len(occupants) != 2 {
		t.Fatalf("occupants: got %d, want 2", len(occupants))
	}
	// 房间消息：写一行，不写待投递记录。
	msg, err := db.SendMessage(ctx, SendParams{
		ConversationID: room.ID, SenderID: owner.ID, Kind: "text", Body: "开播", IsRoom: true,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.Delivery != "broadcast" {
		t.Fatalf("delivery: got %q, want broadcast", msg.Delivery)
	}
	// 关键断言：房间消息一条待投递记录都不该产生。
	stats, err := db.OutboxStats(ctx)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if stats["pending"] != 0 {
		t.Fatalf("room message created %d outbox rows", stats["pending"])
	}
	// 退房后在场人数归零，但会话成员身份保留。
	if err := db.LeaveRoom(ctx, room.ID, listener.ID); err != nil {
		t.Fatalf("leave: %v", err)
	}
	// 在场名单应该少一个人。
	occupants, err = db.RoomOccupants(ctx, room.ID)
	if err != nil {
		t.Fatalf("occupants: %v", err)
	}
	if len(occupants) != 1 {
		t.Fatalf("occupants after leave: got %d, want 1", len(occupants))
	}
	if _, err := db.RoleOf(ctx, room.ID, listener.ID); err != nil {
		t.Fatalf("membership should survive leaving: %v", err)
	}
}

// TestConversationKeepsOutbox proves the other half: a normal conversation
// still writes one obligation per recipient.
func TestConversationKeepsOutbox(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)
	// 普通会话仍然逐接收者落义务：这是「至少一次」的另一半。
	msg, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, Kind: "text", Body: "hi", Recipients: []int64{bID},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// 投递语义写在消息行上，读历史的人不必回头查会话类型。
	if msg.Delivery != "at_least_once" {
		t.Fatalf("delivery: got %q, want at_least_once", msg.Delivery)
	}
	stats, err := db.OutboxStats(ctx)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if stats["pending"] != 1 {
		t.Fatalf("outbox: got %d pending, want 1", stats["pending"])
	}
}
