package root

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

type blockState int

const (
	blockNeutral blockState = iota
	blockWarning
	blockError
)

// Only presentation ranges live here: no command text, executable identity or
// alternate-screen canvas. Logical positions belong to the retained VT owner.
type blockSpan struct {
	first, last uint64
	state       blockState
}

// Checkpoints carry row-relative presentation boundaries, never execution IDs.
// Neutral boundaries matter: later prefix eviction must warn only that block.
type checkpointBlockSpan struct {
	First int        `json:"first"`
	Last  int        `json:"last"`
	State blockState `json:"state"`
}

func (c *terminalCompositor) blockGutter() int {
	if c.chatboxConfig.BlockStates && c.chatboxConfig.Scrollback == "blocks" && c.chatboxConfig.OutputViewport == "pinned" && c.width >= 4 {
		return 2
	}
	return 0
}

func (c *terminalCompositor) outputContentWidth() int { return max(c.width-c.blockGutter(), 1) }

func (v *outputViewport) cursorLogicalRow() uint64 {
	_, rows := v.model.LogicalRowPositions()
	return rows[v.model.CursorPosition().Y]
}

func (c *terminalCompositor) startBlockRows() {
	v := c.viewport
	// Separate commands cannot share a logical row, even when output lacked
	// LF and completed-command hides the outcome or spacing is zero.
	if v.model.CursorPosition().X > 0 {
		v.write([]byte("\r\n"))
	}
	v.blockStart, v.blockEnd, v.blockActive = v.cursorLogicalRow(), 0, true
	v.blockLossStart = v.text.losses
}

func checkpointBlockSuffix(blocks []checkpointBlockSpan, skipped, count int) []checkpointBlockSpan {
	var kept []checkpointBlockSpan
	for _, b := range blocks {
		if b.Last < skipped || b.First >= skipped+count {
			continue
		}
		if b.First < skipped && b.State == blockNeutral {
			b.State = blockWarning
		}
		b.First = max(b.First-skipped, 0)
		b.Last = min(b.Last-skipped, count-1)
		kept = append(kept, b)
	}
	return kept
}

func (c *terminalCompositor) endBlockRows() {
	if c.viewport == nil || !c.viewport.blockActive {
		return
	}
	v := c.viewport
	v.blockEnd = v.cursorLogicalRow()
	if v.model.CursorPosition().X == 0 && v.blockEnd > v.blockStart {
		v.blockEnd--
	}
}

func (c *terminalCompositor) finishBlockState(exit *int, incomplete bool) {
	if c.chatboxConfig.Scrollback != "blocks" || !c.chatboxConfig.BlockStates {
		if c.viewport != nil {
			c.viewport.blockActive = false
		}
		return
	}
	if v := c.viewport; v != nil && v.blockActive {
		// Pane retention also includes block labels and outcomes, so it can
		// lose this command's prefix while the output-only copy still fits.
		first, _ := v.model.LogicalRowPositions()
		incomplete = incomplete || first > v.blockStart || v.text.losses != v.blockLossStart
	}
	state := blockNeutral
	if incomplete || c.blockCommandPublic && c.inputWasClipped {
		state = blockWarning
	}
	// Shell exit codes have no universal warning category. Every known nonzero
	// outcome wins over Vuja's own incomplete-display warning.
	if exit != nil && *exit != 0 {
		state = blockError
	}
	if v := c.viewport; v != nil && v.blockActive {
		v.blockActive = false
		if v.blockEnd >= v.blockStart {
			v.blocks = append(v.blocks, blockSpan{v.blockStart, v.blockEnd, state})
			if v.frozenPane {
				for i := range v.frozenStates {
					row := v.frozenBase + uint64(i)
					if row >= v.blockStart && row <= v.blockEnd {
						v.frozenStates[i] = state
					}
				}
			}
		}
		v.dirty = true
		if !c.viewportPassthrough {
			c.renderOutputViewport()
			return
		}
	}
	if c.chatboxConfig.OutputViewport == "pinned" && !c.viewportPassthrough {
		return
	}
	if state != blockNeutral {
		label := "Warning · retained display is incomplete"
		if state == blockError {
			label = fmt.Sprintf("Failed · exit %d", *exit)
		}
		// Native terminal scrollback cannot be repainted after it leaves the
		// canvas. Only this post-command completion indicator belongs to Vuja.
		c.writeTerminal([]byte("\r\n" + c.blockStateLine(label, state, c.width) + "\r\n"))
	}
}

func (v *outputViewport) stateAt(row uint64) blockState {
	i := sort.Search(len(v.blocks), func(i int) bool { return v.blocks[i].last >= row })
	if i < len(v.blocks) && v.blocks[i].first <= row {
		return v.blocks[i].state
	}
	return blockNeutral
}

func (v *outputViewport) pruneBlockStates() {
	first, _ := v.model.LogicalRowPositions()
	i := sort.Search(len(v.blocks), func(i int) bool { return v.blocks[i].last >= first })
	if i > 0 {
		v.blocks = append([]blockSpan(nil), v.blocks[i:]...)
	}
	if len(v.blocks) > 0 && v.blocks[0].first < first && v.blocks[0].state == blockNeutral {
		v.blocks[0].state = blockWarning
	}
}

func (v *outputViewport) checkpointBlocks(count int) []checkpointBlockSpan {
	if count == 0 {
		return nil
	}
	first, _ := v.model.LogicalRowPositions()
	var blocks []checkpointBlockSpan
	for _, b := range v.blocks {
		if b.last < first || b.first >= first+uint64(count) {
			continue
		}
		blocks = append(blocks, checkpointBlockSpan{int(max(b.first, first) - first), int(min(b.last-first, uint64(count-1))), b.state})
	}
	return blocks
}

func (v *outputViewport) rowStates(count int) []blockState {
	first, _ := v.model.LogicalRowPositions()
	states := make([]blockState, count)
	for i := range states {
		states[i] = v.stateAt(first + uint64(i))
	}
	return states
}

func (c *terminalCompositor) blockStateLine(line string, state blockState, width int) string {
	inner := max(width-2, 0)
	line = ansi.Truncate(line, inner, "")
	if state == blockNeutral {
		return "  " + line
	}
	name := "session-warning"
	if state == blockError {
		name = "exit-failure"
	}
	color := c.chatboxConfig.Colors[name]
	if color == "" {
		color = c.inputBoxTheme.Accent
	}
	// A 20% semantic tint keeps ordinary text readable while the solid rail
	// carries the stronger cue. ANSI terminal palettes use their actual color.
	tint := interpolateHexColor(c.inputBoxTheme.Background, color, 1, 5)
	bg := terminalTrueColor("48", tint)
	background := ansi.XParseColor(tint)
	if index, err := strconv.Atoi(tint); err == nil && index >= 0 && index <= 15 {
		background = ansi.BasicColor(index)
	}
	// The SDK parses every SGR form into defensive drawing cells. The state
	// surface owns background/reverse, but never rewrites retained program text.
	if inner > 0 {
		drawing := uv.NewScreenBuffer(inner, 1)
		drawing.Method = ansi.GraphemeWidth
		uv.NewStyledString(line).Draw(drawing, uv.Rect(0, 0, inner, 1))
		cells := drawing.Line(0)
		for i := range cells {
			cells[i].Style.Bg = background
			cells[i].Style.Attrs &^= uv.AttrReverse
		}
		line = cells.Render()
	}
	pad := max(inner-ansi.StringWidth(line), 0)
	return bg + terminalTrueColor("38", color) + "▌\x1b[39m " + line + bg + strings.Repeat(" ", pad) + "\x1b[0m"
}
