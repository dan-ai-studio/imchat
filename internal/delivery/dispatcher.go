package delivery

// ── 阅读导览 · 第 6 站 ────────────────────────────────────
// 上一站：站5 取号与同事务
// 下一站：站7 扇出

import (
	"context"
	"fmt"
	"time"

	"imchat/internal/bus"
	"imchat/internal/store"
)

// Dispatcher drains the outbox and publishes each due obligation onto the bus.
// It never deletes rows: resolution only happens on client ack.
type Dispatcher struct {
	db  *store.DB
	bus *bus.Bus
}

// New binds the outbox to the bus. The dispatcher keeps no state of its own: the
// outbox table is the state, which is what makes a restart harmless.
func New(db *store.DB, b *bus.Bus) *Dispatcher {
	return &Dispatcher{db: db, bus: b}
}

// Run ticks every 100ms. Each tick does two things, in this order: revive stale
// obligations whose deadline passed without an ack, then publish what is due.
// It stops when ctx is cancelled — that is the shutdown line in main.
func (d *Dispatcher) Run(ctx context.Context) {
	// 100 毫秒是「快到看起来像实时」与「不必为每条消息单独唤醒」之间的折中。
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// 先收拾上一轮：发出去却没等到 ack 的，期限一到就打回待投。
			_, _ = d.db.RepumpStale(ctx, time.Now().UnixMilli())
			// 再把这一轮到期的待投记录真正发出去。
			d.drain(ctx)
		}
	}
}

// drain publishes up to 200 due obligations and marks them in-flight, so the
// next tick will not publish the same rows again while clients are acking.
func (d *Dispatcher) drain(ctx context.Context) {
	// 一次最多 200 条：这是背压的闸门，不是吞吐的承诺。
	entries, err := d.db.DueOutbox(ctx, time.Now().UnixMilli(), 200)
	if err != nil || len(entries) == 0 {
		return
	}
	// 逐条投递。这里没有事务：哪条发失败就不进 ids，下一轮 tick 会再试它。
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		payload := store.FrameJSON(e)
		subject := fmt.Sprintf("user.%d", e.RecipientID)
		if err := d.bus.Publish(subject, payload); err != nil {
			continue
		}
		ids = append(ids, e.OutboxID)
	}
	// 发出去的那批标成 in-flight 并给 5 秒期限：
	// 5 秒内收到 ack 就消解，收不到就由上一轮的 RepumpStale 打回待投。
	if len(ids) > 0 {
		_ = d.db.MarkOutboxInflight(ctx, ids, time.Now().Add(5*time.Second).UnixMilli())
	}
}
