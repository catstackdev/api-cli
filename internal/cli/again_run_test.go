package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cybercat/api/internal/httpfile"
)

// historyFile reads the raw history file of a shared state dir.
func historyFile(t *testing.T, state string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(state, "api", "history"))
	return string(data)
}

// A picked gql query is kept as a file and the history points at it: the query
// text itself is never in the history, and `again` runs the file again.
func TestPickedGQLIsKeptAndAgainRunsItAgain(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	dir := projectWith(t, map[string]string{".env": "A=1\n"})
	t.Setenv("EDITOR_TOUCH", "1")

	if r := runShared(t, state, "", "gql", "-e", "user", "--item", "query me"); r.code != 0 || s.ran("query Me") != 1 {
		t.Fatalf("%+v", r)
	}
	file := filepath.Join(dir, ".api", "requests", "user-me.http")
	if orig, err := os.ReadFile(file + ".orig"); err != nil || !strings.Contains(string(orig), "query Me") {
		t.Fatalf("the generated skeleton is kept for --default: %v\n%s", err, orig)
	}
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), "POST ") || !strings.Contains(string(raw), "query Me") {
		t.Fatalf("the query is kept as a request file: %v\n%s", err, raw)
	}
	if !strings.Contains(string(raw), "POST /graphql/v2/user") || strings.Contains(string(raw), "127.0.0.1") {
		t.Fatalf("the kept file is relative to the active server, like an openapi pick:\n%s", raw)
	}
	h := historyFile(t, state)
	if !strings.Contains(h, "RUN\tuser-me\t") || strings.Contains(h, "query") || strings.Contains(h, "Me {") {
		t.Fatalf("history holds a pointer, never the query:\n%s", h)
	}
	if r := runShared(t, state, "", "history"); !strings.Contains(r.out, "RUN") || !strings.Contains(r.out, "user-me") {
		t.Fatalf("%+v", r)
	}

	if r := runShared(t, state, "", "again"); r.code != 0 || s.ran("query Me") != 2 || !strings.Contains(r.err, ".api/requests/user-me.http") {
		t.Fatalf("again: %+v bodies=%v", r, s.bodies)
	}
	// the token is the current one, never a stored one
	if got := s.auths[len(s.auths)-1]; got != "Bearer TOK-0123456789-SECRETTAIL" {
		t.Fatalf("auth %q", got)
	}
	// --no-save: nothing kept, nothing to repeat
	n := len(strings.Split(strings.TrimSpace(historyFile(t, state)), "\n"))
	if r := runShared(t, state, "", "gql", "-e", "user", "--item", "query user", "--no-save"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if got := len(strings.Split(strings.TrimSpace(historyFile(t, state)), "\n")); got != n {
		t.Fatalf("--no-save must not record: %d -> %d", n, got)
	}
}

