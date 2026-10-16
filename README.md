# api

REST + GraphQL client for the terminal, written in Go. It replaces the zsh
helpers in `src/curl.sh` (`cget`, `cpost`, `cgql`, ...) with one binary that has
real tab completion, a built-in jq, coloured output, a pager, config with named
envs, a two-env diff, saved requests and more. A small zsh wrapper, built into
the binary (`api shell-init`), wires it into the shell.

`curl.sh` still exists and keeps working. `api` is a separate namespace;
`src/up-scale.sh` is already built on it.

## Install

```zsh
cd ~/.config/cybercat/profile/personal-lib/api
make install        # builds bin/api and copies it to ~/.local/bin/api
api shell-init --install   # adds the eval line to the rc file of your $SHELL (zsh or bash), once
exec zsh
api doctor          # checks the whole setup (--json for scripts and CI)
```

The binary is all you install: `api shell-init` prints the shell wrapper and the
completion script (both embedded), so there is no file to copy. The line it adds
is `eval "$(command api shell-init)"` (`... shell-init bash` for bash 3.2+; bash
completion needs bash 4+ and bash-completion). Without it, requests still work,
but `login` and `env load|use|clear` cannot change your shell.

Shell start-up runs the binary once. To skip that, cache its output and
regenerate it only when the binary is newer (the same trick the profile uses for
other tool inits):

```zsh
f=~/.cache/api-shell-init.zsh
[[ -s $f && ! $commands[api] -nt $f ]] || command api shell-init >$f
source $f
```

If `api` ever dies silently right after an install (exit 137, no message, while
`bin/api` still runs): on Apple silicon, copying a new binary **over** one that
already ran leaves a stale code signature and the OS kills it. `make install`
avoids this by replacing the file (copy to `api.new`, then `mv`); if you copy by
hand, `rm` the old one first. Requires Go 1.24+. The binary is static
(`CGO_ENABLED=0`), so it cross-compiles for Termux
(`GOOS=linux GOARCH=arm64 make build`).

## Use it from Claude Code (agent skill)

`skills/api-cli.md` is a
[Claude Code skill](https://docs.claude.com/en/docs/claude-code/skills) that
teaches an agent to drive `api`: how to keep a session across its one-shot
shells, never print a token, check a request with `--dry` first, read `.http`
checks, and what each exit code means. It needs `api` on your `PATH`
(`make install`, above).

```zsh
make install-skill                  # for you: ~/.claude/skills/api-cli/SKILL.md
make install-skill SKILL_DIR=/path/to/project/.claude/skills/api-cli   # one project only
```

Without `make`: copy `skills/api-cli.md` to `<skills dir>/api-cli/SKILL.md`
(`~/.claude/skills` for every project, `<project>/.claude/skills` for one).
Start a new Claude Code session if `/api-cli` is not listed yet. After updating
this repo, run the same command again; to remove it, delete the `api-cli`
directory.

The skill only describes the commands: it grants no access. Claude Code still
asks before running them, and the tool's own rules (a token goes to the active
server only, no secrets in config) apply unchanged.

## Quick start

```zsh
cd ~/work/my-project
api env load staging                                   # source .env.staging
api login /auth/login '{"email":"x","password":"y"}' '.data.access_token'
api get /users '.data[0].id'
api post /users '{"name":"x"}' '.data.id'
api gql -e v2/user 'query { me { id } }' '.data.me'
api <TAB>                                              # commands, flags, paths
```

