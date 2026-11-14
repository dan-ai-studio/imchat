package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

//go:embed schema.sql
var schemaFS embed.FS

type writeReq struct {
	ctx context.Context
	fn  func(context.Context, *sql.Tx) error
	res chan error
}

// DB holds two handles on the same SQLite file:
//   - rdb: a pool of readers. WAL lets them run concurrently with the writer.
//   - wdb: exactly one connection, and every write goes through a single
//     goroutine that owns it. This is the physical basis of the guarantee that
//     "allocating a conversation sequence and inserting the message happen
//     atomically", so a message sequence can never contain a hole.
type DB struct {
	rdb    *sql.DB
	wdb    *sql.DB
	writes chan writeReq
	done   chan struct{}
	once   sync.Once
}

func Open(path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	rdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open reader: %w", err)
	}
	rdb.SetMaxOpenConns(8)
	rdb.SetMaxIdleConns(8)

	// _txlock=immediate makes every write transaction take the write lock up
	// front instead of failing on a deferred upgrade.
	wdb, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("open writer: %w", err)
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetMaxIdleConns(1)

	d := &DB{
		rdb:    rdb,
		wdb:    wdb,
		writes: make(chan writeReq, 256),
		done:   make(chan struct{}),
	}
	go d.writeLoop()
	return d, nil
}

// Read returns the reader pool. Queries must not write.
func (d *DB) Read() *sql.DB { return d.rdb }

// Write runs fn inside a transaction owned by the single writer goroutine.
func (d *DB) Write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	req := writeReq{ctx: ctx, fn: fn, res: make(chan error, 1)}
	select {
	case d.writes <- req:
	case <-d.done:
		return sql.ErrConnDone
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.res:
		return err
	case <-d.done:
		return sql.ErrConnDone
	}
}

func (d *DB) writeLoop() {
	for {
		select {
		case req := <-d.writes:
			req.res <- d.exec(req)
		case <-d.done:
			return
		}
	}
}

func (d *DB) exec(req writeReq) error {
	tx, err := d.wdb.BeginTx(req.ctx, nil)
	if err != nil {
		return err
	}
	if err := req.fn(req.ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *DB) Close() error {
	d.once.Do(func() { close(d.done) })
	errW := d.wdb.Close()
	errR := d.rdb.Close()
	if errW != nil {
		return errW
	}
	return errR
}

func (d *DB) Migrate(ctx context.Context) error {
	var v int
	if err := d.rdb.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v >= SchemaVersion {
		return nil
	}
	body, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion))
		return err
	})
}
