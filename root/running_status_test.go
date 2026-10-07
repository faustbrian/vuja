package root

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunningStatusAppearsBeforeSilentCommandOutput(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100, Title: terminalChatboxBarConfig{Left: []string{"directory"}}, Status: terminalChatboxBarConfig{Right: []string{"duration", "exit"}}})
	c.SetInputBoxPath("/project")
	previousExit := 9
	c.SetStatusSnapshot(statusSnapshot{Duration: 3 * time.Second, ExitCode: &previousExit})
	c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
	c.WritePTY([]byte("› git push"))
	c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	screen := applyTerminalOutput(t, out.Bytes(), 80, 12)
	footer := screenLine(screen, 11)
	if !strings.Contains(footer, "⠋ Running · 0s") || strings.Contains(footer, "exit 9") || strings.Contains(footer, "3s") {
		t.Fatalf("silent command did not replace the prior outcome with running status: %q", footer)
	}
}

func TestRunningStatusScheduleAlignsToCommandStart(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
	t.Cleanup(c.Close)
	now := time.Unix(1000, 999000000)
	c.now = func() time.Time { return now }
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	select {
	case <-c.runningStatusWake:
	default:
		t.Fatal("command start did not wake scheduler")
	}
	if delay := c.NextRunningStatusDelay(); delay != 80*time.Millisecond {
		t.Fatalf("initial delay %v", delay)
	}
	now = now.Add(79 * time.Millisecond)
	if delay := c.NextRunningStatusDelay(); delay != time.Millisecond {
		t.Fatalf("animation-phase delay %v", delay)
	}
	now = now.Add(time.Millisecond)
	if delay := c.NextRunningStatusDelay(); delay != 80*time.Millisecond {
		t.Fatalf("animation boundary delay %v", delay)
	}
	now = now.Add(919 * time.Millisecond)
	if delay := c.NextRunningStatusDelay(); delay != time.Millisecond {
		t.Fatalf("adverse-phase delay %v", delay)
	}
	now = now.Add(time.Millisecond)
	if delay := c.NextRunningStatusDelay(); delay != 40*time.Millisecond {
		t.Fatalf("boundary delay %v", delay)
	}
	c.WritePTY(terminalMarkerBytes("running", "command-end:0"))
	if delay := c.NextRunningStatusDelay(); delay != 0 {
		t.Fatalf("completed timer still active: %v", delay)
	}
}

func TestRunningStatusCompletionWhileLayoutSuspendedStopsSchedule(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
	c.WritePTY([]byte("› slow"))
	c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	<-c.runningStatusWake
	c.Resize(80, 1)
	c.WritePTY(terminalMarkerBytes("running", "command-end:0"))
	if delay := c.NextRunningStatusDelay(); delay != 0 {
		t.Fatalf("completed suspended command remains scheduled: %v", delay)
	}
	select {
	case <-c.runningStatusWake:
	default:
		t.Fatal("suspended completion did not wake scheduler")
	}
}

