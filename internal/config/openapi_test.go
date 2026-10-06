package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env.<name>.openapi says where an env's OpenAPI document lives, for
// `api openapi sync`. It is a PATH on the env's server, never a URL: a config
// file can come from a cloned repo, and it must not choose which host you fetch.
func TestValidateOpenAPIPath(t *testing.T) {
	for _, ok := range []string{"", "/openapi.json", "/v3/api-docs/public", "/docs/spec.json?group=a&v=2", "/api%20x/spec.json"} {
		if err := ValidateOpenAPIPath(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"openapi.json",                 // relative
		"https://evil.example/spec",    // a URL
		"//evil.example/spec",          // protocol-relative: a host in disguise
		"/a/../b",                      // traversal
		"/has space",                   // would break completion and curl lines
		"/new\nline",                   // control character
		"/esc\x1b[2J",                  // terminal escape
		"/" + strings.Repeat("a", 600), // absurd length
	} {
		if err := ValidateOpenAPIPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOpenAPIKeySetGetAndEditInPlace(t *testing.T) {
	var c Config
	if err := Set(&c, "env.dev.openapi", "/docs/spec.json"); err != nil {
		t.Fatal(err)
	}
	if v, ok := Get(c, "env.dev.openapi"); !ok || v != "/docs/spec.json" {
		t.Fatalf("%q %v", v, ok)
	}
	for _, bad := range []string{"https://evil.example/x", "//evil.example/x", "docs/x", "/a b"} {
		if err := Set(&c, "env.dev.openapi", bad); err == nil {
			t.Errorf("Set accepted %q", bad)
		}
	}
	// the in-place editor keeps the file's comments and the value round-trips
	path := filepath.Join(t.TempDir(), "c.toml")
	write(t, path, "# my comment\n[env.dev]\nurl = \"http://localhost:3000\"\n")
	preserved, err := SetInFile(path, "env.dev.openapi", "/openapi.json")
	if err != nil || !preserved {
		t.Fatalf("preserved=%v err=%v", preserved, err)
	}
	back, err := Read(path)
	if err != nil || back.Env["dev"].OpenAPI != "/openapi.json" || back.Env["dev"].URL != "http://localhost:3000" {
		t.Fatalf("%+v %v", back.Env["dev"], err)
	}
	text, _ := os.ReadFile(path)
	if !strings.Contains(string(text), "# my comment") {
		t.Fatalf("comment lost:\n%s", text)
	}
}

func TestReadRejectsABadOpenAPIPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	write(t, path, "[env.dev]\nopenapi = \"https://evil.example/x\"\n")
	_, err := Read(path)
	if err == nil || !strings.Contains(err.Error(), "openapi") {
		t.Fatalf("%v", err)
	}
}

func TestProjectOverridesAndKeepsAnEnvsOpenAPIPath(t *testing.T) {
	merged := Merge(Config{Env: map[string]Env{"dev": {URL: "http://a", OpenAPI: "/global.json"}}},
		Config{Env: map[string]Env{"dev": {OpenAPI: "/project.json"}, "other": {OpenAPI: "/o.json"}}})
	if merged.Env["dev"].OpenAPI != "/project.json" || merged.Env["other"].OpenAPI != "/o.json" {
		t.Fatalf("%+v", merged.Env)
	}
	kept := Merge(Config{Env: map[string]Env{"dev": {URL: "http://a", OpenAPI: "/global.json"}}}, Config{Env: map[string]Env{"dev": {}}})
	if kept.Env["dev"].OpenAPI != "/global.json" {
		t.Fatalf("an empty project value must not erase it: %+v", kept.Env["dev"])
	}
}

func TestBothTemplatesDocumentTheOpenAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.toml")
	for name, tpl := range map[string]string{"global": Render(Global, "", ""), "project": Render(Project, "", "")} {
		if !strings.Contains(tpl, `# openapi = "/openapi.json"`) || !strings.Contains(tpl, "api openapi sync") {
			t.Errorf("%s template does not document openapi:\n%s", name, tpl)
		}
		// uncommenting the env block and the key must stay valid
		on := strings.NewReplacer("# [env.dev]", "[env.dev]", "# [env.local]", "[env.local]", `# openapi = `, `openapi = `).Replace(tpl)
		write(t, path, on)
		c, err := Read(path)
		if err != nil {
			t.Fatalf("%s with the key uncommented: %v", name, err)
		}
		got := ""
		for _, e := range c.Env {
			got = e.OpenAPI
		}
		if got != "/openapi.json" {
			t.Errorf("%s: the example did not parse as the key: %+v", name, c.Env)
		}
	}
}
