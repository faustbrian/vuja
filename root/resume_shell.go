package root

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Zsh's documented `zle -F -w` dispatches inside the actual editor, in all
// keymaps. A probe cannot execute anything; only a correlated, unexpired commit
// can accept a line. Unlike the suggestion query, BUFFER includes RBUFFER and
// PREBUFFER/CONTEXT distinguish an empty PS1 from an incomplete command at PS2.
func shellResumeEditorScript() string {
	return `
if [[ -n ${VUJA_PID-} && -n ${VUJA_FD-} && -n ${VUJA_RESUME_FD-} ]] && zmodload zsh/datetime 2>/dev/null; then
  _vuja_resume_disarm() {
    if [[ -n ${_vuja_resume_key-} ]]; then
      bindkey -M "$_vuja_resume_map" -r "$_vuja_resume_key" 2>/dev/null
    fi
    unset _vuja_resume_nonce _vuja_resume_deadline _vuja_resume_id _vuja_resume_key _vuja_resume_map
  }
  _vuja_resume_accept() {
    emulate -L zsh
    local _nonce=${_vuja_resume_nonce-} _deadline=${_vuja_resume_deadline-} _id=${_vuja_resume_id-}
    [[ -n $_nonce && $KEYS == ${_vuja_resume_key-} ]] || return
    _vuja_resume_disarm
    if (( EPOCHREALTIME >= _deadline )); then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:expired" 2>/dev/null
      return
    fi
    if [[ $CONTEXT != start || -n $BUFFER || -n $PREBUFFER ]]; then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:nonempty" 2>/dev/null
      return
    fi
    BUFFER="codex resume ${_id:l}"
    CURSOR=${#BUFFER}
    if zle .accept-line; then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:submitted" 2>/dev/null
    else
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:unknown" 2>/dev/null
    fi
  }
  _vuja_resume_editor() {
    emulate -L zsh
    local _kind _nonce _deadline _id _grant
    local IFS=$'\t'
    if ! read -r -t 1 -u "$1" _kind _nonce _deadline _id; then
      _vuja_resume_disarm
      zle -F "$1" 2>/dev/null
      return
    fi
    if [[ $_kind == abort && $_nonce == ${_vuja_resume_nonce-} ]]; then
      _vuja_resume_disarm
      return
    fi
    [[ $_kind == probe ]] || return
    [[ $_nonce =~ '^[0-9a-f]{32}$' && $_deadline =~ '^[0-9]+\.[0-9]{9}$' && $_id =~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' ]] || return
    if (( EPOCHREALTIME >= _deadline )); then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:expired" 2>/dev/null
      return
    fi
    if [[ $CONTEXT != start || -n $BUFFER || -n $PREBUFFER || -n ${_vuja_resume_nonce-} ]] || (( PENDING || KEYS_QUEUED_COUNT )); then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:nonempty" 2>/dev/null
      return
    fi
    print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_READY:$_nonce" 2>/dev/null || return
    # ZLE owns the editor throughout this bounded preparation wait. The normal
    # one-shot widget rechecks the full buffer and deadline at acceptance.
    read -r -t 1 -u "$1" _kind _grant || return
    [[ $_kind == commit && $_grant == $_nonce ]] || return
    if (( EPOCHREALTIME >= _deadline )); then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:expired" 2>/dev/null
      return
    fi
    if [[ $CONTEXT != start || -n $BUFFER || -n $PREBUFFER ]]; then
      print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:nonempty" 2>/dev/null
      return
    fi
    typeset -g _vuja_resume_nonce=$_nonce _vuja_resume_deadline=$_deadline _vuja_resume_id=$_id
    typeset -g _vuja_resume_key=$'\e[777;'${_nonce}'~' _vuja_resume_map=$KEYMAP
    bindkey -M "$_vuja_resume_map" "$_vuja_resume_key" _vuja_resume_accept || { _vuja_resume_disarm; return; }
    print -u "$VUJA_FD" -N -r -- "VUJA_RESUME_RESULT:${_nonce}:armed" 2>/dev/null
  }
  zle -N _vuja_resume_accept
  zle -N _vuja_resume_editor
  zle -F -w "$VUJA_RESUME_FD" _vuja_resume_editor
fi
`
}

type codexResumeEditor interface {
	io.Writer
	Prepare(string, time.Time) error
	Cancel()
}

type shellResumeRequest struct {
	nonce     string
	id        string
	deadline  time.Time
	responses chan string
}

type shellResumeEditor struct {
	shell    string
	requests *os.File
	wake     io.Writer
	mu       sync.Mutex
	pending  *shellResumeRequest
}

