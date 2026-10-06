#!/bin/zsh
# End-to-end test of the zsh shim (src/api.sh) against real local servers.
#
# Needs python3 and permission to bind localhost ports (run it outside a
# sandbox that blocks them). Usage:  make e2e
#
# It checks what `go test` cannot: that eval'd output really changes THIS shell,
# that session variables stay unexported, and that per-env tokens are forwarded.

setopt local_options no_unset
HERE="${0:A:h}"
API_DIR="${HERE:h}"
PROFILE_SRC="${API_DIR:h:h}/src"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/api-e2e.XXXXXX")"
typeset -i FAILS=0 CHECKS=0
typeset -a PIDS=()

cleanup() {
  local p
  for p in $PIDS; do kill "$p" 2>/dev/null; done
  rm -rf "$WORK"
}
trap cleanup EXIT

ok() {
  ((CHECKS++))
  print -r -- "  ✓ $1"
}
fail() {
  ((CHECKS++))
  ((FAILS++))
  print -r -- "  ✗ $1"
}
expect() { # expect <description> <actual> <wanted>
  if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1 (got [$2], wanted [$3])"; fi
}

# ── stub servers: GET echoes the Authorization header, POST returns a token ──
cat >"$WORK/srv.py" <<'EOF'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
name, logfile = sys.argv[1], sys.argv[2]
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _reply(self, obj):
        b = json.dumps(obj).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def _log(self):
        with open(logfile, "a") as f: f.write(f'{self.command} {self.path} auth={self.headers.get("Authorization", "")}\n')
    def do_GET(self):  self._log(); self._reply({"srv": name, "auth": self.headers.get("Authorization", "")})
    def do_POST(self):
        self._log(); self.rfile.read(int(self.headers.get("Content-Length", 0) or 0))
        self._reply({"data": {"token": f"TOKEN-{name}"}})
