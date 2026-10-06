// Package har writes HTTP Archive (HAR 1.2) files, for bug reports and for
// loading a request into browser devtools / proxy tools.
//
// Secrets are masked by default: credential headers, secret-looking JSON
// fields in bodies, and secret-looking query/form parameters. Masking is part
// of Build, so a caller cannot forget it; only an explicit IncludeSecrets
// option turns it off.
package har

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/secret"
)

// Redacted replaces a masked value.
const Redacted = "[REDACTED]"

var (
	secretHeader = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true, "x-api-key": true, "x-auth-token": true}
)

// Name/Value pairs as in the HAR spec.
type NV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type postData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Cookies     []NV      `json:"cookies"`
	Headers     []NV      `json:"headers"`
	QueryString []NV      `json:"queryString"`
	PostData    *postData `json:"postData,omitempty"`
	HeadersSize int       `json:"headersSize"`
	BodySize    int       `json:"bodySize"`
}

type content struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Encoding string `json:"encoding,omitempty"`
}

type response struct {
	Status      int     `json:"status"`
	StatusText  string  `json:"statusText"`
	HTTPVersion string  `json:"httpVersion"`
	Cookies     []NV    `json:"cookies"`
	Headers     []NV    `json:"headers"`
	Content     content `json:"content"`
	RedirectURL string  `json:"redirectURL"`
	HeadersSize int     `json:"headersSize"`
	BodySize    int     `json:"bodySize"`
}

type timings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

// Entry is one request/response pair.
type Entry struct {
	Started  string   `json:"startedDateTime"`
	Time     float64  `json:"time"`
	Request  request  `json:"request"`
	Response response `json:"response"`
	Cache    struct{} `json:"cache"`
	Timings  timings  `json:"timings"`
}

type creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type log struct {
	Version string  `json:"version"`
	Creator creator `json:"creator"`
	Entries []Entry `json:"entries"`
}

// Doc is a whole HAR file.
type Doc struct {
	Log log `json:"log"`
}

// Options controls Build.
type Options struct {
	Version        string
	IncludeSecrets bool // default false: mask
}

// Build turns one exchange into a HAR entry.
func Build(spec httpc.Spec, resp *httpc.Response, started time.Time, o Options) Entry {
	mask := !o.IncludeSecrets
	ms := float64(resp.Duration.Microseconds()) / 1000

	reqURL := spec.URL
	if mask {
		reqURL = maskURL(reqURL)
	}
	e := Entry{
		Started: started.UTC().Format(time.RFC3339Nano),
		Time:    ms,
		Request: request{
			Method: spec.Method, URL: reqURL, HTTPVersion: "HTTP/1.1",
			Cookies: []NV{}, Headers: []NV{}, QueryString: queryPairs(reqURL, mask),
			HeadersSize: -1, BodySize: -1,
		},
		Timings: timings{Wait: ms},
	}
	ctype := ""
	for _, h := range spec.Headers {
		e.Request.Headers = append(e.Request.Headers, NV{h.Name, maskHeader(h.Name, h.Value, mask)})
		if strings.EqualFold(h.Name, "Content-Type") {
			ctype = h.Value
		}
	}
	switch {
	case spec.Body != "":
		e.Request.PostData = &postData{MimeType: orMime(ctype), Text: maskBody(spec.Body, ctype, mask)}
		e.Request.BodySize = len(spec.Body)
	case len(spec.Data) > 0:
		text := strings.Join(spec.Data, "&")
		e.Request.PostData = &postData{MimeType: "application/x-www-form-urlencoded", Text: maskBody(text, "application/x-www-form-urlencoded", mask)}
		e.Request.BodySize = len(text)
	}

	e.Response = response{
		Status: resp.Status, StatusText: resp.StatusText, HTTPVersion: resp.Proto,
		Cookies: []NV{}, Headers: []NV{}, HeadersSize: -1, BodySize: len(resp.Body),
		RedirectURL: resp.Header.Get("Location"),
	}
	rtype := resp.Header.Get("Content-Type")
	for name, vals := range resp.Header {
		for _, v := range vals {
			e.Response.Headers = append(e.Response.Headers, NV{name, maskHeader(name, v, mask)})
		}
	}
	sortNV(e.Response.Headers)
	c := content{Size: len(resp.Body), MimeType: orMime(rtype)}
	if utf8.Valid(resp.Body) {
		c.Text = maskBody(string(resp.Body), rtype, mask)
	} else {
		c.Text, c.Encoding = base64.StdEncoding.EncodeToString(resp.Body), "base64"
	}
	e.Response.Content = c
	return e
}

