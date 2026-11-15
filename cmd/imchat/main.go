package main

// ── 阅读导览 · 第 1 站 ────────────────────────────────────
// 上一站：（起点，这一站就是入口）
// 下一站：站2 一条连接
//
// 站序与「这一站为什么在这一站」见 docs/READING.md。

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

// main wires the process together, then decides whether it is a server or a
// one-shot maintenance command. Shutdown is the startup order reversed.
func main() {
	// 参数分两类：常驻服务用的（-addr / -db / -domain / -open），
	// 与一次性的运维命令（-backup / -create-user）。同一个二进制两种用法，
	// 于是运维不需要额外的 sqlite3 CLI，也不需要单独的迁移工具。
	addr := flag.String("addr", "127.0.0.1:8080", "listen address when no domain is given")
	dbPath := flag.String("db", filepath.Join("data", "imchat.db"), "sqlite file path")
	domain := flag.String("domain", "", "public hostname; turns on built-in HTTPS via Let's Encrypt")
	acmeHTTP := flag.String("acme-http", ":80", "address for the ACME HTTP-01 challenge and the http→https redirect")
	httpsAddr := flag.String("https", ":443", "address for HTTPS when -domain is set")
	openBrowserFlag := flag.Bool("open", true, "open a browser on startup in local mode")
	seed := flag.Bool("seed", false, "create demo users alice/bob; local demos and tests only")
	// 下面这几个是一次性运维命令：给完就退出，不进入服务状态。
	backupTo := flag.String("backup", "", "write a consistent snapshot to this path, then exit")
	createUser := flag.String("create-user", "", "create this account, then exit")
	password := flag.String("password", "", "password for -create-user")
	admin := flag.Bool("admin", false, "grant platform admin to -create-user")
	flag.Parse()

	// 先把数据目录建出来：SQLite 不会替我们建目录，它只会告诉你文件打不开。
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// 打开数据库文件。注意此刻还没有建表——建表交给下面的 Migrate，
	// 两步分开是为了让旧库也能升级，而不是一上来就假定库是新的。
	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// 一根能被取消的 context。后面所有长期循环（投递器、监听、后台任务）
	// 都挂在这根线上，关机的第一步就是把线剪断。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 建表或升级到当前版本。这一步是幂等的，每次启动都跑。
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

	// 会话管理器，7 天后过期。它握着 db，所以会话是落库的，不是签在 token 里的——
	// 这才可能在封禁时立刻让某个人的下一次请求变成未授权。
	am := auth.NewManager(db, 7*24*time.Hour)

	if *createUser != "" {
		if *password == "" {
			log.Fatal("-password is required with -create-user")
		}
		// 命令行建号，密码由调用者给：脚本化部署要的就是这个入口。
		u, err := am.Register(ctx, *createUser, *createUser, *password, *admin)
		if err != nil {
			log.Fatalf("create user: %v", err)
		}
		log.Printf("created user %q (admin=%v)", u.Username, u.IsAdmin)
		return
	}

	// 进程内的消息总线，只在内存里，重启即丢。丢的是缓存，不是事实：
	// 事实在 SQLite 的待投递记录里，重启后会被重新投出去。
	b, err := bus.New()
	if err != nil {
		log.Fatalf("start bus: %v", err)
	}
	defer b.Close()

	// 连接中心：每个终端一条连接。接下来把它挂到总线上，扇出这条线就通了。
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

	// 图片落盘目录与数据库同级，跟着数据文件走，备份时不会漏掉。
	filesDir := filepath.Join(filepath.Dir(*dbPath), "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		log.Fatalf("create files dir: %v", err)
	}

	// 装配顺序是有依赖的：存储 → 总线 → 连接中心 → HTTP 层 → 投递器。
	// 后面每一块都要拿前面几块，所以顺序不能调换。
	srvAPI := &api.Server{DB: db, Auth: am, Hub: hub, FilesDir: filesDir, Bus: b}
	disp := delivery.New(db, b)
	srvAPI.Dispatcher = disp
	// 投递器在后台每 100 毫秒扫一次到期记录。它的命挂在 ctx 上，剪断就停。
	go disp.Run(ctx)

	// Bootstrap: an empty database gets one admin account with a generated
	// password, printed once. Demo accounts are opt-in via -seed.
	adminUser, adminPass, bootstrapped := "", "", false
	if *seed {
		seedDemo(ctx, am, db)
	} else {
		adminUser, adminPass, bootstrapped = ensureAdmin(ctx, am, db)
	}

	// 路由规则一句话：/ws、/healthz、/api/、/files/ 交给后端，其余交给内嵌前端。
	handler := assemble(srvAPI.Router())

	// Browser-facing URL, used for the banner and the auto-open.
	// 拼一个给人看的地址。本地模式把 127.0.0.1 换成 localhost：
	// 浏览器开在 127.0.0.1 上没问题，但复制给别人就没用了。
	scheme, host := "http", *addr
	if *domain != "" {
		scheme, host = "https", *domain
	}
	publicURL := scheme + "://" + host
	if *domain == "" && strings.HasPrefix(host, "127.0.0.1:") {
		publicURL = "http://localhost" + strings.TrimPrefix(host, "127.0.0.1")
	}

	// 真正对外服务的那个 HTTP server。超时必须显式设，零值意味着一直等下去。
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	// 两种形态只差一层壳：给了 -domain 就自己申请证书并开 HTTPS，
	// 否则裸跑 HTTP。下面的分支就是这个二选一。
	var acmeSrv *http.Server
	if *domain != "" {
		// 证书缓存放在数据目录旁边，和数据库一起被备份、一起被搬走。
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
			// 80 端口只干两件事：回答 ACME 的挑战，其余请求一律 301 到 HTTPS。
			log.Printf("acme: listening on %s for HTTP-01 and redirects", *acmeHTTP)
			if err := acmeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("acme listener stopped: %v", err)
			}
		}()

		// 443 端口用 autocert 现取证书；TLS 下限钉在 1.2，再低就不该谈了。
		srv.Addr = *httpsAddr
		srv.TLSConfig = &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		go func() {
			// 监听放进 goroutine，主 goroutine 留着等信号——这是「谁负责退出」的分工：
			// 监听只负责报错，退出由信号那一边决定。
			log.Printf("imchat listening on https://%s", *domain)
			if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("https: %v", err)
			}
		}()
	} else {
		// 本地 / 内网形态：直接裸 HTTP，没有证书，也没有重定向。
		srv.Addr = *addr
		go func() {
			// 同上：监听不负责退出。
			log.Printf("imchat listening on http://%s", *addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("http: %v", err)
			}
		}()
	}

	// 横幅是这个人第一次见到系统的唯一机会：地址、数据文件、一次性密码都印在这里。
	printBanner(publicURL, *dbPath, adminUser, adminPass, bootstrapped)

	// 本地模式自动开浏览器；公网模式不开，因为那个地址不是给这台机器看的。
	if *openBrowserFlag && *domain == "" {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openURL(publicURL)
		}()
	}

	// 从这里往下，主 goroutine 只做一件事：等一个退出信号。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	cancel()
	// 关闭顺序是启动顺序倒过来走：
	// 先让后台停止接新活（剪断 ctx，投递器退出），再让 HTTP 层排空在途请求，
	// 最后才轮到 broker 与数据库（靠 defer 的先进后出收尾）。
	// 反过来做会丢东西：请求还在路上，数据库已经关了——这就是「正序会丢什么」的答案。
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if acmeSrv != nil {
		_ = acmeSrv.Shutdown(shutdownCtx)
	}
	// 走到这里，要么在途请求已经排空，要么 5 秒超时放弃了。
	log.Print("imchat stopped")
}

