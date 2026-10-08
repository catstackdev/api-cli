package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── history / again ────────────────────────────────────────────────────────

func TestHistoryAndAgain(t *testing.T) {
	var gotPath, gotAuth string
	calls := 0
	server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath, gotAuth = r.Method+" "+r.URL.RequestURI(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"id":7,"name":"n"},"ok":true}`))
	})
	t.Setenv("API_TOKEN", "TOK-1")
	state := t.TempDir()
	runS := func(args ...string) result { // same state dir across calls
		t.Helper()
		defer leaveSourceTree(t)()
		var out, errb bytes.Buffer
		t.Setenv("XDG_STATE_HOME", state)
		if os.Getenv("API_CONFIG") == "" {
			t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
		}
		code := Run(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
		return result{code, out.String(), errb.String()}
	}

	if r := runS("again"); r.code != 1 || !strings.Contains(r.err, "no history yet") {
		t.Fatalf("empty: %+v", r)
	}
	runS("get", "/users?limit=5", ".data")
	runS("get", "/orders")
	runS("post", "/users", `{"secret-body":1}`)

	if r := runS("history"); !strings.Contains(r.out, "/users?limit=5") || !strings.Contains(r.out, "POST") || strings.Contains(r.out+r.err, "secret-body") {
		t.Fatalf("history (no bodies!): %+v", r)
	}
	// 1 = the POST (refused), 2 = /orders, 3 = /users
	if r := runS("again"); r.code != 1 || !strings.Contains(r.err, "only GET can be repeated") {
		t.Fatalf("POST must not be repeated: %+v", r)
	}
	calls = 0
	if r := runS("again", "2", "-c"); r.code != 0 || gotPath != "GET /orders" || calls != 1 {
		t.Fatalf("again 2: %+v path=%q", r, gotPath)
	}
	// the stored filter is reused; a new one replaces it; the CURRENT token is used
	t.Setenv("API_TOKEN", "TOK-2")
	if r := runS("again", "3", "-c"); strings.TrimSpace(r.out) != `{"id":7,"name":"n"}` || gotPath != "GET /users?limit=5" || gotAuth != "Bearer TOK-2" {
		t.Fatalf("stored filter / current token: %+v path=%q auth=%q", r, gotPath, gotAuth)
	}
	// running an entry moves it to the front, so /users is index 1 now
	if r := runS("again", "1", ".data.id"); strings.TrimSpace(r.out) != "7" {
		t.Fatalf("filter override: %+v", r)
	}
	if r := runS("again", "99"); r.code != 1 || !strings.Contains(r.err, "entries in the history") {
		t.Fatalf("out of range: %+v", r)
	}
	if r := runS("again", "--dry", "2"); !strings.HasPrefix(r.out, "curl ") {
		t.Fatalf("--dry: %+v", r)
	}
	if r := runS("history", "clear"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := runS("history"); !strings.Contains(r.err, "no history") {
		t.Fatalf("after clear: %+v", r)
	}
}

func TestAgainPickWithFzf(t *testing.T) {
	var gotPath string
	server(t, func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path; _, _ = w.Write([]byte(`{}`)) })
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	runQ := func(args ...string) result {
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
		return result{code, out.String(), errb.String()}
	}
	runQ("get", "/first")
	runQ("get", "/second") // newest: index 1

	// a fake fzf that picks the LAST line (index 2 = /first)
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "fzf"), []byte("#!/bin/sh\ntail -n 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if r := runQ("again", "--pick"); r.code != 0 || gotPath != "/first" {
		t.Fatalf("%+v path=%q", r, gotPath)
	}
	t.Setenv("PATH", t.TempDir()) // no fzf at all
	if r := runQ("again", "--pick"); r.code != 1 || !strings.Contains(r.err, "needs fzf") {
		t.Fatalf("%+v", r)
	}
}

// The history file is shared by every project, so a request belongs to the
// server it went to: the default list, `again` and completion never mix them,
// and an entry for another server (or from before servers were recorded) is
// refused instead of being sent to the wrong API.
func TestHistoryIsScopedToTheServer(t *testing.T) {
	var hitA, hitB []string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA = append(hitA, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB = append(hitB, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	t.Setenv("API_GRAPHQL_URL", "")
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_ENV", "")
	_ = os.MkdirAll(filepath.Join(state, "api"), 0o700)
	_ = os.WriteFile(filepath.Join(state, "api", "history"), []byte("GET\t/legacy\t\n"), 0o600)
	runOn := func(base string, args ...string) result {
		t.Helper()
		defer leaveSourceTree(t)()
		t.Setenv("API_BASE_URL", base)
		var out, errb bytes.Buffer
		code := Run(context.Background(), args, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
		return result{code, out.String(), errb.String()}
	}

	runOn(a.URL, "get", "/only-a")
	runOn(b.URL, "get", "/only-b")

	r := runOn(b.URL, "history")
	if !strings.Contains(r.out, "/only-b") || strings.Contains(r.out, "/only-a") || strings.Contains(r.out, "/legacy") {
		t.Fatalf("history must list the current server only: %+v", r)
	}
	if !strings.Contains(r.err, "api history -a") {
		t.Fatalf("it must say the rest exists: %+v", r)
	}
	r = runOn(b.URL, "history", "-a")
	for _, want := range []string{"/only-a", "/only-b", "/legacy", "(no server)", a.URL, b.URL} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("history -a lacks %q:\n%s", want, r.out)
		}
	}

	// the numbers of `again` are the numbers `history` shows
	hitB = nil
	if r := runOn(b.URL, "again", "1", "-c"); r.code != 0 || len(hitB) != 1 || hitB[0] != "/only-b" {
		t.Fatalf("again 1: %+v hit=%v", r, hitB)
	}
	// --all numbering: 1 = /only-b (just run), 2 = /only-a, 3 = /legacy
	hitB = nil
	if r := runOn(b.URL, "again", "--all", "2"); r.code != 1 || !strings.Contains(r.err, a.URL) || len(hitB) != 0 {
		t.Fatalf("another server's entry must be refused, not sent: %+v hit=%v", r, hitB)
	}
	if r := runOn(b.URL, "again", "--all", "3"); r.code != 1 || !strings.Contains(r.err, "without a server") || len(hitB) != 0 {
		t.Fatalf("a legacy entry must be refused: %+v", r)
	}
	// and from the first server the other way round
	hitA = nil
	if r := runOn(a.URL, "again", "1", "-c"); r.code != 0 || len(hitA) != 1 || hitA[0] != "/only-a" {
		t.Fatalf("on A, 1 is A's request: %+v hit=%v", r, hitA)
	}
	// completion offers this server's paths only
	if out := complete(t, "get", "/o"); strings.Contains(out, "/only-b") || !strings.Contains(out, "/only-a") {
		t.Fatalf("completion on A:\n%s", out)
	}
}
