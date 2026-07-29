package root

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [bash|zsh|fish]",
	Short: "Generate the autostart script for your shell",
	Long: `Add the output of this command to your shell's configuration file to start Vuja automatically.
For example, add this to your ~/.zshrc:
  eval "$(vuja init zsh)"`,
	ValidArgs: []string{"bash", "zsh", "fish"},
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	Run: func(cmd *cobra.Command, args []string) {
		shell := args[0]
		switch shell {
		case "zsh":
			fmt.Printf(`
# Vuja Autostart Hook
if [ -n "$TMUX" ] && [ -n "$VUJA_PID" ]; then
    if ps -o comm= -p $PPID 2>/dev/null | grep -q "tmux"; then
        unset VUJA_PID VUJA_IS_CHILD VUJA_FD
    fi
fi

if [ -z "$VUJA_PID" ]; then
    export VUJA_ACTIVE_SHELL="zsh"
    exec vuja
fi

# Vuja Autocomplete Hook
if [ -n "$VUJA_PID" ] && [ -n "$VUJA_FD" ]; then
  _vuja_send_lbuffer() {
    print -u $VUJA_FD -N -r -- "$LBUFFER" 2>/dev/null
  }

  _vuja_precmd() {
    print -u $VUJA_FD -N -r -- "VUJA_CMD_STOP" 2>/dev/null
  }

  _vuja_preexec() {
    print -u $VUJA_FD -N -r -- "VUJA_CMD_START" 2>/dev/null
  }

  autoload -Uz add-zle-hook-widget
  autoload -Uz add-zsh-hook

  add-zle-hook-widget line-pre-redraw _vuja_send_lbuffer
  add-zsh-hook precmd _vuja_precmd
  add-zsh-hook preexec _vuja_preexec
fi
`)
		case "bash":
			fmt.Printf(`
# Vuja Autostart Hook
if [ -n "$TMUX" ] && [ -n "$VUJA_PID" ]; then
    if ps -o comm= -p $PPID 2>/dev/null | grep -q "tmux"; then
        unset VUJA_PID VUJA_IS_CHILD VUJA_FD
    fi
fi

if [ -z "$VUJA_PID" ]; then
    export VUJA_ACTIVE_SHELL="bash"
    exec vuja
fi
`)
		case "fish":
			fmt.Printf(`
# Vuja Autostart Hook
if set -q TMUX; and set -q VUJA_PID
    if ps -o comm= -p $PPID 2>/dev/null | grep -q "tmux"
        set -e VUJA_PID
        set -e VUJA_IS_CHILD
        set -e VUJA_FD
    end
end

if not set -q VUJA_PID
    set -gx VUJA_ACTIVE_SHELL "fish"
    exec vuja
end
`)
		}
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(setupCmd)
}

var setupCmd = &cobra.Command{
	Use:   "setup [shell]",
	Short: "Automatically setup vuja shell integration and install binary",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		home, _ := os.UserHomeDir()

		localBin := filepath.Join(home, ".local", "bin")
		_ = os.MkdirAll(localBin, 0755)

		exe, _ := os.Executable()
		targetExe := filepath.Join(localBin, "vuja")

		fmt.Printf("Installing vuja to %s...\n", targetExe)
		input, err := os.ReadFile(exe)
		if err != nil {
			fmt.Printf("Failed to read current executable: %v\n", err)
			return
		}

		_ = os.Remove(targetExe)
		err = os.WriteFile(targetExe, input, 0755)
		if err != nil {
			fmt.Printf("Failed to write to %s: %v\n", targetExe, err)
			return
		}

		var shellName string
		if len(args) > 0 {
			shellName = args[0]
		} else {
			shellPath := os.Getenv("SHELL")
			shellName = filepath.Base(shellPath)
		}
		var configFile string
		var evalCmd string

		switch shellName {
		case "zsh":
			configFile = filepath.Join(shell.GetZshConfigDir(), ".zshrc")
			evalCmd = `eval "$(vuja init zsh)"`
		case "bash":
			configFile = filepath.Join(home, ".bashrc")
			evalCmd = `eval "$(vuja init bash)"`
		case "fish":
			configFile = filepath.Join(shell.GetFishConfigDir(), "config.fish")
			evalCmd = `vuja init fish | source`
		default:
			fmt.Printf("Unsupported shell: %s. Please add vuja init manually.\n", shellName)
			return
		}

		content, _ := os.ReadFile(configFile)
		if strings.Contains(string(content), "vuja init") {
			fmt.Printf("Vuja is already configured in %s\n", configFile)
		} else {
			f, err := os.OpenFile(configFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
			if err != nil {
				fmt.Printf("Failed to update %s: %v\n", configFile, err)
				return
			}
			defer func() { _ = f.Close() }()

			_, _ = f.WriteString("\n# Vuja Autocomplete\n" + evalCmd + "\n")
			fmt.Printf("✓ Added vuja integration to %s\n", configFile)
		}

		// initialize default config file if it does not exist
		if path, err := config.ConfigPath(); err == nil {
			if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
				_ = os.MkdirAll(filepath.Dir(path), 0755)
				if errWrite := os.WriteFile(path, []byte(defaultConfigContent), 0644); errWrite == nil {
					fmt.Printf("✓ Initialized default config file at %s\n", path)
				}
			}
		}

		fmt.Println("\nSetup complete! Please restart your terminal or run:")
		fmt.Printf("  \033[32msource %s\033[0m\n", configFile)
	},
}
