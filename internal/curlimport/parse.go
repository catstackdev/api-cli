// Package curlimport turns a pasted curl command (typically "Copy as cURL"
// from browser DevTools) into a .http request.
//
// The input is untrusted text. Nothing here runs a shell, reads a file, expands
// a variable or touches the network: the command is only TOKENISED and a small
// subset of curl's flags is understood. Anything that could end up inside the
// generated .http file as structure (a line break in a header, a `###` line in
// a body, a `{{...}}` the runner would expand) is refused, and every
// secret-looking value is replaced by a reference, never copied.
package curlimport

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Header is one request header, in the order it was given.
type Header struct{ Name, Value string }

// Request is what a curl command asked for.
type Request struct {
	Method  string
	URL     string
	Headers []Header
	Body    string
	HasBody bool
	// BasicUser is the -u argument ("user:pass"); it is never written out.
	BasicUser string
	// Ignored lists flags that were understood but have no effect here.
	Ignored []string
}

// Parse reads one curl command.
func Parse(input string) (Request, error) {
	words, err := tokenize(input)
	if err != nil {
		return Request{}, err
	}
	if len(words) > 0 && words[0] == "curl" {
		words = words[1:]
	}
	var r Request
	var data []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		value := func() (string, error) {
			if i+1 >= len(words) {
				return "", fmt.Errorf("%s needs a value", w)
			}
			i++
			return words[i], nil
		}
		switch w {
		case "-X", "--request":
			v, err := value()
			if err != nil {
				return r, err
			}
			if !validMethod(v) {
				return r, fmt.Errorf("unsupported method %q", v)
			}
			r.Method = strings.ToUpper(v)
		case "-H", "--header":
			v, err := value()
			if err != nil {
				return r, err
			}
			name, val, ok := strings.Cut(v, ":")
			name, val = strings.TrimSpace(name), strings.TrimSpace(val)
			if !ok || name == "" {
				r.Ignored = append(r.Ignored, "header without a name")
				continue
			}
			r.Headers = append(r.Headers, Header{name, val})
		case "-d", "--data", "--data-raw", "--data-binary", "--data-ascii":
			v, err := value()
			if err != nil {
				return r, err
			}
			if strings.HasPrefix(v, "@") && w != "--data-raw" {
				return r, fmt.Errorf("%s @file is not supported (the file is not read): paste the body instead", w)
			}
			data = append(data, v)
		case "--json":
			v, err := value()
			if err != nil {
				return r, err
			}
			data = append(data, v)
			r.Headers = append(r.Headers, Header{"Content-Type", "application/json"}, Header{"Accept", "application/json"})
		case "-u", "--user":
			v, err := value()
			if err != nil {
				return r, err
			}
			r.BasicUser = v
		case "-b", "--cookie":
			v, err := value()
			if err != nil {
				return r, err
			}
			r.Headers = append(r.Headers, Header{"Cookie", v})
		case "--url":
			v, err := value()
			if err != nil {
				return r, err
			}
			if err := r.setURL(v); err != nil {
				return r, err
			}
		case "-A", "--user-agent", "-e", "--referer":
			if _, err := value(); err != nil {
				return r, err
			}
			r.Ignored = append(r.Ignored, w)
		case "--compressed", "-s", "--silent", "-S", "--show-error", "-L", "--location",
			"-i", "--include", "-v", "--verbose", "-g", "--globoff", "--http1.1", "--http2",
			"-f", "--fail", "-G":
			r.Ignored = append(r.Ignored, w)
		case "-k", "--insecure":
			r.Ignored = append(r.Ignored, w+" (TLS verification stays on)")
		case "-F", "--form", "-T", "--upload-file", "--data-urlencode", "-o", "--output", "-O", "-K", "--config", "--proxy", "-x":
			return r, fmt.Errorf("%s is not supported", w)
		default:
			switch {
			case strings.HasPrefix(w, "-") && len(w) > 1:
				if !isBoolBundle(w) {
					return r, fmt.Errorf("unsupported flag %s", w)
				}
				r.Ignored = append(r.Ignored, w)
			default:
				if err := r.setURL(w); err != nil {
					return r, err
				}
			}
		}
	}
	if r.URL == "" {
		return r, fmt.Errorf("no URL in the command")
	}
	if len(data) > 0 {
		r.Body, r.HasBody = strings.Join(data, "&"), true
	}
	if r.Method == "" {
		r.Method = "GET"
		if r.HasBody {
			r.Method = "POST"
		}
	}
	for _, h := range r.Headers {
		if err := checkText("header", h.Name+h.Value); err != nil {
			return r, err
		}
	}
	if err := checkText("URL", r.URL); err != nil {
		return r, err
	}
	return r, nil
}

