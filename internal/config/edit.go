package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

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
#   (plain headers only: tenant, version, language. An API key such as X-API-Key is a
#   secret and is not kept here: send it per call, -H "X-API-Key: $MY_KEY")
# subscriptions = "/graphql"            # GraphQL WebSocket path on this server, for api gql --sub (default: the graphql path)
# ws_protocol = "graphql-transport-ws"  # or "graphql-ws" (legacy Apollo subscriptions-transport-ws)
# ws_token_in_init = true                   # also put the token in connection_init (servers that read it there, not from a header)
#   (GraphQL subscriptions: api gql 'subscription { orderUpdated { id } }' .data   streams one JSON per event until Ctrl+C.
#   The WebSocket goes to this env's server only, ws:// or wss:// following the url above, and your token is sent only there.)

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
#   (plain headers only: tenant, version, language. An API key such as X-API-Key is a
#   secret and is not kept here: send it per call, -H "X-API-Key: $MY_KEY")
# subscriptions = "/graphql"            # GraphQL WebSocket path on this server, for api gql --sub (default: the graphql path)
# ws_protocol = "graphql-transport-ws"  # or "graphql-ws" (legacy Apollo subscriptions-transport-ws)
# ws_token_in_init = true                   # also put the token in connection_init (servers that read it there, not from a header)
#   (GraphQL subscriptions: api gql 'subscription { orderUpdated { id } }' .data   streams one JSON per event until Ctrl+C.
#   The WebSocket goes to this env's server only, ws:// or wss:// following the url above, and your token is sent only there.)

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
