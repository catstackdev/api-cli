package cli

import (
	"cybercat/api/internal/har"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// ── curl bridge ────────────────────────────────────────────────────────────

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return session.Quote(s)
}

func maskHeader(name, value string) string {
	if strings.EqualFold(name, "Authorization") {
		if tok, ok := strings.CutPrefix(value, "Bearer "); ok {
			return "Bearer " + session.MaskToken(tok)
		}
	}
	return value
}

// curlLine is the curl command for --dry and --debug, with secrets masked: the
// bearer token (first characters only), credential headers, secret-looking JSON
// fields / form pairs / query parameters. So it is safe to paste into a bug
// report, and a resolved @keychain password never appears in it.
func (a *app) curlLine(spec httpc.Spec, pass []string) string {
	masked := spec
	masked.URL = har.MaskURL(spec.URL)
	masked.Headers = make([]httpc.Header, len(spec.Headers))
	ctype := ""
	for i, h := range spec.Headers {
		v := har.MaskHeader(h.Name, h.Value)
		if strings.EqualFold(h.Name, "Authorization") {
			if tok, ok := strings.CutPrefix(h.Value, "Bearer "); ok {
				v = "Bearer " + session.MaskToken(tok)
			}
		}
		if strings.EqualFold(h.Name, "Content-Type") {
			ctype = h.Value
		}
		masked.Headers[i] = httpc.Header{Name: h.Name, Value: v}
	}
	masked.Body = har.MaskBody(spec.Body, ctype)
	masked.Data = make([]string, len(spec.Data))
	for i, d := range spec.Data {
		masked.Data[i] = har.MaskPair(d)
	}
	masked.Fields = make([]string, len(spec.Fields))
	for i, f := range spec.Fields {
		masked.Fields[i] = har.MaskPair(f)
	}

	words := []string{"curl"}
	for _, arg := range append(masked.CurlArgs(), pass...) {
		words = append(words, shellWord(arg))
	}
	return strings.Join(words, " ")
}

// execCurl hands the request to the real curl, with extra flags after `--`.
func (a *app) execCurl(spec httpc.Spec, pass []string) error {
	path, err := exec.LookPath("curl")
	if err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ curl not found in PATH"}
	}
	c := exec.Command(path, append(spec.CurlArgs(), pass...)...)
	c.Stdin, c.Stdout, c.Stderr = a.io.In, a.io.Out, a.io.Err
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return &ExitError{Code: ee.ExitCode()}
		}
		return err
	}
	return nil
}

// writeHeaders prints sorted "Name: value" lines, names coloured when asked.
func writeHeaders(w io.Writer, prefix string, h http.Header, color bool) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintln(w, ui.HeaderLine(prefix, k, v, color))
		}
	}
}

func printHeaders(w io.Writer, prefix string, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

func shortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
