package openapi

import (
	"errors"
	"strings"
	"testing"
)

const v3 = `{
  "openapi": "3.0.3",
  "info": {"title": "Users API", "version": "1.2.0"},
  "paths": {
    "/users/{id}": {
      "parameters": [{"name": "id", "in": "path"}],
      "get":    {"summary": "Get one user", "operationId": "getUser"},
      "delete": {"summary": "Delete a user"}
    },
    "/users": {
      "get":  {"summary": "List users", "operationId": "listUsers"},
      "post": {"summary": "Create a user"}
    },
    "/shared": {"$ref": "#/components/pathItems/shared"}
  }
}`

func TestParseOpenAPI3(t *testing.T) {
	idx, err := Parse([]byte(v3))
	if err != nil {
		t.Fatal(err)
	}
	if idx.Title != "Users API" || idx.Version != "1.2.0" {
		t.Fatalf("%+v", idx)
	}
	var got []string
	for _, o := range idx.Operations {
		got = append(got, o.Method+" "+o.Path)
	}
	// sorted by path, then the usual method order; $ref items and the
	// path-level "parameters" key are not operations
	want := "GET /users,POST /users,GET /users/{id},DELETE /users/{id}"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v want %v", got, want)
	}
	if idx.Operations[0].Summary != "List users" || idx.Operations[0].OperationID != "listUsers" {
		t.Fatalf("%+v", idx.Operations[0])
	}
}

// "x-..." keys under paths are vendor extensions, not paths: ignored without a warning.
func TestVendorExtensionsUnderPathsAreNotSkippedPaths(t *testing.T) {
	idx, err := Parse([]byte(`{"openapi":"3.0.0","paths":{"x-internal":{"owner":"me"},"/a":{"get":{}}}}`))
	if err != nil || len(idx.Operations) != 1 || idx.Skipped != 0 {
		t.Fatalf("%+v %v", idx, err)
	}
}

func TestParseSwagger2(t *testing.T) {
	idx, err := Parse([]byte(`{"swagger":"2.0","info":{"title":"Old"},"paths":{"/a":{"get":{"summary":"A"}}}}`))
	if err != nil || len(idx.Operations) != 1 || idx.Operations[0].Path != "/a" {
		t.Fatalf("%+v %v", idx, err)
	}
}

func TestParseRejectsWhatIsNotASpec(t *testing.T) {
	for _, in := range []string{``, `   `, `{}`, `{"openapi":"3.0.0"}`, `{"paths":{}}`, `[1,2]`, `{"openapi": 3}`, `not json`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// YAML is the one format people will ask for; the error says what to do, and
// is a distinct sentinel so a YAML decoder can be added without changing callers.
func TestParseYAMLIsAClearUnsupportedError(t *testing.T) {
	_, err := Parse([]byte("openapi: 3.0.0\ninfo:\n  title: x\npaths: {}\n"))
	if !errors.Is(err, ErrUnsupportedFormat) || !strings.Contains(err.Error(), "openapi.json") {
		t.Fatalf("%v", err)
	}
}

func TestDecodersAreAPluginPoint(t *testing.T) {
	old := decoders
	defer func() { decoders = old }()
	decoders = append(decoders, Decoder{
		Name:  "fake-yaml",
		Match: func(d []byte) bool { return strings.HasPrefix(string(d), "openapi:") },
		Decode: func([]byte) (map[string]any, error) {
			return map[string]any{"openapi": "3.0.0", "paths": map[string]any{"/y": map[string]any{"get": map[string]any{"summary": "from yaml"}}}}, nil
		},
	})
	idx, err := Parse([]byte("openapi: 3.0.0\n"))
	if err != nil || len(idx.Operations) != 1 || idx.Operations[0].Path != "/y" {
		t.Fatalf("%+v %v", idx, err)
	}
}

// The spec is data from a server. Everything that ends up in the index is
// printed to a terminal (list) or a completion menu, so control characters and
// absurd values are dropped or flattened here, once.
func TestHostileSpecIsSanitised(t *testing.T) {
	idx, err := Parse([]byte(`{"openapi":"3.0.0","paths":{
	  "/ok": {"get": {"summary": "line one\nline two \u001b[31mred\u001b[0m\u0007", "operationId": "bad id; rm -rf /"}},
	  "/esc\u001b[2J": {"get": {}},
	  "/has space": {"get": {}},
	  "/new\nline": {"get": {}},
	  "relative": {"get": {}},
	  "/` + strings.Repeat("a", 600) + `": {"get": {}}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Operations) != 1 || idx.Operations[0].Path != "/ok" {
		t.Fatalf("only /ok survives: %+v", idx.Operations)
	}
	if idx.Skipped != 5 {
		t.Fatalf("skipped = %d, want 5", idx.Skipped)
	}
	o := idx.Operations[0]
	for _, r := range o.Summary {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character in summary %q", o.Summary)
		}
	}
	if !strings.HasPrefix(o.Summary, "line one line two") {
		t.Fatalf("summary %q", o.Summary)
	}
	if o.OperationID != "" {
		t.Fatalf("an operationId with spaces or shell characters is dropped, got %q", o.OperationID)
	}
	long, _ := Parse([]byte(`{"openapi":"3.0.0","paths":{"/a":{"get":{"summary":"` + strings.Repeat("x", 500) + `"}}}}`))
	if n := len([]rune(long.Operations[0].Summary)); n > MaxSummary {
		t.Fatalf("summary not truncated: %d", n)
	}
}

func TestLimits(t *testing.T) {
	if _, err := Parse(make([]byte, MaxSpecBytes+1)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("%v", err)
	}
	var b strings.Builder
	b.WriteString(`{"openapi":"3.0.0","paths":{`)
	for i := 0; i < MaxOperations+10; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"/p` + strings.Repeat("x", 1) + itoa(i) + `":{"get":{}}`)
	}
	b.WriteString(`}}`)
	idx, err := Parse([]byte(b.String()))
	if err != nil || len(idx.Operations) != MaxOperations || !idx.Truncated {
		t.Fatalf("ops=%d truncated=%v err=%v", len(idx.Operations), idx.Truncated, err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var s string
	for ; i > 0; i /= 10 {
		s = string(digits[i%10]) + s
	}
	return s
}
