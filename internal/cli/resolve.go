package cli

// Everything that decides WHICH server a request goes to, and which token,
// profile and credentials may come with it, lives in this file. It is the
// security-critical core: see SECURITY.md before changing any rule here.

import (
	"os"
	"sort"

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
	s := session.FromLookup(a.lookup)
	var prof config.Env
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
		} else if ok {
			prof = p
			sessionBase := s.BaseURL
			s.BaseURL, s.GraphQLURL = p.URL, p.GraphQL
			// Reuse the session token only for the very server the shell is logged
			// in to: same env name AND same origin. A matching name alone is not
			// enough (a project config can repoint an env's url).
			if !(envName == s.Name && origin(p.URL) != "" && origin(p.URL) == origin(sessionBase)) {
				s.Token = ""
				if p.TokenEnv != "" {
					s.Token = a.getenv(p.TokenEnv)
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
			if p.TokenEnv != "" {
				s.Token = a.getenv(p.TokenEnv)
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
	}
	if a.base != "" {
		s.BaseURL = a.base
	}
	if a.token != "" {
		s.Token = a.token
	}
	return s, prof
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
