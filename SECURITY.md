# Security model

`api` sends your tokens and passwords over the network on your behalf, and it
reads configuration from places you may not control (a cloned repo's
`.api.toml`, its `.env.*` files, aliases, `.http` files, a command pasted from a
README). This file lists what is promised, where the code enforces it and which
test proves it. **Every test named here was verified by reverting the rule and
watching the test fail.** `internal/cli/security_doc_test.go` fails the build if
a function or test named in a table below no longer exists, so this file cannot
rot silently.

## What is protected, and what is not

Protected: `api` itself being misdirected (by a config file, an alias, a `.http`
file, a pasted command, a redirect, a debug flag) into sending a token or
password to a server you did not choose.

**Not** protected: code you run. `api env load` *sources* the project's `.env`
file with your shell (that is running its code: it can export `HTTP_PROXY` or
call `security find-generic-password`); any process of yours can run `security`
and read your Keychain items. Only `api env load` `.env` files you trust.

The token cache (when you enable it) lengthens how long a token lives on your
machine, which is why it is opt-in and global-config-only.

## Rules

Code column: a function in the code base (`name()`). Tests column: test names.

### Where credentials may go

| Rule | Code | Tests |
| ---- | ---- | ----- |
| Your session token goes to the active base URL only; an absolute URL on another host gets none unless `--token` or `--trust-hosts` | `newSpecFor()` | `TestAbsoluteURLOnAnotherHostGetsNoSessionToken` |
| A session env NAME alone never attaches a profile (headers, `token_env`, login): the shell must really point at that env's server | `sameServer()` `resolveFor()` | `TestSessionEnvNameCannotAttachAProfileToAnotherServer` |
| `--env X` reuses the session token only for the same name AND origin; `default_env` never reuses it | `resolveFor()` | `TestEnvFlagBeatsSessionAndNeverLeaksTheSessionToken` `TestDefaultEnvOnlyWhenNoSessionAndNeverUsesTheSessionToken` |
| A project config cannot set `token_env`, nor move the url of a global env that has one | `Load()` | `TestProjectConfigCannotRouteTokensToItsOwnServer` |
| `token_env` may only name `API_TOKEN_*` variables | `ValidateTokenEnv()` | `TestBadOrDangerousConfigIsIgnoredWithAWarning` |
| Redirects to another origin are refused (a 307/308 would re-send the body, a password included) unless `--trust-hosts` | `checkRedirect()` | `TestCrossHostRedirectNeverReceivesTheLoginBody` `TestTrustHostsAllowsACrossHostRedirect` |
| Origins are compared normalised (scheme, lower-case host, default port dropped) | `NormalizeOrigin()` `origin()` | `TestNormalizeOrigin` |

### Secrets (Keychain)

| Rule | Code | Tests |
| ---- | ---- | ----- |
| A Keychain secret bound to servers (`--for`) is released only to them, for every source: typed command, global config, project config, alias | `resolveRefFor()` `AllowedFor()` | `TestClonedRepoCannotRedirectABoundSecret` `TestPastedCommandCannotRedirectABoundSecret` `TestDiffRequiresASecretToBeBoundToBothServers` |
| The destination is computed BEFORE any reference is resolved; an unknown destination fails closed | `destOrigin()` | `TestClonedRepoCannotRedirectABoundSecret` |
| A project login may use only bound secrets and never `@env:`; a project cannot redefine the login of an env the global config anchors | `Load()` `resolveRefFor()` | `TestProjectLoginRules` `TestProjectConfigCannotDefineOrMoveALogin` |
| A login with no url (server taken from `api env load`) is treated as strictly as a project login and contributes only its login | `loginFromConfig()` `resolveFor()` | `TestUrlLessLoginCannotBeAimedByAClonedReposEnv` `TestUrlLessProfileContributesOnlyItsLogin` |
| A literal password in a config file or `config set` is rejected | `ValidateLogin()` | `TestConfigRejectsLiteralPasswords` |
| `--dry` never reads the Keychain, prompts or sends | `maskRef()` | `TestDryRunNeverTouchesSecrets` |
| Replacing a password keeps its server binding | `secretCmd()` | `TestSecretSetBindShow` |
| Keychain entries are namespaced, stored as printable ASCII and written over stdin, never as an argument | `encode()` `decode()` | `TestKeychainFormats` `TestNamesAreValidated` |

### Token cache (`defaults.token_cache`, `defaults.auto_login`)

Both are off by default and honoured from the GLOBAL config only. A cached token
is a credential at rest (Keychain item `api:token.<env>`, bound to the env's base
origin), so it lives longer than the shell that logged in: `api logout` removes it.

