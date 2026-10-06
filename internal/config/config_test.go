package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (global, projectDir string) {
	t.Helper()
	root := t.TempDir()
	global = filepath.Join(root, "global", "config.toml")
	t.Setenv("API_CONFIG", global)
	projectDir = filepath.Join(root, "proj", "sub", "deep")
	_ = os.MkdirAll(projectDir, 0o700)
	return global, projectDir
}

func TestMissingFilesAreAnEmptyConfig(t *testing.T) {
	_, dir := setup(t)
	l, err := Load(dir)
	if err != nil || l.GlobalPath != "" || l.LocalPath != "" || len(l.Env) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestGlobalAndLocalMergeKeyByKey(t *testing.T) {
	global, dir := setup(t)
	write(t, global, `
default_env = "dev"
[defaults]
timeout = "60s"
color = "never"
[env.dev]
url = "https://dev.global"
headers = { "X-A" = "1", "X-B" = "2" }
[env.thien]
url = "https://thien.global"
[alias]
me = "get /me"
`)
	// the local file lives in a PARENT of the working directory
	write(t, filepath.Join(filepath.Dir(filepath.Dir(dir)), ".api.toml"), `
[defaults]
timeout = "5s"
retry = 2
[env.dev]
graphql = "https://dev.local/graphql"
headers = { "X-B" = "local" }
[alias]
me = "get /v2/me"
`)
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.GlobalPath == "" || l.LocalPath == "" {
		t.Fatalf("paths: %+v", l)
	}
	d, _ := Duration(l.Defaults.Timeout)
	if d != 5*time.Second || l.Defaults.Color != "never" || l.Defaults.Retry == nil || *l.Defaults.Retry != 2 {
		t.Fatalf("defaults: %+v", l.Defaults)
	}
	dev := l.Env["dev"]
	if dev.URL != "https://dev.global" || dev.GraphQL != "https://dev.local/graphql" ||
		dev.Headers["X-A"] != "1" || dev.Headers["X-B"] != "local" {
		t.Fatalf("dev merged field by field: %+v", dev)
	}
	if l.Env["thien"].URL != "https://thien.global" || l.DefaultEnv != "dev" || l.Alias["me"] != "get /v2/me" {
		t.Fatalf("%+v", l)
	}
	if got := l.EnvNames(); len(got) != 2 || got[0] != "dev" || got[1] != "thien" {
		t.Fatalf("EnvNames: %v", got)
	}
}

func TestSecretsAndUnknownKeysAreRejectedOnRead(t *testing.T) {
	global, dir := setup(t)
	write(t, global, "[env.dev]\nurl = \"https://x\"\ntoken = \"hunter2\"\n")
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "unknown key") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("want an unknown-key error that does not echo the value, got %v", err)
	}
	write(t, global, "[defaults]\npassword = \"x\"\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("password key accepted")
	}
}

func TestBadTOMLReportsPosition(t *testing.T) {
	global, dir := setup(t)
	write(t, global, "[defaults\ntimeout = 1")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), global) {
		t.Fatalf("%v", err)
	}
}

