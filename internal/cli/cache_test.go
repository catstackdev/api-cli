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
	for _, f := range []string{filepath.Join(oa, "http_a.json"), filepath.Join(ug, "api.json"), filepath.Join(ug, "index.txt"), filepath.Join(ug, "preview.jq"), keep, outside} {
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
	if r := run(t, "", "cache", "clear"); r.code != 0 || !strings.Contains(r.err, "3 cache file") {
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

// Without UGQL_CACHE_DIR every server has its own directory under ~/.cache/ugql
// and `current` points at the last one: clear removes those, and only those. A
// directory without api's own preview.jq is somebody else's.
func TestCacheClearWalksTheServerDirectoriesOfTheDefaultCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("UGQL_CACHE_DIR", "")
	t.Setenv("API_OPENAPI_CACHE_DIR", t.TempDir())
	root := filepath.Join(home, ".cache", "ugql")
	write := func(rel string) string {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, f := range []string{"https_a.example/user.json", "https_a.example/index.txt", "https_a.example/preview.jq",
		"http_b.example-8080/auth.json", "http_b.example-8080/preview.jq", "index.txt", "preview.jq", "old.json"} {
		write(f)
	}
	foreign := write("projects/data.json") // a directory that is not a schema cache
	unrelated := write("projects/notes.txt")
	_ = os.Symlink("https_a.example", filepath.Join(root, "current"))

	r := run(t, "", "cache", "clear", "schema")
	if r.code != 0 || !strings.Contains(r.err, "9 cache file") {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{foreign, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive: %v", p, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); err == nil {
		t.Error("the current link must go with the cache")
	}
	if _, err := os.Stat(filepath.Join(root, "https_a.example")); err != nil {
		t.Error("the directories themselves stay")
	}
}

// The link api makes points at a sibling directory by a plain name; anything
// else called "current" is not ours to remove.
func TestCacheClearLeavesAForeignCurrentLinkAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("UGQL_CACHE_DIR", "")
	t.Setenv("API_OPENAPI_CACHE_DIR", t.TempDir())
	root := filepath.Join(home, ".cache", "ugql")
	_ = os.MkdirAll(root, 0o700)
	link := filepath.Join(root, "current")
	if err := os.Symlink(home, link); err != nil { // an absolute target
		t.Fatal(err)
	}
	if r := run(t, "", "cache", "clear", "schema"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("a link to somewhere else was removed: %v", err)
	}
}
