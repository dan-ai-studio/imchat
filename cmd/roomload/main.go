package main

// roomload measures the fan-out side of a room: how many subscriber
// connections one process can hold, and how long a broadcast takes to reach
// them. It is a measuring tool, not a promise — the numbers it prints are what
// the machine actually did.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// main logs in, opens a room, dials the requested number of subscriber
// connections and measures how long a broadcast takes to reach them.
func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "server base url")
	user := flag.String("user", "alice", "account used for every subscriber connection")
	pass := flag.String("pass", "password123", "password for that account")
	community := flag.Int64("community", 1, "community id")
	// subs 是这次要开的连接数；senders 与 rounds 决定扇出的消息量。
	subs := flag.Int("subs", 1000, "subscriber connections")
	senders := flag.Int("senders", 1, "concurrent senders")
	rounds := flag.Int("rounds", 5, "messages per sender")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 先登录：后面建房、进场、发送都用同一个 cookie。
	client, err := login(*base, *user, *pass)
	if err != nil {
		log.Fatalf("login: %v", err)
	}
	roomID, err := createRoom(client, *base, *community, "压测房间")
	if err != nil {
		log.Fatalf("create room: %v", err)
	}
	if err := enterRoom(client, *base, roomID); err != nil {
		log.Fatalf("enter room: %v", err)
	}

	// 每个订阅者一条连接，全部挂在同一个账号下（多端）。收到广播就记一次时间。
	var received int64
	var mu sync.Mutex
	arrivals := make(map[int64][]int64)
	dialStart := time.Now()
	// conns 与 connMu 是建连结果的收集处：并发 goroutine 只往这里追加。
	var wg sync.WaitGroup
	conns := make([]*websocket.Conn, 0, *subs)
	var connMu sync.Mutex
	wsBase := strings.Replace(*base, "http://", "ws://", 1)
	wsBase = strings.Replace(wsBase, "https://", "wss://", 1)
	// 分批建连：一次性并发几百条会把本机端口和 accept 队列同时打满，
	// 失败的那部分会污染「能不能承载」这个结论。
	batch := 100
	for i := 0; i < *subs; i++ {
		if i > 0 && i%batch == 0 {
			wg.Wait()
			time.Sleep(100 * time.Millisecond)
		}
		// 每条连接一个 goroutine；建连本身不阻塞主循环。
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, err := websocket.Dial(ctx, wsBase+"/ws", &websocket.DialOptions{HTTPClient: client})
			// 建连失败直接放弃这一条：压测关心的是成功率，不是重试。
			if err != nil {
				return
			}
			connMu.Lock()
			conns = append(conns, c)
			connMu.Unlock()
			// 每条连接一个读循环：读到 msg 帧就按 seq 记下到达时刻。
			go func() {
				for {
					_, data, err := c.Read(ctx)
					if err != nil {
						return
					}
					// 只认 msg 帧：ready / pong / read 这些不影响扇出测量。
					var f struct {
						Type string `json:"type"`
						Seq  int64  `json:"seq"`
					}
					if json.Unmarshal(data, &f) != nil || f.Type != "msg" {
						continue
					}
					// 原子计数只用于总量；分位统计走下面的加锁样本表。
					atomic.AddInt64(&received, 1)
					mu.Lock()
					arrivals[f.Seq] = append(arrivals[f.Seq], time.Now().UnixMilli())
					mu.Unlock()
				}
			}()
		}()
	}
	wg.Wait()
	dialDur := time.Since(dialStart)
	fmt.Printf("connections: %d (want %d) in %s\n", len(conns), *subs, dialDur.Round(time.Millisecond))

	// 发送与接收分开记时：延迟是「发出到某条连接读到」的差。
	sent := make(map[int64]int64)
	for r := 0; r < *rounds; r++ {
		var sg sync.WaitGroup
		for s := 0; s < *senders; s++ {
			// 每个发送者一个 goroutine：扇出压力来自并发写，不是串行写。
			sg.Add(1)
			go func(n int) {
				defer sg.Done()
				seq, err := send(client, *base, roomID, fmt.Sprintf("m-%d-%d", r, n))
				if err != nil {
					return
				}
				// 记下发时刻：到达时刻与它的差就是这条消息的延迟。
				mu.Lock()
				sent[seq] = time.Now().UnixMilli()
				mu.Unlock()
			}(s)
		}
		sg.Wait()
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)

	// 把所有到达时刻与发出时刻配对，摊平成一个延迟样本集。
	var lat []int64
	mu.Lock()
	for seq, at := range sent {
		for _, t := range arrivals[seq] {
			lat = append(lat, t-at)
		}
	}
	// 先取快照再解锁：统计阶段不再碰共享状态。
	total := received
	nSent := len(sent)
	nConns := len(conns)
	mu.Unlock()
	// 排序后按位取分位：p99 比平均值更能说明体验。
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pick := func(p int) int64 {
		if len(lat) == 0 {
			return 0
		}
		i := len(lat) * p / 100
		if i >= len(lat) {
			i = len(lat) - 1
		}
		return lat[i]
	}
	// 期望投递数 = 消息数 × 连接数；差值就是丢掉的扇出。
	fmt.Printf("messages: %d, deliveries: %d (expected %d)\n", nSent, total, nSent*nConns)
	fmt.Printf("latency ms: p50=%d p95=%d p99=%d\n", pick(50), pick(95), pick(99))
	// 收尾：显式关连接，避免服务端把这次测量算进下一次。
	for _, c := range conns {
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
	if nConns == 0 {
		log.Fatalf("no connection survived")
	}
}

// login returns a client whose jar carries the session cookie.
func login(base, user, pass string) (*http.Client, error) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	// 登录只要 cookie：后面的 WS 连接复用同一个 client 的 jar。
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass)
	res, err := c.Post(base+"/api/login", "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("login status %d", res.StatusCode)
	}
	return c, nil
}

// createRoom opens a fresh room so the measurement never mixes with old traffic.
func createRoom(c *http.Client, base string, community int64, title string) (int64, error) {
	// 每次压测都开一间新房：不与旧消息混在一起。
	body := fmt.Sprintf(`{"title":%q}`, title)
	res, err := c.Post(fmt.Sprintf("%s/api/communities/%d/rooms", base, community), "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	// 只取 id：后面进场与发送都要用它。
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// enterRoom puts the measuring account on the presence list: broadcast only
// reaches people who are present.
func enterRoom(c *http.Client, base string, roomID int64) error {
	res, err := c.Post(fmt.Sprintf("%s/api/rooms/%d/enter", base, roomID), "application/json", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return nil
}

// send posts one message and returns its sequence number, which is the key the
// arrival times are bucketed by.
func send(c *http.Client, base string, roomID int64, body string) (int64, error) {
	// client_msg_id 必须唯一：重复的 id 会被幂等去重，压测就测不到扇出了。
	payload := fmt.Sprintf(`{"client_msg_id":%q,"kind":"text","body":%q}`, fmt.Sprintf("%s-%d", body, time.Now().UnixNano()), body)
	res, err := c.Post(fmt.Sprintf("%s/api/conversations/%d/send", base, roomID), "application/json", strings.NewReader(payload))
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	// seq 是这条消息的唯一标识：到达时刻按它归档。
	var out struct {
		Seq int64 `json:"seq"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Seq, nil
}
