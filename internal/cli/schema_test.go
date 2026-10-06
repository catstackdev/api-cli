package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cybercat/api/internal/schema"
)

type schemaSrv struct {
	mu       sync.Mutex
	paths    []string
	bodies   []string
	auths    []string
	disabled bool
}

func (s *schemaSrv) handler(t *testing.T) http.HandlerFunc {
	fixture, err := os.ReadFile("../schema/testdata/user.json")
	if err != nil {
		t.Fatal(err)
	}
	var sch schema.Schema
	if err := json.Unmarshal(fixture, &sch); err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.bodies = append(s.bodies, string(body))
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if s.disabled {
			_, _ = w.Write([]byte(`{"errors":[{"message":"GraphQL introspection is not allowed"}],"data":null}`))
			return
		}
		switch {
		case strings.Contains(req.Query, "__type("):
			if ty, ok := sch.FindType(req.Variables["n"]); ok {
				b, _ := json.Marshal(map[string]any{"data": map[string]any{"__type": ty}})
				_, _ = w.Write(b)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"__type":null}}`))
		case strings.Contains(req.Query, "types {"):
			_, _ = w.Write(append(append([]byte(`{"data":{"__schema":`), fixture...), '}', '}'))
		default:
			_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"fields":[{"name":"me"},{"name":"user"}]},"mutationType":{"fields":[{"name":"login"}]}}}}`))
		}
	}
}

func schemaSetup(t *testing.T) (*schemaSrv, string) {
	t.Helper()
	s := &schemaSrv{}
	srv := server(t, s.handler(t))
	t.Setenv("API_GRAPHQL_URL", srv.URL+"/graphql")
	t.Setenv("API_TOKEN", "TOK-0123456789-SECRETTAIL")
	cache := t.TempDir()
	t.Setenv("UGQL_CACHE_DIR", cache)
	return s, cache
}

func TestSchemaNamesAndTypeMatchTheShellHelpers(t *testing.T) {
	s, _ := schemaSetup(t)

	r := run(t, "", "schema", "names", "user", "--prefix", "v2/")
	want := "{\n  \"queries\": [\n    \"me\",\n    \"user\"\n  ],\n  \"mutations\": [\n    \"login\"\n  ]\n}\n"
	if r.code != 0 || r.out != want {
		t.Fatalf("names: %+v", r)
	}
	if s.paths[0] != "/graphql/v2/user" || s.auths[0] != "Bearer TOK-0123456789-SECRETTAIL" {
		t.Fatalf("request: %v %v", s.paths, s.auths)
	}

	r = run(t, "", "schema", "type", "user", "Query", "--prefix", "v2/")
	want = "{\n  \"name\": \"Query\",\n  \"kind\": \"OBJECT\",\n  \"fields\": [\n    \"me: User\",\n    \"user(id: ID!): User\"\n  ],\n  \"inputFields\": [],\n  \"enumValues\": []\n}\n"
	if r.code != 0 || r.out != want {
		t.Fatalf("type: %+v", r)
	}

	if r := run(t, "", "schema", "type", "user", "Nope", "--prefix", "v2/"); r.code != exitGeneric || !strings.Contains(r.err, `no type "Nope"`) {
		t.Fatalf("missing type: %+v", r)
	}
}

func TestSchemaDumpKeepsServerOrder(t *testing.T) {
	schemaSetup(t)
	r := run(t, "", "schema", "dump", "user", "--prefix", "v2/")
	if r.code != 0 || !strings.HasPrefix(r.out, "{\n  \"queryType\"") || !strings.Contains(r.out, `"name": "LoginInput"`) {
		t.Fatalf("%+v", r)
	}
}

