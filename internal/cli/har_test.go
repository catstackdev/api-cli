package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── HAR export ─────────────────────────────────────────────────────────────

func TestHARExport(t *testing.T) {
	server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "sid=COOKIE-CANARY")
		_, _ = w.Write([]byte(`{"ok":true,"accessToken":"BODY-CANARY"}`))
	})
	t.Setenv("API_TOKEN", "BEARER-CANARY")
	path := filepath.Join(t.TempDir(), "out.har")

	r := run(t, "", "post", "/login", `{"user":"me","password":"PW-CANARY"}`, "--har", path)
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	run(t, "", "get", "/second", "--har", path) // appends
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "CANARY") {
		t.Fatalf("a secret reached the HAR file:\n%s", data)
	}
	var doc struct {
		Log struct {
			Version string
			Entries []struct {
				Request  struct{ Method, URL string }
				Response struct{ Status int }
			}
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Log.Version != "1.2" || len(doc.Log.Entries) != 2 ||
		doc.Log.Entries[0].Request.Method != "POST" || doc.Log.Entries[1].Request.Method != "GET" || doc.Log.Entries[0].Response.Status != 200 {
		t.Fatalf("%v %+v", err, doc)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}

	// --har-secrets keeps them and warns
	secretPath := filepath.Join(t.TempDir(), "secrets.har")
	r = run(t, "", "get", "/x", "--har", secretPath, "--har-secrets")
	if b, _ := os.ReadFile(secretPath); !strings.Contains(string(b), "BEARER-CANARY") || !strings.Contains(r.err, "contains secrets") {
		t.Fatalf("--har-secrets: %q / %q", b, r.err)
	}
	// an unwritable HAR path is a warning, never a failed request
	r = run(t, "", "get", "/x", "--har", "/proc/definitely/not/writable.har")
	if r.code != 0 || !strings.Contains(r.err, "could not write HAR") {
		t.Fatalf("%+v", r)
	}
}
