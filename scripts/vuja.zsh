# vuja zsh fast IPC integration

if [[ -n "$VUJA_FD" ]]; then
  _vuja_send_lbuffer() {
    # -u $VUJA_FD writes to the pipe file descriptor set up by Vuja wrappers
    # -N appends a null byte '\0' instead of newline (perfect for parsing)
    # -r prints raw string
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
