#!/bin/zsh
# Smoke test of the whole tool through the REAL binary and the zsh shim, against
# two local stub servers. It covers what the Go tests cannot show together:
# secrets, the token cache across shells, auto login, schema browsing, run
# capture, diff, doctor, config validation and Retry-After.
#
# Keychain: it creates items named api-smoke-* and token.smoke-a / token.smoke-b
# and removes them on exit (it checks they are gone). It never touches other items.
# Needs python3, jq and permission to bind localhost ports:  make smoke

HERE="${0:A:h}"
API_DIR="${HERE:h}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/api-smoke.XXXXXX")"
typeset -i FAILS=0 CHECKS=0 SKIPS=0
typeset -a PIDS=()
KEYCHAIN=0
[[ "$OSTYPE" == darwin* ]] && command -v security >/dev/null && KEYCHAIN=1

export PATH="$API_DIR/bin:$PATH"
API="$API_DIR/bin/api"
[[ -x "$API" ]] || { print "✗ build first: make build"; exit 2; }
command -v python3 >/dev/null && command -v jq >/dev/null || { print "✗ needs python3 and jq"; exit 2; }

cleanup() {
  local p
  for p in $PIDS; do kill "$p" 2>/dev/null; done
  if ((KEYCHAIN)); then
    for n in api-smoke-pw api-smoke-loose token.smoke-a token.smoke-b; do
      "$API" secret rm "$n" >/dev/null 2>&1
    done
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

ok() { ((CHECKS++)); print -r -- "  ✓ $1"; }
fail() { ((CHECKS++)); ((FAILS++)); print -r -- "  ✗ $1"; }
skip() { ((SKIPS++)); print -r -- "  - skipped: $1"; }
expect() { [[ "$2" == "$3" ]] && ok "$1" || fail "$1 (got [$2], wanted [$3])"; }
contains() { [[ "$2" == *"$3"* ]] && ok "$1" || fail "$1 (no [$3] in: ${2:0:300})"; }
lacks() { [[ "$2" != *"$3"* ]] && ok "$1" || fail "$1 (found [$3])"; }

# ── servers ────────────────────────────────────────────────────────────────
start_server() { # start_server <a|b> -> REPLY=port
  : >"$WORK/$1.log"
  python3 "$HERE/smoke_srv.py" "$1" "$WORK/$1.log" "$API_DIR/internal/schema/testdata/user.json" >"$WORK/$1.port" &
  PIDS+=($!)
  local i
  for i in {1..50}; do [[ -s "$WORK/$1.port" ]] && break; sleep 0.1; done
  REPLY="$(<"$WORK/$1.port")"
}
start_server a; PA=$REPLY
start_server b; PB=$REPLY
[[ -n "$PA" && -n "$PB" ]] || { print "✗ could not start the stub servers"; exit 2; }
A="http://127.0.0.1:$PA"
B="http://127.0.0.1:$PB"

cat >"$WORK/config.toml" <<EOF2
[defaults]
token_cache = true
auto_login = true
editor = "$WORK/bin/fake-editor"

[env.smoke-a]
url = "$A"
graphql = "$A/graphql"
endpoints = ["user", "auth"]
endpoint_prefix = "v2/"
[env.smoke-a.login]
path = "/auth"
token_path = ".data.token"
vars = { user = "me", password = "@keychain:api-smoke-pw" }

[env.smoke-b]
url = "$B"
graphql = "$B/graphql"
EOF2

export API_CONFIG="$WORK/config.toml" XDG_STATE_HOME="$WORK/state" UGQL_CACHE_DIR="$WORK/ugql" HOME="$HOME"
unset API_BASE_URL API_GRAPHQL_URL API_TOKEN API_ENV EDITOR VISUAL API_EDITOR
cd "$WORK"

# a shell, the way a new terminal starts: shim loaded, nothing logged in
sh_() { zsh -c "eval \"\$(command api shell-init)\"; $1" 2>&1; }
state() { /usr/bin/curl -q -s "$1/state"; }

print "── config"
contains "config validate is clean" "$(api config validate 2>&1; echo rc=$?)" "rc=0"
expect "config schema is JSON" "$(api config schema | jq -r .title)" "api config (config.toml / .api.toml)"

print "── phase 2: secrets, doctor, JWT expiry"
if ((KEYCHAIN)); then
  print -r -- 'pw-smoke' | api secret set api-smoke-pw --stdin --for "$A" >/dev/null 2>&1
  print -r -- 'loose' | api secret set api-smoke-loose --stdin >/dev/null 2>&1
  list="$(api secret list 2>&1)"
  contains "secret list shows the bound one" "$list" "api-smoke-pw"
  contains "secret list marks the unbound one" "$list" "unbound"
  lacks "secret list never prints a value" "$list" "pw-smoke"
  contains "doctor warns about the unbound secret" "$(api doctor 2>&1)" "not bound to a server: api-smoke-loose"
  api secret bind api-smoke-loose "$A" >/dev/null 2>&1
  lacks "doctor is quiet after binding" "$(api doctor 2>&1)" "not bound to a server"
else
  skip "no macOS Keychain"
fi
exp_ok="$(python3 - <<'PY'
import base64, json, time
b = lambda o: base64.urlsafe_b64encode(json.dumps(o).encode()).rstrip(b"=").decode()
print(b({"alg": "none"}) + "." + b({"exp": int(time.time()) + 7200}) + ".s")
PY
)"
exp_old="$(python3 - <<'PY'
import base64, json, time
b = lambda o: base64.urlsafe_b64encode(json.dumps(o).encode()).rstrip(b"=").decode()
print(b({"alg": "none"}) + "." + b({"exp": int(time.time()) - 7200}) + ".s")
PY
)"
contains "env status shows when a JWT expires" "$(API_TOKEN=$exp_ok API_BASE_URL=$A "$API" env status 2>&1)" "expires in"
contains "env status flags an expired JWT" "$(API_TOKEN=$exp_old API_BASE_URL=$A "$API" env status 2>&1)" "EXPIRED"

