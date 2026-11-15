package bus

// ── 阅读导览 · 第 7 站 ────────────────────────────────────
// 上一站：站6 投递器
// 下一站：站8 最后一跳与确认

import (
	"errors"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Bus is an in-process NATS server used only for fan-out.
// JetStream is deliberately off: persistence belongs to SQLite, and enabling
// JetStream would create a second data directory and break the single-file rule.
type Bus struct {
	ns *server.Server
	nc *nats.Conn
}

// New starts an in-process NATS server on a random loopback port and connects to
// it in-process. The data file and the listening socket stay the only resources
// this program owns; the broker is neither.
func New() (*Bus, error) {
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random loopback port; clients connect in-process
		NoLog:     true,
		NoSigs:    true,
		JetStream: false,
	}
	// 端口 -1 = 随机回环端口：别的进程连不上，我们也不占固定端口。
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	go ns.Start()
	// 等它就绪再连，否则第一次 Publish 会静默失败。
	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, errors.New("bus: nats server not ready")
	}
	// 进程内直连，不走 TCP：省一次系统调用，也不需要网络认证。
	nc, err := nats.Connect("", nats.InProcessServer(ns))
	if err != nil {
		ns.Shutdown()
		return nil, err
	}
	return &Bus{ns: ns, nc: nc}, nil
}

// Publish sends one payload to one subject. Failure means "not published",
// never "not delivered" — the outbox still holds the obligation.
func (b *Bus) Publish(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

// Subscribe registers a handler for a subject pattern. It runs on NATS' own
// goroutine, so the handler must not block.
func (b *Bus) Subscribe(subject string, fn func(subject string, data []byte)) (*nats.Subscription, error) {
	return b.nc.Subscribe(subject, func(m *nats.Msg) {
		fn(m.Subject, m.Data)
	})
}

// Flush waits until the server has acknowledged everything published so far.
// Tests use it; the server path never needs to.
func (b *Bus) Flush() error { return b.nc.Flush() }

// Close shuts the client down before the server, so no in-flight callback lands
// on a half-dead bus.
func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Close()
	}
	if b.ns != nil {
		b.ns.Shutdown()
	}
}
