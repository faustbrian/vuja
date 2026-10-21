package root

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func clearOutputFixture(t *testing.T, mode string) (*terminalCompositor, *bytes.Buffer, func()) {
	return clearOutputFixtureWidth(t, mode, 60)
}

func clearOutputFixtureWidth(t *testing.T, mode string, width int) (*terminalCompositor, *bytes.Buffer, func()) {
	t.Helper()
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "clear", width, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: mode, OutputViewport: "pinned", OutputLines: 100})
	prompt := func() {
		c.WritePTY(terminalMarkerBytes("clear", "prompt-start"))
		c.WritePTY([]byte("› "))
		c.WritePTY(terminalMarkerBytes("clear", "prompt-end"))
	}
	prompt()
	c.WritePTY(terminalMarkerBytes("clear", "command-start"))
	c.WritePTY([]byte("old-output-before-clear\r\n"))
	c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
	prompt()
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), width, 12), "old-output-before-clear") {
		t.Fatal("invalid fixture: prior output is not visible")
	}
	return c, &out, prompt
}

func TestOutputClearCommandDoesNotResurrectOnPromptOrResize(t *testing.T) {
	for _, mode := range []string{"output", "snapshot", "blocks"} {
		for _, controls := range []string{"\x1b[H\x1b[2J", "\x1b[H\x1b[2J\x1b[3J", "\x1b[3J\x1b[H\x1b[2J", "\x1b[1;1H\x1b[2J\x1b[3J"} {
			for split := 0; split <= len(controls); split++ {
				t.Run(fmt.Sprintf("%s/%q/%d", mode, controls, split), func(t *testing.T) {
					c, out, prompt := clearOutputFixture(t, mode)
					c.WritePTY(terminalMarkerBytes("clear", "command-start"))
					c.WritePTY([]byte(controls[:split]))
					c.WritePTY([]byte(controls[split:]))
					c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
					prompt()
					assertCleared := func(width, height int) {
						t.Helper()
						physical := applyTerminalOutput(t, out.Bytes(), width, height)
						if terminalContainsLine(physical, "old-output-before-clear") || !terminalContainsLine(physical, "›") {
							t.Fatalf("clear did not retain a blank pane and usable prompt: %q", terminalScreenLines(physical))
						}
					}
					assertCleared(60, 12)
					out.Reset()
					c.Resize(54, 14)
					assertCleared(54, 14)
					c.WritePTY(terminalMarkerBytes("clear", "command-start"))
					c.WritePTY([]byte("new-output-after-clear\r\n"))
					c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
					prompt()
					if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 54, 14), "new-output-after-clear") {
						t.Fatal("clearing prevented later output")
					}
				})
			}
		}
	}
}

func TestOutputClearPromptRedrawClearsVisiblePane(t *testing.T) {
	controls := "\x1b[H\x1b[2J"
	for _, marked := range []bool{false, true} {
		for split := 0; split <= len(controls); split++ {
			c, out, _ := clearOutputFixture(t, "output")
			c.HandleViewportInput([]byte("\x1b[5~"), false)
			c.WritePTY([]byte(controls[:split]))
			redraw := controls[split:]
			if marked {
				redraw += terminalMarker("clear", "prompt-start")
			}
			redraw += "› typed-buffer"
			if marked {
				redraw += terminalMarker("clear", "prompt-end")
			}
			c.WritePTY([]byte(redraw))
			physical := applyTerminalOutput(t, out.Bytes(), 60, 12)
			if terminalContainsLine(physical, "old-output-before-clear") || !terminalContainsLine(physical, "typed-buffer") {
				t.Fatalf("Ctrl-L retained output or lost input (marked=%v split=%d): %q", marked, split, terminalScreenLines(physical))
			}
		}
	}
}

