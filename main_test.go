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
