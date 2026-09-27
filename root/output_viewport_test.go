package root

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOutputViewportBusyResizeAndApplicationOwnership(t *testing.T) {
	for _, control := range []string{"", "\x1bM", "\x1b7"} {
		t.Run(fmt.Sprintf("control-%q", control), func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 80, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100, Title: terminalChatboxBarConfig{Left: []string{"directory"}}, Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
			c.SetInputBoxPath("/project")
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› running " + strings.Repeat("x", 55) + " end-token"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
			if control != "" {
				c.WritePTY([]byte(control))
				if c.HandleViewportInput([]byte("\x1b[5~"), false) {
					t.Fatal("application navigation intercepted")
				}
				return
			}
			out.Reset()
			c.Resize(40, 10)
			screen := applyTerminalOutput(t, out.Bytes(), 40, 10)
			if !strings.Contains(screenLine(screen, 9), "exit") {
				t.Fatalf("resized status missing: %q", screenLine(screen, 9))
			}
			var visible strings.Builder
			for row := 0; row < 10; row++ {
				visible.WriteString(strings.TrimSpace(screenLine(screen, row)))
			}
			if !strings.Contains(visible.String(), "end-token") {
				t.Fatal("resize cropped submitted command")
			}
		})
	}
}

func TestOutputViewportSnapshotDoesNotRetainPreviousOutcome(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 80, 15)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: "snapshot", OutputViewport: "pinned", OutputLines: 100, Status: terminalChatboxBarConfig{Right: []string{"duration", "exit"}}})
	previousExit := 9
	c.SetStatusSnapshot(statusSnapshot{Duration: 3 * time.Second, ExitCode: &previousExit})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› next"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	for y := 0; y < c.viewport.model.Height(); y++ {
		if strings.Contains(viewportLine(c.viewport.model, y, 80, false), "exit 9") {
			t.Fatal("snapshot retained previous outcome")
		}
	}
}

func TestOutputViewportTinyResizeKeepsCommandOutputVisible(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.Resize(20, 3)
	out.Reset()
	c.WritePTY([]byte("still-visible\r\n"))
	if !strings.Contains(out.String(), "still-visible") {
		t.Fatal("tiny resize dropped command output")
	}
}

func TestOutputViewportPinsInputAndPreservesReadingPosition(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: "output", OutputViewport: "pinned", OutputLines: 100, Title: terminalChatboxBarConfig{Left: []string{"directory"}}, Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	c.SetInputBoxPath("/example/project")
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› printf lines"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	var lines strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&lines, "old line %02d\r\n", i)
	}
	c.WritePTY([]byte(lines.String()))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !strings.Contains(screenLine(screen, 9), "printf lines") || !strings.Contains(screenLine(screen, 7), "/example/project") || !strings.Contains(screenLine(screen, 11), "exit") {
		t.Fatal("running output displaced the pinned chatbox")
	}
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("PageUp did not enter output scrollback")
	}
	browsed := applyTerminalOutput(t, out.Bytes(), 60, 12)
	readingLine := screenLine(browsed, 0)
	if !strings.Contains(readingLine, "old line") {
		t.Fatal("older output missing")
	}
	out.Reset()
	c.WritePTY([]byte("new output\r\n"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if screenLine(screen, 0) != readingLine || !strings.Contains(screenLine(screen, 6), "New output") {
		t.Fatal("new output did not preserve the reading position and show activity")
	}
	if !c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("Escape did not return to latest")
	}
	if c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("Escape was intercepted outside scrollback")
	}
}

func TestOutputViewportLeavesApplicationsAndPasteUntouched(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	if c.HandleViewportInput([]byte("\x1b[200~hello\x1b[5~\x1b[201~"), false) {
		t.Fatal("paste was consumed as navigation")
	}
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("\x1b[?1049happlication"))
	if c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("application PageUp was intercepted")
	}
}

func TestOutputViewportInputChunkingAndPriority(t *testing.T) {
	for _, key := range viewportKeys {
		for split := 1; split < len(key); split++ {
			var stager viewportInputStager
			first := stager.stage([]byte(key[:split]))
			second := stager.stage([]byte(key[split:]))
			if string(append(first, second...)) != key || len(stager.pending) != 0 {
				t.Fatalf("split %d corrupted %q", split, key)
			}
		}
	}
	var stager viewportInputStager
	if len(stager.stage([]byte("\x1b"))) != 0 || string(stager.flush()) != "\x1b" {
		t.Fatal("lone Escape lost")
	}
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	if got := c.FilterViewportInput([]byte("\x1b[5~abc\x1b[5~"), false); string(got) != "abc" {
		t.Fatalf("adjacent bytes corrupted: %q", got)
	}
	if got := c.FilterViewportInput([]byte("\x1b[5~"), true); string(got) != "\x1b[5~" {
		t.Fatal("suggestion navigation stolen")
	}
}

func TestOutputViewportSplitApplicationControlAndNativeMode(t *testing.T) {
	for _, mode := range []string{"pinned", "terminal"} {
		var out bytes.Buffer
		c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
		t.Cleanup(c.Close)
		c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: mode, OutputLines: 100, Scrollback: "output"})
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
		c.WritePTY([]byte("› run"))
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
		if mode == "terminal" && c.HandleViewportInput([]byte("\x1b[5~"), false) {
			t.Fatal("native navigation stolen")
		}
		c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
		c.WritePTY([]byte("\x1b[?10"))
		c.WritePTY([]byte("49happ"))
		if c.HandleViewportInput([]byte("\x1b[5~"), false) {
			t.Fatal("split alternate-screen control did not release viewport")
		}
		c.WritePTY([]byte("\x1b[?1049l"))
		c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
		c.WritePTY([]byte("› "))
		c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
		if mode == "pinned" && !c.HandleViewportInput([]byte("\x1b[5~"), false) {
			t.Fatal("viewport did not resume after application exit")
		}
	}
}

func TestOutputViewportSnapshotBackgroundAndBoundedHistory(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 3, Scrollback: "snapshot"})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› unique-command"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("result\r\n"))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	foundSnapshot := false
	for y := 0; y < 9; y++ {
		foundSnapshot = foundSnapshot || strings.Contains(screenLine(screen, y), "unique-command")
	}
	if !foundSnapshot {
		t.Fatal("snapshot was not retained")
	}
	c.WritePTY([]byte(strings.Repeat("bounded\r\n", 40)))
	if c.viewport.model.ScrollbackLen() > 3 {
		t.Fatal("visual history exceeds configured bound")
	}
	c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("history unavailable after command")
	}
	out.Reset()
	c.WriteNotification([]byte("background-note\r\n"))
	if !strings.Contains(out.String(), "New output") {
		t.Fatal("background output lost while browsing")
	}
	c.Resize(40, 10)
	if !c.HandleViewportInput([]byte("\x1b[F"), false) {
		t.Fatal("resize lost scrollback mode")
	}
}
