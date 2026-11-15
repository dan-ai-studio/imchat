package store

import (
	"context"
	"testing"
)

// TestLevelStartsAtOne proves an account that never earned anything reads as
// level 1 without a row being written.
func TestLevelStartsAtOne(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// 一个全新账号：从没发过言，也没有任何经验事件。
	u, err := db.CreateUser(ctx, "fresh", "Fresh", "h", false)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// 没赚过经验的账号读出来是 1 级 0 经验，不需要先写一行。
	l, err := db.LevelOf(ctx, u.ID)
	if err != nil {
		t.Fatalf("level: %v", err)
	}
	if l.Level != 1 || l.Exp != 0 {
		t.Fatalf("fresh level: %+v", l)
	}
}

// TestAddExpIsIdempotent proves replaying the same event adds nothing: this is
// what stops a redelivered message from granting experience twice.
func TestAddExpIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	u, err := db.CreateUser(ctx, "earner", "Earner", "h", false)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	// 同一条消息 id 记两次：第二次必须被唯一键吃掉。
	if err := db.AddExp(ctx, u.ID, "message", "msg-1", 60); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := db.AddExp(ctx, u.ID, "message", "msg-1", 60); err != nil {
		t.Fatalf("second: %v", err)
	}
	l, err := db.LevelOf(ctx, u.ID)
	if err != nil {
		t.Fatalf("level: %v", err)
	}
	if l.Exp != 60 || l.Level != 1 {
		t.Fatalf("after replay: %+v", l)
	}
	// 换一个 ref 才会真的加，并且跨过 100 分就升到 2 级。
	if err := db.AddExp(ctx, u.ID, "message", "msg-2", 40); err != nil {
		t.Fatalf("third: %v", err)
	}
	l, err = db.LevelOf(ctx, u.ID)
	if err != nil {
		t.Fatalf("level: %v", err)
	}
	if l.Exp != 100 || l.Level != 2 {
		t.Fatalf("after reaching 100: %+v", l)
	}
}

// TestLevelsOfBatch proves the batch read returns a default row for accounts
// that never earned anything, so a member list never shows a hole.
func TestLevelsOfBatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// 两个账号：一个赚过经验，一个没有。
	a, err := db.CreateUser(ctx, "a", "A", "h", false)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := db.CreateUser(ctx, "b", "B", "h", false)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	// 只给 a 加分：b 应该保持默认值，而不是在结果里消失。
	if err := db.AddExp(ctx, a.ID, "message", "m", 150); err != nil {
		t.Fatalf("exp: %v", err)
	}
	// 批量读：两个 id 一次取回，没行的那位也要出现在结果里。
	levels, err := db.LevelsOf(ctx, []int64{a.ID, b.ID})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	// a 跨过 100 分，应该是 2 级。
	if levels[a.ID].Level != 2 {
		t.Fatalf("a level: %+v", levels[a.ID])
	}
	// b 没有事件，应该保持 1 级 0 经验。
	if levels[b.ID].Level != 1 || levels[b.ID].Exp != 0 {
		t.Fatalf("b level: %+v", levels[b.ID])
	}
}