| Rule | Code | Tests |
| ---- | ---- | ----- |
| A project config cannot enable the cache or auto login (they decide whether a token outlives your shell); `config set --local` refuses them | `LoadWith()` `configCmd()` | `TestCacheIsOffByDefaultAndIgnoredFromProjectConfig` |
| A cached token is released only to the origin it was stored for, never unbound, and never to another origin even with `--trust-hosts` or a repo that reuses the env NAME | `readCachedToken()` `newSpecFor()` | `TestCachedTokenIsSentToTheBaseUrlOnly` `TestCachedTokenIsNotUsedWhenTheServerChanged` `TestUnboundOrForeignCacheEntriesAreIgnored` |
| A token is cached only when it came from the env's own base URL | `saveCachedToken()` | `TestLoginCachesTokenBoundToTheEnvUrl` |
| `--dry` never reads the cache; a token about to expire (60s) is not used | `newSpecFor()` `cachedToken()` | `TestDryRunNeverReadsTheCache` `TestExpiredCachedTokenIsNotUsed` |
| After a 401, recovery acts only on a token `api` attached itself to the active base URL: not `--token`, not `-H Authorization`, not a login, and never on another server (even one you passed `--trust-hosts`); at most one automatic login per invocation | `retryOn401()` | `TestAutoLoginDoesNotTouchExplicitTokens` `TestAutoLoginNeverReplaysToAnotherServer` `TestLoginRequestsAreNeverRetried` `TestAutoLoginRunsAtMostOncePerInvocation` |
| Automatic login uses only `@keychain:` references: never `@prompt` (no surprise prompt) and never `@env:` | `resolveRefFor()` `autoLogin()` | `TestAutoLoginAcceptsOnlyKeychainReferences` |
| `api logout` unsets this shell's token only for the env the shell is on | `logoutCmd()` | `TestLogout` |

### Interactive `api gql` (fzf + editor)

| Rule | Code | Tests |
| ---- | ---- | ----- |
| A mutation asks for confirmation before it runs (default no), judged on the query that will actually run, so a query edited into a mutation asks too; `--yes` skips it | `gqlInteractive()` `isReadOnlyQuery()` | `TestGQLPickAsksBeforeRunningAMutation` `TestGQLPickAsksAgainWhenYouEditAQueryIntoAMutation` |
| Quitting the editor without saving (or an editor error) cancels; invalid variables stop the run | `editTemp()` | `TestGQLPickQuittingWithoutSavingCancels` `TestGQLPickSendsTheVariablesYouEdited` |
| The printed one-liner leaves variables out when a name looks like a secret, and comment lines never reach the query | `hasSecretKey()` `stripCommentLines()` | `TestGQLPickAsksBeforeRunningAMutation` `TestCommentLinesNeverReachTheQuery` |

### `api schema` browser and editor

| Rule | Code | Tests |
| ---- | ---- | ----- |
| The editor program comes from your global config or your own environment, never from a project file (it is a program that gets run) | `editorCommand()` `LoadWith()` | `TestEditorComesFromYourConfigNeverFromAProjectFile` |

### `api run` response capture

| Rule | Code | Tests |
| ---- | ---- | ----- |
| A captured response value is data, never a template: a server cannot make `{{token}}` or `{{$env ...}}` expand inside it | `InterpolateWith()` `interpolateOptions()` | `TestCapturedValuesAreDataNeverTemplates` `TestLiteralVariablesAreNeverReExpanded` |
| A captured value counts as a secret: a request to another host is refused without `--trust-hosts` | `interpolateOptions()` | `TestCapturedValuesGoToTheActiveServerOnly` |
| The path into a body is keys and `[n]` indexes only, walked in Go (jq could read your environment); line breaks in a captured URL or header value are refused | `walkBody()` `runRequest()` | `TestCapturePathsAreKeysAndIndexesOnly` `TestCapturedValuesCannotInjectHeaders` |
| Dependencies run once, cycles are refused, a failed dependency stops the request, `--dry` sends nothing | `runDependencies()` | `TestCaptureCyclesAreRefused` `TestFailedDependencyStopsTheRequest` `TestCaptureDryRunSendsNothingAndShowsPlaceholders` |

### `api run` assertions (`# @assert`)

