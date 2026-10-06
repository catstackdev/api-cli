package httpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryDelayHonoursRetryAfterButCapsIt(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	hdr := func(v string) http.Header { h := http.Header{}; h.Set("Retry-After", v); return h }
	cases := []struct {
		name    string
		attempt int
		h       http.Header
		want    time.Duration
	}{
		{"no header: exponential backoff", 1, nil, 300 * time.Millisecond},
		{"second attempt doubles", 2, nil, 600 * time.Millisecond},
		{"seconds", 1, hdr("5"), 5 * time.Second},
		{"shorter than the backoff is ignored", 3, hdr("0"), 1200 * time.Millisecond},
		{"http date", 1, hdr(now.Add(7 * time.Second).UTC().Format(http.TimeFormat)), 7 * time.Second},
		{"a date in the past", 1, hdr(now.Add(-time.Hour).UTC().Format(http.TimeFormat)), 300 * time.Millisecond},
		{"negative", 1, hdr("-3"), 300 * time.Millisecond},
		{"garbage", 1, hdr("soon"), 300 * time.Millisecond},
		{"capped", 1, hdr("86400"), maxRetryAfter},
	}
	for _, c := range cases {
		if got := RetryDelay(c.attempt, c.h, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDoRetriesTooManyRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	resp, err := Do(context.Background(), Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second, ConnectTimeout: time.Second}, 3)
	if err != nil || resp.Status != 200 || calls.Load() != 3 {
		t.Fatalf("status=%v err=%v calls=%d", resp, err, calls.Load())
	}
	// without --retry nothing is retried
	calls.Store(0)
	resp, err = Do(context.Background(), Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second, ConnectTimeout: time.Second}, 0)
	if err != nil || resp.Status != 429 || calls.Load() != 1 {
		t.Fatalf("status=%v err=%v calls=%d", resp, err, calls.Load())
	}
}
