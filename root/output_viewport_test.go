package root

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/config"
)

func TestOutputViewportKeepsBusyChromeDuringLineProgress(t *testing.T) {
	for _, mode := range []string{"output", "snapshot"} {
		for _, update := range []string{"\rOK\x1b[K", "\r\x1b[2KOK", "\rOK\x1b[0K"} {
			for split := 0; split <= len(update); split++ {
				t.Run(fmt.Sprintf("%s/%q/split-%d", mode, update, split), func(t *testing.T) {
					var out bytes.Buffer
					c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
					t.Cleanup(c.Close)
					c.SetInputBoxTheme(testInputBoxTheme())
					c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Scrollback: mode, OutputViewport: "pinned", OutputLines: 100, Title: terminalChatboxBarConfig{Left: []string{"directory"}}, Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
					c.SetInputBoxPath("/project")
					c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
					c.WritePTY([]byte("› git push"))
					c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
					c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
					assertBusy := func() {
						t.Helper()
						physical := applyTerminalOutput(t, out.Bytes(), 60, 12)
						if !physical.IsAltScreen() || !strings.Contains(screenLine(physical, 7), "/project") || !strings.Contains(screenLine(physical, 9), "git push") || !strings.Contains(screenLine(physical, 11), "Running") {
							t.Fatalf("busy command lost its pinned display: %q", terminalScreenLines(physical))
						}
					}
					// No output is needed to keep the submitted command visible.
					assertBusy()
					c.WritePTY([]byte("long-progress"))
					c.WritePTY([]byte(update[:split]))
					c.WritePTY([]byte(update[split:]))
					// Inspect before command-end: this is the silent wait after progress.
					assertBusy()
					physical := applyTerminalOutput(t, out.Bytes(), 60, 12)
					if !terminalContainsLine(physical, "OK") || terminalContainsLine(physical, "long-progress") || c.viewportPassthrough {
						t.Fatalf("line progress was not rendered in the retained output: %q", terminalScreenLines(physical))
					}
					if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
						t.Fatal("progress released managed navigation")
					}
					c.HandleViewportInput([]byte("\x1b[F"), false)
					c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
					c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
					c.WritePTY([]byte("› next"))
					c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
					physical = applyTerminalOutput(t, out.Bytes(), 60, 12)
					if !terminalContainsLine(physical, "OK") || !terminalContainsLine(physical, "› next") {
						t.Fatal("completion lost progress output or the next prompt")
					}
				})
			}
		}
	}
}

func TestOutputViewportLineProgressStillYieldsToNativeApplications(t *testing.T) {
	for _, control := range []string{"\x1b[?1049h", "\x1b[2;2H", "\x1b[2A", "\x1b[10D", "\x1b[1G", "\x1b[?25l", "\x1b[6n", "\x1b[?1000h", "\x1b[?2K", "\x1b[1 K"} {
		for split := 0; split <= len(control); split++ {
			t.Run(fmt.Sprintf("%q/split-%d", control, split), func(t *testing.T) {
				var out bytes.Buffer
				c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
				t.Cleanup(c.Close)
				c.SetInputBoxTheme(testInputBoxTheme())
				c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
				c.WritePTY([]byte("› application"))
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
				c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
				c.WritePTY([]byte("progress\rOK\x1b[K"))
				out.Reset()
				c.WritePTY([]byte(control[:split]))
				if split < len(control) && !c.ViewportNavigationActive() {
					t.Fatal("incomplete native control prematurely released the viewport")
				}
				c.WritePTY([]byte(control[split:]))
				if bytes.Count(out.Bytes(), []byte(control)) != 1 || c.HandleViewportInput([]byte("\x1b[5~"), false) {
					t.Fatal("native control was altered or native navigation was intercepted")
				}
				out.Reset()
				c.WritePTY([]byte("native-content"))
				if out.String() != "native-content" {
					t.Fatal("managed repaint overwrote native application output")
				}
				c.WritePTY([]byte("\x1b[?1049l\r\napplication-result\r\n"))
				c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
				c.WritePTY([]byte("› next"))
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
				physical := applyTerminalOutput(t, out.Bytes(), 60, 12)
				if !terminalContainsLine(physical, "application-result") || !terminalContainsLine(physical, "› next") || !c.ViewportNavigationActive() {
					t.Fatal("native completion did not restore retained output and prompt ownership")
				}
			})
		}
	}
}

