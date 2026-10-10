package cli

import (
	"cybercat/api/internal/gqlquery"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── diff ───────────────────────────────────────────────────────────────────

type diffEnv struct {
	srv  *httptest.Server
	body *string
	code *int
	got  *hit
}

func newDiffEnv(t *testing.T, body string) diffEnv {
	t.Helper()
	b, code, h := &body, new(int), &hit{}
	*code = 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.auth, h.path = r.Header.Get("Authorization"), r.Method+" "+r.URL.Path
		w.WriteHeader(*code)
		_, _ = w.Write([]byte(*b))
	}))
	t.Cleanup(srv.Close)
	return diffEnv{srv, b, code, h}
}

func diffSetup(t *testing.T, devBody, thienBody string) (dev, thien diffEnv) {
	t.Helper()
	dev, thien = newDiffEnv(t, devBody), newDiffEnv(t, thienBody)
	writeConfig(t, `
[env.dev]
url = "`+dev.srv.URL+`"
graphql = "`+dev.srv.URL+`/graphql"
[env.thien]
url = "`+thien.srv.URL+`"
graphql = "`+thien.srv.URL+`/graphql"
token_env = "API_TOKEN_THIEN"
`)
	t.Setenv("API_BASE_URL", dev.srv.URL)
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_TOKEN", "DEV-SECRET")
	t.Setenv("API_TOKEN_THIEN", "THIEN-SECRET")
	t.Setenv("API_GRAPHQL_URL", "")
	return dev, thien
}

func TestDiffShowsDifferencesAndKeepsTokensApart(t *testing.T) {
	dev, thien := diffSetup(t,
		`{"data":{"id":1,"name":"a","only_dev":true,"updatedAt":"1"}}`,
		`{"data":{"id":1,"name":"b","only_thien":2,"updatedAt":"2"}}`)

	r := run(t, "", "diff", "--envs", "dev,thien", "/x", ".data", "--ignore", ".updatedAt", "-M")
	if r.code != 1 {
		t.Fatalf("different must exit 1: %+v", r)
	}
	for _, want := range []string{"--- dev", "+++ thien", `~ .name`, `"a" → "b"`, "- .only_dev", "+ .only_thien", "3 difference(s)"} {
		if !strings.Contains(r.out+r.err, want) {
			t.Errorf("missing %q:\n%s\n%s", want, r.out, r.err)
		}
	}
	if strings.Contains(r.out, "updatedAt") {
		t.Fatalf("--ignore not applied:\n%s", r.out)
	}
	// each env got ITS OWN token
	if dev.got.auth != "Bearer DEV-SECRET" || thien.got.auth != "Bearer THIEN-SECRET" {
		t.Fatalf("tokens: dev=%q thien=%q", dev.got.auth, thien.got.auth)
	}
	if dev.got.path != "GET /x" || thien.got.path != "GET /x" {
		t.Fatalf("requests: %q %q", dev.got.path, thien.got.path)
	}
}

func TestDiffIdenticalStatusAndNonJSON(t *testing.T) {
	dev, thien := diffSetup(t, `{"a":1,"b":[1,2]}`, `{"b":[1,2],"a":1.0}`)
	if r := run(t, "", "diff", "--envs", "dev,thien", "/x"); r.code != 0 || !strings.Contains(r.err, "identical") {
		t.Fatalf("identical (key order / 1.0): %+v", r)
	}
	*thien.code = 500
	if r := run(t, "", "diff", "--envs", "dev,thien", "/x", "-M"); r.code != 1 || !strings.Contains(r.out, "HTTP status") || !strings.Contains(r.out, "200 → 500") {
		t.Fatalf("status difference: %+v", r)
	}
	*thien.code = 200
	*dev.body, *thien.body = "<html>dev</html>", "<html>thien</html>"
	if r := run(t, "", "diff", "--envs", "dev,thien", "/x", "-M"); r.code != 1 || !strings.Contains(r.out, "body (not JSON)") {
		t.Fatalf("non-JSON: %+v", r)
	}
}

