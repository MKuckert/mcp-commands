# fish completion for mcp-commands.
#
# Install in a directory fish scans for completions, e.g.:
#   $(brew --prefix)/share/fish/vendor_completions.d/mcp-commands.fish
#
# Single source of truth: keep the flag set in sync with flags.go.
# The CI drift check extracts exactly this line.
set -l flags --dir --scripts --watch --insecure-no-auth --allow-all-origins --disable-localhost-protection --version --no-timeout --list-tools --api-key-file --tls-cert --tls-key --log-level --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params

# Values must arrive via -a (command output): fish would parse a
# dash-prefixed positional argument as an option to `complete` itself.
function __mcp_commands_flag_candidates
  printf '%s\n' $argv
end

complete -c mcp-commands -n '__fish_use_subcommand' -f -a "(__mcp_commands_flag_candidates $flags)" -d 'mcp-commands flag'

# Value-taking flags keep fish's default file+directory completion (the -d
# entries document the value kind); --log-level completes its fixed set.
complete -c mcp-commands -n '__fish_seen_subcommand_from --dir --scripts' -d 'Directory'
complete -c mcp-commands -n '__fish_seen_subcommand_from --api-key-file --tls-cert --tls-key' -d 'File'
complete -c mcp-commands -n '__fish_seen_subcommand_from --log-level' -f -x -a 'debug info warn error' -d 'Log level'

# Free-form values: suppress file completion.
complete -c mcp-commands -n '__fish_seen_subcommand_from --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params' -f -x
