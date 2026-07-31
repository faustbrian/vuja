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
const shellIntegrationFinalizeComment = "# Vuja Prompt Finalization"

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
  if [[ -n $VUJA_MARKER ]]; then
    typeset +x VUJA_MARKER
    typeset -g POWERLEVEL9K_INSTANT_PROMPT=off
    _vuja_prompt_start=$'%%{\e]777;vuja;'${VUJA_MARKER}$';prompt-start\a%%}'
    _vuja_prompt_end=$'%%{\e]777;vuja;'${VUJA_MARKER}$';prompt-end\a%%}'
    _vuja_continuation_start=$'%%{\e]777;vuja;'${VUJA_MARKER}$';continuation-start\a%%}'
    _vuja_continuation_end=$'%%{\e]777;vuja;'${VUJA_MARKER}$';continuation-end\a%%}'
  fi

  _vuja_send_lbuffer() {
    print -u $VUJA_FD -N -r -- "$LBUFFER" 2>/dev/null
  }

  _vuja_unmark_prompts() {
    if [[ -n ${_vuja_prompt_start-} ]]; then
      PS1=${PS1//"$_vuja_prompt_start"/}
      PS1=${PS1//"$_vuja_prompt_end"/}
      PS2=${PS2//"$_vuja_continuation_start"/}
      PS2=${PS2//"$_vuja_continuation_end"/}
    fi
  }

  _vuja_apply_managed_prompt() {
    if [[ -n ${VUJA_MANAGED_PROMPT-} ]]; then
      PROMPT=${VUJA_PROMPT_TEXT-'› '}
      RPROMPT=
    fi
  }

  _vuja_precmd() {
    local _vuja_exit_code=$?
    local _vuja_stopped_jobs=0
    local _vuja_job_state
    for _vuja_job_state in ${(v)jobstates}; do
      [[ $_vuja_job_state == *suspended* ]] && (( _vuja_stopped_jobs++ ))
    done
    _vuja_unmark_prompts
    _vuja_apply_managed_prompt
    if [[ -n $VUJA_MARKER ]]; then
      PS1="${_vuja_prompt_start}${PS1}${_vuja_prompt_end}"
      PS2="${_vuja_continuation_start}${PS2}${_vuja_continuation_end}"
      builtin printf '\e]777;vuja;%%s;command-end:%%s\a' "$VUJA_MARKER" "$_vuja_exit_code"
    fi
    print -u $VUJA_FD -N -r -- "VUJA_JOBS:${#jobstates}:${_vuja_stopped_jobs}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:direnv:${DIRENV_FILE-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:virtualenv:${VIRTUAL_ENV-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:conda:${CONDA_DEFAULT_ENV-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:mise:${MISE_ENV:-${MISE_PROJECT_ROOT:-${MISE_SHELL-}}}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:nix:${IN_NIX_SHELL-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:aws-profile:${AWS_PROFILE:-${AWS_DEFAULT_PROFILE-}}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:aws-region:${AWS_REGION:-${AWS_DEFAULT_REGION-}}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:docker-context:${DOCKER_CONTEXT-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_ENV:kubeconfig:${KUBECONFIG-}" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_CWD:$PWD" 2>/dev/null
    print -u $VUJA_FD -N -r -- "VUJA_CMD_STOP:${_vuja_exit_code}" 2>/dev/null
    return $_vuja_exit_code
  }

  _vuja_preexec() {
    _vuja_unmark_prompts
    if [[ -n $VUJA_MARKER ]]; then
      builtin printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
    fi
    print -u $VUJA_FD -N -r -- "VUJA_CMD_START" 2>/dev/null
  }

  autoload -Uz add-zle-hook-widget
  autoload -Uz add-zsh-hook

  add-zle-hook-widget -d line-pre-redraw _vuja_send_lbuffer 2>/dev/null
  add-zsh-hook -d precmd _vuja_precmd 2>/dev/null
  add-zsh-hook -d preexec _vuja_preexec 2>/dev/null
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
  if [[ -n "$VUJA_MARKER" ]]; then
    export -n VUJA_MARKER
    _VUJA_PROMPT_START="\\[\e]777;vuja;${VUJA_MARKER};prompt-start\a\\]"
    _VUJA_PROMPT_END="\\[\e]777;vuja;${VUJA_MARKER};prompt-end\a\\]"
    _VUJA_CONTINUATION_START="\\[\e]777;vuja;${VUJA_MARKER};continuation-start\a\\]"
    _VUJA_CONTINUATION_END="\\[\e]777;vuja;${VUJA_MARKER};continuation-end\a\\]"
  fi

  _vuja_unmark_prompts() {
    if [[ -n "${_VUJA_PROMPT_START-}" ]]; then
      PS1=${PS1//$_VUJA_PROMPT_START/}
      PS1=${PS1//$_VUJA_PROMPT_END/}
      PS2=${PS2//$_VUJA_CONTINUATION_START/}
      PS2=${PS2//$_VUJA_CONTINUATION_END/}
    fi
  }

  _vuja_precmd() {
    local _vuja_exit_code=$?
    local -a _vuja_job_ids=($(jobs -p 2>/dev/null))
    local -a _vuja_stopped_job_ids=($(jobs -s -p 2>/dev/null))
    _vuja_unmark_prompts
    if [[ -n "$VUJA_MARKER" ]]; then
      PS1="${_VUJA_PROMPT_START}${PS1}${_VUJA_PROMPT_END}"
      PS2="${_VUJA_CONTINUATION_START}${PS2}${_VUJA_CONTINUATION_END}"
      printf '\e]777;vuja;%%s;command-end:%%s\a' "$VUJA_MARKER" "$_vuja_exit_code"
    fi
    printf 'VUJA_JOBS:%%s:%%s\0' "${#_vuja_job_ids[@]}" "${#_vuja_stopped_job_ids[@]}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:direnv:%%s\0' "${DIRENV_FILE-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:virtualenv:%%s\0' "${VIRTUAL_ENV-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:conda:%%s\0' "${CONDA_DEFAULT_ENV-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:mise:%%s\0' "${MISE_ENV:-${MISE_PROJECT_ROOT:-${MISE_SHELL-}}}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:nix:%%s\0' "${IN_NIX_SHELL-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:aws-profile:%%s\0' "${AWS_PROFILE:-${AWS_DEFAULT_PROFILE-}}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:aws-region:%%s\0' "${AWS_REGION:-${AWS_DEFAULT_REGION-}}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:docker-context:%%s\0' "${DOCKER_CONTEXT-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_ENV:kubeconfig:%%s\0' "${KUBECONFIG-}" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_CWD:%%s\0' "$PWD" >&"$VUJA_FD" 2>/dev/null
    printf 'VUJA_CMD_STOP:%%s\0' "$_vuja_exit_code" >&"$VUJA_FD" 2>/dev/null
    return "$_vuja_exit_code"
  }

  _vuja_preexec() {
    _vuja_unmark_prompts
    if [[ -n "$VUJA_MARKER" ]]; then
      printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
    fi
    printf 'VUJA_CMD_START\0' >&"$VUJA_FD" 2>/dev/null
  }

  if [[ "${PS0-}" != *'$(_vuja_preexec)'* ]]; then
    PS0='$(_vuja_preexec)'"${PS0-}"
  fi
  _vuja_without_precmd() {
    local _vuja_value=$1
    if [[ "$_vuja_value" == "_vuja_precmd" ]]; then
      return
    fi
    _vuja_value=${_vuja_value//;_vuja_precmd;/;}
    _vuja_value=${_vuja_value#_vuja_precmd;}
    _vuja_value=${_vuja_value%%;_vuja_precmd}
    printf '%%s' "$_vuja_value"
  }
  if [[ -n "${STARSHIP_PROMPT_COMMAND-}" ]]; then
    STARSHIP_PROMPT_COMMAND=$(_vuja_without_precmd "$STARSHIP_PROMPT_COMMAND")
  fi
  if declare -p PROMPT_COMMAND 2>/dev/null | grep -q 'declare -a'; then
    _vuja_prompt_commands=()
    for _vuja_prompt_command in "${PROMPT_COMMAND[@]}"; do
      if [[ "$_vuja_prompt_command" != "_vuja_precmd" ]]; then
        _vuja_prompt_commands+=("$_vuja_prompt_command")
      fi
    done
    PROMPT_COMMAND=("${_vuja_prompt_commands[@]}" _vuja_precmd)
    unset _vuja_prompt_commands _vuja_prompt_command
  else
    PROMPT_COMMAND=$(_vuja_without_precmd "${PROMPT_COMMAND-}")
    PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND;}_vuja_precmd"
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
    if set -q VUJA_MARKER
        set --unexport VUJA_MARKER
    end

    if set -q _vuja_fish_markers_installed
        functions -e _vuja_prompt_start
        for _vuja_prompt_name in fish_mode_prompt fish_prompt fish_right_prompt
            set -l _vuja_original_name "_vuja_original_$_vuja_prompt_name"
            if functions -q "$_vuja_prompt_name"; and string match -q "*$_vuja_original_name*" (functions "$_vuja_prompt_name")
                if functions -q "$_vuja_original_name"
                    functions -e "$_vuja_prompt_name"
                    functions -c "$_vuja_original_name" "$_vuja_prompt_name"
                else
                    functions -e "$_vuja_prompt_name"
                end
            end
            functions -e "$_vuja_original_name"
        end
        set -e _vuja_fish_markers_installed
    end

    if set -q VUJA_MARKER; and test -n "$VUJA_MARKER"
        function _vuja_setup_prompt_markers --on-event fish_prompt
            printf '\e]777;vuja;%%s;prompt-start\a' "$VUJA_MARKER"
            functions -e _vuja_setup_prompt_markers

            function _vuja_prompt_start --on-event fish_prompt
                printf '\e]777;vuja;%%s;prompt-start\a' "$VUJA_MARKER"
            end

            if functions -q fish_mode_prompt
                functions -c fish_mode_prompt _vuja_original_fish_mode_prompt
            end
            if functions -q fish_prompt
                functions -c fish_prompt _vuja_original_fish_prompt
            end
            if functions -q fish_right_prompt
                functions -c fish_right_prompt _vuja_original_fish_right_prompt
            end

            function fish_mode_prompt
                if functions -q _vuja_original_fish_mode_prompt
                    _vuja_original_fish_mode_prompt $argv
                end
            end

            function fish_prompt
                if functions -q _vuja_original_fish_prompt
                    _vuja_original_fish_prompt $argv
                end
            end

            function fish_right_prompt
                if functions -q _vuja_original_fish_right_prompt
                    _vuja_original_fish_right_prompt $argv
                end
                printf '\e]777;vuja;%%s;prompt-end\a' "$VUJA_MARKER"
            end
            set -g _vuja_fish_markers_installed 1
        end
    end

    function _vuja_publish_shell_status
        set -l _vuja_jobs (count (jobs -p 2>/dev/null))
        set -l _vuja_stopped_jobs (count (jobs 2>/dev/null | string match -r 'stopped'))
        printf 'VUJA_JOBS:%%s:%%s\0' "$_vuja_jobs" "$_vuja_stopped_jobs" >&$VUJA_FD 2>/dev/null
        set -q DIRENV_FILE; and printf 'VUJA_ENV:direnv:%%s\0' "$DIRENV_FILE" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:direnv:\0' >&$VUJA_FD 2>/dev/null
        set -q VIRTUAL_ENV; and printf 'VUJA_ENV:virtualenv:%%s\0' "$VIRTUAL_ENV" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:virtualenv:\0' >&$VUJA_FD 2>/dev/null
        set -q CONDA_DEFAULT_ENV; and printf 'VUJA_ENV:conda:%%s\0' "$CONDA_DEFAULT_ENV" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:conda:\0' >&$VUJA_FD 2>/dev/null
        set -q MISE_ENV; and printf 'VUJA_ENV:mise:%%s\0' "$MISE_ENV" >&$VUJA_FD 2>/dev/null; or if set -q MISE_PROJECT_ROOT; printf 'VUJA_ENV:mise:%%s\0' "$MISE_PROJECT_ROOT" >&$VUJA_FD 2>/dev/null; else if set -q MISE_SHELL; printf 'VUJA_ENV:mise:%%s\0' "$MISE_SHELL" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:mise:\0' >&$VUJA_FD 2>/dev/null; end; end
        set -q IN_NIX_SHELL; and printf 'VUJA_ENV:nix:%%s\0' "$IN_NIX_SHELL" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:nix:\0' >&$VUJA_FD 2>/dev/null
        set -q AWS_PROFILE; and printf 'VUJA_ENV:aws-profile:%%s\0' "$AWS_PROFILE" >&$VUJA_FD 2>/dev/null; or if set -q AWS_DEFAULT_PROFILE; printf 'VUJA_ENV:aws-profile:%%s\0' "$AWS_DEFAULT_PROFILE" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:aws-profile:\0' >&$VUJA_FD 2>/dev/null; end
        set -q AWS_REGION; and printf 'VUJA_ENV:aws-region:%%s\0' "$AWS_REGION" >&$VUJA_FD 2>/dev/null; or if set -q AWS_DEFAULT_REGION; printf 'VUJA_ENV:aws-region:%%s\0' "$AWS_DEFAULT_REGION" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:aws-region:\0' >&$VUJA_FD 2>/dev/null; end
        set -q DOCKER_CONTEXT; and printf 'VUJA_ENV:docker-context:%%s\0' "$DOCKER_CONTEXT" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:docker-context:\0' >&$VUJA_FD 2>/dev/null
        set -q KUBECONFIG; and printf 'VUJA_ENV:kubeconfig:%%s\0' "$KUBECONFIG" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:kubeconfig:\0' >&$VUJA_FD 2>/dev/null
        printf 'VUJA_CWD:%%s\0' "$PWD" >&$VUJA_FD 2>/dev/null
    end

    function _vuja_initial_shell_status --on-event fish_prompt
        _vuja_publish_shell_status
        functions -e _vuja_initial_shell_status
    end

    function _vuja_preexec --on-event fish_preexec
        if set -q VUJA_MARKER; and test -n "$VUJA_MARKER"
            printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
        end
        printf 'VUJA_CMD_START\0' >&$VUJA_FD 2>/dev/null
    end

    function _vuja_postexec --on-event fish_postexec
        set -l _vuja_exit_code $status
        if set -q VUJA_MARKER; and test -n "$VUJA_MARKER"
            printf '\e]777;vuja;%%s;command-end:%%s\a' "$VUJA_MARKER" "$_vuja_exit_code"
        end
        _vuja_publish_shell_status
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
			fmt.Printf("✓ Placed vuja bootstrap first and prompt finalization last in %s\n", configFile)
		} else {
			fmt.Printf("Vuja bootstrap and prompt finalization are already configured in %s\n", configFile)
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
			if previous != shellIntegrationComment &&
				previous != shellIntegrationFinalizeComment &&
				!slices.Contains(integrationLines, previous) {
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
	updated += "\n" + shellIntegrationFinalizeComment + "\n" + initLine + "\n"
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
