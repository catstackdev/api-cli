package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── config init pre-fills [vars]; unknown-key errors are honest ────────────

func TestConfigInitLocalDetectsVars(t *testing.T) {
	dir := projectWith(t, map[string]string{
		".env.thien": "APP_DOMAIN_NAME=x\nGRAPHQL_URL=a\nVITE_GRAPHQL_URL=b\nREACT_APP_API_URL=c\n",
		".env.dev":   "VITE_GRAPHQL_URL=b\n",
	})
	t.Setenv("API_CONFIG", filepath.Join(dir, "global", "config.toml"))
	r := run(t, "", "config", "init", "--local")
	if r.code != 0 || !strings.Contains(r.err, "graphql_url = VITE_GRAPHQL_URL") || !strings.Contains(r.err, "base_url = REACT_APP_API_URL") {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".api.toml"))
	if !strings.Contains(string(data), "\n[vars]\n") || !strings.Contains(string(data), `graphql_url = "VITE_GRAPHQL_URL"   # detected`) || !strings.Contains(string(data), `base_url = "REACT_APP_API_URL"   # detected`) {
		t.Fatalf("file:\n%s", data)
	}
	// the generated file parses and is what env load then uses
	r = run(t, "", "config", "get", "vars.graphql_url")
	if r.code != 0 || strings.TrimSpace(r.out) != "VITE_GRAPHQL_URL" || strings.Contains(r.err, "ignored") {
		t.Fatalf("generated file not valid: %+v", r)
	}
	// the user's own secret-free values for everything else are still the commented template
	if !strings.Contains(string(data), "# [env.local]") {
		t.Fatalf("rest of the template missing:\n%s", data)
	}
}

