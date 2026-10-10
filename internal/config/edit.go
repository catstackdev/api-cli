package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"cybercat/api/internal/secretref"

	"github.com/pelletier/go-toml/v2"
)

// secretKey matches key names that look like credentials. token_env is fine:
// it only names a variable.
var secretKey = regexp.MustCompile(`(?i)(pass|secret|credential|api[_-]?key|private|^token$|\.token$|auth)`)

// IsSecretKey reports whether a key path looks like it would hold a secret.
func IsSecretKey(key string) bool {
	if strings.HasSuffix(key, ".token_env") || key == "token_env" {
		return false
	}
	return secretKey.MatchString(key)
}

// Keys that Set understands, for help text and completion.
var SettableKeys = []string{
	"default_env",
	"defaults.timeout", "defaults.connect_timeout", "defaults.retry", "defaults.color", "defaults.summary", "defaults.token_cache", "defaults.auto_login", "defaults.editor",
	"env.<name>.url", "env.<name>.graphql", "env.<name>.endpoints", "env.<name>.endpoint_prefix", "env.<name>.openapi", "env.<name>.token_env", "env.<name>.headers.<Header>",
	"vars.base_url", "vars.graphql_url", "vars.env_suffix",
	"env.<name>.login.path", "env.<name>.login.gql", "env.<name>.login.endpoint", "env.<name>.login.token_path", "env.<name>.login.vars.<name>",
	"alias.<name>",
}

// loginKey splits env.<name>.login.<field...> into (name, field).
func loginKey(key string) (name, field string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) >= 4 && parts[0] == "env" && parts[2] == "login" {
		return parts[1], strings.Join(parts[3:], "."), true
	}
	return "", "", false
}

func setLogin(c *Config, name, field, value, key string) error {
	if c.Env == nil {
		c.Env = map[string]Env{}
	}
	e := c.Env[name]
	if e.Login == nil {
		e.Login = &Login{}
	}
	l := e.Login
	switch {
	case field == "path":
		l.Path = value
	case field == "gql":
		l.GQL = value
	case field == "endpoint":
		l.Endpoint = value
	case field == "token_path":
		if !strings.HasPrefix(value, ".") {
			return fmt.Errorf("token_path must be a jq path like .data.token")
		}
		l.TokenPath = value
	case strings.HasPrefix(field, "vars.") && len(field) > len("vars."):
		v := strings.TrimPrefix(field, "vars.")
		if IsSecretKey(v) && !secretref.IsRef(value) {
			return fmt.Errorf("%s looks like a secret: store it with `api secret set NAME` and set the value to @keychain:NAME (or @env:NAME / @prompt)", key)
		}
		if l.Vars == nil {
			l.Vars = map[string]string{}
		}
		l.Vars[v] = value
	default:
		return fmt.Errorf("unknown key %q (known: login.path, login.gql, login.endpoint, login.token_path, login.vars.<name>)", key)
	}
	c.Env[name] = Env{URL: e.URL, GraphQL: e.GraphQL, Endpoints: e.Endpoints, EndpointPrefix: e.EndpointPrefix, OpenAPI: e.OpenAPI, TokenEnv: e.TokenEnv, Headers: e.Headers, Login: l}
	return nil
}

