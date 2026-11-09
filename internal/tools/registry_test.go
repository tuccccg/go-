package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestRouter(reg *Registry) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/tools/:name", reg.Handler())
	return r
}

func callTool(t *testing.T, r *gin.Engine, name, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tools/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandlerValidatesArguments(t *testing.T) {
	reg := NewRegistry(8)
	reg.Register(Definition{
		Name:     "echo",
		Required: []string{"message"},
		Allowed:  map[string]bool{"message": true},
		Run: func(_ map[string]any, input map[string]any) (any, error) {
			return input["message"], nil
		},
	})
	router := newTestRouter(reg)

	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{name: "missing required argument", body: `{}`, wantMsg: "missing required argument: message"},
		{name: "unexpected argument", body: `{"message":"hi","debug":true}`, wantMsg: "unexpected argument: debug"},
		{name: "body is not a JSON object", body: `["hi"]`, wantMsg: "body must be a JSON object"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := callTool(t, router, "echo", tc.body, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantMsg) {
				t.Fatalf("body = %s, want it to contain %q", w.Body.String(), tc.wantMsg)
			}
		})
	}

	if w := callTool(t, router, "missing", `{}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown tool status = %d, want 404", w.Code)
	}
}

func TestHandlerReplaysIdempotentCalls(t *testing.T) {
	reg := NewRegistry(8)
	runs := 0
	reg.Register(Definition{
		Name:     "charge",
		Required: []string{"amount"},
		Allowed:  map[string]bool{"amount": true},
		Run: func(_ map[string]any, input map[string]any) (any, error) {
			runs++
			return map[string]any{"amount": input["amount"], "runs": runs}, nil
		},
	})
	router := newTestRouter(reg)
	headers := map[string]string{"Idempotency-Key": "order-42"}

	first := callTool(t, router, "charge", `{"amount":10}`, headers)
	second := callTool(t, router, "charge", `{"amount":10}`, headers)

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status = %d/%d, want 200/200", first.Code, second.Code)
	}
	if runs != 1 {
		t.Fatalf("tool ran %d times, want 1 for a repeated Idempotency-Key", runs)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay body = %s, want %s", second.Body.String(), first.Body.String())
	}
}

func TestHandlerEnforcesPerRunCallLimit(t *testing.T) {
	reg := NewRegistry(1)
	reg.Register(Definition{
		Name:    "ping",
		Allowed: map[string]bool{},
		Run: func(_ map[string]any, _ map[string]any) (any, error) {
			return "pong", nil
		},
	})
	router := newTestRouter(reg)

	first := callTool(t, router, "ping", `{}`, map[string]string{"Idempotency-Key": "a"})
	if first.Code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", first.Code)
	}

	second := callTool(t, router, "ping", `{}`, map[string]string{"Idempotency-Key": "b"})
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429 once the per-run budget is spent", second.Code)
	}
}
