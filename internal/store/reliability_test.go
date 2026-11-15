package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// newTestDB opens a fresh database in the test's temp directory and migrates it.
// Cleanup closes it, so each test owns one file and never shares state.
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

// seedConv builds the smallest world that can carry a conversation: two users
// and one direct chat between them. It returns the conversation and both ids.
func seedConv(t *testing.T, db *DB) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	a, err := db.CreateUser(ctx, "a", "A", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	// 第二个账号：至少要两个人，投递才有意义。
	b, err := db.CreateUser(ctx, "b", "B", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.CreateConversation(ctx, "dm", "", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 两人都进这个会话：一个 owner，一个 member。
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

	// 40 条足以暴露空洞与重复，又不至于让失败信息淹没在噪音里。
	const n = 40
	for i := 0; i < n; i++ {
		// 一条一条发，每条带不同的 client_msg_id。
		if _, err := db.SendMessage(ctx, SendParams{
			ConversationID: convID,
			SenderID:       aID,
			ClientMsgID:    fmt.Sprintf("cm-%d", i),
			Kind:           "text",
			// 正文各不相同，出问题时报错信息能直接定位是哪一条。
			Body:       fmt.Sprintf("m%d", i),
			Meta:       "{}",
			Recipients: []int64{aID, bID},
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// 全部发完后一次拉回来：条数、顺序、序号连续性三件事一起验。
	msgs, err := db.MessagesAfter(ctx, convID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n {
		t.Fatalf("lost or duplicated: want %d messages, got %d", n, len(msgs))
	}
	// 逐条比序号：出现空洞或乱序，这里第一个就报。
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
	// 重发必须拿回原来的位置，而不是新分配一个序号。
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
	// 8 个 goroutine × 10 条：竞争越激烈，越能证明序号依然稠密。
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		// 每个 worker 一个 goroutine，错误经 channel 汇总，不在 goroutine 里直接 Fatal。
		go func(w int) {
			defer wg.Done()
			// 每个 worker 发 10 条，client_msg_id 带 worker 前缀以免撞车。
			for i := 0; i < perWorker; i++ {
				_, err := db.SendMessage(ctx, SendParams{
					ConversationID: convID,
					SenderID:       aID,
					ClientMsgID:    fmt.Sprintf("w%d-%d", w, i),
					Kind:           "text",
					// 正文相同没关系：这里比的是序号密度，不是内容。
					Body:       "x",
					Meta:       "{}",
					Recipients: []int64{aID, bID},
				})
				// 失败不 Fatal：goroutine 里 Fatal 会直接杀掉测试进程。
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}
	// 等所有 worker 收工，再把收集到的错误一次性报出来。
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent send: %v", err)
	}

	// 拉回全部消息验序号密度：并发写了 80 条，序号就该是 1..80 无空洞。
	msgs, err := db.MessagesAfter(ctx, convID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	// 条数必须一条不差：少一条是丢消息，多一条是重复行。
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

	// 一条消息两个接收者 = 两条待投递义务。这是「不丢」的起点。
	stats, _ := db.OutboxStats(ctx)
	if stats["pending"] != 2 {
		t.Fatalf("want 2 pending obligations, got %v", stats)
	}

	due, err := db.DueOutbox(ctx, 1e15, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("want 2 due entries, got %d (%v)", len(due), err)
	}
	// 标成 inflight 并给最早的期限：接下来要看它会不会自己回来。
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

// TestNotFoundIsSentinel keeps ErrNotFound comparable: callers branch on it with
// errors.Is, so wrapping it into a new error would silently break every caller.
func TestNotFoundIsSentinel(t *testing.T) {
	db := newTestDB(t)
	_, _, err := db.UserByUsername(context.Background(), "nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
