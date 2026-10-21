package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go-agent-service/internal/cache"
	"go-agent-service/internal/gateway"
	"go-agent-service/internal/middleware"
	"go-agent-service/internal/supervisor"
	"go-agent-service/internal/tools"
)

func main() {
	providers, err := gateway.ParseProviders(os.Getenv("LLM_PROVIDERS"))
	if err != nil {
		log.Fatalf("invalid LLM_PROVIDERS: %v", err)
	}
	if len(providers) == 0 {
		log.Println("LLM_PROVIDERS is empty; chat requests will return 503 until an upstream is configured")
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET must be set")
	}

	engine := gateway.New(providers, gateway.Options{Timeout: 90 * time.Second, MaxAttempts: 2})
	engine.SetCache(cache.FromEnv(), time.Duration(envInt("CACHE_TTL_MINUTES", 1440))*time.Minute)
	registry := tools.NewRegistry(8)
	supervisorURL := strings.TrimRight(os.Getenv("SUPERVISOR_URL"), "/")
	supervisorToken := os.Getenv("SUPERVISOR_TOKEN")
	if supervisorURL != "" {
		if err := tools.SyncMCPTools(context.Background(), registry, supervisorURL, supervisorToken); err != nil {
			log.Printf("initial MCP discovery failed: %v", err)
		}
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				if err := tools.SyncMCPTools(ctx, registry, supervisorURL, supervisorToken); err != nil {
					log.Printf("MCP tool refresh failed: %v", err)
				}
				cancel()
			}
		}()
	}
	registry.Register(tools.Definition{
		Name: "echo", Description: "Return the supplied message.", Required: []string{"message"},
		Allowed: map[string]bool{"message": true},
		Run: func(_ map[string]any, input map[string]any) (any, error) {
			return map[string]any{"message": input["message"]}, nil
		},
	})

	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	rate := envFloat("RATE_LIMIT_RPS", 20)
	burst := envFloat("RATE_LIMIT_BURST", 40)
	api := r.Group("/v1")
	api.Use(middleware.JWT(secret), middleware.NewRateLimiter(rate, burst).Middleware())
	api.POST("/chat/completions", engine.ChatHandler())
	api.POST("/embeddings", engine.EmbeddingHandler())
	if supervisorURL != "" {
		api.POST("/agent/plan", supervisor.PlanHandler(supervisorURL, supervisorToken))
	}
	api.GET("/tools", func(c *gin.Context) { c.JSON(http.StatusOK, registry.List()) })
	api.POST("/tools/:name", registry.Handler())

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("agent gateway listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		log.Fatalf("%s must be a positive number", key)
	}
	return parsed
}

func envInt(key string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &value); err != nil || value <= 0 {
		return fallback
	}
	return value
}
