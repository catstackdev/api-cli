# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

Guidance for working on `api`, the Go CLI in `personal-lib/api/`. User docs are
in `README.md`; this file is the architecture and the traps.

## What this is

A Go CLI that replaced the zsh helpers of the retired `src/curl.sh`:
`api get|post|gql|login|logout|env|json|run|diff|schema|secret|config|doctor|...`,
with cobra tab completion and a built-in jq (gojq). It is a standalone module
(`cybercat/api`). Its zsh shim is embedded in the binary (`internal/cli/shim.zsh`,
printed by `api shell-init`), so a user installs one file and adds
`eval "$(command api shell-init)"` to `~/.zshrc`. The profile's `src/api.sh` is
just that one line.
`src/up-scale.sh` is built on it (login, `ugql-*` wrappers); its fzf pickers and
`ugql-browse` stay shell and read the `~/.cache/ugql` files `api schema sync`
writes. Security rules are in `SECURITY.md` (each has a test that fails when the
rule is reverted); read it before touching `resolve.go`, `tokencache.go`,
`refs.go`, `run*.go` or `schema*.go`.

## Layout

```
cmd/api/main.go           entry: signal-aware context -> cli.Run
internal/cli/             cobra commands; one file per concern
  root.go                 app, IO, ExitError, exit codes, resolve() (env/session
                          precedence), aliases, command wiring
  request.go              newSpec/newSpecFor, verbCmd, dispatch/send/finish pipeline
  request_flags.go        reqOpts + bind*Flags, positional/filter/`--` parsing
  request_print.go        curlLine (masked), execCurl, header printing
  gql.go  login.go  env.go  env_example.go  config_cmd.go  diff.go  run.go
  history.go  doctor.go  tools.go  complete.go  secret.go  refs.go
  resolve.go              which server/token/profile a request may use (security core)
  tokencache.go logout.go token cache, 401 recovery, auto login, api logout
  run.go run_capture.go   .http runner and {{name.response.*}} capture
  schema.go schema_sync.go  introspection commands; `schema sync` (cache writer)
  schema_pick.go            fzf browser, editor
  openapi.go                `openapi sync|list`, per-server index cache, verb path completion
internal/ui/              ALL terminal presentation: colour decision, styled
                          messages, status/header lines, pager, tables
internal/httpc/           Spec -> request / curl args / client / trace
internal/render/          body printing, gojq filter, JSON colour, Decode
internal/config/          global + project TOML, merge, Set/Get, Lint, schema.json
                          (JSON Schema; a test keeps it equal to the parser)
internal/envfile/         .env KEY NAMES only (example / check)
internal/jsondiff/        structural JSON diff with jq-style paths
internal/httpfile/        .http parser + {{variable}} interpolation
internal/har/             HAR 1.2 builder (secrets masked in Build)
internal/history/         method+path+filter for completion and `again`
internal/jsonbody/        `api json` builder (ordered, dotted keys, :=, value references)
internal/secret/          the ONE definition of "looks like a secret" (envfile, har, history)
internal/secretref/       value references: @keychain:NAME, @env:NAME, @prompt, @@
internal/secretstore/     credential store: macOS Keychain (`security`), Linux Secret Service
                          (`secret-tool`); shared entry format; fakes for tests
internal/session/         API_* env + shell quoting (Quote, Export, Unset)
internal/schema/          GraphQL introspection: signatures, index lines, embedded preview.jq
internal/jwt/             unverified exp/iat reader (display and cache expiry only)
internal/assert/          `# @assert` evaluation (see gotcha 41)
internal/openapi/         OpenAPI JSON -> sanitised method+path index (pluggable Decoder; see gotcha 42)
Makefile                  build / install / check / fuzz / cover / vuln / release / smoke
test/                     shim.zsh (make e2e), smoke.zsh + smoke_srv.py (make smoke)
SECURITY.md               rule -> code symbol -> test tables (guarded by a test)
examples/                 runnable .http files for the README tour, server.py (stub API) and
                          .api.toml to try them; internal/cli/examples_test.go dry-runs them all