// Handle consumes only the dedicated control envelope. Literal editor text is
// still escaped/framed as VUJA_EDITOR and is never dispatched a second time.
func (e *shellResumeEditor) Handle(message string) bool {
	var nonce, response string
	if rest, ok := strings.CutPrefix(message, "VUJA_RESUME_READY:"); ok {
		nonce, response = rest, "ready"
	} else if rest, ok := strings.CutPrefix(message, "VUJA_RESUME_RESULT:"); ok {
		nonce, response, _ = strings.Cut(rest, ":")
	} else {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil && nonce == e.pending.nonce {
		select {
		case e.pending.responses <- response:
		default:
		}
	}
	return true
}

func (e *shellResumeEditor) Prepare(id string, deadline time.Time) error {
	if e.shell != "zsh" || e.requests == nil {
		return errors.New("automatic resume requires the Zsh editor integration; run the displayed command manually")
	}
	if !isCodexResumeID(id) || !deadline.After(time.Now()) {
		return errors.New("resume activation request expired or is invalid")
	}
	nonce := newTerminalMarkerID()
	if !isCodexResumeToken(nonce) {
		return errors.New("resume request identity unavailable")
	}
	request := &shellResumeRequest{nonce: nonce, id: id, deadline: deadline, responses: make(chan string, 2)}
	e.mu.Lock()
	e.pending = request
	e.mu.Unlock()
	_ = e.requests.SetWriteDeadline(deadline)
	if _, err := fmt.Fprintf(e.requests, "probe\t%s\t%d.%09d\t%s\n", nonce, deadline.Unix(), deadline.Nanosecond(), id); err != nil {
		e.Cancel()
		return errors.New("shell editor request could not be delivered")
	}
	response, err := request.wait()
	if err == nil && response == "ready" {
		return nil
	}
	e.Cancel()
	if response == "nonempty" {
		return errors.New("prompt is not empty or is a continuation; clear it before clicking again")
	}
	return errors.New("shell editor did not accept the request; click again at an idle empty prompt")
}

func (r *shellResumeRequest) wait() (string, error) {
	timer := time.NewTimer(time.Until(r.deadline))
	defer timer.Stop()
	select {
	case response := <-r.responses:
		return response, nil
	case <-timer.C:
		return "", errors.New("shell editor response timed out")
	}
}

func (e *shellResumeEditor) Write(payload []byte) (int, error) {
	e.mu.Lock()
	request := e.pending
	e.mu.Unlock()
	if request == nil || string(payload) != codexResumeCommand(request.id)+"\r" || !request.deadline.After(time.Now()) {
		return 0, errors.New("shell editor activation is no longer prepared")
	}
	_ = e.requests.SetWriteDeadline(request.deadline)
	n, err := fmt.Fprintf(e.requests, "commit\t%s\n", request.nonce)
	if n == 0 && err != nil {
		return 0, errors.New("shell editor acceptance could not be delivered")
	}
	if err == nil {
		response, waitErr := request.wait()
		if waitErr == nil {
			switch response {
			case "armed":
				// In Zsh 5.9 raw_getbyte's FD polling loop does not exit merely
				// because an FD widget calls accept-line (Src/Zle/zle_main.c).
				// Wake its private normal widget, never inject command text or
				// an ordinary Enter binding. That widget rechecks actual state.
				key := "\x1b[777;" + request.nonce + "~"
				if e.wake != nil {
					written, wakeErr := io.WriteString(e.wake, key)
					if wakeErr == nil && written == len(key) {
						response, waitErr = request.wait()
						if waitErr == nil && response == "submitted" {
							return len(payload), nil
						}
						if waitErr == nil && (response == "nonempty" || response == "expired") {
							return 0, errors.New("shell editor rejected activation without executing")
						}
					}
				}
			case "submitted":
				// Submission before the correlated wake is not our protocol.
				break
			case "expired", "nonempty":
				return 0, errors.New("shell editor rejected activation without executing")
			}
		}
	}
	// After any commit bytes, missing acceptance is ambiguous. Never re-arm
	// the capability or release the command reservation on an uncertain effect.
	return len(payload), errors.New("shell editor submission is unconfirmed; inspect the terminal before retrying manually")
}

func (e *shellResumeEditor) Cancel() {
	e.mu.Lock()
	request := e.pending
	e.pending = nil
	e.mu.Unlock()
	if request != nil {
		_ = e.requests.SetWriteDeadline(time.Now().Add(codexResumeActionTimeout))
		_, _ = fmt.Fprintf(e.requests, "abort\t%s\n", request.nonce)
	}
}