print "── phase 3: token cache, logout, auto login"
if ((KEYCHAIN)); then
  out="$(sh_ "api env use smoke-a >/dev/null; api login; print -r -- TOKEN=\$API_TOKEN")"
  contains "login sets the token" "$out" "TOKEN=eyJ"
  "$API" secret show token.smoke-a >/dev/null 2>&1 && ok "the token was cached in the Keychain" || fail "no cached token"
  : >"$WORK/a.log"
  out="$(sh_ "api env use smoke-a >/dev/null; api get /me")"
  contains "a NEW shell works with no login (cache)" "$out" '"me"'
  contains "…and sent the cached token" "$(<"$WORK/a.log")" "auth=Bearer eyJ"
  out="$(sh_ "api env use smoke-a >/dev/null; api login >/dev/null; api logout; print -r -- TOKEN=[\${API_TOKEN-}]")"
  contains "logout unsets the shell's token" "$out" "TOKEN=[]"
  "$API" secret show token.smoke-a >/dev/null 2>&1 && fail "logout left the cached token" || ok "logout removed the cached token"

  sh_ "api env use smoke-a >/dev/null; api login >/dev/null" >/dev/null
  /usr/bin/curl -q -s -X POST "$A/rotate" >/dev/null
  before="$(state "$A" | jq .logins)"
  out="$(sh_ "api env use smoke-a >/dev/null; api get /me")"
  contains "a rejected cached token is replaced by an automatic login" "$out" '"me"'
  expect "…with exactly one more login" "$(state "$A" | jq .logins)" "$((before + 1))"
else
  skip "no macOS Keychain"
fi

print "── phase 4: schema"
expect "schema endpoints (from config)" "$(api --env smoke-a schema endpoints 2>&1 | tr '\n' ' ')" "user auth "
out="$(api --env smoke-a schema names user 2>&1)"
contains "schema names uses endpoint_prefix" "$(<"$WORK/a.log")" "POST /graphql/v2/user"
expect "schema names" "$(print -r -- "$out" | jq -c .)" '{"queries":["me","user"],"mutations":["login"]}'
expect "schema types lists the roots first" "$(api --env smoke-a schema types user 2>&1 | head -2 | tr '\n' ' ')" "Query Mutation "
expect "schema type" "$(api --env smoke-a schema type user Query 2>&1 | jq -r '.fields[1]')" "user(id: ID!): User"
api --env smoke-a schema sync user >/dev/null 2>&1
cmp -s "$UGQL_CACHE_DIR/index.txt" "$API_DIR/internal/schema/testdata/index.golden" && ok "sync wrote the index the browsers read" || fail "index.txt differs from the golden"
want="$(jq -r --arg kind query --arg n me -f "$UGQL_CACHE_DIR/preview.jq" "$UGQL_CACHE_DIR/user.json")"
expect "the Go preview equals the jq preview" "$("$API" schema show --file "$UGQL_CACHE_DIR/user.json" query me)" "$want"

