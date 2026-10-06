package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── config: envs, defaults, aliases ────────────────────────────────────────

func TestEnvFlagBeatsSessionAndNeverLeaksTheSessionToken(t *testing.T) {
	dev, devHit := envServer(t)
	thien, thienHit := envServer(t)
	writeConfig(t, `
[env.dev]
url = "`+dev.URL+`"
[env.thien]
url = "`+thien.URL+`"
token_env = "API_TOKEN_THIEN"
headers = { "X-Tenant" = "thien-tenant" }
`)
	// the shell is logged in to dev
	t.Setenv("API_BASE_URL", dev.URL)
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_TOKEN", "DEV-SECRET")
	t.Setenv("API_TOKEN_THIEN", "")
	t.Setenv("API_GRAPHQL_URL", "")

	// no flag: the session wins
	if r := run(t, "", "get", "/a"); r.code != 0 || devHit.auth != "Bearer DEV-SECRET" || devHit.path != "/a" {
		t.Fatalf("session: %+v %+v", r, devHit)
	}
	// --env thien: goes to thien, and the dev token is NOT sent there
	if r := run(t, "", "--env", "thien", "get", "/b"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if thienHit.path != "/b" || thienHit.auth != "" || thienHit.tenant != "thien-tenant" {
		t.Fatalf("thien hit: %+v (dev token leaked or profile header missing)", thienHit)
	}
	// ...unless the profile names a token variable
	t.Setenv("API_TOKEN_THIEN", "THIEN-TOKEN")
	if r := run(t, "", "get", "/c", "--env", "thien"); r.code != 0 || thienHit.auth != "Bearer THIEN-TOKEN" {
		t.Fatalf("token_env: %+v %+v", r, thienHit)
	}
	// --env of the session's own env keeps the session token
	if r := run(t, "", "--env", "dev", "get", "/d"); r.code != 0 || devHit.auth != "Bearer DEV-SECRET" {
		t.Fatalf("same env: %+v %+v", r, devHit)
	}
	// --base beats --env; -H beats the profile header
	if r := run(t, "", "--env", "thien", "--base", dev.URL, "get", "/e", "-H", "X-Tenant: mine"); r.code != 0 || devHit.path != "/e" || devHit.tenant != "mine" {
		t.Fatalf("--base/-H: %+v %+v", r, devHit)
	}
	// unknown env: usage error listing what exists
	if r := run(t, "", "--env", "nope", "get", "/x"); r.code != 2 || !strings.Contains(r.err, `unknown env "nope" (known: dev, thien)`) {
		t.Fatalf("unknown env: %+v", r)
	}
}

func TestDefaultEnvOnlyWhenNoSessionAndNeverUsesTheSessionToken(t *testing.T) {
	dev, devHit := envServer(t)
	writeConfig(t, "default_env = \"dev\"\n[env.dev]\nurl = \""+dev.URL+"\"\n")
	t.Setenv("API_BASE_URL", "")
	t.Setenv("API_ENV", "")
	t.Setenv("API_GRAPHQL_URL", "")
	t.Setenv("API_TOKEN", "UNKNOWN-ORIGIN")
	t.Setenv("API_TOKEN_DEV", "")

	if r := run(t, "", "get", "/x"); r.code != 0 || devHit.path != "/x" {
		t.Fatalf("default_env not used: %+v", r)
	}
	if devHit.auth != "" {
		t.Fatalf("a session token of unknown origin was sent to a config-chosen server: %q", devHit.auth)
	}
}

func TestConfigDefaultsAliasesAndPrecedence(t *testing.T) {
	writeConfig(t, `
[defaults]
timeout = "30s"
connect_timeout = "3"
retry = 2
[alias]
me = "get /v2/me '.data'"
ping = "get /ping"
get = "get /hijacked"
`)
	t.Setenv("API_BASE_URL", "https://api.test")
	t.Setenv("API_TOKEN", "")
	t.Setenv("CURL_MAX_TIME", "")
	t.Setenv("CURL_CONNECT_TIMEOUT", "")

	r := run(t, "", "get", "/x", "--dry")
	if !strings.Contains(r.out, "--connect-timeout 3 --max-time 30") {
		t.Fatalf("config defaults not applied: %s", r.out)
	}
	// env var beats config; a flag beats both
	t.Setenv("CURL_MAX_TIME", "7")
	if r := run(t, "", "get", "/x", "--dry"); !strings.Contains(r.out, "--max-time 7") {
		t.Fatalf("env var must beat config: %s", r.out)
	}
	if r := run(t, "", "get", "/x", "--dry", "--timeout", "9s"); !strings.Contains(r.out, "--max-time 9") {
		t.Fatalf("flag must beat env: %s", r.out)
	}
	// aliases expand and take extra args; a real command is never shadowed
	if r := run(t, "", "ping", "--dry"); r.code != 0 || !strings.Contains(r.out, "https://api.test/ping") {
		t.Fatalf("alias: %+v", r)
	}
	if r := run(t, "", "me", "--dry"); !strings.Contains(r.out, "https://api.test/v2/me") {
		t.Fatalf("alias with quotes: %+v", r)
	}
	if r := run(t, "", "get", "/real", "--dry"); !strings.Contains(r.out, "https://api.test/real") || strings.Contains(r.out, "hijacked") {
		t.Fatalf("alias shadowed a real command: %+v", r)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords(`gql -e v2/user 'query { me { id } }' ".data.me" plain  "two words"`)
	want := []string{"gql", "-e", "v2/user", "query { me { id } }", ".data.me", "plain", "two words"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestBadOrDangerousConfigIsIgnoredWithAWarning(t *testing.T) {
	srv, h := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")

	// a secret key is rejected, the value is never echoed, commands still work
	writeConfig(t, "[env.dev]\nurl = \"https://x\"\ntoken = \"hunter2\"\n")
	r := run(t, "", "get", "/ok")
	if r.code != 0 || h.path != "/ok" || !strings.Contains(r.err, "config ignored") || strings.Contains(r.err, "hunter2") {
		t.Fatalf("%+v", r)
	}
	// token_env may only name API_TOKEN_*: a cloned repo must not be able to
	// point it at an unrelated secret
	writeConfig(t, "[env.evil]\nurl = \"https://evil.example\"\ntoken_env = \"AWS_SECRET_ACCESS_KEY\"\n")
	r = run(t, "", "--env", "evil", "get", "/x", "--dry")
	if !strings.Contains(r.err, "config ignored") || !strings.Contains(r.err, "API_TOKEN_") {
		t.Fatalf("token_env guard: %+v", r)
	}
}

func TestConfigCommands(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "g", "config.toml")
	t.Setenv("API_CONFIG", global)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	proj := filepath.Join(dir, "proj")
	_ = os.MkdirAll(proj, 0o700)
	_ = os.Chdir(proj)

	// no local file yet: set goes to the global file
	if r := run(t, "", "config", "set", "env.dev.url", "https://dev.example"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(global); !strings.Contains(string(b), "https://dev.example") {
		t.Fatalf("global file: %s", b)
	}
	if r := run(t, "", "config", "get", "env.dev.url"); strings.TrimSpace(r.out) != "https://dev.example" {
		t.Fatalf("get: %+v", r)
	}
	if r := run(t, "", "config", "get", "env.dev.nope"); r.code != 1 || !strings.Contains(r.err, "not set") {
		t.Fatalf("get missing: %+v", r)
	}
	// secrets and bad values are refused
	for _, args := range [][]string{
		{"config", "set", "env.dev.token", "x"},
		{"config", "set", "env.dev.token_env", "AWS_SECRET"},
		{"config", "set", "defaults.retry", "many"},
		{"config", "set", "nonsense", "x"},
	} {
		if r := run(t, "", args...); r.code == 0 {
			t.Fatalf("%v accepted: %+v", args, r)
		}
	}
	// init creates a project file; --force is needed to overwrite; set then prefers it
	if r := run(t, "", "config", "init", "--local"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	if r := run(t, "", "config", "init", "--local"); r.code != 1 || !strings.Contains(r.err, "already exists") {
		t.Fatalf("init twice: %+v", r)
	}
	if r := run(t, "", "config", "set", "env.local.url", "https://local.example"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".api.toml")); !strings.Contains(string(b), "https://local.example") {
		t.Fatalf("local file not used by set: %s", b)
	}
	// merged view shows both
	r := run(t, "", "config", "show")
	if !strings.Contains(r.out, "dev") || !strings.Contains(r.out, "https://local.example") {
		t.Fatalf("show: %s", r.out)
	}
	if r := run(t, "", "config", "path"); !strings.Contains(r.out, "global") || !strings.Contains(r.out, ".api.toml") {
		t.Fatalf("path: %s", r.out)
	}
	if st, _ := os.Stat(global); st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode %v", st.Mode())
	}
}

func TestEnvUseEmitsUnexportedAssignments(t *testing.T) {
	writeConfig(t, `
[env.thien]
url = "https://thien.example"
graphql = "https://thien.example/graphql"
token_env = "API_TOKEN_THIEN"
[env.plain]
url = "https://plain.example"
`)
	r := run(t, "", "env", "use", "thien", "--emit")
	want := `API_BASE_URL='https://thien.example'; API_GRAPHQL_URL='https://thien.example/graphql'; API_ENV='thien'; API_TOKEN="$API_TOKEN_THIEN"`
	if r.code != 0 || strings.TrimSpace(r.out) != want {
		t.Fatalf("\n got: %q\nwant: %q", strings.TrimSpace(r.out), want)
	}
	if strings.Contains(r.out, "export") {
		t.Fatal("session variables must stay unexported")
	}
	// no token_env: the previous token is dropped
	if r := run(t, "", "env", "use", "plain", "--emit"); !strings.HasSuffix(strings.TrimSpace(r.out), "API_TOKEN=''") {
		t.Fatalf("token must be cleared: %q", r.out)
	}
	if r := run(t, "", "env", "use", "thien"); r.code != 2 {
		t.Fatalf("without --emit: %+v", r)
	}
	if r := run(t, "", "env", "use", "nope", "--emit"); r.code != 1 || !strings.Contains(r.err, "known: plain, thien") {
		t.Fatalf("unknown: %+v", r)
	}
}
