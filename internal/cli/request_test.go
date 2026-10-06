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
	"time"
)

func TestGetFilterAndAuth(t *testing.T) {
	var gotAuth string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":7},{"id":8}],"z":1,"a":2}`))
	})
	t.Setenv("API_TOKEN", "tok-123")

	r := run(t, "", "get", "/users", ".data | length")
	if r.code != 0 || strings.TrimSpace(r.out) != "2" {
		t.Fatalf("filter: code=%d out=%q err=%q", r.code, r.out, r.err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("auth header = %q", gotAuth)
	}

	// identity keeps the server's key order (z before a)
	r = run(t, "", "get", "/users")
	if strings.Index(r.out, `"z"`) > strings.Index(r.out, `"a"`) {
		t.Fatalf("key order not preserved:\n%s", r.out)
	}
}

func TestDotSlashArgIsNotAFilter(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	out := filepath.Join(t.TempDir(), "out.json")
	r := run(t, "", "get", "/x", "-o", out)
	if r.code != 0 {
		t.Fatalf("code=%d err=%q", r.code, r.err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("file = %q", b)
	}
	if !isFilter(".data") || isFilter("./out") || isFilter("../out") || isFilter("out") {
		t.Fatal("isFilter classification wrong")
	}
}

func TestPostBodyArgStdinAndFile(t *testing.T) {
	var got, ctype string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		got, ctype = b.String(), r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if r := run(t, "", "post", "/u", `{"a":1}`, ".ok"); r.code != 0 || got != `{"a":1}` || ctype != "application/json" {
		t.Fatalf("arg: %+v got=%q ctype=%q", r, got, ctype)
	}
	if r := run(t, `{"s":2}`+"\n", "post", "/u", ".ok"); r.code != 0 || got != `{"s":2}` {
		t.Fatalf("stdin: %+v got=%q", r, got)
	}
	file := filepath.Join(t.TempDir(), "b.json")
	_ = os.WriteFile(file, []byte(`{"f":3}`), 0o600)
	if r := run(t, "", "post", "/u", "@"+file); r.code != 0 || got != `{"f":3}` {
		t.Fatalf("@file: %+v got=%q", r, got)
	}
	// an explicit body wins over stdin
	if r := run(t, `{"ignored":1}`, "post", "/u", `{"arg":1}`); r.code != 0 || got != `{"arg":1}` {
		t.Fatalf("arg vs stdin: %+v got=%q", r, got)
	}
}

func TestHTTPErrorPrintsBodyAndExits22(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	})
	r := run(t, "", "get", "/missing")
	if r.code != 22 || !strings.Contains(r.out, "nope") || !strings.Contains(r.err, "HTTP 404") {
		t.Fatalf("%+v", r)
	}
}

func TestNonJSONBodyPrintedAsIs(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>boom</html>")) })
	r := run(t, "", "get", "/x", ".data")
	if r.code != 0 || strings.TrimSpace(r.out) != "<html>boom</html>" {
		t.Fatalf("%+v", r)
	}
}

func TestBadFilterPrintsRawAndExits5(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) })
	r := run(t, "", "get", "/x", ".[")
	if r.code != 5 || !strings.Contains(r.out, `{"a":1}`) {
		t.Fatalf("%+v", r)
	}
}

