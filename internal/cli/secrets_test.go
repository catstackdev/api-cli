package cli

import (
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"
)

// ── secrets: value references, api secret, config login ────────────────────

func TestValueReferencesKeepPasswordsOffTheCommandLine(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	mem := secretstore.NewMemory()
	_ = mem.Set("upscale-dev", secretstore.Secret{Value: "kc-PASSWORD"})
	var prompted []string
	withSecrets(t, mem, func(label string) (string, error) { prompted = append(prompted, label); return "typed-PASSWORD", nil })
	t.Setenv("API_BASE_URL", l.srv.URL)
	t.Setenv("API_GRAPHQL_URL", l.srv.URL+"/graphql")
	t.Setenv("UPSCALE_PASS", "env-PASSWORD")

	login := func(pw string, extra ...string) result {
		args := append([]string{"login", "--gql", "-e", "v2/auth", loginMutation, "-V", "input.username=me", "-V", "input.password=" + pw, "--emit"}, extra...)
		return run(t, "", args...)
	}
	for ref, want := range map[string]string{
		"@keychain:upscale-dev": "kc-PASSWORD",
		"@env:UPSCALE_PASS":     "env-PASSWORD",
		"@prompt":               "typed-PASSWORD",
		"@@literal":             "@literal",
		"plain":                 "plain",
	} {
		r := login(ref)
		if r.code != 0 || !strings.Contains(l.body, `"password":"`+want+`"`) {
			t.Fatalf("%s: code=%d body=%s err=%s", ref, r.code, l.body, r.err)
		}
		if strings.Contains(r.out+r.err, want) && want != "plain" && want != "@literal" {
			t.Fatalf("%s: the password leaked into the output:\n%s\n%s", ref, r.out, r.err)
		}
	}
	if len(prompted) != 1 || prompted[0] != "Secret" {
		t.Fatalf("@prompt asked %v", prompted)
	}
	if r := login("@prompt:Password for dev"); r.code != 0 || prompted[len(prompted)-1] != "Password for dev" {
		t.Fatalf("label: %+v %v", r, prompted)
	}

	// problems say what to do
	if r := login("@keychain:nope"); r.code == 0 || !strings.Contains(r.err, "api secret set nope") {
		t.Fatalf("%+v", r)
	}
	if r := login("@env:NOT_SET_ANYWHERE"); r.code == 0 || !strings.Contains(r.err, "is not set") {
		t.Fatalf("%+v", r)
	}

	// api json resolves them too, so the password can be piped
	if r := run(t, "", "json", "email=me", "password=@keychain:upscale-dev", "-c"); strings.TrimSpace(r.out) != `{"email":"me","password":"kc-PASSWORD"}` {
		t.Fatalf("%+v", r)
	}
	// ...but raw JSON (:=) is never interpreted
	if r := run(t, "", "json", `x:="@keychain:upscale-dev"`, "-c"); strings.TrimSpace(r.out) != `{"x":"@keychain:upscale-dev"}` {
		t.Fatalf("%+v", r)
	}
}

func TestDryRunNeverTouchesSecrets(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	t.Setenv("API_BASE_URL", l.srv.URL)
	t.Setenv("API_GRAPHQL_URL", l.srv.URL+"/graphql")
	touched := false
	withSecrets(t, failingStore{onGet: func() { touched = true }}, func(string) (string, error) { touched = true; return "", nil })

	for _, args := range [][]string{
		{"login", "--gql", "-e", "v2/auth", loginMutation, "-V", "input.password=@keychain:x", "--dry"},
		{"gql", "-e", "v2/auth", loginMutation, "-V", "input.password=@prompt", "--dry"},
	} {
		r := run(t, "", args...)
		if r.code != 0 || touched || l.hits != 0 {
			t.Fatalf("%v: a dry run read a secret, prompted or sent: touched=%v hits=%d %+v", args, touched, l.hits, r)
		}
		if !strings.Contains(r.out, "[REDACTED]") || strings.Contains(r.out, "hunter") {
			t.Fatalf("the password field should be shown as redacted: %q", r.out)
		}
	}
}