func TestRunningStatusTicksAndCompletion(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100, Status: terminalChatboxBarConfig{Right: []string{"duration", "exit"}}})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
	c.WritePTY([]byte("› slow"))
	c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	c.WritePTY([]byte(strings.Repeat("retained output\r\n", 30)))
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("PageUp did not freeze output")
	}
	initial := append([]byte(nil), out.Bytes()...)
	before := applyTerminalOutput(t, initial, 80, 12)
	beforeLines := terminalScreenLines(before)
	beforeCursor := before.CursorPosition()
	readingTop, readingRows := c.viewport.top, append([]string(nil), c.viewport.frozen...)
	out.Reset()
	now = now.Add(79 * time.Millisecond)
	c.RefreshRunningStatus()
	if out.Len() != 0 {
		t.Fatal("subsecond tick repainted unchanged status")
	}
	now = now.Add(time.Millisecond)
	c.RefreshRunningStatus()
	if !strings.Contains(out.String(), "⠙ Running · 0s") || strings.Count(out.String(), terminalSyncStart) != 1 || strings.Count(out.String(), "\x1b[2K") != 1 {
		t.Fatalf("tick did not repaint only footer: %q", out.String())
	}
	after := applyTerminalOutput(t, append(initial, out.Bytes()...), 80, 12)
	if after.CursorPosition() != beforeCursor || c.viewport.top != readingTop || !slices.Equal(c.viewport.frozen, readingRows) {
		t.Fatal("animation changed cursor or frozen reading position")
	}
	for _, mode := range []string{"\x1b[?1000", "\x1b[?1003", "\x1b[?1006", "\x1b[?1049"} {
		if strings.Contains(out.String(), mode) {
			t.Fatal("animation changed native selection or screen ownership")
		}
	}
	for row, line := range beforeLines {
		if row == 11 {
			if !strings.Contains(screenLine(after, row), "⠙ Running · 0s") {
				t.Fatalf("tick missed footer: %q", screenLine(after, row))
			}
		} else if screenLine(after, row) != line {
			t.Fatalf("tick changed non-footer row %d: %q -> %q", row, line, screenLine(after, row))
		}
	}
	out.Reset()
	c.RefreshRunningStatus()
	if out.Len() != 0 {
		t.Fatal("duplicate animation frame repainted unchanged status")
	}
	now = now.Add(920 * time.Millisecond)
	c.RefreshRunningStatus()
	if !strings.Contains(out.String(), "⠹ Running · 1s") {
		t.Fatalf("whole-second elapsed status: %q", out.String())
	}
	out.Reset()
	now = now.Add(time.Second)
	c.RefreshRunningStatus()
	if !strings.Contains(out.String(), "⠴ Running · 2s") {
		t.Fatalf("silent elapsed status: %q", out.String())
	}
	out.Reset()
	now = now.Add(400 * time.Millisecond)
	c.WritePTY(terminalMarkerBytes("running", "command-end:7"))
	if strings.Contains(out.String(), "Running") || !strings.Contains(out.String(), "exit 7") || !strings.Contains(out.String(), "2.4s") {
		t.Fatalf("final outcome: %q", out.String())
	}
	out.Reset()
	now = now.Add(time.Second)
	c.RefreshRunningStatus()
	if out.Len() != 0 {
		t.Fatal("completed command still repaints")
	}
	c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
	c.WritePTY([]byte("› next"))
	c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
	out.Reset()
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	if !strings.Contains(out.String(), "⠋ Running · 0s") || c.NextRunningStatusDelay() != 80*time.Millisecond {
		t.Fatal("next command did not restart animation and elapsed time")
	}
}

func TestRunningStatusCyclesAnimationBeforeElapsedSecond(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
	c.WritePTY([]byte("› slow"))
	c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("running", "command-start"))
	for _, frame := range []string{"⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠋"} {
		out.Reset()
		now = now.Add(80 * time.Millisecond)
		c.RefreshRunningStatus()
		if !strings.Contains(out.String(), frame+" Running · 0s") {
			t.Fatalf("subsecond animation stalled or changed elapsed seconds: %q", out.String())
		}
	}
}

func TestRunningStatusDoesNotRepaintNativeApplicationOrClosedSurface(t *testing.T) {
	for _, closeSurface := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "closed"}[closeSurface], func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "running", 80, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
			now := time.Unix(1000, 0)
			c.now = func() time.Time { return now }
			c.WritePTY(terminalMarkerBytes("running", "prompt-start"))
			c.WritePTY([]byte("› app"))
			c.WritePTY(terminalMarkerBytes("running", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("running", "command-start"))
			if closeSurface {
				c.Close()
			} else {
				c.WritePTY([]byte("\x1b[?1049happlication"))
			}
			out.Reset()
			now = now.Add(time.Second)
			c.RefreshRunningStatus()
			if out.Len() != 0 {
				t.Fatalf("inactive managed surface repainted: %q", out.String())
			}
		})
	}
}