func TestConfigInitAlwaysListsEveryUseCase(t *testing.T) {
	// only the GraphQL URL is detected: the other settings must still be listed
	dir := projectWith(t, map[string]string{".env.thien": "VITE_GRAPHQL_URL=b\n"})
	global := filepath.Join(dir, "global", "config.toml")
	t.Setenv("API_CONFIG", global)
	if r := run(t, "", "config", "init", "--local"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".api.toml"))
	proj := string(data)
	for _, want := range []string{
		`graphql_url = "VITE_GRAPHQL_URL"   # detected`, // active
		`# base_url = "REACT_APP_API_URL"`,              // the undetected sibling stays as an example
		`# env_suffix = "dev"`,
		"[defaults]", "# timeout", "# connect_timeout", "# retry", "# color", "# summary",
		"# [env.local]", "# url =", "# graphql =", "# endpoints =", "# headers =",
		"# [env.local.login]", "# gql =", "# token_path =", "# vars =", "api secret set upscale-local --for http://localhost:3000", "No url is needed for the login",
		"# [alias]", "# me =",
	} {
		if !strings.Contains(proj, want) {
			t.Errorf("the PROJECT file lacks %q:\n%s", want, proj)
		}
	}
	if strings.Contains(proj, "# token_env =") || strings.Contains(proj, "# default_env") {
		t.Errorf("the project file must not suggest token_env / default_env (global-only):\n%s", proj)
	}
	// and it is still valid TOML that only activates what was detected
	if r := run(t, "", "config", "get", "vars.graphql_url"); strings.TrimSpace(r.out) != "VITE_GRAPHQL_URL" || strings.Contains(r.err, "ignored") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "get", "vars.base_url"); r.code != 1 {
		t.Fatalf("base_url must stay unset (commented): %+v", r)
	}

	// the GLOBAL default is a different file: shared envs, token_env, aliases, no [vars]
	if r := run(t, "", "config", "init", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	gdata, _ := os.ReadFile(global)
	g := string(gdata)
	for _, want := range []string{"# default_env", "[defaults]", "# [env.dev]", "# token_env =", "# headers =", "# [alias]", "init --local"} {
		if !strings.Contains(g, want) {
			t.Errorf("the GLOBAL file lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "[vars]") || strings.Contains(g, "env_suffix") {
		t.Errorf("per-project settings do not belong in the global file:\n%s", g)
	}
}

func TestConfigInitWithoutEnvFilesKeepsTheCommentedTemplate(t *testing.T) {
	dir := projectWith(t, map[string]string{".env.x": "FOO=1\n"})
	t.Setenv("API_CONFIG", filepath.Join(dir, "global", "config.toml"))
	if r := run(t, "", "config", "init", "--local"); r.code != 0 || strings.Contains(r.err, "detected") {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".api.toml"))
	if !strings.Contains(string(data), "\n[vars]\n") || !strings.Contains(string(data), "# graphql_url =") || strings.Contains(string(data), "\ngraphql_url") {
		t.Fatalf("nothing detected: the [vars] header is active but every key stays a commented example:\n%s", data)
	}
	// global init never guesses from the cwd
	dir2 := projectWith(t, map[string]string{".env.x": "VITE_GRAPHQL_URL=1\n"})
	g := filepath.Join(dir2, "g", "config.toml")
	t.Setenv("API_CONFIG", g)
	if r := run(t, "", "config", "init", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(g); strings.Contains(string(b), "\n[vars]") {
		t.Fatalf("the global file must not be filled from one project:\n%s", b)
	}
}

func TestUnknownKeyErrorIsHonest(t *testing.T) {
	writeConfig(t, "[futuresection]\nanything = 1\n")
	r := run(t, "", "config", "show")
	if !strings.Contains(r.err, "not a setting this version knows") || !strings.Contains(r.err, "make install") || strings.Contains(r.err, "secrets are not allowed") {
		t.Fatalf("a harmless unknown key must not be called a secret: %q", r.err)
	}
	writeConfig(t, "[env.dev]\npassword = \"hunter2\"\n")
	r = run(t, "", "config", "show")
	if !strings.Contains(r.err, "secrets are not allowed") || strings.Contains(r.err, "hunter2") {
		t.Fatalf("%q", r.err)
	}
}

func TestConfigInitExistsMessagePointsAtTheOtherFile(t *testing.T) {
	dir := projectWith(t, map[string]string{".api.toml": "default_env = \"a\"\n"})
	global := filepath.Join(dir, "g", "config.toml")
	t.Setenv("API_CONFIG", global)

	// project file exists: plain init targets it and says where the global one is
	r := run(t, "", "config", "init")
	if r.code != 1 || !strings.Contains(r.err, "already exists") || !strings.Contains(r.err, "--global") || !strings.Contains(r.err, global) || !strings.Contains(r.err, "does not exist yet") {
		t.Fatalf("%+v", r)
	}
	// and --global really does create it
	if r := run(t, "", "config", "init", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	// now global exists too: the hint for the global target points at the project file
	r = run(t, "", "config", "init", "--global")
	if r.code != 1 || !strings.Contains(r.err, "--local") || !strings.Contains(r.err, ".api.toml") || !strings.Contains(r.err, "(exists)") {
		t.Fatalf("%+v", r)
	}
	// nothing was overwritten
	if b, _ := os.ReadFile(filepath.Join(dir, ".api.toml")); string(b) != "default_env = \"a\"\n" {
		t.Fatalf("the project file changed: %s", b)
	}
}

// The point of the in-place editor: `config set` on a freshly created file must
// keep the whole template, not replace it with a bare file.
func TestConfigSetKeepsTheTemplateFromInit(t *testing.T) {
	dir := projectWith(t, map[string]string{".env.thien": "VITE_GRAPHQL_URL=b\n"})
	global := filepath.Join(dir, "g", "config.toml")
	t.Setenv("API_CONFIG", global)
	for _, scope := range []string{"--local", "--global"} {
		if r := run(t, "", "config", "init", scope); r.code != 0 {
			t.Fatalf("%+v", r)
		}
	}
	path := filepath.Join(dir, ".api.toml")
	before, _ := os.ReadFile(path)
	for _, kv := range [][]string{
		{"vars.env_suffix", "thien"},
		{"env.local.url", "http://localhost:3000"},
		{"env.local.login.path", "/auth"},
		{"env.local.login.vars.user", "me"},
		{"defaults.timeout", "30s"},
	} {
		r := run(t, "", "config", "set", kv[0], kv[1], "--local")
		if r.code != 0 || strings.Contains(r.err, "comments were not kept") {
			t.Fatalf("%v: %+v", kv, r)
		}
	}
	after, _ := os.ReadFile(path)
	// every comment line of the generated file survives (except the examples that were turned into real settings)
	turned := map[string]bool{`# env_suffix = "dev"        # the .env.<suffix> that 'api env load' uses with no name`: true, `# timeout = "300s"`: true}
	for _, l := range strings.Split(string(before), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") && !turned[l] && !strings.Contains(string(after), l) {
			t.Fatalf("config set dropped the comment %q:\n%s", l, after)
		}
	}
	if !strings.Contains(string(after), `env_suffix = "thien"`) || !strings.Contains(string(after), `timeout = "30s"`) || !strings.Contains(string(after), "[env.local]") {
		t.Fatalf("settings missing:\n%s", after)
	}
	// and the same for the global file
	gbefore, _ := os.ReadFile(global)
	if r := run(t, "", "config", "set", "defaults.retry", "2", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	gafter, _ := os.ReadFile(global)
	if len(gafter) < len(gbefore)-20 || !strings.Contains(string(gafter), "# [env.dev.login]") || !strings.Contains(string(gafter), "retry = 2") {
		t.Fatalf("the global template was damaged:\n%s", gafter)
	}
}