srv = HTTPServer(("127.0.0.1", 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
EOF
start_server() { # start_server <name> -> sets REPLY to the port
  python3 "$WORK/srv.py" "$1" "$WORK/$1.log" >"$WORK/$1.port" &
  PIDS+=($!)
  local i
  for i in {1..50}; do
    [[ -s "$WORK/$1.port" ]] && break
    sleep 0.1
  done
  REPLY="$(<"$WORK/$1.port")"
}
: >"$WORK/dev.log"
: >"$WORK/thien.log"
start_server dev
DEV_PORT=$REPLY
start_server thien
THIEN_PORT=$REPLY
[[ -n "$DEV_PORT" && -n "$THIEN_PORT" ]] || {
  print "✗ could not start the stub servers"
  exit 2
}

cat >"$WORK/config.toml" <<EOF
[env.dev]
url = "http://127.0.0.1:$DEV_PORT"
token_env = "API_TOKEN_DEV"
[env.thien]
url = "http://127.0.0.1:$THIEN_PORT"
token_env = "API_TOKEN_THIEN"
EOF
export API_CONFIG="$WORK/config.toml" XDG_STATE_HOME="$WORK/state"
cd "$WORK"

# Always test the binary that was just built, never an older one the shim would
# otherwise find first (PATH, then ~/.local/bin).
export PATH="$API_DIR/bin:$PATH"
print -r -- "binary under test: $(command -v api) ($(api --version 2>&1))"

source "$PROFILE_SRC/api.sh"
unset API_BASE_URL API_GRAPHQL_URL API_TOKEN API_ENV API_TOKEN_DEV API_TOKEN_THIEN

reset() {
  unset API_BASE_URL API_GRAPHQL_URL API_TOKEN API_ENV API_TOKEN_DEV API_TOKEN_THIEN
  typeset +x API_BASE_URL 2>/dev/null
}

print "── env use"
api env use dev >/dev/null 2>&1
expect "env use sets API_ENV" "${API_ENV:-}" "dev"
expect "env use sets API_BASE_URL" "${API_BASE_URL:-}" "http://127.0.0.1:$DEV_PORT"
expect "env use leaves API_ENV unexported" "${(t)API_ENV}" "scalar"

print "── a global flag before the subcommand must still change the shell"
LOGIN='{"u":1}'
reset
api env use dev >/dev/null 2>&1
api --debug login /auth $LOGIN >/dev/null 2>&1
expect "api --debug login" "${API_TOKEN:-}" "TOKEN-dev"

reset
api env use dev >/dev/null 2>&1
api -M login /auth $LOGIN >/dev/null 2>&1
expect "api -M login" "${API_TOKEN:-}" "TOKEN-dev"

reset
api env use dev >/dev/null 2>&1
api --color never login /auth $LOGIN >/dev/null 2>&1
expect "api --color never login" "${API_TOKEN:-}" "TOKEN-dev"

reset
api env use dev >/dev/null 2>&1
api --env=thien login /auth $LOGIN >/dev/null 2>&1
expect "api --env=thien login: token" "${API_TOKEN:-}" "TOKEN-thien"
expect "api --env=thien login: switches the session env" "${API_ENV:-}" "thien"
expect "api --env=thien login: switches the base URL" "${API_BASE_URL:-}" "http://127.0.0.1:$THIEN_PORT"
expect "api --env=thien login: sets the per-env token" "${API_TOKEN_THIEN:-}" "TOKEN-thien"
expect "per-env token stays unexported" "${(t)API_TOKEN_THIEN}" "scalar"

reset
api --env dev login /auth $LOGIN >/dev/null 2>&1
expect "api --env dev login (separate value)" "${API_ENV:-}" "dev"

reset
api -M env use thien >/dev/null 2>&1
expect "api -M env use thien" "${API_ENV:-}" "thien"
api --no-pager env use dev >/dev/null 2>&1
expect "api --no-pager env use dev" "${API_ENV:-}" "dev"
api --env thien env use dev >/dev/null 2>&1
expect "flag with a value, then env use" "${API_ENV:-}" "dev"

print "── help and plain commands are never eval'd"
reset
api env use dev >/dev/null 2>&1
before="$API_ENV"
api --env thien login --help >/dev/null 2>&1
expect "login --help does not touch the shell" "${API_ENV:-}" "$before"
out="$(api -M get /ping 2>&1)"
[[ "$out" == *'"srv"'* ]] && ok "api -M get prints the response" || fail "api -M get: $out"

print "── per-env tokens: diff authenticates BOTH sides with their own token"
reset
api env use dev >/dev/null 2>&1
api login /auth $LOGIN >/dev/null 2>&1
api env use thien >/dev/null 2>&1
api login /auth $LOGIN >/dev/null 2>&1
expect "after two logins the dev token is kept" "${API_TOKEN_DEV:-}" "TOKEN-dev"
expect "after two logins the thien token is kept" "${API_TOKEN_THIEN:-}" "TOKEN-thien"
: >"$WORK/dev.log"
: >"$WORK/thien.log"
api diff --envs dev,thien /ping >/dev/null 2>&1
expect "diff sent dev's token to dev" "$(<"$WORK/dev.log")" "GET /ping auth=Bearer TOKEN-dev"
expect "diff sent thien's token to thien" "$(<"$WORK/thien.log")" "GET /ping auth=Bearer TOKEN-thien"

print "── a session token is never sent to another env"
reset
api env use dev >/dev/null 2>&1
api login /auth $LOGIN >/dev/null 2>&1 # session: dev + TOKEN-dev
: >"$WORK/thien.log"
api --env thien get /ping >/dev/null 2>&1
expect "--env thien (no thien token) sends no bearer" "$(<"$WORK/thien.log")" "GET /ping auth="

print "── env load maps a project's own URL variable (VITE_GRAPHQL_URL ...)"
reset
print -r -- "VITE_GRAPHQL_URL=http://127.0.0.1:$DEV_PORT/graphql" > "$WORK/.env.map"
api env load map >/dev/null 2>&1
expect "API_GRAPHQL_URL comes from VITE_GRAPHQL_URL" "${API_GRAPHQL_URL:-}" "http://127.0.0.1:$DEV_PORT/graphql"
expect "API_BASE_URL is derived (without /graphql)" "${API_BASE_URL:-}" "http://127.0.0.1:$DEV_PORT"
expect "API_ENV is the suffix" "${API_ENV:-}" "map"
out="$(api get /ping 2>&1)"
[[ "$out" == *'"srv"'* ]] && ok "a request works right after env load" || fail "request after env load: $out"
rm -f "$WORK/.env.map"

print "── logout changes THIS shell"
reset
api env use dev >/dev/null 2>&1
api login /auth $LOGIN >/dev/null 2>&1
expect "logged in" "${API_TOKEN:-}" "TOKEN-dev"
api --env thien logout >/dev/null 2>&1
expect "logging out of another env keeps this shell's token" "${API_TOKEN:-}" "TOKEN-dev"
api logout >/dev/null 2>&1
expect "api logout unsets API_TOKEN" "${API_TOKEN:-<unset>}" "<unset>"
expect "api logout unsets the env's token_env variable" "${API_TOKEN_DEV:-<unset>}" "<unset>"
expect "api logout keeps the env selected" "${API_ENV:-}" "dev"

print "── login with no session (server from default_env) must leave the shell logged in"
reset
cat >"$WORK/config.default.toml" <<EOF2
default_env = "dev"
[env.dev]
url = "http://127.0.0.1:$DEV_PORT"
EOF2
OLD_CFG="$API_CONFIG"; export API_CONFIG="$WORK/config.default.toml"
api login /auth $LOGIN >/dev/null 2>&1
expect "token set" "${API_TOKEN:-}" "TOKEN-dev"
expect "env adopted" "${API_ENV:-}" "dev"
st_out="$(api env status 2>&1)"
[[ "$st_out" == *"TOKEN-dev"* ]] && ok "env status shows the token" || fail "env status after login: $st_out"
export API_CONFIG="$OLD_CFG"

print
if ((FAILS)); then
  print "✗ $FAILS of $CHECKS checks failed"
  exit 1
fi
print "✓ all $CHECKS shim checks passed"
