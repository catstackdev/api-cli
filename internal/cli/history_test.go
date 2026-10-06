package cli

import (
	"bytes"
	"context"
	"net/http"
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
