package cli

import (
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"
)

// A secret bound to servers (api secret set NAME --for URL) is released only to
// them. That is what makes a per-project login safe: a project can describe its
// login, but a cloned repo cannot send your password anywhere else.

const projectLoginToml = `
[env.dev]
url = "%URL%"
[env.dev.login]
path = "/auth"
vars = { user = "me@x.com", password = "@keychain:pw" }
`

func TestProjectLoginWorksWithASecretBoundToItsServer(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	boundSecret(t, "pw", "BOUND-PASSWORD", real.srv.URL)
	clearSession(t)
	writeConfig(t, "") // nothing global: the env and its login live in the project
	projectWith(t, map[string]string{".api.toml": strings.ReplaceAll(projectLoginToml, "%URL%", real.srv.URL)})

	r := run(t, "", "login", "--env", "dev", "--emit")
	if r.code != 0 || real.hits != 1 || !strings.Contains(real.body, `"password":"BOUND-PASSWORD"`) {
		t.Fatalf("a per-project login must work: %+v hits=%d body=%s", r, real.hits, real.body)
	}
	if !strings.Contains(r.out, "export API_TOKEN='REST-TOKEN'") || strings.Contains(r.out+r.err, "BOUND-PASSWORD") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.err, "ignored") {
		t.Fatalf("a valid project login must not be reported as ignored: %q", r.err)
	}
}

func TestClonedRepoCannotRedirectABoundSecret(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	evil := newLoginSrv(t, loginOK)
	boundSecret(t, "pw", "REAL-PASSWORD", real.srv.URL) // bound to the real server only
	clearSession(t)
	writeConfig(t, "")
	// the cloned repo names YOUR secret but its own server
	projectWith(t, map[string]string{".api.toml": strings.ReplaceAll(projectLoginToml, "%URL%", evil.srv.URL)})

	r := run(t, "", "login", "--env", "dev", "--emit")
	if evil.hits != 0 || strings.Contains(evil.body, "REAL-PASSWORD") {
		t.Fatalf("the password reached the repo's server: %+v body=%q", r, evil.body)
	}
	if r.code == 0 || !strings.Contains(r.err, "is bound to "+origin(real.srv.URL)) || !strings.Contains(r.err, "will not be sent to "+origin(evil.srv.URL)) {
		t.Fatalf("the refusal must say why and how to fix it: %+v", r)
	}
	if !strings.Contains(r.err, "api secret bind pw") {
		t.Fatalf("missing the fix: %q", r.err)
	}
}

func TestProjectLoginRules(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	clearSession(t)

	// unbound secret: refused for a project login, nothing sent
	mem := secretstore.NewMemory()
	_ = mem.Set("pw", secretstore.Secret{Value: "UNBOUND"})
	withSecrets(t, mem, nil)
	writeConfig(t, "")
	projectWith(t, map[string]string{".api.toml": strings.ReplaceAll(projectLoginToml, "%URL%", l.srv.URL)})
	if r := run(t, "", "login", "--env", "dev"); r.code == 0 || l.hits != 0 || !strings.Contains(r.err, "api secret bind pw "+origin(l.srv.URL)) {
		t.Fatalf("unbound: %+v hits=%d", r, l.hits)
	}
	// ...and binding it (what the message says to do) makes it work
	if r := run(t, "", "secret", "bind", "pw", l.srv.URL); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "login", "--env", "dev", "--emit"); r.code != 0 || l.hits != 1 {
		t.Fatalf("after bind: %+v", r)
	}

	// @env: in a project login is dropped: it could read any variable of yours
	t.Setenv("MY_SECRET_VAR", "env-secret")
	l.hits = 0
	projectWith(t, map[string]string{".api.toml": "[env.dev]\nurl = \"" + l.srv.URL + "\"\n[env.dev.login]\npath = \"/auth\"\nvars = { user = \"me\", password = \"@env:MY_SECRET_VAR\" }\n"})
	r := run(t, "", "login", "--env", "dev")
	if r.code == 0 || l.hits != 0 || !strings.Contains(r.err, "cannot use @env:") {
		t.Fatalf("@env: in a project login: %+v hits=%d", r, l.hits)
	}

	// a project cannot redefine the login of an env your GLOBAL config anchors
	writeConfig(t, "[env.dev]\nurl = \""+l.srv.URL+"\"\n[env.dev.login]\npath = \"/auth\"\nvars = { user = \"global-user\", password = \"@keychain:pw\" }\n")
	projectWith(t, map[string]string{".api.toml": "[env.dev.login]\npath = \"/steal\"\nvars = { user = \"proj-user\", password = \"@keychain:pw\" }\n"})
	l.hits = 0
	r = run(t, "", "login", "--env", "dev", "--emit")
	if r.code != 0 || l.path != "/auth" || !strings.Contains(l.body, "global-user") || !strings.Contains(r.err, "defined in your global config") {
		t.Fatalf("the global login must win: %+v path=%q body=%s", r, l.path, l.body)
	}
}