func TestRawOutputCompactAndRawBody(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"cat","n":12345678901234567890}`))
	})
	if r := run(t, "", "get", "/x", "-r", ".name"); strings.TrimSpace(r.out) != "cat" {
		t.Fatalf("-r: %q", r.out)
	}
	if r := run(t, "", "get", "/x", "-c"); strings.TrimSpace(r.out) != `{"name":"cat","n":12345678901234567890}` {
		t.Fatalf("-c: %q", r.out)
	}
	if r := run(t, "", "get", "/x", "-R", ".name"); !strings.Contains(r.out, `"n":12345678901234567890`) {
		t.Fatalf("-R: %q", r.out)
	}
	// big integers survive a non-identity filter
	if r := run(t, "", "get", "/x", ".n"); strings.TrimSpace(r.out) != "12345678901234567890" {
		t.Fatalf("bigint: %q", r.out)
	}
}

func TestLoginSendsNoBearerAndEmitsQuotedExport(t *testing.T) {
	var gotAuth string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"token":"abcdefghijkl-it's$(evil) \"x\""}}`))
	})
	t.Setenv("API_TOKEN", "STALE-TOKEN")

	r := run(t, "", "login", "/auth", `{"u":"x"}`, "--emit")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if gotAuth != "" {
		t.Fatalf("login sent a bearer token: %q", gotAuth)
	}
	want := `export API_TOKEN='abcdefghijkl-it'\''s$(evil) "x"'`
	if strings.TrimSpace(r.out) != want {
		t.Fatalf("export line\n got: %q\nwant: %q", strings.TrimSpace(r.out), want)
	}
	if strings.Contains(r.err, "evil") {
		t.Fatalf("token leaked unmasked to stderr: %q", r.err)
	}
}

func TestLoginTokenNotFound(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":{}}`)) })
	r := run(t, "", "login", "/auth", `{}`, ".data.token", "--emit")
	if r.code != 1 || !strings.Contains(r.err, "token not found at: .data.token") || r.out != "" {
		t.Fatalf("%+v", r)
	}
}

func TestGraphQL(t *testing.T) {
	var path, body string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		path, body = r.URL.Path, b.String()
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"1"}},"errors":[{"message":"partial"}]}`))
	})
	t.Setenv("API_GRAPHQL_URL", os.Getenv("API_BASE_URL")+"/graphql")

	r := run(t, "", "gql", "-e", "v2/user", "query($id: ID!){me(id:$id){id}}", "-V", "id=1", ".data.me.id")
	if r.code != 0 || strings.TrimSpace(r.out) != `"1"` {
		t.Fatalf("%+v", r)
	}
	if path != "/graphql/v2/user" {
		t.Fatalf("path = %q", path)
	}
	if !strings.Contains(body, `"variables":{"id":"1"}`) || !strings.Contains(body, `"query":"query($id: ID!){me(id:$id){id}}"`) {
		t.Fatalf("body = %s", body)
	}

	// query from stdin when the first argument is a filter
	if r := run(t, "{ __typename }", "gql", ".data"); r.code != 0 || !strings.Contains(body, "__typename") {
		t.Fatalf("stdin query: %+v body=%s", r, body)
	}
	// --strict turns GraphQL errors into exit 4
	if r := run(t, "", "gql", "{ me }", "--strict"); r.code != 4 || !strings.Contains(r.err, "partial") {
		t.Fatalf("strict: %+v", r)
	}
	// without it the exit code stays 0
	if r := run(t, "", "gql", "{ me }"); r.code != 0 {
		t.Fatalf("non-strict: %+v", r)
	}
}

func TestGraphQLTarget(t *testing.T) {
	cases := []struct{ base, ep, want string }{
		{"", "", "/graphql"},
		{"https://h/graphql", "", "https://h/graphql"},
		{"https://h/graphql", "v2/user", "https://h/graphql/v2/user"},
		{"https://h/graphql/", "/v2/user", "https://h/graphql/v2/user"},
		{"https://h/graphql", "https://other/x", "https://other/x"},
	}
	for _, c := range cases {
		if got := gqlTarget(c.base, c.ep); got != c.want {
			t.Errorf("gqlTarget(%q,%q) = %q, want %q", c.base, c.ep, got, c.want)
		}
	}
}

func TestFormMultipartAndUrlencoded(t *testing.T) {
	var ctype, field, fname string
	var urlenc string
	server(t, func(w http.ResponseWriter, r *http.Request) {
		ctype = r.Header.Get("Content-Type")
		if strings.HasPrefix(ctype, "multipart/") {
			_ = r.ParseMultipartForm(1 << 20)
			field = r.FormValue("type")
			if f, h, err := r.FormFile("file"); err == nil {
				f.Close()
				fname = h.Filename
			}
		} else {
			b := new(bytes.Buffer)
			_, _ = b.ReadFrom(r.Body)
			urlenc = b.String()
		}
		_, _ = w.Write([]byte(`{}`))
	})
	file := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(file, []byte("bytes"), 0o600)

	if r := run(t, "", "form", "/up", "-F", "file=@"+file, "-F", "type=avatar"); r.code != 0 || field != "avatar" || fname != "photo.jpg" {
		t.Fatalf("multipart: %+v field=%q fname=%q", r, field, fname)
	}
	if r := run(t, "", "form", "/login", "-d", "a=1", "-d", "b=2"); r.code != 0 || urlenc != "a=1&b=2" || ctype != "application/x-www-form-urlencoded" {
		t.Fatalf("urlencoded: %+v body=%q ctype=%q", r, urlenc, ctype)
	}
	if r := run(t, "", "form", "/x"); r.code == 0 {
		t.Fatal("form with no fields should fail")
	}
}

func TestDryRunMasksTokenAndMatchesRequest(t *testing.T) {
	t.Setenv("API_BASE_URL", "https://api.test")
	t.Setenv("API_TOKEN", "abcdefghijklmnopqrstuvwxyz")
	r := run(t, "", "post", "/u", `{"a":"it's"}`, "--dry", "-H", "X-Debug: 1", "-Q", "limit=5")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"curl -sS --fail-with-body --connect-timeout 10 --max-time 300 -X POST",
		"'https://api.test/u?limit=5'",
		"'Authorization: Bearer abcdefghijkl…'",
		"'X-Debug: 1'",
		`'{"a":"it'\''s"}'`,
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("dry output missing %q:\n%s", want, r.out)
		}
	}
	if strings.Contains(r.out, "mnopqrstuvwxyz") {
		t.Fatal("full token printed")
	}
}

