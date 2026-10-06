package jsondiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func dec(t *testing.T, s string) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func lines(cs []Change) string {
	var out []string
	for _, c := range cs {
		k := map[Kind]string{Changed: "~", Added: "+", Removed: "-"}[c.Kind]
		out = append(out, fmt.Sprintf("%s %s %s -> %s", k, c.Path, Format(c.A, 40), Format(c.B, 40)))
	}
	return strings.Join(out, "\n")
}

func TestDiffKindsAndPaths(t *testing.T) {
	a := dec(t, `{"id":1,"name":"a","tags":["x","y","z"],"nested":{"k":true,"gone":1},"a b":1}`)
	b := dec(t, `{"id":1,"name":"b","tags":["x","Y"],"nested":{"k":true,"new":2},"a b":2}`)
	want := strings.Join([]string{
		`~ ["a b"] 1 -> 2`,
		`~ .name "a" -> "b"`,
		`- .nested.gone 1 -> null`,
		`+ .nested.new null -> 2`,
		`~ .tags[1] "y" -> "Y"`,
		`- .tags[2] "z" -> null`,
	}, "\n")
	if got := lines(Diff(a, b, nil)); got != want {
		t.Fatalf("\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIdenticalAndNumbers(t *testing.T) {
	if cs := Diff(dec(t, `{"a":[1,2.0,{"b":null}],"n":1e2}`), dec(t, `{"n":100,"a":[1,2,{"b":null}]}`), nil); len(cs) != 0 {
		t.Fatalf("key order and 2.0 == 2 and 1e2 == 100 must not differ: %s", lines(cs))
	}
	if cs := Diff(dec(t, `{"n":12345678901234567890}`), dec(t, `{"n":12345678901234567891}`), nil); len(cs) != 1 {
		t.Fatalf("large integers must compare exactly: %s", lines(cs))
	}
	if cs := Diff(dec(t, `"1"`), dec(t, `1`), nil); len(cs) != 1 {
		t.Fatal(`"1" and 1 are different`)
	}
}

func TestTypeChangesAndRoot(t *testing.T) {
	cs := Diff(dec(t, `{"a":{"x":1}}`), dec(t, `{"a":[1]}`), nil)
	if len(cs) != 1 || cs[0].Path != ".a" || cs[0].Kind != Changed {
		t.Fatalf("object -> array: %s", lines(cs))
	}
	cs = Diff(dec(t, `[1,2]`), dec(t, `"x"`), nil)
	if len(cs) != 1 || cs[0].Path != "." {
		t.Fatalf("root: %s", lines(cs))
	}
}

func TestIgnore(t *testing.T) {
	a := dec(t, `{"data":{"updatedAt":"1","items":[{"id":1,"ts":"a"}]},"meta":{"req":1}}`)
	b := dec(t, `{"data":{"updatedAt":"2","items":[{"id":1,"ts":"b"}]},"meta":{"req":2}}`)
	if got := lines(Diff(a, b, []string{".data.updatedAt", ".meta"})); got != `~ .data.items[0].ts "a" -> "b"` {
		t.Fatalf("got %q", got)
	}
	// an ignored prefix hides everything below it, but not a sibling that merely shares the text
	c, d := dec(t, `{"ab":1,"a":{"x":1}}`), dec(t, `{"ab":2,"a":{"x":2}}`)
	if got := lines(Diff(c, d, []string{".a"})); got != `~ .ab 1 -> 2` {
		t.Fatalf("prefix match too greedy: %q", got)
	}
}

func TestFormatTruncates(t *testing.T) {
	if got := Format("abcdefghij", 6); got != `"abcd…` {
		t.Fatalf("%q", got)
	}
	if Format(dec(t, `12345678901234567890`), 0) != "12345678901234567890" {
		t.Fatal("numbers keep their digits")
	}
}

func paths(cs []Change) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Kind.String()+" "+c.Path)
	}
	return strings.Join(out, "; ")
}

func TestArrayKeyMatchesElementsByIdentityNotPosition(t *testing.T) {
	a := dec(t, `{"data":[{"id":1,"n":"a"},{"id":2,"n":"b"},{"id":3,"n":"c"}]}`)
	b := dec(t, `{"data":[{"id":3,"n":"c"},{"id":1,"n":"a"},{"id":2,"n":"B"},{"id":4,"n":"d"}]}`)

	// by position everything past the first element looks different
	if byPos := Diff(a, b, nil); len(byPos) < 4 {
		t.Fatalf("sanity: expected many positional differences, got %s", paths(byPos))
	}
	got := paths(DiffWith(a, b, Options{ArrayKey: "id"}))
	if got != "changed .data[id=2].n; added .data[id=4]" {
		t.Fatalf("got %q", got)
	}
	// a removed element, and ignore still applies to keyed paths
	b2 := dec(t, `{"data":[{"id":1,"n":"x"},{"id":2,"n":"b"}]}`)
	if got := paths(DiffWith(a, b2, Options{ArrayKey: "id", Ignore: []string{".data[id=1].n"}})); got != "removed .data[id=3]" {
		t.Fatalf("got %q", got)
	}
}

func TestArrayKeyFallsBackToPositionWhenItCannotMatch(t *testing.T) {
	cases := map[string][2]string{
		"missing key": {`[{"id":1},{"x":2}]`, `[{"id":1},{"x":3}]`},
		"duplicates":  {`[{"id":1},{"id":1}]`, `[{"id":1},{"id":1}]`},
		"scalars":     {`[1,2]`, `[1,3]`},
	}
	for name, c := range cases {
		got := DiffWith(dec(t, c[0]), dec(t, c[1]), Options{ArrayKey: "id"})
		for _, ch := range got {
			if strings.Contains(ch.Path, "id=") {
				t.Errorf("%s: keyed path used: %s", name, paths(got))
			}
		}
	}
	if got := paths(DiffWith(dec(t, `[1,2]`), dec(t, `[1,3]`), Options{ArrayKey: "id"})); got != "changed [1]" {
		t.Fatalf("got %q", got)
	}
}

func TestArrayKeyRootArrayAndNumericIDs(t *testing.T) {
	a := dec(t, `[{"id":10,"v":1}]`)
	b := dec(t, `[{"id":10,"v":2}]`)
	if got := paths(DiffWith(a, b, Options{ArrayKey: "id"})); got != "changed [id=10].v" {
		t.Fatalf("got %q", got)
	}
}
