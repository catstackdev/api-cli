package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cybercat/api/internal/secretstore"
)

// authSrv accepts exactly one bearer token and issues it from POST /auth.
type authSrv struct {
	srv    *httptest.Server
	good   string
	logins int
	seen   []string // Authorization of every non-login request
}

func newAuthSrv(t *testing.T, good string) *authSrv {
	t.Helper()
	a := &authSrv{good: good}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" {
			a.logins++
			if r.Header.Get("Authorization") != "" {
				t.Errorf("a login must not carry a token, got %q", r.Header.Get("Authorization"))
			}
			fmt.Fprintf(w, `{"token":%q}`, a.good)
			return
		}
		a.seen = append(a.seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer "+a.good {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *authSrv) lastAuth() string {
	if len(a.seen) == 0 {
		return "<no request>"
	}
	return a.seen[len(a.seen)-1]
}

// cacheSetup: a global config with env dev pointing at srv, the password secret
// bound to it, and the shell on env dev (as after `api env load dev`).
func cacheSetup(t *testing.T, srv *authSrv, defaults, loginVars string) *secretstore.Memory {
	t.Helper()
	writeConfig(t, fmt.Sprintf(`
[defaults]
%s
[env.dev]
url = %q
[env.dev.login]
path = "/auth"
token_path = ".token"
[env.dev.login.vars]
%s
[env.other]
url = %q
`, defaults, srv.srv.URL, loginVars, srv.srv.URL))
	mem := boundSecret(t, "pw", "the-password", srv.srv.URL)
	clearSession(t)
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", srv.srv.URL)
	return mem
}

const pwVars = `user = "me"` + "\n" + `password = "@keychain:pw"`

func putToken(t *testing.T, mem *secretstore.Memory, env, token string, origins ...string) {
	t.Helper()
	norm, err := secretstore.NormalizeOrigins(origins)
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Set("token."+env, secretstore.Secret{Value: token, Origins: norm}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginCachesTokenBoundToTheEnvUrl(t *testing.T) {
	srv := newAuthSrv(t, "FRESH-TOKEN-1234567")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)

	if r := run(t, "", "login", "--emit"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	sec, err := mem.Get("token.dev")
	if err != nil || sec.Value != "FRESH-TOKEN-1234567" {
		t.Fatalf("not cached: %v %+v", err, sec)
	}
	want, _ := secretstore.NormalizeOrigin(srv.srv.URL)
	if len(sec.Origins) != 1 || sec.Origins[0] != want {
		t.Fatalf("cached token must be bound to the env url, got %v", sec.Origins)
	}
}

func TestLoginDoesNotCacheWhenOff(t *testing.T) {
	srv := newAuthSrv(t, "FRESH-TOKEN-1234567")
	mem := cacheSetup(t, srv, "token_cache = false", pwVars)
	run(t, "", "login", "--emit")
	if _, err := mem.Get("token.dev"); err == nil {
		t.Fatal("token cached although token_cache is off")
	}
}

func TestCachedTokenIsSentToTheBaseUrlOnly(t *testing.T) {
	srv := newAuthSrv(t, "GOOD-CACHED-TOKEN")
	evil := newAuthSrv(t, "irrelevant")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)
	putToken(t, mem, "dev", "GOOD-CACHED-TOKEN", srv.srv.URL)

	if r := run(t, "", "get", "/x"); r.code != 0 || srv.lastAuth() != "Bearer GOOD-CACHED-TOKEN" {
		t.Fatalf("cached token not used: %+v auth=%q", r, srv.lastAuth())
	}
	// an absolute URL on another server: no token, not even with --trust-hosts
	for _, extra := range [][]string{nil, {"--trust-hosts"}} {
		args := append([]string{"get", evil.srv.URL + "/steal"}, extra...)
		run(t, "", args...)
		if got := evil.lastAuth(); got != "" {
			t.Fatalf("%v: the cached token reached another server: %q", extra, got)
		}
		evil.seen = nil
	}
}

func TestCachedTokenIsNotUsedWhenTheServerChanged(t *testing.T) {
	srv := newAuthSrv(t, "GOOD-CACHED-TOKEN")
	evil := newAuthSrv(t, "x")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)
	putToken(t, mem, "dev", "GOOD-CACHED-TOKEN", srv.srv.URL) // bound to the real server

	// a cloned repo's .env.dev points the shell (same env NAME) at its own server
	t.Setenv("API_BASE_URL", evil.srv.URL)
	run(t, "", "get", "/x")
	if got := evil.lastAuth(); got != "" {
		t.Fatalf("the cached token followed the env NAME to another server: %q", got)
	}
}

