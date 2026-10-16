package cli

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestViewerArgv(t *testing.T) {
	jsonNvim := []string{"nvim", "-R", "-c", "set ft=json foldmethod=indent foldlevel=99", "-"}
	cases := []struct {
		spec   string
		isJSON bool
		want   []string
	}{
		{"jless", true, []string{"jless"}},
		{"fx", false, []string{"fx"}},
		{"nvim", true, jsonNvim},
		{"nvim", false, []string{"nvim", "-R", "-"}},
		{"vim", false, []string{"vim", "-R", "-"}},
		{"bat -l json -p", true, []string{"bat", "-l", "json", "-p"}}, // typed command: as is, no preset
		{"nvim -R -", true, []string{"nvim", "-R", "-"}},
		{`sh -c 'cat; echo x'`, true, []string{"sh", "-c", "cat; echo x"}}, // quotes, never a shell
		{"   ", true, nil},
	}
	for _, c := range cases {
		if got := viewerArgv(c.spec, c.isJSON); !reflect.DeepEqual(got, c.want) {
			t.Errorf("viewerArgv(%q, %v) = %q, want %q", c.spec, c.isJSON, got, c.want)
		}
	}
}

// the viewer is for looking at ONE result: the commands that show one take it, the
// ones that print several (run, diff) or stream (watch) do not
func TestViewerFlagIsOnTheCommandsThatShowOneResult(t *testing.T) {
	for _, args := range [][]string{
		{"get", "--help"}, {"post", "--help"}, {"put", "--help"}, {"patch", "--help"}, {"del", "--help"},
		{"form", "--help"}, {"gql", "--help"}, {"openapi", "pick", "--help"}, {"again", "--help"},
	} {
		if r := run(t, "", args...); r.code != 0 || !strings.Contains(r.out, "--viewer") {
			t.Errorf("api %s must list --viewer (exit %d)", strings.Join(args, " "), r.code)
		}
	}
	for _, args := range [][]string{{"run", "--help"}, {"diff", "--help"}, {"watch", "--help"}} {
		if r := run(t, "", args...); strings.Contains(r.out, "--viewer") {
			t.Errorf("api %s must not take --viewer", strings.Join(args, " "))
		}
	}
	if r := run(t, "", "run", "x.http", "--viewer", "cat"); r.code != exitUsage {
		t.Errorf("run --viewer must be a usage error, got exit %d", r.code)
	}
}

// a plain request shows its result in the viewer like gql does: on a terminal only,
// the filter applies, the exit code stays
func TestViewerWorksOnAPlainRequest(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_GRAPHQL_URL", "")

	withTerminal(t)
	r := run(t, "", "get", "/x", ".ok", "--viewer", "sh -c 'echo VIEWED; cat'")
	if r.code != 0 || !strings.HasPrefix(r.out, "VIEWED\n") || !strings.Contains(r.out, "true") {
		t.Fatalf("on a terminal the viewer shows the filtered body: exit %d out %q err %q", r.code, r.out, r.err)
	}

	viewerTTY = func(any) bool { return false } // withTerminal restores the original at cleanup
	r = run(t, "", "get", "/x", ".ok", "--viewer", "sh -c 'echo VIEWED; cat'")
	if r.code != 0 || strings.Contains(r.out, "VIEWED") || !strings.Contains(r.out, "true") {
		t.Fatalf("through a pipe the body is printed as usual: exit %d out %q", r.code, r.out)
	}
}

func withTerminal(t *testing.T) {
	t.Helper()
	old := viewerTTY
	viewerTTY = func(any) bool { return true }
	t.Cleanup(func() { viewerTTY = old })
}

func gqlServer(t *testing.T, status int) {
	t.Helper()
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"1"}}}`))
	})
	t.Setenv("API_GRAPHQL_URL", srv.URL+"/graphql")
}

func TestViewerShowsTheResultAndKeepsTheExitCode(t *testing.T) {
	withTerminal(t)
	gqlServer(t, 200)
	r := run(t, "", "gql", "query { me { id } }", ".data.me.id", "--viewer", "sh -c 'echo VIEWED; cat'")
	if r.code != 0 || !strings.HasPrefix(r.out, "VIEWED\n") || !strings.Contains(r.out, `"1"`) {
		t.Fatalf("exit %d, out %q, err %q", r.code, r.out, r.err)
	}
	if strings.Contains(r.out, "\x1b[") {
		t.Fatalf("the viewer gets plain text, it colours itself: %q", r.out)
	}

	gqlServer(t, 500)
	r = run(t, "", "gql", "query { me { id } }", "--viewer", "cat")
	if r.code != exitHTTP || !strings.Contains(r.out, "me") {
		t.Fatalf("a viewed 500 must still exit 22 after showing the body: exit %d out %q", r.code, r.out)
	}
}

func TestViewerThatFailsFallsBackToPrintingTheBody(t *testing.T) {
	withTerminal(t)
	gqlServer(t, 200)
	for name, viewer := range map[string]string{"missing": "definitely-not-a-viewer-xyz", "exit 1": "sh -c 'cat >/dev/null; exit 1'"} {
		r := run(t, "", "gql", "query { me { id } }", ".data.me.id", "--viewer", viewer)
		if r.code != 0 || !strings.Contains(r.out, `"1"`) || !strings.Contains(r.err, "printing instead") {
			t.Errorf("%s: exit %d, out %q, err %q", name, r.code, r.out, r.err)
		}
	}
}

func TestViewerIsIgnoredWhenStdoutIsNotATerminal(t *testing.T) {
	gqlServer(t, 200)
	r := run(t, "", "gql", "query { me { id } }", ".data.me.id", "--viewer", "sh -c 'echo VIEWED; cat'")
	if r.code != 0 || strings.Contains(r.out, "VIEWED") || !strings.Contains(r.out, `"1"`) {
		t.Fatalf("through a pipe the body is printed as usual: exit %d out %q", r.code, r.out)
	}
}

func TestViewerRefusesASubscription(t *testing.T) {
	gqlServer(t, 200)
	r := run(t, "", "gql", "subscription { a }", "--viewer", "cat")
	if r.code != exitUsage || !strings.Contains(r.err, "--viewer") {
		t.Fatalf("exit %d, err %q", r.code, r.err)
	}
}