func TestSchemaSyncWritesTheCacheTheBrowsersRead(t *testing.T) {
	s, cache := schemaSetup(t)
	t.Setenv("API_ENV", "")

	r := run(t, "", "schema", "sync", "user", "--prefix", "v2/")
	if r.code != 0 || !strings.Contains(r.err, "✓ user (5 types)") || !strings.Contains(r.err, "✓ 6 items") {
		t.Fatalf("%+v", r)
	}
	index, _ := os.ReadFile(filepath.Join(cache, "index.txt"))
	golden, _ := os.ReadFile("../schema/testdata/index.golden")
	if string(index) != string(golden) {
		t.Fatalf("index.txt differs from the shell implementation's:\n%s", index)
	}
	preview, _ := os.ReadFile(filepath.Join(cache, "preview.jq"))
	if string(preview) != schema.PreviewProgram {
		t.Fatal("preview.jq differs")
	}
	if env, _ := os.ReadFile(filepath.Join(cache, ".env")); string(env) != "?\n" {
		t.Fatalf(".env = %q", env)
	}
	if data, err := os.ReadFile(filepath.Join(cache, "user.json")); err != nil || !strings.Contains(string(data), `"mutationType"`) {
		t.Fatalf("user.json: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(cache, ".tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if s.paths[0] != "/graphql/v2/user" {
		t.Fatalf("paths %v", s.paths)
	}
}

func TestSchemaSyncFetchesManyEndpointsConcurrentlySafely(t *testing.T) {
	s, cache := schemaSetup(t)
	args := []string{"schema", "sync", "--prefix", "v2/"}
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"} {
		args = append(args, n)
	}
	r := run(t, "", args...)
	if r.code != 0 || len(s.paths) != 9 {
		t.Fatalf("%+v paths=%d", r, len(s.paths))
	}
	files, _ := filepath.Glob(filepath.Join(cache, "a*.json"))
	if len(files) != 9 {
		t.Fatalf("cached %d files", len(files))
	}
}

func TestSchemaSyncNeverWritesOutsideTheCache(t *testing.T) {
	_, cache := schemaSetup(t)
	parent := filepath.Dir(cache)
	r := run(t, "", "schema", "sync", "v2/..", "..", ".hidden", "a b", "https://evil.example/graphql/x", "x/y\\z", "ok")
	if !strings.Contains(r.err, "skipped: not a usable endpoint name") {
		t.Fatalf("%+v", r)
	}
	// exactly the valid endpoint plus the generated files, nothing else
	entries, _ := os.ReadDir(cache)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if want := ".env index.txt ok.json preview.jq"; strings.Join(got, " ") != want {
		t.Fatalf("cache holds %q, want %q", got, want)
	}
	if matches, _ := filepath.Glob(filepath.Join(parent, "*.json")); len(matches) != 0 {
		t.Fatalf("wrote outside the cache: %v", matches)
	}
}

func TestSchemaSyncFromConfigEndpoints(t *testing.T) {
	_, cache := schemaSetup(t)
	srvURL := os.Getenv("API_BASE_URL")
	writeConfig(t, "[env.dev]\nurl = \""+srvURL+"\"\nendpoints = [\"v2/user\", \"v2/auth\"]\n")
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", srvURL)
	if r := run(t, "", "schema", "sync"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, n := range []string{"user", "auth"} {
		if _, err := os.Stat(filepath.Join(cache, n+".json")); err != nil {
			t.Fatalf("%s not cached: %v", n, err)
		}
	}
	if env, _ := os.ReadFile(filepath.Join(cache, ".env")); string(env) != "dev\n" {
		t.Fatalf(".env = %q", env)
	}
}

func TestSchemaDisabledIntrospectionIsReported(t *testing.T) {
	s, cache := schemaSetup(t)
	s.disabled = true
	if r := run(t, "", "schema", "dump", "user", "--prefix", "v2/"); r.code != exitGraphQL || !strings.Contains(r.err, "introspection is not allowed") {
		t.Fatalf("dump: %+v", r)
	}
	r := run(t, "", "schema", "sync", "user", "--prefix", "v2/")
	if r.code == 0 || !strings.Contains(r.err, "introspection is not allowed") {
		t.Fatalf("sync: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cache, "user.json")); err == nil {
		t.Fatal("a failed fetch must not leave a schema file")
	}
}

func TestSchemaDryRunSendsAndWritesNothing(t *testing.T) {
	s, cache := schemaSetup(t)
	r := run(t, "", "schema", "sync", "user", "--prefix", "v2/", "--dry")
	if r.code != 0 || !strings.HasPrefix(r.out, "curl ") || len(s.paths) != 0 {
		t.Fatalf("%+v paths=%v", r, s.paths)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("--dry wrote %d file(s)", len(entries))
	}
	if strings.Contains(r.out, "SECRETTAIL") {
		t.Fatalf("--dry printed the token: %s", r.out)
	}
}

func TestSchemaNeverSendsYourTokenToAnotherServer(t *testing.T) {
	schemaSetup(t)
	evil := &schemaSrv{}
	evilSrv := server2(t, evil.handler(t))
	t.Setenv("API_TOKEN", "TOK-0123456789-SECRETTAIL") // server() cleared it for the second server
	t.Setenv("API_BASE_URL", os.Getenv("API_GRAPHQL_URL")[:strings.Index(os.Getenv("API_GRAPHQL_URL"), "/graphql")])
	run(t, "", "schema", "dump", evilSrv.URL+"/graphql/x")
	run(t, "", "schema", "sync", evilSrv.URL+"/graphql/x") // URLs are refused as cache labels
	if len(evil.auths) == 0 {
		t.Fatal("the evil server was never contacted: the test checks nothing")
	}
	for _, a := range evil.auths {
		if a != "" {
			t.Fatalf("the session token reached another server: %q", a)
		}
	}
}

func server2(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestSchemaSyncRefusesUrlEndpointsAsCacheLabels(t *testing.T) {
	s, cache := schemaSetup(t)
	url := os.Getenv("API_BASE_URL") + "/graphql/x"
	r := run(t, "", "schema", "sync", url)
	if !strings.Contains(r.err, "not a usable endpoint name") || len(s.paths) != 0 {
		t.Fatalf("%+v requests=%v", r, s.paths)
	}
	if _, err := os.Stat(filepath.Join(cache, "x.json")); err == nil {
		t.Fatal("a URL endpoint was turned into a cache file")
	}
}

func TestSchemaEndpointsAndPrefixFromConfig(t *testing.T) {
	s, _ := schemaSetup(t)
	srvURL := os.Getenv("API_BASE_URL")
	writeConfig(t, "[env.dev]\nurl = \""+srvURL+"\"\nendpoints = [\"user\", \"auth\"]\nendpoint_prefix = \"v2/\"\n")
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", srvURL)

	if r := run(t, "", "schema", "endpoints"); r.code != 0 || r.out != "user\nauth\n" {
		t.Fatalf("%+v", r)
	}
	// no --prefix: the env's endpoint_prefix applies
	if r := run(t, "", "schema", "names", "user"); r.code != 0 || s.paths[len(s.paths)-1] != "/graphql/v2/user" {
		t.Fatalf("%+v %v", r, s.paths)
	}
	// an explicit --prefix wins
	run(t, "", "schema", "names", "user", "--prefix", "v3/")
	if s.paths[len(s.paths)-1] != "/graphql/v3/user" {
		t.Fatalf("%v", s.paths)
	}
	// set/get round trip, and a path-like prefix is refused
	if r := run(t, "", "config", "set", "env.dev.endpoint_prefix", "v4/", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "get", "env.dev.endpoint_prefix"); !strings.Contains(r.out, "v4/") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "set", "env.dev.endpoint_prefix", "../x", "--global"); r.code == 0 {
		t.Fatalf("a .. prefix was accepted: %+v", r)
	}
}

func TestSchemaEndpointsWithNoneConfigured(t *testing.T) {
	schemaSetup(t)
	if r := run(t, "", "schema", "endpoints"); r.code == 0 || !strings.Contains(r.err, "no endpoints") {
		t.Fatalf("%+v", r)
	}
}

func TestSchemaTypesListsRootsFirst(t *testing.T) {
	schemaSetup(t)
	r := run(t, "", "schema", "types", "user", "--prefix", "v2/")
	if r.code != 0 || r.out != "Query\nMutation\nLoginInput\nRole\nUser\n" {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "schema", "types", "user", "--prefix", "v2/", "--kinds"); !strings.HasPrefix(r.out, "query Query\nmutation Mutation\n") {
		t.Fatalf("%+v", r)
	}
}
