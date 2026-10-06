package cli

// Response capture for `api run`: a later request can use a value from an earlier
// one, `{{login.response.body.data.token}}`, the way REST Client / httpyac files
// do. The response is SERVER data in a FILE that may come from a repo, so:
//
//   - a captured value is data, never a template (httpfile.Options.IsLiteral): the
//     server cannot make `{{token}}` or `{{$env ...}}` expand inside it;
//   - it counts as a secret like {{token}}: a request to another host is refused
//     without --trust-hosts;
//   - the path into the body is a plain key/index path walked in Go, never jq
//     (jq could read your environment);
//   - it lives in memory for this invocation only: never in history, never written.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/httpfile"
)

// captured is the response of a request that already ran.
type captured struct {
	status int
	header http.Header
	body   []byte
}

var (
	// {{name.response.status}} / {{name.response.headers.X-Y}} / {{name.response.body[.path]}}
	captureRef = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\.response\.(status|headers\.([A-Za-z0-9-]+)|body(?:\.(.+))?)$`)
	// finds the request names a text depends on
	captureUse = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\.response\.`)
	// a body path: keys and [n] indexes only (an optional leading $.)
	bodyPath = regexp.MustCompile(`^(?:[A-Za-z0-9_-]+|\[\d+\])(?:\.[A-Za-z0-9_-]+|\[\d+\])*$`)
)

func isCaptureRef(expr string) bool { return captureRef.MatchString(expr) }

// dependencies returns the request names that texts take values from, in order.
func dependencies(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, m := range captureUse.FindAllStringSubmatch(t, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// value resolves one reference against a captured response.
func (c *captured) value(m []string) (string, error) {
	switch {
	case m[2] == "status":
		return strconv.Itoa(c.status), nil
	case strings.HasPrefix(m[2], "headers."):
		v := c.header.Get(m[3])
		if v == "" {
			return "", fmt.Errorf("the response has no %s header", m[3])
		}
		return v, nil
	}
	if m[4] == "" || m[4] == "$" {
		return string(c.body), nil
	}
	path := strings.TrimPrefix(strings.TrimPrefix(m[4], "$"), ".")
	if !bodyPath.MatchString(path) {
		return "", fmt.Errorf("body path %q: only keys and [n] indexes are allowed (like body.data.items[0].id)", m[4])
	}
	return walkBody(c.body, path)
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

// captureLookup resolves a {{name.response...}} reference. In a dry run nothing has
// been sent, so it yields a visible placeholder instead.
func (a *app) captureLookup(expr string, dry bool) (string, error) {
	m := captureRef.FindStringSubmatch(expr)
	if dry {
		return "<" + expr + ">", nil
	}
	c, ok := a.captured[m[1]]
	if !ok {
		return "", fmt.Errorf("{{%s}}: request %q has not run yet", expr, m[1])
	}
	v, err := c.value(m)
	if err != nil {
		return "", fmt.Errorf("{{%s}}: %w", expr, err)
	}
	return v, nil
}

func (a *app) remember(name string, resp *httpc.Response) {
	if name == "" {
		return
	}
	if a.captured == nil {
		a.captured = map[string]*captured{}
	}
	a.captured[name] = &captured{status: resp.Status, header: resp.Header, body: resp.Body}
}

// interpolateOptions wires captured values into httpfile: data, secret.
func interpolateOptions(lookup httpfile.Lookup, getenv func(string) (string, bool)) httpfile.Options {
	return httpfile.Options{
		Lookup:    lookup,
		Getenv:    getenv,
		IsSecret:  func(name string) bool { return name == "token" || isCaptureRef(name) },
		IsLiteral: isCaptureRef,
	}
}
