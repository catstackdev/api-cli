package cli

// Everything that decides WHICH server a request goes to, and which token,
// profile and credentials may come with it, lives in this file. It is the
// security-critical core: see SECURITY.md before changing any rule here.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"cybercat/api/internal/config"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jwt"
	"cybercat/api/internal/secretref"
	"cybercat/api/internal/secretstore"
	"cybercat/api/internal/session"
	"path/filepath"
	"time"
)

// session is the shell session after profile and flag resolution.
func (a *app) session() session.Session {
	s, _ := a.resolve()
	return s
}

// resolve builds the effective session and the active env profile.
//
// Precedence: --base/--token, then --env, then the API_* session variables,
// then default_env (only while no session base URL is set), then built-in
// defaults. An explicit --env replaces the session's base URL AND drops its
// token (unless the profile names one via token_env), so `--env thien` can
// never send the token of the env the shell is logged in to.
func (a *app) resolve() (session.Session, config.Env) { return a.resolveFor(a.envName) }

// resolveFor is resolve for a given env name ("" = none). It reads no mutable
// state besides the config and the environment, so callers that need several
// envs at once (diff) can ask for each without touching app.envName.
func (a *app) resolveFor(envName string) (session.Session, config.Env) {
	s, prof, _ := a.resolveWithSources(envName)
	return s, prof
}

// sources says where each part of a resolved session came from, for
// `api env status`. It is display only: nothing decides anything from it, and it
// is filled in by the very branches that choose the values, so it cannot drift
// from the precedence rules above.
type sources struct {
	base, gql, token string
}

// profileLabel names a configured env and the file(s) that define it:
// `toml env.local · project .api.toml`.
func (a *app) profileLabel(name string) string {
	where := ""
	switch a.cfg.EnvOrigin[name] {
	case "global":
		where = "global " + shortPath(a.cfg.GlobalPath)
	case "project":
		where = "project " + shortPath(a.cfg.LocalPath)
	case "global + project":
		where = "global " + shortPath(a.cfg.GlobalPath) + " + project " + shortPath(a.cfg.LocalPath)
	}
	if where == "" {
		return "toml env." + name
	}
	return "toml env." + name + " · " + strings.TrimSpace(where)
}

