package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -v is not a feature of get and post: every command that sends a request can
// show it. A command that sends but has no -v, or has it and ignores it, fails
// here; a new request-sending command belongs in this table.
func TestVerboseWorksOnEveryCommandThatSendsARequest(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_GRAPHQL_URL", srv.URL+"/graphql")
	t.Setenv("API_TOKEN", "")
	t.Setenv("UGQL_CACHE_DIR", t.TempDir()) // never the real ~/.cache/ugql
	t.Setenv("API_OPENAPI_CACHE_DIR", t.TempDir())

	file := filepath.Join(t.TempDir(), "tour.http")
	if err := os.WriteFile(file, []byte("### One\nGET /one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		args   []string
		method string // what the trace must show being sent
	}{
		{"get", []string{"get", "/x"}, "GET"},
		{"post", []string{"post", "/x", `{"a":1}`}, "POST"},
		{"put", []string{"put", "/x", `{"a":1}`}, "PUT"},
		{"patch", []string{"patch", "/x", `{"a":1}`}, "PATCH"},
		{"del", []string{"del", "/x"}, "DELETE"},
		{"form", []string{"form", "/x", "-d", "a=b"}, "POST"},
		{"headers", []string{"headers", "/x"}, "GET"},
		{"time", []string{"time", "/x"}, "GET"},
		{"gql", []string{"gql", "{ me { id } }"}, "POST"},
		{"login", []string{"login", "--gql", "-e", "v2/auth", loginMutation, "-V", "input.username=me", "-V", "input.password=PWSECRET9", "--emit"}, "POST"},
		{"run", []string{"run", file, "--all"}, "GET"},
		{"openapi sync", []string{"openapi", "sync"}, "GET"},
		{"schema sync", []string{"schema", "sync", "svc"}, "POST"},
		{"schema names", []string{"schema", "names", "svc"}, "POST"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := run(t, "", append(c.args, "-v")...)
			if !strings.Contains(r.err, "> "+c.method+" ") {
				t.Errorf("-v did not show the %s request (exit %d):\n%s", c.method, r.code, r.err)
			}
			if !strings.Contains(r.err, "total ") {
				t.Errorf("-v did not show the timings (exit %d):\n%s", r.code, r.err)
			}
			if strings.Contains(r.out+r.err, "PWSECRET9") {
				t.Errorf("-v printed the password:\n%s", r.err)
			}
		})
	}
}