func TestSummaryFooterIsOptInAndOnStderr(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if r := run(t, "", "get", "/x"); strings.Contains(r.err, "─── api") {
		t.Fatalf("summary printed by default: %q", r.err)
	}
	r := run(t, "", "get", "/x", "--summary")
	if !strings.Contains(r.err, "status : 200") || !strings.Contains(r.err, "type   : application/json") || strings.Contains(r.out, "─── api") {
		t.Fatalf("%+v", r)
	}
}

func TestColorFlagsAndEnv(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) })
	t.Setenv("NO_COLOR", "")
	t.Setenv("API_COLOR", "")
	t.Setenv("JQ_COLORS", "")
	t.Setenv("GOJQ_COLORS", "")
	has := func(r result) bool { return strings.Contains(r.out, "\x1b[") }

	if has(run(t, "", "get", "/x")) {
		t.Fatal("auto must not colour a non-terminal")
	}
	if !has(run(t, "", "get", "/x", "-C")) || !has(run(t, "", "get", "/x", "--color", "always")) {
		t.Fatal("-C / --color always must colour")
	}
	if has(run(t, "", "get", "/x", "-C", "-M")) {
		t.Fatal("-M must win over -C")
	}
	t.Setenv("API_COLOR", "always")
	if !has(run(t, "", "get", "/x")) {
		t.Fatal("API_COLOR=always must colour")
	}
	if has(run(t, "", "get", "/x", "--color", "never")) {
		t.Fatal("--color never must win over API_COLOR")
	}
	t.Setenv("API_COLOR", "")
	t.Setenv("JQ_COLORS", "0;90:0;31:0;32:0;33:0;34:1;35:1;36:4;37")
	if r := run(t, "", "get", "/x", "-C", "-c"); !strings.Contains(r.out, "\x1b[4;37m\"a\"") {
		t.Fatalf("JQ_COLORS not applied: %q", r.out)
	}
}

