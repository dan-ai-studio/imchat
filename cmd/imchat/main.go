package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
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
	webui "imchat/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	dbPath := flag.String("db", filepath.Join("data", "imchat.db"), "sqlite file path")
	seed := flag.Bool("seed", true, "create demo users and a dm on first run; never enable this in production")
	backupTo := flag.String("backup", "", "write a consistent snapshot to this path, then exit")
	createUser := flag.String("create-user", "", "create this account, then exit")
	password := flag.String("password", "", "password for -create-user")
	admin := flag.Bool("admin", false, "grant platform admin to -create-user")
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

	// Maintenance commands run against the same binary, which keeps the
	// deployment story at "one file": no sqlite3 CLI, no migration tool.
	if *backupTo != "" {
		if err := db.Backup(ctx, *backupTo); err != nil {
			log.Fatalf("backup: %v", err)
		}
		log.Printf("backup written to %s", *backupTo)
		return
	}

	am := auth.NewManager(db, 7*24*time.Hour)

	if *createUser != "" {
		if *password == "" {
			log.Fatal("-password is required with -create-user")
		}
		u, err := am.Register(ctx, *createUser, *createUser, *password, *admin)
		if err != nil {
			log.Fatalf("create user: %v", err)
		}
		log.Printf("created user %q (admin=%v)", u.Username, u.IsAdmin)
		return
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

	filesDir := filepath.Join(filepath.Dir(*dbPath), "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		log.Fatalf("create files dir: %v", err)
	}

	srvAPI := &api.Server{DB: db, Auth: am, Hub: hub, FilesDir: filesDir, Bus: b}
	disp := delivery.New(db, b)
	srvAPI.Dispatcher = disp
	go disp.Run(ctx)

	if *seed {
		seedDemo(ctx, am, db)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           assemble(srvAPI.Router()),
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

// assemble routes api and websocket traffic to the api handler and everything
// else to the embedded frontend, falling back to index.html so the client can
// own its own routes.
func assemble(apiHandler http.Handler) http.Handler {
	sub, err := webui.FS()
	if err != nil {
		log.Printf("web assets unavailable: %v", err)
		return apiHandler
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/ws" || p == "/healthz" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/files/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(p, "/")); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		files.ServeHTTP(w, clone)
	})
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
