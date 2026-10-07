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

	"cybercat/api/internal/secretstore"
)

type result struct {
	code     int
	out, err string
}

// repoRoot is the module root, taken while the working directory is still the
// package directory. api walks UP from the cwd for a .api.toml, and this repo
// ships one: a test that runs inside the source tree would pick it up.
var repoRoot, _ = filepath.Abs("../..")

// leaveSourceTree moves to an empty directory for the duration of one run when
// the test still sits in the source tree; it returns the way back. A test that
// chose its own directory (projectWith, t.Chdir) is left alone.
func leaveSourceTree(t *testing.T) (restore func()) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil || (wd != repoRoot && !strings.HasPrefix(wd, repoRoot+string(filepath.Separator))) {
		return func() {}
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(wd) }
}

// run executes the CLI with a clean API_* environment and the given stdin.
func run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	defer leaveSourceTree(t)()
	if !strings.HasPrefix(os.Getenv("HOME"), os.TempDir()) { // a test may pick its own
		t.Setenv("HOME", t.TempDir()) // never the real ~/.config/api
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if os.Getenv("API_CONFIG") == "" {
		t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	}
	var out, errb bytes.Buffer
	store := testStore
	if store == nil {
		store = secretstore.NewMemory() // never the real Keychain
	}
	code := Run(context.Background(), args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb, Store: store, ReadSecret: testReadSecret})
	return result{code, out.String(), errb.String()}
}

// testStore / testReadSecret let a test share a store across run() calls and
// answer @prompt. Tests that set them must restore them (see withSecrets).
var (
	testStore      secretstore.Store
	testReadSecret func(string) (string, error)
)

func withSecrets(t *testing.T, store secretstore.Store, prompt func(string) (string, error)) {
	t.Helper()
	oldS, oldP := testStore, testReadSecret
	testStore, testReadSecret = store, prompt
	t.Cleanup(func() { testStore, testReadSecret = oldS, oldP })
}

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("API_BASE_URL", srv.URL)
	t.Setenv("API_GRAPHQL_URL", "")
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_ENV", "")
	t.Setenv("API_DEBUG", "")
	return srv
}

// ── helpers shared by several test files ────────────────────────────────

func boundSecret(t *testing.T, name, value string, urls ...string) *secretstore.Memory {
	t.Helper()
	origins, err := secretstore.NormalizeOrigins(urls)
	if err != nil {
		t.Fatal(err)
	}
	mem := secretstore.NewMemory()
	_ = mem.Set(name, secretstore.Secret{Value: value, Origins: origins})
	withSecrets(t, mem, nil)
	return mem
}

func clearSession(t *testing.T) {
	t.Helper()
	for _, k := range []string{"API_BASE_URL", "API_GRAPHQL_URL", "API_TOKEN", "API_ENV"} {
		t.Setenv(k, "")
	}
}

// shellFromEnvLoad is the state `api env load dev` leaves: env name + URLs of the project's .env.dev.
func shellFromEnvLoad(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", serverURL)
	t.Setenv("API_GRAPHQL_URL", serverURL+"/graphql")
	t.Setenv("API_TOKEN", "")
}

type hit struct{ auth, tenant, path string }

func envServer(t *testing.T) (*httptest.Server, *hit) {
	t.Helper()
	h := &hit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.auth, h.tenant, h.path = r.Header.Get("Authorization"), r.Header.Get("X-Tenant"), r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, h
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_CONFIG", path)
}

func projectWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)
	return dir
}

type loginSrv struct {
	srv            *httptest.Server
	path, body, au string
	hits           int
}

func newLoginSrv(t *testing.T, respond string) *loginSrv {
	t.Helper()
	l := &loginSrv{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		l.path, l.body, l.au = r.URL.Path, b.String(), r.Header.Get("Authorization")
		l.hits++
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

const loginMutation = "mutation($input: LoginInput!) { login(input: $input) { accessToken } }"

const loginOK = `{"data":{"login":{"accessToken":"NEW-TOKEN-123"},"token":"REST-TOKEN"}}`

// failingStore records access and fails: proves nothing was read.
type failingStore struct{ onGet func() }

func (f failingStore) Get(string) (secretstore.Secret, error) {
	f.onGet()
	return secretstore.Secret{}, secretstore.ErrNotFound
}

func (failingStore) Set(string, secretstore.Secret) error { return secretstore.ErrUnavailable }

func (failingStore) Delete(string) error { return secretstore.ErrUnavailable }

type unavailableStore struct{}

func (unavailableStore) Get(string) (secretstore.Secret, error) {
	return secretstore.Secret{}, secretstore.ErrUnavailable
}

func (unavailableStore) Set(string, secretstore.Secret) error { return secretstore.ErrUnavailable }

func (unavailableStore) Delete(string) error { return secretstore.ErrUnavailable }
