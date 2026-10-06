package render

import (
	"bytes"
	"strings"
	"testing"
)

func out(t *testing.T, body, filter string, o Options) string {
	t.Helper()
	o.Filter = filter
	var buf bytes.Buffer
	if err := Write(&buf, []byte(body), o); err != nil {
		t.Fatalf("Write(%q): %v", filter, err)
	}
	return buf.String()
}

func compact(t *testing.T, body, filter string) string {
	t.Helper()
	return strings.TrimSpace(out(t, body, filter, Options{Compact: true}))
}

func TestPathFilterKeepsServerKeyOrder(t *testing.T) {
	body := `{"data":{"z":1,"a":{"y":2,"b":3}},"meta":null}`
	if got := compact(t, body, ".data"); got != `{"z":1,"a":{"y":2,"b":3}}` {
		t.Fatalf(".data: %s", got)
	}
	if got := compact(t, body, ".data.a"); got != `{"y":2,"b":3}` {
		t.Fatalf(".data.a: %s", got)
	}
}

func TestPathFilterSemantics(t *testing.T) {
	body := `{"data":[{"id":7,"n":12345678901234567890},{"id":8}],"s":"x"}`
	cases := map[string]string{
		".data[0].id":      "7",
		".data[1].id":      "8",
		".data[-1].id":     "8",
		".data[5]":         "null",
		".missing":         "null",
		".missing.deeper":  "null",
		".data[0].n":       "12345678901234567890",
		".s":               `"x"`,
		".data | length":   "2", // not a plain path: gojq
		".data[]|.id":      "7\n8",
		".data | map(.id)": "[7,8]",
	}
	for filter, want := range cases {
		if got := compact(t, body, filter); got != want {
			t.Errorf("%s: got %q want %q", filter, got, want)
		}
	}
}

func TestPathFilterTypeMismatchFallsBackToGojq(t *testing.T) {
	var buf bytes.Buffer
	err := Write(&buf, []byte(`{"data":[1,2]}`), Options{Filter: ".data.x"})
	if err == nil || !strings.Contains(err.Error(), "jq:") {
		t.Fatalf("want a jq error for .data.x on an array, got %v (out %q)", err, buf.String())
	}
}

func TestRawOutputOnPath(t *testing.T) {
	if got := out(t, `{"a":{"b":"hi \"there\""}}`, ".a.b", Options{RawOutput: true}); got != "hi \"there\"\n" {
		t.Fatalf("got %q", got)
	}
}

func TestParsePath(t *testing.T) {
	good := []string{".a", ".a.b", ".a[0]", ".[2]", ".a[0].b_c", ".a[-1]"}
	bad := []string{".", "a", ".a.", "..a", ".a[", ".a[x]", ".a | b", ".[]", `."k"`, ".a?", ".a,.b"}
	for _, f := range good {
		if _, ok := parsePath(f); !ok {
			t.Errorf("parsePath(%q) rejected", f)
		}
	}
	for _, f := range bad {
		if _, ok := parsePath(f); ok {
			t.Errorf("parsePath(%q) accepted", f)
		}
	}
}

func TestIdentityNonJSONAndBadFilter(t *testing.T) {
	if got := out(t, `{"z":1,"a":2}`, ".", Options{Compact: true}); got != "{\"z\":1,\"a\":2}\n" {
		t.Fatalf("identity: %q", got)
	}
	if got := out(t, "<html>x</html>", ".data", Options{}); got != "<html>x</html>\n" {
		t.Fatalf("non-json: %q", got)
	}
	var buf bytes.Buffer
	err := Write(&buf, []byte(`{"a":1}`), Options{Filter: ".["})
	if _, ok := err.(ErrFilter); !ok || !strings.Contains(buf.String(), `{"a":1}`) {
		t.Fatalf("bad filter: err=%v out=%q", err, buf.String())
	}
}

func TestFirst(t *testing.T) {
	if s, _ := First([]byte(`{"d":{"t":"abc"}}`), ".d.t"); s != "abc" {
		t.Fatalf("got %q", s)
	}
	if s, _ := First([]byte(`{"d":{}}`), ".d.t"); s != "" {
		t.Fatalf("null should be empty, got %q", s)
	}
}