// What is in the file NOW decides whether to ask, like the pick that made it.
func TestAgainAsksBeforeRepeatingAMutationAndEditRunsACopy(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	dir := projectWith(t, map[string]string{".env": "A=1\n"})
	t.Setenv("EDITOR_TOUCH", "1")
	if r := runShared(t, state, "y\n", "gql", "-e", "user", "--item", "mutation login"); r.code != 0 || s.ran("mutation Login") != 1 {
		t.Fatalf("%+v", r)
	}
	// the variables hold a password-looking name: they are not in the kept file
	raw, _ := os.ReadFile(filepath.Join(dir, ".api", "requests", "user-login.http"))
	if !strings.Contains(string(raw), "left out") || strings.Contains(string(raw), "password") {
		t.Fatalf("secret-looking variables must not be kept:\n%s", raw)
	}

	r := runShared(t, state, "n\n", "again")
	if r.code != exitGeneric || !strings.Contains(r.err, "changes data") || s.ran("mutation Login") != 1 {
		t.Fatalf("a mutation must be confirmed again: %+v", r)
	}
	if r := runShared(t, state, "", "again"); s.ran("mutation Login") != 1 { // no answer = no
		t.Fatalf("an empty answer must mean no: %+v", r)
	}
	if r := runShared(t, state, "", "again", "--yes"); r.code != 0 || s.ran("mutation Login") != 2 {
		t.Fatalf("--yes: %+v", r)
	}

	// --edit asks about what the edited text now contains, and keeps only an edit that was run
	before, _ := os.ReadFile(filepath.Join(dir, ".api", "requests", "user-login.http"))
	edited := filepath.Join(t.TempDir(), "edited.http")
	_ = os.WriteFile(edited, []byte("### x\nPOST /graphql/v2/user\n\n{\"query\":\"query Edited { me { id } }\"}\n"), 0o600)
	t.Setenv("EDITOR_REPLACE", edited)
	if r := runShared(t, state, "", "again", "--edit"); r.code != 0 || s.ran("query Edited") != 1 {
		t.Fatalf("edit: %+v bodies=%v", r, s.bodies)
	}
	_ = os.WriteFile(edited, []byte("### x\nPOST /graphql/v2/user\n\n{\"query\":\"mutation Sneaky { logout }\"}\n"), 0o600)
	if r := runShared(t, state, "n\n", "again", "--edit"); r.code == 0 || s.ran("Sneaky") != 0 || !strings.Contains(r.err, "changes data") {
		t.Fatalf("an edited-in mutation ran without asking: %+v", r)
	}
	// the declined mutation was NOT run, so it was not kept either; the one that ran was
	after, _ := os.ReadFile(filepath.Join(dir, ".api", "requests", "user-login.http"))
	if strings.Contains(string(after), "Sneaky") {
		t.Fatalf("an edit that was declined must not be kept:\n%s", after)
	}
	if !strings.Contains(string(after), "query Edited") || !strings.Contains(string(before), "mutation Login") {
		t.Fatalf("the edit that ran is the file now:\n%s", after)
	}
	// quitting the editor without saving cancels
	t.Setenv("EDITOR_REPLACE", "")
	t.Setenv("EDITOR_TOUCH", "")
	if r := runShared(t, state, "", "again", "--edit"); r.code == 0 || !strings.Contains(r.err, "cancelled") {
		t.Fatalf("%+v", r)
	}
}

// A pointer means something only in the project and for the server it was made
// for; a plain GET has nothing to edit.
func TestAgainRefusesAPickFromAnotherProjectOrServer(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	dir := projectWith(t, map[string]string{".env": "A=1\n"})
	t.Setenv("EDITOR_TOUCH", "1")
	srv := os.Getenv("API_BASE_URL")
	runShared(t, state, "", "gql", "-e", "user", "--item", "query me")
	n := s.ran("query Me")

	// another project that has a file of the same name
	other := projectWith(t, map[string]string{".env": "A=1\n"})
	_ = os.MkdirAll(filepath.Join(other, ".api", "requests"), 0o700)
	_ = os.WriteFile(filepath.Join(other, ".api", "requests", "user-me.http"), []byte("GET /evil\n"), 0o600)
	if r := runShared(t, state, "", "again", "--all"); r.code != exitGeneric || !strings.Contains(r.err, "belongs to the project") || !strings.Contains(r.err, filepath.Base(dir)) || s.ran("query Me") != n {
		t.Fatalf("another project's file must never run: %+v", r)
	}
	if got := s.paths[len(s.paths)-1]; got == "/evil" {
		t.Fatalf("ran the other project's file")
	}

	// the right project, another server
	_ = os.Chdir(dir)
	t.Setenv("API_BASE_URL", "http://other.example")
	if r := runShared(t, state, "", "again", "--all"); r.code != exitGeneric || !strings.Contains(r.err, srv) || s.ran("query Me") != n {
		t.Fatalf("another server: %+v", r)
	}
	t.Setenv("API_BASE_URL", srv)

	// the file is gone
	_ = os.Remove(filepath.Join(dir, ".api", "requests", "user-me.http"))
	if r := runShared(t, state, "", "again"); r.code != exitGeneric || !strings.Contains(r.err, "is gone") {
		t.Fatalf("%+v", r)
	}
}

