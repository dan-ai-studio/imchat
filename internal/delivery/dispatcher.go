package delivery

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

func New(db *store.DB, b *bus.Bus) *Dispatcher {
	return &Dispatcher{db: db, bus: b}
}

func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = d.db.RepumpStale(ctx, time.Now().UnixMilli())
			d.drain(ctx)
		}
	}
}

func (d *Dispatcher) drain(ctx context.Context) {
	entries, err := d.db.DueOutbox(ctx, time.Now().UnixMilli(), 200)
	if err != nil || len(entries) == 0 {
		return
	}
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		payload := store.FrameJSON(e)
		subject := fmt.Sprintf("user.%d", e.RecipientID)
		if err := d.bus.Publish(subject, payload); err != nil {
			continue
		}
		ids = append(ids, e.OutboxID)
	}
	if len(ids) > 0 {
		_ = d.db.MarkOutboxInflight(ctx, ids, time.Now().Add(5*time.Second).UnixMilli())
	}
}
