package httpc

import (
	"strings"
	"testing"
)

func TestParseHeader(t *testing.T) {
	for raw, want := range map[string][2]string{
		"X-API-Key: abc":      {"X-API-Key", "abc"},
		"  X-A  :   b c  ":    {"X-A", "b c"},
		"If-None-Match:":      {"If-None-Match", ""},
		"X-Time: 12:30:00":    {"X-Time", "12:30:00"},
		"X-Tab: a\tb":         {"X-Tab", "a\tb"},
		"Content-Type: x/y;z": {"Content-Type", "x/y;z"},
	} {
		name, value, err := ParseHeader(raw)
		if err != nil || name != want[0] || value != want[1] {
			t.Errorf("%q -> %q %q %v", raw, name, value, err)
		}
	}
}

func TestParseHeaderRefusesWhatCouldStartAnotherLine(t *testing.T) {
	for _, raw := range []string{
		"no colon", ": value", "  : value",
		"X-A: b\r\nX-Evil: 1", "X-A: b\nX-Evil: 1", "X-A: b\x00c", "X-A: b\x7f",
		"X A: b", "X-A\r\nX-Evil: c: b", "X-é: b",
	} {
		if _, _, err := ParseHeader(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

// -H values are often keys: an error must name the header, not repeat the value.
func TestHeaderErrorsNeverEchoTheValue(t *testing.T) {
	_, _, err := ParseHeader("X-API-Key: s3cr3t\nX-Evil: 1")
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("%v", err)
	}
	if err := CheckHeader("X-A", "s3cr3t\r\n"); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("%v", err)
	}
}
