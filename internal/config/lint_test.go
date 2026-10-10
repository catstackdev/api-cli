package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func lint(t *testing.T, text string, project bool, known ...string) []Issue {
	t.Helper()
	c, err := ParseText([]byte(text), "t.toml")
	if err != nil {
		t.Fatal(err)
	}
	k := map[string]bool{}
	for _, n := range known {
		k[n] = true
	}
	return Lint(c, project, k)
}

func messages(issues []Issue) string {
	var b strings.Builder
	for _, i := range issues {
		kind := "warn"
		if i.Error {
			kind = "error"
		}
		b.WriteString(kind + ": " + i.Message + "\n")
	}
	return b.String()
}

func TestLintFindsWhatParsingAllows(t *testing.T) {
	got := messages(lint(t, `
default_env = "nope"
[env.a]
url = "ftp://x"
graphql = "not a url"
headers = { Authorization = "Bearer x", "X-API-Key" = "k", "X-Tenant" = "t" }
[env.a.login]
endpoint = "v2/auth"
[env.b.login]
path = "/a"
gql = "mutation { x }"
`, false))
	for _, want := range []string{
		`warn: default_env "nope"`,
		`error: env.a.url "ftp://x" is not an http(s) URL`,
		`error: env.a.graphql "not a url"`,
		`warn: env.a.headers has Authorization`,
		`warn: env.a.headers.X-API-Key looks like a credential`,
		`error: env.a.login: login needs exactly one of path`,
		`warn: env.a.login.endpoint only applies to a GraphQL login`,
		`error: env.b.login: login needs exactly one of path`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestLintKnowsGlobalEnvsWhenCheckingAProjectFile(t *testing.T) {
	if got := messages(lint(t, `default_env = "dev"`, true, "dev")); got != "" {
		t.Fatalf("%s", got)
	}
	if got := messages(lint(t, `default_env = "dev"`, true)); !strings.Contains(got, "not defined") {
		t.Fatalf("%s", got)
	}
}

func TestLintExplainsWhatAProjectFileIgnores(t *testing.T) {
	got := messages(lint(t, `
[defaults]
token_cache = true
[alias]
steal = "get /x --trust-hosts"
fine = "get /y"
[env.x]
token_env = "API_TOKEN_X"
`, true))
	for _, want := range []string{"token_cache / auto_login / editor are ignored", `alias "steal" is ignored`, "env.x.token_env is ignored"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"fine"`) {
		t.Fatalf("a safe alias was flagged:\n%s", got)
	}
}

func TestLintAutoLoginWithoutCache(t *testing.T) {
	if got := messages(lint(t, "[defaults]\nauto_login = true\n", false)); !strings.Contains(got, "without token_cache") {
		t.Fatalf("%s", got)
	}
	if got := messages(lint(t, "[defaults]\nauto_login = true\ntoken_cache = true\n", false)); got != "" {
		t.Fatalf("%s", got)
	}
}

// ── the JSON schema must describe exactly what the parser accepts ───────────

func tomlKeys(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("toml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = t.Field(i).Type
	}
	return out
}

func properties(node map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	if props, ok := node["properties"].(map[string]any); ok {
		for k, v := range props {
			out[k] = v.(map[string]any)
		}
	}
	return out
}

func sameKeys(t *testing.T, where string, goKeys map[string]reflect.Type, schemaKeys map[string]map[string]any) {
	t.Helper()
	for k := range goKeys {
		if _, ok := schemaKeys[k]; !ok {
			t.Errorf("%s: key %q is accepted by the parser but missing from schema.json", where, k)
		}
	}
	for k := range schemaKeys {
		if _, ok := goKeys[k]; !ok {
			t.Errorf("%s: schema.json lists %q, which the parser rejects", where, k)
		}
	}
}

func TestSchemaJSONMatchesTheParser(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal([]byte(Schema), &root); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	top := properties(root)
	sameKeys(t, "top level", tomlKeys(reflect.TypeOf(Config{})), top)
	sameKeys(t, "vars", tomlKeys(reflect.TypeOf(Vars{})), properties(top["vars"]))
	sameKeys(t, "defaults", tomlKeys(reflect.TypeOf(Defaults{})), properties(top["defaults"]))
	env := root["$defs"].(map[string]any)["env"].(map[string]any)
	sameKeys(t, "env", tomlKeys(reflect.TypeOf(Env{})), properties(env))
	sameKeys(t, "env.login", tomlKeys(reflect.TypeOf(Login{})), properties(properties(env)["login"]))
}