func TestPastedCommandCannotRedirectABoundSecret(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	evil := newLoginSrv(t, loginOK)
	boundSecret(t, "pw", "REAL-PASSWORD", real.srv.URL)
	clearSession(t)
	writeConfig(t, "")
	login := func(base string) result {
		return run(t, "", "login", "--gql", "-e", base+"/graphql", loginMutation, "-V", "input.username=me", "-V", "input.password=@keychain:pw", "--emit")
	}
	// a command copied from a malicious README: right syntax, wrong server
	r := login(evil.srv.URL)
	if evil.hits != 0 || strings.Contains(evil.body, "REAL-PASSWORD") || r.code == 0 || !strings.Contains(r.err, "will not be sent to") {
		t.Fatalf("%+v hits=%d", r, evil.hits)
	}
	if r := login(real.srv.URL); r.code != 0 || real.hits != 1 || !strings.Contains(real.body, "REAL-PASSWORD") {
		t.Fatalf("the bound server must still work: %+v", r)
	}
	// api gql (not only login) checks too
	if r := run(t, "", "gql", "-e", evil.srv.URL+"/graphql", "query($p:String){x(p:$p)}", "-V", "p=@keychain:pw"); r.code == 0 || evil.hits != 0 {
		t.Fatalf("gql: %+v hits=%d", r, evil.hits)
	}
}

func TestDiffRequiresASecretToBeBoundToBothServers(t *testing.T) {
	a := newLoginSrv(t, `{"data":{}}`)
	b := newLoginSrv(t, `{"data":{}}`)
	boundSecret(t, "pw", "SHARED-SECRET", a.srv.URL) // only server A
	clearSession(t)
	writeConfig(t, "[env.a]\nurl = \""+a.srv.URL+"\"\ngraphql = \""+a.srv.URL+"/graphql\"\n[env.b]\nurl = \""+b.srv.URL+"\"\ngraphql = \""+b.srv.URL+"/graphql\"\n")
	args := []string{"diff", "--envs", "a,b", "--gql", "query($p:String){x(p:$p)}", "-V", "p=@keychain:pw"}
	if r := run(t, "", args...); r.code == 0 || a.hits != 0 || b.hits != 0 {
		t.Fatalf("a secret bound to one of the two servers must not be sent to either: %+v a=%d b=%d", r, a.hits, b.hits)
	}
	if r := run(t, "", "secret", "bind", "pw", b.srv.URL); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", args...); a.hits != 1 || b.hits != 1 {
		t.Fatalf("bound to both: %+v a=%d b=%d", r, a.hits, b.hits)
	}
}

func TestUnboundSecretsStillWorkForYourOwnCommandsAndGlobalConfig(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	mem := secretstore.NewMemory()
	_ = mem.Set("pw", secretstore.Secret{Value: "UNBOUND-PW"})
	withSecrets(t, mem, nil)
	clearSession(t)
	// typed on the command line: your choice of server
	if r := run(t, "", "login", "--gql", "-e", l.srv.URL+"/graphql", loginMutation, "-V", "input.password=@keychain:pw", "--emit"); r.code != 0 || !strings.Contains(l.body, "UNBOUND-PW") {
		t.Fatalf("%+v", r)
	}
	// your global config: yours
	writeConfig(t, "[env.dev]\nurl = \""+l.srv.URL+"\"\n[env.dev.login]\npath = \"/auth\"\nvars = { user = \"me\", password = \"@keychain:pw\" }\n")
	l.hits = 0
	if r := run(t, "", "login", "--env", "dev", "--emit"); r.code != 0 || l.hits != 1 {
		t.Fatalf("%+v", r)
	}
	// api json is local (no destination): bound or not, it just prints
	boundSecret(t, "pw", "LOCAL-VALUE", "https://api.example.com")
	if r := run(t, "", "json", "p=@keychain:pw", "-c"); strings.TrimSpace(r.out) != `{"p":"LOCAL-VALUE"}` {
		t.Fatalf("%+v", r)
	}
}