| Rule | Code | Tests |
| ---- | ---- | ----- |
| An assertion is jq from a file that may come from a repo: it is compiled without an environment loader, so `env` and `$ENV` are empty | `Compile()` | `TestAssertCannotReadEnvironment` |
| A failed assertion prints the expression and a reason, never response data: bodies and headers hold tokens and jq's own error text quotes values | `Eval()` `checkAsserts()` | `TestAssertFailureNeverPrintsResponseData` `TestRunAssertOutputNeverContainsResponseData` |
| An evaluation has a time limit, so `last(range(infinite))` cannot hang a CI job | `Eval()` | `TestEvalHasATimeout` |
| Assertions are compiled before the first request is sent, so a typo is a usage error and never a half-finished run; there is no `{{...}}` expansion inside them | `compileAsserts()` | `TestRunAssertSyntaxErrorIsCaughtBeforeAnyRequest` |
| With `@assert`, the assertions decide the exit code (3); the HTTP-status exit (22) applies only to requests without them | `finishAsserted()` | `TestRunAssertDecidesInsteadOfTheHTTPStatus` |

### Cache files written by `api schema sync`

| Rule | Code | Tests |
| ---- | ---- | ----- |
| Endpoint names (which a project config can supply) become file names only if they are plain labels (a URL is not one); nothing is written outside the cache dir, and `--dry` writes nothing | `schemaLabel()` `ValidLabel()` | `TestSchemaSyncNeverWritesOutsideTheCache` `TestSchemaSyncRefusesUrlEndpointsAsCacheLabels` `TestSchemaDryRunSendsAndWritesNothing` |
| Introspection requests follow the normal destination rules: your token never goes to another server | `introspect()` | `TestSchemaNeverSendsYourTokenToAnotherServer` |

### What gets printed or stored

| Rule | Code | Tests |
| ---- | ---- | ----- |
| `--dry`, `--debug` and `API_DEBUG` print the request with secrets masked | `curlLine()` | `TestDebugAndDryOutputNeverContainSecrets` |
| HAR files mask credential headers, secret-looking JSON fields, form pairs and query parameters unless `--har-secrets` | `Build()` `maskBody()` | `TestSecretsAreMaskedByDefault` `TestHARExport` |
| History stores method, path and filter only; paths with secret-looking query parameters are not recorded | `Safe()` | `TestSecretsAreNeverRecorded` `TestHistoryAndAgain` |
| `env example` never prints values (secret-looking keys and credential URLs stay blank even with `--keep-values`) | `Example()` | `TestExampleNeverLeaksValuesByDefault` `TestEnvExampleNeverPrintsValues` |
| Config errors name the key, never the value | `ParseText()` | `TestSecretsAndUnknownKeysAreRejectedOnRead` |

### Things a repo or an alias could try

| Rule | Code | Tests |
| ---- | ---- | ----- |
| Project aliases cannot carry references or `--trust-hosts`, `--token`, `--base`, `--har-secrets` | `unsafeAlias()` | `TestProjectAliasCannotCarryReferencesOrTrustFlags` |
| `.http` files do not get your token for other hosts; `{{token}}` / `{{$env}}` to another host is refused; `$env` reads `API_*` only; no header injection | `runRequest()` | `TestRunSafety` |
| `diff` runs GET and read-only GraphQL only (it hits two servers) | `isReadOnlyQuery()` | `TestDiffGraphQLReadOnlyAndMutationsRefused` |
| `env_suffix` is a plain suffix, never a path (`env load` sources that file) | `ValidateEnvSuffix()` | `TestEnvSuffixCannotBeAPath` |
| `[vars]` names are validated identifiers (they end up in eval'd shell code) | `ValidateVarName()` | `TestVarsConfigIsValidated` |
| `config set` never writes through a planted symlink (random exclusive temp file) | `writeAtomic()` | `TestSetDoesNotWriteThroughAPlantedSymlink` |
| `doctor --fix` skips symlinks and never touches `.env.example` | `fixEnvPerms()` | `TestDoctorFixTightensEnvPermissionsOnly` |

### The shell boundary

| Rule | Code | Tests |
| ---- | ---- | ----- |
| Everything emitted for `eval` is single-quote escaped | `Quote()` | `TestQuoteIsInjectionSafe` `TestLoginSendsNoBearerAndEmitsQuotedExport` |
| Session variables stay unexported (`env use` emits plain assignments) | `useEnvCmd()` | `TestEnvUseEmitsUnexportedAssignments` |
| Login sends no bearer token (a stale one must not ride along) | `loginCmd()` | `TestGraphQLLogin` |

## Adding a rule

1. Write the test first, through `cli.Run` with a real local "evil" server where
   a server is involved.
2. Implement the rule.
3. Revert the rule: the test must fail. Restore it.
4. Add a row here (the guard test checks the names exist).
