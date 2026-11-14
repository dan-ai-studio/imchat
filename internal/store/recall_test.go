package store

import (
	"context"
	"errors"
	"testing"
)

func TestRecallAppendsTombstone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)

	original, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, ClientMsgID: "r-1",
		Kind: "text", Body: "oops", Meta: "{}", Recipients: []int64{aID, bID},
	})
	if err != nil {
		t.Fatal(err)
	}

	tomb, err := db.RecallMessage(ctx, convID, aID, original.Seq, []int64{aID, bID})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if tomb.Kind != "recall" || tomb.RecallOf != original.Seq {
		t.Fatalf("tombstone shape wrong: kind=%q recall_of=%d", tomb.Kind, tomb.RecallOf)
	}
	if tomb.Seq != original.Seq+1 {
		t.Fatalf("tombstone should take the next sequence, want %d got %d", original.Seq+1, tomb.Seq)
	}

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

	if _, err := db.RecallMessage(ctx, convID, aID, original.Seq, []int64{aID}); !errors.Is(err, ErrAlreadyRecalled) {
		t.Fatalf("double recall should fail with ErrAlreadyRecalled, got %v", err)
	}

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
