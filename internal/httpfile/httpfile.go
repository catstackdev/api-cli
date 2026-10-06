// Package httpfile parses .http / .rest request files (the format used by the
// JetBrains HTTP client and the VS Code REST Client):
//
//	@host = https://api.dev.example.com          file variable
//
//	### login                                    starts a request (name)
//	# @name login                                or an annotation
//	# @filter .data.token                        default jq filter
//	# @assert .status == 200                     must be true on the response (repeatable)
//	POST {{host}}/auth/login HTTP/1.1
//	Content-Type: application/json
//
//	{"user": "{{$env API_USER}}"}
//
// Variables: {{name}}, {{$env API_NAME}}, {{$uuid}}, {{$timestamp}},
// {{$isoTimestamp}}. Body `< ./file` reads the body from a file.
package httpfile

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Header is one request header.
type Header struct{ Name, Value string }

// Assert is one `# @assert <jq>` line: an expression over the response that
// must yield true (see package assert). Line is where it was written.
type Assert struct {
	Expr string
	Line int
}

// Request is one request block.
type Request struct {
	Name     string
	Method   string
	URL      string
	Headers  []Header
	Body     string
	BodyFile string // body read from this file (relative to the .http file)
	Filter   string
	Asserts  []Assert
	Line     int // 1-based line of the request line
}

// File is a parsed .http file.
type File struct {
	Vars     map[string]string
	Requests []Request
}

var (
	// "< ./file" or "<@ ./file": the space after < is what tells a file
	// reference from an HTML/XML body such as <html>.
	bodyFileLine = regexp.MustCompile(`^<@?[ \t]+(\S[^\n]*)$`)
	varLine      = regexp.MustCompile(`^@([A-Za-z_][A-Za-z0-9_.\-]*)\s*=\s*(.*)$`)
	annotation   = regexp.MustCompile(`^(?:#|//)\s*@([a-zA-Z]+)\s*(.*)$`)
	methods      = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}
)