func TestPickedOpenAPIRequestIsRepeatable(t *testing.T) {
	p := pickSetup(t, "2") // GET /users
	t.Setenv("EDITOR_TOUCH", "1")
	state := t.TempDir()
	if r := runShared(t, state, "", "openapi", "-M"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if h := historyFile(t, state); !strings.Contains(h, "RUN\tget-users\t") {
		t.Fatalf("the saved pick is remembered:\n%s", h)
	}
	before := len(p.srv.paths)
	if r := runShared(t, state, "", "again", "-M"); r.code != 0 || len(p.srv.paths) != before+1 || p.srv.paths[before] != "/users" {
		t.Fatalf("%+v", r)
	}
}

func TestSavedRequestPathIsBuiltFromANameOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".api", "requests")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "ok.http"), []byte("GET /x\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "outside.http"), []byte("GET /x\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".hidden.http"), []byte("GET /x\n"), 0o600)
	_ = os.Symlink(filepath.Join(root, "outside.http"), filepath.Join(dir, "link.http"))
	if p, err := savedRequestPath(root, "ok"); err != nil || p != filepath.Join(dir, "ok.http") {
		t.Fatalf("%v %v", p, err)
	}
	for _, bad := range []string{"../outside", "..", ".hidden", "a/b", "", "link", "nope", "/etc/passwd"} {
		if p, err := savedRequestPath(root, bad); err == nil {
			t.Errorf("%q must be refused, got %s", bad, p)
		}
	}
}

func TestRequestIsReadOnlyJudgesTheTextThatWillBeSent(t *testing.T) {
	for _, tc := range []struct {
		method, body string
		want         bool
	}{
		{"GET", "", true},
		{"POST", `{"query":"query Me { me { id } }"}`, true},
		{"POST", `{"query":"mutation Login { login }"}`, false},
		{"POST", `{"query":"{{q}}"}`, false}, // could expand to anything
		{"POST", `{"name":"x"}`, false},
		{"DELETE", "", false},
	} {
		r := &httpfile.Request{Method: tc.method, Body: tc.body}
		if got := requestIsReadOnly(r); got != tc.want {
			t.Errorf("%s %s: got %v", tc.method, tc.body, got)
		}
	}
}

// {{ in a kept file would be expanded by `api run` (a variable, $env, a captured
// response), so a query that contains one is run but never saved or remembered.
func TestGQLPickWithTemplateSyntaxIsNotKept(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	dir := projectWith(t, map[string]string{".env": "A=1\n"})
	edited := filepath.Join(t.TempDir(), "edited.graphql")
	_ = os.WriteFile(edited, []byte("query U($id: ID!) { user(id: $id) { id } }\n### variables\n{\"id\": \"{{$env API_TOKEN}}\"}\n"), 0o600)
	t.Setenv("EDITOR_REPLACE", edited)

	r := runShared(t, state, "", "gql", "-e", "user", "--item", "query user")
	if r.code != 0 || s.ran("query U(") != 1 {
		t.Fatalf("the query itself still runs: %+v", r)
	}
	if !strings.Contains(r.err, "not kept") {
		t.Fatalf("it must say so:\n%s", r.err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".api", "requests", "*")); len(files) != 0 {
		t.Fatalf("a template-looking query must not be saved: %v", files)
	}
	if h := historyFile(t, state); strings.Contains(h, "RUN") {
		t.Fatalf("nothing saved, so nothing to remember:\n%s", h)
	}
}

