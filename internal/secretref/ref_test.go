package secretref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
		arg  string
	}{
		{"@env:UPSCALE_PASS", Env, "UPSCALE_PASS"},
		{"@keychain:upscale-dev", Keychain, "upscale-dev"},
		{"@prompt", Prompt, ""},
		{"@prompt:Password for dev", Prompt, "Password for dev"},
		{"@@literal", None, ""},
		{"plain", None, ""},
		{"@unknown:x", None, ""},
		{"@env:", None, ""},
		{"@env:bad name", None, ""},
		{"@env:$(x)", None, ""},
		{"@keychain:", None, ""},
		{"", None, ""},
		{"a@env:X", None, ""},
	}
	for _, c := range cases {
		if k, a := Parse(c.in); k != c.kind || a != c.arg {
			t.Errorf("Parse(%q) = %v,%q want %v,%q", c.in, k, a, c.kind, c.arg)
		}
	}
	if !IsRef("@prompt") || IsRef("@@x") || IsRef("x") {
		t.Fatal("IsRef")
	}
	if Unescape("@@x") != "@x" || Unescape("@x") != "@x" || Unescape("x") != "x" {
		t.Fatal("Unescape")
	}
}
