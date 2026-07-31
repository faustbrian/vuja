package root

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestInputBoxContentReappliesSurfaceAfterCanonicalStyleReset(t *testing.T) {
	compositor := newTerminalCompositor(&bytes.Buffer{}, "bottom", "test-session", 32, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	content := compositor.inputBoxContent("› prompt" + ansi.ResetStyle)
	_, afterReset, found := strings.Cut(content, ansi.ResetStyle)
	if !found || !strings.HasPrefix(afterReset, compositor.inputBoxSurfaceCode) {
		t.Fatalf("expected surface background to cover right input padding, got %q", content)
	}
}

func TestBottomCompositorKeepsSurfaceBackgroundAcrossShellStyleResets(t *testing.T) {
	var output bytes.Buffer
	const marker = "test-session"
	compositor := newTerminalCompositor(&output, "bottom", marker, 32, 8)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	t.Cleanup(compositor.Close)

	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	compositor.WritePTY([]byte("› "))
	compositor.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	compositor.WritePTY([]byte("\x1b[32mcd\x1b[49m hello world"))

	screen := applyTerminalOutput(t, output.Bytes(), 32, 8)
	row := terminalLineIndex(terminalScreenLines(screen), "hello world", 0)
	if row < 0 {
		t.Fatal("expected styled shell input in the managed surface")
	}
	for column := 1; column < 31; column++ {
		cell := screen.CellAt(column, row)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("expected managed background at column %d, got %+v", column, cell)
		}
		red, green, blue, _ := cell.Style.Bg.RGBA()
		if red != 0x2424 || green != 0x2525 || blue != 0x2828 {
			t.Fatalf("expected #242528 at column %d, got #%04x%04x%04x", column, red, green, blue)
		}
	}
}
