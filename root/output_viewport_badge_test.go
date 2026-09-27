package root

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func frozenBadgeFixture(t *testing.T) (*terminalCompositor, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	c := newTerminalCompositor(out, "bottom", "badge", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100, SurfaceWidth: "full-width"})
	c.WritePTY(terminalMarkerBytes("badge", "prompt-start"))
	c.WritePTY([]byte("› input"))
	c.WritePTY(terminalMarkerBytes("badge", "prompt-end"))
	for i := 0; i < 30; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("line-%02d\r\n", i)))
	}
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	return c, out
}

func TestViewportBadgeCenteredSurfaceHoverAndClick(t *testing.T) {
	c, out := frozenBadgeFixture(t)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 12)
	row := 12 - c.surfaceRows - 1
	const label = " ↓ Back to bottom · esc "
	line := screenLine(screen, row)
	start := strings.Index(line, strings.TrimRight(label, " "))
	if start < 0 || start != (80-len([]rune(label)))/2 {
		t.Fatalf("badge not compact and centered: %q", line)
	}
	normal := screen.CellAt(start+1, row).Style
	c.setViewportMouse(false)
	c.setViewportMouse(true)
	if strings.Count(out.String(), "\x1b[?1003h") != 2 {
		t.Fatal("hover tracking was not restored after overlay ownership")
	}
	input := screen.CellAt(2, row+2).Style
	if normal.Bg == nil || !reflect.DeepEqual(normal.Bg, input.Bg) {
		t.Fatal("badge background differs from chatbox")
	}
	if !c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<35;%d;%dM", start+2, row+1)), false) {
		t.Fatal("hover leaked to shell")
	}
	screen = applyTerminalOutput(t, out.Bytes(), 80, 12)
	hover := screen.CellAt(start+1, row).Style
	if !reflect.DeepEqual(hover.Fg, normal.Bg) || !reflect.DeepEqual(hover.Bg, normal.Fg) {
		t.Fatal("hover did not invert foreground/background")
	}
	visible := false
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	c.ComposeUI(func() []byte { visible = true; return []byte("\x1b7\x1b8") })
	c.ComposeUI(func() []byte { visible = false; return []byte("\x1b7\x1b8") })
	screen = applyTerminalOutput(t, out.Bytes(), 80, 12)
	if !reflect.DeepEqual(screen.CellAt(start+1, row).Style, normal) {
		t.Fatal("restored ownership retained stale hover")
	}
	c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<35;%d;%dM", start+2, row+1)), false)
	c.HandleViewportInput([]byte("\x1b[<35;1;1M"), false)
	screen = applyTerminalOutput(t, out.Bytes(), 80, 12)
	if !reflect.DeepEqual(screen.CellAt(start+1, row).Style, normal) {
		t.Fatal("hover did not reset outside badge")
	}
	c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<0;%d;%dM", start+2, row+1)), false)
	if c.viewport.frozen != nil {
		t.Fatal("badge click did not return to latest")
	}
	if !strings.Contains(out.String(), "\x1b[?1003h") || !strings.Contains(out.String(), "\x1b[?1003l") {
		t.Fatal("hover motion ownership was not released")
	}
}

func TestViewportDownReturnsToLatestOnlyWithoutSuggestions(t *testing.T) {
	for _, key := range []string{"\x1b[B", "\x1bOB"} {
		c, _ := frozenBadgeFixture(t)
		if c.HandleViewportInput([]byte(key), true) || c.viewport.frozen == nil {
			t.Fatal("Down stole suggestion navigation")
		}
		if !c.HandleViewportInput([]byte(key), false) || c.viewport.frozen != nil {
			t.Fatal("Down did not return to latest")
		}
		if c.HandleViewportInput([]byte(key), false) {
			t.Fatal("Down stole shell/history navigation at latest")
		}
	}
}