func TestLoginFromConfig(t *testing.T) {
	l := newLoginSrv(t, loginOK)
	mem := secretstore.NewMemory()
	_ = mem.Set("upscale-dev", secretstore.Secret{Value: "cfg-PASSWORD"})
	withSecrets(t, mem, nil)
	writeConfig(t, `
[env.dev]
url = "`+l.srv.URL+`"
graphql = "`+l.srv.URL+`/graphql"
token_env = "API_TOKEN_DEV"
[env.dev.login]
gql = "`+strings.ReplaceAll(loginMutation, `"`, `\"`)+`"
endpoint = "v2/auth"
vars = { "input.username" = "me@x.com", "input.password" = "@keychain:upscale-dev" }

[env.rest]
url = "`+l.srv.URL+`"
[env.rest.login]
path = "/auth/login"
token_path = ".data.token"
vars = { email = "me@x.com", password = "@keychain:upscale-dev" }
`)
	for _, k := range []string{"API_BASE_URL", "API_GRAPHQL_URL", "API_TOKEN", "API_ENV"} {
		t.Setenv(k, "")
	}
	t.Setenv("API_TOKEN", "STALE-TOKEN")

	// no arguments: the configured GraphQL login, no password typed
	r := run(t, "", "login", "--env", "dev", "--emit")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if l.path != "/graphql/v2/auth" || l.au != "" {
		t.Fatalf("path=%q auth=%q (a stale token must not be sent)", l.path, l.au)
	}
	if !strings.Contains(l.body, `"variables":{"input":{"password":"cfg-PASSWORD","username":"me@x.com"}}`) && !strings.Contains(l.body, `"username":"me@x.com"`) {
		t.Fatalf("body: %s", l.body)
	}
	if !strings.Contains(l.body, `"password":"cfg-PASSWORD"`) || strings.Contains(r.out+r.err, "cfg-PASSWORD") {
		t.Fatalf("password must reach the server and nothing else: %s / %s", l.body, r.out+r.err)
	}
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if len(lines) != 3 || lines[1] != "export API_TOKEN='NEW-TOKEN-123'" || lines[2] != "API_TOKEN_DEV='NEW-TOKEN-123'" {
		t.Fatalf("emitted:\n%s", r.out)
	}

	// -V overrides one variable of the configured login
	if r := run(t, "", "login", "--env", "dev", "-V", "input.username=other@x.com", "--emit"); r.code != 0 || !strings.Contains(l.body, `"username":"other@x.com"`) || !strings.Contains(l.body, "cfg-PASSWORD") {
		t.Fatalf("%+v body=%s", r, l.body)
	}

	// REST variant: vars are the JSON body, token_path is honoured
	r = run(t, "", "login", "--env", "rest", "--emit")
	if r.code != 0 || l.path != "/auth/login" || !strings.Contains(l.body, `"email":"me@x.com"`) || !strings.Contains(r.out, "API_TOKEN='REST-TOKEN'") {
		t.Fatalf("REST: %+v path=%q body=%s", r, l.path, l.body)
	}

	// the session env is used when --env is not given
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", l.srv.URL)
	if r := run(t, "", "login", "--emit"); r.code != 0 || !strings.Contains(r.out, "export API_TOKEN='NEW-TOKEN-123'") {
		t.Fatalf("session env: %+v", r)
	}

	// helpful errors, and nothing is sent
	hits := l.hits
	t.Setenv("API_ENV", "")
	t.Setenv("API_BASE_URL", "")
	if r := run(t, "", "login"); r.code == 0 || !strings.Contains(r.err, "no env selected") {
		t.Fatalf("%+v", r)
	}
	writeConfig(t, "[env.bare]\nurl = \""+l.srv.URL+"\"\n")
	if r := run(t, "", "login", "--env", "bare"); r.code == 0 || !strings.Contains(r.err, `no login configured for env "bare"`) {
		t.Fatalf("%+v", r)
	}
	if l.hits != hits {
		t.Fatal("a failed setup must not send a request")
	}
}

