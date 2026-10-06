package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The in-place editor works on text people wrote and commented by hand. Whatever
// it is given, SetInFile must not panic and must leave a file that parses to
// exactly the intended config (in place, or via the rewrite fallback).
func FuzzSetInFile(f *testing.F) {
	seeds := []string{
		"",
		"# only a comment\n",
		"default_env = \"dev\"\n[defaults]\n# retry = 0\n",
		"[defaults]\nretry = 1\n[env.dev]\nurl = \"https://x.example\"\n",
		"[env.dev]\nheaders = { A = \"b\" }\n",
		"[env.dev.login.vars]\nuser = \"me\"\n",
		"[alias]\nme = \"get /me\"\n",
		"[vars]\n# env_suffix = \"dev\"\n",
		"\r\n[defaults]\r\nretry = 2\r\n",
		"a = 1\n[[x]]\n",
	}
	for i, s := range seeds {
		f.Add(s, uint8(i))
	}
	type kv struct{ key, value string }
	keys := []kv{
		{"defaults.retry", "3"},
		{"defaults.summary", "true"},
		{"default_env", "dev"},
		{"env.dev.url", "https://x.example"},
		{"env.dev.headers.X-A", "1"},
		{"env.dev.login.vars.user", "me"},
		{"alias.me", "get /x"},
		{"vars.env_suffix", "dev"},
		{"defaults.token_cache", "true"},
	}
	f.Fuzz(func(t *testing.T, text string, pick uint8) {
		if len(text) > 4096 {
			t.Skip()
		}
		c := keys[int(pick)%len(keys)]
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := Read(path)
		if err != nil {
			t.Skip() // not a valid config to begin with
		}
		want := before
		// Set mutates maps: work on a re-read copy
		want, _ = Read(path)
		if err := Set(&want, c.key, c.value); err != nil {
			t.Skip()
		}
		if _, err := SetInFile(path, c.key, c.value); err != nil {
			t.Fatalf("SetInFile(%q, %s=%s): %v", text, c.key, c.value, err)
		}
		got, err := Read(path)
		if err != nil {
			t.Fatalf("result does not parse: %v\n--- input\n%q", err, text)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("edit changed the wrong thing\n--- input\n%q\n got  %+v\n want %+v", text, got, want)
		}
	})
}

func FuzzParseText(f *testing.F) {
	f.Add("[env.dev]\nurl = \"x\"\n")
	f.Add("default_env = 1")
	f.Add("[env.a.login.vars]\npassword = \"literal\"\n")
	f.Fuzz(func(t *testing.T, text string) {
		c, err := ParseText([]byte(text), "f.toml")
		if err != nil {
			return
		}
		// whatever parsed can be linted and merged without panicking
		Lint(c, true, nil)
		Lint(c, false, nil)
		_ = Merge(Config{}, c)
	})
}