func TestViewportBadgeResizeUpdatesHoverGeometry(t *testing.T) {
	c, out := frozenBadgeFixture(t)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 12)
	row := 12 - c.surfaceRows - 1
	start := strings.Index(screenLine(screen, row), " ↓ Back to bottom")
	normal := screen.CellAt(start+1, row).Style
	c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<35;%d;%dM", start+2, row+1)), false)
	out.Reset()
	c.Resize(120, 12)
	screen = applyTerminalOutput(t, out.Bytes(), 120, 12)
	start = strings.Index(screenLine(screen, row), " ↓ Back to bottom")
	if start < 0 || !reflect.DeepEqual(screen.CellAt(start+1, row).Style, normal) {
		t.Fatal("relocated badge retained hover from old pointer position")
	}
}

func TestViewportReturnRestoresWheelReporting(t *testing.T) {
	for _, key := range []string{"\x1b[B", "\x1bOB", "\x1b[F", "\x1b", "\x1b[6~", "click"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			c, out := frozenBadgeFixture(t)
			out.Reset()
			if key == "click" {
				_, left, _ := c.viewportBadge()
				key = fmt.Sprintf("\x1b[<0;%d;%dM", left+2, c.height-c.surfaceRows)
			}
			for i := 0; c.viewport.frozen != nil && i < 20; i++ {
				c.HandleViewportInput([]byte(key), false)
			}
			if c.viewport.frozen != nil || !strings.Contains(out.String(), "\x1b[?1003l\x1b[?1000h") {
				t.Fatal("return to latest did not restore mutually exclusive normal mouse tracking")
			}
			if !c.HandleViewportInput([]byte("\x1b[<64;2;2M"), false) || c.viewport.frozen == nil {
				t.Fatal("second wheel browsing cycle did not open history")
			}
		})
	}
}

func TestViewportBadgeSitsFlushWithChatboxWhenTitleIsShown(t *testing.T) {
	c, out := frozenBadgeFixture(t)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100, SurfaceWidth: "full-width", Title: terminalChatboxBarConfig{Left: []string{"directory"}}})
	c.SetInputBoxPath("/fixture/project")
	c.Resize(80, 12)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 12)
	badgeRow := -1
	for row := 0; row < 12; row++ {
		if strings.Contains(screenLine(screen, row), "↓ Back to bottom") {
			badgeRow = row
		}
	}
	if badgeRow < 0 || !strings.Contains(screenLine(screen, badgeRow), "/fixture/project") {
		t.Fatal("badge must share the directory row rather than sit above it")
	}
	if !reflect.DeepEqual(screen.CellAt(40, badgeRow).Style.Bg, screen.CellAt(40, badgeRow+1).Style.Bg) {
		t.Fatal("badge and chatbox must touch with no terminal-background row between them")
	}
	normal := screen.CellAt(40, badgeRow).Style
	c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<35;40;%dM", badgeRow+1)), false)
	screen = applyTerminalOutput(t, out.Bytes(), 80, 12)
	if !reflect.DeepEqual(screen.CellAt(40, badgeRow).Style.Bg, normal.Fg) {
		t.Fatal("hover target did not move with badge")
	}
	c.SetInputBoxPath("/fixture/changed")
	screen = applyTerminalOutput(t, out.Bytes(), 80, 12)
	if !strings.Contains(screenLine(screen, badgeRow), "Back to bottom") || !strings.Contains(screenLine(screen, badgeRow), "/fixture/changed") {
		t.Fatal("title refresh must preserve the frozen badge")
	}
	c.Resize(60, 12)
	c.WriteNotification([]byte("new activity\r\n"))
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	line := screenLine(screen, badgeRow)
	if !strings.Contains(line, "New activity") || !strings.Contains(line, "/fixtur…") {
		t.Fatalf("narrow activity badge must reserve space and shorten directory: %q", line)
	}
	c.HandleViewportInput([]byte(fmt.Sprintf("\x1b[<0;40;%dM", badgeRow+1)), false)
	screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
	if c.viewport.frozen != nil || strings.Contains(screenLine(screen, badgeRow), "Back to bottom") || !strings.Contains(screenLine(screen, badgeRow), "/fixture/changed") {
		t.Fatal("return to latest must restore the title row")
	}
}
