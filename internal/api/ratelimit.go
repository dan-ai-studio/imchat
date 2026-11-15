package api

import (
	"sync"
	"time"
)

// sendLimiter is the per-user send budget. It is in memory on purpose: a lost
// budget on restart is harmless, and the limit exists to stop abuse, not to
// enforce a billing rule.
type sendLimiter struct {
	mu     sync.Mutex
	byUser map[int64][]int64
}

// NewSendLimiter returns an empty limiter for the server to use.
func NewSendLimiter() *sendLimiter {
	return &sendLimiter{byUser: map[int64][]int64{}}
}

// allow records one send and reports whether it fits the budget. The window is
// one minute; entries older than that are dropped on every check.
func (l *sendLimiter) allow(userID int64, perMinute int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UnixMilli()
	cutoff := now - 60_000
	// 先过一遍旧时间戳：一分钟之前的记录不再占预算。
	kept := make([]int64, 0, len(l.byUser[userID]))
	for _, ts := range l.byUser[userID] {
		if ts > cutoff {
			kept = append(kept, ts)
		}
	}
	// 预算用尽：把清理后的窗口写回去，然后拒绝这一次。
	if len(kept) >= perMinute {
		l.byUser[userID] = kept
		return false
	}
	l.byUser[userID] = append(kept, now)
	return true
}
