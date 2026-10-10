package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// One row per settable key. This pins what Set, Get, SetInFile (the in-place text
// edit) and the project-file policy do TODAY, key by key, so that changing how
// keys are declared cannot change any of them unnoticed.
type keyCase struct {
	pattern string // as listed in SettableKeys
	key     string // a concrete key for it
	value   string
	want    string // what Get returns after Set
	bad     string // a value Set must refuse ("" when every value is accepted)

	// peek reads the value back for keys Get does not serve (login.*).
	peek func(Config) string

	// projectSets is whether a value in a PROJECT file reaches the merged config
	// when no global file defines the env. false = honoured from the global file
	// only. The zero value is the safe one: a key nobody classified is global-only.
	projectSets bool
}

func login(c Config) *Login { return c.Env["dev"].Login }

var keyCases = []keyCase{
	{pattern: "default_env", key: "default_env", value: "dev", want: "dev", projectSets: true},

	{pattern: "defaults.timeout", key: "defaults.timeout", value: "30s", want: "30s", bad: "soon", projectSets: true},
	{pattern: "defaults.connect_timeout", key: "defaults.connect_timeout", value: "5s", want: "5s", bad: "0", projectSets: true},
	{pattern: "defaults.retry", key: "defaults.retry", value: "3", want: "3", bad: "-1", projectSets: true},
	{pattern: "defaults.color", key: "defaults.color", value: "never", want: "never", bad: "pink", projectSets: true},
	{pattern: "defaults.summary", key: "defaults.summary", value: "true", want: "true", bad: "maybe", projectSets: true},
	{pattern: "defaults.token_cache", key: "defaults.token_cache", value: "true", want: "true", bad: "maybe"},
	{pattern: "defaults.auto_login", key: "defaults.auto_login", value: "true", want: "true", bad: "maybe"},
	{pattern: "defaults.editor", key: "defaults.editor", value: "nvim", want: "nvim", bad: "a\nb"},

	{pattern: "env.<name>.url", key: "env.dev.url", value: "https://dev.example", want: "https://dev.example", projectSets: true},
	{pattern: "env.<name>.graphql", key: "env.dev.graphql", value: "https://dev.example/graphql", want: "https://dev.example/graphql", projectSets: true},
	{pattern: "env.<name>.endpoints", key: "env.dev.endpoints", value: "user,auth", want: "user,auth", projectSets: true},
	{pattern: "env.<name>.endpoint_prefix", key: "env.dev.endpoint_prefix", value: "v2/", want: "v2/", bad: "../x", projectSets: true},
	{pattern: "env.<name>.openapi", key: "env.dev.openapi", value: "/openapi.json", want: "/openapi.json", bad: "https://evil.example/x", projectSets: true},
	{pattern: "env.<name>.subscriptions", key: "env.dev.subscriptions", value: "/graphql", want: "/graphql", bad: "wss://evil.example", projectSets: true},
	{pattern: "env.<name>.ws_protocol", key: "env.dev.ws_protocol", value: "graphql-ws", want: "graphql-ws", bad: "other", projectSets: true},
	{pattern: "env.<name>.ws_token_in_init", key: "env.dev.ws_token_in_init", value: "true", want: "true", bad: "maybe", projectSets: true},
	{pattern: "env.<name>.token_env", key: "env.dev.token_env", value: "API_TOKEN_DEV", want: "API_TOKEN_DEV", bad: "HOME"},
	{pattern: "env.<name>.headers.<Header>", key: "env.dev.headers.X-Tenant", value: "dev", want: "dev", bad: "a\nb", projectSets: true},

	{pattern: "vars.base_url", key: "vars.base_url", value: "VITE_API_URL", want: "VITE_API_URL", bad: "a b", projectSets: true},
	{pattern: "vars.graphql_url", key: "vars.graphql_url", value: "VITE_GRAPHQL_URL", want: "VITE_GRAPHQL_URL", bad: "a b", projectSets: true},
	{pattern: "vars.env_suffix", key: "vars.env_suffix", value: "dev", want: "dev", bad: "../x", projectSets: true},

	{pattern: "env.<name>.login.path", key: "env.dev.login.path", value: "/auth/login", peek: func(c Config) string { return login(c).Path }, projectSets: true},
	{pattern: "env.<name>.login.gql", key: "env.dev.login.gql", value: "mutation { login }", peek: func(c Config) string { return login(c).GQL }, projectSets: true},
	{pattern: "env.<name>.login.endpoint", key: "env.dev.login.endpoint", value: "v2/auth", peek: func(c Config) string { return login(c).Endpoint }, projectSets: true},
	{pattern: "env.<name>.login.token_path", key: "env.dev.login.token_path", value: ".data.token", bad: "data.token", peek: func(c Config) string { return login(c).TokenPath }, projectSets: true},
	{pattern: "env.<name>.login.vars.<name>", key: "env.dev.login.vars.input.username", value: "me@x.com", peek: func(c Config) string { return login(c).Vars["input.username"] }, projectSets: true},

	{pattern: "alias.<name>", key: "alias.me", value: "get /me", want: "get /me", projectSets: true},
}

