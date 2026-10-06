package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// `config set default_env x` with no env x used to say only "✓": the next request from
// tv, nvim or CI then failed with "no base URL" and nothing pointed back at the cause.
func TestSetDefaultEnvWarnsWhenNothingDefinesIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("API_CONFIG", filepath.Join(dir, "g", "config.toml"))
	t.Chdir(dir)
	clearSession(t)

	// a name nothing defines: it is set (the env may come next), but you are told
	r := run(t, "", "config", "set", "default_env", "local", "--local")
	if r.code != 0 || !strings.Contains(r.err, "✓ default_env = local") {
		t.Fatalf("it must still be set: %+v", r)
	}
	if !strings.Contains(r.err, `env "local" is not defined`) || !strings.Contains(r.err, "api config set env.local.url <url> --local") {
		t.Fatalf("the warning must name the env and the command that fixes it: %q", r.err)
	}

	// once the env exists, no warning
	if r := run(t, "", "config", "set", "env.local.url", "http://localhost:3000", "--local"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "set", "default_env", "local", "--local"); r.code != 0 || strings.Contains(r.err, "⚠") {
		t.Fatalf("a defined env must not warn: %+v", r)
	}
	// an env from the OTHER scope counts too (a global env, set from the project)
	if r := run(t, "", "config", "set", "env.shared.url", "http://shared.test", "--global"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "config", "set", "default_env", "shared", "--local"); r.code != 0 || strings.Contains(r.err, "⚠") {
		t.Fatalf("an env defined in the global file must not warn: %+v", r)
	}
	// and the warning names the scope you are writing to
	if r := run(t, "", "config", "set", "default_env", "ghost", "--global"); !strings.Contains(r.err, "env.ghost.url <url> --global") {
		t.Fatalf("%q", r.err)
	}
}
