---
name: api-verify
description:
  Verify a change to the Go `api` CLI (personal-lib/api) or its shell shims
  (internal/cli/shim.zsh, shim.bash) end to end before calling it done: make check, then a fresh-shell
  test against a real local server covering login/eval, .env loading,
  unexported-variable forwarding and eval injection. Use after editing anything
  under personal-lib/api/.
allowed-tools: Bash, Read
argument-hint: '[what changed]'
---

# Verify an `api` change

`go test` alone is not enough here: the risky parts live where Go meets the
shell (eval'd output, unexported vars, completion), and only a real `zsh`
catches those. Do all three steps.

Sandbox notes: localhost listeners and Go module downloads are blocked inside
the Bash sandbox. Run the test/server steps with the sandbox disabled (or have
`sandbox.network.allowLocalBinding: true`), and keep `GOMODCACHE`/`GOCACHE` in
the scratchpad with `GOPROXY=off` once modules are cached.

## 1. Library checks

```bash
cd ~/.config/cybercat/profile/personal-lib/api
make check          # gofmt -l must be empty, go vet, go test -race
make build          # bin/api, picked up by the shim without install
```

Any behaviour change needs a new case in `internal/cli/cli_test.go`.

## 2. Shim end to end (fresh `zsh -c`, never your live shell)

Run `make e2e` first: it automates most of the list below (22 checks: eval of
login / `env use`, global flags before the subcommand, unexported variables,
per-env tokens, no token to another env). Then do whatever it does not cover.

Start a tiny local server (Python `http.server` is enough) whose POST returns a
token containing `'`, `$(touch PWNED)` and `"`, and whose GET echoes the
`Authorization` header. Then, in `zsh -c` from a temp project that has an
`.env.local` using `$VAR` expansion:

- `api env load local` -> status shows the expanded `API_BASE_URL`
- `api login /auth '{...}'` -> `API_TOKEN` set in the shell, and **no `PWNED`
  file is created** (eval injection check)
- `api get /ping .auth` -> shows the bearer token (forwarded)
- an UNEXPORTED `API_BASE_URL` (`typeset +x`) still works (forwarding check)
- `api env clear` empties `API_TOKEN`; `api login --help` prints help and is not
  eval'd

## 3. Profile load scenarios

Run the profile's `verify-init-change` skill (new shell, `exec zsh`, same-shell
re-source), plus: `type cget _creq` must still resolve to `src/curl.sh` (the
shim must not shadow it), and in an interactive zsh after `compinit`,
`_comps[api]` is `_api`.

Report which of the three passed; do not call it done from step 1 alone.
