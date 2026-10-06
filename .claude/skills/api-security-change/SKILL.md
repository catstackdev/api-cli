---
name: api-security-change
description:
  Change or add a security rule in the Go `api` CLI (personal-lib/api): anything
  touching resolve.go, tokencache.go, refs.go, run*.go or schema*.go, or any
  code that attaches a token, resolves a credential reference, follows a
  redirect, prints a request, or expands {{...}}. Walks through reading
  SECURITY.md, writing a mutation-checked test, and keeping the SECURITY.md
  tables in sync. Use before editing those files and again before calling the
  change done.
allowed-tools: Bash, Read, Edit
argument-hint: '[rule or file being changed]'
---

# Change a security rule in `api`

Every promise in `SECURITY.md` has a function that enforces it and a test that
fails when the rule is reverted. A change here is only done when that stays
true. `TestSecurityDocNamesExist` fails the build if a function or test named in
a table no longer exists.

## 1. Read before editing

- `SECURITY.md` (the rule -> code symbol -> test tables), then the gotchas in
  `CLAUDE.md` that match your change: 10, 22-23, 27-29, 32-33, 36.
- Decide which existing rule your change touches. If it loosens one (a new place
  a token can go, a new source of `@env:`, a relaxed `isReadOnlyQuery`), stop
  and tell the user: that is a design decision, not a refactor.

## 2. Change the code through the existing helpers

Do not add a parallel path. Use the one that already owns the decision:

| You are adding...                 | Go through                                                        |
| --------------------------------- | ----------------------------------------------------------------- |
| something that attaches a token   | `newSpecFor`, `resolveFor`                                        |
| a credential reference            | `resolveRefFor` / `destOrigin` (origin computed BEFORE resolving) |
| a request printed to the user     | `curlLine` and the `har.Mask*` maskers                            |
| a token cache read or a 401 retry | the `tokencache.go` helpers, `sendRecovering`                     |
| anything that expands `{{...}}`   | `interpolateOptions`                                              |
| shell code printed for `eval`     | `session.Quote` / `Export`                                        |

## 3. Write the test, then mutation-check it

1. Add a case in the matching `*_test.go` (`config_security_test.go`,
   `bound_secrets_test.go`, `leaks_test.go`, `tokencache_test.go`, ...). Prefer
   one subtest per attack, driven through `cli.Run` with an `httptest` server
   and `secretstore.NewMemory()`; never the real Keychain.
2. Run it with the fix: it must pass.
3. **Revert the fix** (comment out or undo the guarding line), run the same
   test: it must FAIL. Restore the fix and run again. A test that passes both
   ways proves nothing. Say in the report that you did this.

```bash
cd personal-lib/api
go test -count=1 -race ./internal/cli -run '<TestName>'
```

Localhost listeners are blocked by the Bash sandbox: run tests that start a
server with the sandbox disabled, or with `sandbox.network.allowLocalBinding`.

## 4. Keep SECURITY.md honest

- New or changed rule: add or update its row (Rule | Code `name()` | Tests).
  Names must match real functions and tests exactly.
- Renamed a function or test: update every row that names it.
- If the change adds a gotcha others could trip over, add a numbered entry to
  `CLAUDE.md`.

## 5. Finish

```bash
make check                              # gofmt, vet, go test -race -shuffle=on
go test -count=1 ./internal/cli -run TestSecurityDocNamesExist
```

If login, the token cache, `run` capture, `schema` or `diff` changed, also run
`make smoke`; if the shim or `env use` changed, run `make e2e` (see the
`api-verify` skill).

Report: which rule changed, the new test name, that it was seen failing without
the fix, and which of the checks above passed.