// shortPath shows a path relative to the working directory or as ~/..., so the
// status stays readable.
func shortPath(p string) string {
	if p == "" {
		return ""
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

// shellLabel says where a session that came from the API_* variables most
// likely originates. The binary only sees the variables (`env load` and `env
// use` both just set them), so it claims only what it can prove: a profile is
// named when it describes the same server (sameServer), never because the env
// NAME matches (that name is attacker-controllable); a .env file is only a
// "probably", from its existence, and its contents are never read here.
func (a *app) shellLabel(s session.Session) string {
	p, ok := a.cfg.Env[s.Name]
	switch {
	case ok && sameServer(p, s):
		return a.profileLabel(s.Name) + " · same server as the shell"
	case ok:
		return fmt.Sprintf("shell variables · NOT toml env.%s (it points at another server)", s.Name)
	case s.Name != "" && config.ValidateEnvSuffix(s.Name) == nil && fileExists(filepath.Join(findRoot(), ".env."+s.Name)):
		return fmt.Sprintf("shell variables · probably .env.%s (api env load)", s.Name)
	default:
		return "shell variables (API_BASE_URL)"
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// resolveWithSources is resolveFor plus the provenance of each value.
func (a *app) resolveWithSources(envName string) (session.Session, config.Env, sources) {
	s := session.FromLookup(a.lookup)
	var prof config.Env
	var src sources
	switch {
	case envName != "":
		if p, ok := a.cfg.Env[envName]; ok && p.URL == "" && p.Login != nil {
			// A login with no url of its own: the server is whatever the shell
			// points at (api env load took it from your .env). Only the login
			// applies, never the profile's headers or token_env, and the login
			// then accepts only bound Keychain secrets (see loginFromConfig).
			prof = config.Env{Login: p.Login}
			if envName != s.Name {
				s.Token = ""
			}
			s.Name = envName
			if s.BaseURL != "" {
				src.base = "shell variables (API_BASE_URL); " + a.profileLabel(envName) + " supplies only the login"
			}
			if s.GraphQLURL != "" {
				src.gql = "shell variables (API_GRAPHQL_URL)"
			}
			if s.Token != "" {
				src.token = "shell variables (API_TOKEN)"
			}
		} else if ok {
			prof = p
			sessionBase := s.BaseURL
			s.BaseURL, s.GraphQLURL = p.URL, p.GraphQL
			// Reuse the session token only for the very server the shell is logged
			// in to: same env name AND same origin. A matching name alone is not
			// enough (a project config can repoint an env's url).
			label := a.profileLabel(envName) + " via --env"
			src.base = label
			if p.GraphQL != "" {
				src.gql = label
			}
			if envName == s.Name && origin(p.URL) != "" && origin(p.URL) == origin(sessionBase) {
				if s.Token != "" {
					src.token = "shell variables (API_TOKEN, same server)"
				}
			} else {
				s.Token = ""
				if p.TokenEnv != "" {
					s.Token = a.getenv(p.TokenEnv)
					src.token = "token_env " + p.TokenEnv
				}
			}
			s.Name = envName
		}
	case s.BaseURL == "" && a.cfg.DefaultEnv != "":
		if p, ok := a.cfg.Env[a.cfg.DefaultEnv]; ok {
			prof = p
			s.BaseURL, s.GraphQLURL, s.Name = p.URL, p.GraphQL, a.cfg.DefaultEnv
			// the session token has no known origin here: never send it to a
			// server picked by config
			s.Token = ""
			label := a.profileLabel(a.cfg.DefaultEnv) + " via default_env"
			src.base = label
			if p.GraphQL != "" {
				src.gql = label
			}
			if p.TokenEnv != "" {
				s.Token = a.getenv(p.TokenEnv)
				src.token = "token_env " + p.TokenEnv
			}
		}
	default:
		// Logged in via the shell. The env NAME alone proves nothing (a cloned
		// repo's .env.dev can set API_ENV=dev and any URL), so the profile with
		// its headers, token_env and login applies only if it really describes
		// the server the shell points at.
		if p, ok := a.cfg.Env[s.Name]; ok {
			if sameServer(p, s) {
				prof = p
			} else if p.URL == "" && p.Login != nil {
				prof = config.Env{Login: p.Login} // login only: see above
			}
		}
		if s.BaseURL != "" {
			src.base = a.shellLabel(s)
		}
		if s.GraphQLURL != "" {
			src.gql = "shell variables (API_GRAPHQL_URL)"
		}
		if s.Token != "" {
			src.token = "shell variables (API_TOKEN)"
		}
	}
	if a.base != "" {
		s.BaseURL = a.base
		src.base = "--base"
	}
	if a.token != "" {
		s.Token = a.token
		src.token = "--token"
	}
	return s, prof, src
}

// sameServer reports whether a configured env is the server the session points
// at: same origin as the session base URL, and the session's GraphQL URL (when
// set) on that same server too.
func sameServer(p config.Env, s session.Session) bool {
	o := origin(p.URL)
	if o == "" || o != origin(s.BaseURL) {
		return false
	}
	if s.GraphQLURL == "" {
		return true
	}
	g := origin(s.GraphQLURL)
	return g == o || (p.GraphQL != "" && g == origin(p.GraphQL))
}

// origin is the normalised "scheme://host[:port]" of a URL, or "" if it has none.
func origin(raw string) string {
	o, err := secretstore.NormalizeOrigin(raw)
	if err != nil {
		return ""
	}
	return o
}

// destOrigin is the origin a request to target would reach ("" if unknown).
func (a *app) destOrigin(envName, target string, o *reqOpts) string {
	s, _ := a.resolveFor(envName)
	full, err := httpc.ResolveURL(s.BaseURL, target, o.query)
	if err != nil {
		return ""
	}
	return origin(full)
}

// stateDir is where api keeps small state (history, the secret names index).
func (a *app) stateDir() string {
	dir := a.getenv("XDG_STATE_HOME")
	if dir == "" {
		home := a.getenv("HOME")
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return ""
			}
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "api")
}

// secretIndex is the names-only index of stored secrets.
func (a *app) secretIndex() secretstore.Index {
	return secretstore.Index{Path: filepath.Join(a.stateDir(), "secrets")}
}

// tokenExpiry describes when a JWT expires ("" for opaque tokens).
func tokenExpiry(token string, now time.Time) string {
	if c, ok := jwt.Parse(token); ok {
		return c.Describe(now)
	}
	return ""
}

// knownSecretNames are the secrets worth listing and checking: the ones this tool
// recorded (secret set / bind / show) plus every @keychain:NAME a login in your
// config refers to, so a secret stored before the index existed still shows up.
func (a *app) knownSecretNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] && secretstore.ValidateName(n) == nil {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range a.secretIndex().Names() {
		add(n)
	}
	for _, name := range a.cfg.EnvNames() {
		if l := a.cfg.Env[name].Login; l != nil {
			for _, v := range l.Vars {
				if kind, arg := secretref.Parse(v); kind == secretref.Keychain {
					add(arg)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}