skills/api-cli.md         Claude Code skill for USERS of the tool (make install-skill)
internal/cli/shim.zsh     the zsh shim (go:embed; `api shell-init` stamps shimContract in)
  shell_init.go
../../src/api.sh          profile hook: evals `api shell-init` (auto-loaded by init.sh)
```

## The one design rule: a single Spec

Every request becomes an `httpc.Spec` (`newSpec` + `setJSONBody`). The same Spec
feeds the native client, `--dry` (`Spec.CurlArgs`), `--debug`, and the `--` curl
passthrough, so they cannot drift. A new command builds a Spec and calls
`dispatch` (or `send` + `finish`). Do not build requests any other way.

## Gotchas (each one has bitten or would bite)

1. **A child process cannot change the parent shell.** `login`, `env load` and
   `env clear` only PRINT shell code under `--emit`; the shim (`internal/cli/shim.zsh`) evals it.
   Everything printed for eval goes through `session.Quote`/`Export` (token
   values come from server responses). Never print an unquoted value there, and
   keep stdout of those commands to eval-able lines only; messages go to stderr.
2. **Session vars are unexported on purpose** (`up-scale.sh` sets them without
   `export`). The binary only sees them because the shim forwards them as
   per-call prefix assignments (`_api::run`). Don't "fix" this by exporting.
3. **`.env` is sourced by zsh, not parsed in Go.** `api env load --emit` prints
   `source '<file>'` under `allexport`, so `$VAR` expansion keeps working. Do
   not add a Go dotenv parser; it silently changes semantics.
4. **jq filter detection**: an argument is a filter iff it starts with `.` and
   not `./` or `../` (`isFilter`). Keep this in one place.
5. **Key order**: gojq's maps are unordered, so sorting would reorder every
   `.data` result. The identity filter and plain paths (`.a.b[0].c`, see
   `render/path.go`) are resolved on the raw bytes, which keeps the server's key
   order and number literals. Only non-path filters (pipes, functions, `.[]`) go
   through gojq, which sorts keys; numbers are normalised there so large ids
   survive. When a script needs jq's exact key order for a program, pipe the
   result to the real `jq` (as `up-scale.sh` does for its schema shaping).
6. **Exit codes are a contract**: 22 for HTTP >= 400 (body still printed first),
   28/7/6 for transport errors, 5 for a bad filter, 4 for `--strict` GraphQL
   errors, 3 for a failed `# @assert` (which, when present, replaces 22 for
   that request). Scripts rely on `api get ... && ...`.
7. **Completion must stay offline and fast.** It runs on every TAB. No network,
   no slow disk scans. History is best-effort; never fail a request over it.
8. **Name collisions**: the shim defines only `api`. Do not add `cget`/`cpost`
   style functions to it: `src/*.sh` load in glob order and would override
   `curl.sh`, which `up-scale.sh` depends on (`_creq`, `cgql -e`, ...).
9. **Never put a `.sh` file under `src/` for this module**: `init.sh` globs
   `src/*.sh` and `src/*/*.sh`. The Go code lives in `personal-lib/` on purpose.

10. **Session precedence is in one place** (`app.resolve`): `--base/--token`,
    then `--env`, then `API_*` variables, then `default_env` (only while no
    session base is set). The session token is reused for `--env X` ONLY when X
    is the session's env AND the profile URL has the same origin as the session
    base URL (a project config can repoint an env's url, so the name alone
    proves nothing). `default_env` and any other `--env` drop the session token.
    Per-env tokens come only from `token_env`, which config validation limits to
    `API_TOKEN_*` names AND which `config.Load` honours only from the GLOBAL
    file: a project file (possibly from a cloned repo) cannot set it, and cannot
    move the `url`/`graphql` of a global env that has one (the `token_env` is
    dropped). Tests: `TestProjectConfigCannotRouteTokensToItsOwnServer` (one
    subtest per attack, each verified to fail without its fix). Keep it that
    way.
11. **Config has no secrets and rejects unknown keys.** Errors name the key and
    line, never the value (the TOML library's own text quotes the source line:
    do not use `StrictMissingError.String()`).