With config (see [Config](#config)):

```zsh
api config init && api config set env.dev.url https://api.dev.example.com
api env use dev                       # switch the shell to [env.dev]
api --env thien get /users            # one call against another env
api diff --envs dev,thien /users/1 .data
```

## Commands

| Command                                          | What it does                                                    |
| ------------------------------------------------ | --------------------------------------------------------------- |
| `get\|del <path> [.filter]`                      | GET / DELETE                                                    |
| `post\|put\|patch <path> [body] [.f]`            | send a JSON body (arg, `@file`, `-`, stdin)                     |
| `form <path> -F k=v -F f=@file`                  | multipart (`-F`) or urlencoded (`-d`)                           |
| `gql [-e endpoint] <query> [vars]`               | GraphQL; `@file`, stdin, `-V k=v` variables                     |
| `login <path> [body] [.token-path]`              | REST login, sets `API_TOKEN`                                    |
| `login --gql [-e ep] <mutation> [-V k=v]`        | GraphQL login (default `.data.login.accessToken`)               |
| `env status\|load\|use\|clear\|list\|root`       | session and `.env` files                                        |
| `env example [--write\|--check]`, `env init <s>` | generate / check `.env.example`, scaffold                       |
| `config path\|show\|get\|set\|init\|edit`        | global + project config                                         |
| `diff --envs a,b <path>`                         | same request to two envs, structural JSON diff                  |
| `run <file\|name> [request]`                     | saved requests from `.http` files                               |
| `import` (curl on stdin)                         | copied curl -> `.http` request (prints only)                    |
| `openapi [sync\|list\|pick\|show]`               | index an OpenAPI JSON spec; pick, edit, save, run               |
| `history [-a] [--runs]`, `again [N] [--edit]`    | requests to this server; repeat a GET or a saved pick           |
| `requests ls`, `requests rm <name>\|--all`       | list or delete the saved picks in `.api/requests/`              |
| `cache status`, `cache clear [openapi\|schema]`  | size/age of the OpenAPI and schema caches; delete them          |
| `json k=v k:=raw a.b=nested`                     | build a JSON body (replaces `cjson`)                            |
| `watch <interval> <path> [.filter]`              | poll until Ctrl+C                                               |
| `headers <path>`, `time <path>`                  | response headers / dns-tls-ttfb timing                          |
| `doctor [--online] [--fix]`                      | check binary, shim, completion, config, session, saved requests |
| `shell-init [zsh\|bash] [--install]`             | the shell wrapper + completion; `--install` adds it to your rc  |
| `completion zsh`                                 | just the completion script (shell-init embeds it)               |

Any argument starting with `.` is a jq filter (`./file` and `../file` are
paths). The last filter wins. User-defined `[alias]` entries from config also
work as commands.

### Flags (request commands)

| Flag                             | Meaning                                                                                                                 |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `-H "Name: value"`               | extra header (repeatable)                                                                                               |
| `-Q key=value`                   | query parameter (repeatable)                                                                                            |
| `-d`, `--data`                   | body; `@file` reads a file, `-` reads stdin                                                                             |
| `-r` / `-c` / `-R`               | raw strings (jq `-r`) / compact / body untouched                                                                        |
| `-t`, `--table`, `--columns a,b` | print an array of objects (or one object) as a table                                                                    |
| `-i` / `-v`                      | print status+headers / masked request, response headers and timings to stderr                                           |
| `-o file`                        | write the body to a file                                                                                                |
| `--dry`                          | print the exact curl command, don't send                                                                                |
| `--debug`                        | print the curl command to stderr, then send                                                                             |
| `--summary`                      | status/type/time/size footer (`API_SUMMARY=1`)                                                                          |
| `--har file`                     | append the exchange to a HAR file (secrets masked)                                                                      |
| `--har-secrets`                  | keep secrets in the HAR (dangerous to share)                                                                            |
| `--retry N`                      | retry transport errors and 429/502/503/504 (honours `Retry-After`, at most 60s)                                         |
| `--timeout`, `--connect-timeout` | override the limits (300s / 10s)                                                                                        |
| `--no-auth`, `-k`                | skip the bearer token / skip TLS verification                                                                           |
| `--trust-hosts`                  | send your token to an absolute URL on another host (default: only the active base URL)                                  |
| `--strict` (gql)                 | exit 4 when the response has GraphQL `errors`                                                                           |
| `--viewer nvim\|jless\|fx\|cmd`  | (`get`, `post`, `put`, `patch`, `del`, `form`, `gql`, `openapi pick`, `again`) show the result in an interactive viewer |
| `-- <curl args>`                 | hand the request to real curl with extra flags                                                                          |

Global flags (work on every command): `--env <name>`, `--base`, `--token`, `-C`
/ `-M` / `--color`, `--no-pager`, `--debug`.

`api post /x '{"a":1}' --dry` prints, for example:

```
curl -sS --fail-with-body --connect-timeout 10 --max-time 300 -X POST \
  https://api.test/x -H 'Authorization: Bearer abcdefghijkl…' ...
```

### GraphQL subscriptions (WebSocket)

`api gql` runs a `subscription` over a WebSocket on its own (comment lines do
not count; there is no flag to turn it on). It prints one compact JSON line per
event, with the same jq filter, `-t`, `-r` and `--strict` as a query, until
Ctrl+C, the server completes it, `--max N` events or `--timeout`.

```bash
api gql 'subscription { orderUpdated { id status } }' .data.orderUpdated
api gql -e v2/orders 'subscription($id: ID!) { order(id: $id) { status } }' -V id=7 --max 3
api gql @sub.graphql --protocol graphql-ws            # legacy Apollo servers
api gql 'subscription { ping }' --token-in-init --init tenant=acme   # token / params in connection_init
api gql 'subscription { ping }' --dry                 # shows the upgrade and both messages, masked
```

- Protocols: `graphql-transport-ws` (default: graphql-ws library, Apollo Server
  4+, Yoga, Hasura) and the legacy `graphql-ws` (`--protocol`, or `ws_protocol`
  in config).
- The URL is the GraphQL URL with `ws://` / `wss://` instead of `http(s)://`.
  `env.<name>.subscriptions = "/realtime"` is the PATH of a different WebSocket
  endpoint on that same server (never a URL, so the token cannot be aimed
  elsewhere).
- The token is sent exactly like for a request: in the `Authorization` header of
  the upgrade, and only to the active server (an absolute `-e` URL on another
  host gets none). Servers made for browsers read it from `connection_init`
  instead: `--token-in-init` or `ws_token_in_init = true`. `--init key=value`
  adds more fields to that payload (`@keychain:NAME` works, bound secrets are
  checked against the server like everywhere else).
- A redirect on the upgrade is never followed, a plain `ws://` to a non-local
  host warns, `-k` skips certificate checks, and `--dry` / `--debug` mask the
  token in the header and in `connection_init`.
- Exit codes: `0` ended cleanly (the server completing it, `--max`, Ctrl+C), `4`
  the server ended it with an error (or `--strict` and an event with `errors`),
  `22` the upgrade was refused (401, 403...), `5` bad filter, `28` `--timeout`
  was reached (curl's `--max-time`: use `--max N` for an ordinary end; with no
  `--timeout` a stream has no time limit), `7/6` connect/DNS.
- Not supported: `-o`, `--har`, `-- curl args`, `diff` (it would run on both
  envs) and the fzf picker (it prints the `api gql` line to run). A 401 is not
  retried with the token cache.

### Escape hatch

Anything the native client does not cover goes after `--` and runs through the
system `curl` with the same request: `api get /big -- --compressed -o /tmp/f`.

## Environments and login

Three ways to say which server you talk to, in this precedence:

1. `--base` / `--token`
2. `--env <name>`: an `[env.<name>]` from config. It replaces the session's base
   URL **and drops its token**, so `--env thien` never sends the token of the
   env you are logged in to (unless the env names one with `token_env`).
3. the session variables (`API_BASE_URL`, `API_TOKEN`, `API_ENV`, ...)
4. `default_env` from config, only while no session base URL is set

`api env use` / `api env load` only set variables **in your shell**. A program
that starts `api` without them (a television channel, Neovim, a script, CI) sees
only this last one, so a project that wants them to work sets `default_env` in
its `.api.toml`. `api config init --local` writes the line commented out
(uncomment it with the `[env.local]` block);
`api config set default_env <name> --local` warns when no env of that name is
defined, and `api config validate` reports it too.

```zsh
api env load staging      # source .env.staging (zsh does the sourcing, so $VAR works)
api env use thien         # switch to [env.thien] (plain assignments, not exported)
api env use               # no name: pick one with fzf
api login /auth/login '{"u":"x","p":"y"}'
api login --gql -e v2/auth \
  'mutation($input: LoginInput!) { login(input: $input) { accessToken } }' \
  -V input.username=me@x.com -V input.password=secret
```

Login sends no bearer token (a stale one can turn it into a 401) and, with
`--dry`, sends nothing. GraphQL errors in a 200 response are reported as a
failed login.

## Login without typing

Credentials never go in a config file, a command line or your shell history.
Store the password once in the macOS Keychain (on Linux, the Secret Service: see
below), **bound to the server it may be sent to**, describe the login once, then
just run `api login`.

```zsh
api secret set upscale-dev --for https://api.dev.upscale.technology   # hidden prompt
```

Describe the login where it belongs. **Per project** (in the project's
`.api.toml`; `api config init --local` writes the example for you):

```toml
[env.dev]
url = "https://api.dev.upscale.technology"
graphql = "https://api.dev.upscale.technology/graphql"

[env.dev.login]
gql = "mutation($input: LoginInput!) { login(input: $input) { accessToken } }"
endpoint = "v2/auth"
vars = { "input.username" = "me@x.com", "input.password" = "@keychain:upscale-dev" }
```

or **globally** in `~/.config/api/config.toml` for envs you use from several
projects (same block). Then:

```zsh
api login --env dev                     # no typing; sets API_TOKEN, the env and its per-env token
api login                               # uses the env you are on (API_ENV / default_env)
```

or set it from the command line, no editing needed (comments in your file are
kept):

```zsh
api config set env.dev.login.gql 'mutation($input: LoginInput!) { login(input: $input) { accessToken } }'
api config set env.dev.login.vars.input.username me@x.com
api config set env.dev.login.vars.input.password @keychain:upscale-dev
```

A value that starts with `@` is a reference, accepted by `-V`, `api json` and
the `login.vars` of the config:

| Reference         | Value                                            |
| ----------------- | ------------------------------------------------ |
| `@keychain:NAME`  | the Keychain entry `api:NAME` (`api secret set`) |
| `@env:NAME`       | the environment variable `NAME`                  |
| `@prompt[:label]` | asked on the terminal, no echo                   |
| `@@text`          | the literal `@text`                              |

```zsh
api login --gql -e v2/auth @login.graphql -V input.username=me@x.com -V input.password=@keychain:upscale-dev
api json email=me password=@keychain:upscale-dev | api login /auth/login '.data.token'
```

**It links to `api env load` automatically.** A login needs no `url` of its own:
after `api env load dev` (which takes the server from your project's
`.env.dev`), a plain `api login` (or `api login --env dev`) uses that server.
Because the secret is bound, this is safe even if the `.env` is someone else's.
Such a login (no url) accepts only bound Keychain secrets, never `@env:`, and
contributes only its login: the env's headers and `token_env` are not handed to
whatever server the shell points at. With no server yet you get:
`no server for env "dev": run `api env load dev` first`.

**Why a project may describe its login safely: bound secrets.** A secret stored
with `--for URL` is released only to that server (like a browser's password
manager), whoever asks: a cloned repo's config, an alias or a pasted command.
`api` checks the destination of the request _before_ it resolves the reference.

**What binding does NOT protect.** It stops `api` itself from being misdirected
(by a config, an alias or a pasted command). It does not protect you from code
you run: `api env load` _sources_ the project's `.env` file with the shell (that
is running its code, and it can export `HTTP_PROXY` or read the Keychain with
`security find-generic-password` directly), and any process of yours that runs
`security` can read your items. Only `api env load` `.env` files you trust, like
any project you would run.

Redirects: `api` follows redirects within the same host but refuses one to
another host (a 307/308 would re-send the request body, a login password
included, to wherever the server points); `--trust-hosts` follows it.

- A **project** login may use only Keychain secrets **bound** to its server,
  never `@env:` (it could read any variable of yours). An unbound secret is
  refused with the exact command to fix it: `api secret bind NAME <url>`.
- If a repo names your secret but its own server, nothing is sent:
  `@keychain:NAME is bound to https://real… and will not be sent to https://evil…`.
- A project cannot redefine the login of an env your global config already
  anchors to a server (a url, login or `token_env`); it is ignored with a
  warning.
- Secrets you did not bind keep working for your own commands and your global
  config (it is yours), but binding is recommended:
  `api secret bind NAME <url>`.

**Pick and run a query: `api gql` with no query** (on a terminal, or `--pick`):

```bash
api gql                  # fzf: endpoint (the last one first) -> query or mutation, with a preview
api gql -e user          # skip the endpoint step
api gql -e user --item 'query me'   # skip the query step too (implies --pick): for a
                         # television channel or an editor that already chose the item
```

It generates a ready operation (variables declared from the arguments, a
selection of the return type's plain fields) and opens it in your editor: **save
to run, quit without saving to cancel**. Put the variables as JSON below the
`### variables` line. The query that will run decides whether to ask: a mutation
(even one you edited a query into) shows
`changes data on <server>: run it? [y/N]`; `--yes` skips that. The equivalent
`api gql -e … '…'` line is printed so a good query can become an alias;
variables are left out of it when a name looks like a secret.
`--item '<query|mutation> <name>'` takes the operation you already chose
(anything that is not a query or mutation of the endpoint is an error);
everything after that, the save-to-run rule and the mutation question included,
is unchanged.

**Explore a GraphQL service: `api schema`** (replaces the shell introspection
helpers):

```bash
api schema names user --prefix v2/        # query and mutation names
api schema type user Query --prefix v2/   # fields with args / input fields / enum values
api schema type user Tag --prefix v2/ --expand   # ... and what its fields point at: category: TagCategory
                                                 # also lists TagCategory's fields (one level, an extra "related" key)
api schema dump user --prefix v2/         # the whole introspection result
api schema sync --prefix v2/ user auth    # concurrent; rebuilds ~/.cache/ugql for fzf / `tv ugql`
```

`sync` writes `<endpoint>.json`, `index.txt`, `preview.jq` and `.env` in
`$UGQL_CACHE_DIR` if you set it (flat, your own directory), and otherwise in
`~/.cache/ugql/<server>/`, one directory per server, so a project on another
server cannot overwrite these schemas. `~/.cache/ugql/current` is a link to the
last server synced: the fzf pickers and the television channel read it (same
format as before; the previews stay `jq`: they run in subprocesses). With no
endpoint it uses the env's `endpoints` from the config; names become file names,
so only letters, digits and `_ - .` are accepted. `--dry` prints the requests
and writes nothing.

**Stay logged in: `token_cache` and `auto_login`** (both off; global config
only):

```bash
api config set defaults.token_cache true --global   # a new shell needs no login
api config set defaults.auto_login true --global    # a 401 triggers a silent re-login
api logout                                          # forget this shell's token AND the cached one
api logout --all                                    # every env in your config
```

The token is kept in the Keychain (`api:token.<env>`) **bound to the env's base
URL**: it is sent there and nowhere else, not even with `--trust-hosts`, and a
repo's `.env` that reuses the env name cannot aim it elsewhere. A rejected token
(401) is replaced from the cache (another terminal may have logged in) and then,
with `auto_login`, by running the env's login again. Automatic login uses
`@keychain:` references only (never a prompt or `@env:`), skips `--token`,
`-H Authorization` and `--no-auth`, and runs once per command. This shell keeps
its old `API_TOKEN` until you run `api login`: the next call just recovers
again. `api env status` shows the cached token's expiry for JWTs.

`api env status` also says where each value came from (`--base`, `--env`,
`default_env`, or the shell session) and which file defines the profile (global,
project). A profile is called a match only when it describes the same server as
the shell (same origin), never because the env name is the same. `env load` and
`env use` record which of them set the session (`API_ENV_FROM`, display only),
so status says `.env.local (api env load)` or `toml env.local (api env use)`. A
shell started before that shows what the variables alone can prove.

`api secret`: `set NAME [--for URL]... [--stdin]` (replacing the password keeps
its binding; `--unbind` drops it), `bind NAME URL...`, `show NAME` (the value
only with `--reveal`), `rm NAME`, `list [--prune]` (names, binding state and
servers; never values; it reads a names-only index in
`~/.local/state/api/secrets`, and `api doctor` warns about unbound secrets).
`api env status` and `doctor` show a JWT's expiry. Secrets are stored namespaced
(`api:NAME`) and sent to the store over stdin, never as an argument. On Linux
the store is the freedesktop Secret Service (GNOME Keyring, KWallet, KeePassXC)
through `secret-tool` (package `libsecret-tools`), needing a desktop or
`dbus-run-session` session; entries have the attributes
`service=api:NAME account=api`. Without a store (no `secret-tool`, other
systems) use `@env:` or `@prompt`. `--dry` shows the request with secrets masked
and never reads the Keychain or sends. A literal password in `login.vars` (or
via `config set`) is rejected.

`upscale <env>` prefers the Keychain entry `upscale-<env>` when it exists; run
`upscale-save <env>` once to move the password out of `up-scale.sh` (it binds it
to that env's server).

### Projects that name their URL variable differently

`api` reads `API_BASE_URL` and `API_GRAPHQL_URL`, but projects call them
`GRAPHQL_URL`, `VITE_GRAPHQL_URL`, `GRAPHQL_API`, `REACT_APP_API_URL`, ...
`api env load <suffix>` copies the right one over for you, using the names
defined in that `.env` file (values stay in your shell):

- GraphQL URL: `API_GRAPHQL_URL`, then `VITE_/REACT_APP_/NEXT_PUBLIC_` +
  `GRAPHQL_URL|GRAPHQL_API|GRAPHQL_ENDPOINT`, then the bare `GRAPHQL_URL`,
  `GRAPHQL_API`, `GRAPHQL_ENDPOINT`, `GRAPHQL_URI`
- base URL: the same idea with `API_URL`, `API_BASE_URL`, `BASE_URL`,
  `VITE_API_URL`, ...; with none of them, it is the GraphQL URL minus `/graphql`

It tells you what it used (`GraphQL URL ← $VITE_GRAPHQL_URL`). A blank
placeholder (`VITE_GRAPHQL_URL=`) never overwrites a value you already have. If
it guesses wrong, name the variables in the project's `.api.toml`:

```toml
[vars]
base_url = "REACT_APP_API_URL"
graphql_url = "VITE_GRAPHQL_URL"
```

(or `api config set vars.graphql_url GRAPHQL_API`). `api config init --local`
writes this block for you, filled in from the variable names in the project's
`.env` files, so you rarely type it. URLs only: a config file cannot choose
which variable becomes your token.

## Config

`api config init` writes a different, fully commented example for each file,
like `db`'s `.dbrc.global` / `.dbrc.project`:

- `api config init --global`: shared envs (with `token_env`), `[defaults]`,
  `[alias]`.
- `api config init --local`: the project's `[vars]` (detected from its `.env`
  files), `env_suffix`, project-only envs, `[defaults]`, `[alias]`.

`[vars] env_suffix = "dev"` is the `.env.<suffix>` that `api env load` uses when
you give no name. With neither, and no plain `.env`, it falls back to
`.env.local`, `.env.development`, `.env.dev` and says so. It is a suffix, never
a path: a config file cannot point the sourced file elsewhere.

Optional TOML. Global: `~/.config/api/config.toml` (override with `API_CONFIG`).
Project: the nearest `.api.toml` walking up. They merge key by key, project
wins. Create one with `api config init`.

```toml
default_env = "dev"

[defaults]
timeout = "60s"
retry = 2
color = "auto"

[env.dev]
url = "https://api.dev.example.com"
graphql = "https://api.dev.example.com/graphql"
endpoints = ["user", "auth"]        # completions for gql -e
openapi = "/openapi.json"           # where its OpenAPI JSON lives (a path); `api openapi sync` uses it
token_env = "API_TOKEN_DEV"         # NAME (API_TOKEN_*) of the variable with the token
headers = { "X-Tenant" = "dev" }
subscriptions = "/realtime"         # GraphQL WebSocket PATH on this server, for `api gql` subscriptions (default: the graphql path)
ws_protocol = "graphql-ws"          # graphql-transport-ws (default) | graphql-ws (legacy)
ws_token_in_init = true             # also send the token in connection_init

[env.thien]
url = "https://api.thien.example.com"
token_env = "API_TOKEN_THIEN"

[alias]
me = "gql -e v2/user 'query { me { id } }' .data.me"
```

`api config set KEY VALUE` edits the file **in place**: your comments and the
commented template stay, an example line for the key is uncommented, and a
missing table is added at the end. If a file has a layout it cannot edit safely
(e.g. a multi-line inline table) it says so before rewriting it.

Defaults precedence: flags, then env vars (`CURL_MAX_TIME` ...), then config,
then built-in.

**No secrets in config.** Tokens and passwords are not part of the schema,
unknown keys are rejected (the error names the key, never the value), and
`token_env` may only name an `API_TOKEN_*` variable.

### Headers

| Where                       | Use it for                                                                                                                                       |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `-H "Name: value"`          | one call; wins over the env's header of the same name                                                                                            |
| `[env.x] headers = { ... }` | plain headers every call to that env needs (`X-Tenant`, `Accept-Language`, an API version); `api config set env.x.headers.NAME value` writes one |
| a `.http` request           | lines after the request line; `{{variables}}` are expanded                                                                                       |

Order: the env's headers, then `-H`, then the token (`Authorization`, only if
you set none), then the `Accept: application/json` default. Names match
case-insensitively. A header name must be an HTTP token and no value may hold a
control character (a line break would start another header): `-H`, `.http`
requests and the config all go through `httpc.CheckHeader`, the config is
refused on read and in `config set`, and the error names the header, never its
value.

**An API key header (`X-API-Key`, `X-Auth-Token`, ...) is a secret, so it is not
kept in config** (`config set` refuses it, `config validate` warns). Pass it per
call, from your own environment:

```bash
api get /users -H "X-API-Key: $MY_KEY"
# once per shell, if you use it all day:
myapi() { api "$@" -H "X-API-Key: $MY_KEY"; }
```

In a `.http` file use `{{$env API_MY_KEY}}` (it reads `API_*` variables only).
`--dry`, `--debug` and HAR export mask such headers.

**A project `.api.toml` cannot route your tokens.** It may come from a cloned
repo, so it chooses _where_ requests go but never _which of your secrets_ go
there:

- `token_env` is honoured **only in the global config**; in a project file it is
  ignored with a warning (`api config set ... token_env` refuses a project file;
  use `--global`).
- if a project file changes the `url` or `graphql` of an env that the global
  config defines, that env's `token_env` is dropped for the project.
- `--env <name>` reuses the shell's session token only when the env's URL has
  the same origin as the session's base URL (the same name alone is not enough);
  otherwise it uses `token_env` or sends none. `default_env` never reuses the
  session token.

A broken config is ignored with a warning; `api doctor` shows the error.
`api config validate` checks the files in one go (errors with key and line,
half-built logins, urls, and settings a project file may not use, which are
valid TOML but ignored). `api config schema` prints a JSON Schema for editors
(taplo / Even Better TOML: `#:schema file:///path/to/schema.json`); a test keeps
it equal to what the parser accepts.

**Where your token and passwords can go** (all enforced and tested):

- Your session token goes to the **active base URL only**. An absolute URL on
  another host (typed, pasted, or from a project alias) does not get it, and
  `api` says so; pass `--token X` (explicit) or `--trust-hosts` to override.
- A session env name alone does not attach a configured profile (headers,
  `token_env`, `login`): the shell must really point at that env's server (same
  origin for the base URL and the GraphQL URL). A cloned repo's `.env.dev` can
  set `API_ENV=dev` and any URL, so the name proves nothing.
