package ws

// ── 阅读导览 · 第 7 站 ────────────────────────────────────
// 上一站：站6 投递器
// 下一站：站8 最后一跳与确认

import "sync"

// Hub is the fan-out target: one entry per user, holding every live connection
// of that user. A user with three devices gets three sends for one message.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64]map[*Conn]struct{}
}

// NewHub returns an empty hub, ready to use. The map is allocated here so no
// caller ever has to think about nil maps.
func NewHub() *Hub {
	return &Hub{conns: map[int64]map[*Conn]struct{}{}}
}

// Add registers a connection under its user. The per-user map is created on
// first use, because most users have exactly one device.
func (h *Hub) Add(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.conns[c.UserID]
	if m == nil {
		m = map[*Conn]struct{}{}
		h.conns[c.UserID] = m
	}
	m[c] = struct{}{}
}

// Remove drops a connection and prunes the user's map once it is empty, so a
// user who logs out leaves nothing behind in memory.
func (h *Hub) Remove(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.conns[c.UserID]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.conns, c.UserID)
		}
	}
}

// Deliver writes to every live connection of one user. It is called from the
// bus subscriber, so a single publish fans out to all of that user's devices.
func (h *Hub) Deliver(userID int64, data []byte) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for c := range h.conns[userID] {
		if c.Send(data) {
			n++
		}
	}
	return n
}

// Online reports whether the user has at least one live connection. It is
// presence — instantaneous, never persisted, never part of history.
func (h *Hub) Online(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns[userID]) > 0
}

// Connections is the total number of live connections; the health and stats
// endpoints report it as a single number.
func (h *Hub) Connections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, m := range h.conns {
		n += len(m)
	}
	return n
}

// DisconnectUser closes every live connection of one user and returns how many
// were closed. This is the platform-admin "kick" action: it acts on connections,
// never on conversation membership.
func (h *Hub) DisconnectUser(userID int64) int {
	h.mu.RLock()
	targets := make([]*Conn, 0, len(h.conns[userID]))
	for c := range h.conns[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.Close()
	}
	return len(targets)
}