func readBack(c Config, k keyCase) (string, bool) {
	if k.peek != nil {
		v := k.peek(c)
		return v, v != ""
	}
	return Get(c, k.key)
}

func expected(k keyCase) string {
	if k.peek != nil {
		return k.value
	}
	return k.want
}

// Every key Set understands has a row, and every row is a key Set understands.
func TestEveryKeyHasARow(t *testing.T) {
	rows := map[string]bool{}
	for _, k := range keyCases {
		rows[k.pattern] = true
	}
	for _, p := range SettableKeys {
		if !rows[p] {
			t.Errorf("SettableKeys lists %q but keys_test.go has no row for it", p)
		}
		delete(rows, p)
	}
	for p := range rows {
		t.Errorf("keys_test.go has a row for %q, which SettableKeys does not list", p)
	}
}

func TestSetThenGet(t *testing.T) {
	for _, k := range keyCases {
		t.Run(k.pattern, func(t *testing.T) {
			var c Config
			if err := Set(&c, k.key, k.value); err != nil {
				t.Fatalf("Set(%q, %q): %v", k.key, k.value, err)
			}
			got, ok := readBack(c, k)
			if !ok || got != expected(k) {
				t.Fatalf("after Set(%q, %q), read back %q (set=%v), want %q", k.key, k.value, got, ok, expected(k))
			}
			if k.peek == nil {
				var empty Config
				if v, ok := Get(empty, k.key); ok || v != "" {
					t.Fatalf("an unset key reads as %q (set=%v)", v, ok)
				}
			}
		})
	}
}

func TestSetRefusesABadValueAndLeavesTheConfigUntouched(t *testing.T) {
	for _, k := range keyCases {
		if k.bad == "" {
			continue
		}
		t.Run(k.pattern, func(t *testing.T) {
			var c Config
			if err := Set(&c, k.key, k.bad); err == nil {
				t.Fatalf("Set(%q, %q) was accepted", k.key, k.bad)
			}
			// an empty container left behind is harmless (the caller drops c on an
			// error); what must not happen is a setting changing
			if text, _ := toml.Marshal(c); len(text) != 0 {
				t.Fatalf("a refused value still changed the config:\n%s", text)
			}
		})
	}
}

// A secret-looking key is refused by name; the error names the key and never the value.
func TestSecretLookingKeysAreRefusedWithoutEchoingTheValue(t *testing.T) {
	const secret = "hunter2-do-not-print"
	for _, key := range []string{
		"env.dev.password", "env.dev.api_key", "env.dev.headers.X-API-Key", "defaults.secret",
		"env.dev.login.vars.password", "env.dev.login.vars.input.token", "alias.password",
	} {
		var c Config
		err := Set(&c, key, secret)
		if err == nil {
			t.Errorf("Set(%q) accepted a secret-looking key", key)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("Set(%q) echoes the value: %v", key, err)
		}
		if !strings.Contains(err.Error(), key) && !strings.Contains(err.Error(), "secret") {
			t.Errorf("Set(%q): the error says nothing about the key: %v", key, err)
		}
	}
	// a reference is the allowed way to give a login variable a secret
	var c Config
	if err := Set(&c, "env.dev.login.vars.password", "@keychain:dev"); err != nil {
		t.Errorf("a @keychain: reference must be accepted: %v", err)
	}
}