func TestOutputViewportTerminalModePreservesProgressAndNativeBytes(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Scrollback: "output", OutputViewport: "terminal"})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	// The legacy surface transition is emitted with the first output chunk.
	c.WritePTY([]byte("first-output\r\n"))
	for _, text := range []string{"progress\rOK\x1b[K", "\x1b[1A\x1b[2Kupdated", "\x1b[?25l\rOK\x1b[?25h", "\x1b[?1049hnative\x1b[?1049l"} {
		out.Reset()
		c.WritePTY([]byte(text))
		if out.String() != text || c.HandleViewportInput([]byte("\x1b[5~"), false) {
			t.Fatalf("terminal mode changed native output/input semantics: %q", out.String())
		}
	}
}

func TestOutputViewportBusyResizeAndApplicationOwnership(t *testing.T) {
	for _, control := range []string{"", "\x1bM", "\x1b7", "\x1bP$qm\x1b\\", "\x1b_Ga=q;\x1b\\"} {
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
			if !strings.Contains(screenLine(screen, 9), "Running") {
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

func TestOutputViewportRestoredSizeKeepsRunningOutputVisible(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("before-collapse\r\n"))
	c.Resize(60, 1)
	c.WritePTY([]byte("during-collapse\r\n"))
	c.Resize(60, 12)
	out.Reset()
	c.WritePTY([]byte("restored-progress\r\n"))
	if strings.Count(out.String(), "restored-progress") != 1 {
		t.Fatalf("expected continued output exactly once after restored size, got %q", out.String())
	}
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	for _, text := range []string{"before-collapse", "during-collapse", "restored-progress", "› running"} {
		if terminalLineIndex(terminalScreenLines(screen), text, 0) < 0 {
			t.Fatalf("restored running viewport lost %q", text)
		}
	}
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("running viewport navigation did not resume before next prompt")
	}
	c.HandleViewportInput([]byte("\x1b"), false)
	c.Resize(60, 1)
	c.Resize(60, 12)
	c.WritePTY([]byte("second-restoration\r\n"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if terminalLineIndex(terminalScreenLines(screen), "second-restoration", 0) < 0 || terminalLineIndex(terminalScreenLines(screen), "› running", 0) < 0 {
		t.Fatal("repeated collapse did not restore output and fixed chatbox")
	}
	c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› next"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("expected pinned navigation to resume at the next prompt")
	}
}

func TestOutputViewportCollapsedNativeHandoffSurvivesRestore(t *testing.T) {
	for _, control := range []string{"\x1b[?1049h", "\x1b[2;2H"} {
		t.Run(fmt.Sprintf("%q", control), func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› app"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
			c.Resize(60, 1)
			c.WritePTY([]byte(control + "native-screen"))
			out.Reset()
			c.Resize(60, 12)
			if strings.Contains(out.String(), "\x1b[?1049h") || c.HandleViewportInput([]byte("\x1b[5~"), false) {
				t.Fatal("restoration stole native display or input ownership")
			}
		})
	}
}

func TestOutputViewportCollapsedFrozenViewReportsNewOutput(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	for i := 0; i < 30; i++ {
		c.WritePTY([]byte(fmt.Sprintf("row-%02d\r\n", i)))
	}
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	c.Resize(60, 1)
	c.WritePTY([]byte("new-while-collapsed\r\n"))
	out.Reset()
	c.Resize(60, 12)
	if !strings.Contains(out.String(), "New activity") {
		t.Fatalf("missing frozen-view activity indicator: %q", out.String())
	}
}

func TestOutputViewportPromptMarksCannotDisableMarginExtension(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	out.Reset()
	c.WritePTY([]byte("\x1b]133;A\a\x1b]0;keep-title\a"))
	if strings.Contains(out.String(), "\x1b]133;A") {
		t.Fatal("iTerm initial prompt mark cleared managed soft alternate mode")
	}
	if !strings.Contains(out.String(), "\x1b]0;keep-title\a") {
		t.Fatal("unrelated terminal title effect was lost")
	}
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "terminal"})
	out.Reset()
	c.WritePTY([]byte("\x1b]133;A\a"))
	if !strings.Contains(out.String(), "\x1b]133;A\a") {
		t.Fatal("native terminal prompt mark was suppressed")
	}
}

