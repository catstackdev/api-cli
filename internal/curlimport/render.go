package curlimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"cybercat/api/internal/secret"
)

// Context is what the session knows, used to write {{base}} / {{graphql}}
// instead of a hard-coded server, and to say which configured envs describe
// the same server. Matching is by origin (the caller computes EnvNames), never
// by name.
type Context struct {
	Base, GraphQL string
	EnvNames      []string
}

// Result is the generated .http text plus what a caller may want to tell the
// user.
type Result struct {
	HTTP    string
	GraphQL bool
	// Vars are the API_* variables the file expects (names only, never values).
	Vars []string
}

// browserNoise are headers a browser adds that mean nothing to an API call.
var browserNoise = map[string]bool{
	"user-agent": true, "referer": true, "origin": true, "accept-language": true,
	"accept-encoding": true, "connection": true, "host": true, "content-length": true,
	"priority": true, "dnt": true, "cache-control": true, "pragma": true,
	"upgrade-insecure-requests": true, "te": true,
	// cache validators: sent from here they turn a 200 into an empty 304
	"if-none-match": true, "if-modified-since": true, "if-match": true,
	"if-unmodified-since": true, "if-range": true,
}

var (
	// must start with a letter or digit: a name like "###x" would make its line a
	// new request in the .http file
	headerName = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9!#$%&'*+.^_`|~-]*$")
	opRe       = regexp.MustCompile(`(?m)^\s*(query|mutation|subscription)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	opKindRe   = regexp.MustCompile(`^\s*(query|mutation|subscription)\b`)
	safeName   = regexp.MustCompile(`[^A-Za-z0-9 _./:-]+`)
	envNameRe  = regexp.MustCompile(`[^A-Z0-9]+`)
)

// Render writes the request as one .http block. Every value that looks like a
// secret becomes a reference; the original text is not kept anywhere.
func Render(r Request, c Context) (Result, error) {
	var res Result
	vars := map[string]bool{}
	secretVar := func(name string) string {
		v := "API_" + strings.Trim(envNameRe.ReplaceAllString(strings.ToUpper(name), "_"), "_")
		vars[v] = true
		return "{{$env " + v + "}}"
	}
	var dropped, notes []string

	// headers
	var headers []Header
	for _, h := range r.Headers {
		lower := strings.ToLower(h.Name)
		switch {
		case !headerName.MatchString(h.Name):
			dropped = append(dropped, "a header with an invalid name")
		case browserNoise[lower] || strings.HasPrefix(lower, "sec-"):
			dropped = append(dropped, lower)
		case lower == "cookie":
			// a cookie is the BROWSER's session, not your API credential (that is
			// {{token}}): it expires with the tab and would need its own variable
			dropped = append(dropped, "cookie (browser session)")
		case lower == "authorization":
			scheme, rest, _ := strings.Cut(h.Value, " ")
			switch {
			case strings.EqualFold(scheme, "bearer") && placeholderToken(rest):
				dropped = append(dropped, "authorization (empty bearer token)")
			case strings.EqualFold(scheme, "bearer"):
				headers = append(headers, Header{h.Name, "Bearer {{token}}"})
				notes = append(notes, "authorization → {{token}} (your session token; run api login)")
			default:
				headers = append(headers, Header{h.Name, secretVar(h.Name)})
			}
		case secret.Name(h.Name):
			headers = append(headers, Header{h.Name, secretVar(h.Name)})
		default:
			headers = append(headers, Header{h.Name, h.Value})
		}
	}
	if r.BasicUser != "" {
		headers = append(headers, Header{"Authorization", "Basic " + secretVar("basic_auth")})
		notes = append(notes, "basic auth: API_BASIC_AUTH is base64(user:password), build it yourself")
	}

	// body
	ctype := ""
	for _, h := range headers {
		if strings.EqualFold(h.Name, "content-type") {
			ctype = strings.ToLower(h.Value)
		}
	}
	body := ""
	opName, opKind := "", ""
	if r.HasBody {
		if strings.Contains(r.Body, "{{") {
			return res, fmt.Errorf("the body contains {{ which the .http runner would expand")
		}
		switch {
		case json.Valid([]byte(r.Body)):
			var err error
			if body, err = maskJSON(r.Body, secretVar); err != nil {
				return res, err
			}
			var top map[string]any
			if json.Unmarshal([]byte(r.Body), &top) == nil {
				if q, ok := top["query"].(string); ok {
					res.GraphQL = true
					if m := opKindRe.FindStringSubmatch(q); m != nil {
						opKind = m[1]
					} else {
						opKind = "query"
					}
					if n, ok := top["operationName"].(string); ok && n != "" {
						opName = n
					} else if m := opRe.FindStringSubmatch(q); m != nil {
						opName = m[2]
					}
				}
			}
		case strings.Contains(ctype, "x-www-form-urlencoded"):
			if err := checkText("body", r.Body); err != nil {
				return res, err
			}
			if strings.HasPrefix(r.Body, "#") || strings.HasPrefix(r.Body, "//") {
				return res, fmt.Errorf("a body starting with # or // would read as a comment or a new request in a .http file")
			}
			body = maskForm(r.Body, secretVar)
		default:
			if strings.ContainsAny(r.Body, "\r\n") {
				return res, fmt.Errorf("a multi-line body that is not JSON cannot be written safely to a .http file: save it to a file and use `< ./file`")
			}
			if err := checkText("body", r.Body); err != nil {
				return res, err
			}
			if strings.HasPrefix(r.Body, "#") || strings.HasPrefix(r.Body, "//") {
				return res, fmt.Errorf("a body starting with # or // would read as a comment or a new request in a .http file")
			}
			body = r.Body
			notes = append(notes, "body copied as is (not JSON or a form): check it for secrets")
		}
	}

	target := maskQuery(rewrite(r.URL, c), secretVar)

	// name and header comments
	name := opName
	if name == "" {
		path := r.URL
		if u, err := url.Parse(r.URL); err == nil {
			path = u.Path
		}
		name = r.Method + " " + path
	}
	name = strings.TrimSpace(safeName.ReplaceAllString(name, ""))
	if name == "" {
		name = "imported"
	}

	var b strings.Builder
	kind := "REST"
	if res.GraphQL {
		kind = "graphql " + opKind
		if opName != "" {
			kind += " " + opName
		}
	}
	fmt.Fprintf(&b, "### %s\n", name)
	head := "from curl · " + kind
	if len(c.EnvNames) > 0 {
		head += " · env " + strings.Join(c.EnvNames, ", ")
	}
	fmt.Fprintf(&b, "# %s\n", comment(head))
	if len(dropped) > 0 {
		fmt.Fprintf(&b, "# dropped: %s\n", comment(summarizeDropped(dropped)))
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "# %s\n", comment(n))
	}
	for _, v := range sortedKeys(vars) {
		fmt.Fprintf(&b, "# needs %s (set it in your shell)\n", v)
	}
	res.Vars = sortedKeys(vars)

	fmt.Fprintf(&b, "%s %s\n", r.Method, target)
	for _, h := range headers {
		fmt.Fprintf(&b, "%s: %s\n", h.Name, h.Value)
	}
	if body != "" {
		fmt.Fprintf(&b, "\n%s\n", body)
	}
	res.HTTP = b.String()
	return res, nil
}

// comment makes text safe for a "# ..." line: no control characters and nothing
// that could read as an annotation (# @name, # @assert).
func comment(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "@", "(at)")
}

func placeholderToken(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "null", "undefined", "false", "none":
		return true
	}
	return false
}

// rewrite replaces the session's server with {{graphql}} / {{base}} so the file
// follows the active env. A URL on another host is kept whole (and the runner
// sends it no token).
func rewrite(u string, c Context) string {
	for _, p := range []struct{ prefix, v string }{{c.GraphQL, "{{graphql}}"}, {c.Base, "{{base}}"}} {
		pre := strings.TrimRight(p.prefix, "/")
		if pre == "" || !strings.HasPrefix(u, pre) {
			continue
		}
		rest := u[len(pre):]
		if rest == "" || rest[0] == '/' || rest[0] == '?' {
			return p.v + rest
		}
	}
	return u
}

// maskQuery replaces the value of secret-looking query parameters.
func maskQuery(u string, secretVar func(string) string) string {
	base, q, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	return base + "?" + maskForm(q, secretVar)
}

func maskForm(s string, secretVar func(string) string) string {
	parts := strings.Split(s, "&")
	for i, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if ok && v != "" && secret.Param(k) {
			parts[i] = k + "=" + secretVar(k)
		}
	}
	return strings.Join(parts, "&")
}

// maskJSON re-emits a JSON document, in the same key order, with the scalar
// value of every secret-looking key replaced by a reference. It works on the
// token stream because decoding into a map would reorder the keys.
func maskJSON(body string, secretVar func(string) string) (string, error) {
	type frame struct {
		obj     bool
		n       int
		key     string
		wantKey bool
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var out bytes.Buffer
	var stack []frame
	enc := func(v any) string {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		_ = e.Encode(v)
		return strings.TrimRight(b.String(), "\n")
	}
	top := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return &stack[len(stack)-1]
	}
	sep := func() {
		if f := top(); f != nil {
			if f.n > 0 {
				out.WriteByte(',')
			}
			f.n++
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		f := top()
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				if f != nil && !f.obj {
					sep()
				}
				out.WriteRune(rune(d))
				stack = append(stack, frame{obj: d == '{', wantKey: d == '{'})
				continue
			}
			stack = stack[:len(stack)-1]
			out.WriteRune(rune(d))
			if p := top(); p != nil && p.obj {
				p.wantKey = true
			}
			continue
		}
		if f != nil && f.obj && f.wantKey {
			sep()
			out.WriteString(enc(tok))
			out.WriteByte(':')
			f.key, f.wantKey = tok.(string), false
			continue
		}
		if f != nil && !f.obj {
			sep()
		}
		_, isStr := tok.(string)
		_, isNum := tok.(json.Number)
		switch {
		case f != nil && f.obj && secret.Name(f.key) && (isNum || (isStr && tok.(string) != "")):
			out.WriteString(enc(secretVar(f.key)))
		case isStr && len(stack) == 1 && f.obj && f.key == "query":
			out.WriteString(enc(tidyGraphQL(tok.(string))))
		default:
			out.WriteString(enc(tok))
		}
		if f != nil && f.obj {
			f.wantKey = true
		}
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out.Bytes(), "", "  "); err != nil {
		return "", fmt.Errorf("could not rewrite the JSON body: %w", err)
	}
	return pretty.String(), nil
}

// tidyGraphQL makes a query readable on one JSON line: DevTools sends it with
// its source indentation ("\n    mutation ...\n  "). Whitespace is only
// collapsed when that cannot change the meaning: a query with a string literal
// or a # comment (which would swallow the rest of a joined line) is only trimmed.
func tidyGraphQL(q string) string {
	if strings.ContainsAny(q, `"#`) {
		return strings.TrimSpace(q)
	}
	return strings.Join(strings.Fields(q), " ")
}

// summarizeDropped groups the sec-* browser headers: "sec-* (3), referer, ...".
func summarizeDropped(in []string) string {
	sec := 0
	var rest []string
	for _, d := range unique(in) {
		if strings.HasPrefix(d, "sec-") {
			sec++
			continue
		}
		rest = append(rest, d)
	}
	if sec > 0 {
		rest = append([]string{fmt.Sprintf("sec-* (%d)", sec)}, rest...)
	}
	return strings.Join(rest, ", ")
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
