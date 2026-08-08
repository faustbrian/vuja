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
- [Privacy](#privacy)
- [Updates](#updates)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Architecture](#architecture)
- [Command reference](#command-reference)
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

The installer downloads the latest release, verifies its published SHA-256
checksum, installs `vuja`, configures the detected shell, and creates the
default configuration when needed. Set `BIN_DIR` to override the default
`/usr/local/bin` destination. If that directory is not writable, the installer
falls back to `~/.local/bin`.

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
configuration, and exits. Open a new terminal session to use the installed
build; installation intentionally does not reload a running Vuja session.

Shell detection uses `$SHELL`. Override it when needed:

```bash
just install zsh
just install bash
just install fish
```

### Manual shell integration

`vuja setup [shell]` installs the current executable and writes a static shell
hook under `~/.local/share/vuja`. It sources that hook first for the bootstrap
and again after the rest of the shell configuration to finalize prompt hooks.
The first source avoids starting a separate `vuja init` process whenever a
terminal opens. The final source orders Vuja after prompt frameworks that
install or replace prompt hooks while the inner shell starts.

**Zsh (`~/.zshrc`):**

```zsh
export PATH="$HOME/.local/bin:$PATH"
source "$HOME/.local/share/vuja/init.zsh"
# other shell and prompt configuration
source "$HOME/.local/share/vuja/init.zsh"
```

**Bash (`~/.bashrc`):**

```bash
export PATH="$HOME/.local/bin:$PATH"
source "$HOME/.local/share/vuja/init.bash"
# other shell and prompt configuration
source "$HOME/.local/share/vuja/init.bash"
```

**Fish (`~/.config/fish/config.fish`):**

```fish
set -gx PATH "$HOME/.local/bin" $PATH
source "$HOME/.local/share/vuja/init.fish"
# other shell and prompt configuration
source "$HOME/.local/share/vuja/init.fish"
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
vuja config validate [path]
vuja config doctor
vuja config diff --defaults [path]
vuja config migrate [--write] [path]
vuja config preset <minimal|balanced|context-rich|ops> [--write] [--force]
vuja config preview --preset balanced --width 120 --night
vuja debug suggest "<query>" [--cwd <directory>] [--mode spec|history] [--json]
vuja debug latency [--session <id>] [--json]
vuja setup [bash|zsh|fish]
vuja update
vuja version
vuja uninstall
```

## Shortcuts

| Shortcut | Action |
| --- | --- |
| <kbd>Shift</kbd> + <kbd>Tab</kbd> | Show or hide the suggestion menu |
| <kbd>Esc</kbd> | Undo the most recent unedited suggestion acceptance; otherwise hide the menu until the next key press |
| <kbd>Tab</kbd> | Focus and cycle suggestions; accept immediately when only one exists |
| <kbd>Tab</kbd>, <kbd>Tab</kbd> | Quickly accept the first suggestion |
| <kbd>Enter</kbd> | Execute exactly what is typed, or the visibly selected suggestion after navigating the menu |
| <kbd>↑</kbd> | Move up; on an empty prompt, open the most recent executions and mirror the newest command into the prompt |
| <kbd>↓</kbd> | Move down |
| <kbd>→</kbd> | Accept ghost text while the menu is open |
| <kbd>Option/Alt</kbd> + <kbd>→</kbd> | Accept the next token or path segment |
| <kbd>Ctrl</kbd> + <kbd>→</kbd> | Accept the next token or path segment |
| <kbd>←</kbd> / <kbd>→</kbd> | Move within the input buffer |
| <kbd>Ctrl</kbd> + <kbd>R</kbd> | Open rich history search; press again to cycle directory, project, global, machine, and current-session scopes |
| <kbd>Ctrl</kbd> + <kbd>S</kbd> | Toggle successful commands while history search is open |
| <kbd>Ctrl</kbd> + <kbd>A</kbd> | Move to the beginning of the line |
| <kbd>Ctrl</kbd> + <kbd>E</kbd> | Move to the end of the line |
| <kbd>Ctrl</kbd> + <kbd>L</kbd> | Clear the terminal and redraw the input |
| <kbd>Ctrl</kbd> + <kbd>U</kbd> | Clear the current command |
| <kbd>Ctrl</kbd> + <kbd>C</kbd> | Cancel the current command |
| <kbd>Ctrl</kbd> + <kbd>W</kbd> | Delete the previous word |

Vuja handles line-editing shortcuts directly while the terminal is in raw mode
so its input buffer and suggestion menu remain synchronized. The table shows
the defaults. `keybindings.keymap = "emacs"` uses Ctrl+A/C/E/L/U/W; `"vi"`
uses Home and End for line boundaries while retaining safe control bindings for
clear, cancel, and word deletion. `move-beginning`, `move-end`, `clear-screen`,
`clear-line`, `cancel`, and `delete-word` override individual actions.

When Codex prints a `codex resume` instruction, Vuja renders the session ID as
an OSC 8 hyperlink. Activate the link with the terminal's normal link modifier
(Command-click on macOS) to run the exact `codex resume <session-id>` command in
the originating Vuja session. For safety, activation is accepted only while
that shell is idle and its prompt is empty. `vuja setup` registers the private
`vuja://` action handler on macOS and Linux; `vuja uninstall` removes it.

## Configuration

Vuja reads `~/.config/vuja/config.toml`. Create the canonical file or print the
resolved configuration:

```bash
vuja config init
vuja config show
```

### Generated defaults and presets

`vuja config init` writes the canonical `balanced` preset from the same typed
source used by preset rendering and migration. The README intentionally does
not carry a second TOML copy that can drift. Existing files are never replaced.

Available presets are finite and opinionated:

```bash
vuja config preset minimal
vuja config preset balanced
vuja config preset context-rich
vuja config preset ops
```

Preset commands print TOML by default. Use `--write` to install one and add
`--force` only when intentionally replacing an existing configuration.
Previewing never launches Vuja:

```bash
vuja config preview --preset balanced --width 120 --night
```

Configuration maintenance commands are also non-interactive and do not start a
managed shell:

```bash
vuja config validate [path]
vuja config doctor
vuja config diff --defaults [path]
vuja config migrate [path]
vuja config migrate --write [path]
```

Migration prints the resolved current schema unless `--write` is supplied. A
write creates a timestamped backup beside the original first. Schema-1 files
retain the former full-snapshot behavior unless migration writes the equivalent
settings explicitly.

Vuja selects the day or night palette from `COLORFGBG` when available and
otherwise uses the night palette. This avoids terminal status queries entering
the shell input stream. Every configured color must use `#RRGGBB` format. The
defaults are derived from Serein Day and Serein Night.
`palette` accepts `auto`, `serein-day`, `serein-night`, or `terminal`; terminal
uses the terminal's ANSI palette rather than fixed RGB values. `contrast =
"high"` raises muted-text contrast. `color-vision` accepts `default`,
`deuteranopia`, `protanopia`, or `monochrome` while retaining textual labels so
color is never the only status signal.

`max-suggestions` accepts values from 1 to 500. `max-height` accepts values
from 3 to 50.

### Compositor-backed chatbox

Set `ui.prompt-position` to `bottom` to place the active shell prompt and input
inside a persistent chatbox at the bottom of the terminal:

```toml
[ui]
prompt-position = "bottom"
```

The hyphenated key follows Vuja's existing TOML naming convention. Newly
initialized configurations use `bottom`; existing files are never migrated
implicitly. The no-file runtime fallback remains `classic` for compatibility.
Bottom mode supports the same zsh, bash, and fish integrations as Vuja. The
shell integration places session-scoped prompt and command markers directly in
the PTY stream; Vuja consumes those markers and never displays them.
Existing installations must rerun `vuja setup [shell]` after upgrading so the
static shell hook includes these markers and the final prompt-hook source.
For Zsh, bottom mode installs a session-scoped plain-text prompt and clears the
right prompt. Zsh remains the line editor, but prompt frameworks do not render
inside the managed session.

The shell remains the line editor and source of truth: its prompt, editable
buffer, cursor movement, completion behavior, and multiline continuation are
captured from the PTY rather than reimplemented by Vuja. The compositor renders
that live surface on a padded background with optional title and status rows.
Completed commands default to `scrollback = "output"`, which removes the active
chatbox and leaves only the command's unmodified output in terminal scrollback.
Set `scrollback = "snapshot"` to retain the complete title, command, frozen
status context, output, and final outcome described below.
`completed-command = "command"`, `"outcome"`, or `"snapshot"` selects the
retained detail. For full snapshots, `snapshot-metadata = "always"`,
`"changed"`, or `"never"` controls repeated directory, Git, version, and
environment metadata.
`history-spacing` preserves visual separation in both modes without changing
command output. Terminal applications own the number of retained scrollback
lines; Vuja therefore does not expose a misleading completed-item retention
limit.
Each row has independent left, center, and right regions. Empty all three
arrays for a row to hide it. The finite built-in segments are `directory`,
`package`, `versions`, `session`, `git-branch`, `git-status`, `git-added`,
`git-deleted`, `git-stash`, `git-lines`, `environment`, `version-mismatch`,
`contexts`, `stale`, `jobs`, `duration`, `exit`, `cpu`, and `memory`; a segment
may appear in only one region.
The legacy single `status` array remains supported and maps runtime segments to
the right. Home-directory paths use `~`. Completed commands retain the same
surface padding, and ordinary line-oriented output uses the same horizontal
inset. Full-screen and cursor-addressed applications continue to pass through
without padding. Multiline prompts and wrapped input grow upward inside the
surface.

Repository-aware paths default to `path-color-mode = "hierarchy"`, which uses a
deterministic color progression from `directory-root` to `directory` across
the visible path segments. Set it to `"single"` to use `directory` for the
whole path. `path-max-segments` keeps the repository anchor and deepest path
segments while collapsing a long middle to `…`; `0` disables this
segment-based shortening. Width-aware truncation still applies when the
terminal cannot fit the configured number of segments.

The status surface has a finite built-in registry. It includes repository-aware
paths and read-only state; package identity; Git branch, state, stash count, and
changed-line totals; activated environments; exact declared-versus-active
toolchain mismatches; command-aware Kubernetes, AWS, and Docker context; stale
provider state; background and stopped jobs; semantic command failures;
last-command duration; relevant repository tool versions; whole-system CPU
utilization; and used physical memory. It uses one row when possible and a
bounded number of rows selected by density. Narrow terminals remove metrics,
successful exit state, versions, and low-risk detail before directory, branch,
operations, conflicts, mismatches, stale state, risky session context, or
failures.
`density = "compact"`, `"balanced"`, or `"rich"` selects the amount of
context. `responsive = true` keeps width-aware removal enabled without exposing
internal segment priorities.

Repository and version context stays left aligned. Duration, exit status, CPU,
and RAM are independently right aligned. Git reports clean, staged, modified,
renamed, untracked, added, deleted, conflict, ahead,
behind, stash, changed-line, detached-HEAD, merge, rebase, cherry-pick, and
revert states. It reads
cheap branch and operation metadata directly, coalesces refreshes, and runs one
bounded porcelain-v2 probe off the render path after directory changes and
completed commands. Changed-line totals use a separate bounded probe only when
`git-lines` is configured. A failed refresh retains the last valid result and
shows `stale` instead of silently presenting cached data as current.

`session` stays hidden locally and appears for SSH, container, root, or sudo
sessions. Sudo authentication uses a fixed non-interactive `sudo -n true`
probe, bounded to `100ms` and cached for `30s`; it can never open a password
prompt. `jobs` reports shell-owned background and stopped jobs. `environment`
reports active virtualenv, Conda, mise, Nix, and direnv state; an unloaded
repository `.envrc` is called out. `contexts` is deliberately command-aware:
Kubernetes context and namespace appear only while typing Kubernetes commands,
AWS profile and region only for AWS commands, and Docker context only for
Docker commands. These providers read environment state and local config files;
they never contact clusters, clouds, daemons, or networks.
Each built-in context accepts `"auto"`, `"always"`, or `"never"` through
`docker-context`, `kubernetes-context`, and `aws-context`.

Version detection supports Laravel, PHP, Composer, Python, Ruby, Elixir,
Node.js, Bun, Go, Rust, Docker Compose, and Docker. Versions render in that
fixed order from the application layer down through runtimes, toolchains, and
container infrastructure. Python relevance comes from `pyproject.toml`,
`.python-version`, uv, Poetry, Pipenv, and requirements metadata. Ruby uses
`.ruby-version`, Bundler, and gemspec metadata. Elixir uses Mix and
`.tool-versions`. Repository manifests, lockfiles, toolchain files,
and bounded source discovery determine relevance and preferred declared
versions. Only relevant tools may use a short, cached background version probe;
Laravel is resolved from Composer metadata and never invokes `php artisan`.
Docker probes report client versions without contacting the daemon. CPU and RAM
use native OS counters without per-refresh subprocesses and update at
`refresh-interval`. Vuja intentionally has no custom status scripts, commands,
providers, templates, hooks, or plugin API.
`versions = "auto"`, `"always"`, or `"never"` controls version visibility;
`version-allow` and `version-deny` constrain the fixed provider registry.

Duration, CPU, and memory thresholds are configurable with `duration-fast`,
`duration-slow`, `metric-hysteresis`, `cpu-average`, `cpu-high`, `cpu-critical`, and corresponding
memory keys. `metrics = "always"`, `"when-high"`, or `"never"` controls both
sampling and visibility.

`version-mismatch` compares only exact project pins such as `.node-version`,
`.php-version`, `.python-version`, `.ruby-version`, `rust-toolchain`, `go.mod`
toolchains, and exact
`.tool-versions` entries. Version ranges are not misreported as active-runtime
mismatches. `package` reads local `package.json`, `composer.json`, or
`Cargo.toml` metadata. Exit codes 126, 127, 130, 137, and 143 include
human-readable failure meaning.

Suggestions and history render directly above the complete prompt surface. On
command submission Vuja restores the full scrolling region and returns output
to direct terminal flow. Alternate-screen applications remain direct
pass-throughs. The next active prompt appears below completed output. Terminal
resize, clear, reload, exit, and failure cleanup restore the scrolling region,
cursor visibility, and automatic wrapping.

In snapshot scrollback, the chatbox uses the active day or night palette.
`surface-background` fills
the editable input, `completed-surface-background` gives completed command
surfaces lower visual emphasis, and `status-background` fills metadata rows.
Each completed execution mirrors the active chatbox as an immutable snapshot.
Its title bar freezes the directory, versions, and other configured title
context from command start alongside the start time. The completed chatbox is
followed immediately by its frozen Git and system context, then streamed
command output. Because duration and exit status are only known after the
command finishes, they close the execution in a separate outcome strip.
The command surface and outcome strip use the dim completed background; title
and context rows retain the normal metadata background. Later directory changes
or asynchronous provider refreshes do not rewrite previous executions. A blank
row separates executions. Git facts are separate dot-delimited segments.
Duration, CPU, RAM, and exit status use semantic fast/average/slow, load,
success, and failure colors. Tool versions use fixed brand colors, including
PHP purple, Laravel red, Python blue, Ruby red, Elixir purple, Rust orange, Go
blue, Node green, and Docker blue.
There are no status icons.

The generated preset is the canonical source for threshold defaults. Before the
first command, exit state is neutral and duration is hidden. Exit code zero uses
the success color and non-zero codes use the failure color.

Bottom mode uses standard VT cursor positioning and scrolling regions rather
than terminal-specific APIs. It falls back to classic positioning when Vuja
cannot obtain a usable terminal size. Ghostty and iTerm2 are the primary
compatibility targets, but real-terminal verification of this new mode is not
yet recorded in this repository.

Rendering has one serialized output owner. PTY bytes are first applied to an
incremental VT model so prompt boundaries split across reads, ANSI styling,
Unicode cell widths, multiline prompts, and cursor movement are resolved before
a complete bottom surface is presented. Ordinary foreground output streams
directly; prompt/input surfaces repaint only changed rows.
Composed replay is limited to the captured prompt/input surface: replaying
shell-native right prompts, zero-width escapes, combining characters, wrapping,
and arbitrary cursor controls made a line-oriented parser insufficient. Vuja
does not place the shell session in an alternate screen or repaint foreground
command output through the model.

Keybindings accept `tab`, `shift+tab`, `ctrl+a` through `ctrl+z`, `home`, `end`,
`alt+right`, `ctrl+right`, and `meta+right`. Set `keymap = "emacs"` or
`keymap = "vi"` for the finite line-editing preset. `move-beginning`,
`move-end`, `clear-screen`, `clear-line`, `cancel`, and `delete-word` may then
override individual editing actions. The vi preset tracks Home and End for line
boundaries while the shell remains responsible for modal editing. Set a single
binding to `none` or `accept-token` to `[]` to disable it. Vuja rejects
duplicate bindings and does not provide arbitrary input scripts.

The `history-search` binding searches anywhere in the complete command and
falls back to fuzzy subsequence matching. It preserves separate executions of
the same command, highlights every literal or fuzzy match, and shows duration,
relative execution time, and command by default. Set
`ui.history.show-exit-status` or `ui.history.show-cwd` to add those columns.
Optional columns are removed first on narrow terminals so command text retains
the available space. Atuin execution metadata is imported when its local
database is available; otherwise Vuja uses the active shell's history and
records native Vuja executions without doing database work while you type. Set
`history.import-atuin = false` to ignore an existing Atuin database and use
shell and native Vuja history only. Set
`suggestions.import-zoxide = false` to rank directories without executing or
importing Zoxide.

On an empty prompt, Up is a deterministic history operation rather than a
prediction request. It shows at most `ui.max-suggestions` executions newest
first, preserves repeats, includes available time, directory, duration, and
outcome metadata, and selects the newest command immediately. Up and Down then
move in their visual directions through the displayed rows.

`pins` always promotes matching commands. `blocks` and `ignore-patterns`
accept shell-style patterns. Ignored commands are neither stored nor shown.
Known destructive commands remain hidden until the typed query is itself
destructive. Credentials in assignments and common secret flags are always
excluded.

Directory suggestions default to a balanced combination of recent navigation
frequency and continuous recency decay. Balanced mode counts navigation during
a rolling 14-day activity window, then applies a 45-day recency half-life, so
large lifetime totals cannot dominate current work. Explicit frequent mode
still uses lifetime navigation frequency.
Set `directory-ranking = "recent"` or `"frequent"` under `[suggestions]` to
favor either signal explicitly:

```toml
[suggestions]
directory-ranking = "balanced" # balanced | recent | frequent
```

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

## Privacy

Vuja treats a command beginning with whitespace as private and does not add it
to session history, imported history, rich history, scoring, feedback, or
command-transition predictions. This is deliberately enforced even when Zsh's
`HIST_IGNORE_SPACE` option is disabled. Zsh `HISTORY_IGNORE` glob matches are
also excluded. Ignored commands break the transition chain so they cannot
resurface as the predecessor of a later suggestion.

The shell hook sends Vuja only the ignore decision, never the ignored command.
Vuja's configuration, state, history database and SQLite sidecars, debug logs,
latency snapshots, and crash reports are stored with owner-only permissions.
Existing files are repaired when opened.

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

Nightly releases run daily at 02:17 UTC or on manual dispatch. The workflow
retains the newest 14 nightly releases and removes older nightly releases and
their tags.

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

To inspect why candidates rank in a particular order:

```bash
vuja debug suggest "cd scalar-" --cwd ~/Developer --mode spec
vuja debug suggest "cd scalar-" --cwd ~/Developer --mode spec --json
```

The output includes each candidate's source, total score, and the base,
context, frecency, transition, match-quality, and directory-affinity
components.

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
just bench
just lint
just analyze
```

`just bench` runs deterministic scoring, filesystem-completion, history-search,
end-to-end warm suggestion, immediate-cache, status-layout, Git-cache,
repository-relevance, version-cache, system-metric, and full prompt-redraw
benchmarks five times with allocation reporting. Compare
the raw `ns/op`, `B/op`, and `allocs/op` results between commits. Focused local
regression tests cap cached status rendering at `100µs` with at most one
allocation and managed first draw at `5ms`; shared CI runners do not enforce
wall-clock benchmark thresholds because their timing is noisy.

The interactive suggestion path has no fixed keystroke debounce. It renders a
cached, narrowed result immediately. On a cold query it first uses in-memory
static specs and cached history, then enriches the list asynchronously. Older
queries are cancelled and cannot replace newer results; an explicit selection
is preserved by command identity when enrichment arrives. Slow generators use
a stale-while-revalidate cache, while Cobra completion and persistent scoring
run only inside background enrichment. Startup history imports never block the
interactive scoring path. The menu shows `updating` only when enrichment takes
longer than 100 ms.

Bracketed paste is forwarded to the shell as one atomic editing operation.
Embedded newlines remain in the editing buffer and are not interpreted as
separate command submissions; suggestions stay hidden until the multiline
buffer is submitted or cleared.

`vuja debug latency` reports recorded p50 and p95 timing for first suggestion,
first paint, full enrichment, and each built-in suggestion source, plus cache
hits, misses, and bounded-source timeouts. Reports identify their Vuja session,
process, and snapshot time; pass `--session <id>` to read the same session on a
later invocation instead of whichever session updated most recently. Use
`--json` for machine-readable output. Samples are bounded and persisted at most
once per second, so diagnostics do not add synchronous disk work to a
keystroke.

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
track the prompt buffer and respond to navigation keys. A serialized terminal
compositor owns PTY, overlay, and notification writes. In classic mode it is a
pass-through. In bottom mode it captures the active prompt in a bounded VT
model and reserves its rows at the bottom. Ordinary foreground output streams
directly while the active prompt surface remains compositor-owned.
Transient overlay state and presentation are committed in order. When
background output scrolls the upper region, the compositor clears and repaints
the visible overlay within the same synchronized frame so menu rows cannot be
shifted into stale screen fragments.
Non-cell OSC side effects captured with a prompt, such as terminal-title and
working-directory updates, are forwarded once while visible prompt cells remain
compositor-owned.
Model-generated terminal-query replies are drained internally; foreground
queries still pass through to the containing terminal.

Zsh, Bash, and Fish integrations place session-scoped prompt and command
boundaries in the ordered PTY stream. The separate IPC descriptor continues to
carry command metadata, working-directory changes, and input-buffer updates.

An active process listens for `SIGUSR1` and replaces itself with the newly
installed executable while preserving the underlying PTY shell.

### Suggestion sources and ranking

Vuja gathers command specifications, aliases, loaded dotfiles functions,
executable names, filesystem entries, shell history, and optional AI output.
Structured and historical candidates are ranked in one pool. The active
`spec` or `history` mode changes source emphasis without hiding the other
source.

The score combines:

- source priority;
- prefix and fuzzy match quality;
- command frequency and bucketed recency;
- current-directory affinity;
- the previous command;
- exact command sequences; and
- normalized command-skeleton sequences;
- successful argument values learned for each command position.

After a failed command, Vuja temporarily prioritizes recently successful
variants. Exit status 127 also enables local edit-distance correction against
installed executable names.

Persistent shell history is imported as a replaceable snapshot so restarts do
not inflate learned frequency counts. Successful commands recorded by Vuja
remain separate from that snapshot.

Directory suggestions combine paths visited through Vuja with working
directories imported from shell or Atuin history, Zoxide rankings, and linked
Git worktrees. External directory sources refresh asynchronously at startup and
do not run during keystrokes. Equivalent paths with and without a trailing slash
share the same usage evidence, and deterministic tie-breakers keep the order
stable between identical queries.

### Command specifications

The `spec` package models top-level commands, recursive subcommands, options,
and dynamic generators. Lookup tokenizes the current prompt, walks the known
command tree, identifies the partial token, and returns matching candidates.
Flags receive lower priority while typing positional arguments and are promoted
when the partial token starts with `-`.

Aliases discovered in shell configuration are expanded before lookup so an
alias can still receive accurate subcommand and option suggestions.

The shell integration also publishes already-loaded Zsh, Bash, and Fish
functions whose defining source is under `~/.dotfiles`. Vuja never sources
dotfiles to discover them. Function suggestions appear only in command
position, use a distinct `function` source badge, and participate in the same
recency/frequency learning as other commands. Add a directly preceding
`# vuja: description` comment to provide the menu description.

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
palettes, Nerd Font icons, match highlighting, and ghost text. In bottom mode
it uses a borderless full-width surface, aligns its selection marker with the
managed prompt marker, and renders ghost text on the active chatbox background.
Every suggestion ends with a right-aligned, color-coded source badge such as
`history`, `function`, `learned`, `visited`, `directory`, or `command`.

### Updater

The updater checks the configured GitHub release channel in the background,
compares semantic versions, and persists the last check and last notified
version. `vuja update` downloads the matching release archive, verifies it
against the published `SHA256SUMS`, and atomically replaces the running
executable.

Builds inject the release version with Go linker flags:

```bash
go build \
  -ldflags="-X github.com/faustbrian/vuja/root.Version=v1.2.0" \
  -o vuja ./cmd/vuja
```

<!-- BEGIN GENERATED COMMANDS -->
## Command reference

Command specifications are grouped under [`commands/`](commands), registered through [`commands/all.go`](commands/all.go), and resolved by the [`spec/`](spec) completion engine.

Currently, Vuja natively supports **567** top-level CLI commands across **14** categories:

- [Cloud, Containers, Kubernetes, DevOps & Databases (`ops/`)](#ops): **118** commands
- [JavaScript, TypeScript, Frontend & Node.js Tools (`js/`)](#js): **82** commands
- [Python Ecosystem & Data Science Tools (`python/`)](#python): **19** commands
- [Rust Ecosystem & Modern CLI Tools (`rust/`)](#rust): **11** commands
- [Go Development & Project Tools (`golang/`)](#golang): **3** commands
- [Java, Kotlin & JVM Build Tools (`jvm/`)](#jvm): **14** commands
- [C/C++ Compilers & Build Systems (`cc/`)](#cc): **16** commands
- [Git Version Control & GitHub Tools (`git/`)](#git): **8** commands
- [System Package Managers (`pkginstaller/`)](#pkginstaller): **12** commands
- [Filesystem, Directory & Archive Utilities (`fs/`)](#fs): **30** commands
- [Editors, Pagers & File Viewers (`view/`)](#view): **27** commands
- [Text Processing, JSON & Stream Manipulation (`text/`)](#text): **28** commands
- [Task Runners & Build Automation (`runner/`)](#runner): **24** commands
- [System Administration, Network & Process Management (`sys/`)](#sys): **175** commands

<a id="ops"></a>
### Cloud, Containers, Kubernetes, DevOps & Databases (`ops/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`amplify`** | Environment | [`amplify.go`](commands/ops/amplify.go) |
| **`ampx`** | CLI for Amplify Gen 2 | [`ampx.go`](commands/ops/ampx.go) |
| **`ansible`** | Define and run a single Ansible task | [`ansible.go`](commands/ops/ansible.go) |
| **`ansible-config`** | View ansible configuration | [`ansible_config.go`](commands/ops/ansible_config.go) |
| **`ansible-doc`** | Displays information on modules installed in Ansible libraries | [`ansible_doc.go`](commands/ops/ansible_doc.go) |
| **`ansible-galaxy`** | The Galaxy API server URL | [`ansible_galaxy.go`](commands/ops/ansible_galaxy.go) |
| **`ansible-lint`** | Ansible static code analysis | [`ansible_lint.go`](commands/ops/ansible_lint.go) |
| **`ansible-playbook`** | Runs Ansible playbooks, executing the defined tasks on the targeted hosts | [`ansible_playbook.go`](commands/ops/ansible_playbook.go) |
| **`appwrite`** | Appwrite - Open-Source End-to-End Backend Server | [`appwrite.go`](commands/ops/appwrite.go) |
| **`arch`** | 32-bit intel | [`arch.go`](commands/ops/arch.go) |
| **`arduino-cli`** | Arduino CLI - build, compile, and upload Arduino sketches | [`arduino_cli.go`](commands/ops/arduino_cli.go) |
| **`argo`** | If True, Use the HTTP client. Defaults to the ARGO_HTTP1 environment variable | [`argo.go`](commands/ops/argo.go) |
| **`asdf`** | Plugin name | [`asdf.go`](commands/ops/asdf.go) |
| **`atlas`** | CLI tool to manage MongoDB Atlas | [`atlas.go`](commands/ops/atlas.go) |
| **`aws`** | Use a specific profile from your credential file | [`aws.go`](commands/ops/aws.go) |
| **`aws-vault`** | Add credentials to the secure keystore | [`aws_vault.go`](commands/ops/aws_vault.go) |
| **`bit`** | Bit documentation: https://bit.dev/docs | [`bit.go`](commands/ops/bit.go) |
| **`bosh`** | Deployment | [`bosh.go`](commands/ops/bosh.go) |
| **`capacitor`** | Add a native platform project to your app | [`capacitor.go`](commands/ops/capacitor.go) |
| **`cdk`** | AWS CDK CLI | [`cdk.go`](commands/ops/cdk.go) |
| **`cf`** | Cloudfoundry cli | [`cf.go`](commands/ops/cf.go) |
| **`checkov`** | Branch | [`checkov.go`](commands/ops/checkov.go) |
| **`circleci`** | CircleCI CLI | [`circleci.go`](commands/ops/circleci.go) |
| **`cloudflared`** | Specify the hostname of your application | [`cloudflared.go`](commands/ops/cloudflared.go) |
| **`coda`** | Coda CLI - interact with Coda docs and tables | [`coda.go`](commands/ops/coda.go) |
| **`command`** | Run an external command | [`command.go`](commands/ops/command.go) |
| **`copilot`** | Name of the application | [`copilot.go`](commands/ops/copilot.go) |
| **`cosign`** | Provides utilities for attaching artifacts to other artifacts in a registry | [`cosign.go`](commands/ops/cosign.go) |
| **`dapr`** | Distributed Application Runtime CLI | [`dapr.go`](commands/ops/dapr.go) |
| **`datree`** | Help for | [`datree.go`](commands/ops/datree.go) |
| **`deployctl`** | Command line tool for Deno Deploy | [`deployctl.go`](commands/ops/deployctl.go) |
| **`direnv`** | Help for direnv | [`direnv.go`](commands/ops/direnv.go) |
| **`docker`** | container engine | [`docker.go`](commands/ops/docker.go) |
| **`docker-compose`** | multi-container (legacy) | [`docker.go`](commands/ops/docker.go) |
| **`doctl`** | The official DigitalOcean command line interface (CLI) | [`doctl.go`](commands/ops/doctl.go) |
| **`doppler`** | The official CLI for Doppler Secret Operations Platform | [`doppler.go`](commands/ops/doppler.go) |
| **`eas`** | Log in with your Expo account | [`eas.go`](commands/ops/eas.go) |
| **`fastly`** | A CLI for interacting with the Fastly platform | [`fastly.go`](commands/ops/fastly.go) |
| **`firebase`** | ProjectAlias | [`firebase.go`](commands/ops/firebase.go) |
| **`flyctl`** | Command line tool for Fly.io services | [`flyctl.go`](commands/ops/flyctl.go) |
| **`fnm`** | Fast and simple Node.js version manager | [`fnm.go`](commands/ops/fnm.go) |
| **`gcloud`** | Manage Google Cloud Platform resources and developer workflow | [`gcloud.go`](commands/ops/gcloud.go) |
| **`gh`** | Current branch | [`gh.go`](commands/ops/gh.go) |
| **`gpg`** | Encryption and signing tool | [`gpg.go`](commands/ops/gpg.go) |
| **`hasura`** | .env filename to load ENV vars from | [`hasura.go`](commands/ops/hasura.go) |
| **`helm`** | The Helm package manager for Kubernetes | [`helm.go`](commands/ops/helm.go) |
| **`helmfile`** | Deploy helm charts | [`helmfile.go`](commands/ops/helmfile.go) |
| **`hugo`** | The world | [`hugo.go`](commands/ops/hugo.go) |
| **`k3d`** | Little helper to run k3s in Docker | [`k3d.go`](commands/ops/k3d.go) |
| **`k6`** | Create an archive | [`k6.go`](commands/ops/k6.go) |
| **`k9s`** | Kubernetes namespace | [`k9s.go`](commands/ops/k9s.go) |
| **`kind`** | Cluster | [`kind.go`](commands/ops/kind.go) |
| **`knex`** | SQL query builder for JavaScript | [`knex.go`](commands/ops/knex.go) |
| **`kubectl`** | kubernetes cli | [`kubectl.go`](commands/ops/kubectl.go) |
| **`kubectx`** | Switch between Kubernetes-contexts | [`kubectx.go`](commands/ops/kubectx.go) |
| **`kubens`** | Switch between Kubernetes-namespaces | [`kubens.go`](commands/ops/kubens.go) |
| **`limactl`** | Lima: Linux virtual machines, with a focus on running containers | [`limactl.go`](commands/ops/limactl.go) |
| **`locust`** | Show program | [`locust.go`](commands/ops/locust.go) |
| **`lpass`** | Command line interface for LastPass | [`lpass.go`](commands/ops/lpass.go) |
| **`minikube`** | Format to print stdout in | [`minikube.go`](commands/ops/minikube.go) |
| **`mongocli`** | CLI tool to manage your MongoDB Cloud | [`mongocli.go`](commands/ops/mongocli.go) |
| **`mongoimport`** | Import data from a JSON, CSV, or TSV file into a MongoDB instance | [`mongoimport.go`](commands/ops/mongoimport.go) |
| **`mongosh`** | Default Connection String; Equivalent to running mongosh without any commands | [`mongosh.go`](commands/ops/mongosh.go) |
| **`multipass`** | Displays help on commandline options | [`multipass.go`](commands/ops/multipass.go) |
| **`mysql`** | Mysql is a terminal-based front-end to MySQL | [`mysql.go`](commands/ops/mysql.go) |
| **`netlify`** | Print debugging information | [`netlify.go`](commands/ops/netlify.go) |
| **`newman`** | Newman is a command-line collection runner for Postman | [`newman.go`](commands/ops/newman.go) |
| **`nginx`** | Nginx (pronounced | [`nginx.go`](commands/ops/nginx.go) |
| **`ngrok`** | Path to log file, | [`ngrok.go`](commands/ops/ngrok.go) |
| **`nvm`** | Node version | [`nvm.go`](commands/ops/nvm.go) |
| **`oci`** | Oracle Cloud Infrastructure CLI | [`oci.go`](commands/ops/oci.go) |
| **`okteto`** | Context | [`okteto.go`](commands/ops/okteto.go) |
| **`op`** | Official 1Password CLI | [`op.go`](commands/ops/op.go) |
| **`opa`** | Open Policy Agent (OPA) | [`opa.go`](commands/ops/opa.go) |
| **`osqueryi`** | Your OS as a high-performance relational database | [`osqueryi.go`](commands/ops/osqueryi.go) |
| **`pass`** | Pass - stores, retrieves, generates, and synchronizes passwords securely | [`pass.go`](commands/ops/pass.go) |
| **`pg_dump`** | Dumps a database as a text file or to other formats | [`pg_dump.go`](commands/ops/pg_dump.go) |
| **`pgcli`** | Host address of the postgres database | [`pgcli.go`](commands/ops/pgcli.go) |
| **`pm2`** | Outputs the version number | [`pm2.go`](commands/ops/pm2.go) |
| **`pod`** | CocoaPods, the Cocoa library package manager | [`pod.go`](commands/ops/pod.go) |
| **`podman`** | Build an image using instructions from Containerfiles | [`podman.go`](commands/ops/podman.go) |
| **`pscale`** | The client ID for the PlanetScale CLI application | [`pscale.go`](commands/ops/pscale.go) |
| **`psql`** | Psql is a terminal-based front-end to PostgreSQL | [`psql.go`](commands/ops/psql.go) |
| **`pulumi`** | The name of the stack to operate on. Defaults to the current stack | [`pulumi.go`](commands/ops/pulumi.go) |
| **`qodana`** | Run Qodana as fast as possible, with minimum effort required | [`qodana.go`](commands/ops/qodana.go) |
| **`railway`** | CLI for managing Railway Apps | [`railway.go`](commands/ops/railway.go) |
| **`rbenv`** | List all available rbenv commands | [`rbenv.go`](commands/ops/rbenv.go) |
| **`robot`** | Tag | [`robot.go`](commands/ops/robot.go) |
| **`rsync`** | remote sync | [`ssh.go`](commands/ops/ssh.go) |
| **`scp`** | secure copy | [`ssh.go`](commands/ops/ssh.go) |
| **`serverless`** | AWS profile to use with the command | [`serverless.go`](commands/ops/serverless.go) |
| **`sfdx`** | Analyze (lint) Aura component code | [`sfdx.go`](commands/ops/sfdx.go) |
| **`sftp`** | OpenSSH secure file transfer | [`sftp.go`](commands/ops/sftp.go) |
| **`space`** | Deta Space CLI for mananging Deta Space projects | [`space.go`](commands/ops/space.go) |
| **`sqlite3`** | A command line interface for SQLite version 3 | [`sqlite3.go`](commands/ops/sqlite3.go) |
| **`src`** | Interact with Sourcegraph from the command line | [`src.go`](commands/ops/src.go) |
| **`ssh`** | secure shell | [`ssh.go`](commands/ops/ssh.go) |
| **`ssh-keygen`** | Generates, manages and converts authentication keys for ssh | [`ssh_keygen.go`](commands/ops/ssh_keygen.go) |
| **`stripe`** | Stripe CLI - build, test, and manage your Stripe integrations right from your terminal | [`stripe.go`](commands/ops/stripe.go) |
| **`supabase`** | Supabase CLI | [`supabase.go`](commands/ops/supabase.go) |
| **`surreal`** | Database authentication password to use when connecting [default: root] | [`surreal.go`](commands/ops/surreal.go) |
| **`tailscale`** | Connect to Tailscale, logging in if needed | [`tailscale.go`](commands/ops/tailscale.go) |
| **`terraform`** | Workspace | [`terraform.go`](commands/ops/terraform.go) |
| **`terragrunt`** | Workspace | [`terragrunt.go`](commands/ops/terragrunt.go) |
| **`tfenv`** | Version | [`tfenv.go`](commands/ops/tfenv.go) |
| **`tfsec`** | Terraform workspaces | [`tfsec.go`](commands/ops/tfsec.go) |
| **`tkn`** | CLI for tekton pipelines | [`tkn.go`](commands/ops/tkn.go) |
| **`trivy`** | Skip updating built-in policies [$TRIVY_SKIP_POLICY_UPDATE] | [`trivy.go`](commands/ops/trivy.go) |
| **`tsuru`** | Plan | [`tsuru.go`](commands/ops/tsuru.go) |
| **`vault`** | Display help | [`vault.go`](commands/ops/vault.go) |
| **`vela`** | Show the reference doc for component, trait or workflow types | [`vela.go`](commands/ops/vela.go) |
| **`vercel`** | CLI Interface for Vercel.com | [`vercel.go`](commands/ops/vercel.go) |
| **`volta`** | Enables verbose diagnostics | [`volta.go`](commands/ops/volta.go) |
| **`watson`** | A wonderful CLI to track your time | [`watson.go`](commands/ops/watson.go) |
| **`whois`** | Query a database for information about a domain registrant | [`whois.go`](commands/ops/whois.go) |
| **`wrangler`** | Path to configuration file [default: wrangler.toml] | [`wrangler.go`](commands/ops/wrangler.go) |
| **`xc`** | List tasks from an xc-compatible markdown file | [`xc.go`](commands/ops/xc.go) |
| **`xcodes`** | Manage the Xcode versions installed on your Mac | [`xcodes.go`](commands/ops/xcodes.go) |

<a id="js"></a>
### JavaScript, TypeScript, Frontend & Node.js Tools (`js/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`asar`** | A simple extensive tar-like archive format with indexing | [`asar.go`](commands/js/asar.go) |
| **`astro`** | Add an integration | [`astro.go`](commands/js/astro.go) |
| **`babel`** | A comma-separated list of preset names | [`babel.go`](commands/js/babel.go) |
| **`blitz`** | Show help for command | [`blitz.go`](commands/js/blitz.go) |
| **`browser-sync`** | Keep multiple browsers & devices in sync when building websites | [`browser_sync.go`](commands/js/browser_sync.go) |
| **`build-storybook`** | Storybook build CLI tools | [`build_storybook.go`](commands/js/build_storybook.go) |
| **`bun`** | bun js runtime | [`bun.go`](commands/js/bun.go) |
| **`bunx`** | execute package (bun x) | [`bun.go`](commands/js/bun.go) |
| **`cordova`** | Print out the version of your cordova-cli install | [`cordova.go`](commands/js/cordova.go) |
| **`create-completion-spec`** | Setup fig folder and create spec with the given name | [`create_completion_spec.go`](commands/js/create_completion_spec.go) |
| **`create-next-app`** | Output the version number | [`create_next_app.go`](commands/js/create_next_app.go) |
| **`create-nx-workspace`** | The name of the workspace | [`create_nx_workspace.go`](commands/js/create_nx_workspace.go) |
| **`create-react-app`** | Creates a new React project | [`create_react_app.go`](commands/js/create_react_app.go) |
| **`create-react-native-app`** | Creates a new React Native project | [`create_react_native_app.go`](commands/js/create_react_native_app.go) |
| **`create-redwood-app`** | Name of your Redwood project | [`create_redwood_app.go`](commands/js/create_redwood_app.go) |
| **`create-remix`** | Display help for command | [`create_remix.go`](commands/js/create_remix.go) |
| **`create-t3-app`** | The name of the application, as well as the name of the directory to create | [`create_t3_app.go`](commands/js/create_t3_app.go) |
| **`create-video`** | CLI used to create remotion video project | [`create_video.go`](commands/js/create_video.go) |
| **`create-vite`** | Create a new project powered by Vite | [`create_vite.go`](commands/js/create_vite.go) |
| **`create-web3-frontend`** | Quickly create a Next.js project with wagmi and TailwindCSS ready to go | [`create_web3_frontend.go`](commands/js/create_web3_frontend.go) |
| **`deno`** | A modern JavaScript and TypeScript runtime | [`deno.go`](commands/js/deno.go) |
| **`dotenv`** | Loads environment variables from .env | [`dotenv.go`](commands/js/dotenv.go) |
| **`electron`** | Build cross platform desktop apps with JavaScript, HTML and CSS | [`electron.go`](commands/js/electron.go) |
| **`elm`** | Fig spec for the Elm language cli | [`elm.go`](commands/js/elm.go) |
| **`elm-format`** | Format your code in the Elm idiomatic way | [`elm_format.go`](commands/js/elm_format.go) |
| **`elm-json`** | Deal with your elm.json | [`elm_json.go`](commands/js/elm_json.go) |
| **`elm-review`** | Prints a single JSON object | [`elm_review.go`](commands/js/elm_review.go) |
| **`esbuild`** | An extremely fast JavaScript bundler | [`esbuild.go`](commands/js/esbuild.go) |
| **`eslint`** | Pluggable JavaScript linter | [`eslint.go`](commands/js/eslint.go) |
| **`expo`** | Tools for creating, running, and deploying Universal Expo and React Native apps | [`expo.go`](commands/js/expo.go) |
| **`expo-cli`** | Tools for creating, running, and deploying Universal Expo and React Native apps | [`expo_cli.go`](commands/js/expo_cli.go) |
| **`ganache-cli`** | Fast Ethereum RPC client | [`ganache_cli.go`](commands/js/ganache_cli.go) |
| **`gatsby`** | Set host. Defaults to localhost | [`gatsby.go`](commands/js/gatsby.go) |
| **`hardhat`** | Ethereum development environment | [`hardhat.go`](commands/js/hardhat.go) |
| **`ionic`** | Target engine (e.g. browser, cordova) | [`ionic.go`](commands/js/ionic.go) |
| **`jest`** | A delightful JavaScript Testing Framework with a focus on simplicity | [`jest.go`](commands/js/jest.go) |
| **`lerna`** | Branch | [`lerna.go`](commands/js/lerna.go) |
| **`meteor`** | Run the meteor command-line tool | [`meteor.go`](commands/js/meteor.go) |
| **`ncu`** | Clear the default cache, or the cache file specified by --cacheFile | [`ncu.go`](commands/js/ncu.go) |
| **`nest`** | Report actions that would be taken without writing out results | [`nest.go`](commands/js/nest.go) |
| **`next`** | A port number on which to start the application | [`next.go`](commands/js/next.go) |
| **`ng`** | Project name | [`ng.go`](commands/js/ng.go) |
| **`node`** | Run the node interpreter | [`node.go`](commands/js/node.go) |
| **`npm`** | node packages | [`npm.go`](commands/js/npm.go) |
| **`npx`** | Execute binaries from npm packages | [`npx.go`](commands/js/npx.go) |
| **`nuxi`** | The directory of the target application | [`nuxi.go`](commands/js/nuxi.go) |
| **`nuxt`** | Launch the development server | [`nuxt.go`](commands/js/nuxt.go) |
| **`nx`** | All projects | [`nx.go`](commands/js/nx.go) |
| **`oxlint`** | All lints (except nursery) | [`oxlint.go`](commands/js/oxlint.go) |
| **`playwright`** | Display help for command | [`playwright.go`](commands/js/playwright.go) |
| **`pnpm`** | fast node packages | [`pnpm.go`](commands/js/pnpm.go) |
| **`pnpx`** | Execute binaries from npm packages | [`pnpx.go`](commands/js/pnpx.go) |
| **`prettier`** | Run Prettier from the command line | [`prettier.go`](commands/js/prettier.go) |
| **`quasar`** | Quasar Framework CLI | [`quasar.go`](commands/js/quasar.go) |
| **`react-native`** | Attempt to fix all diagnosed issues | [`react_native.go`](commands/js/react_native.go) |
| **`redwood`** | Script | [`redwood.go`](commands/js/redwood.go) |
| **`remix`** | Represent the directory of the Remix application | [`remix.go`](commands/js/remix.go) |
| **`remotion`** | Create videos programmatically in React | [`remotion.go`](commands/js/remotion.go) |
| **`rollup`** | Next-generation ES module bundler | [`rollup.go`](commands/js/rollup.go) |
| **`rome`** | Rome CLI | [`rome.go`](commands/js/rome.go) |
| **`rush`** | Projects | [`rush.go`](commands/js/rush.go) |
| **`sequelize`** | The environment to run the command in | [`sequelize.go`](commands/js/sequelize.go) |
| **`serve`** | Static file serving and directory listing | [`serve.go`](commands/js/serve.go) |
| **`shadcn-ui`** | Shadcn UI CLI | [`shadcn_ui.go`](commands/js/shadcn_ui.go) |
| **`start-storybook`** | Display usage information | [`start_storybook.go`](commands/js/start_storybook.go) |
| **`stencil`** | CLI to build Stencil projects and generate components | [`stencil.go`](commands/js/stencil.go) |
| **`swagger-typescript-api`** | Generate api via swagger scheme | [`swagger_typescript_api.go`](commands/js/swagger_typescript_api.go) |
| **`swc`** | Path to the file | [`swc.go`](commands/js/swc.go) |
| **`truffle`** | Execute build pipeline (if configuration present) | [`truffle.go`](commands/js/truffle.go) |
| **`ts-node`** | Run the TypeScript interpreter for Node.JS | [`ts_node.go`](commands/js/ts_node.go) |
| **`tsc`** | CLI tool for TypeScript compiler | [`tsc.go`](commands/js/tsc.go) |
| **`tsx`** | Run TypeScript file using tsx | [`tsx.go`](commands/js/tsx.go) |
| **`turbo`** | Print the version | [`turbo.go`](commands/js/turbo.go) |
| **`typeorm`** | Show help for command | [`typeorm.go`](commands/js/typeorm.go) |
| **`vite`** | Native ESM-powered web dev build tool | [`vite.go`](commands/js/vite.go) |
| **`vr`** | The npm-style script runner for Deno | [`vr.go`](commands/js/vr.go) |
| **`vsce`** | The Visual Studio Code Extension Manager | [`vsce.go`](commands/js/vsce.go) |
| **`vue`** | Vue cli tools | [`vue.go`](commands/js/vue.go) |
| **`watchman`** | A file watching service | [`watchman.go`](commands/js/watchman.go) |
| **`webpack`** | Run webpack (default command, can be omitted) | [`webpack.go`](commands/js/webpack.go) |
| **`yalc`** | Work with yarn/npm packages locally like a boss | [`yalc.go`](commands/js/yalc.go) |
| **`yarn`** | yarn package manager | [`yarn.go`](commands/js/yarn.go) |

<a id="python"></a>
### Python Ecosystem & Data Science Tools (`python/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`black`** | Version | [`black.go`](commands/python/black.go) |
| **`conda`** | Name of environment | [`conda.go`](commands/python/conda.go) |
| **`django-admin`** | Show this help message and exit | [`django_admin.go`](commands/python/django_admin.go) |
| **`googler`** | Google from the command-line | [`googler.go`](commands/python/googler.go) |
| **`jupyter`** | Set log level to logging.DEBUG (maximize logging output) | [`jupyter.go`](commands/python/jupyter.go) |
| **`mamba`** | Mamba is a fast, robust, and cross-platform package manager | [`mamba.go`](commands/python/mamba.go) |
| **`mypy`** | Mypy is a static type checker for Python | [`mypy.go`](commands/python/mypy.go) |
| **`pipenv`** | Python package manager | [`pipenv.go`](commands/python/pipenv.go) |
| **`pipx`** | Installed package | [`pipx.go`](commands/python/pipx.go) |
| **`poetry`** | python dependency manager | [`python.go`](commands/python/python.go) |
| **`pre-commit`** | Show help message and exit | [`pre_commit.go`](commands/python/pre_commit.go) |
| **`pyenv`** | Pyenv | [`pyenv.go`](commands/python/pyenv.go) |
| **`pytest`** | Control assertion debugging tools. | [`pytest.go`](commands/python/pytest.go) |
| **`ruff`** | Enable verbose logging | [`ruff.go`](commands/python/ruff.go) |
| **`sqlfluff`** | A dialect-flexible and configurable SQL linter | [`sqlfluff.go`](commands/python/sqlfluff.go) |
| **`sqlmesh`** | SQLMesh command line tool | [`sqlmesh.go`](commands/python/sqlmesh.go) |
| **`streamlit`** | Streamlit | [`streamlit.go`](commands/python/streamlit.go) |
| **`uv`** | fast python package manager | [`python.go`](commands/python/python.go) |
| **`youtube-dl`** | Clipboard | [`youtube_dl.go`](commands/python/youtube_dl.go) |

<a id="rust"></a>
### Rust Ecosystem & Modern CLI Tools (`rust/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`cargo`** | rust toolchain | [`cargo.go`](commands/rust/cargo.go) |
| **`dprint`** | Prints the help of the given subcommand(s) | [`dprint.go`](commands/rust/dprint.go) |
| **`pijul`** | Adds a path to the tree | [`pijul.go`](commands/rust/pijul.go) |
| **`rustc`** | Rust compiler | [`rustc.go`](commands/rust/rustc.go) |
| **`rustup`** | The Rust toolchain installer | [`rustup.go`](commands/rust/rustup.go) |
| **`taplo`** | Set color values for the output | [`taplo.go`](commands/rust/taplo.go) |
| **`tokei`** | Count your code, quickly | [`tokei.go`](commands/rust/tokei.go) |
| **`trunk`** | Run on all files instead of only changed files | [`trunk.go`](commands/rust/trunk.go) |
| **`wasm-bindgen`** | Generate bindings between WebAssembly and JavaScript | [`wasm_bindgen.go`](commands/rust/wasm_bindgen.go) |
| **`wasm-pack`** | Build an npm package | [`wasm_pack.go`](commands/rust/wasm_pack.go) |
| **`zellij`** | Change where zellij looks for the configuration file | [`zellij.go`](commands/rust/zellij.go) |

<a id="golang"></a>
### Go Development & Project Tools (`golang/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`go`** | tool for managing Go source code | [`go.go`](commands/golang/go.go) |
| **`goctl`** | A cli tool to generate go-zero code | [`goctl.go`](commands/golang/goctl.go) |
| **`goreleaser`** | Deliver Go binaries as fast and easily as possible | [`goreleaser.go`](commands/golang/goreleaser.go) |

<a id="jvm"></a>
### Java, Kotlin & JVM Build Tools (`jvm/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`clojure`** | An alias to refer to its function or a qualified function | [`clojure.go`](commands/jvm/clojure.go) |
| **`dart`** | The Dart file containing the main function | [`dart.go`](commands/jvm/dart.go) |
| **`flutter`** | Available emulators | [`flutter.go`](commands/jvm/flutter.go) |
| **`fvm`** | Print this usage information | [`fvm.go`](commands/jvm/fvm.go) |
| **`gradle`** | Log all warnings | [`gradle.go`](commands/jvm/gradle.go) |
| **`java`** | Java runtime | [`jvm.go`](commands/jvm/jvm.go) |
| **`javac`** | Java compiler | [`jvm.go`](commands/jvm/jvm.go) |
| **`jenv`** | Executable file | [`jenv.go`](commands/jvm/jenv.go) |
| **`jmeter`** | Apache JMeter - 100% Java Load Testing Tool | [`jmeter.go`](commands/jvm/jmeter.go) |
| **`kdoctor`** | Report a version of KDoctor | [`kdoctor.go`](commands/jvm/kdoctor.go) |
| **`keytool`** | Show help message | [`keytool.go`](commands/jvm/keytool.go) |
| **`kotlinc`** | Kotlin compiler | [`jvm.go`](commands/jvm/jvm.go) |
| **`mvn`** | Maven - a Java based project management and comprehension tool | [`mvn.go`](commands/jvm/mvn.go) |
| **`spring`** | Initialize a new project using Spring Initializr | [`spring.go`](commands/jvm/spring.go) |

<a id="cc"></a>
### C/C++ Compilers & Build Systems (`cc/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`bazel`** | Bazel target | [`bazel.go`](commands/cc/bazel.go) |
| **`c++`** | C++ compiler (alias) | [`cc.go`](commands/cc/cc.go) |
| **`cc`** | C compiler (alias) | [`cc.go`](commands/cc/cc.go) |
| **`clang`** | LLVM C compiler | [`cc.go`](commands/cc/cc.go) |
| **`clang++`** | LLVM C++ compiler | [`cc.go`](commands/cc/cc.go) |
| **`cmake`** | Command-line interface of the cross-platform buildsystem generator CMake | [`cmake.go`](commands/cc/cmake.go) |
| **`g++`** | GNU C++ compiler | [`cc.go`](commands/cc/cc.go) |
| **`gcc`** | GNU C compiler | [`cc.go`](commands/cc/cc.go) |
| **`premake`** | The premake5.lua file | [`premake.go`](commands/cc/premake.go) |
| **`swift`** | Show help information | [`swift.go`](commands/cc/swift.go) |
| **`typst`** | The Typst compiler | [`typst.go`](commands/cc/typst.go) |
| **`xcode-select`** | Active developer directory for Xcode tools | [`xcode_select.go`](commands/cc/xcode_select.go) |
| **`xcodebuild`** | Build Xcode projects | [`xcodebuild.go`](commands/cc/xcodebuild.go) |
| **`xcodeproj`** | Xcodeproj lets you create and modify Xcode projects | [`xcodeproj.go`](commands/cc/xcodeproj.go) |
| **`xcrun`** | SceneKit CLI utilities | [`xcrun.go`](commands/cc/xcrun.go) |
| **`zig`** | Enable or disable colored message | [`zig.go`](commands/cc/zig.go) |

<a id="git"></a>
### Git Version Control & GitHub Tools (`git/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`ghq`** | Clone/sync with a remote repository | [`ghq.go`](commands/git/ghq.go) |
| **`git`** | version control | [`git.go`](commands/git/git.go) |
| **`git-cliff`** | Increases the logging verbosity | [`git_cliff.go`](commands/git/git_cliff.go) |
| **`git-flow`** | Git extensions to provide high-level repository operations for Vincent Driessen | [`git_flow.go`](commands/git/git_flow.go) |
| **`git-profile`** | Use profile | [`git_profile.go`](commands/git/git_profile.go) |
| **`git-quick-stats`** | Show help for git-quick-stats | [`git_quick_stats.go`](commands/git/git_quick_stats.go) |
| **`github`** | Open a git repository in GitHub Desktop | [`github.go`](commands/git/github.go) |
| **`svn`** | Specify a username ARG | [`svn.go`](commands/git/svn.go) |

<a id="pkginstaller"></a>
### System Package Managers (`pkginstaller/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`apt`** | Debian/Ubuntu package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`apt-get`** | Debian/Ubuntu package manager (low-level) | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`brew`** | Homebrew package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`dnf`** | Fedora/RHEL package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`dpkg`** | Debian package management system | [`dpkg.go`](commands/pkginstaller/dpkg.go) |
| **`flatpak`** | flatpak package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`pacman`** | Arch package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`paru`** | AUR helper (feature-rich) | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`pkgutil`** | Query and manipulate for macOS Installer packages and receipts | [`pkgutil.go`](commands/pkginstaller/pkgutil.go) |
| **`snap`** | snap package manager | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`yay`** | AUR helper (pacman wrapper) | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |
| **`yum`** | RHEL/CentOS package manager (legacy) | [`pkgmgr.go`](commands/pkginstaller/pkgmgr.go) |

<a id="fs"></a>
### Filesystem, Directory & Archive Utilities (`fs/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`broot`** | Show the last modified date of files and directories | [`broot.go`](commands/fs/broot.go) |
| **`cd`** | change directory | [`cd.go`](commands/fs/cd.go) |
| **`chmod`** | change file permissions | [`chmod.go`](commands/fs/chmod.go) |
| **`chown`** | change file owner | [`chown.go`](commands/fs/chown.go) |
| **`cp`** | copy files and directories | [`cp.go`](commands/fs/cp.go) |
| **`df`** | Display free disk space | [`df.go`](commands/fs/df.go) |
| **`dust`** | Like du but more intuitive | [`dust.go`](commands/fs/dust.go) |
| **`exa`** | A modern replacement for ls | [`exa.go`](commands/fs/exa.go) |
| **`eza`** | A modern replacement for ls | [`eza.go`](commands/fs/eza.go) |
| **`find`** | Walk a file hierarchy | [`find.go`](commands/fs/find.go) |
| **`fold`** | Fold long lines for finite width output device | [`fold.go`](commands/fs/fold.go) |
| **`ln`** | create links | [`ln.go`](commands/fs/ln.go) |
| **`ls`** | list directory contents | [`ls.go`](commands/fs/ls.go) |
| **`lsd`** | An ls command with a lot of pretty colors and some other stuff | [`lsd.go`](commands/fs/lsd.go) |
| **`mkdir`** | make directories | [`mkdir.go`](commands/fs/mkdir.go) |
| **`mv`** | move (rename) files | [`mv.go`](commands/fs/mv.go) |
| **`paper`** | The Paper CLI | [`paper.go`](commands/fs/paper.go) |
| **`rclone`** | Only list directories | [`rclone.go`](commands/fs/rclone.go) |
| **`readlink`** | Display file status | [`readlink.go`](commands/fs/readlink.go) |
| **`rm`** | remove files or directories | [`rm.go`](commands/fs/rm.go) |
| **`rmdir`** | Remove directories | [`rmdir.go`](commands/fs/rmdir.go) |
| **`stow`** | Manage farms of symbolic links | [`stow.go`](commands/fs/stow.go) |
| **`tar`** | Use archive file or device ARCHIVE | [`tar.go`](commands/fs/tar.go) |
| **`touch`** | create or update file timestamp | [`touch.go`](commands/fs/touch.go) |
| **`trash`** | Trash, move files/folders to the trash | [`trash.go`](commands/fs/trash.go) |
| **`tree`** | Display directories as trees (with optional color/HTML output) | [`tree.go`](commands/fs/tree.go) |
| **`unzip`** | Extract compressed files in a ZIP archive | [`unzip.go`](commands/fs/unzip.go) |
| **`z`** | jump to directory | [`zoxide.go`](commands/fs/zoxide.go) |
| **`zi`** | jump to directory interactively | [`zoxide.go`](commands/fs/zoxide.go) |
| **`zip`** | Package and compress (archive) files into zip file | [`zip.go`](commands/fs/zip.go) |

<a id="view"></a>
### Editors, Pagers & File Viewers (`view/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`bat`** | A cat(1) clone with syntax highlighting and Git integration | [`bat.go`](commands/view/bat.go) |
| **`cat`** | concatenate and print | [`cat.go`](commands/view/cat.go) |
| **`code`** | Read from stdin (e.g. | [`code.go`](commands/view/code.go) |
| **`cot`** | Command-line utility for CotEditor | [`cot.go`](commands/view/cot.go) |
| **`du`** | estimate file space usage | [`du.go`](commands/view/du.go) |
| **`emacs`** | An extensible, customizable, free/libre text editor - and more | [`emacs.go`](commands/view/emacs.go) |
| **`file`** | determine file type | [`file.go`](commands/view/file.go) |
| **`glow`** | Render markdown on the CLI, with pizzazz! | [`glow.go`](commands/view/glow.go) |
| **`head`** | output first lines of file | [`head.go`](commands/view/head.go) |
| **`idea`** | IntelliJ IDEA CLI | [`idea.go`](commands/view/idea.go) |
| **`less`** | view file contents (scrollable) | [`less.go`](commands/view/less.go) |
| **`lvim`** | Hyperextensible Vim-based text editor | [`lvim.go`](commands/view/lvim.go) |
| **`micro`** | True/false | [`micro.go`](commands/view/micro.go) |
| **`more`** | Opposite of less | [`more.go`](commands/view/more.go) |
| **`nano`** | Nano | [`nano.go`](commands/view/nano.go) |
| **`nvim`** | Hyperextensible Vim-based text editor | [`nvim.go`](commands/view/nvim.go) |
| **`rich`** | Rich terminal text formatting | [`rich.go`](commands/view/rich.go) |
| **`stat`** | display file status | [`stat.go`](commands/view/stat.go) |
| **`subl`** | Sublime Text | [`subl.go`](commands/view/subl.go) |
| **`tail`** | output last lines of file | [`tail.go`](commands/view/tail.go) |
| **`vi`** | Print help message for vi and exit | [`vi.go`](commands/view/vi.go) |
| **`vim`** | Vi IMproved, a programmer | [`vim.go`](commands/view/vim.go) |
| **`vimr`** | VimR - Neovim GUI for macOS in Swift | [`vimr.go`](commands/view/vimr.go) |
| **`wc`** | word, line, character count | [`wc.go`](commands/view/wc.go) |
| **`xed`** | Xcode text editor invocation tool | [`xed.go`](commands/view/xed.go) |
| **`xxd`** | Make a hexdump or do the reverse | [`xxd.go`](commands/view/xxd.go) |
| **`zed`** | A lightning-fast, collaborative code editor written in Rust | [`zed.go`](commands/view/zed.go) |

<a id="text"></a>
### Text Processing, JSON & Stream Manipulation (`text/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`awk`** | pattern-directed scanning | [`textproc.go`](commands/text/textproc.go) |
| **`cut`** | extract columns from lines | [`textproc.go`](commands/text/textproc.go) |
| **`diff`** | Compare files line by line | [`diff.go`](commands/text/diff.go) |
| **`dos2unix`** | DOS to Unix file format converter | [`dos2unix.go`](commands/text/dos2unix.go) |
| **`egrep`** | grep with extended regex | [`grep.go`](commands/text/grep.go) |
| **`fd`** | fast find alternative | [`rg.go`](commands/text/rg.go) |
| **`find`** | search for files | [`find.go`](commands/text/find.go) |
| **`gawk`** | GNU awk | [`textproc.go`](commands/text/textproc.go) |
| **`grep`** | search text in files | [`grep.go`](commands/text/grep.go) |
| **`iconv`** | Character set conversion | [`iconv.go`](commands/text/iconv.go) |
| **`jq`** | Output the jq version and exit with zero | [`jq.go`](commands/text/jq.go) |
| **`pandoc`** | A universal document converter | [`pandoc.go`](commands/text/pandoc.go) |
| **`rg`** | ripgrep (fast search) | [`rg.go`](commands/text/rg.go) |
| **`sed`** | stream editor | [`textproc.go`](commands/text/textproc.go) |
| **`seq`** | Print sequences of numbers. (Defaults to increments of 1) | [`seq.go`](commands/text/seq.go) |
| **`sha1sum`** | Print or check SHA1 (160-bit) checksums | [`sha1sum.go`](commands/text/sha1sum.go) |
| **`shasum`** | Print or Check SHA Checksums | [`shasum.go`](commands/text/shasum.go) |
| **`shred`** | Overwrite a file to hide its contents, and optionally delete it | [`shred.go`](commands/text/shred.go) |
| **`sort`** | sort lines of text | [`textproc.go`](commands/text/textproc.go) |
| **`split`** | Use suffix_length letters to form the suffix of the file name | [`split.go`](commands/text/split.go) |
| **`tee`** | read stdin, write to stdout and files | [`textproc.go`](commands/text/textproc.go) |
| **`tr`** | translate or delete characters | [`textproc.go`](commands/text/textproc.go) |
| **`truncate`** | Shrink or extend the size of a file to the specified size | [`truncate.go`](commands/text/truncate.go) |
| **`typos`** | Source code spelling correction | [`typos.go`](commands/text/typos.go) |
| **`uniq`** | filter adjacent duplicate lines | [`textproc.go`](commands/text/textproc.go) |
| **`unix2dos`** | Unix to DOS text file format convertor | [`unix2dos.go`](commands/text/unix2dos.go) |
| **`vale`** | A syntax-aware linter for prose built with speed and extensibility in mind | [`vale.go`](commands/text/vale.go) |
| **`xargs`** | build and run commands from stdin | [`textproc.go`](commands/text/textproc.go) |

<a id="runner"></a>
### Task Runners & Build Automation (`runner/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`ant`** | Apache Ant - Java library and command-line build tool | [`ant.go`](commands/runner/ant.go) |
| **`composer`** | Composer Command | [`composer.go`](commands/runner/composer.go) |
| **`dbt`** | CLI for dbt - Data Build Tool | [`dbt.go`](commands/runner/dbt.go) |
| **`drush`** | Drush is a command line shell and Unix scripting interface for Drupal | [`drush.go`](commands/runner/drush.go) |
| **`elixir`** | Elixir Language | [`elixir.go`](commands/runner/elixir.go) |
| **`gem`** | Use HTTP proxy for remote operations | [`gem.go`](commands/runner/gem.go) |
| **`hexo`** | Draft for | [`hexo.go`](commands/runner/hexo.go) |
| **`just`** | command runner | [`justfile.go`](commands/runner/justfile.go) |
| **`laravel`** | The output format (txt, xml, json, or md) | [`laravel.go`](commands/runner/laravel.go) |
| **`magento`** | Open-source E-commerce | [`magento.go`](commands/runner/magento.go) |
| **`make`** | build automation | [`makefile.go`](commands/runner/makefile.go) |
| **`mix`** | Build tool for Elixir | [`mix.go`](commands/runner/mix.go) |
| **`php`** | Run the PHP interpreter | [`php.go`](commands/runner/php.go) |
| **`phpunit`** | Generate code coverage report in Clover XML format, | [`phpunit.go`](commands/runner/phpunit.go) |
| **`phpunit-watcher`** | Automatically rerun PHPUnit tests when source code changes | [`phpunit_watcher.go`](commands/runner/phpunit_watcher.go) |
| **`rails`** | Create a new rails application | [`rails.go`](commands/runner/rails.go) |
| **`rake`** | A ruby build program with capabilities similar to make | [`rake.go`](commands/runner/rake.go) |
| **`rubocop`** | Run only lint cops | [`rubocop.go`](commands/runner/rubocop.go) |
| **`ruby`** | Interpreted object-oriented scripting language | [`ruby.go`](commands/runner/ruby.go) |
| **`rvm`** | Show version of rvm | [`rvm.go`](commands/runner/rvm.go) |
| **`sidekiq`** | Background job framework for Ruby | [`sidekiq.go`](commands/runner/sidekiq.go) |
| **`symfony`** | Symfony Binary | [`symfony.go`](commands/runner/symfony.go) |
| **`valet`** | Do not output any message | [`valet.go`](commands/runner/valet.go) |
| **`vapor`** | Vapor Toolbox (Server-side Swift web framework) | [`vapor.go`](commands/runner/vapor.go) |

<a id="sys"></a>
### System Administration, Network & Process Management (`sys/`)

| Command | Description | Source File |
| :--- | :--- | :--- |
| **`adb`** | Forward-lock the app | [`adb.go`](commands/sys/adb.go) |
| **`ag`** | Recursively search for PATTERN in PATH. Like grep or ack, but faster | [`ag.go`](commands/sys/ag.go) |
| **`airflow`** | Subcommand | [`airflow.go`](commands/sys/airflow.go) |
| **`aliases`** | Prints help information | [`aliases.go`](commands/sys/aliases.go) |
| **`asciinema`** | Terminal session recorder | [`asciinema.go`](commands/sys/asciinema.go) |
| **`asr`** | Can be a disk image, /dev entry, or volume mountpoint | [`asr.go`](commands/sys/asr.go) |
| **`atuin`** | Magical shell history | [`atuin.go`](commands/sys/atuin.go) |
| **`basename`** | Return filename portion of pathname | [`basename.go`](commands/sys/basename.go) |
| **`bc`** | An arbitrary precision calculator language | [`bc.go`](commands/sys/bc.go) |
| **`btop`** | Beautifuler htop (interactive process viewer) | [`btop.go`](commands/sys/btop.go) |
| **`bundle`** | Gem | [`bundle.go`](commands/sys/bundle.go) |
| **`cal`** | Displays a calendar and the date of Easter | [`cal.go`](commands/sys/cal.go) |
| **`cci`** | CumulusCI command line interface | [`cci.go`](commands/sys/cci.go) |
| **`cdk8s`** | CDK for K8s | [`cdk8s.go`](commands/sys/cdk8s.go) |
| **`chezmoi`** | Attribute modifier | [`chezmoi.go`](commands/sys/chezmoi.go) |
| **`chsh`** | Change your login shell | [`chsh.go`](commands/sys/chsh.go) |
| **`codesign`** | Create and manipulate code signatures | [`codesign.go`](commands/sys/codesign.go) |
| **`croc`** | Send file(s), or folder | [`croc.go`](commands/sys/croc.go) |
| **`crontab`** | Maintain crontab file for individual users | [`crontab.go`](commands/sys/crontab.go) |
| **`curl`** | transfer data via URL | [`network.go`](commands/sys/network.go) |
| **`date`** | Display or set date and time | [`date.go`](commands/sys/date.go) |
| **`dateseq`** | Print help and exit | [`dateseq.go`](commands/sys/dateseq.go) |
| **`dcli`** | Display help for command | [`dcli.go`](commands/sys/dcli.go) |
| **`dd`** | The same as | [`dd.go`](commands/sys/dd.go) |
| **`ddev`** | DDEV-Local local development environment | [`ddev.go`](commands/sys/ddev.go) |
| **`defaults`** | Global domain | [`defaults.go`](commands/sys/defaults.go) |
| **`degit`** | Straightforward project scaffolding | [`degit.go`](commands/sys/degit.go) |
| **`deta`** | Runtime | [`deta.go`](commands/sys/deta.go) |
| **`dig`** | DNS lookup | [`network.go`](commands/sys/network.go) |
| **`dirname`** | Return directory portion of pathname | [`dirname.go`](commands/sys/dirname.go) |
| **`do-release-upgrade`** | Upgrade Ubuntu to latest release | [`do_release_upgrade.go`](commands/sys/do_release_upgrade.go) |
| **`dog`** | Human-readable host names, nameservers, types, or classes | [`dog.go`](commands/sys/dog.go) |
| **`dotnet`** | The dotnet cli | [`dotnet.go`](commands/sys/dotnet.go) |
| **`dscacheutil`** | Utility for managing the Directory Service cache | [`dscacheutil.go`](commands/sys/dscacheutil.go) |
| **`dscl`** | Prompt for password | [`dscl.go`](commands/sys/dscl.go) |
| **`dtm`** | Plugin | [`dtm.go`](commands/sys/dtm.go) |
| **`echo`** | Environment Variable | [`echo.go`](commands/sys/echo.go) |
| **`eleventy`** | Eleventy is a simpler static site generator | [`eleventy.go`](commands/sys/eleventy.go) |
| **`env`** | print environment | [`env.go`](commands/sys/env.go) |
| **`exec`** | Replace the current shell with a program | [`exec.go`](commands/sys/exec.go) |
| **`export`** | set environment variable | [`env.go`](commands/sys/env.go) |
| **`fastlane`** | Helps you with your initial fastlane setup | [`fastlane.go`](commands/sys/fastlane.go) |
| **`fdisk`** | Manipulate disk partition table | [`fdisk.go`](commands/sys/fdisk.go) |
| **`ffmpeg`** | Play, record, convert, and stream audio and video | [`ffmpeg.go`](commands/sys/ffmpeg.go) |
| **`firefox`** | Free open-source web browser developer by Mozilla | [`firefox.go`](commands/sys/firefox.go) |
| **`fisher`** | [Prompt] - 🌊 The ultimate Fish prompt | [`fisher.go`](commands/sys/fisher.go) |
| **`fmt`** | Simple text formatter | [`fmt.go`](commands/sys/fmt.go) |
| **`forc`** | Fuel Orchestrator | [`forc.go`](commands/sys/forc.go) |
| **`forge`** | A command line interface for managing Atlassian-hosted apps | [`forge.go`](commands/sys/forge.go) |
| **`fzf`** | A general-purpose command-line fuzzy finder | [`fzf.go`](commands/sys/fzf.go) |
| **`fzf-tmux`** | Opens a fuzzy finder in a tmux pane | [`fzf_tmux.go`](commands/sys/fzf_tmux.go) |
| **`gltfjsx`** | GLTF to JSX converter | [`gltfjsx.go`](commands/sys/gltfjsx.go) |
| **`goto`** | Goto | [`goto.go`](commands/sys/goto.go) |
| **`gum`** | Background Color | [`gum.go`](commands/sys/gum.go) |
| **`herd`** | Display this application version | [`herd.go`](commands/sys/herd.go) |
| **`hop`** | Interact with Hop in your terminal | [`hop.go`](commands/sys/hop.go) |
| **`hostname`** | Set or print name of current host system | [`hostname.go`](commands/sys/hostname.go) |
| **`htop`** | Improved top (interactive process viewer) | [`htop.go`](commands/sys/htop.go) |
| **`http`** | HTTPie: command-line HTTP client for the API era | [`http.go`](commands/sys/http.go) |
| **`hyper`** | Hyper is an Electron-based terminal | [`hyper.go`](commands/sys/hyper.go) |
| **`hyperfine`** | A command-line benchmarking tool | [`hyperfine.go`](commands/sys/hyperfine.go) |
| **`ibus`** | Set or get engine | [`ibus.go`](commands/sys/ibus.go) |
| **`id`** | Display the full name of the user | [`id.go`](commands/sys/id.go) |
| **`ifconfig`** | configure network interface | [`network.go`](commands/sys/network.go) |
| **`ignite-cli`** | Output usage information | [`ignite_cli.go`](commands/sys/ignite_cli.go) |
| **`install`** | Use suffix as the backup suffix if -b is given | [`install.go`](commands/sys/install.go) |
| **`ip`** | show/manage network | [`network.go`](commands/sys/network.go) |
| **`join`** | The join utility performs an | [`join.go`](commands/sys/join.go) |
| **`julia`** | The Julia Programming Language | [`julia.go`](commands/sys/julia.go) |
| **`kafkactl`** | Command-line interface for Apache Kafka | [`kafkactl.go`](commands/sys/kafkactl.go) |
| **`kamal`** | Skip image build and push | [`kamal.go`](commands/sys/kamal.go) |
| **`kill`** | send signal to process | [`ps.go`](commands/sys/ps.go) |
| **`killall`** | kill by process name | [`ps.go`](commands/sys/ps.go) |
| **`kitty`** | A cat like utility to display images in the terminal | [`kitty.go`](commands/sys/kitty.go) |
| **`klist`** | Credential cache to list | [`klist.go`](commands/sys/klist.go) |
| **`kool`** | Script | [`kool.go`](commands/sys/kool.go) |
| **`launchctl`** | Service or domain target | [`launchctl.go`](commands/sys/launchctl.go) |
| **`leaf`** | Create and interact with your leaf projects | [`leaf.go`](commands/sys/leaf.go) |
| **`lima`** | Lima is an alias for | [`lima.go`](commands/sys/lima.go) |
| **`login`** | Begin session on the system | [`login.go`](commands/sys/login.go) |
| **`lsblk`** | List block devices | [`lsblk.go`](commands/sys/lsblk.go) |
| **`lsof`** | List open files | [`lsof.go`](commands/sys/lsof.go) |
| **`man`** | Format and display manual pages | [`man.go`](commands/sys/man.go) |
| **`meroxa`** | The Meroxa CLI | [`meroxa.go`](commands/sys/meroxa.go) |
| **`mkdocs`** | Project documentation with Markdown | [`mkdocs.go`](commands/sys/mkdocs.go) |
| **`mkfifo`** | Make FIFOs (first-in, first-out) | [`mkfifo.go`](commands/sys/mkfifo.go) |
| **`mkinitcpio`** | Create an initial ramdisk environment | [`mkinitcpio.go`](commands/sys/mkinitcpio.go) |
| **`mknod`** | Create device special file | [`mknod.go`](commands/sys/mknod.go) |
| **`mosh`** | Address of remote machine to log into | [`mosh.go`](commands/sys/mosh.go) |
| **`mount`** | Mount disks and manage subtrees | [`mount.go`](commands/sys/mount.go) |
| **`nc`** | netcat - TCP/UDP tool | [`network.go`](commands/sys/network.go) |
| **`ncal`** | Displays a calendar and the date of Easter | [`ncal.go`](commands/sys/ncal.go) |
| **`neofetch`** | The most complete system information CLI tool | [`neofetch.go`](commands/sys/neofetch.go) |
| **`netstat`** | network statistics | [`network.go`](commands/sys/network.go) |
| **`networkQuality`** | Measure the different aspects of network quality | [`networkquality.go`](commands/sys/networkquality.go) |
| **`networksetup`** | Configuration tool for network settings in macOS | [`networksetup.go`](commands/sys/networksetup.go) |
| **`nextflow`** | Session ID | [`nextflow.go`](commands/sys/nextflow.go) |
| **`nhost`** | Nhost | [`nhost.go`](commands/sys/nhost.go) |
| **`nmap`** | Network exploration tool and security / port scanner | [`nmap.go`](commands/sys/nmap.go) |
| **`nrm`** | Use the right package manage - remove | [`nrm.go`](commands/sys/nrm.go) |
| **`ns`** | Forces rebuilding the native application | [`ns.go`](commands/sys/ns.go) |
| **`nslookup`** | query DNS | [`network.go`](commands/sys/network.go) |
| **`nylas`** | A command line interface for Nylas | [`nylas.go`](commands/sys/nylas.go) |
| **`oh-my-posh`** | The config file to use | [`oh_my_posh.go`](commands/sys/oh_my_posh.go) |
| **`okta`** | The Okta CLI is the easiest way to get started with Okta! | [`okta.go`](commands/sys/okta.go) |
| **`ollama`** | A command-line tool for managing and deploying machine learning models | [`ollama.go`](commands/sys/ollama.go) |
| **`omz`** | Oh My Zsh | [`omz.go`](commands/sys/omz.go) |
| **`pac`** | 7 | [`pac.go`](commands/sys/pac.go) |
| **`passwd`** | Modify a user | [`passwd.go`](commands/sys/passwd.go) |
| **`pathchk`** | Check pathnames for POSIX portability | [`pathchk.go`](commands/sys/pathchk.go) |
| **`pdfunite`** | Combine multiple pdfs | [`pdfunite.go`](commands/sys/pdfunite.go) |
| **`pgrep`** | find process by pattern | [`ps.go`](commands/sys/ps.go) |
| **`ping`** | test network connectivity | [`network.go`](commands/sys/network.go) |
| **`pkg-config`** | Return metainformation about installed libraries | [`pkg_config.go`](commands/sys/pkg_config.go) |
| **`pkill`** | kill by pattern | [`ps.go`](commands/sys/ps.go) |
| **`pmset`** | Display sleep timer (value in minutes, or 0 to disable) | [`pmset.go`](commands/sys/pmset.go) |
| **`pocketbase`** | PocketBase CLI | [`pocketbase.go`](commands/sys/pocketbase.go) |
| **`printenv`** | print environment variables | [`env.go`](commands/sys/env.go) |
| **`prisma`** | Display this help message | [`prisma.go`](commands/sys/prisma.go) |
| **`pro`** | Manage Ubuntu Pro services from Canonical | [`pro.go`](commands/sys/pro.go) |
| **`pry`** | Interactive Ruby | [`pry.go`](commands/sys/pry.go) |
| **`ps`** | report processes | [`ps.go`](commands/sys/ps.go) |
| **`publish`** | Set up a new website in the current folder | [`publish.go`](commands/sys/publish.go) |
| **`pwd`** | Return working directory name | [`pwd.go`](commands/sys/pwd.go) |
| **`rancher`** | Output format: | [`rancher.go`](commands/sys/rancher.go) |
| **`repeat`** | Interpret the result as a number and repeat the commands this many times | [`repeat.go`](commands/sys/repeat.go) |
| **`rscript`** | Scripting Front-End for R | [`rscript.go`](commands/sys/rscript.go) |
| **`sam`** | Host of locally emulated Lambda container | [`sam.go`](commands/sys/sam.go) |
| **`sanity`** | Displays help information about Sanity | [`sanity.go`](commands/sys/sanity.go) |
| **`screen`** | Screen manager with VT100/ANSI terminal emulation | [`screen.go`](commands/sys/screen.go) |
| **`shell-config`** | Display help for command | [`shell_config.go`](commands/sys/shell_config.go) |
| **`shortcuts`** | Run a shortcut | [`shortcuts.go`](commands/sys/shortcuts.go) |
| **`simctl`** | Add photos, live photos, videos, or contacts to the library of a device | [`simctl.go`](commands/sys/simctl.go) |
| **`source`** | Source files in shell | [`source.go`](commands/sys/source.go) |
| **`speedtest-cli`** | Command line interface for testing internet bandwidth using speedtest.net | [`speedtest_cli.go`](commands/sys/speedtest_cli.go) |
| **`spotify`** | CLI to use Spotify from the terminal | [`spotify.go`](commands/sys/spotify.go) |
| **`ss`** | socket statistics | [`network.go`](commands/sys/network.go) |
| **`st2`** | Show this help and exit | [`st2.go`](commands/sys/st2.go) |
| **`stack`** | The Haskell Tool Stack | [`stack.go`](commands/sys/stack.go) |
| **`starkli`** | Starkli, a ⚡ blazing ⚡ fast ⚡ CLI tool for Starknet powered by 🦀 starknet-rs 🦀 | [`starkli.go`](commands/sys/starkli.go) |
| **`su`** | (no letter) The same as -l | [`su.go`](commands/sys/su.go) |
| **`sudo`** | Execute a command as the superuser or another user | [`sudo.go`](commands/sys/sudo.go) |
| **`sysctl`** | Variable name | [`sysctl.go`](commands/sys/sysctl.go) |
| **`systemctl`** | Control the systemd system and service manager | [`systemctl.go`](commands/sys/systemctl.go) |
| **`tac`** | Concatenate and print files in reverse | [`tac.go`](commands/sys/tac.go) |
| **`tailcall`** | TailCall CLI for managing and optimizing GraphQL configurations | [`tailcall.go`](commands/sys/tailcall.go) |
| **`tailwindcss`** | Display usage information | [`tailwindcss.go`](commands/sys/tailwindcss.go) |
| **`time`** | Time how long a command takes! | [`time.go`](commands/sys/time.go) |
| **`tldr`** | Tldr page | [`tldr.go`](commands/sys/tldr.go) |
| **`tmux`** | Format output | [`tmux.go`](commands/sys/tmux.go) |
| **`tmuxinator`** | Project | [`tmuxinator.go`](commands/sys/tmuxinator.go) |
| **`top`** | Display Linux tasks | [`top.go`](commands/sys/top.go) |
| **`traceroute`** | Print the route packets take to network host | [`traceroute.go`](commands/sys/traceroute.go) |
| **`trap`** | Prints all defined signal handlers | [`trap.go`](commands/sys/trap.go) |
| **`trex`** | trex script | [`trex.go`](commands/sys/trex.go) |
| **`tsh`** | Remote host login | [`tsh.go`](commands/sys/tsh.go) |
| **`tuist`** | Build the project in the current directory | [`tuist.go`](commands/sys/tuist.go) |
| **`twilio`** | Level of logging messages | [`twilio.go`](commands/sys/twilio.go) |
| **`uname`** | Print operating system name | [`uname.go`](commands/sys/uname.go) |
| **`unset`** | unset variable | [`env.go`](commands/sys/env.go) |
| **`visudo`** | Checking existing sudoers file for syntax errors | [`visudo.go`](commands/sys/visudo.go) |
| **`vultr-cli`** | Bare Metal ID | [`vultr_cli.go`](commands/sys/vultr_cli.go) |
| **`wezterm`** | Wez | [`wezterm.go`](commands/sys/wezterm.go) |
| **`wget`** | non-interactive downloader | [`network.go`](commands/sys/network.go) |
| **`where`** | For each name, indicate how it should be interpreted | [`where.go`](commands/sys/where.go) |
| **`whereis`** | Locate the binary, source, and manual page files for a command | [`whereis.go`](commands/sys/whereis.go) |
| **`which`** | Executable file | [`which.go`](commands/sys/which.go) |
| **`who`** | Display who is logged in | [`who.go`](commands/sys/who.go) |
| **`wing`** | Runs a Wing executable in the Wing Console | [`wing.go`](commands/sys/wing.go) |
| **`wp`** | Path to the WordPress files | [`wp.go`](commands/sys/wp.go) |
| **`wrk`** | Wrk - a HTTP benchmarking tool | [`wrk.go`](commands/sys/wrk.go) |
| **`wscat`** | Communicate over websocket | [`wscat.go`](commands/sys/wscat.go) |
| **`yank`** | Yank terminal output to clipboard | [`yank.go`](commands/sys/yank.go) |
| **`ykman`** | Configure your YubiKey via the command line | [`ykman.go`](commands/sys/ykman.go) |
| **`zapier`** | Change the way structured data is presented. If | [`zapier.go`](commands/sys/zapier.go) |

<!-- END GENERATED COMMANDS -->

## License

The project is distributed under the [0BSD license](LICENSE). Third-party
copyright and license notices are retained in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
