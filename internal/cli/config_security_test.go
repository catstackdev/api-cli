package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// ── a project .api.toml must never route your tokens to its own server ─────

// evilSetup writes a GLOBAL config (the real dev, with a token_env) and a
// PROJECT config in the current directory, and returns the "evil" server's log.
func evilSetup(t *testing.T, realURL, evilURL, projectToml string) *hit {
	t.Helper()
	writeConfig(t, `
[env.dev]
url = "`+realURL+`"
token_env = "API_TOKEN_DEV"
[env.thien]
url = "`+realURL+`"
token_env = "API_TOKEN_THIEN"
`)
	projectWith(t, map[string]string{".api.toml": strings.ReplaceAll(projectToml, "EVIL", evilURL)})
	return nil
}

func TestProjectConfigCannotRouteTokensToItsOwnServer(t *testing.T) {
	real, realHit := envServer(t)
	evil, evilHit := envServer(t)
	t.Setenv("API_TOKEN_DEV", "GLOBAL-DEV-TOKEN")
	t.Setenv("API_TOKEN_THIEN", "GLOBAL-THIEN-TOKEN")
	t.Setenv("API_GRAPHQL_URL", "")

	noLeak := func(t *testing.T, what string) {
		t.Helper()
		if evilHit.path != "" && evilHit.auth != "" {
			t.Fatalf("%s: a token reached the project's server: %q", what, evilHit.auth)
		}
	}

	// Vector A: the project moves the url of a GLOBAL env that has a token_env
	t.Run("A: project overrides url of a global env", func(t *testing.T) {
		evilSetup(t, real.URL, evil.URL, "[env.dev]\nurl = \"EVIL\"\n")
		t.Setenv("API_BASE_URL", "")
		t.Setenv("API_ENV", "")
		t.Setenv("API_TOKEN", "")
		*evilHit = hit{}
		r := run(t, "", "--env", "dev", "get", "/x")
		if r.code != 0 || evilHit.path != "/x" {
			t.Fatalf("setup: the request should have gone to the project's url: %+v", r)
		}
		noLeak(t, "--env dev")
		if !strings.Contains(r.err, "token_env and login are dropped") {
			t.Fatalf("the user must be warned: %q", r.err)
		}
		if r := run(t, "", "env", "use", "dev", "--emit"); !strings.HasSuffix(strings.TrimSpace(r.out), "API_TOKEN=''") {
			t.Fatalf("env use must not expand the token variable for a repointed env: %q", r.out)
		}
		*evilHit = hit{}
		run(t, "", "diff", "--envs", "dev,thien", "/x")
		noLeak(t, "diff")
		// graphql override is the same attack
		evilSetup(t, real.URL, evil.URL, "[env.dev]\ngraphql = \"EVIL/graphql\"\n")
		*evilHit = hit{}
		run(t, "", "--env", "dev", "gql", "{ a }")
		noLeak(t, "graphql override")
	})

	// Vector B: after `upscale dev` (API_ENV=dev) a project repoints dev; the LIVE session token must not follow
	t.Run("B: --env with the session's own name", func(t *testing.T) {
		writeConfig(t, "[env.dev]\nurl = \""+real.URL+"\"\n")
		projectWith(t, map[string]string{".api.toml": "[env.dev]\nurl = \"" + evil.URL + "\"\n"})
		t.Setenv("API_BASE_URL", real.URL)
		t.Setenv("API_ENV", "dev")
		t.Setenv("API_TOKEN", "LIVE-SESSION-TOKEN")
		*evilHit = hit{}
		r := run(t, "", "--env", "dev", "get", "/x")
		if r.code != 0 || evilHit.path != "/x" {
			t.Fatalf("setup: %+v", r)
		}
		noLeak(t, "--env dev with a live session")
		// and the legitimate case still works: same name AND same origin keeps the token
		projectWith(t, nil)
		*realHit = hit{}
		if r := run(t, "", "--env", "dev", "get", "/y"); r.code != 0 || realHit.auth != "Bearer LIVE-SESSION-TOKEN" {
			t.Fatalf("same env, same origin must keep the session token: %+v %q", r, realHit.auth)
		}
	})

	// Vector C: a project env naming YOUR per-env token variable, selected by default_env
	t.Run("C: project token_env / default_env", func(t *testing.T) {
		writeConfig(t, "[env.dev]\nurl = \""+real.URL+"\"\ntoken_env = \"API_TOKEN_DEV\"\n")
		projectWith(t, map[string]string{".api.toml": "default_env = \"x\"\n[env.x]\nurl = \"" + evil.URL + "\"\ntoken_env = \"API_TOKEN_DEV\"\n"})
		t.Setenv("API_BASE_URL", "")
		t.Setenv("API_ENV", "")
		t.Setenv("API_TOKEN", "")
		*evilHit = hit{}
		r := run(t, "", "get", "/x") // default_env = x, no session
		if r.code != 0 || evilHit.path != "/x" {
			t.Fatalf("setup: %+v", r)
		}
		noLeak(t, "default_env")
		if !strings.Contains(r.err, "token_env ignored") {
			t.Fatalf("warning missing: %q", r.err)
		}
		*evilHit = hit{}
		run(t, "", "--env", "x", "get", "/x")
		noLeak(t, "--env x")
	})

	// the legitimate project override still works: it may add envs and tune everything but url/token_env
	t.Run("legit project overrides", func(t *testing.T) {
		writeConfig(t, "[env.dev]\nurl = \""+real.URL+"\"\ntoken_env = \"API_TOKEN_DEV\"\n")
		projectWith(t, map[string]string{".api.toml": "[env.dev]\nendpoints = [\"user\"]\nheaders = { \"X-Tenant\" = \"t\" }\n"})
		t.Setenv("API_BASE_URL", "")
		t.Setenv("API_ENV", "")
		t.Setenv("API_TOKEN", "")
		*realHit = hit{}
		r := run(t, "", "--env", "dev", "get", "/z")
		if r.code != 0 || realHit.auth != "Bearer GLOBAL-DEV-TOKEN" || realHit.tenant != "t" || strings.Contains(r.err, "⚠") {
			t.Fatalf("a harmless project override must keep working: %+v %+v", r, realHit)
		}
	})
}

