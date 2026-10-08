# Bash completion for mcp-commands (bash 3.2+, bash-completion v1 or v2).
#
# Install in a directory your shell sources, e.g.:
#   $(brew --prefix)/etc/bash_completion.d/mcp-commands
#   /usr/share/bash-completion/completions/mcp-commands
#
# Single source of truth: keep the flag set in sync with flags.go.
# The CI drift check extracts exactly this line.
_mcp_commands_flags="--dir --scripts --watch --insecure-no-auth --allow-all-origins --disable-localhost-protection --version --no-timeout --list-tools --api-key-file --tls-cert --tls-key --log-level --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params --help"

# --log-level accepts only these values (logLevels in flags.go).
_mcp_commands_log_levels="debug info warn error"

# Populate COMPREPLY from compgen output, one candidate per array element.
_mcp-commands-fill() {
  COMPREPLY=()
  local cand
  while IFS= read -r cand; do
    COMPREPLY+=("$cand")
  done < <(compgen "$@")
}

_mcp-commands() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"

  case "$prev" in
  --log-level)
    _mcp-commands-fill -W "$_mcp_commands_log_levels" -- "$cur"
    return 0
    ;;
  --dir | --scripts)
    _mcp-commands-fill -d -o filenames -- "$cur"
    return 0
    ;;
  --api-key-file | --tls-cert | --tls-key)
    _mcp-commands-fill -f -o filenames -- "$cur"
    return 0
    ;;
  esac

  if [[ "$cur" == -* ]]; then
    _mcp-commands-fill -W "$_mcp_commands_flags" -- "$cur"
  fi
  return 0
}

complete -F _mcp-commands -o filenames mcp-commands
