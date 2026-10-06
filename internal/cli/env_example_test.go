package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── env example / check / init ─────────────────────────────────────────────

func TestEnvExampleNeverPrintsValues(t *testing.T) {
	projectWith(t, map[string]string{
		".env":         "API_BASE_URL=https://dev.example.com\nAPI_TOKEN=CANARY-TOKEN\nDB=postgres://u:CANARY-PW@h/db\n",
		".env.staging": "API_BASE_URL=https://stg.example.com\nEXTRA=1\n",
		".env.example": "OLD=1\n", // must not be read as a source
	})
	r := run(t, "", "env", "example")
	if r.code != 0 || strings.Contains(r.out+r.err, "CANARY") || strings.Contains(r.out, "example.com") {
		t.Fatalf("values leaked: %+v", r)
	}
	if !strings.Contains(r.out, "API_BASE_URL=\n") || !strings.Contains(r.out, "EXTRA=\n") || strings.Contains(r.out, "OLD=") {
		t.Fatalf("keys: %q", r.out)
	}
	r = run(t, "", "env", "example", "--keep-values")
	if strings.Contains(r.out, "CANARY") || !strings.Contains(r.out, "API_BASE_URL=https://dev.example.com\n") {
		t.Fatalf("--keep-values: %q", r.out)
	}
}

func TestEnvExampleWriteCheckInit(t *testing.T) {
	dir := projectWith(t, map[string]string{".env": "A=1\nB=2\nSECRET=x\n", ".env.staging": "A=1\n"})

	if r := run(t, "", "env", "example", "--check"); r.code != 1 || !strings.Contains(r.err, "no .env.example") {
		t.Fatalf("check without example: %+v", r)
	}
	if r := run(t, "", "env", "example", "--write"); r.code != 0 {
		t.Fatalf("write: %+v", r)
	}
	ex, _ := os.ReadFile(filepath.Join(dir, ".env.example"))
	if !strings.Contains(string(ex), "SECRET=\n") || strings.Contains(string(ex), "SECRET=x") {
		t.Fatalf("example: %s", ex)
	}
	// idempotent; refuses to clobber a different file without --force
	if r := run(t, "", "env", "example", "--write"); r.code != 0 || !strings.Contains(r.err, "up to date") {
		t.Fatalf("rewrite same: %+v", r)
	}
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("HAND=written\n"), 0o644)
	if r := run(t, "", "env", "example", "--write"); r.code != 1 || !strings.Contains(r.err, "--force") {
		t.Fatalf("clobber guard: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env.example")); string(b) != "HAND=written\n" {
		t.Fatal("the existing example was overwritten without --force")
	}
	if r := run(t, "", "env", "example", "--write", "--force"); r.code != 0 {
		t.Fatalf("force: %+v", r)
	}

	// drift: staging lacks B and SECRET
	r := run(t, "", "env", "example", "--check")
	if r.code != 1 || !strings.Contains(r.out, ".env.staging") || !strings.Contains(r.out, "B SECRET") {
		t.Fatalf("drift: %+v", r)
	}
	// init creates a 0600 copy and refuses to overwrite
	if r := run(t, "", "env", "init", "prod"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	if st, err := os.Stat(filepath.Join(dir, ".env.prod")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("init mode: %v %v", st, err)
	}
	if r := run(t, "", "env", "init", "prod"); r.code != 1 || !strings.Contains(r.err, "already exists") {
		t.Fatalf("init twice: %+v", r)
	}
	for _, bad := range []string{"../x", "example", "a b"} {
		if r := run(t, "", "env", "init", bad); r.code == 0 {
			t.Fatalf("init %q accepted", bad)
		}
	}
	// the example file is not offered as an env to load
	if r := run(t, "", "env", "list"); strings.Contains(r.out, "example") {
		t.Fatalf("env list shows the example file: %q", r.out)
	}
	// once staging is completed the check passes
	_ = os.WriteFile(filepath.Join(dir, ".env.staging"), []byte("A=1\nB=2\nSECRET=3\n"), 0o600)
	_ = os.Remove(filepath.Join(dir, ".env.prod"))
	if r := run(t, "", "env", "example", "--check"); r.code != 0 || !strings.Contains(r.err, "match .env.example") {
		t.Fatalf("clean check: %+v", r)
	}
}
