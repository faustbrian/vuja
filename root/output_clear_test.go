package root

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func clearOutputFixture(t *testing.T, mode string) (*terminalCompositor, *bytes.Buffer, func()) {
	t.Helper()
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "clear", 60, 12)
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
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 12), "old-output-before-clear") {
		t.Fatal("invalid fixture: prior output is not visible")
	}
	return c, &out, prompt
}

func TestOutputClearCommandDoesNotResurrectOnPromptOrResize(t *testing.T) {
	for _, mode := range []string{"output", "snapshot"} {
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

func TestOutputClearNativeCanvasDoesNotEraseRetainedPane(t *testing.T) {
	for _, controls := range []string{"\x1b[H\x1b[2Jprivate-canvas", "\x1b[?1049h\x1b[H\x1b[2Jprivate-canvas\x1b[?1049l"} {
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
