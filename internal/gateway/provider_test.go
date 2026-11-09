package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestParseProviders(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []Provider
		wantErr bool
	}{
		{
			name: "parses multiple providers and trims trailing slash",
			raw:  "a=https://a.example.com/v1/|sk-a|gpt-4o-mini, b=https://b.example.com/v1|sk-b|qwen-max",
			want: []Provider{
				{Name: "a", BaseURL: "https://a.example.com/v1", APIKey: "sk-a", DefaultModel: "gpt-4o-mini"},
				{Name: "b", BaseURL: "https://b.example.com/v1", APIKey: "sk-b", DefaultModel: "qwen-max"},
			},
		},
		{
			name: "skips empty segments and surrounding spaces",
			raw:  " ,  a=https://a.example.com/v1|sk|m ",
			want: []Provider{{Name: "a", BaseURL: "https://a.example.com/v1", APIKey: "sk", DefaultModel: "m"}},
		},
		{
			name: "empty config yields no providers",
			raw:  "   ",
			want: nil,
		},
		{
			name:    "rejects entry without equals sign",
			raw:     "https://a.example.com/v1|sk|m",
			wantErr: true,
		},
		{
			name:    "rejects entry without a model",
			raw:     "a=https://a.example.com/v1|sk",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseProviders(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseProviders(%q) = %+v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProviders(%q) unexpected error: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseProviders(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func chat(t *testing.T, gw *Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	gw.ChatHandler()(c)
	return w
}

func TestChatFailsOverToHealthyProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	var hits []string
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		hits = append(hits, name)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("broken")
		http.Error(w, `{"error":"upstream down"}`, http.StatusServiceUnavailable)
	}))
	defer broken.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("healthy")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer healthy.Close()

	gw := New([]Provider{
		{Name: "broken", BaseURL: broken.URL, APIKey: "k1", DefaultModel: "m1"},
		{Name: "healthy", BaseURL: healthy.URL, APIKey: "k2", DefaultModel: "m2"},
	}, Options{Timeout: 5 * time.Second, MaxAttempts: 2})

	w := chat(t, gw, `{"messages":[{"role":"user","content":"ping"}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pong") {
		t.Fatalf("body = %s, want the healthy provider response", w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"broken", "healthy"}; !reflect.DeepEqual(hits, want) {
		t.Fatalf("upstream calls = %v, want %v", hits, want)
	}
}

func TestChatDoesNotFailOverOnClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var secondCalled bool
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"context_length_exceeded"}`))
	}))
	defer rejecting.Close()

	spare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalled = true
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer spare.Close()

	gw := New([]Provider{
		{Name: "rejecting", BaseURL: rejecting.URL, APIKey: "k1", DefaultModel: "m1"},
		{Name: "spare", BaseURL: spare.URL, APIKey: "k2", DefaultModel: "m2"},
	}, Options{Timeout: 5 * time.Second, MaxAttempts: 2})

	w := chat(t, gw, `{"messages":[{"role":"user","content":"ping"}]}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 passed through from upstream", w.Code)
	}
	if !strings.Contains(w.Body.String(), "context_length_exceeded") {
		t.Fatalf("body = %s, want the upstream error payload", w.Body.String())
	}
	if secondCalled {
		t.Fatal("a 4xx from the first provider must not trigger failover")
	}
}

// memStore is an in-memory cache.Store used to assert cache gating rules.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
	sets int
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value, ok := m.data[key]; ok {
		return value, nil
	}
	return nil, errors.New("cache miss")
}

func (m *memStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = value
	m.sets++
	return nil
}

func TestChatCachesOnlyDeterministicRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		upstreamCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"cached"}}]}`))
	}))
	defer upstream.Close()

	providers := []Provider{{Name: "p", BaseURL: upstream.URL, APIKey: "k", DefaultModel: "m"}}
	newGateway := func() (*Gateway, *memStore) {
		store := &memStore{}
		gw := New(providers, Options{Timeout: 5 * time.Second, MaxAttempts: 1})
		gw.SetCache(store, time.Minute)
		return gw, store
	}

	t.Run("temperature=0 is cached", func(t *testing.T) {
		gw, store := newGateway()
		body := `{"messages":[{"role":"user","content":"ping"}],"temperature":0}`

		first := chat(t, gw, body)
		second := chat(t, gw, body)

		if first.Code != http.StatusOK || second.Code != http.StatusOK {
			t.Fatalf("status = %d/%d, want 200/200", first.Code, second.Code)
		}
		if hit := second.Header().Get("X-Cache"); hit != "HIT" {
			t.Fatalf("X-Cache = %q, want HIT on the second call", hit)
		}
		mu.Lock()
		calls := upstreamCalls
		mu.Unlock()
		if calls != 1 {
			t.Fatalf("upstream calls = %d, want 1", calls)
		}
		if store.sets != 1 {
			t.Fatalf("cache sets = %d, want 1", store.sets)
		}
	})

	t.Run("sampling requests are not cached", func(t *testing.T) {
		mu.Lock()
		upstreamCalls = 0
		mu.Unlock()

		gw, store := newGateway()
		body := `{"messages":[{"role":"user","content":"ping"}],"temperature":0.7}`

		first := chat(t, gw, body)
		second := chat(t, gw, body)

		if first.Code != http.StatusOK || second.Code != http.StatusOK {
			t.Fatalf("status = %d/%d, want 200/200", first.Code, second.Code)
		}
		if hit := first.Header().Get("X-Cache"); hit != "" {
			t.Fatalf("X-Cache = %q, want empty - sampling requests must bypass the cache", hit)
		}
		if hit := second.Header().Get("X-Cache"); hit != "" {
			t.Fatalf("X-Cache = %q, want empty for a sampling request", hit)
		}
		mu.Lock()
		calls := upstreamCalls
		mu.Unlock()
		if calls != 2 {
			t.Fatalf("upstream calls = %d, want 2", calls)
		}
		if store.sets != 0 {
			t.Fatalf("cache sets = %d, want 0", store.sets)
		}
	})
}