- Aliases from a **project** `.api.toml` cannot contain `@keychain:` / `@env:` /
  `@prompt` references or `--trust-hosts`, `--token`, `--base`, `--har-secrets`;
  they are ignored with a warning. Your global aliases can.
- `--dry`, `--debug` and `API_DEBUG=1` print the curl command with secrets
  masked (credential headers, secret-looking JSON fields, form pairs and query
  parameters), so a resolved password never appears there.

`API_TOKEN_*` shell variables are forwarded to the binary by the shim even when
unexported. `api login` sets the env's `token_env` variable too, and
`upscale <env>` sets `API_TOKEN_<ENV>`, so after logging in to each env
`api diff` can authenticate both sides. `api --env thien login` switches the
whole session to thien (base URL, env name and token), so the next plain call
cannot send thien's token to the previous env's server.

## `.env` helpers

```zsh
api env example                 # print a .env.example (keys only, values blank)
api env example --write         # write it (--force to overwrite a different one)
api env example --check         # which .env.* files drift from the example (exit 1)
api env init prod               # create .env.prod (0600) from the example
api doctor --fix                # chmod 600 .env files other users can read
```

Only key names are read. Values never reach the output unless you pass
`--keep-values`, and even then secret-looking keys (`TOKEN`, `SECRET`,
`PASSWORD`, `KEY`, ...) and URLs with embedded credentials stay blank.