// redirectToHTTPS sends every request that is not an ACME challenge to its
// https:// twin. The challenge itself is already answered before we get here.
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

// randomPassword draws from an alphabet with no look-alike characters (no I, l,
// O or 0). The result is read off a terminal and typed once, by a human.
func randomPassword(n int) string {
	// 去掉 I/l/O/0 这类形近字符：密码是要人眼读一遍再手打的，读错一个字母就白费。
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	// 用密码学随机源，不用 math/rand：这是账号密码，不是抽奖。
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// openURL asks the platform's default browser to show url. Best effort: a
// failure is a log line, never a reason to refuse to start.
func openURL(url string) {
	// 每个平台开浏览器的姿势都不一样，这里只求尽力而为。
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		// Linux 与其他类 Unix：xdg-open 是桌面环境约定的入口。
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open a browser (%v); visit %s", err, url)
	}
}

// printBanner writes the one screen a first run gets: where to open, which file
// holds the data, and the generated admin password when there is one.
func printBanner(url, dbPath, user, pass string, bootstrapped bool) {
	// 纯文本画框：宽度写死 58，宽了在窄终端会折行，窄了框会歪。
	width := 58
	line := func(s string) { fmt.Printf("  │ %-*s │\n", width, s) }
	border := "  ┌" + strings.Repeat("─", width+2) + "┐"
	bottom := "  └" + strings.Repeat("─", width+2) + "┘"

	fmt.Println()
	fmt.Println(border)
	// 先画框再填行：每一行的对齐由上面那个 line() 统一负责，这里只管内容。
	line("imchat is running")
	line("")
	line("open    " + url)
	line("data    " + dbPath)
	// 只有首次启动才有这一块：密码只在这里出现一次，之后库里只剩它的哈希。
	if bootstrapped {
		line("")
		line("first-run account (shown once):")
		line("  user  " + user)
		line("  pass  " + pass)
		line("")
		// 提醒改密码：这串字符只在屏幕上活过这一次，之后库里只剩哈希。
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
	// 内嵌的前端是一棵静态文件树，交给标准库的 FileServer 直接发。
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 先认路由前缀：这些路径归后端，一个都不能落到静态文件上。
		p := r.URL.Path
		if p == "/ws" || p == "/healthz" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/files/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, strings.TrimPrefix(p, "/")); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		// 既不是接口、也不是真实文件——那是前端自己的路由，回 index.html 让它接管。
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		files.ServeHTTP(w, clone)
	})
}