// A pick that fails (a 400 for a body you still have to fix) is kept and
// remembered, and every edit you save is kept too, worked or not: fixing a
// request takes a few tries and the next one starts from the last.
func TestFailedPickCanBeRetriedAndEditedUntilItWorks(t *testing.T) {
	p := pickSetup(t, "2") // GET /users
	state := t.TempDir()
	replaceWith(t, "### x\nGET /does-not-exist\n")
	r := runShared(t, state, "", "openapi", "-M")
	if r.code == 0 || !strings.Contains(r.err, "HTTP 404") || !strings.Contains(r.err, "api again --edit") {
		t.Fatalf("the pick should fail and say how to retry: %+v", r)
	}
	if h := historyFile(t, state); !strings.Contains(h, "RUN\t") {
		t.Fatalf("a failed pick is still remembered:\n%s", h)
	}
	file := p.saved(t)[0]
	first, _ := os.ReadFile(file)
	if r := runShared(t, state, "", "again", "-M"); r.code == 0 {
		t.Fatalf("a plain retry runs the same request: %+v", r)
	}

	// a first attempt at a fix that is still wrong: it must NOT be lost
	replaceWith(t, "### x\nGET /still-wrong\n")
	if r := runShared(t, state, "", "again", "--edit", "-M"); r.code == 0 || !strings.Contains(r.err, "kept your edit") {
		t.Fatalf("%+v", r)
	}
	if got, _ := os.ReadFile(file); !strings.Contains(string(got), "/still-wrong") {
		t.Fatalf("the edit was thrown away because the run failed:\n%s", got)
	}
	if bak, _ := os.ReadFile(file + ".bak"); string(bak) != string(first) {
		t.Fatalf("the previous version is kept once as .bak:\n%s", bak)
	}
	// the next --edit starts from the last attempt, not from the original
	t.Setenv("EDITOR_REPLACE", "")
	t.Setenv("EDITOR_TOUCH", "1")
	runShared(t, state, "", "again", "--edit", "-M")
	if given, _ := os.ReadFile(p.rec); !strings.Contains(string(given), "/still-wrong") {
		t.Fatalf("the editor was given the old file:\n%s", given)
	}

	// and the one that works
	replaceWith(t, "### x\nGET /users\n")
	n := len(p.srv.paths)
	if r := runShared(t, state, "", "again", "--edit", "-M"); r.code != 0 || p.srv.paths[len(p.srv.paths)-1] != "/users" || len(p.srv.paths) != n+1 {
		t.Fatalf("edit then run: %+v", r)
	}
	n = len(p.srv.paths)
	if r := runShared(t, state, "", "again", "-M"); r.code != 0 || len(p.srv.paths) != n+1 || p.srv.paths[len(p.srv.paths)-1] != "/users" {
		t.Fatalf("again now repeats the fixed one: %+v", r)
	}
	if len(p.saved(t)) != 1 {
		t.Fatalf("one file, edited in place: %v", p.saved(t))
	}
	// quitting the editor without saving changes nothing
	t.Setenv("EDITOR_REPLACE", "")
	t.Setenv("EDITOR_TOUCH", "")
	before, _ := os.ReadFile(file)
	if r := runShared(t, state, "", "again", "--edit", "-M"); r.code == 0 {
		t.Fatalf("%+v", r)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("an unsaved edit must change nothing")
	}
}