mkdir -p "$WORK/bin"
cat >"$WORK/bin/fzf" <<'EOF2'
#!/bin/sh
expect=0; for a in "$@"; do case "$a" in --expect*) expect=1;; esac; done
in=$(mktemp); cat >"$in"; line=1
if [ "$expect" = 1 ]; then echo "$KEY"; line=4; fi
sed -n "${line}p" "$in"; rm -f "$in"
EOF2
cat >"$WORK/bin/fake-editor" <<'EOF2'
#!/bin/sh
for f; do :; done; cp "$f" "$EDITED" 2>/dev/null; basename "$f" >"$EDITED.name" 2>/dev/null; touch "$f"
EOF2
chmod +x "$WORK/bin/fzf" "$WORK/bin/fake-editor"
out="$(PATH="$WORK/bin:$PATH" KEY= "$API" --env smoke-a schema 2>&1)"
contains "the picker prints the chosen item" "$out" "me: User"
EDITED="$WORK/edited" PATH="$WORK/bin:$PATH" KEY=ctrl-e "$API" --env smoke-a schema >/dev/null 2>&1
expect "ctrl-e opens it in the configured editor" "$(<"$WORK/edited.name")" "user-me.graphql"

cat >"$WORK/bin/fzf" <<'EOF2'
#!/bin/sh
expect=0; for a in "$@"; do case "$a" in --expect*) expect=1;; esac; done
in=$(mktemp); cat >"$in"; line=${FAKE_PLAIN_LINE:-1}
if [ "$expect" = 1 ]; then echo "$KEY"; line=4; fi
sed -n "${line}p" "$in"; rm -f "$in"
EOF2
: >"$WORK/a.log"
out="$(FAKE_PLAIN_LINE=2 PATH="$WORK/bin:$PATH" "$API" --env smoke-a gql --pick -e user 2>&1)"
contains "gql --pick generates and runs a query" "$(<"$WORK/a.log")" "POST /graphql/v2/user"
contains "…and prints the one-liner" "$out" "→ api gql -e v2/user 'query Me"
: >"$WORK/a.log"
out="$(print n | FAKE_PLAIN_LINE=1 PATH="$WORK/bin:$PATH" "$API" --env smoke-a gql --pick -e user 2>&1)"
contains "a mutation asks first and 'n' stops it" "$out" "(not run)"
lacks "…and nothing mutating was sent" "$(grep -c 'mutation Login' "$WORK/a.log")" "1"

print "── phase 5: run capture, diff, doctor --json, Retry-After"
cat >"$WORK/flow.http" <<'EOF2'
### Login
# @name login
POST /auth
Content-Type: application/json

{"user":"me"}

### Me
# @name me
GET /me
Authorization: Bearer {{login.response.body.data.token}}
X-Req: {{login.response.headers.X-Req-Id}}
EOF2
: >"$WORK/a.log"
out="$(sh_ "api env use smoke-a >/dev/null; api run '$WORK/flow.http' me")"
contains "run captured the login response and used it" "$out" '"me"'
contains "…the dependency ran first" "$(<"$WORK/a.log")" "POST /auth"
contains "…and the captured token authenticated /me" "$(<"$WORK/a.log")" "GET /me auth=Bearer eyJ"

res="$(api diff --envs smoke-a,smoke-b /items .data --array-key id --json 2>/dev/null)"
expect "diff --array-key sees one real change" "$(print -r -- "$res" | jq -r '.changes | length')" "1"
expect "…at the right path" "$(print -r -- "$res" | jq -r '.changes[0].path')" "[id=2].n"
api diff --envs smoke-a,smoke-b /items .data >/dev/null 2>&1
expect "without --array-key a reordered list differs by position (exit 1)" "$?" "1"

expect "doctor --json is valid JSON with a verdict" "$(api doctor --json 2>/dev/null | jq -r 'has("ok")')" "true"
: >"$WORK/a.log"
sh_ "api env use smoke-a >/dev/null; api get /flaky --retry 2 >/dev/null"
expect "a 429 with Retry-After is retried" "$(grep -c '/flaky' "$WORK/a.log")" "2"

print "── phase 6: tooling targets exist"
(cd "$API_DIR" && make -n fuzz vuln cover release >/dev/null 2>&1) && ok "make fuzz / vuln / cover / release are defined" || fail "a make target is missing"

print "── cleanup"
if ((KEYCHAIN)); then
  for n in api-smoke-pw api-smoke-loose token.smoke-a token.smoke-b; do "$API" secret rm "$n" >/dev/null 2>&1; done
  gone=1
  for n in api-smoke-pw api-smoke-loose token.smoke-a token.smoke-b; do
    security find-generic-password -s "api:$n" >/dev/null 2>&1 && gone=0
  done
  ((gone)) && ok "no smoke item is left in the Keychain" || fail "a smoke item is still in the Keychain"
fi

print
((SKIPS)) && print "($SKIPS check group skipped)"
if ((FAILS)); then print "✗ $FAILS of $CHECKS smoke checks failed"; exit 1; fi
print "✓ all $CHECKS smoke checks passed"