func orMime(m string) string {
	if m == "" {
		return "application/octet-stream"
	}
	return m
}

func sortNV(l []NV) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].Name < l[j-1].Name; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func maskHeader(name, value string, mask bool) string {
	if mask && (secretHeader[strings.ToLower(name)] || secret.Param(name)) {
		return Redacted
	}
	return value
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = url.User(Redacted)
	}
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			if secret.Param(k) {
				q.Set(k, Redacted)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func queryPairs(raw string, mask bool) []NV {
	out := []NV{}
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	for k, vs := range u.Query() {
		for _, v := range vs {
			out = append(out, NV{k, v})
		}
	}
	sortNV(out)
	return out
}

// maskBody redacts secret-looking JSON fields (any depth) and form pairs.
func maskBody(body, ctype string, mask bool) string {
	if !mask {
		return body
	}
	trimmed := strings.TrimSpace(body)
	if json.Valid([]byte(trimmed)) && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) == nil {
			if !redactJSON(v) {
				return body // nothing secret: keep the original bytes and key order
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if enc.Encode(v) == nil {
				return strings.TrimRight(buf.String(), "\n")
			}
		}
	}
	if strings.Contains(strings.ToLower(ctype), "x-www-form-urlencoded") || (!strings.ContainsAny(trimmed, "{<\n") && strings.Contains(trimmed, "=")) {
		parts := strings.Split(body, "&")
		for i, p := range parts {
			if k, _, ok := strings.Cut(p, "="); ok && secret.Param(k) {
				parts[i] = k + "=" + Redacted
			}
		}
		return strings.Join(parts, "&")
	}
	return body
}

// redactJSON replaces secret-looking fields at any depth and reports whether it
// changed anything.
func redactJSON(v any) (changed bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if secret.Param(k) && x != nil {
				t[k] = Redacted
				changed = true
				continue
			}
			changed = redactJSON(x) || changed
		}
	case []any:
		for _, x := range t {
			changed = redactJSON(x) || changed
		}
	}
	return changed
}

// Append adds an entry to the HAR file at path (created 0600), keeping any
// entries already there so multi-request commands build one archive.
func Append(path string, e Entry, version string) error {
	doc := Doc{Log: log{Version: "1.2", Creator: creator{"api", version}, Entries: []Entry{}}}
	if data, err := os.ReadFile(path); err == nil {
		var old Doc
		if json.Unmarshal(data, &old) == nil && old.Log.Version != "" {
			doc = old
		}
	}
	doc.Log.Entries = append(doc.Log.Entries, e)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// The functions below are for printing a request (--dry, --debug): they always
// mask. Build uses the same code for HAR files.

// MaskBody redacts secret-looking JSON fields and form pairs in a body.
func MaskBody(body, contentType string) string { return maskBody(body, contentType, true) }

// MaskURL redacts credentials and secret-looking query parameters of a URL.
func MaskURL(raw string) string { return maskURL(raw) }

// MaskHeader redacts the value of a credential header.
func MaskHeader(name, value string) string { return maskHeader(name, value, true) }

// MaskPair redacts the value of one "key=value" pair when the key looks secret
// (urlencoded fields, -F fields).
func MaskPair(pair string) string {
	if k, _, ok := strings.Cut(pair, "="); ok && secret.Param(k) {
		return k + "=" + Redacted
	}
	return pair
}
