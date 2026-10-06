package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"
)

// run the CLI with an injected environment: nothing in the process environment
// may leak in, and nothing is written to it.
func runWithEnv(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, IO{
		In: strings.NewReader(""), Out: &out, Err: &errb, Store: secretstore.NewMemory(),
		Getenv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	})
	return result{code, out.String(), errb.String()}
}

func TestInjectedEnvironmentIsUsedAndTheRealOneIgnored(t *testing.T) {
	// the REAL process environment says one thing...
	t.Setenv("API_BASE_URL", "https://REAL-PROCESS-ENV.example")
	t.Setenv("API_ENV", "real-env")
	t.Setenv("API_TOKEN", "REAL-PROCESS-TOKEN")
	t.Setenv("API_CONFIG", "/nonexistent/real.toml")

	cfg := filepath.Join(t.TempDir(), "injected.toml")
	if err := os.WriteFile(cfg, []byte("[env.fake]\nurl = \"https://fake.example\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ...the injected one says another
	env := map[string]string{
		"API_BASE_URL": "https://injected.example",
		"API_ENV":      "injected",
		"API_TOKEN":    "INJECTED-TOKEN-123456",
		"API_CONFIG":   cfg,
		"NO_COLOR":     "1",
	}

	r := runWithEnv(t, env, "env", "status")
	if r.code != 0 || !strings.Contains(r.out, "https://injected.example") || !strings.Contains(r.out, "injected") || !strings.Contains(r.out, "INJECTED-TOK") {
		t.Fatalf("the injected environment was not used: %+v", r)
	}
	if strings.Contains(r.out+r.err, "REAL-PROCESS") || strings.Contains(r.out, "real-env") {
		t.Fatalf("the real process environment leaked in:\n%s", r.out)
	}
	// the config path comes from the injected environment too
	r = runWithEnv(t, env, "config", "show")
	if !strings.Contains(r.out, "fake") || !strings.Contains(r.out, "https://fake.example") {
		t.Fatalf("API_CONFIG not taken from the injected environment: %+v", r)
	}
	// and the environment is only READ: nothing was set on the process
	if os.Getenv("API_BASE_URL") != "https://REAL-PROCESS-ENV.example" {
		t.Fatal("the process environment was modified")
	}
	// token_env and @env: references read the injected environment as well
	env["API_TOKEN_X"] = "FROM-INJECTED-TOKEN-ENV"
	if err := os.WriteFile(cfg, []byte("[env.x]\nurl = \"https://x.example\"\ntoken_env = \"API_TOKEN_X\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runWithEnv(t, env, "--env", "x", "get", "/p", "--dry"); !strings.Contains(r.out, "Bearer FROM-INJECTE") {
		t.Fatalf("token_env must be read from the injected environment: %s", r.out)
	}
}
