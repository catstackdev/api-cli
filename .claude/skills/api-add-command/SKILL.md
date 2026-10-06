---
name: api-add-command
description:
  Add a subcommand, flag or completion to the Go `api` CLI (personal-lib/api)
  following its conventions: one httpc.Spec pipeline, ExitError exit codes,
  offline completion, httptest coverage, README update. Use when asked to add
  or change an `api` command, flag or completion.
allowed-tools: Bash, Read, Edit, Write
argument-hint: '<command or flag to add>'
---

# Add a command / flag to `api`

Read `personal-lib/api/CLAUDE.md` first (the gotchas section). Then:

1. **Pick the file.** One concern per file in `internal/cli/`. New request
   command -> its own file next to `gql.go`; tiny helpers -> `tools.go`.
2. **Build on the pipeline, not around it.**
   - Requests: `a.newSpec(method, target, o)` -> `setJSONBody` ->
     `a.dispatch(...)` (or `a.send` + `a.finish` when you need the response).
     Never construct `http.Request` directly; `--dry`, `--debug` and the `--`
     curl passthrough all depend on the Spec.
   - Bind the shared flags with `o.bind(cmd, withBody)`; add only flags that are
     specific to the command.
   - Positionals: `splitPositionals` + `isFilter` (do not re-implement filter
     detection).
3. **Output.** All styling lives in `internal/ui`. Status lines go through
   `a.say("✗ ...")` (never a bare `fmt.Fprintln(a.io.Err, ...)`); colour is
   `a.colorOut()` / `a.colorErr()`; long body output goes through `ui.Page`
   (not for looping or file-writing commands); tables use `ui.Table`.
3b. **Errors and exit codes.** Return `*ExitError{Code, Msg}` for a specific code
   (see the constants in `root.go`); a plain error is exit 2. Messages go to
   stderr; stdout stays pipeable.
4. **State changes need `--emit`.** If the command must change the user's shell,
   print shell code only under `--emit`, build every value with
   `session.Quote`/`Export`, and add the subcommand to the `case` in
   `src/api.sh` (`mutating`). Anything else stays a plain pass-through.
5. **Completion.** Set `ValidArgsFunction` (use `pathCompletion` for a path
   argument) and `RegisterFlagCompletionFunc` for flag values. It must be
   offline and fast.
6. **Test.** Add cases to `internal/cli/cli_test.go` using `server(t, ...)` and
   `run(t, stdin, args...)`: success, the failure exit code, and the edge that
   motivated the change.
7. **Docs.** Update the command/flag table in `README.md`. Update `CLAUDE.md`
   only if you changed an invariant.
7a. **Secrets.** A command that takes a password or token must accept a value
   reference (`a.resolveRef`; `maskRef` under `--dry`), never a bare argument
   stored or echoed anywhere, and must never read the Keychain from a project config.
7b. **Security review.** Does the command send a request somewhere the user did
   not type (config, a `.http` file, two envs at once)? Then follow the
   invariants 10-17 in `CLAUDE.md` (token origin, mutations, masking, no secrets
   stored).
8. **Verify.** Run the `api-verify` skill; `make check` alone is not enough for
   anything that touches the shim.

Do not add `cget`-style functions to `src/api.sh` (they would shadow `curl.sh`,
which `up-scale.sh` depends on).