func TestProjectConfigCannotDefineOrMoveALogin(t *testing.T) {
	real := newLoginSrv(t, loginOK)
	evil := newLoginSrv(t, loginOK)
	mem := secretstore.NewMemory()
	_ = mem.Set("upscale-dev", secretstore.Secret{Value: "REAL-KEYCHAIN-PASSWORD"})
	withSecrets(t, mem, nil)
	for _, k := range []string{"API_BASE_URL", "API_GRAPHQL_URL", "API_TOKEN", "API_ENV"} {
		t.Setenv(k, "")
	}
	writeConfig(t, `
[env.dev]
url = "`+real.srv.URL+`"
[env.dev.login]
path = "/auth"
vars = { user = "me", password = "@keychain:upscale-dev" }
`)

	// 1. a project defines a login for a NEW env pointing at its own server
	projectWith(t, map[string]string{".api.toml": "[env.evil]\nurl = \"" + evil.srv.URL + "\"\n[env.evil.login]\npath = \"/steal\"\nvars = { password = \"@keychain:upscale-dev\" }\n"})
	r := run(t, "", "login", "--env", "evil")
	if r.code == 0 || evil.hits != 0 || !strings.Contains(r.err, "must be bound to it") {
		t.Fatalf("a project login with an UNBOUND secret must be refused before anything is sent: %+v hits=%d", r, evil.hits)
	}

	// 2. a project moves the URL of the global env that HAS a login
	projectWith(t, map[string]string{".api.toml": "[env.dev]\nurl = \"" + evil.srv.URL + "\"\n"})
	r = run(t, "", "login", "--env", "dev")
	if evil.hits != 0 || strings.Contains(evil.body, "REAL-KEYCHAIN-PASSWORD") {
		t.Fatalf("the Keychain password reached the project's server: %+v body=%q", r, evil.body)
	}
	if r.code == 0 || !strings.Contains(r.err, "login are dropped") {
		t.Fatalf("expected the login to be dropped with a warning: %+v", r)
	}

	// 3. a project overriding the same env's login fields
	projectWith(t, map[string]string{".api.toml": "[env.dev.login]\npath = \"/steal\"\nvars = { user = \"x\", password = \"@keychain:upscale-dev\" }\n"})
	real.hits = 0
	r = run(t, "", "login", "--env", "dev", "--emit")
	if r.code != 0 || real.path != "/auth" || real.hits != 1 {
		t.Fatalf("the GLOBAL login must be used unchanged: %+v path=%q", r, real.path)
	}
	if !strings.Contains(r.err, "login ignored") {
		t.Fatalf("warn about the ignored project login: %q", r.err)
	}
}

