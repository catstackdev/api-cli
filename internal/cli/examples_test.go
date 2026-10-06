package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cybercat/api/internal/httpfile"
)

// The files in examples/ are documentation people copy from, so they must keep
// parsing and running as the format and the runner change.

func exampleFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.http"))
	if err != nil || len(files) < 3 {
		t.Fatalf("examples/*.http: found %v (err %v), want at least 3", files, err)
	}
	return files
}

func example(name string) string { return filepath.Join("..", "..", "examples", name) }

func TestExamplesParseAndDryRun(t *testing.T) {
	for _, f := range exampleFiles(t) {
		t.Run(filepath.Base(f), func(t *testing.T) {
			clearSession(t)
			t.Setenv("API_BASE_URL", "http://localhost:3000")

			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			file, err := httpfile.Parse(data)
			if err != nil {
				t.Fatalf("does not parse: %v", err)
			}
			if len(file.Requests) == 0 {
				t.Fatal("no requests")
			}

			r := run(t, "", "run", f, "--all", "--dry")
			if r.code != 0 {
				t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.out, r.err)
			}
			if got := strings.Count(r.out, "curl "); got != len(file.Requests) {
				t.Errorf("--dry printed %d requests, the file has %d\n%s", got, len(file.Requests), r.out)
			}
		})
	}
}

func TestExampleChainingPassesTheCapturedID(t *testing.T) {
	srv, log := runServer(t)
	clearSession(t)
	t.Setenv("API_BASE_URL", srv.URL)

	if r := run(t, "", "run", example("chaining.http"), "--all"); r.code != 0 {
		t.Fatalf("exit %d\nstderr: %s", r.code, r.err)
	}
	var got []string
	for _, s := range *log {
		got = append(got, s.method+" "+s.path)
	}
	want := "POST /users, GET /users/42, DELETE /users/42"
	if strings.Join(got, ", ") != want {
		t.Errorf("requests = %v, want %s", got, want)
	}

	// asking for one request runs only what it needs first
	*log = nil
	if r := run(t, "", "run", example("chaining.http"), "fetch"); r.code != 0 {
		t.Fatalf("exit %d\nstderr: %s", r.code, r.err)
	}
	if len(*log) != 2 || (*log)[0].method != "POST" || (*log)[1].path != "/users/42" {
		t.Errorf("fetch should run create first, got %v", *log)
	}
}

func TestExampleChecksPassAndFail(t *testing.T) {
	healthy := true
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health" && healthy:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/health":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"status":"down"}`))
		case r.Method == "POST" && r.URL.Path == "/users":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"data":{"id":1}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	})

	r := run(t, "", "run", example("checks.http"), "--all")
	if r.code != 0 {
		t.Fatalf("a correct server must pass every check: exit %d\nstderr: %s", r.code, r.err)
	}

	healthy = false
	r = run(t, "", "run", example("checks.http"), "--all")
	if r.code != 3 {
		t.Fatalf("a failing check must exit 3, got %d\nstderr: %s", r.code, r.err)
	}
	if !strings.Contains(r.err, ".status == 200") || strings.Contains(r.err, "down") {
		t.Errorf("the failure names the expression and never the response:\n%s", r.err)
	}
}