func TestUnboundOrForeignCacheEntriesAreIgnored(t *testing.T) {
	srv := newAuthSrv(t, "GOOD-CACHED-TOKEN")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)

	putToken(t, mem, "dev", "GOOD-CACHED-TOKEN") // no binding: never used
	run(t, "", "get", "/x")
	if got := srv.lastAuth(); got != "" {
		t.Fatalf("an unbound cache entry was used: %q", got)
	}
	putToken(t, mem, "dev", "GOOD-CACHED-TOKEN", "https://elsewhere.example")
	run(t, "", "get", "/x")
	if got := srv.lastAuth(); got != "" {
		t.Fatalf("an entry bound to another server was used: %q", got)
	}
}

func TestCacheIsOffByDefaultAndIgnoredFromProjectConfig(t *testing.T) {
	srv := newAuthSrv(t, "GOOD-CACHED-TOKEN")
	mem := cacheSetup(t, srv, "", pwVars) // global: cache not enabled
	putToken(t, mem, "dev", "GOOD-CACHED-TOKEN", srv.srv.URL)
	run(t, "", "get", "/x")
	if got := srv.lastAuth(); got != "" {
		t.Fatalf("cache used although not enabled: %q", got)
	}

	// a project file (maybe a cloned repo) must not switch it on
	projectWith(t, map[string]string{".api.toml": "[defaults]\ntoken_cache = true\nauto_login = true\n"})
	r := run(t, "", "get", "/x")
	if got := srv.lastAuth(); got != "" {
		t.Fatalf("a project config enabled the token cache: %q", got)
	}
	if srv.logins != 0 {
		t.Fatalf("a project config enabled auto_login: %d logins (%+v)", srv.logins, r)
	}
	if !strings.Contains(r.err, "only honoured in the global config") {
		t.Fatalf("no warning about the ignored keys: %q", r.err)
	}
	// and `config set` refuses to write them to a project file
	if r := run(t, "", "config", "set", "defaults.token_cache", "true", "--local"); r.code == 0 {
		t.Fatalf("config set --local accepted token_cache: %+v", r)
	}
}

func TestDryRunNeverReadsTheCache(t *testing.T) {
	srv := newAuthSrv(t, "x")
	cacheSetup(t, srv, "token_cache = true", pwVars)
	reads := 0
	withSecrets(t, failingStore{onGet: func() { reads++ }}, nil)
	if r := run(t, "", "get", "/x", "--dry"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if reads != 0 {
		t.Fatalf("--dry read the credential store %d time(s)", reads)
	}
}

func TestExpiredCachedTokenIsNotUsed(t *testing.T) {
	srv := newAuthSrv(t, "x")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)
	putToken(t, mem, "dev", fakeJWT(time.Now().Add(20*time.Second)), srv.srv.URL) // inside the 60s skew
	run(t, "", "get", "/x")
	if got := srv.lastAuth(); got != "" {
		t.Fatalf("an about-to-expire token was sent: %q", got)
	}
}