func TestOutputViewportDegradationThenResizeKeepsOutputVisible(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.degradeVisualPipeline()
	c.Resize(60, 12)
	out.Reset()
	c.WritePTY([]byte("after-degradation\r\n"))
	if strings.Count(out.String(), "after-degradation") != 1 {
		t.Fatalf("recovery swallowed command output: %q", out.String())
	}
}

func TestOutputViewportWheelScrollsWithoutMovingChatbox(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{SurfaceWidth: "full-width", OutputViewport: "pinned", OutputMouse: "navigate", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	for i := 0; i < 30; i++ {
		c.WritePTY([]byte(fmt.Sprintf("row-%02d\r\n", i)))
	}
	before := applyTerminalOutput(t, out.Bytes(), 60, 12)
	inputRow := terminalLineIndex(terminalScreenLines(before), "› running", 0)
	out.Reset()
	var filter terminalInputFilter
	if got := filter.FilterInput([]byte("\x1b[<64;2;2M"), c, false, false); len(got) != 0 {
		t.Fatalf("wheel leaked to shell: %q", got)
	}
	up := applyTerminalOutput(t, out.Bytes(), 60, 12)
	if screenLine(up, 0) == screenLine(before, 0) {
		t.Fatal("wheel did not reveal older output")
	}
	if !strings.Contains(screenLine(up, inputRow), "› running") {
		t.Fatal("wheel moved the fixed chatbox")
	}
	for column := 0; column < 60; column++ {
		cell := up.CellAt(column, inputRow)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("full-width background missing at column %d", column)
		}
	}
	if got := filter.FilterInput([]byte("\x1b[<65;2;2M"), c, false, false); len(got) != 0 {
		t.Fatalf("wheel down leaked to shell: %q", got)
	}
	if c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("wheel down did not return to latest output")
	}
}

func TestConfiguredMouseNavigationReachesPinnedViewport(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UI.Chatbox.OutputMouse = "navigate"
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "configured-mouse", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	c.WritePTY(terminalMarkerBytes("configured-mouse", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("configured-mouse", "prompt-end"))
	for i := 0; i < 30; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("line-%02d\r\n", i)))
	}
	if !strings.Contains(out.String(), "\x1b[?1000h\x1b[?1006h") {
		t.Fatal("configured navigation did not enable viewport mouse reporting")
	}
	if !c.HandleViewportInput([]byte("\x1b[<64;2;2M"), false) || c.viewport.frozen == nil {
		t.Fatal("configured navigation did not browse output with the wheel")
	}
}

func TestOutputViewportAlternateScreenRestoresPrimaryOnClose(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{SurfaceWidth: "full-width", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	screen := applyTerminalOutput(t, append([]byte("primary-history\r\n"), out.Bytes()...), 60, 12)
	if !screen.IsAltScreen() {
		t.Fatal("pinned UI must own alternate screen for terminal margin extension")
	}
	if strings.Count(out.String(), "\x1b[?1049h") != 1 {
		t.Fatal("alternate screen must be entered once, not cleared on every repaint")
	}
	row := terminalLineIndex(terminalScreenLines(screen), "› input", 0)
	for _, column := range []int{0, 59} {
		cell := screen.CellAt(column, row)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("edge background missing at column %d", column)
		}
	}
	c.Close()
	restored := applyTerminalOutput(t, append([]byte("primary-history\r\n"), out.Bytes()...), 60, 12)
	if restored.IsAltScreen() || !strings.Contains(screenLine(restored, 0), "primary-history") {
		t.Fatal("closing pinned UI did not restore original primary history")
	}
}

