package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cybercat/api/internal/secretstore"
)

// runShared is run() with ONE state dir across calls (the index lives there).
func runShared(t *testing.T, state, stdin string, args ...string) result {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", state)
	if os.Getenv("API_CONFIG") == "" {
		t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	}
	var out, errb bytes.Buffer
	store := testStore
	if store == nil {
		store = secretstore.NewMemory()
	}
	code := Run(context.Background(), args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb, Store: store})
	return result{code, out.String(), errb.String()}
}

func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

func TestSecretListFollowsSetBindRmAndPrune(t *testing.T) {
	mem := secretstore.NewMemory()
	withSecrets(t, mem, nil)
	state := t.TempDir()
	clearSession(t)

	if r := runShared(t, state, "", "secret", "list"); r.code != 0 || !strings.Contains(r.err+r.out, "no secrets known") {
		t.Fatalf("empty: %+v", r)
	}
	runShared(t, state, "pw-one\n", "secret", "set", "bound-one", "--stdin", "--for", "https://api.one.example")
	runShared(t, state, "pw-two\n", "secret", "set", "loose-two", "--stdin")

	r := runShared(t, state, "", "secret", "list")
	if r.code != 0 || !strings.Contains(r.out, "bound-one") || !strings.Contains(r.out, "https://api.one.example") ||
		!strings.Contains(r.out, "loose-two") || !strings.Contains(r.out, "unbound") {
		t.Fatalf("list: %+v", r)
	}
	if strings.Contains(r.out+r.err, "pw-one") || strings.Contains(r.out+r.err, "pw-two") {
		t.Fatalf("a value leaked into list: %+v", r)
	}

	// the index file holds NAMES only: no values, no origins
	data, err := os.ReadFile(filepath.Join(state, "api", "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "bound-one\nloose-two\n" {
		t.Fatalf("index content %q", got)
	}

	// bind through the CLI keeps it listed as bound
	runShared(t, state, "", "secret", "bind", "loose-two", "https://api.two.example")
	if r := runShared(t, state, "", "secret", "list"); strings.Contains(r.out, "unbound") {
		t.Fatalf("bind should have removed 'unbound': %s", r.out)
	}

	// a secret that vanished from the store (deleted in Keychain Access) is "missing"; --prune forgets it
	_ = mem.Delete("bound-one")
	if r := runShared(t, state, "", "secret", "list"); !strings.Contains(r.out, "missing") {
		t.Fatalf("missing not reported: %+v", r)
	}
	runShared(t, state, "", "secret", "list", "--prune")
	if r := runShared(t, state, "", "secret", "list"); strings.Contains(r.out, "bound-one") {
		t.Fatalf("prune did not forget it: %+v", r)
	}

	// rm updates the index
	runShared(t, state, "", "secret", "rm", "loose-two")
	if r := runShared(t, state, "", "secret", "list"); !strings.Contains(r.err+r.out, "no secrets known") {
		t.Fatalf("rm did not update the index: %+v", r)
	}
}

func TestDoctorWarnsAboutUnboundSecrets(t *testing.T) {
	withSecrets(t, secretstore.NewMemory(), nil)
	state := t.TempDir()
	clearSession(t)
	runShared(t, state, "x\n", "secret", "set", "loose", "--stdin")

	r := runShared(t, state, "", "doctor")
	if !strings.Contains(r.out+r.err, "not bound to a server: loose") || !strings.Contains(r.out+r.err, "api secret bind NAME <url>") {
		t.Fatalf("doctor did not warn about the unbound secret:\n%s%s", r.out, r.err)
	}
	runShared(t, state, "", "secret", "bind", "loose", "https://api.example.com")
	r = runShared(t, state, "", "doctor")
	if strings.Contains(r.out+r.err, "not bound to a server") || !strings.Contains(r.out+r.err, "all bound to a server") {
		t.Fatalf("doctor still warns after binding:\n%s%s", r.out, r.err)
	}
}

func TestEnvStatusAndDoctorShowTokenExpiry(t *testing.T) {
	state := t.TempDir()
	srv := server(t, nil)
	_ = srv

	t.Setenv("API_TOKEN", fakeJWT(time.Now().Add(90*time.Minute)))
	r := runShared(t, state, "", "env", "status")
	if !strings.Contains(r.out, "expires in") {
		t.Fatalf("status should show expiry: %+v", r)
	}

	t.Setenv("API_TOKEN", fakeJWT(time.Now().Add(-2*time.Hour)))
	r = runShared(t, state, "", "env", "status")
	if !strings.Contains(r.out, "EXPIRED") {
		t.Fatalf("status should flag an expired token: %+v", r)
	}
	r = runShared(t, state, "", "doctor")
	if !strings.Contains(r.out+r.err, "EXPIRED") || !strings.Contains(r.out+r.err, "run api login") {
		t.Fatalf("doctor should flag an expired token:\n%s%s", r.out, r.err)
	}

	// an opaque token has no expiry text at all
	t.Setenv("API_TOKEN", "opaque-token-value")
	if r := runShared(t, state, "", "env", "status"); strings.Contains(r.out, "expires") || strings.Contains(r.out, "EXPIRED") {
		t.Fatalf("opaque token: %+v", r)
	}
}

func TestSecretListIncludesSecretsReferencedByConfigAndShowAdoptsThem(t *testing.T) {
	// stored before the names index existed: not in the index, but a login uses it
	mem := secretstore.NewMemory()
	_ = mem.Set("old-pw", secretstore.Secret{Value: "x", Origins: []string{"https://api.example.com"}})
	withSecrets(t, mem, nil)
	state := t.TempDir()
	clearSession(t)
	writeConfig(t, `
[env.dev]
url = "https://api.example.com"
[env.dev.login]
path = "/auth"
vars = { user = "me", password = "@keychain:old-pw" }
[env.thien]
url = "https://t.example.com"
[env.thien.login]
path = "/auth"
vars = { user = "me", password = "@keychain:not-stored-yet" }
`)
	r := runShared(t, state, "", "secret", "list")
	if !strings.Contains(r.out, "old-pw") || !strings.Contains(r.out, "bound") || !strings.Contains(r.out, "not-stored-yet") || !strings.Contains(r.out, "missing") {
		t.Fatalf("config references not listed:\n%s", r.out)
	}
	if data, _ := os.ReadFile(filepath.Join(state, "api", "secrets")); len(data) != 0 {
		t.Fatalf("listing must not write the index: %q", data)
	}
	// show adopts it
	runShared(t, state, "", "secret", "show", "old-pw")
	if data, _ := os.ReadFile(filepath.Join(state, "api", "secrets")); string(data) != "old-pw\n" {
		t.Fatalf("show did not adopt the secret: %q", data)
	}
	// doctor also looks at secrets the config refers to
	if r := runShared(t, state, "", "doctor"); !strings.Contains(r.out, "not in the Keychain") {
		t.Fatalf("doctor ignored a referenced-but-missing secret:\n%s", r.out)
	}
}
