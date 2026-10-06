package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Tag points at a TagCategory, which points back at Tag: the case that made opening a
// type unhelpful ("category: TagCategory" and nothing about what that is).
const tagSchemaJSON = `{"queryType":{"name":"Query"},"types":[
 {"name":"Query","kind":"OBJECT","fields":[{"name":"tag","args":[],"type":{"kind":"OBJECT","name":"Tag"}}]},
 {"name":"Tag","kind":"OBJECT","fields":[
   {"name":"id","args":[],"type":{"kind":"SCALAR","name":"String"}},
   {"name":"category","args":[],"type":{"kind":"OBJECT","name":"TagCategory"}}]},
 {"name":"TagCategory","kind":"OBJECT","fields":[
   {"name":"id","args":[],"type":{"kind":"SCALAR","name":"String"}},
   {"name":"totalTags","args":[],"type":{"kind":"SCALAR","name":"Int"}},
   {"name":"tags","args":[],"type":{"kind":"LIST","ofType":{"kind":"OBJECT","name":"Tag"}}}]},
 {"name":"String","kind":"SCALAR"},{"name":"Int","kind":"SCALAR"}]}`

func TestSchemaShowExpandAddsTheTypesAFieldPointsAt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(file, []byte(tagSchemaJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	plain := run(t, "", "schema", "show", "--file", file, "type", "Tag")
	if plain.code != 0 || strings.Contains(plain.out, "related:") || !strings.Contains(plain.out, "category: TagCategory") {
		t.Fatalf("the default detail must not change: %+v", plain)
	}

	r := run(t, "", "schema", "show", "--file", file, "--expand", "type", "Tag")
	if r.code != 0 || !strings.HasPrefix(r.out, plain.out[:len(plain.out)-1]) {
		t.Fatalf("expanded output must start with the plain detail: %+v", r)
	}
	for _, want := range []string{"related: type TagCategory", "  totalTags: Int", "  tags: [Tag]"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if strings.Contains(r.out, "related: type Tag\n") {
		t.Errorf("the loop back to Tag must not be listed:\n%s", r.out)
	}
	if r := run(t, "", "schema", "show", "--file", file, "--expand", "type", "Nope"); r.code == 0 {
		t.Errorf("an unknown type must still fail with --expand: %+v", r)
	}
}

func TestSchemaTypeExpandReadsTheWholeSchemaOnceAndKeepsTheDefault(t *testing.T) {
	var queries []string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Query string }
		_ = json.Unmarshal(body, &req)
		queries = append(queries, req.Query)
		switch {
		case strings.Contains(req.Query, "__type("):
			_, _ = w.Write([]byte(`{"data":{"__type":{"name":"Tag","kind":"OBJECT","fields":[{"name":"category","args":[],"type":{"kind":"OBJECT","name":"TagCategory"}}]}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"__schema":` + tagSchemaJSON + `}}`))
		}
	})
	t.Setenv("API_GRAPHQL_URL", os.Getenv("API_BASE_URL")+"/graphql")

	// default: the one-type query, no "related" key
	r := run(t, "", "schema", "type", "contact", "Tag")
	if r.code != 0 || strings.Contains(r.out, "related") || len(queries) != 1 || !strings.Contains(queries[0], "__type(") {
		t.Fatalf("default: %+v queries=%d", r, len(queries))
	}

	queries = nil
	r = run(t, "", "schema", "type", "contact", "Tag", "--expand")
	if r.code != 0 || len(queries) != 1 || !strings.Contains(queries[0], "types {") {
		t.Fatalf("--expand must read the whole schema in one request: %+v queries=%d", r, len(queries))
	}
	var got struct {
		Name        string
		Fields      []string
		Related     []struct{ Name, Kind string }
		RelatedMore int
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.out)
	}
	if got.Name != "Tag" || len(got.Fields) != 2 || len(got.Related) != 1 || got.Related[0].Name != "TagCategory" || got.Related[0].Kind != "OBJECT" {
		t.Fatalf("%+v", got)
	}

	if r := run(t, "", "schema", "type", "contact", "Nope", "--expand"); r.code == 0 || !strings.Contains(r.err, `no type "Nope" on contact`) {
		t.Errorf("an unknown type: %+v", r)
	}
	// --dry sends nothing and prints the request
	queries = nil
	if r := run(t, "", "schema", "type", "contact", "Tag", "--expand", "--dry"); r.code != 0 || len(queries) != 0 || !strings.Contains(r.out, "curl") {
		t.Errorf("--dry: %+v queries=%d", r, len(queries))
	}
}
