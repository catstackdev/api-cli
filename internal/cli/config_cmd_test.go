package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.toml")
	_ = os.WriteFile(good, []byte("[env.dev]\nurl = \"https://x.example\"\n"), 0o600)
	bad := filepath.Join(dir, "bad.toml")
	_ = os.WriteFile(bad, []byte("[env.dev]\nurl = \"ftp://x\"\n[env.dev.login]\nendpoint = \"e\"\n"), 0o600)
	secret := filepath.Join(dir, "secret.toml")
	_ = os.WriteFile(secret, []byte("[env.dev.login.vars]\npassword = \"hunter2-literal\"\n"), 0o600)
	proj := filepath.Join(dir, ".api.toml")
	_ = os.WriteFile(proj, []byte("[defaults]\ntoken_cache = true\n"), 0o600)

	if r := run(t, "", "config", "validate", good); r.code != 0 || !strings.Contains(r.err, "✓ "+good) {
		t.Fatalf("good: %+v", r)
	}
	r := run(t, "", "config", "validate", bad)
	if r.code != exitGeneric || !strings.Contains(r.err, "not an http(s) URL") || !strings.Contains(r.err, "login needs exactly one of path") {
		t.Fatalf("bad: %+v", r)
	}
	// a literal password is reported by key, never echoed
	r = run(t, "", "config", "validate", secret)
	if r.code != exitGeneric || !strings.Contains(r.err, "login.vars.password") || strings.Contains(r.err+r.out, "hunter2-literal") {
		t.Fatalf("secret: %+v", r)
	}
	// warnings alone do not fail
	r = run(t, "", "config", "validate", proj)
	if r.code != 0 || !strings.Contains(r.err, "ignored in a project file") {
		t.Fatalf("project: %+v", r)
	}
	if r := run(t, "", "config", "validate", filepath.Join(dir, "missing.toml")); r.code == 0 {
		t.Fatalf("a missing file must fail: %+v", r)
	}
}

func TestConfigSchemaPrintsValidJSON(t *testing.T) {
	r := run(t, "", "config", "schema")
	var v map[string]any
	if r.code != 0 || json.Unmarshal([]byte(r.out), &v) != nil || v["$schema"] == nil {
		t.Fatalf("%+v", r)
	}
}
