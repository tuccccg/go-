package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"
)

func main() {
	sub := flag.String("sub", "demo-user", "JWT subject")
	tenant := flag.String("tenant", "", "optional tenant id")
	secret := flag.String("secret", "", "HS256 secret")
	minutes := flag.Int("minutes", 60, "token lifetime")
	flag.Parse()
	if *secret == "" {
		panic("-secret is required")
	}
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims := map[string]any{"sub": *sub, "exp": time.Now().Add(time.Duration(*minutes) * time.Minute).Unix()}
	if *tenant != "" {
		claims["tenant_id"] = *tenant
	}
	payload, _ := json.Marshal(claims)
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	h := hmac.New(sha256.New, []byte(*secret))
	_, _ = h.Write([]byte(unsigned))
	fmt.Println(strings.Join([]string{unsigned, enc.EncodeToString(h.Sum(nil))}, "."))
}