func TestStderrMessagesAndStatusLineAreStyledOnlyWhenColourIsOn(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"e":1}`))
	})
	t.Setenv("API_COLOR", "")
	t.Setenv("NO_COLOR", "")

	plain := run(t, "", "get", "/x", "-i")
	if strings.Contains(plain.err, "\x1b[") || strings.Contains(plain.out, "\x1b[") {
		t.Fatalf("colour leaked into a non-terminal: %+v", plain)
	}

	r := run(t, "", "get", "/x", "-i", "-C")
	if !strings.Contains(r.err, "\x1b[31m✗ HTTP 404") {
		t.Fatalf("error line not red on stderr: %q", r.err)
	}
	if !strings.Contains(r.out, "\x1b[31m404\x1b[0m") { // status code coloured by class
		t.Fatalf("status line not coloured: %q", r.out)
	}
	if !strings.Contains(r.out, "\x1b[36mContent-Length\x1b[0m") && !strings.Contains(r.out, "\x1b[36mDate\x1b[0m") {
		t.Fatalf("header names not coloured: %q", r.out)
	}

	// -M beats -C and is honoured on stderr too
	if r := run(t, "", "get", "/x", "-C", "-M"); strings.Contains(r.err, "\x1b[") || strings.Contains(r.out, "\x1b[") {
		t.Fatalf("-M ignored: %+v", r)
	}
	// the global flags work before and after the subcommand
	if r := run(t, "", "-C", "get", "/x"); !strings.Contains(r.err, "\x1b[31m") {
		t.Fatalf("-C before the subcommand ignored: %q", r.err)
	}
	// --no-pager is accepted everywhere
	if r := run(t, "", "get", "/x", "--no-pager"); r.code != 22 {
		t.Fatalf("--no-pager: %+v", r)
	}
}

func TestMissingBaseURL(t *testing.T) {
	t.Setenv("API_BASE_URL", "")
	r := run(t, "", "get", "/x")
	if r.code == 0 || !strings.Contains(r.err, "API_BASE_URL not set") {
		t.Fatalf("%+v", r)
	}
	// a full URL does not need a base
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	if r := run(t, "", "get", srv.URL+"/x"); r.code != 0 {
		t.Fatalf("full url: %+v", r)
	}
}

func TestTimeoutExits28(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(500 * time.Millisecond) })
	r := run(t, "", "get", "/slow", "--timeout", "100ms")
	if r.code != 28 {
		t.Fatalf("%+v", r)
	}
}

func TestConnectionRefusedExits7(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	t.Setenv("API_BASE_URL", url)
	if r := run(t, "", "get", "/x"); r.code != 7 {
		t.Fatalf("%+v", r)
	}
}

func TestRetryOn503(t *testing.T) {
	calls := 0
	server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r := run(t, "", "get", "/x", "--retry", "3")
	if r.code != 0 || calls != 3 {
		t.Fatalf("%+v calls=%d", r, calls)
	}
}

func TestIncludeAndHeadersCommand(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Thing", "42")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if r := run(t, "", "get", "/x", "-i"); !strings.Contains(r.out, "X-Thing: 42") || !strings.Contains(r.out, `"ok": true`) {
		t.Fatalf("-i: %q", r.out)
	}
	if r := run(t, "", "headers", "/x"); !strings.Contains(r.out, "X-Thing: 42") || strings.Contains(r.out, `"ok"`) {
		t.Fatalf("headers: %q", r.out)
	}
}

func TestJSONCommand(t *testing.T) {
	r := run(t, "", "json", "name=cat", "age:=30", "ok:=true", `tags:=["a","b"]`, "user.id:=1", "user.name=x", "-c")
	want := `{"name":"cat","age":30,"ok":true,"tags":["a","b"],"user":{"id":1,"name":"x"}}`
	if strings.TrimSpace(r.out) != want {
		t.Fatalf("got %q want %q", strings.TrimSpace(r.out), want)
	}
	if r := run(t, "", "json", "a.b=1", "--flat", "-c"); strings.TrimSpace(r.out) != `{"a.b":"1"}` {
		t.Fatalf("flat: %q", r.out)
	}
	if r := run(t, "", "json", "bad:=not-json"); r.code == 0 {
		t.Fatal("invalid raw json accepted")
	}
}

func TestEnvLoadEmitQuotesAndRequiresEmit(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.staging"), []byte("API_BASE_URL=https://s\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("X=1\n"), 0o600)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	r := run(t, "", "env", "load", "staging", "--emit")
	if r.code != 0 || !strings.Contains(r.out, "set -o allexport; source '") || !strings.Contains(r.out, ".env.staging'") ||
		!strings.Contains(r.out, "export API_ENV='staging'") {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "env", "load", "staging"); r.code != 2 {
		t.Fatalf("without --emit: %+v", r)
	}
	if r := run(t, "", "env", "load", "nope", "--emit"); r.code != 1 || !strings.Contains(r.err, "not found") {
		t.Fatalf("missing: %+v", r)
	}
	if r := run(t, "", "env", "list"); strings.TrimSpace(r.out) != "staging" {
		t.Fatalf("list: %q", r.out)
	}
	sub := filepath.Join(dir, "a", "b")
	_ = os.MkdirAll(sub, 0o700)
	_ = os.Chdir(sub)
	gotRoot, _ := filepath.EvalSymlinks(strings.TrimSpace(run(t, "", "env", "root").out))
	wantRoot, _ := filepath.EvalSymlinks(dir)
	if gotRoot != wantRoot {
		t.Fatalf("env root from a subdir: got %q want %q", gotRoot, wantRoot)
	}
	_ = os.Chdir(dir)
	if r := run(t, "", "env", "clear", "--emit"); !strings.Contains(r.out, "unset API_BASE_URL API_GRAPHQL_URL API_TOKEN API_ENV") {
		t.Fatalf("clear: %q", r.out)
	}
}

func TestPassthroughArgsAfterDash(t *testing.T) {
	t.Setenv("API_BASE_URL", "https://api.test")
	r := run(t, "", "get", "/x", "--dry", "--", "-o", "./out", "--max-time", "5")
	if r.code != 0 || !strings.HasSuffix(strings.TrimSpace(r.out), "-o ./out --max-time 5") {
		t.Fatalf("%+v", r)
	}
}

func TestCompletion(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("API_BASE_URL", "")
	// seed history through a real request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	t.Setenv("API_BASE_URL", srv.URL)
	for _, p := range []string{"/users", "/users/1", "/orders"} {
		var out, errb bytes.Buffer
		if code := Run(context.Background(), []string{"get", p}, IO{In: strings.NewReader(""), Out: &out, Err: &errb}); code != 0 {
			t.Fatalf("seed %s: %d %s", p, code, errb.String())
		}
	}

	var out, errb bytes.Buffer
	Run(context.Background(), []string{"__complete", "get", "/us"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	got := out.String()
	if !strings.Contains(got, "/users/1") || !strings.Contains(got, "/users\n") || strings.Contains(got, "/orders") {
		t.Fatalf("path completion:\n%s", got)
	}

	out.Reset()
	Run(context.Background(), []string{"__complete", "get", "/x", "--header", ""}, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	if !strings.Contains(out.String(), "Content-Type: application/json") {
		t.Fatalf("header completion:\n%s", out.String())
	}

	t.Setenv("API_GQL_ENDPOINTS", "v2/user v2/auth")
	out.Reset()
	Run(context.Background(), []string{"__complete", "gql", "--endpoint", "v2/u"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	if !strings.Contains(out.String(), "v2/user") || strings.Contains(out.String(), "v2/auth") {
		t.Fatalf("endpoint completion:\n%s", out.String())
	}
}
