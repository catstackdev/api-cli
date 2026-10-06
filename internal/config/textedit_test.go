package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileWith(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.toml")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func setKV(t *testing.T, path, key, value string) {
	t.Helper()
	preserved, err := SetInFile(path, key, value)
	if err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
	if !preserved {
		t.Fatalf("set %s: comments were not preserved", key)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// every line of the original that is a comment must still be there, in order
func assertCommentsKept(t *testing.T, before, after string) {
	t.Helper()
	var missing []string
	for _, l := range strings.Split(before, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") && !strings.Contains(after, l) {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("comments were lost:\n%s", strings.Join(missing, "\n"))
	}
}

func TestSetKeepsTheGlobalTemplate(t *testing.T) {
	tpl := Render(Global, "", "")
	p := fileWith(t, tpl)
	setKV(t, p, "defaults.timeout", "30s")
	got := read(t, p)
	if !strings.Contains(got, `timeout = "30s"`) || strings.Contains(got, `# timeout = "300s"`) {
		t.Fatalf("the commented example should be uncommented in place:\n%s", got)
	}
	// ONLY that example changed: every other template line is untouched
	for _, l := range strings.Split(tpl, "\n") {
		if l != `# timeout = "300s"` && !strings.Contains(got, l) {
			t.Fatalf("lost template line %q:\n%s", l, got)
		}
	}
	setKV(t, p, "defaults.retry", "2")
	setKV(t, p, "defaults.summary", "true")
	setKV(t, p, "default_env", "dev")
	got = read(t, p)
	for _, want := range []string{"retry = 2", "summary = true", `default_env = "dev"`, `timeout = "30s"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	assertCommentsKept(t, strings.NewReplacer(`# timeout = "300s"`, "", `# retry = 0`, "", `# summary = false`, "", `# default_env = "dev"`, "").Replace(tpl), got)
	c, err := Read(p)
	if err != nil || c.DefaultEnv != "dev" || *c.Defaults.Retry != 2 || c.Defaults.Timeout != "30s" || !*c.Defaults.Summary {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSetAddsTablesAndKeepsTheirCommentedExamples(t *testing.T) {
	tpl := Render(Project, "", "")
	p := fileWith(t, tpl)
	setKV(t, p, "vars.graphql_url", "VITE_GRAPHQL_URL")
	setKV(t, p, "env.local.url", "http://localhost:3000")
	setKV(t, p, "env.local.graphql", "http://localhost:3000/graphql")
	setKV(t, p, "env.local.endpoints", "user, auth")
	setKV(t, p, "alias.me", "get /me .data")
	got := read(t, p)
	// the env table is created once, after the commented example block, which stays
	if strings.Count(got, "\n[env.local]\n") != 1 || !strings.Contains(got, "# [env.local]") {
		t.Fatalf("%s", got)
	}
	assertCommentsKept(t, strings.ReplaceAll(tpl, `# graphql_url = "VITE_GRAPHQL_URL"`, ""), got)
	c, err := Read(p)
	if err != nil || c.Vars.GraphQLURL != "VITE_GRAPHQL_URL" || c.Env["local"].URL != "http://localhost:3000" ||
		c.Env["local"].GraphQL != "http://localhost:3000/graphql" || len(c.Env["local"].Endpoints) != 2 || c.Alias["me"] != "get /me .data" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSetUpdatesInPlaceAndKeepsUserComments(t *testing.T) {
	p := fileWith(t, `# my notes about this file
default_env = "dev"   # my default

[defaults]
# keep this explanation
timeout = "60s"
retry = 1

# the dev environment
[env.dev]
url = "https://old.example"   # old url
`)
	setKV(t, p, "defaults.timeout", "10s")
	setKV(t, p, "env.dev.url", "https://new.example")
	setKV(t, p, "env.dev.graphql", "https://new.example/graphql")
	setKV(t, p, "default_env", "thien")
	got := read(t, p)
	for _, keep := range []string{"# my notes about this file", "# keep this explanation", "retry = 1", "# the dev environment"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	if strings.Count(got, "timeout") != 1 || strings.Contains(got, "old.example") || !strings.Contains(got, `url = "https://new.example"`) {
		t.Fatalf("not updated in place:\n%s", got)
	}
	c, _ := Read(p)
	if c.Defaults.Timeout != "10s" || c.DefaultEnv != "thien" || c.Env["dev"].GraphQL != "https://new.example/graphql" {
		t.Fatalf("%+v", c)
	}
}

func TestSetLoginVarsAndHeadersUseSubTablesAndConvertInlineOnes(t *testing.T) {
	p := fileWith(t, Render(Global, "", ""))
	setKV(t, p, "env.dev.url", "https://api.dev.example.com")
	setKV(t, p, "env.dev.login.gql", "mutation { login }")
	setKV(t, p, "env.dev.login.vars.input.username", "me@x.com")
	setKV(t, p, "env.dev.login.vars.input.password", "@keychain:upscale-dev")
	setKV(t, p, "env.dev.headers.X-Tenant", "dev")
	got := read(t, p)
	if !strings.Contains(got, "[env.dev.login.vars]") || !strings.Contains(got, `"input.password" = "@keychain:upscale-dev"`) || !strings.Contains(got, "[env.dev.headers]") {
		t.Fatalf("%s", got)
	}
	c, err := Read(p)
	l := c.Env["dev"].Login
	if err != nil || l == nil || l.Vars["input.username"] != "me@x.com" || l.Vars["input.password"] != "@keychain:upscale-dev" || c.Env["dev"].Headers["X-Tenant"] != "dev" {
		t.Fatalf("%+v %v", c, err)
	}

	// an inline table written by hand is converted, with its entries kept
	q := fileWith(t, `[env.x]
url = "https://x.example"
headers = { "A" = "1", "B" = "2" }  # hand written
[env.x.login]
path = "/auth"
vars = { user = "me", password = "@keychain:pw" }
`)
	setKV(t, q, "env.x.headers.C", "3")
	setKV(t, q, "env.x.login.vars.extra", "v")
	c, err = Read(q)
	if err != nil || c.Env["x"].Headers["A"] != "1" || c.Env["x"].Headers["B"] != "2" || c.Env["x"].Headers["C"] != "3" ||
		c.Env["x"].Login.Vars["user"] != "me" || c.Env["x"].Login.Vars["password"] != "@keychain:pw" || c.Env["x"].Login.Vars["extra"] != "v" {
		t.Fatalf("%+v %v\n%s", c, err, read(t, q))
	}
	if strings.Contains(read(t, q), "headers = {") || strings.Contains(read(t, q), "vars = {") {
		t.Fatalf("the inline tables should be gone:\n%s", read(t, q))
	}
}

func TestSetEscapesAwkwardValues(t *testing.T) {
	p := fileWith(t, "")
	awkward := "say \"hi\" \\ back\\slash\ttab ünïcode 'single' # not a comment = [x]"
	setKV(t, p, "alias.weird name", awkward)
	setKV(t, p, "alias.q", "gql 'query { me { id } }' .data")
	c, err := Read(p)
	if err != nil || c.Alias["weird name"] != awkward || c.Alias["q"] != "gql 'query { me { id } }' .data" {
		t.Fatalf("%+v %v\n%s", c, err, read(t, p))
	}
	if !strings.Contains(read(t, p), `"weird name" = `) {
		t.Fatalf("a key that is not bare must be quoted:\n%s", read(t, p))
	}
}

func TestSetCreatesAMissingFileWithMode0600AndKeepsCRLF(t *testing.T) {
	p := fileWith(t, "")
	setKV(t, p, "default_env", "dev")
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	crlf := fileWith(t, "# c\r\n[defaults]\r\n# timeout = \"1s\"\r\n")
	setKV(t, crlf, "defaults.timeout", "5s")
	b := read(t, crlf)
	if !strings.Contains(b, "\r\n") || strings.Contains(strings.ReplaceAll(b, "\r\n", ""), "\n") {
		t.Fatalf("line endings must stay CRLF: %q", b)
	}
	if c, err := Read(crlf); err != nil || c.Defaults.Timeout != "5s" {
		t.Fatalf("%+v %v", c, err)
	}
	// a file without a trailing newline
	nonl := fileWith(t, "[alias]\nme = \"get /me\"")
	setKV(t, nonl, "alias.you", "get /you")
	if c, err := Read(nonl); err != nil || c.Alias["me"] != "get /me" || c.Alias["you"] != "get /you" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSetFallsBackWhenTheLayoutCannotBeEditedAndSaysSo(t *testing.T) {
	multi := "# precious comment\n[env.x]\nurl = \"https://x.example\"\nheaders = {\n  \"A\" = \"1\",\n}\n"
	p := fileWith(t, multi)
	preserved, err := SetInFile(p, "env.x.headers.B", "2")
	if err != nil {
		t.Fatal(err)
	}
	if preserved {
		t.Fatal("a multi-line inline table cannot be edited in place: the caller must be told")
	}
	if c, err := Read(p); err != nil || c.Env["x"].Headers["A"] != "1" || c.Env["x"].Headers["B"] != "2" || c.Env["x"].URL != "https://x.example" {
		t.Fatalf("the fallback must still set the value and lose nothing else: %+v %v", c, err)
	}
}

func TestSetRefusesBadValuesWithoutTouchingTheFile(t *testing.T) {
	tpl := Render(Global, "", "")
	p := fileWith(t, tpl)
	for _, bad := range [][2]string{{"defaults.retry", "many"}, {"env.dev.token", "x"}, {"env.dev.login.vars.password", "hunter2"}, {"nope", "x"}, {"vars.env_suffix", "../x"}} {
		if _, err := SetInFile(p, bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if read(t, p) != tpl {
		t.Fatal("a rejected set must not change the file at all")
	}
}

func TestSetInANewDirectory(t *testing.T) {
	// a fresh machine: ~/.config/api does not exist yet
	p := filepath.Join(t.TempDir(), "not", "yet", "there", "config.toml")
	setKV(t, p, "defaults.retry", "1")
	if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", st, err)
	}
	if c, err := Read(p); err != nil || *c.Defaults.Retry != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".api-*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestSetDoesNotWriteThroughAPlantedSymlink(t *testing.T) {
	// a cloned repo can ship ".api.toml.tmp" (the old fixed temp name) as a symlink
	// to a file of yours; config set --local must not truncate it
	dir := t.TempDir()
	victim := filepath.Join(dir, "precious")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".api.toml")
	if err := os.WriteFile(p, []byte("default_env = \"a\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".api.toml.tmp", ".api.toml~", ".api-1.tmp"} {
		_ = os.Symlink(victim, filepath.Join(dir, name))
	}
	setKV(t, p, "defaults.retry", "2")
	if b, _ := os.ReadFile(victim); string(b) != "do not touch" {
		t.Fatalf("config set wrote through a symlink: %q", b)
	}
	if c, err := Read(p); err != nil || *c.Defaults.Retry != 2 || c.DefaultEnv != "a" {
		t.Fatalf("%+v %v", c, err)
	}
}