func TestOutputClearIncludesShellCompletionDecoration(t *testing.T) {
	// An isolated Zsh PTY emits this PROMPT_SP decoration after /usr/bin/clear
	// and before precmd (where our command-end marker is emitted). The final
	// CR/space/CR erases the temporary partial-line marker, leaving a blank canvas.
	start := zshClearStartMarker(t, "", "clear", "clear")
	for _, mode := range []string{"output", "snapshot"} {
		for _, width := range []int{60, 83} {
			decoration := "\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m" + strings.Repeat(" ", width-1) + "\r \r\x1b]0;fixture-title\x1b\\"
			for split := 0; split <= len(decoration)+1; split++ {
				t.Run(fmt.Sprintf("%s/%d/%d", mode, width, split), func(t *testing.T) {
					c, out, prompt := clearOutputFixtureWidth(t, mode, width)
					c.WritePTY(start)
					c.WritePTY([]byte("\x1b[3J\x1b[H\x1b[2J"))
					if split > len(decoration) {
						for _, b := range []byte(decoration) {
							c.WritePTY([]byte{b})
						}
					} else {
						c.WritePTY([]byte(decoration[:split]))
						c.WritePTY([]byte(decoration[split:]))
					}
					c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
					prompt()
					physical := applyTerminalOutput(t, out.Bytes(), width, 12)
					if terminalContainsLine(physical, "old-output-before-clear") || !terminalContainsLine(physical, "›") {
						t.Fatalf("clear plus shell completion restored old output: %q", terminalScreenLines(physical))
					}
					out.Reset()
					c.Resize(width-4, 14)
					if terminalContainsLine(applyTerminalOutput(t, out.Bytes(), width-4, 14), "old-output-before-clear") {
						t.Fatal("resize resurrected output cleared by the shell")
					}
				})
			}
		}
	}
}

func zshClearStartMarker(t *testing.T, setup, typed, expanded string) []byte {
	t.Helper()
	path, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	hook := filepath.Join(t.TempDir(), "init.zsh")
	if err := os.WriteFile(hook, []byte(shellInitScript("zsh", "/unused/vuja")), 0600); err != nil {
		t.Fatal(err)
	}
	// -d/-f and the private runner environment exclude user startup and live
	// session identities. The sentinel PID prevents this fixture's autostart;
	// this script never invokes or signals that PID or the unused executable.
	command := exec.CommandContext(t.Context(), path, "-d", "-f", "-c", setup+`
source "$1"
_vuja_preexec "$2" "$3" "$3"
`, "vuja-clear-producer", hook, typed, expanded)
	command.Env = append(os.Environ(), "VUJA_PID=1", "VUJA_FD=2", "VUJA_MARKER=clear")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("isolated Zsh preexec failed: %v", err)
	}
	return output
}

func TestOutputClearZshIntentExcludesFunctionsAliasesAndCompoundCommands(t *testing.T) {
	for _, test := range []struct {
		name, setup, typed, expanded string
		clear                        bool
	}{
		{"external", "", "clear", "clear", true},
		{"whitespace", "", " clear ", " clear ", true},
		{"function", "clear() { :; }\n", "clear", "clear", false},
		{"alias", "alias clear=codex\n", "clear", "codex", false},
		{"external alias", "alias clear=clear\n", "clear", "clear", true},
		{"compound", "", "clear; codex", "clear; codex", false},
		{"native", "", "codex", "codex", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := append(terminalMarkerBytes("clear", "command-private"), terminalMarkerBytes("clear", "command-start")...)
			if test.clear {
				want = append(want, terminalMarkerBytes("clear", "command-clear")...)
			}
			if got := zshClearStartMarker(t, test.setup, test.typed, test.expanded); !bytes.Equal(got, want) {
				t.Fatalf("expected %q, got %q", want, got)
			}
		})
	}
}

func TestOutputClearZshPreservesTheExistingCommandStartProtocol(t *testing.T) {
	wire := zshClearStartMarker(t, "", "clear", "clear")
	if bytes.Count(wire, terminalMarkerBytes("clear", "command-start")) != 1 {
		t.Fatalf("an older compositor would miss the clear execution start: %q", wire)
	}
}