func TestUnknownKeysAreRefused(t *testing.T) {
	for _, key := range []string{"nope", "defaults.nope", "vars.nope", "env.dev.nope", "env.dev", "env.dev.login.nope", "env.dev.headers", "x.y.z"} {
		var c Config
		if err := Set(&c, key, "v"); err == nil {
			t.Errorf("Set(%q) was accepted", key)
		}
	}
}

// SetInFile edits the text in place, re-parses it and demands it equals what Set
// built. Every key must survive that, starting from the commented templates.
func TestEveryKeyCanBeEditedInPlace(t *testing.T) {
	for _, tpl := range []struct {
		name string
		text string
	}{{"global template", Template}, {"project template", Render(Project, "", "")}, {"empty file", ""}} {
		for _, k := range keyCases {
			t.Run(tpl.name+"/"+k.pattern, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				if tpl.text != "" {
					write(t, path, tpl.text)
				}
				preserved, err := SetInFile(path, k.key, k.value)
				if err != nil {
					t.Fatalf("SetInFile(%q): %v", k.key, err)
				}
				if tpl.text != "" && !preserved {
					t.Errorf("SetInFile(%q) rewrote the file and lost its comments", k.key)
				}
				got, err := Read(path)
				if err != nil {
					t.Fatal(err)
				}
				var want Config
				if err := Set(&want, k.key, k.value); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("the file holds %+v, Set built %+v", got, want)
				}
				if tpl.text != "" {
					data, _ := os.ReadFile(path)
					if !strings.Contains(string(data), "# ──") {
						t.Errorf("the template's comments are gone after SetInFile(%q)", k.key)
					}
				}
			})
		}
	}
}

// What a PROJECT file (possibly from a cloned repo) can set, key by key.
func TestWhatAProjectFileMayOverride(t *testing.T) {
	for _, k := range keyCases {
		t.Run(k.pattern, func(t *testing.T) {
			global, dir := setup(t)
			_ = global // never written: no global config at all
			project := filepath.Join(dir, ".api.toml")
			if _, err := SetInFile(project, k.key, k.value); err != nil {
				t.Fatalf("SetInFile: %v", err)
			}
			l, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got, ok := readBack(l.Config, k)
			if k.projectSets {
				if !ok || got != expected(k) {
					t.Fatalf("a project file should be able to set %s: read %q (set=%v)", k.key, got, ok)
				}
				return
			}
			if ok {
				t.Fatalf("%s is global-only but a project file set it to %q", k.key, got)
			}
			if len(l.Warnings) == 0 {
				t.Errorf("%s was dropped from the project file without a warning", k.key)
			}
		})
	}
}

// The same key in the GLOBAL file always counts.
func TestEveryKeyIsHonouredInTheGlobalFile(t *testing.T) {
	for _, k := range keyCases {
		t.Run(k.pattern, func(t *testing.T) {
			global, dir := setup(t)
			if _, err := SetInFile(global, k.key, k.value); err != nil {
				t.Fatalf("SetInFile: %v", err)
			}
			l, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got, ok := readBack(l.Config, k); !ok || got != expected(k) {
				t.Fatalf("the global file's %s reads %q (set=%v), want %q", k.key, got, ok, expected(k))
			}
		})
	}
}

// A project cannot move the server of a global env that holds a token, and its
// token_env / login do not follow it (gotchas 10 and 33).
func TestAProjectCannotMoveAnAnchoredGlobalEnv(t *testing.T) {
	global, dir := setup(t)
	write(t, global, "[env.dev]\nurl = \"https://dev.example\"\ntoken_env = \"API_TOKEN_DEV\"\n")
	write(t, filepath.Join(dir, ".api.toml"), "[env.dev]\nurl = \"https://evil.example\"\ntoken_env = \"API_TOKEN_OTHER\"\n")
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Env["dev"].TokenEnv != "" {
		t.Fatalf("the token_env followed the url to %s: %q", l.Env["dev"].URL, l.Env["dev"].TokenEnv)
	}
	if len(l.Warnings) == 0 {
		t.Fatal("no warning")
	}
}