## `.http` files: a short tour

A `.http` file is a plain-text list of requests. It is the format of the
JetBrains HTTP client and the VS Code REST Client, so the same file works in
your editor and from the shell. The files in [`examples/`](examples/) run as
they are (change the paths to match your API):

| File                                      | Shows                                                                        |
| ----------------------------------------- | ---------------------------------------------------------------------------- |
| [`users.http`](examples/users.http)       | variables, a query string, JSON bodies, headers, filters, a body from a file |
| [`chaining.http`](examples/chaining.http) | using one response in the next request                                       |
| [`checks.http`](examples/checks.http)     | `# @assert` response checks for CI                                           |

```zsh
api run examples/users.http --list         # what is in the file
api run examples/users.http "List users"   # one request, by name or number
api run examples/users.http --all --dry    # print every request, send none
```

What a file looks like:

```
@limit = 10

### List users
# @filter .data
GET /users?limit={{limit}}
Accept: application/json

### Create user
# @name create
POST /users
Content-Type: application/json

{"name": "Ann", "id": "{{$uuid}}"}

### Fetch it back
GET /users/{{create.response.body.data.id}}
```

- `@limit = 10` is a file variable, used as `{{limit}}`; `--var limit=5` beats
  it.
- `###` starts a request and the rest of the line is its name
  (`api run file "List users"`).