func TestOutputViewportAlternateScreenYieldsToNativeApplication(t *testing.T) {
	for _, mode := range []string{"9", "1000", "1001", "1002", "1003"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› app"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
			out.Reset()
			mouseEnable := "\x1b[?" + mode + "h"
			mouseDisable := "\x1b[?" + mode + "l"
			c.WritePTY([]byte("\x1b[?1049h" + mouseEnable + "application-screen"))
			if !strings.Contains(out.String(), "\x1b[?1049l") {
				t.Fatal("managed alternate screen was not released before native application")
			}
			if !strings.Contains(out.String(), mouseEnable+"application-screen") {
				t.Fatal("native application's mouse control was not forwarded")
			}
			if c.HandleViewportInput([]byte("\x1b[<64;2;2M"), false) {
				t.Fatal("native application's wheel input was stolen")
			}
			c.WritePTY([]byte("\x1b[?1049l\r\nresume-output\r\n"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› next"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			if strings.LastIndex(out.String(), mouseDisable) < strings.LastIndex(out.String(), mouseEnable) {
				t.Fatal("managed prompt retained mouse tracking from native application")
			}
			screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
			if !screen.IsAltScreen() || terminalLineIndex(terminalScreenLines(screen), "resume-output", 0) < 0 || terminalLineIndex(terminalScreenLines(screen), "› next", 0) < 0 {
				t.Fatal("pinned alternate screen did not reacquire output and prompt after native app")
			}
		})
	}
}

func TestOutputViewportWheelFramingAndOwnership(t *testing.T) {
	const wheel = "\x1b[<64;2;2M"
	for cut := 0; cut <= len(wheel); cut++ {
		t.Run(fmt.Sprintf("split-%d", cut), func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputMouse: "navigate", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› input"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			for i := 0; i < 30; i++ {
				c.WriteNotification([]byte(fmt.Sprintf("history-%02d\r\n", i)))
			}
			var filter terminalInputFilter
			got := filter.FilterInput([]byte("x"+wheel[:cut]), c, false, true)
			got = append(got, filter.FilterInput([]byte(wheel[cut:]+"y"), c, false, true)...)
			if string(got) != "xy" || c.viewport.frozen == nil {
				t.Fatalf("fragmented wheel consumed typed bytes or failed to browse: %q", got)
			}
			top := c.viewport.top
			paste := append(append(append([]byte(nil), bracketedPasteStart...), []byte(wheel)...), bracketedPasteEnd...)
			if got := filter.FilterInput(paste, c, false, true); !bytes.Equal(got, paste) || c.viewport.top != top {
				t.Fatal("paste wheel was interpreted as navigation")
			}
			if got := filter.FilterInput([]byte(wheel), c, true, true); len(got) != 0 || c.viewport.top != top {
				t.Fatal("wheel over suggestions changed output or leaked into shell")
			}
			if got := filter.FilterInput([]byte("\x1b[<64;2;12M"), c, false, true); len(got) != 0 || c.viewport.top != top {
				t.Fatal("wheel over fixed chatbox changed output or leaked into shell")
			}
		})
	}
}

func TestOutputViewportMouseModeFollowsOverlayOwner(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputMouse: "navigate", OutputLines: 100})
	visible := false
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	if !strings.Contains(out.String(), "\x1b[?1000h\x1b[?1006h") {
		t.Fatal("viewport did not request bounded mouse reports")
	}
	out.Reset()
	c.ComposeUI(func() []byte { visible = true; return []byte("\x1b7\x1b8") })
	if !strings.Contains(out.String(), "\x1b[?1000l\x1b[?1006l") {
		t.Fatal("suggestion overlay did not release viewport mouse capture")
	}
	out.Reset()
	c.ComposeUI(func() []byte { visible = false; return []byte("\x1b7\x1b8") })
	if !strings.Contains(out.String(), "\x1b[?1000h\x1b[?1006h") {
		t.Fatal("closing overlay did not restore viewport mouse capture")
	}
	out.Reset()
	c.Close()
	if !strings.Contains(out.String(), "\x1b[?1000l\x1b[?1006l") {
		t.Fatal("close leaked mouse mode")
	}
}

