package render

import (
	"bytes"
	"strings"
	"testing"
)

const esc = "\x1b["

func paint(code, s string) string { return esc + code + "m" + s + esc + "0m" }

func TestColorizeCompactMatchesJq(t *testing.T) {
	p := DefaultPalette()
	got := string(Colorize([]byte(`{"a":[1,null,"x",true,false]}`), p))
	want := paint(p.Object, "{") + paint(p.Key, `"a"`) + paint(p.Object, ":") +
		paint(p.Array, "[") + paint(p.Number, "1") + paint(p.Array, ",") +
		paint(p.Null, "null") + paint(p.Array, ",") + paint(p.String, `"x"`) + paint(p.Array, ",") +
		paint(p.True, "true") + paint(p.Array, ",") + paint(p.False, "false") +
		paint(p.Array, "]") + paint(p.Object, "}")
	if got != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

func TestColorizeKeyVersusStringAndEscapes(t *testing.T) {
	p := DefaultPalette()
	in := "{\n  \"k\": \"a \\\"quoted\\\": value\"\n}"
	got := string(Colorize([]byte(in), p))
	if !strings.Contains(got, paint(p.Key, `"k"`)) {
		t.Fatalf("key not coloured as key: %q", got)
	}
	if !strings.Contains(got, paint(p.String, `"a \"quoted\": value"`)) {
		t.Fatalf("string with escapes/colon mis-scanned: %q", got)
	}
	if strip(got) != in {
		t.Fatalf("whitespace changed:\n%q\n%q", strip(got), in)
	}
}

// strip removes SGR codes.
func strip(s string) string {
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

func TestColorizeRoundTripsAnyJSON(t *testing.T) {
	in := `{"n":-1.5e3,"o":{},"a":[],"nested":[{"x":[1,[2,[3]]]}],"s":"éé \\ \n"}`
	if got := strip(string(Colorize([]byte(in), DefaultPalette()))); got != in {
		t.Fatalf("round trip differs:\n%q\n%q", got, in)
	}
}

func TestParsePalette(t *testing.T) {
	d := DefaultPalette()
	p := ParsePalette("0;90:0;31:0;32:0;33:0;34:1;35:1;36:4;37")
	if p.Null != "0;90" || p.False != "0;31" || p.True != "0;32" || p.Number != "0;33" ||
		p.String != "0;34" || p.Array != "1;35" || p.Object != "1;36" || p.Key != "4;37" {
		t.Fatalf("full spec: %+v", p)
	}
	if p := ParsePalette("0;90:0;31"); p.Null != "0;90" || p.False != "0;31" || p.String != d.String {
		t.Fatalf("partial spec should keep the remaining defaults: %+v", p)
	}
	for _, bad := range []string{"red:blue", "1;2;x", "1:2:3:4:5:6:7:8:9"} {
		if ParsePalette(bad) != d {
			t.Errorf("malformed %q should fall back to defaults", bad)
		}
	}
}

func TestWriteColorOnlyWhenAskedAndOnlyJSON(t *testing.T) {
	body := []byte(`{"a":1,"s":"x"}`)
	var plain, colored bytes.Buffer
	_ = Write(&plain, body, Options{Filter: ".", Compact: true})
	_ = Write(&colored, body, Options{Filter: ".", Compact: true, Color: true})
	if strings.Contains(plain.String(), esc) {
		t.Fatal("colour without Color")
	}
	if !strings.Contains(colored.String(), esc) || strip(colored.String()) != plain.String() {
		t.Fatalf("colored: %q", colored.String())
	}

	// every output path colours: plain path, gojq filter, identity
	for _, f := range []string{".", ".s", ".a | tostring", ".[]"} {
		var b bytes.Buffer
		if err := Write(&b, body, Options{Filter: f, Color: true}); err != nil {
			t.Fatal(err)
		}
		if f != ".a | tostring" && !strings.Contains(b.String(), esc) {
			t.Errorf("filter %q printed no colour: %q", f, b.String())
		}
	}

	// non-JSON bodies and -r strings stay untouched
	var raw bytes.Buffer
	_ = Write(&raw, []byte("<html>"), Options{Filter: ".", Color: true})
	if strings.Contains(raw.String(), esc) {
		t.Fatalf("html coloured: %q", raw.String())
	}
	var rs bytes.Buffer
	_ = Write(&rs, []byte(`{"s":"plain"}`), Options{Filter: ".s", RawOutput: true, Color: true})
	if rs.String() != "plain\n" {
		t.Fatalf("-r string coloured: %q", rs.String())
	}
}