func TestAutoLoginOn401(t *testing.T) {
	srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
	mem := cacheSetup(t, srv, "token_cache = true\nauto_login = true", pwVars)
	t.Setenv("API_TOKEN", "STALE-SESSION-TOKEN")

	r := run(t, "", "get", "/x")
	if r.code != 0 || srv.logins != 1 || srv.lastAuth() != "Bearer NEW-GOOD-TOKEN" {
		t.Fatalf("auto login: %+v logins=%d auth=%q", r, srv.logins, srv.lastAuth())
	}
	if sec, err := mem.Get("token.dev"); err != nil || sec.Value != "NEW-GOOD-TOKEN" {
		t.Fatalf("new token not cached: %v %+v", err, sec)
	}
	if strings.Contains(r.out+r.err, "the-password") {
		t.Fatalf("password leaked: %+v", r)
	}

	// next invocation: the shell still has the stale token, the cache has the new one: no login needed
	srv.logins = 0
	if r := run(t, "", "get", "/x"); r.code != 0 || srv.logins != 0 {
		t.Fatalf("second call: %+v logins=%d", r, srv.logins)
	}
}

func TestNoAutoLoginWhenNotOptedIn(t *testing.T) {
	srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
	cacheSetup(t, srv, "token_cache = true", pwVars)
	t.Setenv("API_TOKEN", "STALE")
	if r := run(t, "", "get", "/x"); r.code != exitHTTP || srv.logins != 0 {
		t.Fatalf("%+v logins=%d", r, srv.logins)
	}
}

func TestAutoLoginDoesNotTouchExplicitTokens(t *testing.T) {
	srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
	cacheSetup(t, srv, "token_cache = true\nauto_login = true", pwVars)
	t.Setenv("API_TOKEN", "STALE")

	if r := run(t, "", "--token", "TYPED", "get", "/x"); r.code != exitHTTP || srv.logins != 0 {
		t.Fatalf("--token: %+v logins=%d", r, srv.logins)
	}
	if r := run(t, "", "get", "/x", "-H", "Authorization: Bearer HAND-WRITTEN"); r.code != exitHTTP || srv.logins != 0 {
		t.Fatalf("-H Authorization: %+v logins=%d", r, srv.logins)
	}
	if r := run(t, "", "get", "/x", "--no-auth"); r.code != exitHTTP || srv.logins != 0 {
		t.Fatalf("--no-auth: %+v logins=%d", r, srv.logins)
	}
}

func TestAutoLoginAcceptsOnlyKeychainReferences(t *testing.T) {
	for name, vars := range map[string]string{
		"@prompt": `user = "me"` + "\n" + `password = "@prompt"`,
		"@env":    `user = "me"` + "\n" + `password = "@env:API_PW_TEST"`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
			cacheSetup(t, srv, "token_cache = true\nauto_login = true", vars)
			t.Setenv("API_PW_TEST", "pw-from-env")
			t.Setenv("API_TOKEN", "STALE")
			withSecrets(t, secretstore.NewMemory(), func(string) (string, error) {
				t.Error("automatic login must never prompt")
				return "typed", nil
			})
			r := run(t, "", "get", "/x")
			if r.code != exitHTTP || srv.logins != 0 || !strings.Contains(r.err, "only uses @keychain") {
				t.Fatalf("%+v logins=%d", r, srv.logins)
			}
		})
	}
}

func TestLoginRequestsAreNeverRetried(t *testing.T) {
	// a login answered 401 must not trigger another login (loop / lockout risk)
	var hits int
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "no", http.StatusUnauthorized)
	})
	writeConfig(t, fmt.Sprintf("[defaults]\ntoken_cache = true\nauto_login = true\n[env.dev]\nurl = %q\n[env.dev.login]\npath = \"/auth\"\ntoken_path = \".token\"\n[env.dev.login.vars]\nuser = \"me\"\npassword = \"@keychain:pw\"\n", srv.URL))
	boundSecret(t, "pw", "p", srv.URL)
	t.Setenv("API_ENV", "dev")
	if r := run(t, "", "login", "--emit"); r.code == 0 || hits != 1 {
		t.Fatalf("%+v hits=%d", r, hits)
	}
}

