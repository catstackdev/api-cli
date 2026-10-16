package cli

import (
	"bytes"
	"cybercat/api/internal/har"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/render"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// ── curl bridge ────────────────────────────────────────────────────────────

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return session.Quote(s)
}

// maskedSpec is the request with every secret hidden: the bearer token (first
// characters only), credential headers, secret-looking JSON fields / form pairs /
// query parameters. It is the ONE definition of what may be shown of a request,
// used by --dry, --debug and -v, so they cannot drift apart.
func maskedSpec(spec httpc.Spec) httpc.Spec {
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
	return masked
}

// curlLine is the curl command for --dry and --debug, with secrets masked (see
// maskedSpec). So it is safe to paste into a bug report, and a resolved
// @keychain password never appears in it.
func (a *app) curlLine(spec httpc.Spec, pass []string) string {
	masked := maskedSpec(spec)

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

// verboseBodyCap bounds the request body -v prints: a body can be a whole file
// or stdin, and the terminal is no place for megabytes (or binary).
const verboseBodyCap = 4096

// traceBody prints the already masked body (or form pairs) of a request on
// stderr for -v, cut at verboseBodyCap bytes. On a terminal a body that fits and
// is JSON is pretty-printed and coloured like a response body; anything else is
// shown as it was written.
func (a *app) traceBody(prefix string, masked httpc.Spec, color bool) {
	if masked.Body != "" {
		for _, line := range a.bodyLines(masked.Body, color) {
			a.trace("%s%s", prefix, line)
		}
	}
	for _, pair := range append(append([]string{}, masked.Data...), masked.Fields...) {
		a.trace("%s%s", prefix, pair)
	}
}

func (a *app) bodyLines(body string, color bool) []string {
	if color && len(body) <= verboseBodyCap && json.Valid([]byte(body)) {
		var buf bytes.Buffer
		if render.Write(&buf, []byte(body), render.Options{Color: true}) == nil {
			return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		}
	}
	more := 0
	if len(body) > verboseBodyCap {
		cut := verboseBodyCap
		for cut > 0 && !utf8.RuneStart(body[cut]) { // never split a character
			cut--
		}
		body, more = body[:cut], len(body)-cut
	}
	lines := strings.Split(body, "\n")
	if more > 0 {
		lines = append(lines, fmt.Sprintf("… %d more bytes", more))
	}
	return lines
}

// timingPhases hands the request's timings to the presentation layer.
func timingPhases(t httpc.Timing) ui.TimingPhases {
	return ui.TimingPhases{DNS: t.DNS, Connect: t.Connect, TLS: t.TLS, TTFB: t.TTFB, Total: t.Total, Reused: t.Reused}
}

func shortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
