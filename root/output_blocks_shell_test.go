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
				if reply != 'e' && reply != 'm' {
					if _, err := w.Write([]byte{reply}); err != nil {
						t.Fatal(err)
					}
				}
				w.Close()
				var args []string
				switch name {
				case "zsh":
					args = []string{"-d", "-f", "-c", `source "$1"; _vuja_history_ack=1; _vuja_preexec 'printf fixture' 'printf fixture' 'printf fixture'`, "fixture", hook}
				case "bash":
					args = []string{"--noprofile", "--norc", "-c", `source "$1"; _vuja_history_ack=1; _vuja_preexec`, "fixture", hook}
				case "fish":
					args = []string{"--no-config", "-c", `source $argv[1]; set -g _vuja_history_ack 1; _vuja_preexec 'printf fixture'`, hook}
				}
				cmd := exec.CommandContext(t.Context(), path, args...)
				cmd.Dir = dir
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "ZDOTDIR=" + dir, "TMPDIR=" + dir, "TERM=xterm-256color", "VUJA_PID=1", "VUJA_IS_CHILD=true", "VUJA_FD=2", "VUJA_MARKER=blocks"}
				if reply != 'm' {
					cmd.Env = append(cmd.Env, "VUJA_HISTORY_ACK_FD=3")
				}
				cmd.ExtraFiles = []*os.File{r}
				var wire bytes.Buffer
				cmd.Stdout = &wire
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
				policy := "private"
				if reply == '1' {
					policy = "public"
				}
				want := terminalMarker("blocks", "command-"+policy) + terminalMarker("blocks", "command-start")
				if !strings.Contains(wire.String(), want) {
					t.Fatalf("shell did not order acknowledged label policy before execution: %q", wire.String())
				}
			})
		}
	}
}
