package root

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
    if [[ -n ${VUJA_MARKER-} && ${VUJA_MANAGED_PROMPT-} == ${VUJA_MARKER-} ]]; then
      PROMPT=${VUJA_PROMPT_TEXT-'› '}
      RPROMPT=
    fi
  }

  _vuja_publish_functions() {
    local -a _vuja_function_records
    local _vuja_function_name _vuja_function_source _vuja_function_record
    for _vuja_function_name _vuja_function_source in ${(kv)functions_source}; do
      _vuja_function_records+=("${_vuja_function_name}"$'\t'"${_vuja_function_source}")
    done
    _vuja_function_records=("${(@on)_vuja_function_records}")
    local _vuja_function_signature="${(j:\n:)_vuja_function_records}"
    [[ $_vuja_function_signature == ${_vuja_last_function_signature-} ]] && return
    typeset -g _vuja_last_function_signature=$_vuja_function_signature
    print -u $VUJA_FD -N -r -- "VUJA_FUNCTIONS_BEGIN" 2>/dev/null
    for _vuja_function_record in "${_vuja_function_records[@]}"; do
      print -u $VUJA_FD -N -r -- "VUJA_FUNCTION:${_vuja_function_record}" 2>/dev/null
    done
    print -u $VUJA_FD -N -r -- "VUJA_FUNCTIONS_END" 2>/dev/null
  }

  # Observe the complete native history-policy decision without asking user
  # hooks to run twice or changing their first-nonzero return semantics.
  if [[ -n ${_vuja_zsh_history_policy_wrapped-} ]]; then
    typeset -ga _vuja_resourced_zshaddhistory_functions=("${zshaddhistory_functions[@]}")
    if (( $+functions[zshaddhistory] && $+functions[_vuja_installed_zshaddhistory] )) &&
       [[ ${functions[zshaddhistory]} != ${functions[_vuja_installed_zshaddhistory]} ]]; then
      functions -c zshaddhistory _vuja_resourced_zshaddhistory
    fi
    unfunction zshaddhistory 2>/dev/null
    if (( $+functions[_vuja_resourced_zshaddhistory] )); then
      functions -c _vuja_resourced_zshaddhistory zshaddhistory
    elif (( $+functions[_vuja_original_zshaddhistory] )); then
      functions -c _vuja_original_zshaddhistory zshaddhistory
    fi
    unfunction _vuja_original_zshaddhistory _vuja_installed_zshaddhistory _vuja_resourced_zshaddhistory 2>/dev/null
    zshaddhistory_functions=("${_vuja_original_zshaddhistory_functions[@]}" "${_vuja_resourced_zshaddhistory_functions[@]}")
    zshaddhistory_functions=("${(u)zshaddhistory_functions[@]}")
    unset _vuja_original_zshaddhistory_functions _vuja_resourced_zshaddhistory_functions _vuja_zsh_history_policy_wrapped
  fi
  if (( $+functions[zshaddhistory] )); then
    functions -c zshaddhistory _vuja_original_zshaddhistory
  fi
  typeset -ga _vuja_original_zshaddhistory_functions=("${zshaddhistory_functions[@]}")
  unset zshaddhistory_functions
  zshaddhistory() {
    local _vuja_policy_status=0
    local _vuja_hook_status=0
    local _vuja_hook
    if (( ${#zshaddhistory_functions} )); then
      _vuja_original_zshaddhistory_functions+=("${zshaddhistory_functions[@]}")
      _vuja_original_zshaddhistory_functions=("${(u)_vuja_original_zshaddhistory_functions[@]}")
      unset zshaddhistory_functions
    fi
    if (( $+functions[_vuja_original_zshaddhistory] )); then
      _vuja_original_zshaddhistory "$@"
      _vuja_hook_status=$?
      (( _vuja_hook_status != 0 )) && _vuja_policy_status=$_vuja_hook_status
    fi
    if (( _vuja_policy_status == 0 )); then
      for _vuja_hook in "${_vuja_original_zshaddhistory_functions[@]}"; do
        (( $+functions[$_vuja_hook] )) || continue
        "$_vuja_hook" "$@"
        _vuja_hook_status=$?
        if (( _vuja_hook_status != 0 )); then
          _vuja_policy_status=$_vuja_hook_status
          break
        fi
      done
    fi
    typeset -g _vuja_history_policy_status=$_vuja_policy_status
    return $_vuja_policy_status
  }
  functions -c zshaddhistory _vuja_installed_zshaddhistory
  typeset -g _vuja_zsh_history_policy_wrapped=1
  typeset -g _vuja_history_policy_status=0

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
    _vuja_publish_functions
    print -u $VUJA_FD -N -r -- "VUJA_CMD_STOP:${_vuja_exit_code}" 2>/dev/null
    typeset -g _vuja_history_policy_status=0
    return $_vuja_exit_code
  }

  _vuja_preexec() {
    _vuja_unmark_prompts
    if [[ -n $VUJA_MARKER ]]; then
      builtin printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
    fi
    local _vuja_history_marker="VUJA_CMD_START"
    if [[ $1 == [[:space:]]* ]] || [[ -n ${HISTORY_IGNORE-} && $1 == ${~HISTORY_IGNORE} ]] || (( ${_vuja_history_policy_status:-0} != 0 )); then
      _vuja_history_marker="VUJA_CMD_START:IGNORE"
    fi
    print -u $VUJA_FD -N -r -- "$_vuja_history_marker" 2>/dev/null
	    if [[ -n ${VUJA_HISTORY_ACK_FD-} ]]; then
	      read -u "$VUJA_HISTORY_ACK_FD" -k 1 _vuja_history_ack 2>/dev/null
	    fi
	    typeset -g _vuja_history_recordable=${_vuja_history_ack:-0}
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

  _vuja_publish_functions() {
    local _vuja_restore_extdebug=0
    local _vuja_function_name _vuja_function_line _vuja_function_source _vuja_function_record
    local -a _vuja_function_records=()
    if ! shopt -q extdebug; then
      shopt -s extdebug
      _vuja_restore_extdebug=1
    fi
    while read -r _vuja_function_name _vuja_function_line _vuja_function_source; do
      _vuja_function_records+=("${_vuja_function_name}"$'\t'"${_vuja_function_source}")
    done < <(declare -F)
    (( _vuja_restore_extdebug )) && shopt -u extdebug
    local _vuja_function_signature
    printf -v _vuja_function_signature '%%s\n' "${_vuja_function_records[@]}"
    [[ $_vuja_function_signature == "${_vuja_last_function_signature-}" ]] && return
    _vuja_last_function_signature=$_vuja_function_signature
    printf 'VUJA_FUNCTIONS_BEGIN\0' >&"$VUJA_FD" 2>/dev/null
    for _vuja_function_record in "${_vuja_function_records[@]}"; do
      printf 'VUJA_FUNCTION:%%s\0' "$_vuja_function_record" >&"$VUJA_FD" 2>/dev/null
    done
    printf 'VUJA_FUNCTIONS_END\0' >&"$VUJA_FD" 2>/dev/null
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
    _vuja_publish_functions
    printf 'VUJA_CMD_STOP:%%s\0' "$_vuja_exit_code" >&"$VUJA_FD" 2>/dev/null
	    _vuja_previous_histcmd=${HISTCMD-}
    return "$_vuja_exit_code"
  }

  _vuja_preexec() {
    _vuja_unmark_prompts
    if [[ -n "$VUJA_MARKER" ]]; then
      printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
    fi
	    local _vuja_history_marker="VUJA_CMD_START"
	    # Bash applies HISTCONTROL and HISTIGNORE before PS0 is expanded. A
	    # recordable line advances HISTCMD; an ignored line does not. Observe
	    # that shell-owned decision instead of attempting to reimplement the
	    # patterns against only the current simple command.
	    if [[ -o history && ${HISTSIZE-0} != 0 && -n ${_vuja_previous_histcmd+x} && ${HISTCMD-} == ${_vuja_previous_histcmd-} ]]; then
	      _vuja_history_marker="VUJA_CMD_START:IGNORE"
	    fi
	    printf '%%s\0' "$_vuja_history_marker" >&"$VUJA_FD" 2>/dev/null
	    if [[ -n "${VUJA_HISTORY_ACK_FD-}" ]]; then
	      IFS= read -r -n 1 -u "$VUJA_HISTORY_ACK_FD" _vuja_history_ack 2>/dev/null
	    fi
	    _vuja_history_recordable=${_vuja_history_ack:-0}
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
        set --unexport VUJA_MARKER "$VUJA_MARKER"
    end

    if set -q _vuja_fish_history_policy_wrapped
        if functions -q fish_should_add_to_history; and functions -q _vuja_installed_fish_should_add_to_history
            # The functions builtin includes the function name in its first line. Normalize
            # copied names before comparing definitions so merely re-sourcing the
            # unchanged Vuja wrapper cannot be mistaken for a user replacement.
            set -l _vuja_current_fish_history_policy (functions fish_should_add_to_history | string match -rv '^# Defined ' | string replace -r '^function [^ ]+' 'function' | string collect)
            set -l _vuja_installed_fish_history_policy (functions _vuja_installed_fish_should_add_to_history | string match -rv '^# Defined ' | string replace -r '^function [^ ]+' 'function' | string collect)
            if test "$_vuja_current_fish_history_policy" != "$_vuja_installed_fish_history_policy"
                functions -c fish_should_add_to_history _vuja_resourced_fish_should_add_to_history
            end
        end
        functions -e fish_should_add_to_history
        if functions -q _vuja_resourced_fish_should_add_to_history
            functions -c _vuja_resourced_fish_should_add_to_history fish_should_add_to_history
        else if functions -q _vuja_original_fish_should_add_to_history
            functions -c _vuja_original_fish_should_add_to_history fish_should_add_to_history
        end
        functions -e _vuja_original_fish_should_add_to_history
        functions -e _vuja_installed_fish_should_add_to_history
        functions -e _vuja_resourced_fish_should_add_to_history
        set -e _vuja_fish_history_policy_wrapped
    end

    if functions -q fish_should_add_to_history
        functions -c fish_should_add_to_history _vuja_original_fish_should_add_to_history
    end
    function fish_should_add_to_history
        set -l _vuja_policy_status 0
        if functions -q _vuja_original_fish_should_add_to_history
            _vuja_original_fish_should_add_to_history $argv
            set _vuja_policy_status $status
        else if string match -qr '^[[:space:]]' -- "$argv[1]"
            set _vuja_policy_status 1
        end
        set -g _vuja_history_policy_status $_vuja_policy_status
        return $_vuja_policy_status
    end
    functions -c fish_should_add_to_history _vuja_installed_fish_should_add_to_history
    set -g _vuja_fish_history_policy_wrapped 1
    set -g _vuja_history_policy_status 0

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
		set -g _vuja_history_policy_status 0
        set -l _vuja_jobs (count (jobs -p 2>/dev/null))
        set -l _vuja_stopped_jobs (count (jobs 2>/dev/null | string match -r 'stopped'))
        printf 'VUJA_JOBS:%%s:%%s\0' "$_vuja_jobs" "$_vuja_stopped_jobs" >&$VUJA_FD 2>/dev/null
        set -q DIRENV_FILE; and printf 'VUJA_ENV:direnv:%%s\0' "$DIRENV_FILE" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:direnv:\0' >&$VUJA_FD 2>/dev/null
        set -q VIRTUAL_ENV; and printf 'VUJA_ENV:virtualenv:%%s\0' "$VIRTUAL_ENV" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:virtualenv:\0' >&$VUJA_FD 2>/dev/null
        set -q CONDA_DEFAULT_ENV; and printf 'VUJA_ENV:conda:%%s\0' "$CONDA_DEFAULT_ENV" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:conda:\0' >&$VUJA_FD 2>/dev/null
        set -q MISE_ENV; and printf 'VUJA_ENV:mise:%%s\0' "$MISE_ENV" >&$VUJA_FD 2>/dev/null; or if set -q MISE_PROJECT_ROOT; printf 'VUJA_ENV:mise:%%s\0' "$MISE_PROJECT_ROOT" >&$VUJA_FD 2>/dev/null; else if set -q MISE_SHELL; printf 'VUJA_ENV:mise:%%s\0' "$MISE_SHELL" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:mise:\0' >&$VUJA_FD 2>/dev/null; end
        set -q IN_NIX_SHELL; and printf 'VUJA_ENV:nix:%%s\0' "$IN_NIX_SHELL" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:nix:\0' >&$VUJA_FD 2>/dev/null
        set -q AWS_PROFILE; and printf 'VUJA_ENV:aws-profile:%%s\0' "$AWS_PROFILE" >&$VUJA_FD 2>/dev/null; or if set -q AWS_DEFAULT_PROFILE; printf 'VUJA_ENV:aws-profile:%%s\0' "$AWS_DEFAULT_PROFILE" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:aws-profile:\0' >&$VUJA_FD 2>/dev/null; end
        set -q AWS_REGION; and printf 'VUJA_ENV:aws-region:%%s\0' "$AWS_REGION" >&$VUJA_FD 2>/dev/null; or if set -q AWS_DEFAULT_REGION; printf 'VUJA_ENV:aws-region:%%s\0' "$AWS_DEFAULT_REGION" >&$VUJA_FD 2>/dev/null; else; printf 'VUJA_ENV:aws-region:\0' >&$VUJA_FD 2>/dev/null; end
        set -q DOCKER_CONTEXT; and printf 'VUJA_ENV:docker-context:%%s\0' "$DOCKER_CONTEXT" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:docker-context:\0' >&$VUJA_FD 2>/dev/null
        set -q KUBECONFIG; and printf 'VUJA_ENV:kubeconfig:%%s\0' "$KUBECONFIG" >&$VUJA_FD 2>/dev/null; or printf 'VUJA_ENV:kubeconfig:\0' >&$VUJA_FD 2>/dev/null
        printf 'VUJA_CWD:%%s\0' "$PWD" >&$VUJA_FD 2>/dev/null
        _vuja_publish_functions
    end

    function _vuja_publish_functions
        set -l _vuja_function_records
        set -l _vuja_dotfiles_root (path resolve "$HOME/.dotfiles" 2>/dev/null)
        for _vuja_function_name in (functions -n)
            set -l _vuja_function_source (functions --details "$_vuja_function_name" 2>/dev/null | string split -m1 ' ')[1]
            set _vuja_function_source (path resolve "$_vuja_function_source" 2>/dev/null)
            string match -q "$_vuja_dotfiles_root/*" "$_vuja_function_source"; or continue
            set -a _vuja_function_records "$_vuja_function_name\t$_vuja_function_source"
        end
        set -l _vuja_function_signature (string join \n $_vuja_function_records)
        test "$_vuja_function_signature" = "$_vuja_last_function_signature"; and return
        set -g _vuja_last_function_signature "$_vuja_function_signature"
        printf 'VUJA_FUNCTIONS_BEGIN\0' >&$VUJA_FD 2>/dev/null
        for _vuja_function_record in $_vuja_function_records
            printf 'VUJA_FUNCTION:%%b\0' "$_vuja_function_record" >&$VUJA_FD 2>/dev/null
        end
        printf 'VUJA_FUNCTIONS_END\0' >&$VUJA_FD 2>/dev/null
    end

    function _vuja_initial_shell_status --on-event fish_prompt
        _vuja_publish_shell_status
        functions -e _vuja_initial_shell_status
    end

    function _vuja_preexec --on-event fish_preexec
        if set -q VUJA_MARKER; and test -n "$VUJA_MARKER"
            printf '\e]777;vuja;%%s;command-start\a' "$VUJA_MARKER"
        end
	        set -l _vuja_history_marker VUJA_CMD_START
	        if string match -qr '^[[:space:]]' -- "$argv[1]"; or set -q fish_private_mode; or test "$_vuja_history_policy_status" -ne 0
	            set _vuja_history_marker VUJA_CMD_START:IGNORE
	        end
	        printf '%%s\0' "$_vuja_history_marker" >&$VUJA_FD 2>/dev/null
	        if set -q VUJA_HISTORY_ACK_FD
	            read --nchars 1 --local _vuja_history_ack <&$VUJA_HISTORY_ACK_FD 2>/dev/null
	        end
	        set -g _vuja_history_recordable $_vuja_history_ack
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
		if err := installCodexResumeURLHandler(targetExe); err != nil {
			fmt.Printf("! Could not register clickable terminal actions: %v\n", err)
		} else if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			fmt.Println("✓ Registered clickable terminal actions")
		}

		// initialize default config file if it does not exist
		if path, err := config.ConfigPath(); err == nil {
			if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
				if errDir := config.EnsurePrivateDir(filepath.Dir(path)); errDir == nil {
					if content, renderErr := config.DefaultConfigContent(); renderErr == nil {
						if errWrite := config.WritePrivateFile(path, content); errWrite == nil {
							fmt.Printf("✓ Initialized default config file at %s\n", path)
						}
					}
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
	if err := config.EnsurePrivateDir(filepath.Dir(integrationFile)); err != nil {
		return err
	}
	return config.WritePrivateFile(integrationFile, []byte(shellInitScript(shellName, binaryPath)))
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
