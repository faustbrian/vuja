package root

import "fmt"

// ZLE widgets run only when the editor actually owns input. Foreground process
// groups cannot establish that fact with job control disabled or background jobs.
func shellMouseEditorScript() string {
	return fmt.Sprintf(`
if [[ -n ${VUJA_PID-} && -n ${VUJA_FD-} ]]; then
  _vuja_mouse_report() {
    emulate -L zsh
    setopt extendedglob
    local _body='' _char
    # The parent frames complete CSI reports before forwarding. Use ZLE-aware
    # read (including its pushed-back queue), never a separate tty descriptor.
    # Bound malformed direct input so an incomplete prefix cannot hold ZLE.
    while (( ${#_body} < %d )); do
      if ! builtin read -r -k 1 -t 0.1 _char; then
        break
      fi
      _body+=$_char
      if [[ $_char != [0-9\;] ]]; then
        if [[ $_body == [0-9]##\;[0-9]##\;[0-9]##[Mm] ]]; then
          return 0
        fi
        break
      fi
    done
    # Only the reserved prefix was consumed by the key binding. Replay rejected
    # body keys through ZLE, including controls/paste, without recursing on it.
    [[ -z $_body ]] || zle -U -- "$_body"
    return 0
  }
  _vuja_mouse_bindings() {
    emulate -L zsh
    local _map _binding
    local -a _words
    for _map in ${(f)"$(bindkey -l)"}; do
      [[ $_map == .safe ]] && continue
      _binding=$(bindkey -M "$_map" $'\e[<') || continue
      _words=(${(z)_binding})
      # Preserve an explicit user binding for this prefix.
      if [[ ${_words[-1]} == undefined-key || ${_words[-1]} == _vuja_mouse_report ]]; then
        bindkey -M "$_map" $'\e[<' _vuja_mouse_report
      fi
    done
  }
  zle -N _vuja_mouse_report
  autoload -Uz add-zle-hook-widget
  add-zle-hook-widget -d line-init _vuja_mouse_bindings 2>/dev/null
  add-zle-hook-widget -d keymap-select _vuja_mouse_bindings 2>/dev/null
  add-zle-hook-widget line-init _vuja_mouse_bindings
  add-zle-hook-widget keymap-select _vuja_mouse_bindings
  _vuja_mouse_bindings
fi
`, terminalModelSequenceLimit)
}