- `# @filter` is the default jq filter, `# @name` names a request so a later one
  can read its response, `# @assert` checks it. Other `#` and `//` lines are
  comments.
- A path that starts with `/` goes to the **active server** with your session
  token; a full URL on another host gets none.
- Headers follow the request line, then a blank line, then the body
  (`< ./file.json` reads it from a file next to the `.http` file).
- Keep your own files in `.api/requests/` so `api run <name>` finds them, or let
  `api openapi` write one for you from your API's spec.

**Try them for real** (the [examples README](examples/README.md) has the full
recipe). `examples/server.py` is a tiny in-memory API (Python 3, no installs)
with the endpoints the examples call, and `examples/.api.toml` points an env
called `local` at it. Run `api` from inside `examples/` and it finds that
config:

```zsh
python3 examples/server.py        # terminal 1: http://127.0.0.1:8787, logs each request
cd examples                       # terminal 2
api config validate               # the .api.toml is read from here
api run users.http --all
api run chaining.http --all       # create, then fetch and delete the id it returned
api run checks.http --all         # 7 assertions; exit 0
```

From another directory, add the server instead:
`api --base http://127.0.0.1:8787 run examples/users.http --all`. The server
never deletes its seed users, so the files can be re-run. In Neovim, start
`nvim` inside `examples/` so a terminal inherits that directory.

