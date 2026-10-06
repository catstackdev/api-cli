package secretstore

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecretTool installs a `secret-tool` that keeps items in a directory and
// logs every argv, so the tests can run on any OS without a Secret Service. It
// mimics the real tool: store reads the secret from stdin, lookup prints it
// (exit 1, no output, when absent), clear exits 0 even when nothing matched.
// FAKE_DOWN=1 makes every call fail like a missing D-Bus session.
func fakeSecretTool(t *testing.T) (bin, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "secret-tool")
	argvLog = filepath.Join(dir, "argv.log")
	items := filepath.Join(dir, "items")
	_ = os.MkdirAll(items, 0o700)
	script := `#!/bin/sh
echo "$@" >> '` + argvLog + `'
if [ -n "$FAKE_DOWN" ]; then echo "secret-tool: Cannot autolaunch D-Bus without X11 \$DISPLAY" >&2; exit 1; fi
cmd=$1; shift
service=""
while [ $# -gt 0 ]; do
  [ "$1" = "service" ] && service=$2
  shift
done
f='` + items + `'/"$service"
case "$cmd" in
  store)  cat > "$f" ;;
  lookup) [ -f "$f" ] || exit 1; cat "$f" ;;
  clear)  rm -f "$f" ;;
  *) echo "unknown command $cmd" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argvLog
}

func TestSecretServiceRoundTrip(t *testing.T) {
	bin, argvLog := fakeSecretTool(t)
	s := newSecretService(bin)

	if _, err := s.Get("upscale-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing item is ErrNotFound, got %v", err)
	}
	pw := "p@ss wörd'\"$(touch pwned)\n2nd line"
	if err := s.Set("upscale-dev", Secret{Value: pw, Origins: []string{"https://api.dev.example.com"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("upscale-dev")
	if err != nil || got.Value != pw || !got.AllowedFor("https://api.dev.example.com") || got.AllowedFor("https://evil.example") {
		t.Fatalf("%+v %v", got, err)
	}
	// binding survives replacing the value is the caller's rule; here: overwrite works
	if err := s.Set("upscale-dev", Secret{Value: "second"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("upscale-dev"); got.Value != "second" || len(got.Origins) != 0 {
		t.Fatalf("%+v", got)
	}
	if err := s.Delete("upscale-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("upscale-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.Delete("upscale-dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing item is ErrNotFound (clear itself exits 0): %v", err)
	}
	// entries are namespaced exactly like the Keychain's
	log, _ := os.ReadFile(argvLog)
	if !strings.Contains(string(log), "service api:upscale-dev account api") {
		t.Fatalf("attributes:\n%s", log)
	}
}

// The secret travels over stdin only: an argument shows up in `ps`.
func TestSecretServiceNeverPutsTheSecretInAnArgument(t *testing.T) {
	bin, argvLog := fakeSecretTool(t)
	s := newSecretService(bin)
	const secret = "tok-SECRET-0123456789"
	sec := Secret{Value: secret, Origins: []string{"https://a.example"}}
	if err := s.Set("n", sec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("n"); err != nil {
		t.Fatal(err)
	}
	_ = s.Delete("n")
	log, _ := os.ReadFile(argvLog)
	// the plain value, base64 of it, and the exact entry that gets stored
	for _, leak := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))[:12], encode(sec), encode(Secret{Value: secret})} {
		if strings.Contains(string(log), leak) {
			t.Fatalf("the secret reached an argument list (%q):\n%s", leak, log)
		}
	}
}

func TestSecretServiceValidatesNamesBeforeRunningAnything(t *testing.T) {
	bin, argvLog := fakeSecretTool(t)
	s := newSecretService(bin)
	for _, bad := range []string{"", "a b", "$(x)", "-flag", "../x"} {
		if _, err := s.Get(bad); err == nil {
			t.Errorf("Get(%q) accepted", bad)
		}
		if err := s.Set(bad, Secret{Value: "x"}); err == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Fatal("secret-tool ran for an invalid name")
	}
}

// A session without a Secret Service is an error to show, not "not found":
// "not found" would let `api` fall through and prompt for a password it cannot save.
func TestSecretServiceDownIsNotNotFound(t *testing.T) {
	bin, _ := fakeSecretTool(t)
	t.Setenv("FAKE_DOWN", "1")
	s := newSecretService(bin)
	_, err := s.Get("x")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "D-Bus") {
		t.Fatalf("%v", err)
	}
	if err := s.Set("x", Secret{Value: "v"}); err == nil || !strings.Contains(err.Error(), "D-Bus") {
		t.Fatalf("%v", err)
	}
	if err := s.Delete("x"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestSecretServiceCorruptEntryIsAnError(t *testing.T) {
	bin, _ := fakeSecretTool(t)
	s := newSecretService(bin)
	// an item written by something else, with a marker that does not decode
	items := filepath.Join(filepath.Dir(bin), "items")
	_ = os.WriteFile(filepath.Join(items, "api:bad"), []byte("v2:***"), 0o600)
	if _, err := s.Get("bad"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
