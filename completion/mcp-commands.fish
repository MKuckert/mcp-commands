# fish completion for mcp-commands.
#
# Install in a directory fish scans for completions, e.g.:
#   $(brew --prefix)/share/fish/vendor_completions.d/mcp-commands.fish
#
# Single source of truth: keep the flag set in sync with flags.go.
# The CI drift check extracts exactly this line.
set -l _mcp_commands_flags --dir --scripts --watch --insecure-no-auth --allow-all-origins --disable-localhost-protection --version --no-timeout --list-tools --api-key-file --tls-cert --tls-key --log-level --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params --help

# Flag candidates are offered while the current token looks like a flag.
function __mcp_commands_token_is_flag
  string match -q -- '-*' "$commandline_tokens[-1]"
end

# Value completion keys off the immediately preceding token only. The
# standard __fish_seen_subcommand_from is subcommand-oriented: this CLI has
# no subcommands, so it would stay true for every later argument.
function __mcp_commands_prev_is_flag
  test (count $commandline_tokens) -ge 2
  and string match -q -- "$argv" "$commandline_tokens[(count $commandline_tokens)-1]"
end

# Candidate values must arrive via -a (command output): fish would parse a
# dash-prefixed positional argument as an option to `complete` itself.
function __mcp_commands_flag_candidates
  printf '%s\n' $argv
end

complete -c mcp-commands -n '__mcp_commands_token_is_flag' -f -a "(__mcp_commands_flag_candidates $_mcp_commands_flags)" -d 'mcp-commands flag'

# Value-taking flags keep fish's default file+directory completion (the -d
# entries document the value kind); --log-level completes its fixed set.
complete -c mcp-commands -n '__mcp_commands_prev_is_flag --dir --scripts' -d 'Directory'
complete -c mcp-commands -n '__mcp_commands_prev_is_flag --api-key-file --tls-cert --tls-key' -d 'File'
complete -c mcp-commands -n '__mcp_commands_prev_is_flag --log-level' -f -x -a 'debug info warn error' -d 'Log level'

# Free-form values: suppress file completion.
complete -c mcp-commands -n '__mcp_commands_prev_is_flag --host --port --api-key --max-concurrent --allowed-origins --timeout --call-tool --params' -f -x
