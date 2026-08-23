// podlab is a minimal Redis-backed URL shortener — a small lab service for
// experimenting with Podman pod networking (app + redis in one pod, sharing a
// network namespace so REDIS_ADDR is localhost).
//
// POST a URL to /shorten -> returns a short link.
// GET /<code> -> 302 redirect to the original URL.
// GET /healthz -> liveness/readiness (checks Redis).
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
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// rdb is the Redis client, set in main (and swapped by tests).
var rdb *redis.Client

const (
	// redisOpTimeout bounds every Redis call so a slow/hung Redis can't pin a
	// request open indefinitely.
	redisOpTimeout = 3 * time.Second
	// maxBodyBytes caps the request body — a URL, not a payload.
	maxBodyBytes = 4096
	// codeBytes is the number of random bytes per short code (hex-encoded, so
	// the visible code is twice this many chars).
	codeBytes = 4
	// maxCodeGenAttempts bounds the collision-retry loop.
	maxCodeGenAttempts = 5
)

// shorten generates a short code for a POSTed URL and stores it in Redis.
func shorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil || len(body) == 0 {
		http.Error(w, "body must be a URL", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(string(body))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), redisOpTimeout)
	defer cancel()

	// Generate a code and claim it atomically with SetNX, retrying on the
	// (astronomically unlikely) collision so we never clobber an existing
	// mapping.
	var code string
	for attempt := 0; attempt < maxCodeGenAttempts; attempt++ {
		c, genErr := newCode()
		if genErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			log.Printf("shorten: code generation failed: %v", genErr)
			return
		}
		ok, setErr := rdb.SetNX(ctx, "url:"+c, url, 0).Result()
		if setErr != nil {
			http.Error(w, "redis: "+setErr.Error(), http.StatusBadGateway)
			return
		}
		if ok {
			code = c
			break
		}
	}
	if code == "" {
		http.Error(w, "could not allocate a code, try again", http.StatusServiceUnavailable)
		return
	}

	base := os.Getenv("BASE_URL")
	if base == "" {
		base = "http://localhost:" + port()
	}
	_, _ = fmt.Fprintf(w, "%s/%s\n", strings.TrimRight(base, "/"), code)
}

// newCode returns a random hex short code, or an error if the system RNG fails.
func newCode() (string, error) {
	buf := make([]byte, codeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// resolve looks up the URL for a code and redirects to it.
func resolve(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/")
	if code == "" {
		_, _ = fmt.Fprintln(w, "POST a URL to /shorten")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), redisOpTimeout)
	defer cancel()

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

// healthz reports service health, including Redis reachability, for container
// liveness/readiness probes.
func healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), redisOpTimeout)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		http.Error(w, "redis unreachable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

// port returns the listen port from PORT, defaulting to 8080.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}

// newMux wires the routes. Separated from main so tests can exercise the full
// handler set through one mux.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/shorten", shorten)
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/", resolve)
	return mux
}

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379" // pod-shared network namespace
	}
	rdb = redis.NewClient(&redis.Options{Addr: addr})

	// Fail fast if Redis is unreachable at startup rather than surfacing it
	// only on the first request.
	startupCtx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	if err := rdb.Ping(startupCtx).Err(); err != nil {
		log.Printf("warning: redis not reachable at %s: %v (continuing; /healthz will report unready)", addr, err)
	}

	srv := &http.Server{
		Addr:              ":" + port(),
		Handler:           newMux(),
		ReadHeaderTimeout: 5 * time.Second, // slowloris guard
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM so in-flight requests finish and the
	// container stops cleanly.
	go func() {
		log.Printf("listening on %s, redis at %s", srv.Addr, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
	_ = rdb.Close()
}
