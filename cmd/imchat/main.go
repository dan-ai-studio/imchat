package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"imchat/internal/api"
	"imchat/internal/auth"
	"imchat/internal/bus"
	"imchat/internal/delivery"
	"imchat/internal/store"
	"imchat/internal/ws"
	webui "imchat/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address when no domain is given")
	dbPath := flag.String("db", filepath.Join("data", "imchat.db"), "sqlite file path")
	domain := flag.String("domain", "", "public hostname; turns on built-in HTTPS via Let's Encrypt")
	acmeHTTP := flag.String("acme-http", ":80", "address for the ACME HTTP-01 challenge and the http→https redirect")
	httpsAddr := flag.String("https", ":443", "address for HTTPS when -domain is set")
	openBrowserFlag := flag.Bool("open", true, "open a browser on startup in local mode")
	seed := flag.Bool("seed", false, "create demo users alice/bob; local demos and tests only")
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

	// Maintenance commands share the same binary: no sqlite3 CLI, no migration tool.
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

	// Bootstrap: an empty database gets one admin account with a generated
	// password, printed once. Demo accounts are opt-in via -seed.
	adminUser, adminPass, bootstrapped := "", "", false
	if *seed {
		seedDemo(ctx, am, db)
	} else {
		adminUser, adminPass, bootstrapped = ensureAdmin(ctx, am, db)
	}

	handler := assemble(srvAPI.Router())

	// Browser-facing URL, used for the banner and the auto-open.
	scheme, host := "http", *addr
	if *domain != "" {
		scheme, host = "https", *domain
	}
	publicURL := scheme + "://" + host
	if *domain == "" && strings.HasPrefix(host, "127.0.0.1:") {
		publicURL = "http://localhost" + strings.TrimPrefix(host, "127.0.0.1")
	}

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	var acmeSrv *http.Server
	if *domain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(filepath.Join(filepath.Dir(*dbPath), "certs")),
			HostPolicy: autocert.HostWhitelist(*domain),
		}
		// Port 80 answers the HTTP-01 challenge and redirects everything else.
		acmeSrv = &http.Server{
			Addr:              *acmeHTTP,
			Handler:           m.HTTPHandler(http.HandlerFunc(redirectToHTTPS)),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Printf("acme: listening on %s for HTTP-01 and redirects", *acmeHTTP)
			if err := acmeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("acme listener stopped: %v", err)
			}
		}()

		srv.Addr = *httpsAddr
		srv.TLSConfig = &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		go func() {
			log.Printf("imchat listening on https://%s", *domain)
			if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("https: %v", err)
			}
		}()
	} else {
		srv.Addr = *addr
		go func() {
			log.Printf("imchat listening on http://%s", *addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("http: %v", err)
			}
		}()
	}

	printBanner(publicURL, *dbPath, adminUser, adminPass, bootstrapped)

	if *openBrowserFlag && *domain == "" {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openURL(publicURL)
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if acmeSrv != nil {
		_ = acmeSrv.Shutdown(shutdownCtx)
	}
	log.Print("imchat stopped")
}

func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	target := "https://" + r.Host + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// ensureAdmin creates a single platform admin with a generated password when
// the database has no accounts yet. The password is shown exactly once.
func ensureAdmin(ctx context.Context, am *auth.Manager, db *store.DB) (string, string, bool) {
	counts, err := db.Counts(ctx)
	if err != nil || counts["users"] > 0 {
		return "", "", false
	}
	pw := randomPassword(14)
	if _, err := am.Register(ctx, "admin", "Admin", pw, true); err != nil {
		log.Printf("bootstrap admin failed: %v", err)
		return "", "", false
	}
	return "admin", pw, true
}

func randomPassword(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open a browser (%v); visit %s", err, url)
	}
}

func printBanner(url, dbPath, user, pass string, bootstrapped bool) {
	width := 58
	line := func(s string) { fmt.Printf("  │ %-*s │\n", width, s) }
	border := "  ┌" + strings.Repeat("─", width+2) + "┐"
	bottom := "  └" + strings.Repeat("─", width+2) + "┘"

	fmt.Println()
	fmt.Println(border)
	line("imchat is running")
	line("")
	line("open    " + url)
	line("data    " + dbPath)
	if bootstrapped {
		line("")
		line("first-run account (shown once):")
		line("  user  " + user)
		line("  pass  " + pass)
		line("")
		line("change it after logging in; it is stored hashed")
	}
	fmt.Println(bottom)
	fmt.Println()
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
	log.Printf("seeded demo accounts: alice/bob (password123), conversation %d", c.ID)
}