func TestConfigRejectsLiteralPasswords(t *testing.T) {
	// hand-written
	writeConfig(t, "[env.dev]\nurl = \"https://x\"\n[env.dev.login]\npath = \"/a\"\nvars = { password = \"hunter2\" }\n")
	r := run(t, "", "config", "show")
	if !strings.Contains(r.err, "config ignored") || !strings.Contains(r.err, "literal") || strings.Contains(r.err, "hunter2") {
		t.Fatalf("a literal password must be rejected without echoing it: %q", r.err)
	}
	// via config set
	writeConfig(t, "")
	for _, k := range []string{"env.dev.login.vars.password", "env.dev.login.vars.input.password", "env.dev.login.vars.api_key"} {
		if r := run(t, "", "config", "set", k, "hunter2", "--global"); r.code == 0 || strings.Contains(r.err, "hunter2") {
			t.Fatalf("%s accepted a literal secret: %+v", k, r)
		}
		if r := run(t, "", "config", "set", k, "@keychain:upscale-dev", "--global"); r.code != 0 {
			t.Fatalf("%s must accept a reference: %+v", k, r)
		}
	}
	// a non-secret variable may be a literal
	if r := run(t, "", "config", "set", "env.dev.login.vars.input.username", "me@x.com", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, k := range []string{"path", "gql", "endpoint", "token_path"} {
		v := "x"
		if k == "token_path" {
			v = ".data.t"
		}
		if r := run(t, "", "config", "set", "env.dev.login."+k, v, "--global"); r.code != 0 {
			t.Fatalf("%s: %+v", k, r)
		}
	}
	// path AND gql together is caught when you log in (not when reading the file,
	// so a half-built block never makes the rest of the config unusable)
	if r := run(t, "", "config", "show"); strings.Contains(r.err, "ignored") {
		t.Fatalf("an unfinished/odd login must not poison the config: %+v", r)
	}
	if r := run(t, "", "login", "--env", "dev"); r.code == 0 || !strings.Contains(r.err, "exactly one of path") {
		t.Fatalf("path + gql must be rejected at login: %+v", r)
	}
	if r := run(t, "", "config", "set", "env.dev.login.token_path", "data.t", "--global"); r.code == 0 {
		t.Fatal("token_path without a leading dot accepted")
	}
}

func TestSecretCommands(t *testing.T) {
	mem := secretstore.NewMemory()
	withSecrets(t, mem, func(label string) (string, error) { return "prompted-SECRET-1", nil })

	// hidden prompt
	r := run(t, "", "secret", "set", "upscale-dev")
	if r.code != 0 || !strings.Contains(r.err, "stored") || strings.Contains(r.out+r.err, "prompted-SECRET-1") {
		t.Fatalf("%+v", r)
	}
	if v, _ := mem.Get("upscale-dev"); v.Value != "prompted-SECRET-1" {
		t.Fatalf("stored %q", v)
	}
	// stdin, for scripts
	if r := run(t, "from-stdin\n", "secret", "set", "other", "--stdin"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if v, _ := mem.Get("other"); v.Value != "from-stdin" {
		t.Fatalf("stdin value %q", v)
	}
	// show: masked by default, value only with --reveal
	if r := run(t, "", "secret", "show", "other"); r.code != 0 || strings.Contains(r.out+r.err, "from-stdin") || !strings.Contains(r.err, "10 characters") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "secret", "show", "other", "--reveal"); strings.TrimSpace(r.out) != "from-stdin" {
		t.Fatalf("%+v", r)
	}
	// rm
	if r := run(t, "", "secret", "rm", "other"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "secret", "show", "other"); r.code != 1 || !strings.Contains(r.err, "api secret set other") {
		t.Fatalf("%+v", r)
	}
	// bad names and empty values
	for _, bad := range []string{"a b", "../x", "$(x)", "-n"} {
		if r := run(t, "", "secret", "set", bad, "--stdin"); r.code == 0 {
			t.Errorf("name %q accepted", bad)
		}
	}
	if r := run(t, "\n", "secret", "set", "empty", "--stdin"); r.code == 0 {
		t.Fatalf("empty secret stored: %+v", r)
	}
	// a system with no store says so plainly
	withSecrets(t, failingStore{onGet: func() {}}, nil)
	if r := run(t, "x\n", "secret", "set", "n", "--stdin"); r.code != 1 || !strings.Contains(r.err, "no credential store") {
		t.Fatalf("%+v", r)
	}
}

func TestDoctorReportsTheCredentialStore(t *testing.T) {
	withSecrets(t, secretstore.NewMemory(), nil)
	if r := run(t, "", "doctor"); !strings.Contains(r.out, "credentials") || !strings.Contains(r.out, "credential store available") {
		t.Fatalf("%s", r.out)
	}
	withSecrets(t, unavailableStore{}, nil)
	if r := run(t, "", "doctor"); !strings.Contains(r.out, "no credential store on this system") {
		t.Fatalf("%s", r.out)
	}
}