// Set applies key=value to c. Secret-looking keys are refused.
func Set(c *Config, key, value string) error {
	if name, field, ok := loginKey(key); ok {
		return setLogin(c, name, field, value, key)
	}
	if IsSecretKey(key) {
		return fmt.Errorf("refusing to store %q: config never holds secrets (use token_env to name a variable)", key)
	}
	parts := strings.Split(key, ".")
	switch {
	case key == "default_env":
		c.DefaultEnv = value
	case parts[0] == "vars" && len(parts) == 2:
		switch parts[1] {
		case "base_url", "graphql_url":
			if err := ValidateVarName(value); err != nil {
				return err
			}
			if parts[1] == "base_url" {
				c.Vars.BaseURL = value
			} else {
				c.Vars.GraphQLURL = value
			}
		case "env_suffix":
			if err := ValidateEnvSuffix(value); err != nil {
				return err
			}
			c.Vars.EnvSuffix = value
		default:
			return fmt.Errorf("unknown key %q (known: vars.base_url, vars.graphql_url, vars.env_suffix)", key)
		}
	case parts[0] == "defaults" && len(parts) == 2:
		return setDefault(&c.Defaults, parts[1], value)
	case parts[0] == "alias" && len(parts) == 2:
		if c.Alias == nil {
			c.Alias = map[string]string{}
		}
		c.Alias[parts[1]] = value
	case parts[0] == "env" && len(parts) >= 3:
		if c.Env == nil {
			c.Env = map[string]Env{}
		}
		e := c.Env[parts[1]]
		field := strings.Join(parts[2:], ".")
		switch {
		case field == "url":
			e.URL = value
		case field == "graphql":
			e.GraphQL = value
		case field == "token_env":
			if err := ValidateTokenEnv(value); err != nil {
				return err
			}
			e.TokenEnv = value
		case field == "endpoint_prefix":
			if err := ValidateEndpointPrefix(value); err != nil {
				return err
			}
			e.EndpointPrefix = value
		case field == "openapi":
			if err := ValidateOpenAPIPath(value); err != nil {
				return err
			}
			e.OpenAPI = value
		case field == "endpoints":
			e.Endpoints = nil
			for _, x := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
				e.Endpoints = append(e.Endpoints, x)
			}
		case strings.HasPrefix(field, "headers.") && len(parts) == 4:
			if e.Headers == nil {
				e.Headers = map[string]string{}
			}
			e.Headers[parts[3]] = value
		default:
			return fmt.Errorf("unknown key %q", key)
		}
		c.Env[parts[1]] = e
	default:
		return fmt.Errorf("unknown key %q (known: %s)", key, strings.Join(SettableKeys, ", "))
	}
	return nil
}

func setDefault(d *Defaults, name, value string) error {
	switch name {
	case "timeout", "connect_timeout":
		if _, err := Duration(value); err != nil {
			return err
		}
		if name == "timeout" {
			d.Timeout = value
		} else {
			d.ConnectTimeout = value
		}
	case "retry":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("retry must be a non-negative integer")
		}
		d.Retry = &n
	case "color":
		switch value {
		case "auto", "always", "never":
			d.Color = value
		default:
			return fmt.Errorf("color must be auto, always or never")
		}
	case "summary":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("summary must be true or false")
		}
		d.Summary = &b
	case "editor":
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("editor must be one line")
		}
		d.Editor = value
	case "token_cache", "auto_login":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be true or false", name)
		}
		if name == "token_cache" {
			d.TokenCache = &b
		} else {
			d.AutoLogin = &b
		}
	default:
		return fmt.Errorf("unknown key defaults.%s", name)
	}
	return nil
}

