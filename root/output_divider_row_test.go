package root

import (
	"strings"
	"testing"
)

func TestInteractiveBlocksPixelDividerConsumesNoTextRow(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "bad-command", "bad-output\r\n", "1")
	blocksExecution(c, "next-command", "next-output café 日本語\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	footer := terminalLineIndex(lines, "exit 1", 0)
	next := terminalLineIndex(lines, "next-command", 0)
	if footer < 0 || next != footer+1 {
		t.Fatalf("adjacent blocks must share a pixel boundary without a divider text row: %q", lines)
	}
	for _, row := range lines {
		if row == strings.Repeat("─", 60) {
			t.Fatal("a centered text glyph cannot implement a zero-pixel-gap divider")
		}
	}
}
