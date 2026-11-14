package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedConv(t *testing.T, db *DB) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	a, err := db.CreateUser(ctx, "a", "A", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateUser(ctx, "b", "B", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConversation(ctx, "dm", "", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(ctx, c.ID, a.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(ctx, c.ID, b.ID, "member"); err != nil {
		t.Fatal(err)
	}
	return c.ID, a.ID, b.ID
}

// TestReliability is the executable form of ADR-0004: no loss, no duplicate
// rows, no reordering, no holes in the conversation sequence.
func TestReliability(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)

	const n = 40
	for i := 0; i < n; i++ {
		if _, err := db.SendMessage(ctx, SendParams{
			ConversationID: convID,
			SenderID:       aID,
			ClientMsgID:    fmt.Sprintf("cm-%d", i),
			Kind:           "text",
			Body:           fmt.Sprintf("m%d", i),
			Meta:           "{}",
			Recipients:     []int64{aID, bID},
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	msgs, err := db.MessagesAfter(ctx, convID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n {
		t.Fatalf("lost or duplicated: want %d messages, got %d", n, len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Fatalf("sequence hole or reorder at index %d: seq=%d", i, m.Seq)
		}
	}

	// Idempotent resend must return the original position and add no row.
	again, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, ClientMsgID: "cm-0",
		Kind: "text", Body: "resend", Meta: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Seq != 1 {
		t.Fatalf("idempotent resend returned seq %d, want 1", again.Seq)
	}
	after, _ := db.MessagesAfter(ctx, convID, 0, 1000)
	if len(after) != n {
		t.Fatalf("idempotent resend created a row: %d", len(after))
	}
}

// TestConcurrentSendsKeepSequenceDense proves the single-writer design under
// contention: many goroutines, still exactly one dense sequence.
func TestConcurrentSendsKeepSequenceDense(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)

	const workers = 8
	const perWorker = 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_, err := db.SendMessage(ctx, SendParams{
					ConversationID: convID,
					SenderID:       aID,
					ClientMsgID:    fmt.Sprintf("w%d-%d", w, i),
					Kind:           "text",
					Body:           "x",
					Meta:           "{}",
					Recipients:     []int64{aID, bID},
				})
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent send: %v", err)
	}

	msgs, err := db.MessagesAfter(ctx, convID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	want := workers * perWorker
	if len(msgs) != want {
		t.Fatalf("want %d messages, got %d", want, len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Fatalf("hole at index %d: seq=%d", i, m.Seq)
		}
	}
}

// TestOutboxResolvesOnlyOnAck proves at-least-once: a delivery handed to the bus
// stays inflight until acked, and comes back for retry if it is not.
func TestOutboxResolvesOnlyOnAck(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convID, aID, bID := seedConv(t, db)

	if _, err := db.SendMessage(ctx, SendParams{
		ConversationID: convID, SenderID: aID, ClientMsgID: "only",
		Kind: "text", Body: "hi", Meta: "{}", Recipients: []int64{aID, bID},
	}); err != nil {
		t.Fatal(err)
	}

	stats, _ := db.OutboxStats(ctx)
	if stats["pending"] != 2 {
		t.Fatalf("want 2 pending obligations, got %v", stats)
	}

	due, err := db.DueOutbox(ctx, 1e15, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("want 2 due entries, got %d (%v)", len(due), err)
	}
	ids := []int64{due[0].OutboxID, due[1].OutboxID}
	if err := db.MarkOutboxInflight(ctx, ids, 1); err != nil {
		t.Fatal(err)
	}

	// Unacked: still nobody has consumed it, so repump must return it.
	n, err := db.RepumpStale(ctx, 1e15)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 repumped, got %d", n)
	}

	// Ack one recipient only.
	if err := db.AckOutbox(ctx, convID, 1, aID); err != nil {
		t.Fatal(err)
	}
	stats, _ = db.OutboxStats(ctx)
	if stats["done"] != 1 {
		t.Fatalf("want 1 done after ack, got %v", stats)
	}
	if stats["pending"] != 1 {
		t.Fatalf("want 1 still pending, got %v", stats)
	}
}

func TestNotFoundIsSentinel(t *testing.T) {
	db := newTestDB(t)
	_, _, err := db.UserByUsername(context.Background(), "nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