// Get returns the value of a key as text ("" and false when unset).
func Get(c Config, key string) (string, bool) {
	parts := strings.Split(key, ".")
	switch {
	case key == "default_env":
		return c.DefaultEnv, c.DefaultEnv != ""
	case parts[0] == "vars" && len(parts) == 2:
		switch parts[1] {
		case "base_url":
			return c.Vars.BaseURL, c.Vars.BaseURL != ""
		case "graphql_url":
			return c.Vars.GraphQLURL, c.Vars.GraphQLURL != ""
		case "env_suffix":
			return c.Vars.EnvSuffix, c.Vars.EnvSuffix != ""
		}
	case parts[0] == "defaults" && len(parts) == 2:
		d := c.Defaults
		switch parts[1] {
		case "timeout":
			return d.Timeout, d.Timeout != ""
		case "connect_timeout":
			return d.ConnectTimeout, d.ConnectTimeout != ""
		case "color":
			return d.Color, d.Color != ""
		case "retry":
			if d.Retry != nil {
				return strconv.Itoa(*d.Retry), true
			}
		case "summary":
			if d.Summary != nil {
				return strconv.FormatBool(*d.Summary), true
			}
		case "editor":
			return d.Editor, d.Editor != ""
		case "token_cache":
			if d.TokenCache != nil {
				return strconv.FormatBool(*d.TokenCache), true
			}
		case "auto_login":
			if d.AutoLogin != nil {
				return strconv.FormatBool(*d.AutoLogin), true
			}
		}
	case parts[0] == "alias" && len(parts) == 2:
		v, ok := c.Alias[parts[1]]
		return v, ok
	case parts[0] == "env" && len(parts) >= 3:
		e, ok := c.Env[parts[1]]
		if !ok {
			return "", false
		}
		switch strings.Join(parts[2:], ".") {
		case "url":
			return e.URL, e.URL != ""
		case "graphql":
			return e.GraphQL, e.GraphQL != ""
		case "token_env":
			return e.TokenEnv, e.TokenEnv != ""
		case "endpoint_prefix":
			return e.EndpointPrefix, e.EndpointPrefix != ""
		case "openapi":
			return e.OpenAPI, e.OpenAPI != ""
		case "endpoints":
			return strings.Join(e.Endpoints, ","), len(e.Endpoints) > 0
		default:
			if len(parts) == 4 && parts[2] == "headers" {
				v, ok := e.Headers[parts[3]]
				return v, ok
			}
		}
	}
	return "", false
}

// Write saves one config file (0600, directories created). Comments in an
// existing file are not preserved.
func Write(path string, c Config) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Scope says which config file a template is for.
type Scope int

const (
	Global  Scope = iota // ~/.config/api/config.toml: shared across projects
	Project              // <project>/.api.toml: this project only
)

// Template is the default global file: every global setting, commented out.
var Template = Render(Global, "", "")

// Render builds the template for a scope. For Project, a non-empty name is
// written as an ACTIVE [vars] entry (detected from the project's .env files);
// everything else stays a commented example. With nothing detected the whole
// [vars] block stays commented.
func Render(scope Scope, baseURL, graphqlURL string) string {
	if scope == Global {
		return globalTemplate
	}
	vars := func(key, name, example string) string {
		if name != "" {
			return key + " = " + strconv.Quote(name) + "   # detected in your .env files"
		}
		return "# " + key + " = " + strconv.Quote(example)
	}
	// The header is always active (an empty table is valid TOML), so uncommenting
	// any single key below just works.
	return `# api project config (.api.toml). Safe to commit: there is no place for secrets here.
# Overrides the global config (~/.config/api/config.toml) key by key.
# Precedence: flags > --env > API_* variables > default_env > [defaults] > built-in.

# Env to use when the shell has no session. 'api env use' only sets variables in your
# shell, so tv, nvim, scripts and CI start api without them and find no server unless a
# default_env names one. Uncomment it together with the [env.local] block below.
# (Keep it above [vars]: a key written after a table header belongs to that table.)
# default_env = "local"

# ── .env mapping: this project's own variable names ────────────────────────
# Which variables of this project's .env files hold its URLs, for projects that
# do not use API_BASE_URL / API_GRAPHQL_URL. 'api env load' copies them over, and
# detects common names (GRAPHQL_URL, VITE_GRAPHQL_URL, ...) by itself.
# URLs only: a config file can never choose which variable becomes your token.
[vars]
` + vars("graphql_url", graphqlURL, "VITE_GRAPHQL_URL") + `
` + vars("base_url", baseURL, "REACT_APP_API_URL") + `
# env_suffix = "dev"        # the .env.<suffix> that 'api env load' uses with no name

# ── project defaults for flags ─────────────────────────────────────────────
[defaults]
# timeout = "300s"
# connect_timeout = "10s"
# retry = 0
# color = "auto"      # auto | always | never
# summary = false

# ── envs that exist only in THIS project (uncomment the whole block) ───────
# Use a name the global config does not define: changing the url of a global env
# drops its token_env here. token_env is ignored in a project file: define envs
# that need a token in the global config.
# [env.local]
# url = "http://localhost:3000"
# graphql = "http://localhost:3000/graphql"
# endpoints = ["user", "auth"]          # completions for api gql -e, and api schema sync
# endpoint_prefix = "v2/"               # "user" means v2/user for api schema
# openapi = "/openapi.json"           # where its OpenAPI JSON lives (a path); api openapi sync uses it
# headers = { "X-Tenant" = "local" }    # sent with every request to this env

# ── login for this project, no typing: api login --env local ───────────────
# The password is never written here: "@keychain:NAME" refers to a secret you
# store once, BOUND to this env's server so that no config file, alias or pasted
# command can send it anywhere else:
#     api secret set project-local --for http://localhost:3000
# A project login may only use bound Keychain secrets (not @env: references).
# No url is needed for the login: after 'api env load local' the server is the
# one your .env gave the shell, and the bound secret makes that safe.
# [env.local.login]
# gql = "mutation($input: LoginInput!) { login(input: $input) { accessToken } }"   # or: path = "/auth/login" (REST)
# endpoint = "v2/auth"                    # GraphQL: like api gql -e
# token_path = ".data.login.accessToken"  # where the token is in the response
# vars = { "input.username" = "me@x.com", "input.password" = "@keychain:project-local" }

# ── aliases for this project: api me ───────────────────────────────────────
# [alias]
# me = "gql -e v2/user 'query { me { id } }' .data.me"
`
}

