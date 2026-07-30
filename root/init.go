package root

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

const shellIntegrationComment = "# Vuja Autocomplete"

var initCmd = &cobra.Command{
	Use:   "init [bash|zsh|fish]",
	Short: "Generate the autostart script for your shell",
	Long: `Add the output of this command to your shell's configuration file to start Vuja automatically.
For example, add this to your ~/.zshrc:
  eval "$(vuja init zsh)"`,
	ValidArgs: []string{"bash", "zsh", "fish"},
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Print(shellInitScript(args[0], "vuja"))
	},
}

func shellInitScript(shellName, binaryPath string) string {
	switch shellName {
	case "zsh":
		return fmt.Sprintf(`
# Vuja Autostart Hook
if [ -n "$TMUX" ] && [ -n "$VUJA_PID" ]; then
    if ps -o comm= -p $PPID 2>/dev/null | grep -q "tmux"; then
        unset VUJA_PID VUJA_IS_CHILD VUJA_FD
    fi
fi

if [ -z "$VUJA_PID" ]; then
    export VUJA_ACTIVE_SHELL="zsh"
    exec %q
fi

# Vuja Autocomplete Hook
if [ -n "$VUJA_PID" ] && [ -n "$VUJA_FD" ]; then
  _vuja_send_lbuffer() {
    print -u $VUJA_FD -N -r -- "$LBUFFER" 2>/dev/null
  }

  _vuja_precmd() {
    local _vuja_exit_code=$?
    print -u $VUJA_FD -N -r -- "VUJA_CWD:$PWD" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_CMD_STOP:${_vuja_exit_code}" 2>/dev/null
    return $_vuja_exit_code
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
`, binaryPath)
	case "bash":
		return fmt.Sprintf(`
# Vuja Autostart Hook
if [ -n "$TMUX" ] && [ -n "$VUJA_PID" ]; then
    if ps -o comm= -p $PPID 2>/dev/null | grep -q "tmux"; then
        unset VUJA_PID VUJA_IS_CHILD VUJA_FD
    fi
fi

if [ -z "$VUJA_PID" ]; then
    export VUJA_ACTIVE_SHELL="bash"
    exec %q
fi

# Vuja Autocomplete Hook
if [ -n "$VUJA_PID" ] && [ -n "$VUJA_FD" ]; then
  _vuja_preexec() {
    printf 'VUJA_CMD_START\0' >&"$VUJA_FD" 2>/dev/null
  }

  _vuja_precmd() {
    local _vuja_exit_code=$?
    printf 'VUJA_CWD:%%s\0' "$PWD" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_CMD_STOP:%%s\0' "$_vuja_exit_code" >&"$VUJA_FD" 2>/dev/null
    return "$_vuja_exit_code"
  }

  PS0='$(_vuja_preexec)'"${PS0-}"
  if declare -p PROMPT_COMMAND 2>/dev/null | grep -q 'declare -a'; then
    PROMPT_COMMAND=(_vuja_precmd "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_vuja_precmd${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi
`, binaryPath)
	case "fish":
		return fmt.Sprintf(`
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
    exec %q
end

# Vuja Autocomplete Hook
if set -q VUJA_PID; and set -q VUJA_FD
    function _vuja_preexec --on-event fish_preexec
        printf 'VUJA_CMD_START\0' >&$VUJA_FD 2>/dev/null
    end

    function _vuja_postexec --on-event fish_postexec
        set -l _vuja_exit_code $status
        printf 'VUJA_CWD:%%s\0' "$PWD" >&$VUJA_FD 2>/dev/null
        printf 'VUJA_CMD_STOP:%%s\0' "$_vuja_exit_code" >&$VUJA_FD 2>/dev/null
        return $_vuja_exit_code
    end
end
`, binaryPath)
	default:
		return ""
	}
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

		switch shellName {
		case "zsh":
			configFile = filepath.Join(shell.GetZshConfigDir(), ".zshrc")
		case "bash":
			configFile = filepath.Join(home, ".bashrc")
		case "fish":
			configFile = filepath.Join(shell.GetFishConfigDir(), "config.fish")
		default:
			fmt.Printf("Unsupported shell: %s. Please add vuja init manually.\n", shellName)
			return
		}

		integrationFile := filepath.Join(home, ".local", "share", "vuja", "init."+shellName)
		if writeErr := writeShellIntegration(integrationFile, shellName, targetExe); writeErr != nil {
			fmt.Printf("Failed to write shell integration to %s: %v\n", integrationFile, writeErr)
			return
		}

		changed, err := configureShellIntegration(configFile, shellIntegration(shellName, integrationFile))
		if err != nil {
			fmt.Printf("Failed to update %s: %v\n", configFile, err)
			return
		}
		if changed {
			fmt.Printf("✓ Placed vuja integration first in %s\n", configFile)
		} else {
			fmt.Printf("Vuja is already configured first in %s\n", configFile)
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

func shellIntegration(shellName, integrationFile string) string {
	switch shellName {
	case "zsh", "bash":
		return `export PATH="$HOME/.local/bin:$PATH"` + "\n" +
			`source "` + integrationFile + `"`
	case "fish":
		return `set -gx PATH "$HOME/.local/bin" $PATH` + "\n" +
			`source "` + integrationFile + `"`
	default:
		return ""
	}
}

func writeShellIntegration(integrationFile, shellName, binaryPath string) error {
	if err := os.MkdirAll(filepath.Dir(integrationFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(integrationFile, []byte(shellInitScript(shellName, binaryPath)), 0644)
}

func configureShellIntegration(configFile, integration string) (bool, error) {
	content, err := os.ReadFile(configFile)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	mode := os.FileMode(0644)
	if info, statErr := os.Stat(configFile); statErr == nil {
		mode = info.Mode().Perm()
	}

	integrationLines := strings.Split(integration, "\n")
	initLine := integrationLines[len(integrationLines)-1]
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		legacyInit := strings.Contains(trimmed, "vuja init")
		if trimmed != initLine && !legacyInit {
			filtered = append(filtered, line)
			continue
		}
		for len(filtered) > 0 {
			previous := strings.TrimSpace(filtered[len(filtered)-1])
			if previous != shellIntegrationComment && !slices.Contains(integrationLines, previous) {
				break
			}
			filtered = filtered[:len(filtered)-1]
		}
	}

	remaining := strings.Trim(strings.Join(filtered, "\n"), "\n")
	updated := shellIntegrationComment + "\n" + integration + "\n"
	if remaining != "" {
		updated += "\n" + remaining + "\n"
	}
	if updated == string(content) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(configFile), 0755); err != nil {
		return false, err
	}
	if err := os.WriteFile(configFile, []byte(updated), mode); err != nil {
		return false, err
	}
	return true, nil
}
