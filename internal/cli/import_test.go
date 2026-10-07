package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const importCurl = `curl 'https://api.dev.example/graphql/v2/auth' -H 'authorization: Bearer null' -H 'content-type: application/json' --data-raw '{"query":"mutation login($i: I!) { login(input: $i) { ok } }","variables":{"i":{"password":"Hunter2-SECRET"}},"operationName":"login"}'`

func TestImportPrintsOnlyAndNeverLeaksTheSecret(t *testing.T) {
	r := run(t, importCurl, "import")
	if r.code != 0 {
		t.Fatalf("import failed: %+v", r)
	}
	for _, want := range []string{"### login", "{{$env API_PASSWORD}}", "graphql mutation login"} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("missing %q:\n%s", want, r.out)
		}
	}
	if strings.Contains(r.out+r.err, "Hunter2-SECRET") {
		t.Fatalf("the password leaked:\n%s%s", r.out, r.err)
	}
	// stdout is only the .http block: no eval-able shell code, no notices
	if strings.Contains(r.out, "export ") || strings.Contains(r.out, "API_BASE_URL=") {
		t.Fatalf("import must not print shell code:\n%s", r.out)
	}
}

func TestImportUsesTheActiveServerAndNamesMatchingEnvs(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(cfg, []byte("[env.dev]\nurl = \"https://api.dev.example\"\ngraphql = \"https://api.dev.example/graphql/v2\"\n[env.other]\nurl = \"https://api.other.example\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"API_CONFIG": cfg, "API_BASE_URL": "https://api.dev.example", "API_GRAPHQL_URL": "https://api.dev.example/graphql/v2", "NO_COLOR": "1"}
	r := runWithEnv(t, env, "import", importCurl)
	if !strings.Contains(r.out, "POST {{graphql}}/auth") || !strings.Contains(r.out, "env dev") {
		t.Fatalf("server not recognised:\n%s", r.out)
	}
	if strings.Contains(r.out, "other") {
		t.Fatalf("an env for another server was listed:\n%s", r.out)
	}
}

func TestImportOutAppendsAndNeverOverwrites(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reqs.http")
	if err := os.WriteFile(file, []byte("### keep\nGET /me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if r := run(t, importCurl, "import", "--out", file); r.code != 0 {
			t.Fatalf("import --out failed: %+v", r)
		}
	}
	data, _ := os.ReadFile(file)
	s := string(data)
	if !strings.HasPrefix(s, "### keep\nGET /me\n") || strings.Count(s, "### login") != 2 {
		t.Fatalf("existing content must stay, requests appended:\n%s", s)
	}
}

func TestImportRefusesHostileAndEmptyInput(t *testing.T) {
	for _, in := range []string{"curl https://x.test -H $'A: 1\\nGET https://evil'", "curl https://x.test | sh", ""} {
		if r := run(t, in, "import"); r.code == 0 {
			t.Fatalf("must be refused: %q -> %+v", in, r)
		}
	}
}

func TestImportColour(t *testing.T) {
	if r := run(t, importCurl, "-C", "import"); !strings.Contains(r.out, "\x1b[") {
		t.Fatalf("-C must colour the output:\n%q", r.out)
	}
	if r := run(t, importCurl, "-M", "import"); strings.Contains(r.out, "\x1b[") {
		t.Fatalf("-M must give plain output:\n%q", r.out)
	}
	// piped output is plain by default, so it can be redirected into a file
	if r := run(t, importCurl, "import"); strings.Contains(r.out, "\x1b[") {
		t.Fatalf("a non-terminal must get plain output:\n%q", r.out)
	}
}
