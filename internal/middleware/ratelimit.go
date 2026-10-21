package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type bucket struct {
	tokens float64
	last   time.Time
}
type RateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	rate, burst float64
}

func NewRateLimiter(rate, burst float64) *RateLimiter {
	return &RateLimiter{buckets: map[string]*bucket{}, rate: rate, burst: burst}
}
func (l *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := c.Get(SubjectKey)
		key, _ := id.(string)
		if key == "" {
			key = c.ClientIP()
		}
		if !l.allow(key) {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}
func (l *RateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
