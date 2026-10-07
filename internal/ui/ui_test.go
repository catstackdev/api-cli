package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{"always": Always, "ALWAYS": Always, "never": Never, "off": Never, "auto": Auto, "": Auto, "x": Auto}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEnabled(t *testing.T) {
	var buf bytes.Buffer // not a terminal
	if Enabled(&buf, Auto) {
		t.Fatal("auto must be off for a non-terminal")
	}
	if !Enabled(&buf, Always) || Enabled(&buf, Never) {
		t.Fatal("explicit modes must win")
	}
	t.Setenv("NO_COLOR", "1")
	if !Enabled(&buf, Always) {
		t.Fatal("Always must beat NO_COLOR (the user asked explicitly)")
	}
}

func TestPaint(t *testing.T) {
	if got := Paint("x", Red, true); got != "\x1b[31mx\x1b[0m" {
		t.Fatalf("got %q", got)
	}
	if Paint("x", Red, false) != "x" || Paint("", Red, true) != "" || Paint("x", "", true) != "x" {
		t.Fatal("no-op cases")
	}
}

func TestMessageStyles(t *testing.T) {
	if got := Message("✗ boom", true); got != "\x1b[31m✗ boom\x1b[0m" {
		t.Fatalf("error: %q", got)
	}
	if got := Message("✓ saved", true); got != "\x1b[32m✓\x1b[0m saved" {
		t.Fatalf("success paints only the glyph: %q", got)
	}
	if got := Message("⟳ logging in…", true); !strings.HasPrefix(got, "\x1b[36m⟳\x1b[0m") {
		t.Fatalf("progress: %q", got)
	}
	if got := Message("→ curl -sS", true); got != "\x1b[2m→ curl -sS\x1b[0m" {
		t.Fatalf("trace: %q", got)
	}
	if Message("plain text", true) != "plain text" || Message("✗ boom", false) != "✗ boom" {
		t.Fatal("unknown glyph and colour-off must be untouched")
	}
}

func TestStatusLineClasses(t *testing.T) {
	cases := map[int]string{200: Green, 301: Yellow, 404: Red, 503: Red + ";" + Bold}
	for code, c := range cases {
		got := StatusLine("HTTP/1.1", code, "X", true)
		if !strings.Contains(got, "\x1b["+c+"m") {
			t.Errorf("%d: %q lacks colour %s", code, got, c)
		}
	}
	if got := StatusLine("HTTP/1.1", 200, "OK", false); got != "HTTP/1.1 200 OK" {
		t.Fatalf("plain: %q", got)
	}
	if got := HeaderLine("< ", "X-A", "1", true); got != "< \x1b[36mX-A\x1b[0m: 1" {
		t.Fatalf("header: %q", got)
	}
}

func TestPageDirectAndThroughPager(t *testing.T) {
	data := []byte("line1\nline2\n")
	var direct bytes.Buffer
	if err := Page(&direct, data, PagerOptions{Enabled: false, Cmd: "cat"}); err != nil || direct.String() != string(data) {
		t.Fatalf("direct: %v %q", err, direct.String())
	}
	var piped bytes.Buffer
	if err := Page(&piped, data, PagerOptions{Enabled: true, Cmd: "cat"}); err != nil || piped.String() != string(data) {
		t.Fatalf("pager: %v %q", err, piped.String())
	}
	// empty output never starts a pager (e.g. api get -o file)
	var empty bytes.Buffer
	if err := Page(&empty, nil, PagerOptions{Enabled: true, Cmd: "echo SHOULD-NOT-RUN"}); err != nil || empty.Len() != 0 {
		t.Fatalf("empty: %v %q", err, empty.String())
	}
	// a pager that does not exist must not lose the output
	var fallback bytes.Buffer
	if err := Page(&fallback, data, PagerOptions{Enabled: true, Cmd: "definitely-not-a-pager-xyz"}); err != nil || fallback.String() != string(data) {
		t.Fatalf("fallback: %v %q", err, fallback.String())
	}
	// a pager the user quits early (non-zero exit) is not an error and not retried
	var quit bytes.Buffer
	if err := Page(&quit, data, PagerOptions{Enabled: true, Cmd: "cat >/dev/null; exit 1"}); err != nil || quit.String() != "" {
		t.Fatalf("early quit: %v %q", err, quit.String())
	}
}

func TestPagerCmdAndShouldPage(t *testing.T) {
	t.Setenv("API_PAGER", "")
	if PagerCmd() != "" {
		t.Fatal("empty API_PAGER must disable paging")
	}
	var buf bytes.Buffer
	if ShouldPage(&buf, false) {
		t.Fatal("never page a non-terminal")
	}
}

func TestTableAlignsOnPlainWidthAndIgnoresColour(t *testing.T) {
	rows := [][]string{{"dev", "https://a"}, {"thien-development", "https://b"}}
	var plain, colored bytes.Buffer
	Table(&plain, []string{"NAME", "URL"}, rows, false)
	Table(&colored, []string{"NAME", "URL"}, rows, true)
	want := "NAME               URL\ndev                https://a\nthien-development  https://b\n"
	if plain.String() != want {
		t.Fatalf("plain:\n%q\nwant:\n%q", plain.String(), want)
	}
	if stripSGR(colored.String()) != want {
		t.Fatalf("colour changed the layout:\n%q", stripSGR(colored.String()))
	}
}

func stripSGR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestHTTPHighlightKeepsTheText(t *testing.T) {
	src := "### login\n# imported from curl\nPOST {{graphql}}/auth\ncontent-type: application/json\nX-Key: {{$env API_KEY}}\n\n{\n  \"password\": \"{{$env API_PASSWORD}}\"\n}\n"
	if got := HTTP(src, false, nil); got != src {
		t.Fatal("off must return the text unchanged")
	}
	on := HTTP(src, true, nil)
	if strings.Contains(on, "\x1b[") == false {
		t.Fatal("on must add colour")
	}
	if sgr.ReplaceAllString(on, "") != src {
		t.Fatalf("removing the colour must give back the input:\n%q", on)
	}
	if !strings.Contains(on, "\x1b[33m{{$env API_KEY}}") || !strings.Contains(on, "\x1b[32m{{graphql}}") {
		t.Fatalf("variables must be coloured by kind:\n%q", on)
	}
}
