package secretstore

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	s := NewMemory()
	if _, err := s.Get("upscale-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.Set("upscale-dev", Secret{Value: "p@ss word'\"$(x)"}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("upscale-dev"); err != nil || v.Value != "p@ss word'\"$(x)" || len(v.Origins) != 0 {
		t.Fatalf("%+v %v", v, err)
	}
	if err := s.Delete("upscale-dev"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("upscale-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestNamesAreValidated(t *testing.T) {
	for _, bad := range []string{"", "a b", "a;b", "$(x)", "../x", "-flag", "a/b", strings.Repeat("a", 65), "é"} {
		if ValidateName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"upscale-dev", "a", "x.y_z-1"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	if Service("x") != "api:x" {
		t.Fatal("secrets are namespaced")
	}
	s := NewMemory()
	if s.Set("bad name", Secret{Value: "x"}) == nil || func() bool { _, err := s.Get("../x"); return err == nil }() {
		t.Fatal("the store must validate names itself")
	}
}

// TestKeychain talks to the REAL macOS Keychain with a throwaway item that it
// removes again. It is opt-in: API_TEST_KEYCHAIN=1 (and a normal, unsandboxed
// session).
func TestKeychain(t *testing.T) {
	if os.Getenv("API_TEST_KEYCHAIN") == "" {
		t.Skip("set API_TEST_KEYCHAIN=1 to test the real Keychain")
	}
	if _, err := exec.LookPath("security"); err != nil {
		t.Skip("no `security` command (not macOS)")
	}
	s := Default()
	name := "test-" + strings.ReplaceAll(t.Name(), "/", "-") + "-tmp"
	_ = s.Delete(name)
	t.Cleanup(func() { _ = s.Delete(name) })

	if _, err := s.Get(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fresh name must be not found, got %v", err)
	}
	for _, secret := range []string{"simple", "with space and 'quotes' \"dq\" $(cmd) `bt` ;|&", "päss-wörd-ünïcode"} {
		if err := s.Set(name, Secret{Value: secret}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if got, err := s.Get(name); err != nil || got.Value != secret || len(got.Origins) != 0 {
			t.Fatalf("round trip of %q: got %+v, %v", secret, got, err)
		}
	}
	if err := s.Delete(name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestOriginBinding(t *testing.T) {
	bound := Secret{Value: "x", Origins: []string{"https://api.dev.example.com"}}
	if !bound.AllowedFor("https://api.dev.example.com") || bound.AllowedFor("https://evil.example") || bound.AllowedFor("http://api.dev.example.com") {
		t.Fatal("a bound secret is allowed for exactly its origins (scheme and host)")
	}
	if !(Secret{Value: "x"}).AllowedFor("https://anywhere.example") {
		t.Fatal("an unbound secret has no restriction")
	}
}

func TestNormalizeOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://API.Dev.Example.com/graphql?x=1": "https://api.dev.example.com",
		"http://localhost:3000/path":              "http://localhost:3000",
		" https://a.example ":                     "https://a.example",
		"https://Host.Example:443/x":              "https://host.example",
		"http://host.example:80":                  "http://host.example",
		"https://host.example:8443":               "https://host.example:8443",
		"http://host.example:443":                 "http://host.example:443", // not the default for http
		"http://[::1]:80/x":                       "http://[::1]",
		"http://[::1]:3000":                       "http://[::1]:3000",
		"HTTPS://HOST.example":                    "https://host.example",
	} {
		if got, err := NormalizeOrigin(in); err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "api.example.com", "ftp://x", "https://", "javascript:alert(1)", "/relative"} {
		if _, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, err := NormalizeOrigins([]string{"https://A.example/x", "https://a.example/y", "http://b.example"})
	if err != nil || len(got) != 2 || got[0] != "https://a.example" || got[1] != "http://b.example" {
		t.Fatalf("de-duplicate: %v %v", got, err)
	}
}
