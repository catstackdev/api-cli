package session

import "testing"

func TestQuoteIsInjectionSafe(t *testing.T) {
	cases := map[string]string{
		"plain":       `'plain'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": `'$(rm -rf /)'`,
		"a;b`c`":      "'a;b`c`'",
		"":            `''`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskToken(t *testing.T) {
	if got := MaskToken(""); got != "<not set>" {
		t.Errorf("empty: %q", got)
	}
	if got := MaskToken("abcdefghijklmnopqrstuvwxyz"); got != "abcdefghijkl…" {
		t.Errorf("long: %q", got)
	}
	if got := MaskToken("short"); got != "short…" {
		t.Errorf("short: %q", got)
	}
}
