package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── env use with no name ───────────────────────────────────────────────────

func TestEnvUsePicksWithFzf(t *testing.T) {
	writeConfig(t, "[env.dev]\nurl = \"https://dev.example\"\n[env.thien]\nurl = \"https://thien.example\"\n")
	bin := t.TempDir()
	// fake fzf: choose the last line (the index prefix is what fzfPick reads)
	_ = os.WriteFile(filepath.Join(bin, "fzf"), []byte("#!/bin/sh\ntail -n 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := run(t, "", "env", "use", "--emit")
	if r.code != 0 || !strings.Contains(r.out, "API_ENV='thien'") || !strings.Contains(r.out, "https://thien.example") {
		t.Fatalf("%+v", r)
	}
	// a name still works without fzf at all
	t.Setenv("PATH", t.TempDir())
	if r := run(t, "", "env", "use", "dev", "--emit"); r.code != 0 || !strings.Contains(r.out, "API_ENV='dev'") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "env", "use", "--emit"); r.code != 1 || !strings.Contains(r.err, "needs fzf") {
		t.Fatalf("no fzf, no name: %+v", r)
	}
	writeConfig(t, "")
	if r := run(t, "", "env", "use", "--emit"); r.code != 1 || !strings.Contains(r.err, "no envs configured") {
		t.Fatalf("%+v", r)
	}
}
