package httpc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOriginOf(t *testing.T) {
	cases := map[string]string{
		"https://API.test/x":       "https://api.test",
		"https://api.test:443/x":   "https://api.test", // default port is the same origin
		"http://api.test:80":       "http://api.test",
		"http://api.test:8080/x":   "http://api.test:8080",
		"https://api.test:80/x":    "https://api.test:80", // 80 is only the default for http
		"HTTP://api.test/x":        "http://api.test",
		"http://[::1]:3000/x":      "http://::1:3000",
		"http://127.0.0.1:1/a?b=c": "http://127.0.0.1:1",
	}
	for raw, want := range cases {
		if got := originOf(mustURL(t, raw)); got != want {
			t.Errorf("originOf(%s) = %q, want %q", raw, got, want)
		}
	}
}

// A redirect is only followed to the origin the request started at: a 307/308
// would re-send the body (a login password) to wherever the server points.
func TestCheckRedirectRules(t *testing.T) {
	first, _ := http.NewRequest("POST", "https://api.test/login", nil)
	next := func(raw string) *http.Request {
		r, _ := http.NewRequest("POST", raw, nil)
		return r
	}
	via := []*http.Request{first}

	cases := []struct {
		name    string
		spec    Spec
		to      string
		wantErr string
	}{
		{"same origin", Spec{}, "https://api.test/other", ""},
		{"same origin, explicit default port", Spec{}, "https://API.test:443/other", ""},
		{"another host", Spec{}, "https://evil.test/x", "refusing redirect to https://evil.test"},
		{"another port", Spec{}, "https://api.test:8443/x", "refusing redirect"},
		{"downgrade to http", Spec{}, "http://api.test/x", "refusing redirect"},
		{"a subdomain is another origin", Spec{}, "https://sub.api.test/x", "refusing redirect"},
		{"opted in: another host", Spec{FollowCrossOrigin: true}, "https://evil.test/x", ""},
	}
	for _, c := range cases {
		err := checkRedirect(c.spec)(next(c.to), via)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}

	// the hop limit applies even with --trust-hosts
	long := make([]*http.Request, 10)
	for i := range long {
		long[i] = first
	}
	if err := checkRedirect(Spec{FollowCrossOrigin: true})(next("https://api.test/x"), long); err == nil {
		t.Error("an 11th redirect must stop")
	}
	chain := []*http.Request{first, next("https://api.test/hop")}
	if err := checkRedirect(Spec{})(next("https://evil.test/x"), chain); err == nil {
		t.Error("a hop to another origin must be refused even after a same-origin hop")
	}
}

func TestDoWontForwardTheBodyToAnotherOrigin(t *testing.T) {
	var gotBody atomic.Value
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect) // 307 keeps the body
	}))
	defer origin.Close()

	spec := Spec{Method: "POST", URL: origin.URL, Body: `{"password":"s3cret"}`, Timeout: 5 * time.Second, ConnectTimeout: time.Second}

	_, err := Do(context.Background(), spec, 0)
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("err = %v, want a refused redirect", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the other origin was contacted")
	}

	spec.FollowCrossOrigin = true
	resp, err := Do(context.Background(), spec, 0)
	if err != nil || resp.Status != 200 {
		t.Fatalf("opted in: resp=%v err=%v", resp, err)
	}
	if hits.Load() != 1 || gotBody.Load() != `{"password":"s3cret"}` {
		t.Errorf("opted in: hits=%d body=%v", hits.Load(), gotBody.Load())
	}
}

func TestDoFollowsSameOriginRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/new", http.StatusFound) })
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("moved")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := Do(context.Background(), Spec{Method: "GET", URL: srv.URL + "/old", Timeout: 5 * time.Second}, 0)
	if err != nil || string(resp.Body) != "moved" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestDoRetryEdges(t *testing.T) {
	status := func(code int) (*httptest.Server, *atomic.Int32) {
		var calls atomic.Int32
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(code)
		})), &calls
	}
	spec := func(url string) Spec { return Spec{Method: "GET", URL: url, Timeout: 5 * time.Second} }

	// 500 and 404 are answers, not transient failures
	for _, code := range []int{500, 404} {
		srv, calls := status(code)
		resp, err := Do(context.Background(), spec(srv.URL), 3)
		srv.Close()
		if err != nil || resp.Status != code || calls.Load() != 1 {
			t.Errorf("%d: resp=%v err=%v calls=%d, want one call", code, resp, err, calls.Load())
		}
	}

	// when the retries run out the last response is returned, not an error
	srv, calls := status(503)
	defer srv.Close()
	resp, err := Do(context.Background(), spec(srv.URL), 2)
	if err != nil || resp.Status != 503 || calls.Load() != 3 {
		t.Errorf("exhausted: resp=%v err=%v calls=%d, want the 503 after 3 calls", resp, err, calls.Load())
	}

	// a cancelled context ends the loop instead of sleeping through the backoff
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := Do(ctx, spec(srv.URL), 5); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("a cancelled request must not wait out the retries")
	}
}

func TestInsecureOnlyWhenAsked(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	spec := Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second}
	if _, err := Do(context.Background(), spec, 0); err == nil {
		t.Fatal("an untrusted certificate must fail by default")
	}
	spec.Insecure = true
	if resp, err := Do(context.Background(), spec, 0); err != nil || resp.Status != 200 {
		t.Fatalf("-k: resp=%v err=%v", resp, err)
	}
}

func TestTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	tm, err := Trace(context.Background(), Spec{Method: "GET", URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil || tm.Status != 200 {
		t.Fatalf("timing=%+v err=%v", tm, err)
	}
	// cumulative, like curl's time_connect / time_starttransfer
	if !(tm.Connect > 0 && tm.Connect <= tm.TTFB && tm.TTFB <= tm.Total) {
		t.Errorf("phases must be cumulative: %+v", tm)
	}
	if tm.TLS != 0 {
		t.Errorf("plain http has no TLS phase: %+v", tm)
	}
	if _, err := Trace(context.Background(), Spec{Method: "BAD METHOD", URL: srv.URL}); err == nil {
		t.Error("an invalid request must fail before sending")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// scripts rely on curl's codes: 6 DNS, 28 timeout, 7 connect, anything else 1
func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "x.test"}, 6},
		{"dns wins over its own timeout flag", &net.DNSError{Err: "timeout", IsTimeout: true}, 6},
		{"wrapped dns", &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host"}}, 6},
		{"net timeout", &net.OpError{Op: "read", Err: timeoutErr{}}, 28},
		{"deadline", context.DeadlineExceeded, 28},
		{"wrapped deadline", &url.Error{Op: "Get", URL: "http://x", Err: context.DeadlineExceeded}, 28},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, 7},
		{"not a transport error", errors.New("boom"), 1},
		{"nil-ish usage error", context.Canceled, 1},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}