func TestOutputClearNativeCanvasDoesNotEraseRetainedPane(t *testing.T) {
	for _, controls := range []string{"\x1b[H\x1b[2Jprivate-canvas", "\x1b[?1049h\x1b[H\x1b[2Jprivate-canvas\x1b[?1049l", "\x1b[H\x1b[2J\x1b[2;2H", "\x1b[H\x1b[2Jprivate-canvas\x1b[1m\x1b[7m%\x1b[27m\x1b[0m" + strings.Repeat(" ", 59) + "\r \r\x1b]0;fixture-title\a"} {
		c, out, prompt := clearOutputFixture(t, "output")
		c.WritePTY(terminalMarkerBytes("clear", "command-start"))
		out.Reset()
		for _, b := range []byte(controls) {
			c.WritePTY([]byte{b})
		}
		if strings.Count(out.String(), controls) != 1 || c.HandleViewportInput([]byte("\x1b[5~"), false) {
			t.Fatal("native clear bytes were changed or navigation was intercepted")
		}
		c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
		prompt()
		physical := applyTerminalOutput(t, out.Bytes(), 60, 12)
		if !terminalContainsLine(physical, "old-output-before-clear") || terminalContainsLine(physical, "private-canvas") {
			t.Fatal("native application clearing changed the retained pane")
		}
		if strings.Contains(strings.Join(retainedModelRows(c.viewport.model), "\n"), "private-canvas") {
			t.Fatal("private native canvas entered retained output")
		}
	}
}

func TestOutputClearBytewiseRepeatedAndScrollbackSemantics(t *testing.T) {
	for _, controls := range []string{"\x1b[H\x1b[2J", "\x1b[H\x1b[2J\x1b[3J"} {
		c, out, prompt := clearOutputFixture(t, "output")
		for repeat := 0; repeat < 2; repeat++ {
			c.WritePTY(terminalMarkerBytes("clear", "command-start"))
			for _, b := range []byte(controls) {
				c.WritePTY([]byte{b})
			}
			c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
			prompt()
			if terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 12), "old-output-before-clear") {
				t.Fatal("repeated bytewise clear resurrected output")
			}
		}
		retained := strings.Join(retainedModelRows(c.viewport.model), "\n")
		if strings.Contains(retained, "old-output-before-clear") != !strings.Contains(controls, "3J") {
			t.Fatal("ED2/ED3 history semantics changed")
		}
	}
}

func TestOutputClearTerminalModeKeepsNativeSemantics(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "clear", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: "output", OutputViewport: "terminal"})
	c.WritePTY(terminalMarkerBytes("clear", "prompt-start"))
	c.WritePTY([]byte("› clear"))
	c.WritePTY(terminalMarkerBytes("clear", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("clear", "command-start"))
	c.WritePTY([]byte("old-output-before-clear\r\n"))
	out.Reset()
	const controls = "\x1b[H\x1b[2J\x1b[3J"
	c.WritePTY([]byte(controls))
	if out.String() != controls || c.ViewportNavigationActive() {
		t.Fatal("terminal mode changed native clear bytes or navigation")
	}
}

func TestOutputClearCheckpointDoesNotRestoreErasedScrollback(t *testing.T) {
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "clear-pane", "clear", nil)
	c.WritePTY(terminalMarkerBytes("clear", "command-start"))
	c.WritePTY([]byte("old-output-before-clear\r\n"))
	c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("clear", "prompt-start"))
	c.WritePTY([]byte("› clear"))
	c.WritePTY(terminalMarkerBytes("clear", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("clear", "command-start"))
	c.WritePTY([]byte("\x1b[H\x1b[2J\x1b[3J"))
	c.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
	c.Close()
	reopened, _ := recoveryCompositor(t, dir, "clear-pane", "reopened", nil)
	if strings.Contains(strings.Join(retainedModelRows(reopened.viewport.model), "\n"), "old-output-before-clear") {
		t.Fatal("checkpoint restore resurrected ED3-erased output")
	}
}
