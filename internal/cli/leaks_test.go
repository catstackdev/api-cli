package cli

import (
	"encoding/base64"
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"
)

// Everything here asserts the same thing from different angles: a secret or a
// token must only ever reach the server the user chose, never one that a cloned
// repo, a pasted URL or a debug flag could name.

func TestSessionEnvNameCannotAttachAProfileToAnotherServer(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	evilLogin := newLoginSrv(t, loginOK)
	evil, evilHit := envServer(t)
	mem := secretstore.NewMemory()
	_ = mem.Set("upscale-dev", secretstore.Secret{Value: "REAL-KEYCHAIN-PASSWORD"})
	withSecrets(t, mem, nil)
	writeConfig(t, `
[env.dev]
url = "`+real.srv.URL+`"
token_env = "API_TOKEN_DEV"
headers = { "X-Tenant" = "dev-tenant" }
[env.dev.login]
path = "/auth"
vars = { user = "me", password = "@keychain:upscale-dev" }
`)
	t.Setenv("API_TOKEN_DEV", "DEV-ENV-TOKEN")
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_GRAPHQL_URL", "")
	// `api env load dev` inside a cloned repo: its .env.dev supplies the URL and
	// the shell ends up with API_ENV=dev pointing at the repo's server
	t.Setenv("API_ENV", "dev")

	t.Setenv("API_BASE_URL", evilLogin.srv.URL)
	r := run(t, "", "login")
	if evilLogin.hits != 0 || strings.Contains(evilLogin.body, "REAL-KEYCHAIN-PASSWORD") {
		t.Fatalf("the Keychain password reached a server that only shares the env NAME: %+v body=%q", r, evilLogin.body)
	}
	if r.code == 0 || !strings.Contains(r.err, "no login configured") {
		t.Fatalf("plain login must refuse: %+v", r)
	}

	// headers and token_env are attached by profile too
	t.Setenv("API_BASE_URL", evil.URL)
	*evilHit = hit{}
	if r := run(t, "", "get", "/x"); r.code != 0 || evilHit.path != "/x" {
		t.Fatalf("setup: %+v", r)
	}
	if evilHit.tenant != "" || evilHit.auth != "" {
		t.Fatalf("the profile's header / token_env followed the env name to another server: %+v", evilHit)
	}

	// the GraphQL URL is a second way to point elsewhere
	t.Setenv("API_BASE_URL", real.srv.URL)
	t.Setenv("API_GRAPHQL_URL", evilLogin.srv.URL+"/graphql")
	run(t, "", "login")
	if evilLogin.hits != 0 {
		t.Fatalf("a repointed API_GRAPHQL_URL must also detach the profile (hits=%d)", evilLogin.hits)
	}

	// and the legitimate case still works: same server
	t.Setenv("API_GRAPHQL_URL", "")
	real.hits = 0
	if r := run(t, "", "login", "--emit"); r.code != 0 || real.hits != 1 || !strings.Contains(real.body, "REAL-KEYCHAIN-PASSWORD") {
		t.Fatalf("same server: %+v hits=%d", r, real.hits)
	}
}

func TestDebugAndDryOutputNeverContainSecrets(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	mem := secretstore.NewMemory()
	const pw = "kc-PASSWORD-9z"
	_ = mem.Set("pw", secretstore.Secret{Value: pw})
	withSecrets(t, mem, nil)
	t.Setenv("API_BASE_URL", l.srv.URL)
	t.Setenv("API_GRAPHQL_URL", l.srv.URL+"/graphql")
	t.Setenv("API_TOKEN", "")
	forms := []string{pw, base64.StdEncoding.EncodeToString([]byte(pw))}
	leaked := func(s string) bool {
		for _, f := range forms {
			if strings.Contains(s, f) {
				return true
			}
		}
		return false
	}
	login := []string{"login", "--gql", "-e", "v2/auth", loginMutation, "-V", "input.username=me", "-V", "input.password=@keychain:pw", "--emit"}

	// --debug prints the curl command while the request IS sent (references resolved)
	r := run(t, "", append([]string{"--debug"}, login...)...)
	if r.code != 0 || !strings.Contains(l.body, pw) {
		t.Fatalf("setup: the real request must carry the password: %+v", r)
	}
	if leaked(r.out + r.err) {
		t.Fatalf("--debug printed the resolved password:\n%s", r.err)
	}
	if !strings.Contains(r.err, "→ curl") || !strings.Contains(r.err, "[REDACTED]") {
		t.Fatalf("--debug should still show the command, masked:\n%s", r.err)
	}
	// API_DEBUG=1 is the same switch
	t.Setenv("API_DEBUG", "1")
	r = run(t, "", login...)
	if leaked(r.out+r.err) || !strings.Contains(r.err, "→ curl") {
		t.Fatalf("API_DEBUG=1:\n%s", r.err)
	}
	t.Setenv("API_DEBUG", "")
	// -v prints headers of a login
	if r := run(t, "", append([]string{"-v"}, login[:len(login)-1]...)...); leaked(r.out + r.err) {
		t.Fatalf("-v leaked: %s", r.err)
	}

	// --dry on literal credentials, forms, query strings and headers
	mask := func(args ...string) string {
		t.Helper()
		r := run(t, "", append(args, "--dry")...)
		if r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		return r.out
	}
	if out := mask("post", "/login", `{"password":"hunter2","a":1}`); strings.Contains(out, "hunter2") || !strings.Contains(out, `"a":1`) {
		t.Fatalf("json body: %s", out)
	}
	if out := mask("form", "/f", "-d", "password=hunter2", "-d", "email=a@b.c", "-F", "client_secret=zzz9"); strings.Contains(out, "hunter2") || strings.Contains(out, "zzz9") || !strings.Contains(out, "email=a@b.c") {
		t.Fatalf("form: %s", out)
	}
	if out := mask("get", "/x?access_token=tok777&limit=5"); strings.Contains(out, "tok777") || !strings.Contains(out, "limit=5") {
		t.Fatalf("query: %s", out)
	}
	if out := mask("get", "/x", "-H", "X-Api-Key: key888", "-H", "X-Request-Id: keep-me"); strings.Contains(out, "key888") || !strings.Contains(out, "keep-me") {
		t.Fatalf("headers: %s", out)
	}
	// a body with nothing secret is shown exactly as sent (key order intact)
	if out := mask("post", "/x", `{"zeta":1,"alpha":2}`); !strings.Contains(out, `{"zeta":1,"alpha":2}`) {
		t.Fatalf("a harmless body must not be rewritten: %s", out)
	}
}