func TestDiffGraphQLReadOnlyAndMutationsRefused(t *testing.T) {
	dev, thien := diffSetup(t, `{"data":{"me":{"id":"1"}}}`, `{"data":{"me":{"id":"2"}}}`)
	r := run(t, "", "diff", "--envs", "dev,thien", "--gql", "-e", "v2/user", "query { me { id } }", ".data", "-M")
	if r.code != 1 || !strings.Contains(r.out, `.me.id`) || !strings.Contains(r.out, `"1" → "2"`) {
		t.Fatalf("%+v", r)
	}
	if dev.got.path != "POST /graphql/v2/user" || thien.got.path != "POST /graphql/v2/user" {
		t.Fatalf("paths %q %q", dev.got.path, thien.got.path)
	}

	dev.got.path, thien.got.path = "", ""
	for _, q := range []string{"mutation { delete(id: 1) }", "  # hi\n mutation M { x }", "query { a } mutation { b }", "subscription { s }"} {
		if r := run(t, "", "diff", "--envs", "dev,thien", "--gql", q); r.code == 0 || !strings.Contains(r.err, "refusing") {
			t.Fatalf("%q was not refused: %+v", q, r)
		}
	}
	if dev.got.path != "" || thien.got.path != "" {
		t.Fatal("a refused mutation must send nothing")
	}
	if !gqlquery.IsReadOnly("query { me { id } }") || !gqlquery.IsReadOnly("{ __typename }") || !gqlquery.IsReadOnly("# mutation in a comment\n{ a }") {
		t.Fatal("false positive on a read-only query")
	}
}

func TestDiffArgumentErrors(t *testing.T) {
	diffSetup(t, `{}`, `{}`)
	for _, args := range [][]string{
		{"diff", "/x"},                       // --envs is required
		{"diff", "--envs", "dev", "/x"},      // needs two
		{"diff", "--envs", "dev,dev", "/x"},  // different
		{"diff", "--envs", "dev,nope", "/x"}, // unknown env
	} {
		if r := run(t, "", args...); r.code == 0 {
			t.Fatalf("%v accepted: %+v", args, r)
		}
	}
	if r := run(t, "", "diff", "--envs", "dev,nope", "/x"); !strings.Contains(r.err, `unknown env "nope" (known: dev, thien)`) {
		t.Fatalf("%q", r.err)
	}
}

func TestDiffArrayKeyIgnoresOrder(t *testing.T) {
	diffSetup(t,
		`{"data":[{"id":1,"n":"a"},{"id":2,"n":"b"}]}`,
		`{"data":[{"id":2,"n":"b"},{"id":1,"n":"a"}]}`)

	// by position: both elements differ
	if r := run(t, "", "diff", "--envs", "dev,thien", "/x", ".data", "-M"); r.code != 1 {
		t.Fatalf("positional diff of a reordered list must differ: %+v", r)
	}
	// by key: identical
	if r := run(t, "", "diff", "--envs", "dev,thien", "/x", ".data", "--array-key", "id", "-M"); r.code != 0 || !strings.Contains(r.err, "identical") {
		t.Fatalf("--array-key id: %+v", r)
	}
}

func TestDiffJSON(t *testing.T) {
	diffSetup(t,
		`{"data":[{"id":1,"n":"a"},{"id":2,"n":"b"}]}`,
		`{"data":[{"id":2,"n":"B"},{"id":1,"n":"a"},{"id":3,"n":"c"}]}`)

	r := run(t, "", "diff", "--envs", "dev,thien", "/x", ".data", "--array-key", "id", "--json")
	if r.code != 1 {
		t.Fatalf("exit status must stay 1 when different: %+v", r)
	}
	var rep struct {
		Identical   bool
		Left, Right struct {
			Env    string
			Status int
		}
		Changes []struct {
			Path, Kind string
			A, B       any
		}
	}
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.out)
	}
	if rep.Identical || rep.Left.Env != "dev" || rep.Right.Env != "thien" || rep.Left.Status != 200 || len(rep.Changes) != 2 {
		t.Fatalf("%+v", rep)
	}
	if rep.Changes[0].Path != "[id=2].n" || rep.Changes[0].Kind != "changed" || rep.Changes[0].A != "b" || rep.Changes[0].B != "B" {
		t.Fatalf("%+v", rep.Changes[0])
	}
	if rep.Changes[1].Path != "[id=3]" || rep.Changes[1].Kind != "added" || rep.Changes[1].A != nil {
		t.Fatalf("%+v", rep.Changes[1])
	}
	if strings.Contains(r.out, "DEV-SECRET") || strings.Contains(r.out, "THIEN-SECRET") || strings.Contains(r.out, "---") {
		t.Fatalf("JSON mode must contain only the report: %s", r.out)
	}

	// identical -> exit 0, still JSON
	diffSetup(t, `{"a":1}`, `{"a":1}`)
	r = run(t, "", "diff", "--envs", "dev,thien", "/x", "--json")
	if r.code != 0 || !strings.Contains(r.out, `"identical": true`) || !strings.Contains(r.out, `"changes": []`) {
		t.Fatalf("%+v", r)
	}
}
