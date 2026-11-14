package bus

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

func New() (*Bus, error) {
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random loopback port; clients connect in-process
		NoLog:     true,
		NoSigs:    true,
		JetStream: false,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		return nil, errors.New("bus: nats server not ready")
	}
	nc, err := nats.Connect("", nats.InProcessServer(ns))
	if err != nil {
		ns.Shutdown()
		return nil, err
	}
	return &Bus{ns: ns, nc: nc}, nil
}

func (b *Bus) Publish(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

func (b *Bus) Subscribe(subject string, fn func(subject string, data []byte)) (*nats.Subscription, error) {
	return b.nc.Subscribe(subject, func(m *nats.Msg) {
		fn(m.Subject, m.Data)
	})
}

func (b *Bus) Flush() error { return b.nc.Flush() }

func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Close()
	}
	if b.ns != nil {
		b.ns.Shutdown()
	}
}
