package root

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

const defaultConfigContent = `# ~/.config/vuja/config.toml
# vuja configuration file

[core]
# schema version
# do not edit this field manually
version = 1

# override shell: "bash", "zsh", "fish", keep empty for auto detection
shell = ""

# startup mode: "last", "spec", "history"
# "last" = remember last mode used
mode = "last"

# enable debug logging
debug = false

[keybindings]
# Vuja actions; use "none" to disable a single binding
history-search = "ctrl+r"
history-success-only = "ctrl+s"
accept = "tab"
toggle-overlay = "shift+tab"

# one or more bindings; use [] to disable token acceptance
accept-token = ["alt+right", "ctrl+right", "meta+right"]

[ui]
# visual style: "modern" (icons, category pills, shortcut footer) or "classic" (minimalist, centered number, no icons)
style = "modern"

# prompt placement: "classic" (normal terminal flow) or "bottom" (persistent chatbox at the bottom edge)
prompt-position = "classic"

# enable Nerd Fonts icons in overlay menu
nerd-fonts = true

# enable inline ghost text
ghost-text = true

# maximum suggestions to display
max-suggestions = 100

# maximum height of the overlay
max-height = 15

[ui.chatbox]
# plain-text prompt used by Vuja-managed bottom sessions
prompt = "› "

# fixed separator between status segments
separator = " · "

# completed command scrollback: "output" (raw output only) or "snapshot" (title, command, status, and output)
scrollback = "output"

# path colors: "hierarchy" (per-segment progression) or "single"
path-color-mode = "hierarchy"

# maximum visible path segments; 0 disables segment-based shortening
path-max-segments = 6

# blank rows between completed executions
history-spacing = 1

# finite built-in title and status regions; empty all three arrays to hide a bar
title-left = ["directory"]
title-center = []
title-right = ["package", "versions"]
status-left = ["session", "git-branch", "git-status", "git-added", "git-deleted", "git-stash", "git-lines", "environment", "version-mismatch", "contexts", "stale"]
status-center = []
status-right = ["jobs", "duration", "exit", "cpu", "memory"]

# bounded refresh interval for enabled system metrics
refresh-interval = "1s"

# combine relevant repository tool versions into one responsive group
collapse-versions = true

[ui.chatbox.colors]
directory = "#61ffcf"
directory-root = "#739ee8"
directory-read-only = "#ff7b72"
git-branch = "#fd7df4"
git-status = "#f3c969"
git-clean = "#61ffcf"
git-operation = "#fd7df4"
git-conflicts = "#ff7b72"
git-staged = "#61ffcf"
git-modified = "#f3c969"
git-renamed = "#739ee8"
git-untracked = "#caa472"
git-ahead = "#00add8"
git-behind = "#ce422b"
git-added = "#61ffcf"
git-deleted = "#ff7b72"
git-stash = "#caa472"
git-lines-added = "#61ffcf"
git-lines-deleted = "#ff7b72"
session = "#739ee8"
session-warning = "#f3c969"
session-critical = "#ff7b72"
contexts = "#2496ed"
environment = "#68a063"
version-mismatch = "#ff7b72"
package = "#caa472"
stale = "#f3c969"
jobs = "#739ee8"
jobs-stopped = "#f3c969"
exit-neutral = "#747579"
exit-success = "#61ffcf"
exit-failure = "#ff7b72"
duration = "#c6cad7"
duration-fast = "#61ffcf"
duration-average = "#f3c969"
duration-slow = "#ff7b72"
load-low = "#61ffcf"
load-average = "#f3c969"
load-high = "#ce422b"
load-critical = "#ff7b72"
laravel = "#ff2d20"
php = "#777bb4"
composer = "#caa472"
python = "#3776ab"
ruby = "#cc342d"
elixir = "#6e4a7e"
go = "#00add8"
node = "#68a063"
bun = "#f6e8d5"
rust = "#ce422b"
docker = "#2496ed"
docker-compose = "#5b8def"
cpu = "#c6cad7"
memory = "#c6cad7"

# Serein Day colors used on light terminal backgrounds
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
surface-background = "#e9e4de"
completed-surface-background = "#f1ede8"
status-background = "#f8f5f1"
status-text = "#242529"
scroll-info = "#984ea5"
ghost-text = "#747579"

# Serein Night colors used on dark terminal backgrounds
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
surface-background = "#242528"
completed-surface-background = "#17191d"
status-background = "#080a0d"
status-text = "#c6cad7"
scroll-info = "#fd7df4"
ghost-text = "#404658"

[git]
# hide current branch in checkout/switch list
filter-active-branch = true

# merge remote and local branches with same name
deduplicate-branches = true

[updater]
# check for updates on startup
check-on-startup = true

# update channel: "stable", "nightly"
channel = "stable"

# interval between update checks, e.g. "24h", "6h", "30m"
check-interval = "24h"
`

var ConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "manage vuja configuration",
}

var ConfigInitCmd = &cobra.Command{
	Use:   "init",
	Short: "initialize default configuration file with comments",
	Run: func(cmd *cobra.Command, args []string) {
		path, err := config.ConfigPath()
		if err != nil {
			fmt.Printf("failed to get config path: %v\n", err)
			return
		}

		if _, statErr := os.Stat(path); statErr == nil {
			fmt.Printf("config file already exists at %s\n", path)
			return
		}

		_ = os.MkdirAll(filepath.Dir(path), 0755)

		err = os.WriteFile(path, []byte(defaultConfigContent), 0644)
		if err != nil {
			fmt.Printf("failed to write config file: %v\n", err)
			return
		}
		fmt.Printf("initialized config file at %s\n", path)
	},
}

var ConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "show the resolved configuration",
	Run: func(cmd *cobra.Command, args []string) {
		enc := toml.NewEncoder(cmd.OutOrStdout())
		if err := enc.Encode(config.Get()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to encode config: %v\n", err)
		}
	},
}

func init() {
	ConfigCmd.AddCommand(ConfigInitCmd)
	ConfigCmd.AddCommand(ConfigShowCmd)
	rootCmd.AddCommand(ConfigCmd)
}
