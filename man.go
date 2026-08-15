package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
)

var (
	ctx = context.Background()
	rdb *redis.Client
)

func shorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || len(body) == 0 {
		http.Error(w, "body must be a URL", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(string(body))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
		return
	}

	buf := make([]byte, 4)
	rand.Read(buf)
	code := hex.EncodeToString(buf)

	if err := rdb.Set(ctx, "url:"+code, url, 0).Err(); err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, "http://localhost:8080/%s\n", code)
}

func resolve(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/")
	if code == "" {
		fmt.Fprintln(w, "POST a URL to /shorten")
		return
	}
	url, err := rdb.Get(ctx, "url:"+code).Result()
	if errors.Is(err, redis.Nil) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "redis: "+err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379" // pod-shared network namespace
	}
	rdb = redis.NewClient(&redis.Options{Addr: addr})

	http.HandleFunc("/shorten", shorten)
	http.HandleFunc("/", resolve)

	log.Println("listening on :8080, redis at", addr)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