// Parse reads a .http file.
func Parse(data []byte) (*File, error) {
	f := &File{Vars: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	type block struct {
		name      string
		filter    string
		lines     []string
		firstLine int
	}
	var blocks []block
	cur := block{firstLine: 1}
	flush := func() {
		if len(cur.lines) > 0 || cur.name != "" {
			blocks = append(blocks, cur)
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "###") {
			flush()
			cur = block{name: strings.TrimSpace(strings.TrimLeft(l, "#")), firstLine: i + 2}
			continue
		}
		cur.lines = append(cur.lines, fmt.Sprintf("%d\t%s", i+1, l))
	}
	flush()

	for _, b := range blocks {
		req := Request{Name: b.name}
		state := "start" // start -> headers -> body
		var body []string
		for _, packed := range b.lines {
			num, l, _ := strings.Cut(packed, "\t")
			n, _ := strconv.Atoi(num)
			trimmed := strings.TrimSpace(l)
			switch state {
			case "start":
				switch {
				case trimmed == "":
				case annotationMatch(trimmed, &req, n):
				case strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//"):
				case varLine.MatchString(trimmed):
					m := varLine.FindStringSubmatch(trimmed)
					f.Vars[m[1]] = strings.TrimSpace(m[2])
				default:
					method, url, err := requestLine(trimmed)
					if err != nil {
						return nil, fmt.Errorf("line %d: %w", n, err)
					}
					req.Method, req.URL, req.Line = method, url, n
					state = "headers"
				}
			case "headers":
				switch {
				case trimmed == "":
					state = "body"
				case (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && (strings.HasPrefix(trimmed, "?") || strings.HasPrefix(trimmed, "&")):
					req.URL += trimmed // multi-line query string
				case strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//"):
				default:
					name, value, ok := strings.Cut(l, ":")
					if !ok || strings.TrimSpace(name) == "" {
						return nil, fmt.Errorf("line %d: expected \"Name: value\" header, got %q", n, trimmed)
					}
					req.Headers = append(req.Headers, Header{strings.TrimSpace(name), strings.TrimSpace(value)})
				}
			case "body":
				body = append(body, l)
			}
		}
		for _, as := range req.Asserts {
			if as.Expr == "" {
				return nil, fmt.Errorf("line %d: @assert needs an expression, e.g. # @assert .status == 200", as.Line)
			}
		}
		if req.Method == "" {
			if req.Name != "" || len(strings.TrimSpace(strings.Join(body, ""))) > 0 {
				return nil, fmt.Errorf("line %d: request %q has no request line (METHOD URL)", b.firstLine, req.Name)
			}
			continue
		}
		text := strings.TrimRight(strings.Join(body, "\n"), "\n \t")
		text = strings.TrimLeft(text, "\n")
		if m := bodyFileLine.FindStringSubmatch(text); m != nil {
			req.BodyFile = strings.TrimSpace(m[1])
		} else {
			req.Body = text
		}
		if req.Name == "" {
			req.Name = fmt.Sprintf("request-%d", len(f.Requests)+1)
		}
		f.Requests = append(f.Requests, req)
	}
	return f, nil
}

func annotationMatch(line string, req *Request, lineNo int) bool {
	m := annotation.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	switch strings.ToLower(m[1]) {
	case "name":
		req.Name = strings.TrimSpace(m[2])
	case "filter":
		req.Filter = strings.TrimSpace(m[2])
	case "assert":
		req.Asserts = append(req.Asserts, Assert{Expr: strings.TrimSpace(m[2]), Line: lineNo})
	default:
		return false
	}
	return true
}

func requestLine(l string) (method, url string, err error) {
	fields := strings.Fields(l)
	if len(fields) > 1 && strings.HasPrefix(strings.ToUpper(fields[len(fields)-1]), "HTTP/") {
		fields = fields[:len(fields)-1]
	}
	switch {
	case len(fields) == 1:
		return "GET", fields[0], nil
	case len(fields) == 2 && methods[strings.ToUpper(fields[0])]:
		return strings.ToUpper(fields[0]), fields[1], nil
	}
	return "", "", fmt.Errorf("bad request line %q: want METHOD URL", l)
}

// Find returns the request with the given name (case-insensitive) or 1-based
// index ("2" or "#2").
func (f *File) Find(sel string) (*Request, error) {
	if len(f.Requests) == 0 {
		return nil, fmt.Errorf("no requests in the file")
	}
	if sel == "" {
		if len(f.Requests) == 1 {
			return &f.Requests[0], nil
		}
		return nil, fmt.Errorf("%d requests: pick one by name or number (%s)", len(f.Requests), f.names())
	}
	for i := range f.Requests {
		if strings.EqualFold(f.Requests[i].Name, sel) {
			return &f.Requests[i], nil
		}
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(sel, "#")); err == nil && n >= 1 && n <= len(f.Requests) {
		return &f.Requests[n-1], nil
	}
	return nil, fmt.Errorf("no request %q (have: %s)", sel, f.names())
}

func (f *File) names() string {
	names := make([]string, len(f.Requests))
	for i, r := range f.Requests {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// Lookup resolves a variable name; ok=false means unknown.
type Lookup func(name string) (value string, ok bool)

var placeholder = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Options tune Interpolate.
type Options struct {
	Lookup Lookup
	// IsSecret names variables that can carry a secret ({{token}}).
	IsSecret func(string) bool
	// Getenv reads {{$env NAME}}; callers restrict which names are readable.
	Getenv func(string) (string, bool)
	// IsLiteral names variables whose value is DATA, never a template: it is
	// inserted as is. Values that came from a server response must be literal,
	// or the server could make `{{token}}` or `{{$env ...}}` expand inside them.
	IsLiteral func(string) bool
}

// Interpolate replaces {{...}} in s. usedSecret reports whether the result
// includes anything that can carry a secret ({{$env ...}}); callers add their
// own knowledge of which variables are secret via isSecretVar.
func Interpolate(s string, lookup Lookup, isSecretVar func(string) bool, getenv func(string) (string, bool)) (out string, usedSecret bool, err error) {
	return InterpolateWith(s, Options{Lookup: lookup, IsSecret: isSecretVar, Getenv: getenv})
}

// InterpolateWith is Interpolate with every option.
func InterpolateWith(s string, o Options) (out string, usedSecret bool, err error) {
	return interpolate(s, o, 0)
}

func interpolate(s string, o Options, depth int) (string, bool, error) {
	if depth > 5 {
		return "", false, fmt.Errorf("variables nest too deeply (a cycle?)")
	}
	var firstErr error
	secret := false
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		expr := strings.TrimSpace(placeholder.FindStringSubmatch(m)[1])
		var val string
		switch {
		case expr == "$uuid":
			val = uuid4()
		case expr == "$timestamp":
			val = strconv.FormatInt(time.Now().Unix(), 10)
		case expr == "$isoTimestamp":
			val = time.Now().UTC().Format(time.RFC3339)
		case strings.HasPrefix(expr, "$env "):
			name := strings.TrimSpace(strings.TrimPrefix(expr, "$env "))
			v, ok := o.Getenv(name)
			if !ok {
				if firstErr == nil {
					firstErr = fmt.Errorf("{{$env %s}}: not allowed or not set (only API_* variables can be read)", name)
				}
				return m
			}
			secret = true
			val = v
		case strings.HasPrefix(expr, "$"):
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown dynamic variable {{%s}}", expr)
			}
			return m
		default:
			v, ok := o.Lookup(expr)
			if !ok {
				if firstErr == nil {
					firstErr = fmt.Errorf("undefined variable {{%s}} (define it with @%s = ... or --var %s=...)", expr, expr, expr)
				}
				return m
			}
			if o.IsSecret != nil && o.IsSecret(expr) {
				secret = true
			}
			if o.IsLiteral != nil && o.IsLiteral(expr) {
				val = v
				break
			}
			inner, innerSecret, err := interpolate(v, o, depth+1)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			secret = secret || innerSecret
			val = inner
		}
		return val
	})
	return out, secret, firstErr
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