func TestPinnedViewportLeavesNativeMouseSelectionAvailable(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "selection", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("selection", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("selection", "prompt-end"))
	for i := 0; i < 30; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("line-%02d\r\n", i)))
	}
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) || c.viewport.frozen == nil {
		t.Fatal("Page Up did not enter older output")
	}
	visible := false
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	c.ComposeUI(func() []byte { visible = true; return []byte("\x1b7\x1b8") })
	c.ComposeUI(func() []byte { visible = false; return []byte("\x1b7\x1b8") })
	c.Resize(70, 12)
	if !c.HandleViewportInput([]byte("\x1b[F"), false) || c.viewport.frozen != nil {
		t.Fatal("End did not return to latest output")
	}
	c.Close()
	for _, mode := range []string{"9", "1000", "1001", "1002", "1003"} {
		if strings.Contains(out.String(), "\x1b[?"+mode+"h") {
			t.Fatalf("viewport enabled mouse tracking mode %s, preventing native drag selection", mode)
		}
	}
}

func TestOutputViewportAlternateScreenReleaseBoundaries(t *testing.T) {
	for _, boundary := range []string{"terminal-mode", "visual-recovery", "degradation", "tiny-geometry"} {
		t.Run(boundary, func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› input"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			switch boundary {
			case "terminal-mode":
				c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "terminal"})
			case "visual-recovery":
				c.recoverVisualPipeline()
			case "degradation":
				c.degradeVisualPipeline()
			case "tiny-geometry":
				c.Resize(60, 1)
			}
			screen := applyTerminalOutput(t, append([]byte("primary-history\r\n"), out.Bytes()...), 60, 12)
			if screen.IsAltScreen() || !strings.Contains(screenLine(screen, 0), "primary-history") {
				t.Fatal("release boundary did not restore saved primary screen")
			}
			if strings.LastIndex(out.String(), "\x1b[?1000l") < strings.LastIndex(out.String(), "\x1b[?1000h") || strings.LastIndex(out.String(), "\x1b[?1006l") < strings.LastIndex(out.String(), "\x1b[?1006h") {
				t.Fatal("release boundary left mouse reporting active")
			}
		})
	}
}

