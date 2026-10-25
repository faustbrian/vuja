package root

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestOutputClearZshInteractiveProducer(t *testing.T) {
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	for _, fixture := range []struct{ name, setup string }{
		{"plain", ""},
		{"title-hook", "precmd() { printf '\\e]0;fixture-title\\a'; }; "},
		{"external-alias", "alias clear=clear; "},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			hook := filepath.Join(dir, "init.zsh")
			if err := os.WriteFile(hook, []byte(shellInitScript("zsh", "/unused/vuja")), 0600); err != nil {
				t.Fatal(err)
			}
			null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer null.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			// Run only an isolated shell and generated hook, never a Vuja binary.
			// No user configuration, credentials, inherited FD or live PID is used.
			cmd := exec.CommandContext(ctx, path, "-d", "-f", "-i")
			cmd.Dir = dir
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "ZDOTDIR=" + dir, "TMPDIR=" + dir, "XDG_CONFIG_HOME=" + dir, "XDG_DATA_HOME=" + dir, "XDG_CACHE_HOME=" + dir, "XDG_RUNTIME_DIR=" + dir, "TERM=xterm-256color", "VUJA_PID=1", "VUJA_IS_CHILD=true", "VUJA_FD=3", "VUJA_MARKER=clear", "VUJA_MANAGED_PROMPT=clear"}
			cmd.ExtraFiles = []*os.File{null}
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 12, Cols: 60})
			if err != nil {
				t.Fatal(err)
			}
			chunks := make(chan []byte, 16)
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer close(chunks)
				for {
					buffer := make([]byte, 4096)
					n, err := terminal.Read(buffer)
					if n > 0 {
						select {
						case chunks <- buffer[:n]:
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			defer func() {
				cancel()
				_ = terminal.Close()
				_ = cmd.Process.Kill() // Only this captured fixture-owned child.
				_ = cmd.Wait()
				<-done
			}()
			var output bytes.Buffer
			c := newTerminalCompositor(&output, "bottom", "clear", 60, 12)
			defer c.Close()
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: "output", OutputViewport: "pinned", OutputLines: 100})
			run := func(command string) []byte {
				t.Helper()
				if _, err := terminal.Write([]byte(command + "\n")); err != nil {
					t.Fatal(err)
				}
				var transcript bytes.Buffer
				for !bytes.Contains(transcript.Bytes(), terminalMarkerBytes("clear", "prompt-end")) {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							t.Fatal("fixture shell closed before prompt")
						}
						transcript.Write(chunk)
						c.WritePTY(chunk)
					case <-ctx.Done():
						t.Fatalf("fixture prompt timed out: %q", transcript.Bytes())
					}
				}
				return transcript.Bytes()
			}
			run(fixture.setup + "source '" + hook + "'")
			run("printf 'old-output-before-clear\\n'")
			if !terminalContainsLine(applyTerminalOutput(t, output.Bytes(), 60, 12), "old-output-before-clear") {
				t.Fatal("invalid fixture: command output was not visible before clear")
			}
			wire := run("clear")
			if terminalContainsLine(applyTerminalOutput(t, output.Bytes(), 60, 12), "old-output-before-clear") {
				t.Fatal("literal clear+Enter restored prior visible output")
			}
			if !bytes.Contains(wire, terminalMarkerBytes("clear", "command-clear")) {
				t.Fatalf("actual Zsh preexec did not identify clear: %q", wire)
			}
			run("printf 'new-output-after-clear\\n'")
			if !terminalContainsLine(applyTerminalOutput(t, output.Bytes(), 60, 12), "new-output-after-clear") {
				t.Fatal("output after interactive clear was lost")
			}
		})
	}
}
