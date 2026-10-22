package root

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputBlocksShellAdmissionPrecedesCommandStart(t *testing.T) {
	for _, name := range []string{"zsh", "bash", "fish"} {
		for _, reply := range []byte{'0', '1', 'x', 'e', 'm'} {
			t.Run(name+"/"+string(reply), func(t *testing.T) {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Skip(name + " unavailable")
				}
				dir := t.TempDir()
				hook := filepath.Join(dir, "hook")
				if err := os.WriteFile(hook, []byte(shellInitScript(name, "/unused/vuja")), 0600); err != nil {
					t.Fatal(err)
				}
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				defer w.Close()
				// Start with a public execution, then check that the next
				// submission uses only its own acknowledgement.
				if _, err := w.Write([]byte{'1'}); err != nil {
					t.Fatal(err)
				}
				if reply != 'e' && reply != 'm' {
					if _, err := w.Write([]byte{reply}); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				var args []string
				switch name {
				case "zsh":
					args = []string{"-d", "-f", "-c", `source "$1"; _vuja_history_ack=1; _vuja_preexec 'printf fixture' 'printf fixture' 'printf fixture'; [[ $2 != m ]] || unset VUJA_HISTORY_ACK_FD; _vuja_preexec 'printf fixture' 'printf fixture' 'printf fixture'; IFS= read -r _vuja_remaining; printf '%s' "$_vuja_remaining"`, "fixture", hook, string(reply)}
				case "bash":
					args = []string{"--noprofile", "--norc", "-c", `source "$1"; _vuja_history_ack=1; _vuja_preexec; [[ $2 != m ]] || unset VUJA_HISTORY_ACK_FD; _vuja_preexec; IFS= read -r _vuja_remaining; printf '%s' "$_vuja_remaining"`, "fixture", hook, string(reply)}
				case "fish":
					// --no-config enables Fish private mode. This fixture checks
					// the acknowledgement channel; policy exclusions have their
					// own real-admission coverage in history_blankline_test.go.
					args = []string{"--no-config", "-c", `set -e fish_private_mode; source $argv[1]; set -g _vuja_history_ack 1; _vuja_preexec 'printf fixture'; if test "$argv[2]" = m; set -e VUJA_HISTORY_ACK_FD; end; _vuja_preexec 'printf fixture'; read --local _vuja_remaining; printf '%s' "$_vuja_remaining"`, hook, string(reply)}
				}
				cmd := exec.CommandContext(t.Context(), path, args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "ZDOTDIR=" + dir, "TMPDIR=" + dir, "TERM=xterm-256color", "VUJA_PID=1", "VUJA_IS_CHILD=true", "VUJA_FD=2", "VUJA_MARKER=blocks", "VUJA_HISTORY_ACK_FD=3"}
				cmd.ExtraFiles = []*os.File{r}
				cmd.Stdin = strings.NewReader("1shell-input\n")
				var wire bytes.Buffer
				cmd.Stdout = &wire
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
				policy := "private"
				if reply == '1' {
					policy = "public"
				}
				want := terminalMarker("blocks", "command-public") + terminalMarker("blocks", "command-start") +
					terminalMarker("blocks", "command-"+policy) + terminalMarker("blocks", "command-start") + "1shell-input"
				if wire.String() != want {
					t.Fatalf("shell did not order acknowledged label policy before execution: %q", wire.String())
				}
			})
		}
	}
}

func TestFishHistoryRecordingReadsOnlyAcknowledgementPipe(t *testing.T) {
	path, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish unavailable")
	}
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook")
	if err := os.WriteFile(hook, []byte(shellInitScript("fish", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := w.Write([]byte("10x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), path, "--no-config", "-c", `
source $argv[1]
vuja_history_pause; printf 'pause=%s:%s\n' $status "$_vuja_history_paused"
vuja_history_resume; printf 'resume=%s:%s\n' $status "$_vuja_history_paused"
vuja_history_pause; printf 'invalid=%s:%s\n' $status "$_vuja_history_paused"
vuja_history_pause; printf 'eof=%s:%s\n' $status "$_vuja_history_paused"
set -e VUJA_HISTORY_ACK_FD
vuja_history_pause; printf 'missing=%s:%s\n' $status "$_vuja_history_paused"
read --local remaining; printf 'stdin=%s\n' "$remaining"
`, hook)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "VUJA_PID=1", "VUJA_IS_CHILD=true", "VUJA_FD=2", "VUJA_HISTORY_ACK_FD=3"}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Stdin = strings.NewReader("1shell-input\n")
	var output, requests bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &requests
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "pause=0:1\nresume=0:0\ninvalid=1:0\neof=1:0\nmissing=1:0\nstdin=1shell-input\n"; got != want {
		t.Fatalf("recording controls lost their own acknowledgement or consumed shell input: %q", got)
	}
	if got, want := requests.String(), "VUJA_HISTORY_RECORDING_PAUSE\x00VUJA_HISTORY_RECORDING_RESUME\x00VUJA_HISTORY_RECORDING_PAUSE\x00VUJA_HISTORY_RECORDING_PAUSE\x00"; got != want {
		t.Fatalf("recording controls changed request order or emitted an unacknowledgeable request: %q", got)
	}
}