func TestSecretSetBindShow(t *testing.T) {
	mem := secretstore.NewMemory()
	withSecrets(t, mem, nil)

	r := run(t, "pw-1\n", "secret", "set", "n", "--stdin", "--for", "https://API.Dev.Example.com/graphql", "--for", "http://localhost:3000", "--for", "https://api.dev.example.com")
	if r.code != 0 || !strings.Contains(r.err, "bound to https://api.dev.example.com, http://localhost:3000") {
		t.Fatalf("normalised and de-duplicated: %+v", r)
	}
	if s, _ := mem.Get("n"); s.Value != "pw-1" || len(s.Origins) != 2 {
		t.Fatalf("%+v", s)
	}
	if r := run(t, "", "secret", "show", "n"); !strings.Contains(r.err, "bound to https://api.dev.example.com") || strings.Contains(r.out+r.err, "pw-1") {
		t.Fatalf("%+v", r)
	}
	// replacing the password keeps the binding
	if r := run(t, "pw-2\n", "secret", "set", "n", "--stdin"); r.code != 0 || !strings.Contains(r.err, "bound to") {
		t.Fatalf("%+v", r)
	}
	if s, _ := mem.Get("n"); s.Value != "pw-2" || len(s.Origins) != 2 {
		t.Fatalf("the binding must survive a password change: %+v", s)
	}
	// bind adds a server without re-entering the value
	if r := run(t, "", "secret", "bind", "n", "https://other.example"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if s, _ := mem.Get("n"); s.Value != "pw-2" || len(s.Origins) != 3 {
		t.Fatalf("%+v", s)
	}
	// --unbind is the explicit way to drop it
	if r := run(t, "pw-3\n", "secret", "set", "n", "--stdin", "--unbind"); r.code != 0 || !strings.Contains(r.err, "not bound") {
		t.Fatalf("%+v", r)
	}
	if s, _ := mem.Get("n"); len(s.Origins) != 0 {
		t.Fatalf("%+v", s)
	}
	// bad input
	for _, bad := range []string{"not-a-url", "ftp://x", "api.example.com"} {
		if r := run(t, "x\n", "secret", "set", "m", "--stdin", "--for", bad); r.code == 0 {
			t.Errorf("--for %q accepted", bad)
		}
		if r := run(t, "", "secret", "bind", "n", bad); r.code == 0 {
			t.Errorf("bind %q accepted", bad)
		}
	}
	if r := run(t, "", "secret", "bind", "missing", "https://x.example"); r.code != 1 || !strings.Contains(r.err, "api secret set missing") {
		t.Fatalf("%+v", r)
	}
}

// ── a login with no url of its own: the server comes from `api env load` ───

const urlLessLogin = "[env.dev.login]\ngql = \"mutation { login }\"\nendpoint = \"v2/auth\"\nvars = { user = \"me\", password = \"@keychain:upscale-dev\" }\n"

func TestUrlLessLoginUsesTheServerFromEnvLoad(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	boundSecret(t, "upscale-dev", "REAL-PASSWORD", real.srv.URL)
	writeConfig(t, "")
	projectWith(t, map[string]string{".api.toml": urlLessLogin})
	shellFromEnvLoad(t, real.srv.URL)

	// the flow: `api env load dev`, then `api login` (or --env dev). No url in the config.
	for _, args := range [][]string{{"login", "--emit"}, {"login", "--env", "dev", "--emit"}} {
		real.hits = 0
		r := run(t, "", args...)
		if r.code != 0 || real.hits != 1 || !strings.Contains(real.body, "REAL-PASSWORD") || real.path != "/graphql/v2/auth" {
			t.Fatalf("%v must link the login to the env-loaded server: %+v hits=%d path=%q", args, r, real.hits, real.path)
		}
		if !strings.Contains(r.out, "export API_TOKEN='NEW-TOKEN-123'") {
			t.Fatalf("%v: %q", args, r.out)
		}
	}
}

func TestUrlLessLoginCannotBeAimedByAClonedReposEnv(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	evil := newLoginSrv(t, loginOK)
	boundSecret(t, "upscale-dev", "REAL-PASSWORD", real.srv.URL)
	writeConfig(t, "")
	projectWith(t, map[string]string{".api.toml": urlLessLogin})

	// the cloned repo's .env.dev supplied the URL: the bound secret refuses it
	shellFromEnvLoad(t, evil.srv.URL)
	r := run(t, "", "login", "--emit")
	if evil.hits != 0 || strings.Contains(evil.body, "REAL-PASSWORD") {
		t.Fatalf("the password reached the server a .env named: %+v body=%q", r, evil.body)
	}
	if r.code == 0 || !strings.Contains(r.err, "is bound to "+origin(real.srv.URL)) || !strings.Contains(r.err, "will not be sent to "+origin(evil.srv.URL)) {
		t.Fatalf("%+v", r)
	}

	// an UNBOUND secret has no such protection, so a url-less login refuses it
	mem := secretstore.NewMemory()
	_ = mem.Set("upscale-dev", secretstore.Secret{Value: "UNBOUND"})
	withSecrets(t, mem, nil)
	shellFromEnvLoad(t, evil.srv.URL)
	if r := run(t, "", "login", "--emit"); r.code == 0 || evil.hits != 0 || !strings.Contains(r.err, "must be bound to it") {
		t.Fatalf("unbound: %+v hits=%d", r, evil.hits)
	}

	// @env: could read any variable: refused here even from YOUR global config
	t.Setenv("MY_PASSWORD", "env-pw")
	writeConfig(t, "[env.dev.login]\ngql = \"mutation { login }\"\nendpoint = \"v2/auth\"\nvars = { user = \"me\", password = \"@env:MY_PASSWORD\" }\n")
	projectWith(t, nil)
	if r := run(t, "", "login", "--emit"); r.code == 0 || evil.hits != 0 || !strings.Contains(r.err, "is not allowed here") {
		t.Fatalf("@env: %+v hits=%d", r, evil.hits)
	}
}

func TestUrlLessProfileContributesOnlyItsLogin(t *testing.T) {
	// a global env with a login but no url must NOT hand its headers or token_env
	// to whatever server the shell happens to point at
	srv, h := envServer(t)
	writeConfig(t, "[env.dev]\ntoken_env = \"API_TOKEN_DEV\"\nheaders = { \"X-Tenant\" = \"dev-tenant\" }\n"+urlLessLogin)
	t.Setenv("API_TOKEN_DEV", "DEV-ENV-TOKEN")
	shellFromEnvLoad(t, srv.URL)
	*h = hit{}
	if r := run(t, "", "get", "/x"); r.code != 0 || h.path != "/x" {
		t.Fatalf("setup: %+v", r)
	}
	if h.tenant != "" || h.auth != "" {
		t.Fatalf("a url-less profile leaked headers / token_env onto the shell's server: %+v", h)
	}
	*h = hit{}
	if r := run(t, "", "get", "/x", "--env", "dev"); r.code != 0 || h.tenant != "" || h.auth != "" {
		t.Fatalf("--env dev: %+v %+v", r, h)
	}
}

func TestUrlLessLoginWithNoServerSaysHowToGetOne(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	boundSecret(t, "upscale-dev", "PW", l.srv.URL)
	clearSession(t)
	writeConfig(t, "")
	projectWith(t, map[string]string{".api.toml": urlLessLogin})
	for _, args := range [][]string{{"login", "--env", "dev"}} {
		r := run(t, "", args...)
		if r.code == 0 || !strings.Contains(r.err, `no server for env "dev"`) || !strings.Contains(r.err, "api env load dev") {
			t.Fatalf("%v: %+v", args, r)
		}
		if strings.Contains(r.err, "unknown server") || l.hits != 0 {
			t.Fatalf("%q hits=%d", r.err, l.hits)
		}
	}
}