func TestSetGetRoundTripAndTypes(t *testing.T) {
	var c Config
	for k, v := range map[string]string{
		"default_env":             "thien",
		"defaults.timeout":        "30",
		"defaults.retry":          "3",
		"defaults.summary":        "true",
		"defaults.color":          "always",
		"env.dev.url":             "https://d",
		"env.dev.endpoints":       "user, auth",
		"env.dev.token_env":       "API_TOKEN_DEV",
		"env.dev.headers.X-Fresh": "yes",
		"alias.me":                "get /me",
	} {
		if err := Set(&c, k, v); err != nil {
			t.Fatalf("Set(%s): %v", k, err)
		}
		if got, ok := Get(c, k); !ok || (k != "env.dev.endpoints" && got != v) {
			t.Fatalf("Get(%s) = %q, %v", k, got, ok)
		}
	}
	if got, _ := Get(c, "env.dev.endpoints"); got != "user,auth" {
		t.Fatalf("endpoints: %q", got)
	}

	path := filepath.Join(t.TempDir(), "sub", "c.toml")
	if err := Write(path, c); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	back, err := Read(path)
	if err != nil || back.Env["dev"].Headers["X-Fresh"] != "yes" || *back.Defaults.Retry != 3 || !*back.Defaults.Summary {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestSetRefusesSecretsAndBadValues(t *testing.T) {
	var c Config
	for _, k := range []string{"env.dev.token", "env.dev.password", "env.dev.headers.Authorization", "defaults.secret", "env.dev.api_key"} {
		if err := Set(&c, k, "x"); err == nil {
			t.Errorf("Set(%q) accepted a secret-looking key", k)
		}
	}
	for k, v := range map[string]string{
		"defaults.retry": "-1", "defaults.timeout": "soon", "defaults.color": "red",
		"defaults.summary": "maybe", "nope": "x", "env.dev.bogus": "x",
	} {
		if err := Set(&c, k, v); err == nil {
			t.Errorf("Set(%q,%q) accepted", k, v)
		}
	}
	if IsSecretKey("env.dev.token_env") || !IsSecretKey("env.dev.token") {
		t.Fatal("token_env names a variable and is allowed; token is not")
	}
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30": 30 * time.Second, "2m": 2 * time.Minute, "1.5": 1500 * time.Millisecond} {
		if got, err := Duration(in); err != nil || got != want {
			t.Errorf("Duration(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-5", "abc"} {
		if _, err := Duration(bad); err == nil {
			t.Errorf("Duration(%q) accepted", bad)
		}
	}
}

func TestTemplateParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.toml")
	write(t, path, Template)
	if _, err := Read(path); err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	// and an uncommented env block from it is valid too
	write(t, path, strings.ReplaceAll(Template, "# [env.dev]", "[env.dev]"))
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
}

// Without a default_env a project's env is invisible to anything that starts api
// without the shell's session (tv, nvim, CI): `api env use local` is not enough. The
// project template must offer the line, and it must sit ABOVE [vars], or it would
// belong to that table.
func TestProjectTemplateOffersADefaultEnvThatWorksWhenUncommented(t *testing.T) {
	tpl := Render(Project, "", "")
	line := `# default_env = "local"`
	i, vars := strings.Index(tpl, line), strings.Index(tpl, "\n[vars]")
	if i < 0 || vars < 0 || i > vars {
		t.Fatalf("the default_env example must exist and come before [vars] (at %d, [vars] at %d):\n%s", i, vars, tpl)
	}

	path := filepath.Join(t.TempDir(), ".api.toml")
	uncommented := strings.NewReplacer(
		line, `default_env = "local"`,
		"# [env.local]\n# url = ", "[env.local]\nurl = ",
	).Replace(tpl)
	write(t, path, uncommented)
	c, err := Read(path)
	if err != nil {
		t.Fatalf("default_env + [env.local] uncommented does not parse: %v\n%s", err, uncommented)
	}
	if c.DefaultEnv != "local" || c.Env["local"].URL != "http://localhost:3000" {
		t.Fatalf("default_env=%q env.local=%+v", c.DefaultEnv, c.Env["local"])
	}

	// the trap the position guards: under the header it is a [vars] key, which does not
	// exist, and unknown keys are rejected (so a misplaced line cannot be ignored silently)
	write(t, path, strings.Replace(tpl, "[vars]\n", "[vars]\ndefault_env = \"local\"\n", 1))
	if _, err := Read(path); err == nil {
		t.Fatal("default_env under [vars] must be rejected")
	}
}

func TestBothTemplatesParseAndActivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.toml")
	cases := map[string]string{
		"global":           Render(Global, "", ""),
		"project":          Render(Project, "", ""),
		"project detected": Render(Project, "REACT_APP_API_URL", "VITE_GRAPHQL_URL"),
	}
	for name, tpl := range cases {
		write(t, path, tpl)
		if _, err := Read(path); err != nil {
			t.Fatalf("%s template does not parse: %v\n%s", name, err, tpl)
		}
	}
	// uncommenting the example blocks must stay valid (what a user does next)
	write(t, path, strings.NewReplacer("# [env.dev]", "[env.dev]", "# [alias]", "[alias]").Replace(Render(Global, "", "")))
	if _, err := Read(path); err != nil {
		t.Fatalf("global with [env.dev] uncommented: %v", err)
	}
	// uncommenting a single [vars] key must work on its own (the header is active)
	write(t, path, strings.NewReplacer("# env_suffix", "env_suffix", "# [env.local]", "[env.local]").Replace(Render(Project, "", "")))
	if _, err := Read(path); err != nil {
		t.Fatalf("project with [env.local] uncommented: %v", err)
	}
	c, _ := Read(path)
	if c.Vars.EnvSuffix != "dev" {
		t.Fatalf("env_suffix example should parse: %+v", c.Vars)
	}
	// the detected block activates exactly the detected names
	write(t, path, Render(Project, "", "VITE_GRAPHQL_URL"))
	c, _ = Read(path)
	if c.Vars.GraphQLURL != "VITE_GRAPHQL_URL" || c.Vars.BaseURL != "" {
		t.Fatalf("%+v", c.Vars)
	}
	if Template != Render(Global, "", "") {
		t.Fatal("Template must be the global default")
	}
}
