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
}

// Timing is the phase breakdown shown by `api time`.
type Timing struct {
	DNS, Connect, TLS, TTFB, Total time.Duration
	Status                         int
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

func once(ctx context.Context, client *http.Client, s Spec) (*Response, error) {
	req, err := NewRequest(ctx, s)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return &Response{
		Status:     res.StatusCode,
		StatusText: http.StatusText(res.StatusCode),
		Proto:      res.Proto,
		Header:     res.Header,
		Body:       body,
		Duration:   time.Since(start),
	}, nil
}

// Trace sends the request once and records the phase timings.
func Trace(ctx context.Context, s Spec) (Timing, error) {
	var (
		t     Timing
		start time.Time
	)
	// Cumulative from the start, like curl's time_namelookup / time_connect /
	// time_appconnect / time_starttransfer.
	trace := &httptrace.ClientTrace{
		DNSDone:              func(httptrace.DNSDoneInfo) { t.DNS = time.Since(start) },
		ConnectDone:          func(string, string, error) { t.Connect = time.Since(start) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.TLS = time.Since(start) },
		GotFirstResponseByte: func() { t.TTFB = time.Since(start) },
	}

	req, err := NewRequest(httptrace.WithClientTrace(ctx, trace), s)
	if err != nil {
		return t, err
	}
	start = time.Now()
	res, err := newClient(s).Do(req)
	if err != nil {
		return t, err
	}
	defer res.Body.Close()
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		return t, err
	}
	t.Total = time.Since(start)
	t.Status = res.StatusCode
	return t, nil
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
