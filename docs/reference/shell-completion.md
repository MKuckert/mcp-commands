# Shell Completion

The Homebrew formula installs completion scripts for bash, zsh, and fish into
the brew prefix ([Homebrew shell completion docs](https://docs.brew.sh/Shell-Completion)
for further documentation). Activation per shell:

## bash

Install `bash-completion` (works with the macOS stock bash 3.2) or
`bash-completion@2` (plus `brew install bash` for bash 4+), then in
`~/.bashrc`:

```bash
[[ -r "$(brew --prefix)/etc/profile.d/bash_completion.sh" ]] && . "$(brew --prefix)/etc/profile.d/bash_completion.sh"
```

(Both v1 and `@2` source `$(brew --prefix)/etc/bash_completion.d`, where the
formula places `mcp-commands`.)

## zsh

In `~/.zshrc` (the `fpath` line must come **before** your `compinit` call):

```bash
fpath=($(brew --prefix)/share/zsh/site-functions $fpath)
autoload -Uz compinit && compinit
```

Add the `compinit` line only if your `~/.zshrc` does not already run it —
without `compinit`, stock zsh never loads completions at all. (Homebrew's own
`zsh` picks up the fpath entry automatically.)

## fish

Automatic once the brew prefix is on fish's path; the formula places
`mcp-commands.fish` in `$(brew --prefix)/share/fish/vendor_completions.d`.

## Linuxbrew note

On Linuxbrew, the distro's bash-completion reads
`/usr/share/bash-completion/completions`, not the brew prefix — brew's
`bash-completion@2` is required for the brew file to load.

## Non-brew users

Every release archive ships the three scripts (`mcp-commands.bash`,
`mcp-commands.zsh`, `mcp-commands.fish`) — drop them in your shell's
completion directory (`etc/bash_completion.d/`, your `fpath` as
`_mcp-commands`, or `share/fish/vendor_completions.d/`). `go install`
binaries carry no completion files.
