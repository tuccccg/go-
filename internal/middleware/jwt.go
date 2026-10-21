package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const SubjectKey = "agent.subject"

func JWT(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")), ".", 3)
		if len(parts) != 3 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed bearer token"})
			return
		}
		enc := base64.RawURLEncoding
		sig, err := enc.DecodeString(parts[2])
		if err != nil {
			unauthorized(c)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
		if !hmac.Equal(sig, mac.Sum(nil)) {
			unauthorized(c)
			return
		}
		headerBytes, e1 := enc.DecodeString(parts[0])
		payloadBytes, e2 := enc.DecodeString(parts[1])
		var header map[string]any
		var claims struct {
			Subject string `json:"sub"`
			Tenant  string `json:"tenant_id"`
			Expires int64  `json:"exp"`
		}
		if e1 != nil || e2 != nil || json.Unmarshal(headerBytes, &header) != nil || header["alg"] != "HS256" || json.Unmarshal(payloadBytes, &claims) != nil || claims.Subject == "" || (claims.Expires != 0 && time.Now().Unix() >= claims.Expires) {
			unauthorized(c)
			return
		}
		c.Set(SubjectKey, claims.Subject)
		if claims.Tenant != "" {
			c.Set("agent.tenant", claims.Tenant)
		} else {
			c.Set("agent.tenant", claims.Subject)
		}
		c.Next()
	}
}

func unauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired bearer token"})
}
