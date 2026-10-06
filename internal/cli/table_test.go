package cli

import (
	"net/http"
	"strings"
	"testing"
)

// ── --table ────────────────────────────────────────────────────────────────

func TestTableOutput(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[` +
			`{"id":2,"name":"bravo","zeta":true,"nested":{"a":1},"note":null},` +
			`{"id":10,"name":"a much longer name","extra":"héllo wörld"}],"one":{"k":"v","n":3},"s":"text"}`))
	})
	// array of objects: id and name first, the rest alphabetical, missing/null as "-"
	r := run(t, "", "get", "/x", ".data", "--table", "-M")
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if r.code != 0 || len(lines) != 3 {
		t.Fatalf("%+v", r)
	}
	if !strings.HasPrefix(lines[0], "id  name") || !strings.Contains(lines[0], "extra") || !strings.Contains(lines[0], "zeta") {
		t.Fatalf("header: %q", lines[0])
	}
	if !strings.Contains(lines[1], `{"a":1}`) || !strings.Contains(lines[1], "-") || !strings.Contains(lines[2], "héllo wörld") {
		t.Fatalf("rows:\n%s", r.out)
	}
	// columns align on text width (rune count), not bytes
	idx := strings.Index(lines[2], "héllo wörld")
	if col := strings.Index(lines[0], "extra"); col < 0 || len([]rune(lines[2][:idx])) != len([]rune(lines[0][:col])) {
		t.Fatalf("misaligned unicode:\n%s", r.out)
	}
	// --columns picks and orders
	r = run(t, "", "get", "/x", ".data", "--table", "--columns", "name,id", "-M")
	if got := strings.Split(strings.TrimSpace(r.out), "\n")[0]; !strings.HasPrefix(got, "name") || !strings.Contains(got, "id") || strings.Contains(got, "zeta") {
		t.Fatalf("--columns: %q", got)
	}
	// one object: key / value
	r = run(t, "", "get", "/x", ".one", "--table", "-M")
	if !strings.Contains(r.out, "KEY") || !strings.Contains(r.out, "k    v") && !strings.Contains(r.out, "k  v") {
		t.Fatalf("object table: %q", r.out)
	}
	// not table-shaped: printed normally, nothing lost
	if r := run(t, "", "get", "/x", ".s", "--table"); strings.TrimSpace(r.out) != `"text"` {
		t.Fatalf("fallback: %q", r.out)
	}
	// -R keeps the raw body even with --table
	if r := run(t, "", "get", "/x", "--table", "-R"); !strings.Contains(r.out, `"data":[`) {
		t.Fatalf("-R must win: %q", r.out)
	}
}
