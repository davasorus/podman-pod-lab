package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTest(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr
}

func TestShortenAndResolve(t *testing.T) {
	setupTest(t)

	// shorten
	req := httptest.NewRequest(http.MethodPost, "/shorten", strings.NewReader("https://example.com"))
	rec := httptest.NewRecorder()
	shorten(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("shorten status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	parts := strings.Split(strings.TrimSpace(rec.Body.String()), "/")
	code := parts[len(parts)-1]
	if len(code) != 8 {
		t.Fatalf("expected 8-char hex code, got %q", code)
	}

	// resolve
	req = httptest.NewRequest(http.MethodGet, "/"+code, nil)
	rec = httptest.NewRecorder()
	resolve(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("resolve status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com" {
		t.Fatalf("Location = %q, want https://example.com", loc)
	}
}

func TestShortenRejectsBadInput(t *testing.T) {
	setupTest(t)

	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"wrong method", http.MethodGet, "https://example.com", http.StatusMethodNotAllowed},
		{"empty body", http.MethodPost, "", http.StatusBadRequest},
		{"not a url", http.MethodPost, "gopher://old.school", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/shorten", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			shorten(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestResolveUnknownCode(t *testing.T) {
	setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/deadbeef", nil)
	rec := httptest.NewRecorder()
	resolve(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzOK(t *testing.T) {
	setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz body = %q, want ok", rec.Body.String())
	}
}

func TestHealthzRedisDown(t *testing.T) {
	mr := setupTest(t)
	mr.Close() // kill Redis so the ping fails

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthz(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with redis down = %d, want 503", rec.Code)
	}
}

func TestNewCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c, err := newCode()
		if err != nil {
			t.Fatalf("newCode error: %v", err)
		}
		if len(c) != codeBytes*2 { // hex doubles the byte count
			t.Fatalf("code %q length = %d, want %d", c, len(c), codeBytes*2)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q within 100 draws", c)
		}
		seen[c] = true
	}
}

func TestShortenCollisionRetry(t *testing.T) {
	mr := setupTest(t)

	// Shorten once, capture the code.
	req := httptest.NewRequest(http.MethodPost, "/shorten", strings.NewReader("https://example.com"))
	rec := httptest.NewRecorder()
	shorten(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first shorten failed: %d", rec.Code)
	}
	parts := strings.Split(strings.TrimSpace(rec.Body.String()), "/")
	first := parts[len(parts)-1]

	// The key must exist (SetNX claimed it) and hold the URL.
	got, err := mr.Get("url:" + first)
	if err != nil {
		t.Fatalf("stored key missing: %v", err)
	}
	if got != "https://example.com" {
		t.Fatalf("stored value = %q", got)
	}

	// A second shorten of a different URL must get a DIFFERENT code (no clobber).
	req = httptest.NewRequest(http.MethodPost, "/shorten", strings.NewReader("https://other.example"))
	rec = httptest.NewRecorder()
	shorten(rec, req)
	parts = strings.Split(strings.TrimSpace(rec.Body.String()), "/")
	second := parts[len(parts)-1]
	if first == second {
		t.Fatalf("two shortens produced the same code %q", first)
	}
}

func TestMuxRoutes(t *testing.T) {
	setupTest(t)
	mux := newMux()

	// /healthz routes to healthz (not the catch-all resolve).
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mux /healthz = %d, want 200", rec.Code)
	}
}