12. **`env use` emits plain assignments, not `export`** (session variables stay
    unexported, like `upscale-login`), and drops the old token. Any new
    shell-changing command must be added to the `mutating` case in `internal/cli/shim.zsh`
    (which skips leading global flags like `--env x`, `-M` before the
    subcommand) AND bump `shimContract` (doctor.go) together with the
    `API_SHIM` marker (the shim file holds `@CONTRACT@`, stamped from `shimContract`), so `api doctor` flags a shell still
    running the old shim.
13. **`diff` runs a request against two envs: GET and GraphQL queries only.**
    `isReadOnlyQuery` is deliberately conservative. Never relax it to allow
    mutations, and keep each env's token separate.
14. **`run` executes a file that may come from a repo.** A foreign origin gets
    no session token (`noAuth`), and a request that interpolates `{{token}}` /
    `{{$env ...}}` for a foreign origin is refused without `--trust-hosts`.
    `{{$env X}}` reads `API_*` only. Line breaks in interpolated headers/URL are
    rejected.
15. **History stores method, path and filter only**: never bodies, headers or
    tokens; paths with secret-looking query params are not recorded. That is why
    `again` repeats GET only.
16. **HAR masking lives in `har.Build`** (headers, secret-looking JSON fields,
    query/form params). `--har-secrets` is the only way off. Do not add a path
    that writes unmasked exchanges.
17. **`.env` files are read for KEY NAMES only** (`envfile`), values never reach
    stdout unless `--keep-values`, and secret-looking keys / credential URLs
    stay blank even then.

