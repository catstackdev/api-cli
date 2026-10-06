package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const assertSecret = "tok-SECRET-0123456789"

// assertServer answers /ok (200), /missing (404) and counts every request.
func assertServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Secret", assertSecret)
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":42,"items":[1,2,3],"token":"` + assertSecret + `"}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_ENV", "")
	t.Setenv("API_GRAPHQL_URL", "")
	return srv, &hits
}

func httpFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.http")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunAssertPassesAndPrintsEachCheck(t *testing.T) {
	assertServer(t)
	f := httpFile(t, "### a\n# @assert .status == 200\n# @assert .body.data.items | length == 3\nGET /ok\n")
	r := run(t, "", "run", f, "-M")
	if r.code != 0 || !strings.Contains(r.err, "✓ .status == 200") || !strings.Contains(r.err, "✓ .body.data.items | length == 3") {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.out, `"id": 42`) {
		t.Fatalf("the body is still printed:\n%s", r.out)
	}
}

func TestRunAssertFailureExitsWith3(t *testing.T) {
	assertServer(t)
	f := httpFile(t, "### a\n# @assert .status == 200\n# @assert .status == 201\nGET /ok\n")
	r := run(t, "", "run", f, "-M")
	if r.code != 3 {
		t.Fatalf("want exit 3: %+v", r)
	}
	for _, want := range []string{"✓ .status == 200", "✗ .status == 201 — false (line 3)", "a: 1 of 2 assertions failed"} {
		if !strings.Contains(r.err, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.err)
		}
	}
}

// With @assert, the assertions decide: a 404 that was asserted is a pass, and
// the same request without @assert keeps failing with 22.
func TestRunAssertDecidesInsteadOfTheHTTPStatus(t *testing.T) {
	assertServer(t)
	expected := httpFile(t, "### a\n# @assert .status == 404\n# @assert .body.error == \"nope\"\nGET /missing\n")
	if r := run(t, "", "run", expected, "-M"); r.code != 0 {
		t.Fatalf("an asserted 404 must pass: %+v", r)
	}
	plain := httpFile(t, "### a\nGET /missing\n")
	if r := run(t, "", "run", plain, "-M"); r.code != 22 {
		t.Fatalf("no @assert: exit 22 is unchanged: %+v", r)
	}
	wrong := httpFile(t, "### a\n# @assert .status == 200\nGET /missing\n")
	if r := run(t, "", "run", wrong, "-M"); r.code != 3 {
		t.Fatalf("a 404 where 200 was asserted fails with 3, not 22: %+v", r)
	}
}

func TestRunAssertSyntaxErrorIsCaughtBeforeAnyRequest(t *testing.T) {
	_, hits := assertServer(t)
	f := httpFile(t, "### a\nGET /ok\n\n### b\n# @assert .status ==\nGET /ok\n")
	r := run(t, "", "run", f, "--all", "-M")
	if r.code != 2 || atomic.LoadInt32(hits) != 0 {
		t.Fatalf("want usage error and no request: %+v hits=%d", r, atomic.LoadInt32(hits))
	}
	if !strings.Contains(r.err, "t.http:5:") || !strings.Contains(r.err, "@assert") {
		t.Fatalf("the error must name the line:\n%s", r.err)
	}
}

func TestRunAssertAllStopsAtTheFirstFailureUnlessKeepGoing(t *testing.T) {
	_, hits := assertServer(t)
	f := httpFile(t, "### one\n# @assert .status == 200\nGET /ok\n\n### two\n# @assert .status == 500\nGET /ok\n\n### three\n# @assert .status == 200\nGET /ok\n")
	r := run(t, "", "run", f, "--all", "-M")
	if r.code != 3 || atomic.LoadInt32(hits) != 2 {
		t.Fatalf("stops after the failing request: %+v hits=%d", r, atomic.LoadInt32(hits))
	}
	atomic.StoreInt32(hits, 0)
	r = run(t, "", "run", f, "--all", "--keep-going", "-M")
	if r.code != 3 || atomic.LoadInt32(hits) != 3 {
		t.Fatalf("--keep-going runs all three: %+v hits=%d", r, atomic.LoadInt32(hits))
	}
	if !strings.Contains(r.err, "3 assertion(s): 2 passed, 1 failed") {
		t.Fatalf("summary line missing:\n%s", r.err)
	}
}

func TestRunAssertDryRunSendsNothingAndSaysSo(t *testing.T) {
	_, hits := assertServer(t)
	f := httpFile(t, "### a\n# @assert .status == 200\nGET /ok\n")
	r := run(t, "", "run", f, "--dry", "-M")
	if r.code != 0 || atomic.LoadInt32(hits) != 0 || !strings.Contains(r.err, "1 assertion(s) not evaluated with --dry") {
		t.Fatalf("%+v hits=%d", r, atomic.LoadInt32(hits))
	}
}

// The expression and a reason are printed, never the response: bodies and
// headers carry tokens and CI logs are shared.
func TestRunAssertOutputNeverContainsResponseData(t *testing.T) {
	assertServer(t)
	f := httpFile(t, strings.Join([]string{
		"### a",
		"# @assert .body.data.token + 1",
		`# @assert .body.data.token == "other"`,
		"# @assert .headers[\"x-secret\"]",
		"# @assert (.headers[\"x-secret\"] | tonumber) > 0",
		"GET /ok",
		"",
	}, "\n"))
	r := run(t, "", "run", f, "-M") // stdout has the body as usual; only stderr is checked
	if r.code != 3 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.err, assertSecret) || strings.Contains(r.err, "0123456789") {
		t.Fatalf("a failure leaked response data:\n%s", r.err)
	}
}