// seedDemo creates alice, bob and one direct conversation between them. It is
// opt-in (-seed) because a real deployment must never ship known passwords.
func seedDemo(ctx context.Context, am *auth.Manager, db *store.DB) {
	// 幂等：已经有 alice 就什么都不做，重复跑 -seed 不会报错也不会重复建号。
	if _, _, err := db.UserByUsername(ctx, "alice"); err == nil {
		return
	}
	// 两个演示账号，密码写死——所以这条路径只能在演示与测试里走。
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
	// 演示账号要显式进默认社群：注册本身不再自动分配社群。
	// alice 在默认社群里是管理员，这样演示才能走「发邀请码 / 审批」两条路。
	for _, u := range []*store.User{alice, bob} {
		role := "member"
		if u.ID == alice.ID {
			role = "admin"
		}
		if err := db.AddCommunityMember(ctx, store.DefaultCommunityID, u.ID, role); err != nil {
			log.Printf("seed community member %d: %v", u.ID, err)
		}
	}
	// 建一个两人直聊，再把双方都以正确的会话内角色加进去。
	c, err := db.CreateConversation(ctx, "dm", "", alice.ID)
	if err != nil {
		log.Printf("seed conversation: %v", err)
		return
	}
	if err := db.AddMember(ctx, c.ID, alice.ID, "owner"); err != nil {
		log.Printf("seed alice member: %v", err)
	}
	// 两人都用会话内角色：alice 是 owner，bob 是 member。
	// 平台管理员是另一套正交的主体，这里一个都没给。
	if err := db.AddMember(ctx, c.ID, bob.ID, "member"); err != nil {
		log.Printf("seed bob member: %v", err)
	}
	log.Printf("seeded demo accounts: alice/bob (password123), conversation %d", c.ID)
}