// --default starts the edit from the skeleton the pick generated, not from the
// last version, and the version it replaces is still recoverable.
func TestAgainDefaultReopensTheGeneratedSkeleton(t *testing.T) {
	p := pickSetup(t, "2") // GET /users
	state := t.TempDir()
	replaceWith(t, "### x\nGET /users?limit=5\n") // what you edited at pick time
	if r := runShared(t, state, "", "openapi", "-M"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	file := p.saved(t)[0]
	if orig, err := os.ReadFile(file + ".orig"); err != nil || !strings.Contains(string(orig), "GET /users") || strings.Contains(string(orig), "limit=5") || strings.Contains(string(orig), "openapi-pick:") {
		t.Fatalf("the generated skeleton is kept beside the file: %v\n%s", err, orig)
	}
	yours, _ := os.ReadFile(file)
	if !strings.Contains(string(yours), "limit=5") {
		t.Fatalf("the saved file is the one you edited:\n%s", yours)
	}

	t.Setenv("EDITOR_REPLACE", "")
	t.Setenv("EDITOR_TOUCH", "1") // save unchanged
	n := len(p.srv.paths)
	if r := runShared(t, state, "", "again", "--default", "-M"); r.code != 0 || len(p.srv.paths) != n+1 || p.srv.paths[n] != "/users" {
		t.Fatalf("%+v paths=%v", r, p.srv.paths)
	}
	if given, _ := os.ReadFile(p.rec); strings.Contains(string(given), "limit=5") || !strings.Contains(string(given), "GET /users") {
		t.Fatalf("the editor must open the skeleton, not your version:\n%s", given)
	}
	if now, _ := os.ReadFile(file); strings.Contains(string(now), "limit=5") {
		t.Fatalf("the skeleton run is now the saved file:\n%s", now)
	}
	if bak, _ := os.ReadFile(file + ".bak"); !strings.Contains(string(bak), "limit=5") {
		t.Fatalf("your previous version is recoverable:\n%s", bak)
	}
	// quitting the editor without saving changes nothing
	t.Setenv("EDITOR_TOUCH", "")
	before, _ := os.ReadFile(file)
	if r := runShared(t, state, "", "again", "--default", "-M"); r.code == 0 {
		t.Fatalf("%+v", r)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("an unsaved --default must change nothing")
	}
	// a pick saved before .orig existed says so
	_ = os.Remove(file + ".orig")
	if r := runShared(t, state, "", "again", "--default", "-M"); r.code != exitGeneric || !strings.Contains(r.err, "no original skeleton") {
		t.Fatalf("%+v", r)
	}
}

// --edit / --default only apply to saved requests, so `again --pick` with either
// offers just those (a plain GET in the list would end in an error after the
// choice), and the number it resolves to is still the one `api history` shows.
func TestAgainPickWithEditOffersOnlySavedRequests(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	projectWith(t, map[string]string{".env": "A=1\n"})
	t.Setenv("EDITOR_TOUCH", "1")
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "fzf"), []byte("#!/bin/sh\nhead -n 1\n"), 0o755) // the first line offered
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	runShared(t, state, "", "get", "/older")
	if r := runShared(t, state, "", "again", "--pick", "--edit"); r.code != exitGeneric || !strings.Contains(r.err, "no saved requests") {
		t.Fatalf("nothing saved yet: %+v", r)
	}
	runShared(t, state, "", "gql", "-e", "user", "--item", "query me")
	runShared(t, state, "", "get", "/ping") // newest: #1 is a plain GET, the saved request is #2
	n := s.ran("query Me")

	if r := runShared(t, state, "", "again", "--pick"); r.code != 0 || s.ran("query Me") != n {
		t.Fatalf("without --edit the first line is the GET: %+v", r)
	}
	if r := runShared(t, state, "", "again", "--pick", "--edit"); r.code != 0 || s.ran("query Me") != n+1 {
		t.Fatalf("--edit must offer the saved request, not the GET: %+v", r)
	}
	if r := runShared(t, state, "", "again", "--pick", "--default"); r.code != 0 || s.ran("query Me") != n+2 {
		t.Fatalf("--default too: %+v", r)
	}
}

// --runs lists the saved requests only, numbered as in the full list so the
// number works with `again N`.
func TestHistoryRunsKeepsTheFullListNumbers(t *testing.T) {
	pickEnv(t)
	state := t.TempDir()
	projectWith(t, map[string]string{".env": "A=1\n"})
	t.Setenv("EDITOR_TOUCH", "1")
	runShared(t, state, "", "get", "/older")
	if r := runShared(t, state, "", "history", "--runs"); !strings.Contains(r.err, "no saved requests") {
		t.Fatalf("%+v", r)
	}
	runShared(t, state, "", "gql", "-e", "user", "--item", "query me")
	runShared(t, state, "", "get", "/ping")
	r := runShared(t, state, "", "history", "--runs")
	if r.code != 0 || strings.Contains(r.out, "/ping") || !strings.Contains(r.out, "user-me") {
		t.Fatalf("only the saved request: %+v", r)
	}
	if f := strings.Fields(strings.Split(strings.TrimSpace(r.out), "\n")[len(strings.Split(strings.TrimSpace(r.out), "\n"))-1]); len(f) == 0 || f[0] != "2" {
		t.Fatalf("the number is the full list's (2), not 1:\n%s", r.out)
	}
}
