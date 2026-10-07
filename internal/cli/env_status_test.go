package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func statusEnv(t *testing.T, toml string, env map[string]string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	env["API_CONFIG"] = cfg
	env["NO_COLOR"] = "1"
	return runWithEnv(t, env, "env", "status").out
}

const devProfile = "[env.dev]\nurl = \"https://api.dev.example\"\ngraphql = \"https://api.dev.example/graphql/v2\"\n"

// The env NAME is attacker-controllable (a cloned repo's .env.dev), so status
// must claim a profile only for the server it really describes.
func TestStatusNeverClaimsAProfileByNameAlone(t *testing.T) {
	out := statusEnv(t, devProfile, map[string]string{
		"API_ENV": "dev", "API_BASE_URL": "https://evil.example",
	})
	if strings.Contains(out, "same server as the shell") {
		t.Fatalf("a profile was claimed for a different server:\n%s", out)
	}
	if !strings.Contains(out, "NOT toml env.dev") {
		t.Fatalf("a mismatch must be called out:\n%s", out)
	}
}

func TestStatusShowsWhereEachValueCameFrom(t *testing.T) {
	out := statusEnv(t, devProfile, map[string]string{
		"API_ENV": "dev", "API_BASE_URL": "https://api.dev.example",
		"API_GRAPHQL_URL": "https://api.dev.example/graphql/v2", "API_TOKEN": "tok-123456789012",
	})
	for _, want := range []string{
		`source: toml env.dev · global`,
		"source: shell variables (API_GRAPHQL_URL)",
		"source: shell variables (API_TOKEN)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestStatusExplicitEnvAndDefaults(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(cfg, []byte(devProfile+"[env.nogql]\nurl = \"https://x.example\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"API_CONFIG": cfg, "NO_COLOR": "1"}
	out := runWithEnv(t, env, "--env", "dev", "env", "status").out
	if !strings.Contains(out, `source: toml env.dev · global`) {
		t.Fatalf("--env source missing:\n%s", out)
	}
	out = runWithEnv(t, env, "--env", "nogql", "env", "status").out
	if !strings.Contains(out, "default: /graphql on the base URL") {
		t.Fatalf("the derived GraphQL default must be shown:\n%s", out)
	}
	out = runWithEnv(t, env, "--base", "https://b.example", "env", "status").out
	if !strings.Contains(out, "source: --base") {
		t.Fatalf("--base source missing:\n%s", out)
	}
}

func TestStatusColour(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(cfg, []byte(devProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"API_CONFIG": cfg, "API_ENV": "dev", "API_BASE_URL": "https://evil.example"}
	on := runWithEnv(t, env, "-C", "env", "status").out
	if !strings.Contains(on, "\x1b[33m") { // yellow: the mismatch warning
		t.Fatalf("a mismatch must be painted yellow:\n%q", on)
	}
	if off := runWithEnv(t, env, "-M", "env", "status").out; strings.Contains(off, "\x1b[") {
		t.Fatalf("-M must give plain output:\n%q", off)
	}
}

// `env load` and `env use` set the same variables; API_ENV_FROM is how status
// tells them apart. It is display only: a profile is still claimed only for the
// same server.
func TestStatusTellsEnvLoadFromEnvUse(t *testing.T) {
	base := func(from string) map[string]string {
		return map[string]string{"API_ENV": "dev", "API_BASE_URL": "https://api.dev.example", "API_ENV_FROM": from}
	}
	cases := []struct{ from, want string }{
		{"toml", `toml env.dev · global`},
		{"toml", `(api env use)`},
		{"file:.env.dev", `.env.dev (api env load) · same server as toml env.dev`},
	}
	for _, c := range cases {
		if out := statusEnv(t, devProfile, base(c.from)); !strings.Contains(out, c.want) {
			t.Fatalf("from=%q: missing %q in:\n%s", c.from, c.want, out)
		}
	}
	// a file marker that is not a plain .env name is ignored, never shown
	if out := statusEnv(t, devProfile, base("file:../../etc/passwd")); strings.Contains(out, "passwd") {
		t.Fatalf("an unvalidated marker was displayed:\n%s", out)
	}
	// the marker never makes a profile match a different server
	env := base("toml")
	env["API_BASE_URL"] = "https://evil.example"
	if out := statusEnv(t, devProfile, env); strings.Contains(out, "(api env use)") || !strings.Contains(out, "NOT toml env.dev") {
		t.Fatalf("a marker must not claim a profile for another server:\n%s", out)
	}
}
