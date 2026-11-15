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

func main() {
	base := flag.String("base", "http://127.0.0.1:18095", "server base url")
	user := flag.String("user", "alice", "username")
	pass := flag.String("pass", "password123", "password")
	conv := flag.Int64("conv", 1, "conversation id")
	workers := flag.Int("workers", 8, "concurrent writers")
	perWorker := flag.Int("per", 25, "messages per writer")
	flag.Parse()

	client := &http.Client{Timeout: 15 * time.Second}

	cred, _ := json.Marshal(map[string]string{"username": *user, "password": *pass})
	res, err := client.Post(*base+"/api/login", "application/json", bytes.NewReader(cred))
	if err != nil {
		log.Fatalf("login: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "imchat_session" {
			cookie = c
		}
	}
	res.Body.Close()
	if cookie == nil {
		log.Fatal("login: no session cookie")
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		ok     int
		failed int
		lat    []time.Duration
	)

	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < *perWorker; i++ {
				payload, _ := json.Marshal(map[string]any{
					"client_msg_id": fmt.Sprintf("load-%d-%d-%d", w, i, time.Now().UnixNano()),
					"kind":          "text",
					"body":          fmt.Sprintf("load message %d/%d", w, i),
				})
				req, _ := http.NewRequest("POST",
					fmt.Sprintf("%s/api/conversations/%d/send", *base, *conv),
					bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(cookie)

				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)

				mu.Lock()
				if err != nil || resp.StatusCode != 201 {
					failed++
				} else {
					ok++
					lat = append(lat, d)
				}
				mu.Unlock()
				if resp != nil {
					resp.Body.Close()
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pick := func(p int) int64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[len(lat)*p/100].Milliseconds()
	}

	out := map[string]any{
		"attempted":          ok + failed,
		"ok":                 ok,
		"failed":             failed,
		"elapsed_ms":         elapsed.Milliseconds(),
		"throughput_per_sec": float64(ok) / elapsed.Seconds(),
		"p50_ms":             pick(50),
		"p95_ms":             pick(95),
		"p99_ms":             pick(99),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