18. **`env load` maps a project's URL variables** (`env_map.go`) by emitting
    shell statements that reference the variable by NAME
    (`API_GRAPHQL_URL="$VITE_..."`): values never pass through Go. `[vars]`
    accepts URL variable names only, validated as shell identifiers (they end up
    in eval'd code) and never a token variable. The base URL is derived only
    from a GraphQL variable the loaded file provides: deriving from the session
    would overwrite the base you are logged in to.

19. **`env_suffix` is a suffix, never a path** (`config.ValidateEnvSuffix`,
    enforced on read, on `config set` and for the explicit `env load <suffix>`):
    `env load` `source`s `.env.<suffix>` in the project root, so a path from a
    config file would make a cloned repo choose which file your shell executes.
20. **Two config templates** (`config.Render(Global|Project, ...)`): the global
    one holds shared envs and `token_env`; the project one holds `[vars]`,
    `env_suffix` and project-only envs and must never suggest `token_env`. Keep
    the `[vars]` header active so uncommenting one key works.
21. **`doctor --fix` skips symlinks** (chmod would follow them out of the
    project) and leaves `.env.example` alone.

22. **Credentials are references, never values.** `@keychain:NAME` / `@env:NAME`
    / `@prompt` are resolved at the last moment (`app.resolveRef`) and never
    under `--dry` (`maskRef`). `config.ValidateLogin` rejects a literal for a
    secret-looking `login.vars` key on read and on `config set`.
23. **A secret's destination is checked before it is resolved.** Keychain
    secrets carry the origins they may be sent to (`secretstore.Secret.Origins`,
    set with `api secret set --for`). `resolveRefFor` compares the request's
    origin (`destOrigin`, computed BEFORE any reference is resolved; "" fails
    closed) with them for every source: typed command, global config, project
    config, alias. A project `[env.x.login]` is allowed (`Login.FromProject`)
    only for envs the global config does not anchor, and may use only BOUND
    secrets and never `@env:`. `token_env` stays global-only. Tests:
    `bound_secrets_test.go`, each rule mutation-checked (revert it, the test
    must fail). Scope, stated honestly: binding protects `api` from being
    misdirected, NOT from code you run (`env load` sources `.env`; any process
    can run `security`). Redirects: `httpc.checkRedirect` refuses cross-origin
    ones (a 307/308 re-sends the body) unless `--trust-hosts`.
24. **Keychain entries are `api:NAME`, stored as `b64:`+base64** (printable
    ASCII; `security -w` prints other data as ambiguous hex) and written over
    stdin to `security -i`, never as an argument. Tests must never touch the
    real Keychain: `run()` injects `secretstore.NewMemory()`; `TestKeychain` is
    opt-in (`make e2e-keychain`, throwaway item). The entry format
    (`encoding.go`) is shared by both backends. Linux uses `secret-tool`
    (`secretservice.go`, built on every OS and tested against a fake binary;
    `TestSecretService` is the opt-in real one, `make e2e-secretservice`): the
    secret goes over stdin, and a dead Secret Service must stay an error, not
    `ErrNotFound`.
25. **`resolveFor(env)` is pure**: need several envs at once (diff)? call it per
    env; never set and restore `app.envName`.
26. **Dependencies keep `go 1.24`**: `go get ...@latest` can raise the `go`
    directive (it did once, via x/term); pin versions and
    `go mod edit -go=1.24.0`.

27. **A profile applies to the session only if it is the same server**
    (`sameServer`: origin of the profile url == origin of `API_BASE_URL`, and
    the session's GraphQL URL on that server too). The env NAME is
    attacker-controllable (a cloned repo's `.env.dev`). Never pick a profile by
    name alone for anything that carries a header, token or credential.
28. **Your token goes to the active base URL only** (`newSpecFor`): an absolute
    URL on another origin gets none unless `--token` (explicit) or
    `--trust-hosts`. Project `[alias]` entries with references or
    `--trust-hosts/--token/--base/ --har-secrets` are dropped on load
    (`unsafeAlias`).
29. **Printing a request masks it** (`curlLine` via `har.Mask*`): `--dry`,
    `--debug` and `API_DEBUG` run after references are resolved, so anything
    printed must go through the maskers. Tests:
    `TestDebugAndDryOutputNeverContainSecrets`. All four of the tests in
    `leaks_test.go` were mutation-checked: revert the fix, the test must fail.
    Keep it that way for new rules.

30. **`config set` edits files in place** (`config.SetInFile`): comments and the
    commented template must survive. Every edit is re-parsed and must equal the
    intended config exactly (`reflect.DeepEqual`); otherwise it falls back to a
    rewrite and the CLI warns that comments were lost. A new write path must
    create the parent directory (a fresh machine has no `~/.config/api`).
31. **Secret bindings survive a password change** (`api secret set` without
    `--for` keeps the old origins; `--unbind` is the explicit way out).

32. **A login with no url links to `api env load`** (`resolveFor`): only its
    LOGIN is attached (never headers or `token_env`) and the server is whatever
    the shell points at. That is safe only because such a login is treated like
    a project login (`source = srcProject` in `loginFromConfig`): bound Keychain
    secrets only, never `@env:`. Tests: `TestUrlLess*` (mutation-checked). Do
    not loosen it without re-reading invariants 22-23 and 27.

33. **The token cache is global-only, bound and off by default**
    (`tokencache.go`): `defaults.token_cache` / `auto_login` come from the
    global file only (`Merge` does not copy them; `LoadWith` warns). Cached
    tokens are Keychain items `api:token.<env>` bound to the env's base origin,
    attached in `newSpecFor` only when the final URL's origin IS the base origin
    (never `--trust-hosts`, never unbound), never read under `--dry`.
    `retryOn401` (called from `sendRecovering`: the single-request commands,
    `run` and `schema`; NOT from `send`, so logins and `diff` are never retried)
    acts only on a token we attached to the active base, and an automatic login
    happens at most once per invocation (`reloginTried`, so `run --all` cannot
    log in per request); automatic login accepts `@keychain:` references only
    (`a.strictRefs`). A new place that attaches a token or recovers from 401
    must go through these helpers. Tests: `tokencache_test.go`.
34. **Secret names are indexed in `$XDG_STATE_HOME/api/secrets`** (names only,
    from `secret set/bind/show/rm`) because a credential store cannot be listed
    safely. `knownSecretNames()` unions it with every `@keychain:NAME` the
    config's logins refer to, so older secrets still show up. Cache entries
    (`token.*`) are deliberately NOT indexed.

35. **`api schema sync` output is a contract with other tools**: `~/.cache/ugql`
    (`<ep>.json`, `index.txt` = `<ep> <kind> <name>` lines, `preview.jq`,
    `.env`) is read by `fzf` pickers in `src/up-scale.sh` and the television
    channel `ugql.toml` with plain `jq`. `internal/schema/preview.jq` is
    embedded verbatim and `testdata/index.golden` was produced by the shell
    implementation it replaced; change either only together with those
    consumers. Output objects are built from structs, never maps, to keep key
    order.

36. **`run` captures are server data in a possibly-foreign file**
    (`run_capture.go`): `{{name.response.*}}` values are inserted as literals
    (`httpfile.Options.IsLiteral`), classed as secrets (foreign host needs
    `--trust-hosts`), resolved by a Go key/index walker (never jq: it can read
    `env`), never stored. Anything new that expands `{{...}}` must go through
    `interpolateOptions`.
37. **`Retry-After` is capped** (`httpc.RetryDelay`, 60s): a header must not be
    able to stall a script.

38. **`defaults.editor` is global-only** (like `token_cache`): `api schema` runs
    it. `schemaProfile()` may pick the only env that lists endpoints when none
    is selected, but only for NAMES (endpoints, prefix): requests still go to
    the server the shell points at. `schema.Detail` is the Go twin of
    `preview.jq` (the fzf and tv previews still run jq); a test keeps them
    identical.

39. **A login with no session adopts the env** (`loginCmd`): when only
    `default_env` supplied the server, the token is never attached to a
    config-chosen server, so login also emits
    `API_BASE_URL`/`API_GRAPHQL_URL`/`API_ENV`, or it would look logged out.
    Test: `TestLoginWithoutASessionAdoptsTheEnvSoTheTokenIsUsable`.

40. **Interactive `api gql`** (`gql_interactive.go`): the confirmation is
    decided by `isReadOnlyQuery` on the text that will be sent, never by what
    was picked; unsaved editor = cancel (the temp file's mtime is moved to the
    past first); comment lines are stripped before sending (collapsed onto one
    line a `#` would swallow the query); the printed one-liner never carries
    secret-looking variables. `schema.Skeleton` builds the operation (one
    selection level, plain fields only).

41. **`# @assert` is jq from a possibly-foreign file** (`internal/assert`): compiled
    with NO environment loader (gojq's CLI passes `os.Environ`, the library does
    not; never add `WithEnvironLoader`), time-limited, compiled for every selected
    request before the first one is sent, and a failure prints the expression
    plus a reason, never response data (jq error text quotes values: use the fixed
    reasons in `Eval`). No `{{...}}` expansion in assertions. Tests are
    mutation-checked: `TestAssertCannotReadEnvironment`,
    `TestAssertFailureNeverPrintsResponseData`.

42. **An OpenAPI spec is server data** (`internal/openapi`): `Parse` keeps only an
    index of sanitised strings (no control characters, bounded size/count), never
    the raw document; the cache file is keyed by the base URL's ORIGIN, never by
    the env name (gotcha 27), owner-only, and completion only reads it (offline,
    gotcha 7). Only JSON is decoded; YAML is one more `Decoder` in `decoders`
    (it needs a YAML library: the module cache has `gopkg.in/yaml.v3`, but
    `go.mod` stays dependency-light on purpose). Tests are mutation-checked:
    `TestHostileSpecIsSanitised`, `TestLimits`, the per-server completion test.
    `openapi pick` is the `api gql` flow for REST (`openapi.go`): a `.http` file in your
    editor, unsaved = cancel, and the confirmation for anything but GET/HEAD/OPTIONS
    is decided on the SAVED request, never on what was picked (gotcha 40). The
    spec's summary is written as `# openapi: <text>` so it can never become an
    annotation. `env.<name>.openapi` (where the spec lives) is a validated PATH, never a URL
    (`config.ValidateOpenAPIPath`); an explicit argument beats it, it beats the
    probes, and a configured path that 404s is an error, not a reason to probe.

## Output rules (internal/ui)

- Commands decide WHAT to print; `internal/ui` decides how it looks. Never write
  SGR escape codes or check for a TTY outside `ui` (JSON colouring is the one
  exception: it lives in `render` because it needs the jq palette).
- User-facing stderr lines go through `a.say("✗ ...")` / `✓` / `⟳` / `→`, never
  a bare `fmt.Fprintln(a.io.Err, ...)`: that is what styles them and keeps them
  plain on a non-terminal.
- Colour is `a.colorOut()` / `a.colorErr()` (flags `-C`/`-M`/`--color` are
  global, `-M` beats `-C`). Body output is buffered and sent through `ui.Page`;
  commands that loop (`watch`) or write files must not page.
- Tables for new commands: `ui.Table` (aligns on plain width, so colour never
  breaks columns).

## Working here

```bash
make check                 # gofmt + vet + go test -race -shuffle=on (run before "done")
make test                  # go test -count=1 -race ./... (no shuffle)
go test -count=1 -race ./internal/cli -run TestLogin   # single test / package
make build                 # -> bin/api (the shim finds it without install)
make fuzz [FUZZTIME=30s]   # every fuzz target; their seeds already run in `make check`
make cover                 # coverage summary (cover.out is gitignored)
make vuln                  # govulncheck (network; pinned version; not part of check)
make release               # static binaries + SHA256SUMS in dist/ (gitignored)
make e2e                   # shim end to end (34 checks), needs python3 + localhost
make smoke                 # whole tool end to end (36 checks), see below
```

- A fuzz failure writes its input to `testdata/fuzz/<Target>/`: keep that file,
  it becomes a regression seed. Before trusting a failing property, check
  whether the property is wrong (the first finding, a repeated `.env` key, was a
  wrong property).
- `govulncheck` currently reports one advisory in `golang.org/x/sys` (Windows
  only, unreachable); fixing it means bumping x/sys, which can raise the `go`
  directive (see invariant 26): not worth it until a Windows build exists.
- Tests are split per feature (`login_test.go`, `config_security_test.go`, ...);
  shared helpers are in `helpers_test.go`.
- Tests use `httptest` servers and exercise `cli.Run` end to end with injected
  `IO`; add a case there for every behaviour change. The sandbox blocks
  localhost listeners, so tests that start a server need
  `sandbox.network.allowLocalBinding: true` (or an unsandboxed run).
- Module download is also blocked by the sandbox proxy (TLS verify). Use a warm
  module cache: `GOMODCACHE`/`GOCACHE` in the scratchpad, then `GOPROXY=off`.
- `make e2e` (`test/shim.zsh`) tests the shim against real local servers: run it
  after touching `internal/cli/shim.zsh`, `env use`, `login` or token handling. It runs
  with `no_unset` on purpose: the shim must not die under `set -u`.
- Verify shim changes with the profile's `verify-init-change` skill (new shell,
  `exec zsh`, same-shell re-source), and the end-to-end shim check in the
  `api-verify` skill here.
- `make smoke` (`test/smoke.zsh` + `smoke_srv.py`) drives the real binary and
  shim against two stub servers. It uses only `api-smoke-*` / `token.smoke-*`
  Keychain items (removed on exit, and it verifies they are gone); never reuse a
  real env name there (`token.dev` is yours). Run it after changes to login, the
  token cache, schema, `run` capture or `diff`.
- Don't pipe the shim: `api env load x | tail` runs it in a subshell and loses
  the variables (this fooled a repro once). Also `status` is read-only in zsh.
- Backticks inside Go raw strings (the config templates in `edit.go`) break the
  build.
- **Install by replacing, never overwriting** (`make install`: copy to `api.new`, then
  `mv`): on Apple silicon `cp` over a binary that already ran leaves a stale cached
  code signature and the kernel SIGKILLs the new one at launch (exit 137, no output,
  while the same bytes run fine from a new inode). If `api` "stopped working" after an
  install, check `~/.local/bin/api --version; echo $?` before suspecting the code.
- Docs: update `README.md` when a flag or command changes, and
  `skills/api-cli.md` when it changes what an agent should do (exit codes, the
  session rules, a command it names). That file is shipped to users: no personal
  paths in it.

## Conventions

- Go: stdlib first; cobra for CLI, gojq for jq. Errors that should end the run
  with a code are `*ExitError`; everything else is exit 2 (usage).
- Comments explain WHY (the non-obvious constraint), not what.
- Shell (`internal/cli/shim.zsh`): house style from `~/dotfiles/CLAUDE.md` (`::` namespace,
  `local` everywhere, quote variables, zsh builtins over forks).
