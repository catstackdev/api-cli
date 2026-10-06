package secretstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndex(t *testing.T) {
	i := Index{Path: filepath.Join(t.TempDir(), "state", "secrets")}
	if len(i.Names()) != 0 {
		t.Fatal("a missing index is empty")
	}
	for _, n := range []string{"upscale-dev", "a", "upscale-dev", "m.x_y-1"} {
		if err := i.Add(n); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(i.Names(), ","); got != "a,m.x_y-1,upscale-dev" {
		t.Fatalf("sorted and de-duplicated: %q", got)
	}
	if err := i.Remove("a"); err != nil || strings.Join(i.Names(), ",") != "m.x_y-1,upscale-dev" {
		t.Fatalf("%v %v", err, i.Names())
	}
	if err := i.Remove("never-there"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(i.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(i.Path), ".secrets-*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestIndexRejectsBadNamesAndIgnoresGarbageLines(t *testing.T) {
	i := Index{Path: filepath.Join(t.TempDir(), "secrets")}
	for _, bad := range []string{"a b", "../x", "", "$(x)"} {
		if err := i.Add(bad); err == nil {
			t.Errorf("Add(%q) accepted", bad)
		}
	}
	// a hand-edited file with junk: only valid names survive
	if err := os.WriteFile(i.Path, []byte("good\n../evil\n\n  also-good  \n$(rm -rf /)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(i.Names(), ","); got != "also-good,good" {
		t.Fatalf("%q", got)
	}
}