**From a browser request.** `pbpaste | api import` turns a copied curl command
(DevTools "Copy as cURL") into a `.http` request and prints it; it sends nothing
and changes no session or config. Add `--out file.http` to append it to a file
(never overwritten). The active server becomes `{{graphql}}` / `{{base}}` (so
the file follows `api env use`), a GraphQL body is detected and named after its
operation, a real bearer token becomes `{{token}}`, and every other
secret-looking value (headers, JSON fields, query parameters) becomes
`{{$env API_NAME}}`: it is read from your shell when the request runs and is
never written to the file. What is left out is listed with its reason in a
comment: `browser` (`sec-*`, `user-agent`, `referer`, ...), `cache` (an
`If-None-Match` would make the server answer an empty 304), `tracing`, the
browser's `Cookie` (not your API credential), `already sent by api` (a default
`Accept`), and `already in your env` (a header your env profile sends). A header
it does not recognise is kept but flagged with `# check:` so you can delete it
if the request works without it; `--minimal` drops those too and `--keep NAME`
keeps one that was dropped (a kept cookie is still a reference, never a value).
Pass the command on stdin rather than as an argument: it contains quotes and
tokens, and an argument lands in your shell history. Pipes, redirects, `-F`,
`@file` and anything that would add a line or a `{{...}}` to the file are
refused.

The next sections list every variable, the response capture and the checks.

## Diff, saved requests, history, HAR

**`api diff --envs dev,thien <path> [.filter]`** sends one request to two envs,
each with its own token, and prints a jq-path diff (`~` changed, `+` added, `-`
removed). `--ignore .data.updatedAt` skips paths; `--gql` compares a query.
`--array-key id` matches the items of an array by that field instead of by
position (a reordered list is not a difference; changes read
`.data[id=42].name`; an array it cannot match falls back to position). `--json`
prints the result for scripts. GET and GraphQL queries only: mutations and
subscriptions are refused because they would run against both envs. Exit 0
identical, 1 different.

**`api run <name|file> [request]`** runs requests from a `.http` file (the
JetBrains / VS Code REST Client format). Names resolve to
`.api/requests/<name>.http` in the project. Variables: `@var = value`,
`{{var}}`, `--var k=v`, `{{base}}` `{{token}}`, `{{$env API_X}}`, `{{$uuid}}`,
`{{$timestamp}}`. `--list` shows what is in a file, `--all` runs them in order.
A file can come from a repository, so the session token and `{{token}}` /
`{{$env ...}}` are **not** sent to a host other than the active base URL unless
you pass `--trust-hosts`.

