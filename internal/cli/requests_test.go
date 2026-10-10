package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// savedProject is a project with two saved requests, one with the .bak and .orig
// that a pick and an edit leave next to it.
func savedProject(t *testing.T) (dir, reqs string) {
	t.Helper()
	dir = projectWith(t, map[string]string{".env": "A=1\n"})
	reqs = filepath.Join(dir, ".api", "requests")
	if err := os.MkdirAll(reqs, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"get-users.http":      "### users\nGET /users\n",
		"get-users.http.bak":  "old\n",
		"get-users.http.orig": "skeleton\n",
		"user-me.http":        "### me\nPOST /graphql\n\n{\"query\":\"query Me { me { id } }\"}\n",
	} {
		if err := os.WriteFile(filepath.Join(reqs, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, reqs
}

func TestRequestsLsShowsMethodAndURL(t *testing.T) {
	savedProject(t)
	r := runShared(t, t.TempDir(), "", "requests", "ls")
	if r.code != 0 || !strings.Contains(r.out, "get-users") || !strings.Contains(r.out, "GET") || !strings.Contains(r.out, "/users") || !strings.Contains(r.out, "POST") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, ".bak") || strings.Contains(r.out, ".orig") {
		t.Fatalf("side files are not requests:\n%s", r.out)
	}
}

func TestRequestsRmDeletesTheRequestAndItsSideFiles(t *testing.T) {
	_, reqs := savedProject(t)
	state := t.TempDir()
	if r := runShared(t, state, "", "requests", "rm", "get-users"); r.code != 0 || !strings.Contains(r.err, "deleted") {
		t.Fatalf("%+v", r)
	}
	for _, f := range []string{"get-users.http", "get-users.http.bak", "get-users.http.orig"} {
		if _, err := os.Lstat(filepath.Join(reqs, f)); err == nil {
			t.Fatalf("%s is still there", f)
		}
	}
	if _, err := os.Lstat(filepath.Join(reqs, "user-me.http")); err != nil {
		t.Fatalf("another request must stay: %v", err)
	}
	// the .http suffix is accepted; a missing one is an error, the rest still go
	if r := runShared(t, state, "", "requests", "rm", "nope", "user-me.http"); r.code != exitGeneric || !strings.Contains(r.err, "nope") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Lstat(filepath.Join(reqs, "user-me.http")); err == nil {
		t.Fatal("the valid name after a failing one must still be deleted")
	}
}

// A name is never a path, and a symlink is not followed out of the project.
func TestRequestsRmCannotReachOutsideTheRequestsDir(t *testing.T) {
	dir, reqs := savedProject(t)
	state := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	_ = os.WriteFile(victim, []byte("keep me"), 0o600)
	outside := filepath.Join(t.TempDir(), "outside.http")
	_ = os.WriteFile(outside, []byte("### x\nGET /x\n"), 0o600)
	if err := os.Symlink(outside, filepath.Join(reqs, "linked.http")); err != nil {
		t.Skip("no symlinks:", err)
	}

	for _, name := range []string{"../../victim.txt", "../victim.txt", "/etc/passwd", ".hidden", ".."} {
		if r := runShared(t, state, "", "requests", "rm", name); r.code == 0 {
			t.Fatalf("%q was accepted: %+v", name, r)
		}
	}
	if r := runShared(t, state, "", "requests", "rm", "linked"); r.code == 0 {
		t.Fatalf("a symlink was deleted through: %+v", r)
	}
	for _, f := range []string{victim, outside, filepath.Join(reqs, "linked.http")} {
		if _, err := os.Lstat(f); err != nil {
			t.Fatalf("%s must be untouched: %v", f, err)
		}
	}
}

func TestRequestsRmAllAsksFirst(t *testing.T) {
	_, reqs := savedProject(t)
	state := t.TempDir()
	if r := runShared(t, state, "n\n", "requests", "rm", "--all"); r.code == 0 {
		t.Fatalf("declined, yet: %+v", r)
	}
	if m, _ := filepath.Glob(filepath.Join(reqs, "*.http")); len(m) != 2 {
		t.Fatalf("nothing may be deleted when declined: %v", m)
	}
	if r := runShared(t, state, "", "requests", "rm", "--all", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if m, _ := filepath.Glob(filepath.Join(reqs, "*")); len(m) != 0 {
		t.Fatalf("everything goes with --all: %v", m)
	}
	if r := runShared(t, state, "", "requests", "rm"); r.code != exitUsage {
		t.Fatalf("no names and no --all is a usage error: %+v", r)
	}
	if r := runShared(t, state, "", "requests", "rm", "x", "--all"); r.code != exitUsage {
		t.Fatalf("names and --all together: %+v", r)
	}
}
