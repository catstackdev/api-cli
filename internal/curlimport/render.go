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
	// Redundant holds the headers the matching env profile already sends
	// (lower-cased name -> value): the same header in the request is not needed.
	Redundant map[string]string
	// Keep names headers (lower-cased) to keep even if they would be dropped.
	Keep map[string]bool
	// Minimal drops every header that is not recognised, instead of flagging it.
	Minimal bool
}

// Result is the generated .http text plus what a caller may want to tell the
// user.
type Result struct {
	HTTP    string
	GraphQL bool
	// Check lists headers that were kept but not recognised: the user may prune them.
	Check []string
	// Vars are the API_* variables the file expects (names only, never values).
	Vars []string
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
	g := &renderer{c: c, vars: map[string]bool{}}

	headers := g.headers(r)
	res.Check = g.check
	body, err := g.body(r, headers)
	if err != nil {
		return res, err
	}
	res.GraphQL = body.graphql
	// masking the query can add a variable too, so it comes before Vars is read
	target := maskQuery(rewrite(r.URL, c), g.secretVar)
	res.Vars = sortedKeys(g.vars)
	res.HTTP = g.document(r, target, headers, body, res)
	return res, nil
}

// renderer collects what the pieces of a request report while it is written:
// the variables it needs, the notes and the headers that were left out.
type renderer struct {
	c       Context
	vars    map[string]bool
	notes   []string
	dropped dropLog
	check   []string // headers kept but not recognised
}

// secretVar is the reference that stands in for a secret value.
func (g *renderer) secretVar(name string) string {
	v := "API_" + strings.Trim(envNameRe.ReplaceAllString(strings.ToUpper(name), "_"), "_")
	g.vars[v] = true
	return "{{$env " + v + "}}"
}

// headers keeps, rewrites or drops each header, in the order given.
func (g *renderer) headers(r Request) []Header {
	var headers []Header
	for _, h := range r.Headers {
		lower := strings.ToLower(h.Name)
		if !headerName.MatchString(h.Name) {
			g.dropped.add("invalid name", "a header")
			continue
		}
		d := classify(lower, h.Value, g.c)
		switch d.v {
		case drop:
			g.dropped.add(d.group, lower)
		case auth:
			scheme, rest, _ := strings.Cut(h.Value, " ")
			switch {
			case strings.EqualFold(scheme, "bearer") && placeholderToken(rest):
				g.dropped.add("auth", "authorization (empty bearer token)")
			case strings.EqualFold(scheme, "bearer"):
				headers = append(headers, Header{h.Name, "Bearer {{token}}"})
				g.notes = append(g.notes, "authorization → {{token}} (your session token; run api login)")
			default:
				headers = append(headers, Header{h.Name, g.secretVar(h.Name)})
			}
		case secretH:
			headers = append(headers, Header{h.Name, g.secretVar(h.Name)})
		case check:
			headers = append(headers, Header{h.Name, h.Value})
			g.check = append(g.check, lower)
		default:
			headers = append(headers, Header{h.Name, h.Value})
		}
	}
	if r.BasicUser != "" {
		headers = append(headers, Header{"Authorization", "Basic " + g.secretVar("basic_auth")})
		g.notes = append(g.notes, "basic auth: API_BASIC_AUTH is base64(user:password), build it yourself")
	}
	return headers
}

// renderedBody is the body to write and, for GraphQL, what to name the request after.
type renderedBody struct {
	text           string
	graphql        bool
	opName, opKind string
}

// body masks the request body according to its kind (JSON, form, anything else)
// and refuses what the runner would read as structure.
func (g *renderer) body(r Request, headers []Header) (renderedBody, error) {
	var out renderedBody
	if !r.HasBody {
		return out, nil
	}
	if strings.Contains(r.Body, "{{") {
		return out, fmt.Errorf("the body contains {{ which the .http runner would expand")
	}
	ctype := ""
	for _, h := range headers {
		if strings.EqualFold(h.Name, "content-type") {
			ctype = strings.ToLower(h.Value)
		}
	}
	switch {
	case json.Valid([]byte(r.Body)):
		text, err := maskJSON(r.Body, g.secretVar)
		if err != nil {
			return out, err
		}
		out.text = text
		out.graphqlOperation(r.Body)
	case strings.Contains(ctype, "x-www-form-urlencoded"):
		if err := checkPlainBody(r.Body); err != nil {
			return out, err
		}
		out.text = maskForm(r.Body, g.secretVar)
	default:
		if strings.ContainsAny(r.Body, "\r\n") {
			return out, fmt.Errorf("a multi-line body that is not JSON cannot be written safely to a .http file: save it to a file and use `< ./file`")
		}
		if err := checkPlainBody(r.Body); err != nil {
			return out, err
		}
		out.text = r.Body
		g.notes = append(g.notes, "body copied as is (not JSON or a form): check it for secrets")
	}
	return out, nil
}

// checkPlainBody refuses a body that cannot be written as plain text: control
// characters, or a first line the .http file would read as a comment or a new request.
func checkPlainBody(body string) error {
	if err := checkText("body", body); err != nil {
		return err
	}
	if strings.HasPrefix(body, "#") || strings.HasPrefix(body, "//") {
		return fmt.Errorf("a body starting with # or // would read as a comment or a new request in a .http file")
	}
	return nil
}

// graphqlOperation recognises a {"query": ...} body and records its kind and name.
func (b *renderedBody) graphqlOperation(raw string) {
	var top map[string]any
	if json.Unmarshal([]byte(raw), &top) != nil {
		return
	}
	q, ok := top["query"].(string)
	if !ok {
		return
	}
	b.graphql = true
	if m := opKindRe.FindStringSubmatch(q); m != nil {
		b.opKind = m[1]
	} else {
		b.opKind = "query"
	}
	if n, ok := top["operationName"].(string); ok && n != "" {
		b.opName = n
	} else if m := opRe.FindStringSubmatch(q); m != nil {
		b.opName = m[2]
	}
}

// document lays the request out: the name, the comments that explain what was
// changed, then the request line, headers and body.
func (g *renderer) document(r Request, target string, headers []Header, body renderedBody, res Result) string {
	name := body.opName
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
	if body.graphql {
		kind = "graphql " + body.opKind
		if body.opName != "" {
			kind += " " + body.opName
		}
	}
	fmt.Fprintf(&b, "### %s\n", name)
	head := "from curl · " + kind
	if len(g.c.EnvNames) > 0 {
		head += " · env " + strings.Join(g.c.EnvNames, ", ")
	}
	fmt.Fprintf(&b, "# %s\n", comment(head))
	if !g.dropped.empty() {
		fmt.Fprintf(&b, "# dropped: %s\n", comment(g.dropped.String()))
	}
	if len(res.Check) > 0 {
		fmt.Fprintf(&b, "# check: %s (not recognised, kept: delete if the request works without it)\n", comment(strings.Join(unique(res.Check), ", ")))
	}
	for _, n := range g.notes {
		fmt.Fprintf(&b, "# %s\n", comment(n))
	}
	for _, v := range res.Vars {
		fmt.Fprintf(&b, "# needs %s (set it in your shell)\n", v)
	}

	fmt.Fprintf(&b, "%s %s\n", r.Method, target)
	for _, h := range headers {
		fmt.Fprintf(&b, "%s: %s\n", h.Name, h.Value)
	}
	if body.text != "" {
		fmt.Fprintf(&b, "\n%s\n", body.text)
	}
	return b.String()
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
