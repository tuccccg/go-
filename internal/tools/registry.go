package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type Definition struct {
	Name        string                                                          `json:"name"`
	Description string                                                          `json:"description"`
	Required    []string                                                        `json:"required"`
	Allowed     map[string]bool                                                 `json:"-"`
	Run         func(context map[string]any, input map[string]any) (any, error) `json:"-"`
}
type callResult struct {
	value     any
	err       error
	ready     chan struct{}
	createdAt time.Time
}
type Registry struct {
	mu            sync.RWMutex
	defs          map[string]Definition
	calls         map[string]*callResult
	maxPerSubject int
	counts        map[string]int
}

func NewRegistry(maxPerSubject int) *Registry {
	return &Registry{defs: map[string]Definition{}, calls: map[string]*callResult{}, maxPerSubject: maxPerSubject, counts: map[string]int{}}
}
func (r *Registry) Register(d Definition) { r.mu.Lock(); defer r.mu.Unlock(); r.defs[d.Name] = d }
func (r *Registry) RemovePrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.defs {
		if strings.HasPrefix(name, prefix) {
			delete(r.defs, name)
		}
	}
}
func (r *Registry) List() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, Definition{Name: d.Name, Description: d.Description, Required: d.Required})
	}
	return out
}

func (r *Registry) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		r.mu.RLock()
		d, ok := r.defs[c.Param("name")]
		r.mu.RUnlock()
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "tool not found"})
			return
		}
		var input map[string]any
		if err := c.ShouldBindJSON(&input); err != nil || input == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body must be a JSON object"})
			return
		}
		for _, key := range d.Required {
			if _, exists := input[key]; !exists {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing required argument: " + key})
				return
			}
		}
		for key := range input {
			if !d.Allowed[key] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "unexpected argument: " + key})
				return
			}
		}
		subjectAny, _ := c.Get("agent.subject")
		subject, _ := subjectAny.(string)
		if subject == "" {
			subject = c.ClientIP()
		}
		runID := strings.TrimSpace(c.GetHeader("X-Agent-Run-ID"))
		if runID == "" {
			runID = time.Now().UTC().Format("2006-01-02T15:04")
		}
		limitKey := subject + ":" + runID
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" {
			key = hashInput(input)
		}
		key = subject + ":" + d.Name + ":" + key
		result, owner := r.getOrCreate(key)
		if owner {
			r.mu.Lock()
			if r.counts[limitKey] >= r.maxPerSubject {
				r.mu.Unlock()
				result.err = errLimit
				close(result.ready)
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "tool call limit exceeded"})
				return
			}
			r.counts[limitKey]++
			r.mu.Unlock()
			result.value, result.err = d.Run(map[string]any{"subject": subject}, input)
			close(result.ready)
		} else {
			<-result.ready
		}
		if result.err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "tool execution failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"result": result.value})
	}
}

var errLimit = &toolError{"tool call limit exceeded"}

type toolError struct{ message string }

func (e *toolError) Error() string { return e.message }
func (r *Registry) getOrCreate(key string) (*callResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for oldKey, value := range r.calls {
		if value.createdAt.Before(cutoff) {
			delete(r.calls, oldKey)
		}
	}
	if existing := r.calls[key]; existing != nil {
		return existing, false
	}
	created := &callResult{ready: make(chan struct{}), createdAt: time.Now()}
	r.calls[key] = created
	return created, true
}
func hashInput(input map[string]any) string {
	b, _ := json.Marshal(input)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
