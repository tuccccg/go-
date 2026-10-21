package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func PlanHandler(baseURL, token string) gin.HandlerFunc {
	client := &http.Client{Timeout: 120 * time.Second}
	return func(c *gin.Context) {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request"})
			return
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/plan", bytes.NewReader(encoded))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "supervisor request failed"})
			return
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "supervisor unavailable"})
			return
		}
		defer response.Body.Close()
		c.Data(response.StatusCode, response.Header.Get("Content-Type"), readBounded(response.Body, 4<<20))
	}
}

func readBounded(reader io.Reader, limit int64) []byte {
	data, _ := io.ReadAll(io.LimitReader(reader, limit))
	return data
}
