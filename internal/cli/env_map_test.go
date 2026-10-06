package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// ── .env URL variable mapping ──────────────────────────────────────────────

// mappingResult runs the statements `env load --emit` appends after sourcing
// (everything after the API_ENV export) in a real sh, with vars preset.
func mappingResult(t *testing.T, emitted string, preset map[string]string) map[string]string {
	t.Helper()
	line := strings.TrimSpace(emitted)
	_, after, ok := strings.Cut(line, "export API_ENV='")
	if !ok {
		t.Fatalf("no API_ENV export in %q", line)
	}
	_, stmts, _ := strings.Cut(after, "'")
	stmts = strings.TrimPrefix(strings.TrimSpace(stmts), ";")
	script := stmts + `; printf 'GQL=%s\nBASE=%s\n' "${API_GRAPHQL_URL-}" "${API_BASE_URL-}"`
	cmd := exec.Command("sh", "-c", script)
	for k, v := range preset {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh failed: %v\n%s\nscript: %s", err, out, script)
	}
	res := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(l, "=")
		res[k] = v
	}
	return res
}

func TestEnvLoadMapsProjectUrlVariables(t *testing.T) {
	// the real shape of an Upscale project .env: several candidates, only the
	// framework-prefixed one is what the app reads
	projectWith(t, map[string]string{
		".env.thien": "APP_DOMAIN_NAME=x\nGRAPHQL_URL=https://old.example/graphql\nVITE_GRAPHQL_URL=https://api.thien.example/graphql\nVITE_CLIENT_NAME=c\n",
	})
	r := run(t, "", "env", "load", "thien", "--emit")
	if r.code != 0 || !strings.Contains(r.out, `API_GRAPHQL_URL="$VITE_GRAPHQL_URL"`) || strings.Contains(r.out, `"$GRAPHQL_URL"`) {
		t.Fatalf("VITE_ must win over the bare name: %+v", r)
	}
	if !strings.Contains(r.err, "GraphQL URL ← $VITE_GRAPHQL_URL") || !strings.Contains(r.err, "base URL ←") {
		t.Fatalf("the user must be told what was mapped: %q", r.err)
	}
	got := mappingResult(t, r.out, map[string]string{
		"VITE_GRAPHQL_URL": "https://api.thien.example/graphql", "GRAPHQL_URL": "https://old.example/graphql",
	})
	if got["GQL"] != "https://api.thien.example/graphql" || got["BASE"] != "https://api.thien.example" {
		t.Fatalf("values after eval: %v", got)
	}
}

func TestEnvMappingRules(t *testing.T) {
	load := func(file, toml string) result {
		writeConfig(t, toml) // every case starts from its own config ("" = none)
		projectWith(t, map[string]string{".env.x": file})
		return run(t, "", "env", "load", "x", "--emit")
	}
	// config [vars] beats detection (and need not exist in the file)
	r := load("GRAPHQL_URL=a\nVITE_GRAPHQL_URL=b\n", "[vars]\ngraphql_url = \"GRAPHQL_URL\"\nbase_url = \"MY_API_HOST\"\n")
	if !strings.Contains(r.out, `API_GRAPHQL_URL="$GRAPHQL_URL"`) || !strings.Contains(r.out, `API_BASE_URL="$MY_API_HOST"`) {
		t.Fatalf("[vars] override: %q", r.out)
	}
	if got := mappingResult(t, r.out, map[string]string{"GRAPHQL_URL": "https://g/graphql", "MY_API_HOST": "https://h"}); got["GQL"] != "https://g/graphql" || got["BASE"] != "https://h" {
		t.Fatalf("%v", got)
	}

	// a file that already defines API_BASE_URL: no base mapping, no derivation
	r = load("API_BASE_URL=https://own\nGRAPHQL_API=https://own/graphql\n", "")
	if strings.Contains(r.out, "API_BASE_URL=") {
		t.Fatalf("must not touch a base the file defines: %q", r.out)
	}
	if !strings.Contains(r.out, `API_GRAPHQL_URL="$GRAPHQL_API"`) || strings.Contains(r.out, "%/graphql") {
		t.Fatalf("GRAPHQL_API variant: %q", r.out)
	}

	// nothing recognisable: the output is exactly the old behaviour
	r = load("FOO=1\nBAR=2\n", "")
	if strings.Count(strings.TrimSpace(r.out), ";") != 3 || strings.Contains(r.err, "←") {
		t.Fatalf("no candidates, no extra statements: %q / %q", r.out, r.err)
	}

	// a blank placeholder (VITE_GRAPHQL_URL=) never wipes an existing value
	r = load("VITE_GRAPHQL_URL=\n", "")
	got := mappingResult(t, r.out, map[string]string{"VITE_GRAPHQL_URL": "", "API_GRAPHQL_URL": "https://keep/graphql"})
	if got["GQL"] != "https://keep/graphql" {
		t.Fatalf("blank source clobbered the existing value: %v", got)
	}
	// a graphql url without /graphql does not become the base url
	r = load("GRAPHQL_URL=https://only.example/gql\n", "")
	if got := mappingResult(t, r.out, map[string]string{"GRAPHQL_URL": "https://only.example/gql"}); got["BASE"] != "" {
		t.Fatalf("base must stay unset: %v", got)
	}
}

