// Package httpc builds, prints and sends HTTP requests from one Spec, so the
// request that is sent, the one that is dry-run, and the curl passthrough can
// never drift apart.
package httpc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Header is one request header; a slice keeps the order stable for dry runs.
type Header struct {
	Name  string
	Value string
}

// Spec is everything needed to send (or print) one request.
type Spec struct {
	Method         string
	URL            string
	Headers        []Header
	Body           string   // raw body, usually JSON
	Fields         []string // -F multipart: key=value or key=@file
	Data           []string // -d urlencoded: joined with &
	Timeout        time.Duration
	ConnectTimeout time.Duration
	Insecure       bool
	// FollowCrossOrigin allows redirects to another host (--trust-hosts). By
	// default they are refused: a redirect re-sends the request, body included,
	// to wherever the server points.
	FollowCrossOrigin bool
}

// SetHeader sets a header, replacing any existing one of the same name.
func (s *Spec) SetHeader(name, value string) {
	for i, h := range s.Headers {
		if strings.EqualFold(h.Name, name) {
			s.Headers[i].Value = value
			return
		}
	}
	s.Headers = append(s.Headers, Header{name, value})
}

// DefaultHeader sets a header only if the caller has not set it.
func (s *Spec) DefaultHeader(name, value string) {
	if !s.HasHeader(name) {
		s.Headers = append(s.Headers, Header{name, value})
	}
}

// Header returns the value of a header (case-insensitive), or "".
func (s *Spec) Header(name string) string {
	for _, h := range s.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// HasHeader reports whether a header (case-insensitive) is set.
func (s *Spec) HasHeader(name string) bool {
	for _, h := range s.Headers {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// CurlArgs returns the equivalent curl arguments, so --dry and the `--`
// passthrough use exactly what the native client would send.
func (s Spec) CurlArgs() []string {
	args := []string{"-sS", "--fail-with-body"}
	if s.ConnectTimeout > 0 {
		args = append(args, "--connect-timeout", seconds(s.ConnectTimeout))
	}
	if s.Timeout > 0 {
		args = append(args, "--max-time", seconds(s.Timeout))
	}
	if s.Insecure {
		args = append(args, "-k")
	}
	args = append(args, "-X", s.Method, s.URL)
	for _, h := range s.Headers {
		args = append(args, "-H", h.Name+": "+h.Value)
	}
	if s.Body != "" {
		args = append(args, "-d", s.Body)
	}
	for _, f := range s.Fields {
		args = append(args, "-F", f)
	}
	for _, d := range s.Data {
		args = append(args, "-d", d)
	}
	return args
}

// ResolveURL joins a path onto base. A path starting with "/" needs base; any
// other target is used as-is. Query pairs ("k=v") are appended.
func ResolveURL(base, target string, query []string) (string, error) {
	full := target
	if strings.HasPrefix(target, "/") {
		if base == "" {
			return "", fmt.Errorf("API_BASE_URL not set — run: api env load [suffix]")
		}
		full = strings.TrimRight(base, "/") + target
	}
	if len(query) == 0 {
		return full, nil
	}
	u, err := url.Parse(full)
	if err != nil {
		return "", fmt.Errorf("bad url %q: %w", full, err)
	}
	q := u.Query()
	for _, pair := range query {
		k, v, _ := strings.Cut(pair, "=")
		q.Add(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// NewRequest builds the net/http request for a Spec.
func NewRequest(ctx context.Context, s Spec) (*http.Request, error) {
	var (
		body        io.Reader
		contentType string
	)
	switch {
	case len(s.Fields) > 0:
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, f := range s.Fields {
			if err := writeField(w, f); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		body, contentType = &buf, w.FormDataContentType()
	case len(s.Data) > 0:
		body = strings.NewReader(strings.Join(s.Data, "&"))
		contentType = "application/x-www-form-urlencoded"
	case s.Body != "":
		body = strings.NewReader(s.Body)
	}

	req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, body)
	if err != nil {
		return nil, err
	}
	for _, h := range s.Headers {
		req.Header.Set(h.Name, h.Value)
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func writeField(w *multipart.Writer, field string) error {
	key, val, ok := strings.Cut(field, "=")
	if !ok {
		return fmt.Errorf("bad -F %q: want key=value or key=@file", field)
	}
	if !strings.HasPrefix(val, "@") {
		return w.WriteField(key, val)
	}
	path := val[1:]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	part, err := w.CreateFormFile(key, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}
