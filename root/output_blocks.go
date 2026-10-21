package root

import (
	"fmt"
	"io"
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Like model-written spaces, this is private display metadata, not an action
// URL. A one-cell logical divider is expanded only while drawing, so resizing
// never turns a previous terminal-width rule into wrapped output rows.
const outputBlockDividerTarget = "vuja:block-divider"
const outputBlockDividerRow = "\x1b]8;;" + outputBlockDividerTarget + "\x1b\\─\x1b]8;;\x1b\\"

func isOutputBlockDivider(line string) bool {
	// Match the complete OSC 8 target, not a substring of an application's
	// hyperlink URL. The emulator may serialize its terminator as BEL or ST.
	const opening = "\x1b]8;;" + outputBlockDividerTarget
	return (strings.Contains(line, opening+"\x1b\\") || strings.Contains(line, opening+"\a")) && strings.TrimSpace(ansi.Strip(line)) == "─"
}

func (c *terminalCompositor) blockDividerLine() string {
	if !c.chatboxConfig.BlockDividers {
		return ""
	}
	return terminalDefaultBackground + "\x1b[2m" + terminalTrueColor("38", c.inputBoxTheme.Muted) + strings.Repeat("─", max(c.width, 1)) + "\x1b[0m"
}

func (c *terminalCompositor) blockCommandRows() []string {
	rows := make([]string, 0, len(c.surfaceContentLines)+1)
	if c.chatboxConfig.BlockDividers {
		rows = append(rows, outputBlockDividerRow)
	}
	// The shell emits its acknowledged history decision before command-start
	// in the same ordered PTY stream. Unknown/older hooks fail closed: command
	// labels must not bypass leading-space, ignore-pattern or paused privacy.
	if !c.blockCommandPublic {
		return append(rows, terminalDefaultBackground+c.chatboxConfig.Prompt+"[private command]\x1b[0m")
	}
	if c.inputWasClipped {
		rows = append(rows, terminalDefaultBackground+"[command input truncated to visible rows]\x1b[0m")
	}
	for i, line := range c.surfaceContentLines {
		if i < len(c.surfaceContentCells) {
			cells := slices.Clone(c.surfaceContentCells[i])
			for j := range cells {
				cells[j].Style.Bg = nil
				cells[j].Style.Attrs &^= uv.AttrReverse
			}
			line = cells.Render()
		}
		rows = append(rows, terminalDefaultBackground+line+"\x1b[0m")
	}
	return rows
}

// Native scrollback has no retained viewport to decorate. Only the command
// transition is ours; application output continues through the existing native
// path without a new emulator, mouse mode, or command-capture owner.
func (c *terminalCompositor) enterBlockCommandOutput() {
	var frame strings.Builder
	frame.WriteString(terminalSyncStart)
	top, rows := c.currentTransientUIRegion()
	c.clearAndDisableTransientUI(&frame)
	frame.Write(c.restoreBackdropRows(top, rows))
	baseStart := frame.Len()
	frame.WriteString("\x1b[?25l\x1b[r")
	top = clamp(c.height-max(c.surfaceRows, 1), 0, c.height-1)
	for row := top; row < c.height; row++ {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row+1)
	}
	fmt.Fprintf(&frame, "\x1b[%d;1H", top+1)
	for i, line := range c.blockCommandRows() {
		// Only the generator's injected first row is chrome. Command-label
		// cells may carry the same non-action hyperlink without being a rule.
		if i == 0 && c.chatboxConfig.BlockDividers {
			line = c.blockDividerLine()
		}
		frame.WriteString(line + "\x1b[0m\r\n")
	}
	frame.WriteString("\x1b[?25h")
	baseEnd := frame.Len()
	frame.WriteString(terminalSyncEnd)
	contents := frame.String()
	_, _ = io.WriteString(c.out, contents)
	c.recordBackdrop([]byte(contents[baseStart:baseEnd]))
	if c.pendingLineBreak {
		c.writeModel([]byte("\r\n"))
	}
	c.pendingLineBreak = false
	c.surfaceRows, c.surfaceContentRows = 0, 0
	c.surfaceContentLines, c.surfaceContentCells, c.renderedLines = nil, nil, nil
	c.transientGeometryDirty = true
	c.promptPrelude = ""
	c.commandCardOpen = true
	c.commandOutputDirect, c.commandOutputLineOpen, c.commandOutputStarted = false, false, false
	c.commandOutputPositioned = true
	c.commandOutputColumn = 0
}
