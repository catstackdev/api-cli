# api shell integration (zsh). Load it with:  eval "$(command api shell-init)"
#
# The binary does the work. This wrapper exists for two things a child process
# cannot do:
#   1. Change THIS shell. `api login`, `api logout` and `api env load|use|clear`
#      run the binary with --emit and eval what it prints (values are
#      single-quote-escaped in Go).
#   2. See unexported variables. API_BASE_URL / API_TOKEN / ... stay plain shell
#      variables on purpose; they are forwarded per call as prefix assignments.

# Sets REPLY to the real binary (not this function); no fork.
function _api::bin() {
  REPLY="${commands[api]:-$HOME/.local/bin/api}"
  [[ -x "$REPLY" ]]
}

# Run the binary with the session variables forwarded (never exported).
function _api::run() {
  _api::bin || {
    echo "✗ api binary not found: put it on PATH (see: make install)" >&2
    return 127
  }
  # Per-env tokens (config: token_env = "API_TOKEN_<NAME>") are forwarded too.
  local -a fwd=(API_BASE_URL="${API_BASE_URL-}" API_GRAPHQL_URL="${API_GRAPHQL_URL-}"
    API_TOKEN="${API_TOKEN-}" API_ENV="${API_ENV-}" API_SHIM=@CONTRACT@) # marker for `api doctor`
  local v
  for v in ${(k)parameters[(I)API_TOKEN_*]}; do
    fwd+=("$v=${(P)v}")
  done
  command env "${fwd[@]}" "$REPLY" "$@"
}

function api() {
  # The subcommand is the first argument AFTER the global flags, so
  # `api --env x login ...` and `api -M env use dev` count too.
  local -a rest=("$@")
  while ((${#rest})); do
    case "${rest[1]}" in
    --env | --base | --token | --color) rest=("${(@)rest[3,-1]}") ;;
    --env=* | --base=* | --token=* | --color=* | -C | -M | --no-pager | --debug | --color-output | --monochrome-output)
      rest=("${(@)rest[2,-1]}")
      ;;
    *) break ;;
    esac
  done

  # Help output must never be eval'd.
  local mutating=0 arg
  case "${rest[1]}" in
  login | logout) mutating=1 ;;
  env) [[ "${rest[2]}" == (load|clear|use) ]] && mutating=1 ;;
  esac
  for arg in "$@"; do
    [[ "$arg" == (-h|--help|--emit) ]] && mutating=0
  done

  if ((! mutating)); then
    _api::run "$@"
    return
  fi

  local out
  out="$(_api::run "$@" --emit)" || return $?
  eval "$out"
  [[ "${rest[1]}" == env && "${rest[2]}" == (load|use) ]] && _api::run env status
  return 0
}

# Completion: cached, regenerated only when the binary is newer than the cache.
function _api::load_completion() {
  _api::bin || return 0
  local bin="$REPLY" cache="${XDG_CACHE_HOME:-$HOME/.cache}/api-completion.zsh"
  if [[ ! -s "$cache" || "$bin" -nt "$cache" ]]; then
    mkdir -p "${cache:h}"
    "$bin" completion zsh >"$cache" 2>/dev/null || rm -f "$cache"
  fi
  [[ -s "$cache" ]] && source "$cache"
}

if [[ -o interactive ]]; then
  if (($+functions[compdef])); then
    _api::load_completion
  else
    # compinit has not run yet: register once on the first prompt.
    function _api::completion_once() {
      add-zsh-hook -d precmd _api::completion_once
      unfunction _api::completion_once
      (($+functions[compdef])) && _api::load_completion
    }
    autoload -Uz add-zsh-hook
    add-zsh-hook precmd _api::completion_once
  fi
fi