func TestLogout(t *testing.T) {
	srv := newAuthSrv(t, "x")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)
	putToken(t, mem, "dev", "CACHED", srv.srv.URL)

	if r := run(t, "", "logout"); r.code != exitUsage {
		t.Fatalf("logout without --emit must explain the shim: %+v", r)
	}
	r := run(t, "", "logout", "--emit")
	if r.code != 0 || strings.TrimSpace(r.out) != "unset API_TOKEN" {
		t.Fatalf("%+v", r)
	}
	if _, err := mem.Get("token.dev"); err == nil {
		t.Fatal("cached token not removed")
	}

	// --env other: the shell's own token is not unset
	putToken(t, mem, "other", "CACHED-OTHER", srv.srv.URL)
	r = run(t, "", "--env", "other", "logout", "--emit")
	if strings.Contains(r.out, "API_TOKEN") {
		t.Fatalf("logging out of another env unset this shell's token: %q", r.out)
	}
	if _, err := mem.Get("token.other"); err == nil {
		t.Fatalf("other cached token not removed: %+v", r)
	}
}

func TestEnvStatusShowsTheCache(t *testing.T) {
	srv := newAuthSrv(t, "x")
	mem := cacheSetup(t, srv, "token_cache = true", pwVars)
	if r := run(t, "", "env", "status"); !strings.Contains(r.out, "cache : none") {
		t.Fatalf("%+v", r)
	}
	putToken(t, mem, "dev", fakeJWT(time.Now().Add(time.Hour)), srv.srv.URL)
	if r := run(t, "", "env", "status"); !strings.Contains(r.out, "cache : ") || !strings.Contains(r.out, "expires in") {
		t.Fatalf("%+v", r)
	}
}

func TestAutoLoginNeverReplaysToAnotherServer(t *testing.T) {
	// --trust-hosts lets the session token go to a foreign host; when THAT host
	// answers 401, a fresh login must not be replayed to it.
	srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
	evil := newAuthSrv(t, "never-matches")
	cacheSetup(t, srv, "token_cache = true\nauto_login = true", pwVars)
	t.Setenv("API_TOKEN", "STALE")

	r := run(t, "", "get", evil.srv.URL+"/steal", "--trust-hosts")
	if srv.logins != 0 {
		t.Fatalf("a 401 from another server triggered a login: %+v", r)
	}
	for _, got := range evil.seen {
		if strings.Contains(got, "NEW-GOOD-TOKEN") {
			t.Fatalf("the fresh token was replayed to another server: %q", got)
		}
	}
}

func TestAutoLoginRunsAtMostOncePerInvocation(t *testing.T) {
	// `run --all` sends several requests in one process. Without a token cache
	// every one of them would be answered 401 again: the invocation must still
	// log in once, not once per request (a loop / account lockout risk).
	srv := newAuthSrv(t, "NEW-GOOD-TOKEN")
	cacheSetup(t, srv, "auto_login = true", pwVars)
	t.Setenv("API_TOKEN", "STALE")
	dir := projectWith(t, nil)
	file := filepath.Join(dir, "three.http")
	if err := os.WriteFile(file, []byte("### A\nGET /a\n\n### B\nGET /b\n\n### C\nGET /c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, "", "run", file, "--all", "--keep-going")
	if srv.logins != 1 {
		t.Fatalf("%d logins in one invocation (want 1): %+v", srv.logins, r)
	}
	if r.code == 0 {
		t.Fatalf("without a cache the later requests are still rejected: %+v", r)
	}
}

func TestLoginWithoutASessionAdoptsTheEnvSoTheTokenIsUsable(t *testing.T) {
	// the shell has no session; default_env supplies the server. A token is never
	// attached to a config-chosen server, so login must also set the session.
	srv := newAuthSrv(t, "FRESH-TOKEN-1234567")
	cacheSetup(t, srv, "", pwVars)
	clearSession(t)
	writeConfigAppend(t, "default_env = \"dev\"\n")

	r := run(t, "", "login", "--emit")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{"API_BASE_URL='" + srv.srv.URL + "'", "API_ENV='dev'", "export API_TOKEN='FRESH-TOKEN-1234567'"} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("login output lacks %q:\n%s", want, r.out)
		}
	}
}

func writeConfigAppend(t *testing.T, prefix string) {
	t.Helper()
	path := os.Getenv("API_CONFIG")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(prefix), data...), 0o600); err != nil {
		t.Fatal(err)
	}
}
