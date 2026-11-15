package ws

// ── 阅读导览 · 第 8 站 ────────────────────────────────────
// 上一站：站7 扇出
// 下一站：站9 断线补齐

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Conn is one terminal: a socket, a user, and a bounded send queue. Everything
// written to this terminal goes through that queue, and only the owning
// goroutine ever touches the socket itself.
type Conn struct {
	ws     *websocket.Conn
	UserID int64
	Device string

	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

// NewConn builds a connection with a 64-slot send queue. The queue is the whole
// back-pressure story: 64 frames of slack, then frames start being dropped.
func NewConn(c *websocket.Conn, userID int64, device string) *Conn {
	return &Conn{
		ws:     c,
		UserID: userID,
		Device: device,
		send:   make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

// Send is non-blocking. A slow client loses frames rather than stalling the
// fan-out path; the client recovers the loss through the sequence gap.
func (c *Conn) Send(data []byte) bool {
	select {
	case c.send <- data:
		return true
	case <-c.closed:
		return false
	default:
		return false
	}
}

// WriteLoop is the only goroutine that writes to the socket. One writer means
// no write mutex and no interleaved frames.
func (c *Conn) WriteLoop(ctx context.Context) {
	for {
		select {
		// 写出去不等于送达：这条消息在服务端仍是 pending，直到客户端发 ack。
		// 所以同一条被投两次是允许的——接收方按 (会话, 序号) 去重。
		case data := <-c.send:
			// 每次写给 10 秒上限：一条卡死的连接不该把写循环无限期挂住。
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(wctx, websocket.MessageText, data)
			cancel()
			// 写失败说明这条连接已经废了：关掉它，剩下的交给客户端重连补齐。
			if err != nil {
				c.Close()
				return
			}
		case <-c.closed:
			return
		case <-ctx.Done():
			c.Close()
			return
		}
	}
}

// ReadLoop is the connection's inbound half: it reads frames and hands each one
// to the caller's switch. Malformed frames are skipped, never fatal.
func (c *Conn) ReadLoop(ctx context.Context, onFrame func(Frame)) {
	c.ws.SetReadLimit(1 << 20)
	// 读是阻塞的：连接断开或 ctx 被取消时，Read 会以错误返回，循环随之结束。
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			c.Close()
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		// 解不开的帧直接丢：一个坏帧不该让整条连接断掉。
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		onFrame(f)
	}
}

// Close is idempotent: sync.Once guarantees the channels close exactly once,
// which is what makes it safe to call from any goroutine racing to shut down.
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}
