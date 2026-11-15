package store

import (
	"context"
	"errors"
	"testing"
)

// TestRecallAppendsTombstone proves ADR-0005 in executable form: recall writes a
// new message and never edits the original row, and it refuses to run twice.
func TestRecallAppendsTombstone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)

	// 先发一条，再撤回它：撤回的目标是一个已存在的序号。
	original, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, ClientMsgID: "r-1",
		Kind: "text", Body: "oops", Meta: "{}", Recipients: []int64{aID, bID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 撤回：写入一条墓碑，指向刚才那个序号。
	tomb, err := db.RecallMessage(ctx, convID, aID, original.Seq, []int64{aID, bID})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if tomb.Kind != "recall" || tomb.RecallOf != original.Seq {
		t.Fatalf("tombstone shape wrong: kind=%q recall_of=%d", tomb.Kind, tomb.RecallOf)
	}
	// 墓碑占的是下一个序号：撤回本身也是一条消息，不额外开通道。
	if tomb.Seq != original.Seq+1 {
		t.Fatalf("tombstone should take the next sequence, want %d got %d", original.Seq+1, tomb.Seq)
	}

	// 再把整个会话拉一遍：原来那条还在，墓碑也在，两行都不被改写。
	all, err := db.MessagesAfter(ctx, convID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("append-only violated: want 2 rows, got %d", len(all))
	}
	if all[0].Body != "oops" || all[0].Seq != 1 {
		t.Fatalf("original row was mutated: %+v", all[0])
	}

	// 再撤一次必须被拒：同一条消息只能被撤回一次。
	if _, err := db.RecallMessage(ctx, convID, aID, original.Seq, []int64{aID}); !errors.Is(err, ErrAlreadyRecalled) {
		t.Fatalf("double recall should fail with ErrAlreadyRecalled, got %v", err)
	}

	// 换个人发一条，再让 a 去撤它：越权撤回必须被拒。
	other, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: bID, ClientMsgID: "r-2",
		Kind: "text", Body: "bobs", Meta: "{}", Recipients: []int64{aID, bID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecallMessage(ctx, convID, aID, other.Seq, []int64{aID}); !errors.Is(err, ErrNotSender) {
		t.Fatalf("recalling someone else's message should fail with ErrNotSender, got %v", err)
	}
}