func TestOutputViewportRetainsApplicationExitOutput(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› application"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("\x1b[?1049happlication screen\x1b[?1049l\r\nresume-example\r\n"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	var visible strings.Builder
	for row := 0; row < 12; row++ {
		visible.WriteString(screenLine(screen, row))
	}
	if !strings.Contains(visible.String(), "resume-example") {
		t.Fatal("new prompt erased application exit output")
	}
}

func TestOutputViewportContainsNativeFullHeightMargins(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› application"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	out.Reset()
	native := "\x1b[1;12r\x1b[H\x1bMsentinel"
	c.WritePTY([]byte(native))
	if c.viewport == nil || c.visualRecoveries != 0 {
		t.Fatal("native full-height margins destroyed the retained output model")
	}
	if !strings.Contains(out.String(), native) {
		t.Fatal("native application bytes did not reach the terminal unchanged")
	}
}

func TestOutputViewportDisplaysPausedCarriageReturnImmediately(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	out.Reset()
	c.WritePTY([]byte("working\r"))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !terminalContainsLine(screen, "working") || c.viewportPassthrough {
		t.Fatal("paused carriage-return progress was delayed or released pinned ownership")
	}
	c.WritePTY([]byte("finished\r"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !terminalContainsLine(screen, "finished") || terminalContainsLine(screen, "working") || c.viewportPassthrough {
		t.Fatal("carriage-return progress did not overwrite its line inside the viewport")
	}
	c.WritePTY([]byte("\rOK\r"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !terminalContainsLine(screen, "OKnished") || !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("short progress update erased its untouched suffix or lost navigation")
	}
	c.HandleViewportInput([]byte("\x1b[F"), false)
	c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› next"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !terminalContainsLine(screen, "OKnished") || !terminalContainsLine(screen, "next") || !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("progress completion lost output, chrome, or managed navigation")
	}
}

func TestOutputViewportCRLFIsIndependentOfReadBoundaries(t *testing.T) {
	const text = "first-line\r\nsecond-line\r\n"
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› running"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
			out.Reset()
			for _, part := range []string{text[:split], text[split:]} {
				c.WritePTY([]byte(part))
				if c.viewportPassthrough {
					t.Fatal("ordinary line output released pinned ownership at a read boundary")
				}
			}
			screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
			if screenLine(screen, 0) != "first-line" || screenLine(screen, 1) != "second-line" || screenLine(screen, 2) != "" || !terminalContainsLine(screen, "running") {
				t.Fatal("split CRLF lost output or fixed input chrome")
			}
		})
	}
}

func TestDisabledCompositorRetainsCarriageReturnBytes(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "top", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned"})
	const text = "progress\rOK\r\nnext\r\n"
	for _, part := range []string{"progress\r", "OK\r", "\nnext\r\n"} {
		c.WritePTY([]byte(part))
	}
	if out.String() != text || c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("disabled compositor changed native output or navigation ownership")
	}
}

func TestOutputViewportNativeControlsRestoreOwnershipAfterPrompt(t *testing.T) {
	for _, parts := range [][]string{{"\x1b[2;1Hnative"}, {"\x1b[?1049", "happlication", "\x1b[?1049l"}} {
		t.Run(fmt.Sprintf("%q", parts), func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› application"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
			out.Reset()
			for _, part := range parts {
				c.WritePTY([]byte(part))
			}
			if strings.Count(out.String(), strings.Join(parts, "")) != 1 || c.HandleViewportInput([]byte("\x1b[5~"), false) {
				t.Fatal("explicit native controls were captured, duplicated, or lost")
			}
			c.WritePTY(terminalMarkerBytes("viewport", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
			c.WritePTY([]byte("› next"))
			c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
			if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
				t.Fatal("next prompt did not restore managed navigation")
			}
		})
	}
}

func TestTerminalOutputRetainsNativeCarriageReturn(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "terminal"})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	out.Reset()
	c.WritePTY([]byte("\rprogress"))
	if !strings.Contains(out.String(), "\rprogress") || c.viewport != nil {
		t.Fatal("terminal-mode output changed native carriage-return semantics")
	}
}

func TestOutputViewportSplitControlDoesNotReplayShadowText(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 80, 15)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("unique-output\x1b[3"))
	c.WritePTY([]byte("1mred\x1b[0m\r\n"))
	screen := strings.Join(terminalScreenLines(c.emulator), "\n")
	if strings.Count(screen, "unique-output") != 1 || !strings.Contains(screen, "unique-outputred") {
		t.Fatalf("split control replayed or corrupted shadow output: %q", screen)
	}
}

func TestOutputViewportRefreshPreservesActiveOverlay(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› active"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	dirty := true
	render := func() []byte {
		if !dirty {
			return nil
		}
		dirty = false
		return []byte("\x1b7\x1b[7;1Hsuggestion-visible\x1b8")
	}
	c.SetTransientUIReflow(nil, func(bool, int) { dirty = true }, render, nil, func() (int, int) { return 6, 1 }, func() bool { return true })
	c.ComposeUI(render)
	c.WriteNotification([]byte("notification\r\n"))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !terminalContainsLine(screen, "suggestion-visible") {
		t.Fatal("viewport refresh erased the active overlay")
	}
	if terminalContainsLine(c.backdrop, "suggestion-visible") {
		t.Fatal("transient overlay contaminated the saved output backdrop")
	}
	c.Resize(60, 13)
	screen = applyTerminalOutput(t, out.Bytes(), 60, 13)
	if !terminalContainsLine(screen, "suggestion-visible") || terminalContainsLine(c.backdrop, "suggestion-visible") {
		t.Fatal("prompt reflow lost overlay ownership or contaminated the backdrop")
	}
}

