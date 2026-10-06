# `.http` examples

Runnable files for `api run`. A `.http` file is a plain-text list of requests
(the JetBrains / VS Code REST Client format), so the same file works in your
editor and from the shell.

| File                             | Shows                                                                        |
| -------------------------------- | ---------------------------------------------------------------------------- |
| [`users.http`](users.http)       | variables, a query string, JSON bodies, headers, filters, a body from a file |
| [`chaining.http`](chaining.http) | using one response in the next request (`create`, then `fetch` and `delete`) |
| [`checks.http`](checks.http)     | `# @assert` response checks for CI                                           |
| [`user.json`](user.json)         | the body file `users.http` reads with `< ./user.json`                        |
| [`server.py`](server.py)         | a tiny in-memory API with the endpoints above (Python 3, no installs)        |
| [`.api.toml`](.api.toml)         | project config: an env `local` pointing at that server                       |

## Try them

Two terminals. Run `api` from **inside this directory**: it uses the nearest
`.api.toml` above where you are, which is how it finds the `local` env.

```zsh
# terminal 1
python3 server.py                 # http://127.0.0.1:8787, logs every request

# terminal 2
cd examples
api config validate               # reads .api.toml
api run users.http --list         # what is in the file
api run users.http --all          # every request, in order
api run chaining.http --all
api run checks.http --all         # 7 assertions, exit 0
```

From another directory, name the server instead of relying on the config:

```zsh
api --base http://127.0.0.1:8787 run examples/users.http --all
```

The server keeps its data in memory and never deletes its seed users (ids below
100), so the files can be re-run. Restart it to start clean.

## Running one request

```zsh
api run users.http "List users"                    # by name
api run users.http 2 '.data[0]'                    # by number, with a jq filter
api run users.http "Get user" --var id=2 .data.name   # --var beats @id in the file
api run users.http "Create user" --dry             # print the curl, send nothing
api run chaining.http fetch                        # runs `create` first, then `fetch`
```

- `--dry` masks secret-looking headers (you will see
  `Idempotency-Key: [REDACTED]`) and sends nothing. `--all --dry` prints the
  whole file.
- Exit codes: `0` ok, `22` an HTTP error (try `--var id=999`), `3` a failed
  `# @assert`, `5` a bad jq filter, `2` a usage error such as an undefined
  variable.
- Quote filters like `'.data[0]'`: zsh would otherwise try to expand the
  brackets.

## The format in a minute

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

{"name": "Ann"}

### Fetch it back
GET /users/{{create.response.body.data.id}}
```

- `@limit = 10` is a file variable, used as `{{limit}}`. `--var limit=5`
  overrides it. `{{$uuid}}`, `{{$timestamp}}` and `{{$isoTimestamp}}` are built
  in.
- `###` starts a request and the rest of the line is its name. `# @name` gives
  it another one, which is what `{{create.response...}}` refers to.
- `# @filter` is the default jq filter, `# @assert` checks the response. Other
  `#` and `//` lines are comments, and a comment must be on its own line.
- A path starting with `/` goes to the **active server** with your session
  token. A full URL on another host gets no token unless you pass
  `--trust-hosts`.
- After the request line come the headers, a blank line, then the body.
  `< ./user.json` reads the body from a file next to the `.http` file.

The full list of variables, captures and checks is in the
[main README](../README.md#http-files-a-short-tour).

## Using your own API

Copy a file, change the paths, and point `api` at your server: a `url` under
`[env.<name>]` in a `.api.toml` (as here), or `--base` for one run. If the API
needs a token, `api login --env <name>` once; the requests then carry it to that
server only. Keep your files in `.api/requests/` of the project so
`api run <name>` finds them, or let `api openapi` write one from your API's
spec.
