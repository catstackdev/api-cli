package cli

import (
	"regexp"
	"strings"
	"testing"
)

// -v prints what is sent: it must mask the same things --debug and --dry mask,
// in the URL, the headers and the body, and still show what is harmless.
func TestVerboseMasksTheRequestItPrints(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "BEARER-TOKEN-ABCDEFGH-12345")
	t.Setenv("API_GRAPHQL_URL", "")

	r := run(t, "", "post", "/x?api_key=QUERYSECRET1&limit=5",
		`{"password":"BODYSECRET2","name":"visible-name"}`,
		"-H", "X-Api-Key: HEADERSECRET3", "-H", "X-Request-Id: keep-me", "-v")
	if r.code != 0 {
		t.Fatalf("setup: %+v", r)
	}
	for _, secret := range []string{"QUERYSECRET1", "BODYSECRET2", "HEADERSECRET3", "BEARER-TOKEN-ABCDEFGH-12345"} {
		if strings.Contains(r.out+r.err, secret) {
			t.Errorf("-v printed %q:\n%s", secret, r.err)
		}
	}
	for _, shown := range []string{"limit=5", "keep-me", "visible-name"} {
		if !strings.Contains(r.err, shown) {
			t.Errorf("-v should still show %q:\n%s", shown, r.err)
		}
	}
}

// the --summary footer names the URL: it is masked like the one -v and --dry print
func TestSummaryMasksTheURLItPrints(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_GRAPHQL_URL", "")

	r := run(t, "", "get", "/x?api_key=SUMMARYSECRET1&limit=5", "--summary")
	if r.code != 0 {
		t.Fatalf("setup: %+v", r)
	}
	if strings.Contains(r.out+r.err, "SUMMARYSECRET1") {
		t.Errorf("--summary printed the query secret:\n%s", r.err)
	}
	if !strings.Contains(r.err, "limit=5") || !strings.Contains(r.err, "status : 200") {
		t.Errorf("--summary should still show the harmless parts:\n%s", r.err)
	}
}

// a body can be a whole file or stdin: -v shows the start of it, not all of it
func TestVerboseCapsTheBodyItPrints(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_GRAPHQL_URL", "")

	big := `{"blob":"` + strings.Repeat("x", 50000) + `"}`
	r := run(t, "", "post", "/x", big, "-v")
	if r.code != 0 {
		t.Fatalf("setup: %+v", r)
	}
	if len(r.err) > 20000 || !strings.Contains(r.err, "more bytes") {
		t.Fatalf("-v must cap the printed body (stderr is %d bytes):\n%.300s", len(r.err), r.err)
	}
}

// -v shows where the time went, from the request that was actually sent
func TestVerbosePrintsThePhaseTimings(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_GRAPHQL_URL", "")

	r := run(t, "", "get", "/x", "-v")
	if r.code != 0 {
		t.Fatalf("setup: %+v", r)
	}
	for _, phase := range []string{"connect", "wait", "download", "total"} {
		if !strings.Contains(r.err, phase) {
			t.Errorf("-v should print the %s phase:\n%s", phase, r.err)
		}
	}
	if strings.Contains(r.err, "\x1b[") {
		t.Errorf("no colour when stderr is not a terminal:\n%q", r.err)
	}
}

// colour is presentation only: with it on, -v still masks, and removing the
// escape codes gives back the plain text
func TestVerboseColourKeepsTheMasking(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_TOKEN", "BEARER-TOKEN-ABCDEFGH-12345")
	t.Setenv("API_GRAPHQL_URL", "")

	r := run(t, "", "--color", "always", "post", "/x?api_key=QUERYSECRET1&limit=5",
		`{"password":"BODYSECRET2","name":"visible-name"}`, "-H", "X-Api-Key: HEADERSECRET3", "-v")
	if r.code != 0 {
		t.Fatalf("setup: %+v", r)
	}
	if !strings.Contains(r.err, "\x1b[") {
		t.Fatalf("--color always should colour -v:\n%q", r.err)
	}
	for _, secret := range []string{"QUERYSECRET1", "BODYSECRET2", "HEADERSECRET3", "BEARER-TOKEN-ABCDEFGH-12345"} {
		if strings.Contains(r.err, secret) {
			t.Errorf("coloured -v printed %q:\n%s", secret, r.err)
		}
	}
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(r.err, "")
	for _, shown := range []string{"> POST ", "limit=5", "visible-name", "< HTTP/1.1 200 OK", "total "} {
		if !strings.Contains(plain, shown) {
			t.Errorf("without the escape codes -v should still show %q:\n%s", shown, plain)
		}
	}
}