func TestOutputViewportPreservesSplitControlAcrossTinyResize(t *testing.T) {
	for _, height := range []int{1, 3} {
		for split := 1; split < len("\x1b[2J"); split++ {
			t.Run(fmt.Sprintf("height%d/split%d", height, split), func(t *testing.T) {
				var out bytes.Buffer
				c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
				t.Cleanup(c.Close)
				c.SetInputBoxTheme(testInputBoxTheme())
				c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
				c.WritePTY([]byte("› running"))
				c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
				c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
				out.Reset()
				c.WritePTY([]byte("\x1b[2J"[:split]))
				c.Resize(60, height)
				if height > 1 && !c.viewportPassthrough {
					t.Fatal("tiny resize did not relinquish viewport ownership")
				}
				c.WritePTY([]byte("\x1b[2J"[split:] + "sentinel"))
				if strings.Count(out.String(), "\x1b[2Jsentinel") != 1 || len(c.viewportControlTail) != 0 {
					t.Fatal("resize dropped a pending native control prefix")
				}
			})
		}
	}
}

func TestOutputViewportRetainsOutcomeForNativeApplicationSnapshot(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 80, 15)
	t.Cleanup(c.Close)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100, Scrollback: "snapshot", Status: terminalChatboxBarConfig{Right: []string{"duration", "exit"}}})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› application"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("\x1b[?1049happlication\x1b[?1049l\r\nresult\r\n"))
	now = now.Add(125 * time.Millisecond)
	out.Reset()
	c.WritePTY(terminalMarkerBytes("viewport", "command-end:7"))
	if strings.Count(out.String(), "exit 7") != 1 || strings.Count(out.String(), "125ms") != 1 {
		t.Fatal("native application snapshot lost its duration/exit outcome")
	}
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› next"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	screen := strings.Join(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 80, 15)), "\n")
	if !strings.Contains(screen, "next") || strings.Count(screen, "exit 7") != 1 || strings.Count(screen, "125ms") != 1 {
		t.Fatalf("next prompt erased or duplicated the native outcome: %q", screen)
	}
}

func TestOutputViewportPreservesNonvisualTerminalControls(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	for _, control := range []string{"\x1b]0;example-title\a", "\x1b]7;file://localhost/example\x1b\\", "\a", "\a\x1b]0;after-bell\a"} {
		out.Reset()
		if strings.HasPrefix(control, "\a\x1b]") {
			// Notifications share the output renderer without the PTY marker
			// stream splitting each OSC into a separate render operation.
			c.WriteNotification([]byte(control))
		} else {
			c.WritePTY([]byte(control))
		}
		if bytes.Count(out.Bytes(), []byte(control)) != 1 {
			t.Fatalf("control %q not forwarded exactly once", control)
		}
	}
	// Large OSC payloads are deliberately released by the bounded marker
	// stream in chunks; they must still reach the terminal intact.
	out.Reset()
	large := "\x1b]0;" + strings.Repeat("x", 5000)
	c.WritePTY([]byte(large))
	c.WritePTY([]byte("\a"))
	if strings.Count(out.String(), large+"\a") != 1 {
		t.Fatal("large split OSC was not forwarded exactly once")
	}
}

func TestOutputViewportRetainsHyperlinkCells(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› running"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	c.WritePTY([]byte("\x1b]8;;https://example.com\x1b\\linked\x1b]8;;\x1b\\"))
	// Inspect the native wire protocol, not the same VT parser used by the
	// output model: parsing twice can conceal swapped URL/parameter fields.
	if !strings.Contains(out.String(), "\x1b]8;;https://example.com\a") {
		t.Fatal("viewport rendering lost the native output hyperlink target")
	}
}

