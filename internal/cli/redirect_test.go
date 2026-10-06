package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"
)

// A redirect re-sends the request, body included, to wherever the server points
// (307/308 keep POST and its body). The destination checks only see the first
// hop, so cross-host redirects are refused unless the user opts in.

func redirectingServer(t *testing.T, status int, to func() string) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/final" {
			_, _ = w.Write([]byte(`{"data":{"login":{"accessToken":"FINAL-TOKEN"}}}`))
			return
		}
		http.Redirect(w, r, to(), status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCrossHostRedirectNeverReceivesTheLoginBody(t *testing.T) {
	evil := newLoginSrv(t, loginOK)
	for _, status := range []int{307, 308, 301, 302, 303} {
		evil.hits = 0
		bound, _ := redirectingServer(t, status, func() string { return evil.srv.URL + "/steal" })
		boundSecret(t, "pw", "BOUND-PW", bound.URL)
		clearSession(t)
		r := run(t, "", "login", "--gql", "-e", bound.URL+"/graphql", loginMutation, "-V", "input.password=@keychain:pw", "--emit")
		if evil.hits != 0 || strings.Contains(evil.body, "BOUND-PW") {
			t.Fatalf("HTTP %d: the password was forwarded to another host: hits=%d body=%q", status, evil.hits, evil.body)
		}
		if r.code == 0 || !strings.Contains(r.err, "refusing redirect to "+origin(evil.srv.URL)) || !strings.Contains(r.err, "--trust-hosts") {
			t.Fatalf("HTTP %d: the refusal must say why and how to opt in: %+v", status, r)
		}
	}
}

func TestSameHostRedirectsStillWork(t *testing.T) {
	bound, hits := redirectingServer(t, 307, func() string { return "/final" })
	clearSession(t)
	r := run(t, "", "login", "--gql", "-e", bound.URL+"/graphql", loginMutation, "--emit")
	if r.code != 0 || *hits != 2 || !strings.Contains(r.out, "FINAL-TOKEN") {
		t.Fatalf("a redirect within the same origin must be followed: %+v hits=%d", r, *hits)
	}
	// a plain GET too
	t.Setenv("API_BASE_URL", bound.URL)
	if r := run(t, "", "get", "/anything", ".data.login.accessToken", "-r"); r.code != 0 || strings.TrimSpace(r.out) != "FINAL-TOKEN" {
		t.Fatalf("%+v", r)
	}
}

func TestTrustHostsAllowsACrossHostRedirect(t *testing.T) {
	other := newLoginSrv(t, `{"ok":true}`)
	redir, _ := redirectingServer(t, 302, func() string { return other.srv.URL + "/x" })
	t.Setenv("API_BASE_URL", redir.URL)
	t.Setenv("API_TOKEN", "")

	if r := run(t, "", "get", "/a"); r.code == 0 || other.hits != 0 || !strings.Contains(r.err, "refusing redirect") {
		t.Fatalf("default: %+v hits=%d", r, other.hits)
	}
	if r := run(t, "", "get", "/a", "--trust-hosts"); r.code != 0 || other.hits != 1 {
		t.Fatalf("--trust-hosts: %+v hits=%d", r, other.hits)
	}
}

func TestRedirectToAnotherPortOrSchemeIsAnotherHost(t *testing.T) {
	a := newLoginSrv(t, loginOK) // a different port on the same machine = a different origin
	redir, _ := redirectingServer(t, 307, func() string { return a.srv.URL + "/x" })
	_ = secretstore.NewMemory()
	t.Setenv("API_BASE_URL", redir.URL)
	t.Setenv("API_TOKEN", "TOKEN")
	if r := run(t, "", "get", "/a"); r.code == 0 || a.hits != 0 {
		t.Fatalf("%+v hits=%d", r, a.hits)
	}
}
