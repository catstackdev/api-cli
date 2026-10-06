package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── doctor ─────────────────────────────────────────────────────────────────

func TestDoctor(t *testing.T) {
	dir := projectWith(t, map[string]string{".env": "A=1\n", ".env.staging": "A=1\n"})
	_ = os.Chmod(filepath.Join(dir, ".env.staging"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o700)
	t.Setenv("API_SHIM", "")
	t.Setenv("API_BASE_URL", "http://example.org")
	t.Setenv("API_TOKEN", "")
	t.Setenv("API_ENV", "")
	t.Setenv("API_PAGER", "definitely-not-a-pager")

	r := run(t, "", "doctor")
	if r.code != 0 {
		t.Fatalf("warnings alone must exit 0: %+v", r)
	}
	for _, want := range []string{
		"shell shim", "not detected",
		"no token",
		"not https",
		".env.staging (0644)",    // permissions
		"does not ignore .env",   // .gitignore
		"definitely-not-a-pager", // pager missing
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, r.out)
		}
	}

	// fixed: shim marker, 0600, ignored, https + token: no warnings about them
	_ = os.Chmod(filepath.Join(dir, ".env.staging"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*\n"), 0o644)
	t.Setenv("API_SHIM", "1")
	t.Setenv("API_BASE_URL", "https://api.example.com")
	t.Setenv("API_TOKEN", "abcdefghijklmnop")
	t.Setenv("API_PAGER", "")
	r = run(t, "", "doctor")
	for _, bad := range []string{"not detected", "no token", "not https", "readable by others", "does not ignore"} {
		if strings.Contains(r.out, bad) {
			t.Errorf("unexpected %q:\n%s", bad, r.out)
		}
	}
	if !strings.Contains(r.out, "token abcdefghijkl…") || strings.Contains(r.out, "mnop") {
		t.Fatalf("token must be masked:\n%s", r.out)
	}

	// a broken config is a hard failure (exit 1) and never echoes the secret
	writeConfig(t, "[env.dev]\ntoken = \"hunter2\"\n")
	r = run(t, "", "doctor")
	if r.code != 1 || !strings.Contains(r.out, "unknown key") || strings.Contains(r.out+r.err, "hunter2") {
		t.Fatalf("broken config: %+v", r)
	}
}

// The shim marker carries the contract number it implements, so a shim loaded
// before the binary learned a new shell-changing command is reported, not
// silently trusted.
func TestDoctorShimContract(t *testing.T) {
	for _, c := range []struct {
		marker, want, notWant string
	}{
		{shimContract, "loaded", "differs"},
		{"0", "older than this binary", "loaded:"},
		{"99", "newer than this binary", "loaded:"},
		{"junk", "older than this binary", "loaded:"},
	} {
		t.Setenv("API_SHIM", c.marker)
		r := run(t, "", "doctor")
		if !strings.Contains(r.out, c.want) || strings.Contains(r.out, c.notWant) {
			t.Errorf("API_SHIM=%q: want %q and not %q:\n%s", c.marker, c.want, c.notWant, r.out)
		}
	}
}

func TestDoctorOnline(t *testing.T) {
	srv, _ := envServer(t)
	t.Setenv("API_BASE_URL", srv.URL)
	if r := run(t, "", "doctor", "--online"); !strings.Contains(r.out, "answered HTTP 200") {
		t.Fatalf("%+v", r)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	t.Setenv("API_BASE_URL", url)
	if r := run(t, "", "doctor", "--online"); r.code != 1 || !strings.Contains(r.out, "online") {
		t.Fatalf("unreachable must fail: %+v", r)
	}
}

// ── doctor --fix ───────────────────────────────────────────────────────────

func TestDoctorFixTightensEnvPermissionsOnly(t *testing.T) {
	dir := projectWith(t, map[string]string{".env": "A=1\n", ".env.dev": "A=1\n", ".env.example": "A=\n"})
	outside := filepath.Join(t.TempDir(), "outside-secret")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	_ = os.Symlink(outside, filepath.Join(dir, ".env.linked"))
	for _, f := range []string{".env", ".env.dev", ".env.example"} {
		_ = os.Chmod(filepath.Join(dir, f), 0o644)
	}
	t.Setenv("API_BASE_URL", "https://api.example.com")

	if r := run(t, "", "doctor"); !strings.Contains(r.out, "readable by others") {
		t.Fatalf("setup: expected the warning first:\n%s", r.out)
	}
	r := run(t, "", "doctor", "--fix")
	if !strings.Contains(r.err, "✓ .env: 0644 → 0600") || !strings.Contains(r.err, "✓ .env.dev: 0644 → 0600") {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, "readable by others") {
		t.Fatalf("still warns after --fix:\n%s", r.out)
	}
	for _, f := range []string{".env", ".env.dev"} {
		if st, _ := os.Stat(filepath.Join(dir, f)); st.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", f, st.Mode().Perm())
		}
	}
	// the example file is documentation, not a secret: left alone
	if st, _ := os.Stat(filepath.Join(dir, ".env.example")); st.Mode().Perm() != 0o644 {
		t.Errorf(".env.example was changed: %v", st.Mode().Perm())
	}
	// a symlink must never be followed out of the project
	if st, _ := os.Stat(outside); st.Mode().Perm() != 0o644 {
		t.Fatalf("chmod followed a symlink out of the project: %v", st.Mode().Perm())
	}
}

func TestDoctorJSON(t *testing.T) {
	r := run(t, "", "doctor", "--json")
	var got struct {
		OK       bool `json:"ok"`
		Failures int  `json:"failures"`
		Checks   []struct{ Name, Level, Detail string }
	}
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.out)
	}
	if len(got.Checks) == 0 || got.Checks[0].Name != "binary" || got.Checks[0].Level != "ok" {
		t.Fatalf("%+v", got)
	}
	if got.OK != (r.code == 0) {
		t.Fatalf("ok=%v but exit code %d", got.OK, r.code)
	}
	if strings.Contains(r.out, "✓") || strings.Contains(r.err, "all good") {
		t.Fatalf("JSON mode must not mix in the human summary: %+v", r)
	}
}
