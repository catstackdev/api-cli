package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── run (.http files) ──────────────────────────────────────────────────────

type seen struct {
	method, path, auth, ctype, body, trace string
}

func runServer(t *testing.T) (*httptest.Server, *[]seen) {
	t.Helper()
	var log []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		log = append(log, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Content-Type"), b.String(), r.Header.Get("X-Trace")})
		if r.URL.Path == "/fail" {
			w.WriteHeader(500)
		}
		_, _ = w.Write([]byte(`{"data":{"id":42,"n":3}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &log
}

func TestRunSavedRequests(t *testing.T) {
	srv, log := runServer(t)
	dir := projectWith(t, map[string]string{".git": ""})
	_ = os.Remove(filepath.Join(dir, ".git"))
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o700)
	reqDir := filepath.Join(dir, ".api", "requests")
	_ = os.MkdirAll(reqDir, 0o700)
	_ = os.WriteFile(filepath.Join(reqDir, "users.http"), []byte(`
@id = 7

### List
# @filter .data.id
GET /users
    ?limit=5
Accept: application/json

### Create
POST /users
Content-Type: application/json
X-Trace: {{$uuid}}

{"id": {{id}}, "base": "{{base}}"}

### FromFile
PUT /things/{{id}}

< ./payload.json

### Fail
GET /fail
`), 0o600)
	_ = os.WriteFile(filepath.Join(reqDir, "payload.json"), []byte(`{"from":"file"}`+"\n"), 0o600)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "TOK")
	t.Setenv("API_ENV", "")
	t.Setenv("API_GRAPHQL_URL", "")

	// by saved name, picking a request by name; the @filter annotation applies; session auth is sent
	if r := run(t, "", "run", "users", "List"); r.code != 0 || strings.TrimSpace(r.out) != "42" {
		t.Fatalf("%+v", r)
	}
	if got := (*log)[0]; got.method != "GET" || got.path != "/users?limit=5" || got.auth != "Bearer TOK" {
		t.Fatalf("%+v", got)
	}
	// a number works, --var beats the file variable, {{base}} is the session, body is interpolated
	if r := run(t, "", "run", "users", "2", "--var", "id=99", ".data.n"); r.code != 0 || strings.TrimSpace(r.out) != "3" {
		t.Fatalf("%+v", r)
	}
	got := (*log)[1]
	if got.method != "POST" || got.body != `{"id": 99, "base": "`+srv.URL+`"}` || got.ctype != "application/json" || len(got.trace) != 36 {
		t.Fatalf("%+v", got)
	}
	// body from a file, relative to the .http file
	if r := run(t, "", "run", "users", "FromFile", "--var", "id=5"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if got := (*log)[2]; got.method != "PUT" || got.path != "/things/5" || got.body != `{"from":"file"}` {
		t.Fatalf("%+v", got)
	}
	// listing
	if r := run(t, "", "run", "--list"); !strings.Contains(r.out, "users") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "run", "users", "--list"); !strings.Contains(r.out, "FromFile") || !strings.Contains(r.out, "PUT") {
		t.Fatalf("%+v", r)
	}
	// an ambiguous file asks for a selector; an unknown one lists the choices
	if r := run(t, "", "run", "users"); r.code == 0 || !strings.Contains(r.err, "4 requests") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "run", "users", "nope"); r.code == 0 || !strings.Contains(r.err, "have: List, Create") {
		t.Fatalf("%+v", r)
	}
	// --dry sends nothing
	n := len(*log)
	if r := run(t, "", "run", "users", "List", "--dry"); r.code != 0 || !strings.HasPrefix(r.out, "curl ") || len(*log) != n {
		t.Fatalf("%+v", r)
	}
	// --all stops at the first failure unless --keep-going
	*log = nil
	r := run(t, "", "run", "users", "--all", "--var", "id=1", "-M")
	if r.code != 22 || len(*log) != 4 { // List, Create, FromFile, Fail(500) -> stop (Fail is last anyway)
		t.Fatalf("all: %+v log=%d", r, len(*log))
	}
	if !strings.Contains(r.err, "→ Create: POST /users") {
		t.Fatalf("progress lines: %q", r.err)
	}
}

func TestRunSafety(t *testing.T) {
	srv, log := runServer(t)
	evil, evilLog := runServer(t)
	dir := projectWith(t, nil)
	file := filepath.Join(dir, "x.http")
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "TOK")
	t.Setenv("API_ENV", "")
	t.Setenv("API_PASSWORD", "pw")
	t.Setenv("HOME_SECRET", "nope")
	runFile := func(body string, args ...string) result {
		_ = os.WriteFile(file, []byte(body), 0o600)
		return run(t, "", append([]string{"run", file}, args...)...)
	}

	// a foreign host never gets the session token implicitly
	if r := runFile("GET " + evil.URL + "/x\n"); r.code != 0 || (*evilLog)[0].auth != "" {
		t.Fatalf("session token leaked to a foreign host: %+v %+v", r, *evilLog)
	}
	// ...and a request that explicitly interpolates it is refused, sending nothing
	*evilLog = nil
	r := runFile("GET " + evil.URL + "/x\nAuthorization: Bearer {{token}}\n")
	if r.code == 0 || !strings.Contains(r.err, "--trust-hosts") || len(*evilLog) != 0 {
		t.Fatalf("token to foreign host: %+v sent=%d", r, len(*evilLog))
	}
	if r := runFile("GET " + evil.URL + "/x\nX-P: {{$env API_PASSWORD}}\n"); r.code == 0 || !strings.Contains(r.err, "--trust-hosts") || len(*evilLog) != 0 {
		t.Fatalf("$env to foreign host: %+v", r)
	}
	// --trust-hosts is the explicit opt-in
	if r := runFile("GET "+evil.URL+"/x\nAuthorization: Bearer {{token}}\n", "--trust-hosts"); r.code != 0 || (*evilLog)[0].auth != "Bearer TOK" {
		t.Fatalf("trust: %+v %+v", r, *evilLog)
	}
	// the active base URL is fine
	*log = nil
	if r := runFile("GET {{base}}/ok\nX-P: {{$env API_PASSWORD}}\n"); r.code != 0 || (*log)[0].auth != "Bearer TOK" {
		t.Fatalf("own base: %+v", r)
	}
	// $env only reads API_*
	if r := runFile("GET /x\nX-P: {{$env HOME_SECRET}}\n"); r.code == 0 || !strings.Contains(r.err, "only API_*") {
		t.Fatalf("$env HOME_SECRET: %+v", r)
	}
	// undefined variables fail with the line number; nothing is sent
	n := len(*log)
	if r := runFile("\n\nGET /x/{{nope}}\n"); r.code == 0 || !strings.Contains(r.err, "undefined variable {{nope}}") || !strings.Contains(r.err, "line 3") || len(*log) != n {
		t.Fatalf("undefined: %+v", r)
	}
	// a variable cannot smuggle a line break into a header
	if r := runFile("GET /x\nX-A: {{v}}\n", "--var", "v=a\r\nX-Evil: 1"); r.code == 0 || !strings.Contains(r.err, "line break") || len(*log) != n {
		t.Fatalf("header injection: %+v", r)
	}
	if r := run(t, "", "run", "does-not-exist"); r.code == 0 || !strings.Contains(r.err, "no such request file") {
		t.Fatalf("%+v", r)
	}
}