const globalTemplate = `# api global config (~/.config/api/config.toml): what is shared across projects.
# Everything is optional: uncomment only what you need.
# Per-project settings (which .env variable holds the URL, project-only envs)
# belong in the project's .api.toml: run 'api config init --local' there.
# Precedence: flags > --env > API_* variables > default_env > [defaults] > built-in.

# Env to use when the shell has no base URL yet (an [env.<name>] below).
# default_env = "dev"

# ── defaults for flags (flags > env vars > these > built-in) ───────────────
[defaults]
# timeout = "300s"
# connect_timeout = "10s"
# retry = 0
# color = "auto"      # auto | always | never
# summary = false
# token_cache = false   # keep login tokens in the Keychain (bound to the env url) so a new shell needs no login
# auto_login = false    # on a 401, log in again from the env's login block (Keychain secrets only)
# editor = "nvim"       # what api schema opens items in (default: $API_EDITOR, $VISUAL, $EDITOR, nvim)

# ── named environments (uncomment the whole block): api --env dev, api env use dev
# Define the envs you log in to from several projects here, with unique names
# (project-dev, project-local): a project file cannot change their urls without
# losing the token.
# [env.dev]
# url = "https://api.dev.example.com"
# graphql = "https://api.dev.example.com/graphql"
# endpoints = ["user", "auth"]          # completions for api gql -e, and api schema sync
# endpoint_prefix = "v2/"               # "user" means v2/user for api schema
# openapi = "/openapi.json"           # where its OpenAPI JSON lives (a path); api openapi sync uses it
# token_env = "API_TOKEN_DEV"           # NAME (API_TOKEN_*) of the variable with the token; honoured HERE only
# headers = { "X-Tenant" = "dev" }      # sent with every request to this env

# ── login without typing: api login --env dev ──────────────────────────────
# Passwords are never stored here. A value starting with @ is a reference:
# @keychain:NAME (store it once: api secret set NAME --for <server url>), @env:NAME or @prompt.
# Define it here for envs you log in to from several projects; a single project
# can also describe its own login in its .api.toml (with a secret bound to that
# server: api secret set NAME --for URL). Uncomment it together with its [env.dev].
# [env.dev.login]
# gql = "mutation($input: LoginInput!) { login(input: $input) { accessToken } }"   # or path = "/auth/login" for REST
# endpoint = "v2/auth"                   # GraphQL: like api gql -e
# token_path = ".data.login.accessToken" # where the token is in the response
# vars = { "input.username" = "me@x.com", "input.password" = "@keychain:project-dev" }

# ── aliases: api me ────────────────────────────────────────────────────────
# [alias]
# me = "gql -e v2/user 'query { me { id } }' .data.me"
`
