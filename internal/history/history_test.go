package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return filepath.Join(dir, "api", "history")
}

func TestRecordAndEntriesNewestFirstDeduped(t *testing.T) {
	setup(t)
	Record(Entry{"GET", "/users", ".data"})
	Record(Entry{"GET", "/orders", "."})
	Record(Entry{"POST", "/users", ""})
	Record(Entry{"GET", "/users", ".data"}) // duplicate: moves to the front
	got := Entries(0)
	if len(got) != 3 || got[0] != (Entry{"GET", "/users", ".data"}) || got[1] != (Entry{"POST", "/users", ""}) || got[2] != (Entry{"GET", "/orders", ""}) {
		t.Fatalf("%+v", got)
	}
	if e := Entries(2); len(e) != 2 {
		t.Fatalf("limit: %+v", e)
	}
	if p := Paths("/u"); len(p) != 1 || p[0] != "/users" {
		t.Fatalf("Paths dedupes across methods: %v", p)
	}
}

func TestLegacyBarePathLinesStillWork(t *testing.T) {
	f := setup(t)
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	_ = os.WriteFile(f, []byte("/old/one\n/old/two\n"), 0o600)
	got := Entries(0)
	if len(got) != 2 || got[0].Path != "/old/two" || got[0].Method != "GET" {
		t.Fatalf("%+v", got)
	}
	if p := Paths(""); len(p) != 2 {
		t.Fatalf("%v", p)
	}
}

func TestSecretsAreNeverRecorded(t *testing.T) {
	f := setup(t)
	for _, p := range []string{
		"/cb?access_token=abc", "/x?api_key=1", "/x?password=1", "/x?Signature=abc", "/x?session=1",
		"https://user:pass@host/x", "/bad\npath", "/tab\tpath", "",
	} {
		Record(Entry{"GET", p, ""})
	}
	Record(Entry{"GET", "/ok?limit=5&sort=asc", ""})
	data, _ := os.ReadFile(f)
	if strings.Contains(string(data), "abc") || strings.Contains(string(data), "pass") || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("history file:\n%s", data)
	}
	if !Safe("/ok?limit=5") || Safe("/x?token=1") || !Safe("https://host/x?limit=1") {
		t.Fatal("Safe")
	}
}

func TestFileIsPrivateAndCapped(t *testing.T) {
	f := setup(t)
	for i := 0; i < maxLines+20; i++ {
		Record(Entry{"GET", "/p" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26), ""})
	}
	data, _ := os.ReadFile(f)
	if n := strings.Count(string(data), "\n"); n > maxLines {
		t.Fatalf("%d lines", n)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}
