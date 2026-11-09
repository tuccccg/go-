// Command mockupstream is an OpenAI-compatible fake upstream used by bench/run.ps1.
//
// It exists so benchmarks measure the gateway instead of a model provider.
// A Go mock is used rather than a Python one because a Python HTTP server
// becomes the bottleneck around a few thousand requests per second and starts
// returning connection errors, which would be misread as gateway failures.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "listen address")
	firstChunkDelay := flag.Duration("first-chunk-delay", 0,
		"sleep before the first SSE chunk, emulating model time-to-first-token")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat(*firstChunkDelay))
	mux.HandleFunc("/v1/embeddings", handleEmbeddings)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("mock upstream listening on http://%s/v1", *addr)
	log.Fatal(srv.ListenAndServe())
}

func handleChat(firstChunkDelay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = r.Body.Close()

		if body.Stream {
			writeSSE(w, firstChunkDelay)
			return
		}
		writeJSON(w, map[string]any{
			"id":     "chatcmpl-mock",
			"object": "chat.completion",
			"model":  modelOr(body.Model),
			"choices": []any{
				map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "pong"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	}
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	_ = r.Body.Close()

	vector := make([]float64, 8)
	for i := range vector {
		vector[i] = float64(i) * 0.01
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"model":  modelOr(body.Model),
		"data": []any{
			map[string]any{"object": "embedding", "index": 0, "embedding": vector},
		},
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":"mock encode failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", itoa(len(data)))
	_, _ = w.Write(data)
}

func writeSSE(w http.ResponseWriter, firstChunkDelay time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if firstChunkDelay > 0 {
		time.Sleep(firstChunkDelay)
	}

	for i := 0; i < 5; i++ {
		chunk := map[string]any{
			"id":     "chatcmpl-mock",
			"object": "chat.completion.chunk",
			"choices": []any{
				map[string]any{"index": 0, "delta": map[string]any{"content": "tok"}},
			},
		}
		data, _ := json.Marshal(chunk)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func modelOr(model string) string {
	if model == "" {
		return "mock-model"
	}
	return model
}

// itoa avoids pulling in strconv solely for a Content-Length header.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for n > 0 {
		pos--
		digits[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[pos:])
}