func TestProjectAliasCannotCarryReferencesOrTrustFlags(t *testing.T) {
	evil := newLoginSrv(t, loginOK)
	real, realHit := envServer(t)
	withSecrets(t, secretstore.NewMemory(), nil)
	t.Setenv("API_BASE_URL", real.URL)
	t.Setenv("API_TOKEN", "SESSION-TOKEN")
	t.Setenv("MY_VALUE", "from-env")
	writeConfig(t, "[alias]\ngme = \"json v=@env:MY_VALUE -c\"\n")
	projectWith(t, map[string]string{".api.toml": `
[alias]
me = "login --gql -e ` + evil.srv.URL + `/graphql 'mutation { x }' -V input.password=@keychain:upscale-dev"
t = "run x --trust-hosts"
b = "get /x --base ` + evil.srv.URL + `"
tok = "get /x --token abc"
e = "json p=@env:HOME"
h = "get /x --har-secrets"
ok = "get /ping"
`})

	r := run(t, "", "ok")
	if r.code != 0 || realHit.path != "/ping" {
		t.Fatalf("a harmless project alias must keep working: %+v", r)
	}
	for _, name := range []string{"me", "t", "b", "tok", "e", "h"} {
		if r := run(t, "", name); r.code == 0 || !strings.Contains(r.err, "unknown command") {
			t.Errorf("project alias %q ran: %+v", name, r)
		}
	}
	if evil.hits != 0 {
		t.Fatalf("a project alias reached the evil server %d time(s)", evil.hits)
	}
	if r := run(t, "", "ok"); !strings.Contains(r.err, `alias "me" ignored`) || !strings.Contains(r.err, "global config") {
		t.Fatalf("the user must be told why: %q", r.err)
	}
	// YOUR global alias may use references: it is yours
	if r := run(t, "", "gme"); r.code != 0 || strings.TrimSpace(r.out) != `{"v":"from-env"}` {
		t.Fatalf("global alias with a reference: %+v", r)
	}
}

func TestAbsoluteURLOnAnotherHostGetsNoSessionToken(t *testing.T) {
	real, realHit := envServer(t)
	other, otherHit := envServer(t)
	t.Setenv("API_BASE_URL", real.URL)
	t.Setenv("API_TOKEN", "SESSION-TOKEN")

	r := run(t, "", "get", other.URL+"/x")
	if r.code != 0 || otherHit.auth != "" || !strings.Contains(r.err, "not sending your token") {
		t.Fatalf("session token sent to another host: %+v auth=%q", r, otherHit.auth)
	}
	if r := run(t, "", "get", other.URL+"/x", "--trust-hosts"); r.code != 0 || otherHit.auth != "Bearer SESSION-TOKEN" {
		t.Fatalf("--trust-hosts: %+v auth=%q", r, otherHit.auth)
	}
	if r := run(t, "", "get", other.URL+"/x", "--token", "EXPLICIT"); r.code != 0 || otherHit.auth != "Bearer EXPLICIT" {
		t.Fatalf("an explicit --token is the user's choice: %+v auth=%q", r, otherHit.auth)
	}
	if r := run(t, "", "get", real.URL+"/y"); r.code != 0 || realHit.auth != "Bearer SESSION-TOKEN" || strings.Contains(r.err, "not sending") {
		t.Fatalf("an absolute URL on the active base keeps working: %+v auth=%q", r, realHit.auth)
	}
	if r := run(t, "", "get", "/z"); realHit.auth != "Bearer SESSION-TOKEN" {
		t.Fatalf("relative: %+v", r)
	}
	// with no active base at all there is no host to trust
	t.Setenv("API_BASE_URL", "")
	if r := run(t, "", "get", other.URL+"/w"); r.code != 0 || otherHit.auth != "Bearer EXPLICIT" && otherHit.auth != "" {
		t.Fatalf("%+v", r)
	}
}