func TestVarsConfigIsValidated(t *testing.T) {
	writeConfig(t, "[vars]\ngraphql_url = \"not a name; rm -rf /\"\n")
	if r := run(t, "", "config", "show"); !strings.Contains(r.err, "config ignored") || !strings.Contains(r.err, "not a variable name") {
		t.Fatalf("a bad variable name must be rejected (it ends up in eval'd shell code): %+v", r)
	}
	writeConfig(t, "")
	for _, bad := range []string{"has space", "a;b", "$(x)", "1ABC", "a-b"} {
		if r := run(t, "", "config", "set", "vars.graphql_url", bad, "--global"); r.code == 0 {
			t.Fatalf("set accepted %q", bad)
		}
	}
	if r := run(t, "", "config", "set", "vars.graphql_url", "VITE_GRAPHQL_URL", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "get", "vars.graphql_url"); strings.TrimSpace(r.out) != "VITE_GRAPHQL_URL" {
		t.Fatalf("%+v", r)
	}
}

// ── env load picks a default file ──────────────────────────────────────────

func TestEnvLoadDefaultFile(t *testing.T) {
	loadOut := func(files map[string]string, toml string, args ...string) result {
		writeConfig(t, toml)
		projectWith(t, files)
		return run(t, "", append([]string{"env", "load"}, append(args, "--emit")...)...)
	}
	envOf := func(r result) string { // the API_ENV value that would be set
		_, after, _ := strings.Cut(r.out, "export API_ENV='")
		v, _, _ := strings.Cut(after, "'")
		return v
	}

	// a plain .env wins: nothing automatic happens
	r := loadOut(map[string]string{".env": "A=1\n", ".env.dev": "A=2\n"}, "")
	if envOf(r) != "default" || strings.Contains(r.err, "automatically") {
		t.Fatalf("%+v", r)
	}
	// no .env: fall back to .env.local, then .env.development, then .env.dev, and say so
	for want, files := range map[string]map[string]string{
		"local":       {".env.local": "A=1\n", ".env.development": "A=1\n", ".env.dev": "A=1\n"},
		"development": {".env.development": "A=1\n", ".env.dev": "A=1\n"},
		"dev":         {".env.dev": "A=1\n", ".env.thien": "A=1\n"},
	} {
		r = loadOut(files, "")
		if envOf(r) != want || !strings.Contains(r.err, "picked automatically (no .env here)") {
			t.Fatalf("want %s: %+v", want, r)
		}
	}
	// config's vars.env_suffix wins over the fallbacks (and over a plain .env), an explicit name wins over everything
	files := map[string]string{".env": "A=1\n", ".env.dev": "A=1\n", ".env.thien": "A=1\n"}
	r = loadOut(files, "[vars]\nenv_suffix = \"thien\"\n")
	if envOf(r) != "thien" || !strings.Contains(r.err, "vars.env_suffix") {
		t.Fatalf("%+v", r)
	}
	r = loadOut(files, "[vars]\nenv_suffix = \"thien\"\n", "dev")
	if envOf(r) != "dev" || strings.Contains(r.err, "automatically") {
		t.Fatalf("explicit name must win: %+v", r)
	}
	// not found: say what exists
	r = loadOut(map[string]string{".env.dev": "A=1\n", ".env.thien": "A=1\n"}, "", "prod")
	if r.code != 1 || !strings.Contains(r.err, "available: dev, thien") {
		t.Fatalf("%+v", r)
	}
}

func TestEnvSuffixCannotBeAPath(t *testing.T) {
	// the file `source`d is always .env.<suffix> in the project root, never a path from config
	for _, bad := range []string{"../../.ssh/config", "/etc/passwd", "a/b", "..", ".hidden", "x y"} {
		writeConfig(t, "[vars]\nenv_suffix = \""+bad+"\"\n")
		if r := run(t, "", "config", "show"); !strings.Contains(r.err, "config ignored") {
			t.Errorf("env_suffix %q accepted from a file", bad)
		}
		writeConfig(t, "")
		if r := run(t, "", "config", "set", "vars.env_suffix", bad, "--global"); r.code == 0 {
			t.Errorf("config set accepted env_suffix %q", bad)
		}
	}
	projectWith(t, map[string]string{".env.dev": "A=1\n"})
	if r := run(t, "", "env", "load", "../x", "--emit"); r.code == 0 {
		t.Fatalf("a path as the explicit suffix must be refused: %+v", r)
	}
	writeConfig(t, "")
	if r := run(t, "", "config", "set", "vars.env_suffix", "dev", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
}
