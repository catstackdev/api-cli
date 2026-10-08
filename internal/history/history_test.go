package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const A = "http://a.example"

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return filepath.Join(dir, "api", "history")
}

func TestRecordAndEntriesNewestFirstDeduped(t *testing.T) {
	setup(t)
	Record(Entry{Method: "GET", Path: "/users", Filter: ".data", Origin: A})
	Record(Entry{Method: "GET", Path: "/orders", Filter: ".", Origin: A})
	Record(Entry{Method: "POST", Path: "/users", Origin: A})
	Record(Entry{Method: "GET", Path: "/users", Filter: ".data", Origin: A}) // duplicate: moves to the front
	got := Entries(0)
	if len(got) != 3 || got[0] != (Entry{Method: "GET", Path: "/users", Filter: ".data", Origin: A}) || got[1] != (Entry{Method: "POST", Path: "/users", Origin: A}) || got[2] != (Entry{Method: "GET", Path: "/orders", Origin: A}) {
		t.Fatalf("%+v", got)
	}
	if e := Entries(2); len(e) != 2 {
		t.Fatalf("limit: %+v", e)
	}
	if p := Paths("/u", A); len(p) != 1 || p[0] != "/users" {
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
	if got[0].Origin != "" {
		t.Fatalf("a legacy line has no server: %+v", got[0])
	}
	if p := Paths("", A); len(p) != 0 {
		t.Fatalf("a legacy line is not offered for any server: %v", p)
	}
}

func TestSecretsAreNeverRecorded(t *testing.T) {
	f := setup(t)
	for _, p := range []string{
		"/cb?access_token=abc", "/x?api_key=1", "/x?password=1", "/x?Signature=abc", "/x?session=1",
		"https://user:pass@host/x", "/bad\npath", "/tab\tpath", "",
	} {
		Record(Entry{Method: "GET", Path: p})
	}
	Record(Entry{Method: "GET", Path: "/ok?limit=5&sort=asc", Filter: ""})
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
		Record(Entry{Method: "GET", Path: "/p" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)})
	}
	data, _ := os.ReadFile(f)
	if n := strings.Count(string(data), "\n"); n > maxLines {
		t.Fatalf("%d lines", n)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

// A request belongs to the server it went to: the default view, replay and
// completion must never mix servers, and an entry from before the origin was
// recorded belongs to none.
func TestEntriesAreScopedToTheirServer(t *testing.T) {
	f := setup(t)
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	_ = os.WriteFile(f, []byte("GET\t/legacy\t\n"), 0o600)
	Record(Entry{Method: "GET", Path: "/shared", Origin: A})
	Record(Entry{Method: "GET", Path: "/shared", Origin: "http://b.example"})
	Record(Entry{Method: "GET", Path: "/only-a", Origin: A})
	Record(Entry{Method: RunMethod, Path: "saved-a", Origin: A})

	if got := ForOrigin(A, 0); len(got) != 3 {
		t.Fatalf("A: %+v", got)
	}
	if got := ForOrigin("http://b.example", 0); len(got) != 1 || got[0].Path != "/shared" {
		t.Fatalf("B: %+v", got)
	}
	if got := ForOrigin("", 0); len(got) != 0 {
		t.Fatalf("no server must match nothing, legacy included: %+v", got)
	}
	if got := Entries(0); len(got) != 5 || got[len(got)-1].Origin != "" {
		t.Fatalf("all, legacy last: %+v", got)
	}
	if p := Paths("/", A); len(p) != 2 || p[0] != "/only-a" || p[1] != "/shared" {
		t.Fatalf("paths: %v", p)
	}
	if p := Paths("", A); len(p) != 2 {
		t.Fatalf("a RUN name is not a path: %v", p)
	}
	if p := Paths("/", "http://c.example"); len(p) != 0 {
		t.Fatalf("%v", p)
	}
	Record(Entry{Method: "GET", Path: "/x", Origin: "http://a\nb"})
	if got := Entries(0); len(got) != 5 {
		t.Fatalf("an origin with a line break must be dropped: %+v", got)
	}
}
