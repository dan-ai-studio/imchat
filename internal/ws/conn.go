package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Conn struct {
	ws     *websocket.Conn
	UserID int64
	Device string

	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

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

func (c *Conn) WriteLoop(ctx context.Context) {
	for {
		select {
		case data := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(wctx, websocket.MessageText, data)
			cancel()
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

func (c *Conn) ReadLoop(ctx context.Context, onFrame func(Frame)) {
	c.ws.SetReadLimit(1 << 20)
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			c.Close()
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		onFrame(f)
	}
}

func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}
