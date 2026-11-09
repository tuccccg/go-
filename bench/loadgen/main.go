// Command loadgen is a minimal HTTP load generator used by bench/run.ps1.
//
// It only depends on the standard library, so benchmark runs never depend on
// a third-party tool version. In -stream mode it measures time-to-first-byte
// (TTFT) separately from total response time, which is the only meaningful way
// to describe SSE latency.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"
)

type sample struct {
	totals []time.Duration
	ttfts  []time.Duration
	errors int64
}

func main() {
	target := flag.String("url", "http://127.0.0.1:8080/v1/chat/completions", "target URL")
	method := flag.String("method", http.MethodPost, "HTTP method")
	body := flag.String("body", `{"model":"mock-model","messages":[{"role":"user","content":"hello"}]}`, "request body")
	token := flag.String("token", "", "sent as Authorization: Bearer <token> when non-empty")
	concurrency := flag.Int("c", 50, "number of concurrent workers")
	duration := flag.Duration("z", 30*time.Second, "run duration")
	timeout := flag.Duration("timeout", 30*time.Second, "per-request timeout")
	stream := flag.Bool("stream", false, "set stream=true in the body and measure TTFT")
	flag.Parse()

	if *concurrency <= 0 || *duration <= 0 {
		fmt.Println("concurrency and duration must be positive")
		return
	}

	payload, err := buildPayload(*body, *stream)
	if err != nil {
		fmt.Printf("invalid -body JSON: %v\n", err)
		return
	}

	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			DisableCompression:  true,
		},
	}

	deadline := time.Now().Add(*duration)
	results := make([]*sample, *concurrency)
	var wg sync.WaitGroup
	startedAt := time.Now()

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := &sample{
				totals: make([]time.Duration, 0, 4096),
				ttfts:  make([]time.Duration, 0, 4096),
			}
			results[idx] = s
			for time.Now().Before(deadline) {
				ttft, total, status, err := one(client, *method, *target, payload, *token, *stream)
				if err != nil {
					s.errors++
					continue
				}
				s.totals = append(s.totals, total)
				if *stream {
					s.ttfts = append(s.ttfts, ttft)
				}
				if status >= 400 {
					s.errors++
				}
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(startedAt)

	var totals, ttfts []time.Duration
	var failures int64
	for _, s := range results {
		if s == nil {
			continue
		}
		totals = append(totals, s.totals...)
		ttfts = append(ttfts, s.ttfts...)
		failures += s.errors
	}
	sortDurations(totals)
	sortDurations(ttfts)

	total := len(totals)
	if total == 0 {
		fmt.Printf("no successful requests in %s (errors=%d)\n", elapsed.Round(time.Millisecond), failures)
		return
	}

	rps := float64(total) / elapsed.Seconds()
	avg := average(totals)

	if *stream {
		fmt.Printf("stream=on  concurrency=%d  requests=%d  rps=%.1f  ttft_p50=%.2fms  ttft_p95=%.2fms  ttft_p99=%.2fms  total_p50=%.2fms  total_p95=%.2fms  errors=%d\n",
			*concurrency, total, rps,
			ms(percentile(ttfts, 50)), ms(percentile(ttfts, 95)), ms(percentile(ttfts, 99)),
			ms(percentile(totals, 50)), ms(percentile(totals, 95)), failures)
		fmt.Printf("RESULT {\"mode\":\"stream\",\"concurrency\":%d,\"requests\":%d,\"rps\":%.1f,\"ttft_p50_ms\":%.2f,\"ttft_p95_ms\":%.2f,\"ttft_p99_ms\":%.2f,\"total_p50_ms\":%.2f,\"total_p95_ms\":%.2f,\"errors\":%d,\"duration_s\":%.1f}\n",
			*concurrency, total, rps,
			ms(percentile(ttfts, 50)), ms(percentile(ttfts, 95)), ms(percentile(ttfts, 99)),
			ms(percentile(totals, 50)), ms(percentile(totals, 95)), failures, elapsed.Seconds())
		return
	}

	fmt.Printf("concurrency=%d  requests=%d  rps=%.1f  avg=%.2fms  p50=%.2fms  p95=%.2fms  p99=%.2fms  errors=%d\n",
		*concurrency, total, rps, ms(avg), ms(percentile(totals, 50)), ms(percentile(totals, 95)), ms(percentile(totals, 99)), failures)
	fmt.Printf("RESULT {\"mode\":\"json\",\"concurrency\":%d,\"requests\":%d,\"rps\":%.1f,\"avg_ms\":%.2f,\"p50_ms\":%.2f,\"p95_ms\":%.2f,\"p99_ms\":%.2f,\"errors\":%d,\"duration_s\":%.1f}\n",
		*concurrency, total, rps, ms(avg), ms(percentile(totals, 50)), ms(percentile(totals, 95)), ms(percentile(totals, 99)), failures, elapsed.Seconds())
}

// one performs a single request and returns TTFT (stream mode only) and total duration.
func one(client *http.Client, method, target string, payload []byte, token string, stream bool) (time.Duration, time.Duration, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	begin := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()

	if !stream {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, time.Since(begin), resp.StatusCode, nil
	}

	var ttft time.Duration
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 && ttft == 0 {
			ttft = time.Since(begin)
		}
		if readErr != nil {
			break
		}
	}
	return ttft, time.Since(begin), resp.StatusCode, nil
}

func buildPayload(body string, stream bool) ([]byte, error) {
	if !stream {
		return []byte(body), nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return nil, err
	}
	decoded["stream"] = true
	return json.Marshal(decoded)
}

func sortDurations(values []time.Duration) {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
}

func average(values []time.Duration) time.Duration {
	var sum time.Duration
	for _, v := range values {
		sum += v
	}
	if len(values) == 0 {
		return 0
	}
	return sum / time.Duration(len(values))
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func ms(d time.Duration) float64 {
	return math.Round(float64(d.Microseconds())/10) / 100
}