func TestConfigSetRefusesTokenEnvInAProjectFile(t *testing.T) {
	dir := projectWith(t, map[string]string{".api.toml": "default_env = \"a\"\n"})
	t.Setenv("API_CONFIG", filepath.Join(dir, "global", "config.toml"))
	if r := run(t, "", "config", "set", "env.a.token_env", "API_TOKEN_A"); r.code == 0 || !strings.Contains(r.err, "--global") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "set", "env.a.token_env", "API_TOKEN_A", "--global"); r.code != 0 {
		t.Fatalf("--global must work: %+v", r)
	}
}

func TestLoginUnderEnvSwitchesTheWholeSession(t *testing.T) {
	devSrv, _ := envServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"token":"THIEN-NEW"}}`))
	}))
	t.Cleanup(srv.Close)
	writeConfig(t, "[env.dev]\nurl = \""+devSrv.URL+"\"\n[env.thien]\nurl = \""+srv.URL+"\"\ntoken_env = \"API_TOKEN_THIEN\"\n")
	t.Setenv("API_BASE_URL", devSrv.URL) // the shell is on dev
	t.Setenv("API_ENV", "dev")

	r := run(t, "", "--env", "thien", "login", "/auth", `{"u":1}`, "--emit")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if len(lines) != 3 ||
		lines[0] != "API_BASE_URL='"+srv.URL+"'; API_GRAPHQL_URL=''; API_ENV='thien'" ||
		lines[1] != "export API_TOKEN='THIEN-NEW'" ||
		lines[2] != "API_TOKEN_THIEN='THIEN-NEW'" {
		t.Fatalf("login --env must switch the session and set the per-env token:\n%s", r.out)
	}
	// without --env the session is untouched, but the per-env variable is still set when the env has one
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_ENV", "thien")
	r = run(t, "", "login", "/auth", `{"u":1}`, "--emit")
	if got := strings.TrimSpace(r.out); got != "export API_TOKEN='THIEN-NEW'\nAPI_TOKEN_THIEN='THIEN-NEW'" {
		t.Fatalf("plain login: %q", got)
	}
}
