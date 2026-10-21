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
	blockFocused // drawing-only; never stored as a completion outcome
)

// Only presentation ranges live here: no command text, executable identity or
// alternate-screen canvas. Logical positions belong to the retained VT owner.
type blockSpan struct {
	first, last uint64
	state       blockState
	divider     bool
}

// Checkpoints carry row-relative presentation boundaries, never execution IDs.
// Neutral boundaries matter: later prefix eviction must warn only that block.
type checkpointBlockSpan struct {
	First   int             `json:"first"`
	Last    int             `json:"last"`
	State   blockState      `json:"state"`
	Divider bool            `json:"divider,omitempty"`
	Card    *checkpointCard `json:"card,omitempty"`
}

// Optional numeric ranges describe text regions, not durable executions.
type checkpointCard struct {
	BodyFirst    int                   `json:"body_first"`
	BodyEnd      int                   `json:"body_end"`
	OutcomeFirst int                   `json:"outcome_first,omitempty"`
	OutcomeEnd   int                   `json:"outcome_end,omitempty"`
	Newline      bool                  `json:"newline,omitempty"`
	Native       bool                  `json:"native,omitempty"`
	Truncated    bool                  `json:"truncated,omitempty"`
	PaneOnly     bool                  `json:"pane_only,omitempty"`
	Notices      []checkpointTextRange `json:"notices,omitempty"`
}

type checkpointTextRange struct {
	First int `json:"first"`
	Last  int `json:"last"`
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
	v.cards.pending = &outputCard{first: v.blockStart, divider: c.chatboxConfig.BlockDividers}
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
		if b.First < skipped {
			b.Divider = false
		}
		b.First = max(b.First-skipped, 0)
		b.Last = min(b.Last-skipped, count-1)
		if b.Card != nil {
			card := *b.Card
			card.Truncated = card.Truncated || card.BodyFirst < skipped
			card.BodyFirst = clamp(card.BodyFirst-skipped, b.First, b.Last+1)
			card.BodyEnd = clamp(card.BodyEnd-skipped, card.BodyFirst, b.Last+1)
			if card.OutcomeEnd > 0 {
				card.OutcomeFirst = clamp(card.OutcomeFirst-skipped, b.First, b.Last+1)
				card.OutcomeEnd = clamp(card.OutcomeEnd-skipped, card.OutcomeFirst, b.Last+1)
			}
			card.Notices = nil
			for _, notice := range b.Card.Notices {
				start, end := max(notice.First-skipped, card.BodyFirst), min(notice.Last-skipped, card.BodyEnd-1)
				if start <= end {
					card.Notices = append(card.Notices, checkpointTextRange{First: start, Last: end})
				}
			}
			b.Card = &card
		}
		kept = append(kept, b)
	}
	return kept
}

// Both checkpoint sizing passes can retain a suffix within their first row.
// Whole-row offsets cannot represent that loss; compare semantic text so
// dropping only ANSI styling does not falsely truncate a complete card body.
func checkpointBlocksForRows(blocks []checkpointBlockSpan, original, rows []string) []checkpointBlockSpan {
	skipped := len(original) - len(rows)
	kept := checkpointBlockSuffix(blocks, skipped, len(rows))
	if len(rows) == 0 || ansi.Strip(original[skipped]) == ansi.Strip(rows[0]) {
		return kept
	}
	for i := range kept {
		b := &kept[i]
		if b.First != 0 {
			continue
		}
		if b.State == blockNeutral {
			b.State = blockWarning
		}
		if b.Card != nil && b.Card.BodyFirst == 0 && b.Card.BodyEnd > 0 {
			b.Card.Truncated = true
		}
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
	if v.cards.pending != nil {
		v.cards.pending.last = v.blockEnd
	}
}

func (c *terminalCompositor) finishBlockState(exit *int, incomplete bool) {
	if c.chatboxConfig.Scrollback != "blocks" {
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
	if !c.chatboxConfig.BlockStates {
		state = blockNeutral
	}
	if v := c.viewport; v != nil && v.blockActive {
		v.blockActive = false
		if v.blockEnd >= v.blockStart {
			v.blocks = append(v.blocks, blockSpan{first: v.blockStart, last: v.blockEnd, state: state, divider: v.cards.pending != nil && v.cards.pending.divider})
			if card := v.cards.pending; card != nil {
				card.state = state
				v.cards.completed = append(v.cards.completed, *card)
			}
			if v.frozenPane {
				for i := range v.frozenStates {
					row := v.frozenBase + uint64(i)
					if row >= v.blockStart && row <= v.blockEnd {
						v.frozenStates[i] = state
					}
				}
			}
		}
		v.cards.pending = nil
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
	return blockStateAt(v.blocks, row)
}

func blockStateAt(blocks []blockSpan, row uint64) blockState {
	i := sort.Search(len(blocks), func(i int) bool { return blocks[i].last >= row })
	if i < len(blocks) && blocks[i].first <= row {
		return blocks[i].state
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
	i = sort.Search(len(v.cards.completed), func(i int) bool { return v.cards.completed[i].last >= first })
	if i > 0 {
		v.cards.completed = append([]outputCard(nil), v.cards.completed[i:]...)
	}
	pruneNotices := func(card *outputCard) {
		i := sort.Search(len(card.notices), func(i int) bool { return card.notices[i].last >= first })
		if i > 0 {
			card.notices = append([]blockSpan(nil), card.notices[i:]...)
		}
	}
	// Ordered card ranges do not overlap; only the first surviving card can
	// straddle prefix eviction. Later notice ranges are still fully retained.
	if len(v.cards.completed) > 0 {
		pruneNotices(&v.cards.completed[0])
	}
	if card := v.cards.pending; card != nil {
		pruneNotices(card)
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
		span := checkpointBlockSpan{First: int(max(b.first, first) - first), Last: int(min(b.last-first, uint64(count-1))), State: b.state, Divider: b.divider && b.first >= first}
		i := sort.Search(len(v.cards.completed), func(i int) bool { return v.cards.completed[i].first >= b.first })
		if i < len(v.cards.completed) && v.cards.completed[i].first == b.first {
			span.Card = v.cards.completed[i].checkpoint(first, count)
		}
		blocks = append(blocks, span)
	}
	if card := v.cards.pending; card != nil && card.first >= first && card.first < first+uint64(count) {
		copy := *card
		copy.last, copy.bodyEnd = v.cursorLogicalRow(), v.cursorLogicalRow()
		if v.model.CursorPosition().X > 0 {
			copy.bodyEnd++
		}
		if a := v.executions.active; a != nil {
			copy.native = a.applicationOwned
			if !copy.native {
				r := a.snapshot()
				copy.newline, copy.truncated = r.newline, r.truncated
			}
		}
		blocks = append(blocks, checkpointBlockSpan{First: int(copy.first - first), Last: min(int(copy.last-first), count-1), State: blockWarning, Divider: copy.divider, Card: copy.checkpoint(first, count)})
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
	if state == blockFocused {
		color = c.inputBoxTheme.Accent
	}
	if color == "" {
		color = c.inputBoxTheme.Accent
	}
	// A 20% semantic tint keeps ordinary text readable while the solid rail
	// carries the stronger cue. ANSI terminal palettes use their actual color.
	tint := interpolateHexColor(c.inputBoxTheme.Background, color, 1, 5)
	if state == blockFocused {
		tint = interpolateHexColor(c.inputBoxTheme.Background, color, 1, 12)
	}
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