func TestOutputViewportPreservesCodexReconnectWireTarget(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 100, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› codex"))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("viewport", "command-start"))
	const id = "01a0fac4-73ff-7592-9184-47b8bea04ed5"
	const actionURL = "vuja://codex-resume/" + id + "?token=fixture&socket=fixture.sock"
	linkifier := newCodexResumeLinkifier(func(string) string { return actionURL })
	c.WritePTY(linkifier.Transform([]byte("To reconnect, run:\r\n  codex resume " + id + "\r\n")))
	if !strings.Contains(out.String(), "\x1b]8;id=vuja-codex-"+id+";"+actionURL+"\a") {
		t.Fatalf("expected the native terminal to receive the resume URL as its target, got %q", out.String())
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
	if !strings.Contains(screenLine(screen, 9), "printf lines") || !strings.Contains(screenLine(screen, 7), "/example/project") || !strings.Contains(screenLine(screen, 11), "Running") {
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
	if screenLine(screen, 0) != readingLine || !strings.Contains(screenLine(screen, 7), "New activity") {
		t.Fatal("new output did not preserve the reading position and show activity")
	}
	if !c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("Escape did not return to latest")
	}
	if c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("Escape was intercepted outside scrollback")
	}
	for _, key := range []string{"\x1b[F", "\x1b[4~", "\x1bOF"} {
		c.HandleViewportInput([]byte("\x1b[5~"), false)
		out.Reset()
		c.FilterViewportInput([]byte(key), false)
		if c.viewport.frozen != nil || !strings.Contains(out.String(), "new output") || strings.Contains(out.String(), "Back to bottom") {
			t.Fatalf("End encoding %q did not render latest output", key)
		}
	}
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	before := c.viewport.top
	c.FilterViewportInput([]byte("\x1b[6~"), false)
	if c.viewport.frozen == nil || c.viewport.top <= before {
		t.Fatal("PageDown did not advance toward newer rows")
	}
	for i := 0; i < 30 && c.viewport.frozen != nil; i++ {
		c.FilterViewportInput([]byte("\x1b[6~"), false)
	}
	if c.viewport.frozen != nil {
		t.Fatal("PageDown did not return to latest")
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
	paste := []byte("\x1b[200~" + strings.Join(viewportKeys, "") + "\x1b\x1b[201~")
	if !bytes.Equal(c.FilterViewportInput(paste, false), paste) {
		t.Fatal("runtime filter modified pasted navigation bytes")
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
			var framer terminalInputFilter
			first := framer.Filter([]byte(key[:split]))
			second := framer.Filter([]byte(key[split:]))
			if string(append(first, second...)) != key || len(framer.pending) != 0 {
				t.Fatalf("split %d corrupted %q", split, key)
			}
		}
	}
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "viewport", 60, 12)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("viewport", "prompt-end"))
	paste := []byte("\x1b[200~" + strings.Join(viewportKeys, "") + "\x1b[201~")
	for split := 1; split < len(bracketedPasteStart); split++ {
		var reports terminalInputFilter
		first := reports.FilterInput(paste[:split], c, false, true)
		second := reports.FilterInput(paste[split:], c, false, true)
		if !bytes.Equal(append(first, second...), paste) {
			t.Fatalf("fragmented paste opener at %d corrupted input", split)
		}
	}
	for split := 1; split < len(bracketedPasteEnd); split++ {
		reports := terminalInputFilter{paste: true}
		first := reports.FilterInput(bracketedPasteEnd[:split], c, false, true)
		second := reports.FilterInput(bracketedPasteEnd[split:], c, false, true)
		if len(first) != 0 || !bytes.Equal(second, bracketedPasteEnd) {
			t.Fatalf("fragmented paste closer at %d escaped framing", split)
		}
	}
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
	if !strings.Contains(out.String(), "New activity") {
		t.Fatal("background output lost while browsing")
	}
	c.Resize(40, 10)
	if !c.HandleViewportInput([]byte("\x1b[F"), false) {
		t.Fatal("resize lost scrollback mode")
	}
}