_Use a response in the next request._ Name a request with `# @name login`, then
refer to it: `{{login.response.body.data.token}}`,
`{{login.response.body.items[0].id}}`, `{{login.response.headers.X-Req-Id}}`,
`{{login.response.status}}`. The request you ask for runs its dependencies first
(once, silently); `--all` runs everything in order; `--dry` shows
`<placeholders>` and sends nothing. The response is server data in a file that
may come from a repo, so a captured value is **data, never a template** (it
cannot pull in `{{token}}`), counts as a secret (not sent to another host
without `--trust-hosts`), is limited to key / `[n]` paths (no jq), may not
contain line breaks in a URL or header, and is never stored.

_Check the response (CI)._ `# @assert <jq>` lines above the request line (any
number) say what the response must look like. The expression runs on one object,
`{status, headers, body, ms}` (header names lower-case, a non-JSON body is a
string), and passes only when its first result is `true`:

```
### create
# @assert .status == 201
# @assert .body.data.id != null
# @assert .headers["content-type"] | test("json")
POST /users
```

Each assertion prints `✓` or `✗ <expr> — <reason> (line N)` on stderr; the body
is printed as usual. A failure exits **3** (`--all` stops at the first failing
request, `--keep-going` runs on and ends with a summary). When a request has
`@assert`, the assertions decide: `# @assert .status == 404` passes for a 404,
and a request without them still fails with 22. A bad expression is a usage
error (exit 2) before any request is sent, and `--dry` skips them. Failure lines
show the expression and a reason (`false`, `null`, `not a boolean (string)`,
`evaluation error`, `timed out`), never the response, so CI logs do not collect
tokens. `{{...}}` is not expanded inside assertions. VS Code and JetBrains
ignore the unknown comment, so the same file works there.

**`api history`, `api again [N]`** list and repeat recent requests. Only the
method, path, filter and the **server** (its origin, not a token) are stored:
never bodies, headers or tokens, and a path with a secret-looking query
parameter is not recorded. The file is shared by every project, so `history` and
`again` only see requests to the server the shell points at (`history -a` shows
all of them, with the server; entries from before servers were recorded show as
`(no server)` and are never repeated). `again` refuses an entry that went to
another server instead of sending it to this one. A GET is repeated from its
path. `again --pick` chooses with fzf.

What you pick with `api openapi pick` or `api gql --pick` is kept as a file in
`.api/requests/` (`--no-save` skips that), next to a `.orig` copy of the
skeleton it generated, and the history holds a pointer to it (`RUN  <name>`),
not the query. It is remembered even when the request failed (a 400 for a body
you still have to fix). `api again` runs that file again, only from the project
it was saved in and for the same server, and asks again before anything that
changes data (`--yes` skips the question).

`api again --edit` opens the file in your editor: save to run it, quit without
saving to cancel. What you saved stays, whether or not the run worked, so the
next `--edit` starts from your last attempt; the version it replaced is kept
once as `<name>.http.bak`. `api again --default` starts that edit from the
original skeleton instead (the `.orig` copy), when you want to begin again. With
`--pick`, `--edit` and `--default` offer only saved requests, and
`api history --runs` lists just those (with the same numbers as the full list).

`api requests ls` shows the saved requests with their method and URL, and
`api requests rm <name>... | --all` deletes them with their `.bak` and `.orig`
(`--all` asks first; `--yes` skips that). Nothing else prunes `.api/requests/`.

A `gql` query containing `{{`, or whose variables look like secrets, is saved
without them (the runner would expand `{{`).

**`--har file`** appends the exchange to a HAR 1.2 file for bug reports or
devtools: credential headers, cookies, secret-looking JSON fields and query
parameters are masked unless `--har-secrets`.

**`api openapi sync|list`** teaches completion the endpoints your API really
has. `sync` fetches an OpenAPI 3 / Swagger 2 document and keeps a small index of
its method + path pairs (and summaries) for the **active server**:

```bash
api openapi sync                    # the env's `openapi` path, else tries /openapi.json, /swagger.json,
                                    # /v3/api-docs, /api-docs, /api-json, /docs-json
api openapi sync /docs/spec.json    # an explicit path (no probing); a full URL works too
api openapi sync --file ./spec.json # a local file
api openapi list users --method POST
api openapi                         # fzf picker: edit the request, save to run, confirm if it changes data
api get /us<TAB>                    # /users  List users   /users/{id}  Get a user
```

**The picker (like `api gql`).** `api openapi` (or `api openapi pick [text]`)
opens fzf over `METHOD  PATH  summary` with a preview of the summary and the
body skeleton. Pick one and your editor opens a **`.http` request**: the method
and path, `Accept`, and for POST/PUT/PATCH a JSON body skeleton built from the
spec's request schema (required fields first). Fill in any `{placeholder}` in
the path, add `# @assert` lines if you like, and **save to run; quit without
saving to cancel**. Anything that changes data asks first, as `api gql` does:

```
✱ POST /users changes data on http://localhost:3000: run it? [y/N]
```

What you **saved** decides whether to ask (a picked GET you edited into a DELETE
asks; a GET never does). After you confirm, the request is kept as
`.api/requests/<name>.http` (never overwriting an earlier one: `-2`, `-3`, ...),
so `api run <name>` repeats it later. `--no-save` keeps nothing, `--yes` skips
the question, `--dry` shows the curl, and `--print` only prints the command
(`api post /users '{}'`) with no editor. It needs fzf and a synced index;
`api openapi list` works without fzf.

`sync` needs no configuration: it only needs a base URL (`api env load`, `--env`
or `--base`) and tries the usual locations. If your spec lives somewhere else,
say so once in the config and `sync` stops guessing:
`api config set env.local.openapi /docs/spec.json` (a **path** on that env's
server, never a URL), then `api --env local openapi sync` (or after
`api env use local` / `api login --env local`). `api config init` writes the
commented example in both templates.

