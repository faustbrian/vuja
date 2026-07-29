# Vuja

TTY-native predictive shell completion combining structured command specs with
personalized history ranking.

Vuja wraps Zsh, Bash, or Fish in a pseudoterminal and renders suggestions
directly in the active terminal session. It combines command specifications,
shell aliases, filesystem entries, executable discovery, and learned command
history in one ranked suggestion list.

Vuja supports Linux and macOS. Windows is not supported.

## Contents

- [Requirements](#requirements)
- [Installation](#installation)
- [Usage](#usage)
- [Shortcuts](#shortcuts)
- [Configuration](#configuration)
- [Updates](#updates)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Architecture](#architecture)
- [License](#license)

## Requirements

- Linux or macOS
- Zsh, Bash, or Fish
- A terminal with ANSI color support
- Go 1.26 or newer when building from source
- [`just`](https://github.com/casey/just) when using repository recipes

## Installation

### Release installer

```bash
curl -sSL https://raw.githubusercontent.com/faustbrian/vuja/main/scripts/install.sh | sh
```

The installer downloads the latest release, installs `vuja`, configures the
detected shell, and creates the default configuration when needed. Set
`BIN_DIR` to override the default `/usr/local/bin` destination. If that
directory is not writable, the installer falls back to `~/.local/bin`.

### Go install

```bash
go install github.com/faustbrian/vuja/cmd/vuja@latest
vuja setup
```

Ensure the Go binary directory is on `PATH` before running `vuja setup`.

### Build from source

```bash
git clone https://github.com/faustbrian/vuja.git
cd vuja
just install
```

`just install` builds the current checkout, copies the executable to
`~/.local/bin/vuja`, configures shell integration, initializes the default
configuration, and reloads an active Vuja session.

Shell detection uses `$SHELL`. Override it when needed:

```bash
just install zsh
just install bash
just install fish
```

### Manual shell integration

`vuja setup [shell]` installs the current executable and adds the appropriate
integration line to the shell configuration. To manage the integration
manually, add one of the following:

**Zsh (`~/.zshrc`):**

```zsh
eval "$(vuja init zsh)"
```

**Bash (`~/.bashrc`):**

```bash
eval "$(vuja init bash)"
```

**Fish (`~/.config/fish/config.fish`):**

```fish
vuja init fish | source
```

Restart the terminal or source the changed shell configuration after setup.

## Usage

When shell integration is installed, Vuja starts with the shell. It can also be
launched directly:

```bash
vuja
```

Override shell detection for one launch:

```bash
vuja --shell zsh
```

Available management commands:

```text
vuja config init
vuja config show
vuja setup [bash|zsh|fish]
vuja update
vuja version
vuja uninstall
```

## Shortcuts

| Shortcut | Action |
| --- | --- |
| <kbd>Shift</kbd> + <kbd>Tab</kbd> | Show or hide the suggestion menu |
| <kbd>Esc</kbd> | Hide the menu until the next key press |
| <kbd>Tab</kbd> | Insert the selected suggestion |
| <kbd>Enter</kbd> | Execute the current command |
| <kbd>↑</kbd> | Move up, or open history when the prompt is empty |
| <kbd>↓</kbd> | Move down, or open history when the prompt is empty |
| <kbd>→</kbd> | Accept ghost text while the menu is open |
| <kbd>←</kbd> / <kbd>→</kbd> | Move within the input buffer |
| <kbd>Ctrl</kbd> + <kbd>R</kbd> | Toggle specification and history emphasis |
| <kbd>Ctrl</kbd> + <kbd>A</kbd> | Move to the beginning of the line |
| <kbd>Ctrl</kbd> + <kbd>E</kbd> | Move to the end of the line |
| <kbd>Ctrl</kbd> + <kbd>L</kbd> | Clear the terminal and redraw the input |
| <kbd>Ctrl</kbd> + <kbd>U</kbd> | Clear the current command |
| <kbd>Ctrl</kbd> + <kbd>C</kbd> | Cancel the current command |
| <kbd>Ctrl</kbd> + <kbd>W</kbd> | Delete the previous word |

Vuja handles line-editing shortcuts directly while the terminal is in raw mode
so its input buffer and suggestion menu remain synchronized.

## Configuration

Vuja reads `~/.config/vuja/config.toml`. Create the file with comments or print
the resolved configuration:

```bash
vuja config init
vuja config show
```

### Default configuration

```toml
[core]
version = 1
shell = ""        # "zsh", "bash", "fish", or empty for auto-detection
mode = "last"     # "last", "spec", or "history"
debug = false

[ui]
style = "modern"  # "modern" or "classic"
nerd-fonts = true
ghost-text = true
max-suggestions = 100
max-height = 15

[ui.colors.day]
background = "#f8f5f1"
border = "#1d67f6"
accent = "#3c9339"
muted = "#747579"
text = "#242529"
text-selected = "#242529"
match = "#084ccf"
description = "#747579"
description-selected = "#242529"
selection-background = "#dbe2f2"
scroll-info = "#984ea5"
ghost-text = "#747579"

[ui.colors.night]
background = "#080a0d"
border = "#739ee8"
accent = "#61ffcf"
muted = "#404658"
text = "#c6cad7"
text-selected = "#ffffff"
match = "#61eeff"
description = "#739ee8"
description-selected = "#c6cad7"
selection-background = "#1a1e24"
scroll-info = "#fd7df4"
ghost-text = "#404658"

[git]
filter-active-branch = true
deduplicate-branches = true

[updater]
check-on-startup = true
channel = "stable" # "stable" or "nightly"
check-interval = "24h"
```

Vuja selects the day or night palette from the terminal background. Every
configured color must use `#RRGGBB` format. The defaults are derived from
Serein Day and Serein Night.

`max-suggestions` accepts values from 1 to 500. `max-height` accepts values
from 3 to 50.

### Optional AI suggestions

AI suggestions are disabled by default. Vuja can use an
OpenAI-compatible chat completions endpoint when a provider is configured:

```toml
[ai]
enabled = true
provider = "groq"
debounce_ms = 500
min_interval_ms = 1000

[ai.providers.groq]
endpoint = "https://api.groq.com/openai/v1/chat/completions"
api_key_env = "GROQ_API_KEY"
model = "llama-3.3-70b-versatile"
timeout_ms = 3000
```

Use `api_key_env` instead of storing credentials directly in the configuration
file.

## Updates

When enabled, Vuja checks GitHub releases asynchronously at the configured
interval. A new-version notice appears after a command completes and is shown
once per version.

```bash
vuja version
vuja update
```

Updater state is stored under Vuja's user state directory. Development builds
use the version `dev` and do not display release notifications.

## Troubleshooting

### Debug logging

Run Vuja with debug logging:

```bash
vuja --debug
```

Or enable it in the configuration:

```toml
[core]
debug = true
```

Debug logs include shell input. Enable debug mode only while investigating a
problem and inspect the log before attaching it to an issue.

Bug reports should include:

- the Vuja version;
- the operating system, terminal, and shell;
- steps to reproduce the problem;
- expected and actual behavior; and
- sanitized debug output when relevant.

### Shell setup

Check that `vuja` is on `PATH` and that the shell configuration contains the
matching integration command from [Manual shell integration](#manual-shell-integration).
Rerun setup with an explicit shell when auto-detection is incorrect:

```bash
vuja setup zsh
```

## Development

Clone the repository and download dependencies:

```bash
git clone https://github.com/faustbrian/vuja.git
cd vuja
go mod download
```

List all repository recipes:

```bash
just --list
```

Common workflows:

```bash
just build
just run
just reload
just test
just lint
just analyze
```

Build a versioned release binary:

```bash
just build-release v1.2.0
```

`just reload` rebuilds the executable, updates an existing
`~/.local/bin/vuja`, and signals an active session. If there is no active Vuja
session, it starts one.

The updater has isolated development recipes:

```bash
just build-release v0.0.1
just debug-update v1.99.0
just debug-notify v1.99.0
```

`debug-update` exercises version fetching and comparison without a full Vuja
session. `debug-notify` opens a live session in which the notification appears
after the first completed command.

## Architecture

### Runtime and PTY bridge

The `root` package selects the shell, starts it in a pseudoterminal, and runs
separate input and output pumps. Input is intercepted in raw mode so Vuja can
track the prompt buffer and respond to navigation keys. Output is forwarded
through a synchronized writer to prevent shell output and overlay rendering
from interleaving.

Shell integrations can report command boundaries over the Vuja IPC file
descriptor. The Zsh integration installs `preexec` and `precmd` hooks for this
purpose.

An active process listens for `SIGUSR1` and replaces itself with the newly
installed executable while preserving the underlying PTY shell.

### Suggestion sources and ranking

Vuja gathers command specifications, aliases, executable names, filesystem
entries, shell history, and optional AI output. Structured and historical
candidates are ranked in one pool. The active `spec` or `history` mode changes
source emphasis without hiding the other source.

The score combines:

- source priority;
- prefix and fuzzy match quality;
- command frequency and bucketed recency;
- current-directory affinity;
- the previous command;
- exact command sequences; and
- normalized command-skeleton sequences.

Persistent shell history is imported as a replaceable snapshot so restarts do
not inflate learned frequency counts. Successful commands recorded by Vuja
remain separate from that snapshot.

### Command specifications

The `spec` package models top-level commands, recursive subcommands, options,
and dynamic generators. Lookup tokenizes the current prompt, walks the known
command tree, identifies the partial token, and returns matching candidates.
Flags receive lower priority while typing positional arguments and are promoted
when the partial token starts with `-`.

Aliases discovered in shell configuration are expanded before lookup so an
alias can still receive accurate subcommand and option suggestions.

### Filesystem suggestions

`spec.FileGenerator` resolves the directory and partial filename from the
current token. It supports nested paths, extension filters, directory-only
completion, human-readable file descriptions, and trailing `/` on directory
suggestions.

### AI engine

The optional `internal/ai` engine gathers a bounded environment snapshot,
debounces requests, cancels stale requests, and calls the configured
OpenAI-compatible provider. Responses are converted to suggestions and passed
through the same rendering path as other candidates. Rate limiting introduces
a cooldown before further provider requests.

### Overlay

The `integration` package renders the suggestion menu directly on the terminal
grid with ANSI cursor movement and Lip Gloss styles. It saves and restores the
prompt cursor, reserves space near the bottom of the terminal to avoid
scrolling over the active input, and calculates displayed cell widths before
drawing.

The overlay supports modern and classic layouts, configurable day and night
palettes, Nerd Font icons, match highlighting, and ghost text.

### Updater

The updater checks the configured GitHub release channel in the background,
compares semantic versions, and persists the last check and last notified
version. `vuja update` downloads and installs a newer release through the
release installer.

Builds inject the release version with Go linker flags:

```bash
go build \
  -ldflags="-X github.com/faustbrian/vuja/root.Version=v1.2.0" \
  -o vuja ./cmd/vuja
```

## License

The project is distributed under the [0BSD license](LICENSE). Third-party
copyright and license notices are retained in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
