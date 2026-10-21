package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go-agent-service/internal/cache"
)

type Provider struct{ Name, BaseURL, APIKey, DefaultModel string }
type Options struct {
	Timeout     time.Duration
	MaxAttempts int
}
type Gateway struct {
	providers []Provider
	opts      Options
	client    *http.Client
	cache     cache.Store
	cacheTTL  time.Duration
}

func ParseProviders(raw string) ([]Provider, error) {
	var result []Provider
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		pair := strings.SplitN(item, "=", 2)
		if len(pair) != 2 {
			return nil, fmt.Errorf("provider must be name=base_url|api_key|model")
		}
		fields := strings.Split(pair[1], "|")
		if len(fields) != 3 {
			return nil, fmt.Errorf("provider %q must have base_url, api_key, and model", pair[0])
		}
		result = append(result, Provider{Name: pair[0], BaseURL: strings.TrimRight(fields[0], "/"), APIKey: fields[1], DefaultModel: fields[2]})
	}
	return result, nil
}

func New(providers []Provider, opts Options) *Gateway {
	if opts.Timeout <= 0 {
		opts.Timeout = 90 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 2
	}
	return &Gateway{providers: providers, opts: opts, client: &http.Client{Timeout: opts.Timeout}, cacheTTL: 24 * time.Hour}
}

func (g *Gateway) SetCache(store cache.Store, ttl time.Duration) {
	g.cache = store
	if ttl > 0 {
		g.cacheTTL = ttl
	}
}

func (g *Gateway) ChatHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req map[string]any
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request"})
			return
		}
		if _, ok := req["messages"]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "messages is required"})
			return
		}
		if len(g.providers) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no model providers configured"})
			return
		}
		stream, _ := req["stream"].(bool)
		if stream {
			g.stream(c, req)
			return
		}
		g.complete(c, req)
	}
}

func (g *Gateway) EmbeddingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload map[string]any
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request"})
			return
		}
		if payload["input"] == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "input is required"})
			return
		}
		if len(g.providers) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no model providers configured"})
			return
		}
		tenant, _ := c.Get("agent.tenant")
		for _, p := range g.providers {
			body := clonePayload(payload)
			if model, _ := body["model"].(string); model == "" {
				body["model"] = p.DefaultModel
			}
			key := g.cacheKey("embedding", fmt.Sprint(tenant), p.Name, body)
			if data := g.cacheGet(c, key); data != nil {
				c.Header("X-Cache", "HIT")
				c.Data(http.StatusOK, "application/json", data)
				return
			}
			status, data, err := g.callEndpoint(c.Request.Context(), p, "/embeddings", body)
			if err == nil && status < 500 && status != http.StatusTooManyRequests {
				if status >= 200 && status < 300 {
					g.cacheSet(c, key, data)
				}
				c.Data(status, "application/json", data)
				return
			}
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "all model providers failed"})
	}
}

func (g *Gateway) complete(c *gin.Context, payload map[string]any) {
	var last error
	tenant, _ := c.Get("agent.tenant")
	temperature, hasTemperature := payload["temperature"].(float64)
	cacheable := hasTemperature && temperature == 0
	for attempt := 0; attempt < g.opts.MaxAttempts; attempt++ {
		for i, p := range g.providers {
			body := clonePayload(payload)
			if model, _ := body["model"].(string); model == "" {
				body["model"] = p.DefaultModel
			}
			key := g.cacheKey("chat", fmt.Sprint(tenant), p.Name, body)
			if cacheable {
				if data := g.cacheGet(c, key); data != nil {
					c.Header("X-Cache", "HIT")
					c.Data(http.StatusOK, "application/json", data)
					return
				}
			}
			status, data, err := g.call(c.Request.Context(), p, body)
			if err == nil && status < 500 && status != http.StatusTooManyRequests {
				if cacheable && status >= 200 && status < 300 {
					g.cacheSet(c, key, data)
				}
				c.Data(status, "application/json", data)
				return
			}
			if err != nil {
				last = err
			} else {
				last = fmt.Errorf("provider %s returned HTTP %d", p.Name, status)
			}
			if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
				c.Data(status, "application/json", data)
				return
			}
			if i < len(g.providers)-1 {
				continue
			}
		}
		if attempt+1 < g.opts.MaxAttempts {
			select {
			case <-c.Request.Context().Done():
				c.Status(499)
				return
			case <-time.After(time.Duration(1<<attempt) * 150 * time.Millisecond):
			}
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "all model providers failed", "detail": last.Error()})
}

func (g *Gateway) stream(c *gin.Context, payload map[string]any) {
	var last error
	for _, p := range g.providers {
		body := clonePayload(payload)
		if model, _ := body["model"].(string); model == "" {
			body["model"] = p.DefaultModel
		}
		b, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			last = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
		resp, err := g.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			last = fmt.Errorf("provider %s returned HTTP %d", p.Name, resp.StatusCode)
			continue
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			c.Data(resp.StatusCode, "application/json", readLimit(resp.Body, 1<<20))
			return
		}
		defer resp.Body.Close()
		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Status(http.StatusOK)
		type chunk struct {
			data []byte
			err  error
		}
		chunks := make(chan chunk, 1)
		go func() {
			buf := make([]byte, 4096)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					copied := append([]byte(nil), buf[:n]...)
					select {
					case chunks <- chunk{data: copied}:
					case <-c.Request.Context().Done():
						return
					}
				}
				if readErr != nil {
					select {
					case chunks <- chunk{err: readErr}:
					case <-c.Request.Context().Done():
					}
					return
				}
			}
		}()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		c.Stream(func(w io.Writer) bool {
			select {
			case <-c.Request.Context().Done():
				return false
			case <-ticker.C:
				if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
					return false
				}
				flusher.Flush()
				return true
			case item := <-chunks:
				if len(item.data) > 0 {
					if _, err := w.Write(item.data); err != nil {
						return false
					}
					flusher.Flush()
				}
				return item.err == nil
			}
		})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "all model providers failed", "detail": errorString(last)})
}

func (g *Gateway) call(ctx context.Context, p Provider, payload map[string]any) (int, []byte, error) {
	return g.callEndpoint(ctx, p, "/chat/completions", payload)
}

func (g *Gateway) callEndpoint(ctx context.Context, p Provider, endpoint string, payload map[string]any) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data := readLimit(resp.Body, 4<<20)
	return resp.StatusCode, data, nil
}

func (g *Gateway) cacheKey(kind, tenant, provider string, payload map[string]any) string {
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return "agent:v1:" + kind + ":" + tenant + ":" + provider + ":" + hex.EncodeToString(sum[:])
}
func (g *Gateway) cacheGet(c *gin.Context, key string) []byte {
	if g.cache == nil {
		return nil
	}
	value, err := g.cache.Get(c.Request.Context(), key)
	if err != nil || len(value) == 0 {
		return nil
	}
	return value
}
func (g *Gateway) cacheSet(c *gin.Context, key string, value []byte) {
	if g.cache != nil {
		_ = g.cache.Set(c.Request.Context(), key, value, g.cacheTTL)
	}
}

func clonePayload(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
func errorString(err error) string {
	if err == nil {
		return "unknown upstream error"
	}
	return err.Error()
}

func readLimit(r io.Reader, limit int64) []byte {
	data, _ := io.ReadAll(io.LimitReader(r, limit))
	return data
}
