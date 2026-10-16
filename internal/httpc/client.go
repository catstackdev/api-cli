package httpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Response is a fully-read HTTP response.
type Response struct {
	Status     int
	StatusText string
	Proto      string
	Header     http.Header
	Body       []byte
	Duration   time.Duration
	Timing     Timing
}

// Timing is the phase breakdown shown by `api time` and `-v`. The phases are
// cumulative from the start of the attempt, like curl's time_namelookup /
// time_connect / time_appconnect / time_starttransfer.
type Timing struct {
	DNS, Connect, TLS, TTFB, Total time.Duration
	Status                         int
	// Reused is true when the connection was already open: DNS, Connect and TLS
	// then did not happen in this attempt, and 0 means "skipped", not "instant".
	Reused bool
}

// timingRecorder collects the trace hooks of one attempt. The dial hooks run on
// the transport's own goroutines (and ConnectDone can fire once per address),
// so the writes are guarded.
type timingRecorder struct {
	mu    sync.Mutex
	start time.Time
	t     Timing
}

func (r *timingRecorder) hooks() *httptrace.ClientTrace {
	since := func(set func(*Timing, time.Duration)) {
		r.mu.Lock()
		defer r.mu.Unlock()
		set(&r.t, time.Since(r.start))
	}
	return &httptrace.ClientTrace{
		DNSDone:          func(httptrace.DNSDoneInfo) { since(func(t *Timing, d time.Duration) { t.DNS = d }) },
		ConnectDone:      func(string, string, error) { since(func(t *Timing, d time.Duration) { t.Connect = d }) },
		TLSHandshakeDone: func(tls.ConnectionState, error) { since(func(t *Timing, d time.Duration) { t.TLS = d }) },
		GotConn: func(info httptrace.GotConnInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.t.Reused = info.Reused
		},
		GotFirstResponseByte: func() { since(func(t *Timing, d time.Duration) { t.TTFB = d }) },
	}
}

func (r *timingRecorder) snapshot() Timing {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func newClient(s Spec) *http.Client {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		DialContext:     (&net.Dialer{Timeout: s.ConnectTimeout}).DialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: s.Insecure}, //nolint:gosec // opt-in via -k
	}
	return &http.Client{Transport: transport, Timeout: s.Timeout, CheckRedirect: checkRedirect(s)}
}

// checkRedirect follows up to 10 redirects, but only to the same origin unless
// the caller opted in. A 307/308 re-sends the body (a login password included)
// to the new location, so a server we trust, or an open redirect on it, could
// otherwise forward it anywhere; the destination checks only see the first hop.
func checkRedirect(s Spec) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !s.FollowCrossOrigin && originOf(req.URL) != originOf(via[0].URL) {
			return fmt.Errorf("refusing redirect to %s: that host would receive your request (re-run with --trust-hosts to follow it)", originOf(req.URL))
		}
		return nil
	}
}

func originOf(u *url.URL) string {
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	h := strings.ToLower(u.Hostname())
	if port != "" {
		h += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + h
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// maxRetryAfter caps how long a server can make us wait: a hostile or broken
// header must not hang a script for hours.
const maxRetryAfter = 60 * time.Second

// RetryDelay is the wait before retry number attempt (1-based): the exponential
// backoff, or the server's Retry-After (seconds or an HTTP date) when that is
// longer, never more than maxRetryAfter.
func RetryDelay(attempt int, h http.Header, now time.Time) time.Duration {
	delay := 300 * time.Millisecond << (attempt - 1)
	if after := retryAfter(h.Get("Retry-After"), now); after > delay {
		delay = after
	}
	if delay > maxRetryAfter {
		delay = maxRetryAfter
	}
	return delay
}

func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// Do sends the request, retrying transport errors and 429/502/503/504 up to
// retries extra times with exponential backoff.
func Do(ctx context.Context, s Spec, retries int) (*Response, error) {
	client := newClient(s)
	var (
		resp *Response
		err  error
	)
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			var hdr http.Header
			if resp != nil {
				hdr = resp.Header
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(RetryDelay(attempt, hdr, time.Now())):
			}
		}
		resp, err = once(ctx, client, s)
		if err == nil && !retryable(resp.Status) {
			return resp, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			break
		}
	}
	return resp, err
}

// once sends one attempt. The trace is per attempt, so after a retry the
// timings are those of the attempt that produced the answer.
func once(ctx context.Context, client *http.Client, s Spec) (*Response, error) {
	rec := &timingRecorder{}
	req, err := NewRequest(httptrace.WithClientTrace(ctx, rec.hooks()), s)
	if err != nil {
		return nil, err
	}
	rec.start = time.Now()
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	total := time.Since(rec.start)
	timing := rec.snapshot()
	timing.Total, timing.Status = total, res.StatusCode
	return &Response{
		Status:     res.StatusCode,
		StatusText: http.StatusText(res.StatusCode),
		Proto:      res.Proto,
		Header:     res.Header,
		Body:       body,
		Duration:   total,
		Timing:     timing,
	}, nil
}

// ExitCode maps a transport error to a curl-compatible exit code.
func ExitCode(err error) int {
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return 6
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return 28
	case errors.As(err, &netErr):
		return 7
	default:
		return 1
	}
}
