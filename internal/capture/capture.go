// Package capture resolves the {{name.response.*}} references of `api run`: a
// later request takes a value from the response of an earlier one, the way REST
// Client / httpyac files do. The response is SERVER data in a FILE that may come
// from a repo, so:
//
//   - the path into the body is a plain key/index path walked here, never jq (jq
//     could read your environment);
//   - nothing here expands a template: the caller inserts the value as a literal
//     and counts it as a secret (httpfile.Options.IsLiteral / IsSecret);
//   - a response lives in memory for one invocation: this package stores nothing.
package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Response is the response of a request that already ran.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Ref is a parsed {{name.response...}} reference.
type Ref struct {
	Name   string // the request it takes a value from
	what   string // "status", "headers.X" or "body[.path]"
	header string // X of headers.X
	path   string // the path after body., "" for the whole body
}

var (
	// {{name.response.status}} / {{name.response.headers.X-Y}} / {{name.response.body[.path]}}
	refRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\.response\.(status|headers\.([A-Za-z0-9-]+)|body(?:\.(.+))?)$`)
	// finds the request names a text depends on
	useRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\.response\.`)
	// a body path: keys and [n] indexes only (an optional leading $.)
	bodyPath = regexp.MustCompile(`^(?:[A-Za-z0-9_-]+|\[\d+\])(?:\.[A-Za-z0-9_-]+|\[\d+\])*$`)
)

// IsRef reports whether expr, the inside of a {{...}}, is a response reference.
func IsRef(expr string) bool { return refRe.MatchString(expr) }

// ParseRef parses the inside of a {{...}}.
func ParseRef(expr string) (Ref, bool) {
	m := refRe.FindStringSubmatch(expr)
	if m == nil {
		return Ref{}, false
	}
	return Ref{Name: m[1], what: m[2], header: m[3], path: m[4]}, true
}

// Dependencies returns the request names that texts take values from, in order.
func Dependencies(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, m := range useRe.FindAllStringSubmatch(t, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// Value resolves one reference against the response.
func (c *Response) Value(r Ref) (string, error) {
	switch {
	case r.what == "status":
		return strconv.Itoa(c.Status), nil
	case strings.HasPrefix(r.what, "headers."):
		v := c.Header.Get(r.header)
		if v == "" {
			return "", fmt.Errorf("the response has no %s header", r.header)
		}
		return v, nil
	}
	if r.path == "" || r.path == "$" {
		return string(c.Body), nil
	}
	path := strings.TrimPrefix(strings.TrimPrefix(r.path, "$"), ".")
	if !bodyPath.MatchString(path) {
		return "", fmt.Errorf("body path %q: only keys and [n] indexes are allowed (like body.data.items[0].id)", r.path)
	}
	return walkBody(c.Body, path)
}

func walkBody(body []byte, path string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("the response body is not JSON")
	}
	rest := path
	for rest != "" {
		var key string
		switch {
		case rest[0] == '[':
			end := strings.IndexByte(rest, ']')
			idx, _ := strconv.Atoi(rest[1:end])
			arr, ok := v.([]any)
			if !ok || idx >= len(arr) {
				return "", fmt.Errorf("nothing at %s", path)
			}
			v, rest = arr[idx], rest[end+1:]
		default:
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			key, rest = rest[:end], rest[end:]
			obj, ok := v.(map[string]any)
			if !ok {
				return "", fmt.Errorf("nothing at %s", path)
			}
			if v, ok = obj[key]; !ok {
				return "", fmt.Errorf("nothing at %s", path)
			}
		}
		rest = strings.TrimPrefix(rest, ".")
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	case nil:
		return "", fmt.Errorf("%s is null", path)
	}
	b, _ := json.Marshal(v)
	return string(b), nil
}
