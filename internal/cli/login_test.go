package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── GraphQL login ──────────────────────────────────────────────────────────

func TestGraphQLLogin(t *testing.T) {
	var path, body, auth string
	respond := `{"data":{"login":{"accessToken":"gql-tok-abcdef-it's","session":"sess-1"}}}`
	server(t, func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		path, body, auth = r.URL.Path, b.String(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(respond))
	})
	t.Setenv("API_GRAPHQL_URL", os.Getenv("API_BASE_URL")+"/graphql")
	t.Setenv("API_TOKEN", "STALE")
	mut := "mutation($input: LoginInput!) { login(input: $input) { accessToken } }"

	r := run(t, "", "login", "--gql", "-e", "v2/auth", mut, "-V", "input.username=me@x.com", "-V", "input.password=pw", "--emit")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if path != "/graphql/v2/auth" || auth != "" {
		t.Fatalf("path=%q auth=%q (a stale token must not be sent)", path, auth)
	}
	if !strings.Contains(body, `"variables":{"input":{"username":"me@x.com","password":"pw"}}`) || !strings.Contains(body, "LoginInput") {
		t.Fatalf("body = %s", body)
	}
	if got := strings.TrimSpace(r.out); got != `export API_TOKEN='gql-tok-abcdef-it'\''s'` {
		t.Fatalf("export line: %q", got)
	}
	if strings.Contains(r.err, "it's") || !strings.Contains(r.err, ".data.login.accessToken") {
		t.Fatalf("stderr must mask the token and name the path: %q", r.err)
	}

	// a custom token path is the last positional
	r = run(t, "", "login", "--gql", "-e", "v2/auth", mut, "-V", "input.username=a", ".data.login.session", "--emit")
	if strings.TrimSpace(r.out) != `export API_TOKEN='sess-1'` {
		t.Fatalf("custom path: %+v", r)
	}

	// the mutation can come from stdin or a file
	file := filepath.Join(t.TempDir(), "login.graphql")
	_ = os.WriteFile(file, []byte(mut), 0o600)
	if r := run(t, "", "login", "--gql", "@"+file, "--emit"); r.code != 0 || !strings.Contains(body, "LoginInput") {
		t.Fatalf("@file: %+v body=%s", r, body)
	}
	if r := run(t, mut, "login", "--gql", "--emit"); r.code != 0 {
		t.Fatalf("stdin: %+v", r)
	}

	// GraphQL-level failure (HTTP 200 + errors) is a login failure, not an empty token
	respond = `{"errors":[{"message":"bad creds"}],"data":null}`
	r = run(t, "", "login", "--gql", "-e", "v2/auth", mut, "--emit")
	if r.code != 1 || !strings.Contains(r.err, "login failed: bad creds") || r.out != "" {
		t.Fatalf("graphql error: %+v", r)
	}
}

func TestLoginDryRunSendsNothing(t *testing.T) {
	calls := 0
	server(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) })
	for _, args := range [][]string{
		{"login", "/auth", `{"u":1}`, "--dry"},
		{"login", "--gql", "mutation { x }", "--dry"},
	} {
		if r := run(t, "", args...); r.code != 0 || !strings.HasPrefix(r.out, "curl ") {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	if calls != 0 {
		t.Fatalf("--dry sent %d request(s)", calls)
	}
}
