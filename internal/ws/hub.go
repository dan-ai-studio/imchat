package ws

import "sync"

type Hub struct {
	mu    sync.RWMutex
	conns map[int64]map[*Conn]struct{}
}

func NewHub() *Hub {
	return &Hub{conns: map[int64]map[*Conn]struct{}{}}
}

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

func (h *Hub) Online(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns[userID]) > 0
}

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