func (r *Request) setURL(u string) error {
	if r.URL != "" {
		return fmt.Errorf("more than one URL")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return fmt.Errorf("only absolute http(s) URLs are supported: %q", clip(u))
	}
	r.URL = u
	return nil
}

// checkText refuses what would break out of a single .http line, or that the
// runner would expand as a variable. A pasted command must not be able to add a
// request, a header or a {{$env ...}} to the file we write.
func checkText(what, s string) error {
	for _, c := range s {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return fmt.Errorf("a %s contains a line break or control character", what)
		}
	}
	if strings.Contains(s, "{{") {
		return fmt.Errorf("a %s contains {{ which the .http runner would expand", what)
	}
	return nil
}

func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for _, c := range m {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// isBoolBundle: -sSL and friends, flags that take no value.
func isBoolBundle(w string) bool {
	if strings.HasPrefix(w, "--") {
		return false
	}
	for _, c := range w[1:] {
		if !strings.ContainsRune("sSLikvgfG", c) {
			return false
		}
	}
	return true
}

func clip(s string) string {
	if utf8.RuneCountInString(s) > 60 {
		return string([]rune(s)[:60]) + "..."
	}
	return s
}

// tokenize splits the command like a POSIX shell would, without ever evaluating
// anything: $VAR and $(...) stay literal text, and unquoted pipes, redirects,
// ; and & are refused. It understands 'single', "double" and $'ANSI-C' quoting
// (what DevTools emits) and backslash-newline continuations.
func tokenize(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '\\':
			if i+1 < len(rs) && (rs[i+1] == '\n' || rs[i+1] == '\r') {
				i++
				if rs[i] == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
					i++
				}
				continue
			}
			if i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
				inWord = true
			}
		case c == '\'':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				cur.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated ' quote")
			}
			i = j
		case c == '"':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				if rs[j] == '\\' && j+1 < len(rs) {
					switch n := rs[j+1]; n {
					case '"', '\\', '$', '`':
						cur.WriteRune(n)
						j += 2
						continue
					case '\n':
						j += 2
						continue
					}
				}
				cur.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated \" quote")
			}
			i = j
		case c == '$' && i+1 < len(rs) && rs[i+1] == '\'':
			inWord = true
			j, err := ansiC(rs, i+2, &cur)
			if err != nil {
				return nil, err
			}
			i = j
		case c == '|' || c == ';' || c == '&' || c == '<' || c == '>':
			return nil, fmt.Errorf("pipes, redirects and ; & are not supported: paste one plain curl command")
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	flush()
	return words, nil
}

// ansiC reads a $'...' string starting after the opening quote and returns the
// index of the closing quote.
func ansiC(rs []rune, i int, out *strings.Builder) (int, error) {
	for ; i < len(rs); i++ {
		c := rs[i]
		if c == '\'' {
			return i, nil
		}
		if c != '\\' {
			out.WriteRune(c)
			continue
		}
		i++
		if i >= len(rs) {
			break
		}
		switch e := rs[i]; e {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case 'a':
			out.WriteByte(7)
		case 'b':
			out.WriteByte(8)
		case 'e', 'E':
			out.WriteByte(27)
		case 'f':
			out.WriteByte(12)
		case 'v':
			out.WriteByte(11)
		case '\\', '\'', '"', '?':
			out.WriteRune(e)
		case 'x':
			n, used := hexRun(rs[i+1:], 2)
			if used == 0 {
				return 0, fmt.Errorf("bad \\x escape")
			}
			out.WriteByte(byte(n))
			i += used
		case 'u', 'U':
			max := 4
			if e == 'U' {
				max = 8
			}
			n, used := hexRun(rs[i+1:], max)
			if used == 0 {
				return 0, fmt.Errorf("bad \\%c escape", e)
			}
			out.WriteRune(rune(n))
			i += used
		case '0', '1', '2', '3', '4', '5', '6', '7':
			n, used := 0, 0
			for used < 3 && i+used < len(rs) && rs[i+used] >= '0' && rs[i+used] <= '7' {
				n = n*8 + int(rs[i+used]-'0')
				used++
			}
			out.WriteByte(byte(n))
			i += used - 1
		default:
			out.WriteRune('\\')
			out.WriteRune(e)
		}
	}
	return 0, fmt.Errorf("unterminated $' quote")
}

func hexRun(rs []rune, max int) (int, int) {
	used := 0
	for used < max && used < len(rs) && strings.ContainsRune("0123456789abcdefABCDEF", rs[used]) {
		used++
	}
	if used == 0 {
		return 0, 0
	}
	n, _ := strconv.ParseInt(string(rs[:used]), 16, 32)
	return int(n), used
}
