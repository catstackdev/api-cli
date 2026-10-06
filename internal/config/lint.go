package config

import (
	_ "embed"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Issue is one finding of Lint.
type Issue struct {
	Error   bool // false = warning
	Message string
}

// Lint checks a parsed file for mistakes ParseText does not reject because they
// are harmless to read: a default_env that names nothing, a half-built login, a
// url that is not a url, and settings that are valid TOML but ignored in a
// project file (they only matter in the global one). known are the env names that
// exist outside this file (the global config's, when linting a project file).
func Lint(c Config, project bool, known map[string]bool) []Issue {
	var out []Issue
	warn := func(format string, a ...any) { out = append(out, Issue{false, fmt.Sprintf(format, a...)}) }
	fail := func(format string, a ...any) { out = append(out, Issue{true, fmt.Sprintf(format, a...)}) }

	envs := map[string]bool{}
	for n := range known {
		envs[n] = true
	}
	for n := range c.Env {
		envs[n] = true
	}
	if c.DefaultEnv != "" && !envs[c.DefaultEnv] {
		warn("default_env %q is not defined under [env.*]", c.DefaultEnv)
	}

	names := make([]string, 0, len(c.Env))
	for n := range c.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := c.Env[n]
		for key, v := range map[string]string{"url": e.URL, "graphql": e.GraphQL} {
			if v != "" && !isHTTPURL(v) {
				fail("env.%s.%s %q is not an http(s) URL", n, key, v)
			}
		}
		for h := range e.Headers {
			if strings.EqualFold(h, "authorization") {
				warn("env.%s.headers has Authorization: tokens come from a login or token_env, not from the config", n)
			}
		}
		if project && e.TokenEnv != "" {
			warn("env.%s.token_env is ignored in a project file (only the global config may set it)", n)
		}
		if l := e.Login; l != nil {
			if err := l.Complete(); err != nil {
				fail("env.%s.login: %v", n, err)
			}
			if l.Endpoint != "" && l.GQL == "" {
				warn("env.%s.login.endpoint only applies to a GraphQL login (gql)", n)
			}
			if project && loginUsesEnvRef(l) {
				warn("env.%s.login uses @env: references, which a project file may not use: the login is ignored", n)
			}
			if project && e.URL == "" && len(l.Vars) > 0 {
				// legal: the server then comes from `api env load`; remind what that implies
				warn("env.%s.login has no url: it uses the server your .env gives the shell, so its Keychain secrets must be bound to that server (api secret bind)", n)
			}
		}
	}

	if project {
		if c.Defaults.TokenCache != nil || c.Defaults.AutoLogin != nil || c.Defaults.Editor != "" {
			warn("defaults.token_cache / auto_login / editor are ignored in a project file (global config only)")
		}
		aliases := make([]string, 0, len(c.Alias))
		for n := range c.Alias {
			aliases = append(aliases, n)
		}
		sort.Strings(aliases)
		for _, n := range aliases {
			if unsafeAlias(c.Alias[n]) {
				warn("alias %q is ignored in a project file (references or --trust-hosts/--token/--base/--har-secrets)", n)
			}
		}
	} else if c.Defaults.AutoLogin != nil && *c.Defaults.AutoLogin && (c.Defaults.TokenCache == nil || !*c.Defaults.TokenCache) {
		warn("auto_login without token_cache logs in again on every 401, because the new token is not kept (set defaults.token_cache = true)")
	}
	return out
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Schema is the JSON Schema of the config files, for editors (taplo, Even Better
// TOML: a `#:schema` line) and CI. A test keeps it identical to what the parser
// accepts.
//
//go:embed schema.json
var Schema string
