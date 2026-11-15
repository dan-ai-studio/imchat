package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// main logs in, then drives N workers that each POST one send request per
// iteration, and reports throughput plus latency percentiles. It measures; it
// never asserts — the assertions live in the store tests.
func main() {
	base := flag.String("base", "http://127.0.0.1:18095", "server base url")
	user := flag.String("user", "alice", "username")
	pass := flag.String("pass", "password123", "password")
	conv := flag.Int64("conv", 1, "conversation id")
	workers := flag.Int("workers", 8, "concurrent writers")
	perWorker := flag.Int("per", 25, "messages per writer")
	flag.Parse()

	// 先登录拿会话，再并发打发送接口；单个请求失败只记数，不中断整轮。
	client := &http.Client{Timeout: 15 * time.Second}

	cred, _ := json.Marshal(map[string]string{"username": *user, "password": *pass})
	// 登录：之后每个请求都要带这张 cookie。
	res, err := client.Post(*base+"/api/login", "application/json", bytes.NewReader(cred))
	if err != nil {
		log.Fatalf("login: %v", err)
	}
	// 从 Set-Cookie 里挑出会话那一张；名字是硬编码的，因为它就是协议的一部分。
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "imchat_session" {
			cookie = c
		}
	}
	// 关掉响应体再判空：没有会话 cookie 就没法继续。
	res.Body.Close()
	if cookie == nil {
		log.Fatal("login: no session cookie")
	}

	var (
		// 四个计数：等在途请求、保护计数器、成功数、失败数与每条延迟。
		wg     sync.WaitGroup
		mu     sync.Mutex
		ok     int
		failed int
		// 每条请求的耗时都留下，最后算分位数。
		lat []time.Duration
	)

	// 起 N 个 worker，每个发 perWorker 条；延迟各自量、最后汇总。
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		// 每个 worker 一个 goroutine；失败计数走 mutex，不在 goroutine 里 Fatal。
		go func(w int) {
			defer wg.Done()
			for i := 0; i < *perWorker; i++ {
				payload, _ := json.Marshal(map[string]any{
					"client_msg_id": fmt.Sprintf("load-%d-%d-%d", w, i, time.Now().UnixNano()),
					"kind":          "text",
					"body":          fmt.Sprintf("load message %d/%d", w, i),
				})
				// 每个请求都带会话 cookie；正文里 client_msg_id 唯一，重跑也不会撞幂等键。
				req, _ := http.NewRequest("POST",
					fmt.Sprintf("%s/api/conversations/%d/send", *base, *conv),
					bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(cookie)

				// 只量这一次调用本身的耗时；排队与连接复用都算在里面。
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)

				// 计数与延迟都在锁内更新：worker 之间不能互相踩。
				mu.Lock()
				if err != nil || resp.StatusCode != 201 {
					failed++
				} else {
					ok++
					lat = append(lat, d)
				}
				mu.Unlock()
				// 请求失败时 resp 是 nil，直接关会崩。
				if resp != nil {
					resp.Body.Close()
				}
			}
		}(w)
	}
	// 所有 worker 收工后再结算：否则会读到写了一半的计数。
	wg.Wait()
	elapsed := time.Since(start)

	// 排序之后按位取分位数：p50 / p95 / p99 比平均值更能说明体验。
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	// 取分位数的小工具：位点算出来，换算成毫秒。
	pick := func(p int) int64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[len(lat)*p/100].Milliseconds()
	}

	// 输出一个 JSON 对象：这个程序是测量器，输出要能被别的脚本直接消费。
	out := map[string]any{
		"attempted":          ok + failed,
		"ok":                 ok,
		"failed":             failed,
		"elapsed_ms":         elapsed.Milliseconds(),
		"throughput_per_sec": float64(ok) / elapsed.Seconds(),
		"p50_ms":             pick(50),
		"p95_ms":             pick(95),
		"p99_ms":             pick(99), // 最慢的 1% 决定体感，比平均值更值得盯
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