The document is fetched with the normal request pipeline (your token goes to the
active base URL only). The raw spec is **not** kept: only an index of sanitised
strings (control characters dropped, bounded sizes and counts) is written to
`~/.cache/api/openapi/<scheme>_<host>.json` (owner-only;
`$API_OPENAPI_CACHE_DIR` overrides), one file per server, so a spec is only ever
offered for the server it came from. Completion reads that one file: it stays
offline. **Only JSON is read**; for a YAML spec fetch its JSON form, or convert
it (`yq -o=json spec.yaml > spec.json`) and use `--file`. Other formats can be
added as one more decoder in `internal/openapi`.

## Output

- **Table:** `--table` (or `-t`) prints an array of objects as a table (`id`,
  `name` first, then alphabetical; `--columns name,id` picks and orders), or one
  object as KEY / VALUE. Anything else prints normally.

- **Colour:** JSON is coloured like jq on a terminal (palette from `JQ_COLORS`),
  and stderr status lines are styled too: errors red, `✓` green, `⟳` cyan. Piped
  output is always plain. `-C` / `-M` / `--color` / `NO_COLOR` control it.
- **Pager:** long output on a terminal goes through `less -FRX` (it quits by
  itself when the output fits one screen). `--no-pager` or an empty `API_PAGER`
  turns it off; `api watch`, `-o` and empty output never page.
- **Viewer:** `api get`, `post`, `put`, `patch`, `del`, `form`, `gql`,
  `openapi pick` and `again` take `--viewer` to look at the result in a viewer
  that can fold and search instead of printing it: `--viewer nvim` (read-only,
  folded by indentation), `jless`, `fx`, or any command that reads stdin
  (`--viewer "bat -l json"`; words are split, no shell). It is only for you at
  the keyboard: nothing in a config file or a repo can choose it, it opens only
  when stdout is a terminal (through a pipe the body is printed as usual), the
  filter still applies, the exit code is unchanged (a viewed 500 is still 22),
  and a viewer that is missing or fails just prints the body instead. Not for
  subscriptions (pipe those) nor for the commands that print several results
  (`run`, `diff`) or stream (`watch`): pipe those, e.g.
  `api run f.http | jless`. A shell alias keeps it short:
  `alias apiv='api get --viewer nvim'`.

## Environment variables

| Variable                                | Use                                                 |
| --------------------------------------- | --------------------------------------------------- |
| `API_BASE_URL`, `API_GRAPHQL_URL`       | base URLs                                           |
| `API_TOKEN`, `API_ENV`                  | bearer token, active env name                       |
| `API_TOKEN_<NAME>`                      | per-env tokens named by `token_env`                 |
| `API_CONFIG`                            | path of the global config file                      |
| `CURL_MAX_TIME`, `CURL_CONNECT_TIMEOUT` | default limits in seconds (300 / 10)                |
| `API_DEBUG=1`, `API_SUMMARY=1`          | same as `--debug`, `--summary`                      |
| `API_COLOR`, `NO_COLOR`, `JQ_COLORS`    | colour mode; disable; jq-style palette              |
| `API_PAGER`                             | pager command (default `less -FRX`; empty disables) |
| `API_GQL_ENDPOINTS`                     | extra `gql -e` completions (space/comma)            |
| `API_OPENAPI_CACHE_DIR`                 | where `openapi sync` keeps its indexes              |

These are plain (unexported) shell variables, as before. The shim forwards them
to the binary on each call, so they never leak into other child processes.

## Differences from the curl helpers

`api` is a native client, so `~/.curlrc` is **not** read. What that changes:

- no automatic response footer: add `--summary` (or `API_SUMMARY=1`)
- no implicit retries: opt in with `--retry N` (a login mutation is never
  silently sent twice)
- redirects are followed (up to 10), the default total timeout is 300s, and
  `Accept-Encoding: gzip` is handled transparently, like your `.curlrc`
- for anything else, `-- <curl args>` runs real curl (which does read `.curlrc`)

## Exit codes

`0` ok · `1` generic / `diff` found differences · `2` usage · `3` a `# @assert`
failed · `4` GraphQL errors (`--strict`) · `5` bad jq filter · `22` HTTP >= 400
(body is still printed) · `28` timeout · `7` connect failed · `6` DNS failed.
`api get ... && echo ok` works.

## Completion

`api <TAB>` completes commands, flags and:

- the first argument of `get`/`post`/...: the operations of the synced OpenAPI
  spec for that method (with their summaries), then recently requested paths
  (`~/.local/state/api/history`)
- `--env`, `env use`, `diff --envs`: the envs in config; `config get|set`: keys
- `env load`: the `.env.<suffix>` files of the project
- `run`: saved files, then the requests inside the chosen file
- `again`: the history entries; `-H`: common headers
- `gql -e`: the env's `endpoints`, `$API_GQL_ENDPOINTS`,
  `~/.cache/ugql/<server>/*.json`

Completion is offline and fast; it never calls the network.

## Not included

- **Token auto-refresh.** A plain request cannot hand a refreshed token back to
  your shell (a child process cannot change its parent), so every later call
  would refresh again, or the token would have to be written to disk, which this
  tool never does. When a token expires, run `api login` (or `upscale <env>`)
  again.

## Development

```zsh
make check      # gofmt + vet + go test -race (httptest servers)
make e2e-keychain  # the real Keychain, one throwaway item (macOS)
make e2e-secretservice  # the real Secret Service via secret-tool (Linux desktop)
make build      # bin/api
```

`make e2e` runs `test/shim.zsh`: the zsh shim against real local servers (eval
of login / `env use`, global flags before the subcommand, unexported variables,
per-env token forwarding). It needs python3 and localhost ports.

Layout and conventions: see `CLAUDE.md`.
