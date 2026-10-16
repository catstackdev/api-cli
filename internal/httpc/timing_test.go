package httpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// every request records its phase timings, so -v and --summary can show them
// without sending the request a second time
func TestDoRecordsPhaseTimings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := Do(context.Background(), Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tm := resp.Timing
	if tm.Status != 200 || tm.Reused {
		t.Fatalf("timing=%+v", tm)
	}
	if !(tm.Connect > 0 && tm.Connect <= tm.TTFB && tm.TTFB <= tm.Total) {
		t.Errorf("phases must be cumulative: %+v", tm)
	}
	if tm.TLS != 0 {
		t.Errorf("plain http has no TLS phase: %+v", tm)
	}
	if resp.Duration != tm.Total {
		t.Errorf("Duration and Timing.Total must agree: %v vs %v", resp.Duration, tm.Total)
	}
}

// a retry keeps the timings of the attempt that produced the answer, not the
// first one
func TestDoTimingsBelongToTheLastAttempt(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := Do(context.Background(), Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second}, 1)
	if err != nil || resp.Status != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if resp.Timing.Status != 200 || calls != 2 {
		t.Errorf("timing must describe the 200, got %+v after %d calls", resp.Timing, calls)
	}
}
