#compdef mcp-commands

# zsh completion for mcp-commands (compinit; no other completion framework
# is required).
#
# Install as _mcp-commands in a directory on your fpath, e.g.:
#   $(brew --prefix)/share/zsh/site-functions/_mcp-commands
#
# Single source of truth: keep the flag set in sync with flags.go.
# The CI drift check extracts exactly this line.
flags=( --dir --scripts --watch --insecure-no-auth --allow-all-origins --disable-localhost-protection --version --no-timeout --list-tools --api-key-file --tls-cert --tls-key --log-level --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params )

# --log-level accepts only these values (logLevels in flags.go).
log_levels=( debug info warn error )

# The body only runs under the completion system (compinit), where the
# _files helper is available; the guarded registration below keeps the file
# sourceable in a plain zsh, where compdef does not exist.
_mcp-commands() {
  local prev
  prev="${words[CURRENT-1]}"

  case "$prev" in
  --log-level)
    compadd -- "${log_levels[@]}"
    ;;
  --dir | --scripts)
    _files -/
    ;;
  --api-key-file | --tls-cert | --tls-key)
    _files
    ;;
  *)
    if [[ "${words[CURRENT]}" == -* ]]; then
      compadd -- "${flags[@]}"
    fi
    ;;
  esac
  return 0
}

if (( $+functions[compdef] || $+builtins[compdef] )); then
  compdef _mcp-commands mcp-commands
fi
