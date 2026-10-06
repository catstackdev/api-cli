package schema

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) Schema {
	t.Helper()
	data, err := os.ReadFile("testdata/user.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIndexMatchesTheShellHelperByteForByte(t *testing.T) {
	// testdata/index.golden was written by the shell implementation this replaced
	want, err := os.ReadFile("testdata/index.golden")
	if err != nil {
		t.Fatal(err)
	}
	lines := loadFixture(t).Index("user")
	SortIndex(lines)
	if got := strings.Join(lines, "\n") + "\n"; got != string(want) {
		t.Fatalf("index differs from the shell helper:\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestSignatureAndTypeStrings(t *testing.T) {
	s := loadFixture(t)
	q, _ := s.FindType("Query")
	if got := q.Fields[1].Signature(); got != "user(id: ID!): User" {
		t.Fatalf("signature %q", got)
	}
	str := func(s string) *string { return &s }
	list := Ref{Kind: "NON_NULL", OfType: &Ref{Kind: "LIST", OfType: &Ref{Kind: "NON_NULL", OfType: &Ref{Kind: "OBJECT", Name: str("User")}}}}
	if list.String() != "[User!]!" || list.Base() != "User" {
		t.Fatalf("%q %q", list.String(), list.Base())
	}
}

func TestParseRefusesAnEmptyOrDisabledIntrospection(t *testing.T) {
	for _, in := range []string{`{}`, `{"types":[]}`, `null`, `[1]`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%s) accepted", in)
		}
	}
}

func TestRenderKeepsKeyOrderAndEmptyListsNotNull(t *testing.T) {
	s := loadFixture(t)
	role, _ := s.FindType("Role")
	out, err := Pretty(Render(role))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"Role\",\n  \"kind\": \"ENUM\",\n  \"fields\": [],\n  \"inputFields\": [],\n  \"enumValues\": [\n    \"ADMIN\",\n    \"USER\"\n  ]\n}\n"
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
}

func TestValidLabelRefusesPaths(t *testing.T) {
	for _, bad := range []string{"", "..", "../x", "a/b", ".hidden", "a b", "x\\y", strings.Repeat("a", 65)} {
		if ValidLabel(bad) {
			t.Errorf("ValidLabel(%q) = true", bad)
		}
	}
	for _, good := range []string{"user", "smartcard_config", "brand-kit", "v2.user"} {
		if !ValidLabel(good) {
			t.Errorf("ValidLabel(%q) = false", good)
		}
	}
}

func TestPreviewProgramIsTheOneTheBrowsersExpect(t *testing.T) {
	for _, must := range []string{"def tr:", "def sig:", "def body:", `$kind == "query"`, "$T[$n]"} {
		if !strings.Contains(PreviewProgram, must) {
			t.Errorf("preview.jq lost %q", must)
		}
	}
}

func TestTypeListPutsRootsFirst(t *testing.T) {
	s := loadFixture(t)
	if got := strings.Join(s.TypeList(false), ","); got != "Query,Mutation,LoginInput,Role,User" {
		t.Fatalf("%q", got)
	}
	if got := strings.Join(s.TypeList(true), ","); got != "query Query,mutation Mutation,input LoginInput,enum Role,type User" {
		t.Fatalf("%q", got)
	}
}

// Detail is the Go twin of preview.jq: for every item of the fixture the two must
// print exactly the same text (the browsers still run the jq one).
func TestDetailMatchesThePreviewJqProgram(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not installed")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "preview.jq")
	if err := os.WriteFile(prog, []byte(PreviewProgram), 0o600); err != nil {
		t.Fatal(err)
	}
	s := loadFixture(t)
	for _, line := range s.Index("user") {
		parts := strings.SplitN(line, " ", 3)
		kind, name := parts[1], parts[2]
		want, err := exec.Command(jq, "-r", "--arg", "kind", kind, "--arg", "n", name, "-f", prog, "testdata/user.json").Output()
		if err != nil {
			t.Fatalf("jq %s %s: %v", kind, name, err)
		}
		got, err := s.Detail(kind, name)
		if err != nil {
			t.Fatalf("%s %s: %v", kind, name, err)
		}
		if strings.Join(got, "\n")+"\n" != string(want) {
			t.Errorf("%s %s differs\n--- go\n%s\n--- jq\n%s", kind, name, strings.Join(got, "\n"), want)
		}
	}
	if _, err := s.Detail("query", "nope"); err == nil {
		t.Error("an unknown query must be an error")
	}
}

func TestSkeletonBuildsARunnableOperation(t *testing.T) {
	s := loadFixture(t)
	cases := []struct{ kind, name, query, vars string }{
		{"query", "me", "query Me {\n  me {\n    id\n    name\n  }\n}\n", "{}"},
		{"query", "user", "query User($id: ID!) {\n  user(id: $id) {\n    id\n    name\n  }\n}\n", "{\n  \"id\": \"\"\n}"},
		// Auth is not in the fixture's types: ask for __typename rather than guess
		{"mutation", "login", "mutation Login($input: LoginInput!) {\n  login(input: $input) {\n    __typename\n  }\n}\n", "{\n  \"input\": {\"username\": \"\", \"password\": \"\"}\n}"},
	}
	for _, c := range cases {
		q, v, err := s.Skeleton(c.kind, c.name)
		if err != nil || q != c.query || v != c.vars {
			t.Errorf("%s %s:\n--- query\n%s\n--- vars\n%s\n(err %v)", c.kind, c.name, q, v, err)
		}
		var any map[string]any
		if err := json.Unmarshal([]byte(v), &any); err != nil {
			t.Errorf("%s %s: variables are not JSON: %v", c.kind, c.name, err)
		}
	}
	if _, _, err := s.Skeleton("query", "nope"); err == nil {
		t.Error("an unknown field must be an error")
	}
}

func TestSkeletonTypeShapes(t *testing.T) {
	raw := `{"queryType":{"name":"Query"},"types":[
	 {"name":"Query","kind":"OBJECT","fields":[
	  {"name":"list","args":[{"name":"ids","type":{"kind":"NON_NULL","ofType":{"kind":"LIST","ofType":{"kind":"SCALAR","name":"ID"}}}},{"name":"n","type":{"kind":"SCALAR","name":"Int"}},{"name":"on","type":{"kind":"SCALAR","name":"Boolean"}},{"name":"role","type":{"kind":"ENUM","name":"Role"}}],
	   "type":{"kind":"LIST","ofType":{"kind":"OBJECT","name":"Thing"}}},
	  {"name":"count","args":[],"type":{"kind":"SCALAR","name":"Int"}},
	  {"name":"u","args":[],"type":{"kind":"UNION","name":"U"}}]},
	 {"name":"Thing","kind":"OBJECT","fields":[{"name":"id","args":[],"type":{"kind":"SCALAR","name":"ID"}},{"name":"deep","args":[],"type":{"kind":"OBJECT","name":"Thing"}},{"name":"withArg","args":[{"name":"x","type":{"kind":"SCALAR","name":"Int"}}],"type":{"kind":"SCALAR","name":"Int"}},{"name":"r","args":[],"type":{"kind":"ENUM","name":"Role"}}]},
	 {"name":"Role","kind":"ENUM","enumValues":[{"name":"ADMIN"}]},
	 {"name":"U","kind":"UNION"}]}`
	s, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	q, v, _ := s.Skeleton("query", "list")
	// plain fields only: no nested object, no field that needs arguments
	if !strings.Contains(q, "    id\n    r\n") || strings.Contains(q, "deep") || strings.Contains(q, "withArg") {
		t.Errorf("selection:\n%s", q)
	}
	for _, want := range []string{`"ids": []`, `"n": 0`, `"on": false`, `"role": "ADMIN"`} {
		if !strings.Contains(v, want) {
			t.Errorf("variables lack %s:\n%s", want, v)
		}
	}
	if q, _, _ := s.Skeleton("query", "count"); q != "query Count {\n  count\n}\n" {
		t.Errorf("a scalar needs no selection:\n%s", q)
	}
	if q, _, _ := s.Skeleton("query", "u"); !strings.Contains(q, "__typename") {
		t.Errorf("a union needs __typename:\n%s", q)
	}
}
