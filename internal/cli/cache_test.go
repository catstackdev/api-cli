package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheClearOnlyRemovesOwnFiles(t *testing.T) {
	oa, ug := t.TempDir(), t.TempDir()
	t.Setenv("API_OPENAPI_CACHE_DIR", oa)
	t.Setenv("UGQL_CACHE_DIR", ug)
	keep := filepath.Join(ug, "notes.txt")
	outside := filepath.Join(t.TempDir(), "target.json")
	for _, f := range []string{filepath.Join(oa, "http_a.json"), filepath.Join(ug, "api.json"), filepath.Join(ug, "index.txt"), keep, outside} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink(outside, filepath.Join(oa, "link.json"))
	_ = os.Mkdir(filepath.Join(ug, "sub.json"), 0o700)

	if r := run(t, "", "cache", "status"); r.code != 0 || !strings.Contains(r.out, "openapi") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "cache", "clear", "openapi"); r.code != 0 || !strings.Contains(r.err, "1 cache file") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(ug, "api.json")); err != nil {
		t.Fatal("schema cache must survive `clear openapi`")
	}
	if r := run(t, "", "cache", "clear"); r.code != 0 || !strings.Contains(r.err, "2 cache file") {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{keep, outside, filepath.Join(ug, "sub.json"), oa} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive: %v", p, err)
		}
	}
	if r := run(t, "", "cache", "clear", "nope"); r.code == 0 {
		t.Fatal("unknown cache accepted")
	}
}
