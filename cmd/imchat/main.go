package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"imchat/internal/api"
	"imchat/internal/auth"
	"imchat/internal/bus"
	"imchat/internal/delivery"
	"imchat/internal/store"
	"imchat/internal/ws"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	dbPath := flag.String("db", filepath.Join("data", "imchat.db"), "sqlite file path")
	seed := flag.Bool("seed", true, "create demo users and a dm on first run")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	b, err := bus.New()
	if err != nil {
		log.Fatalf("start bus: %v", err)
	}
	defer b.Close()

	hub := ws.NewHub()
	if _, err := b.Subscribe("user.*", func(subject string, data []byte) {
		id, err := strconv.ParseInt(strings.TrimPrefix(subject, "user."), 10, 64)
		if err != nil {
			return
		}
		hub.Deliver(id, data)
	}); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	am := auth.NewManager(db, 7*24*time.Hour)

	srvAPI := &api.Server{DB: db, Auth: am, Hub: hub}
	disp := delivery.New(db, b)
	srvAPI.Dispatcher = disp
	go disp.Run(ctx)

	if *seed {
		seedDemo(ctx, am, db)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           srvAPI.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("imchat listening on http://%s (db=%s)", *addr, *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Print("imchat stopped")
}

func seedDemo(ctx context.Context, am *auth.Manager, db *store.DB) {
	if _, _, err := db.UserByUsername(ctx, "alice"); err == nil {
		return
	}
	alice, err := am.Register(ctx, "alice", "Alice", "password123", true)
	if err != nil {
		log.Printf("seed alice: %v", err)
		return
	}
	bob, err := am.Register(ctx, "bob", "Bob", "password123", false)
	if err != nil {
		log.Printf("seed bob: %v", err)
		return
	}
	c, err := db.CreateConversation(ctx, "dm", "", alice.ID)
	if err != nil {
		log.Printf("seed conversation: %v", err)
		return
	}
	if err := db.AddMember(ctx, c.ID, alice.ID, "owner"); err != nil {
		log.Printf("seed alice member: %v", err)
	}
	if err := db.AddMember(ctx, c.ID, bob.ID, "member"); err != nil {
		log.Printf("seed bob member: %v", err)
	}
	log.Printf("seeded: alice/bob (password123), conversation %d", c.ID)
}
