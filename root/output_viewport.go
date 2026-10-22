package root

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/vt"
)

// The output model never receives prompt or suggestion frames. Browsing freezes
// rendered rows, so background output and retention eviction cannot move the
// user's reading position. Both the live model and frozen view are bounded.
type outputViewport struct {
	model             *vt.Emulator
	native            *nativeOutputTail
	done              chan struct{}
	frozen            []string
	frozenLayout      []frozenOutputRow
	frozenWidth       int
	frozenTruncated   bool
	top               int
	newOutput         bool
	dirty             bool
	csiTail           []byte
	pointerX          int
	pointerY          int
	search            *outputSearch
	text              outputModelStream
	executions        outputExecutions
	restoredTruncated bool
	blocks            []blockSpan
	blockStart        uint64
	blockEnd          uint64
	blockActive       bool
	blockLossStart    uint64
	frozenBase        uint64
	frozenPane        bool
	frozenStates      []blockState
	frozenDividers    map[int]bool
	cards             outputCards
	revision          uint64
}

// The SDK owns live eviction, including writes made during restoration. Prior
// checkpoint loss is independent: its visible notice can itself be evicted.
func (v *outputViewport) outputTruncated() bool {
	return v.restoredTruncated || v.model.RetainedOutputTruncated()
}

func (v *outputViewport) write(data []byte) {
	data = sanitizeTerminalModelCSI(data, &v.csiTail, v.model.Width(), v.model.Height())
	v.text.write(v.model, data)
	v.revision++
}

var viewportKeys = []string{"\x1b[5~", "\x1b[6~", "\x1b[4~", "\x1b[F", "\x1bOF", "\x1b[B", "\x1bOB", "\x1b[18~", "\x1b[19~", "\x1b[19;2~", "\x1b[21~", "\x1b[21;2~", "\x1b[23~", "\x1b[24~"}

func (c *terminalCompositor) ViewportNavigationActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outputViewportEnabled() && !c.closed
}

func (c *terminalCompositor) outputViewportEnabled() bool {
	return c.enabled && c.layout && c.chatboxConfig.OutputViewport == "pinned" && !c.viewportPassthrough && c.surfaceRows > 0 && c.height > c.surfaceRows
}

func (c *terminalCompositor) ensureOutputViewport() {
	rows := max(c.height-c.surfaceRows, 1)
	if c.viewport == nil {
		model := vt.NewEmulator(c.width, rows)
		model.SetReflow(true)
		model.SetScrollbackSize(clamp(c.chatboxConfig.OutputLines, 1, 10000))
		v := &outputViewport{model: model, done: make(chan struct{}), dirty: true}
		c.viewport = v
		go func() { _, _ = io.Copy(io.Discard, model); close(v.done) }()
		c.restoreOutputRecovery()
	} else if c.viewport.model.Width() != c.width || c.viewport.model.Height() != rows {
		c.viewport.text.resize(c.viewport.model, c.width, rows)
		c.viewport.dirty = true
		c.viewport.revision++
	}
}

func (c *terminalCompositor) closeOutputViewport() {
	if c.viewport == nil {
		return
	}
	v := c.viewport
	c.viewport = nil
	if a := v.executions.active; a != nil {
		abandonTerminalModel(a.model, a.done)
	}
	abandonTerminalModel(v.model, v.done)
}

func (c *terminalCompositor) writeViewportOutput(data []byte) {
	c.writeViewportText(data, false)
}

func (c *terminalCompositor) writeViewportNotification(data []byte) {
	c.writeViewportText(data, true)
}

func (c *terminalCompositor) writeViewportText(data []byte, notification bool) {
	c.ensureOutputViewport()
	if len(data) > 0 {
		card, capture := c.viewport.cards.pending, c.viewport.executions.active
		if notification && card != nil && capture != nil && !capture.applicationOwned {
			// Operational notices belong to the pane, never the execution
			// capture. Isolated hard rows keep later PTY bytes from sharing them.
			if c.viewport.model.CursorPosition().X > 0 {
				c.recordViewportOutput([]byte("\r\n"))
			}
			first := c.viewport.cursorLogicalRow()
			c.recordViewportOutput(data)
			if c.viewport.model.CursorPosition().X > 0 {
				c.recordViewportOutput([]byte("\r\n"))
			}
			if end := c.viewport.cursorLogicalRow(); end > first {
				card.paneOnly = true
				if n := len(card.notices); n > 0 && card.notices[n-1].last+1 >= first {
					card.notices[n-1].last = end - 1
				} else {
					card.notices = append(card.notices, blockSpan{first: first, last: end - 1})
				}
			}
		} else {
			c.recordViewportOutput(data)
		}
		// Rendering cells must not swallow nonvisual terminal effects. OSC 8
		// hyperlinks remain in cells; titles, cwd, notifications and other OSC
		// effects retain their native terminal owner.
		var controls []byte
		for index := 0; index < len(data); index++ {
			if data[index] == '\x1b' && index+1 < len(data) && data[index+1] == ']' {
				if end, length := oscEnd(data, index+2); end >= 0 {
					selector, _, _ := bytes.Cut(data[index+2:end], []byte(";"))
					if !bytes.Equal(selector, []byte("8")) {
						controls = append(controls, data[index:end+length]...)
					}
					index = end + length - 1
					continue
				}
			}
			if data[index] == '\a' {
				controls = append(controls, '\a')
			}
		}
		if len(controls) > 0 {
			c.writeTerminal(controls)
		}
	}
	c.renderOutputViewport()
}

func (c *terminalCompositor) recordViewportOutput(data []byte) {
	c.viewport.write(data)
	c.viewport.pruneBlockStates()
	if c.outputRecovery != nil && len(data) > 0 {
		c.outputRecovery.dirty = true
	}
	c.viewport.dirty = true
	if c.viewport.frozen != nil || c.viewport.cards.reading != nil {
		c.viewport.newOutput = true
	}
}

// iTerm's initial-prompt marker clears soft alternate-screen mode even when
// the physical alternate buffer remains active. The managed prompt owns that
// semantic boundary; other OSC effects and all native prompt marks survive.
func withoutInitialPromptMarks(data []byte) []byte {
	var filtered []byte
	start := 0
	for index := 0; index+1 < len(data); index++ {
		if data[index] != '\x1b' || data[index+1] != ']' {
			continue
		}
		end, length := oscEnd(data, index+2)
		if end < 0 {
			break
		}
		payload := data[index+2 : end]
		if bytes.Equal(payload, []byte("133;A")) || bytes.HasPrefix(payload, []byte("133;A;")) {
			filtered = append(filtered, data[start:index]...)
			start = end + length
		}
		index = end + length - 1
	}
	if start == 0 {
		return data
	}
	return append(filtered, data[start:]...)
}

func (c *terminalCompositor) clearViewportOverlay() {
	var frame strings.Builder
	c.clearAndDisableTransientUI(&frame)
	_, _ = io.WriteString(c.out, frame.String())
}

func (c *terminalCompositor) suspendOutputViewport() {
	c.setViewportScreen(false)
	c.viewportPassthrough = true
	if c.viewport != nil {
		if c.viewport.native == nil {
			c.viewport.native = newNativeOutputTail(c.resumeLinkFor)
		}
		c.viewport.frozen = nil
		c.viewport.cards.reading = nil
		c.viewport.cards.history, c.viewport.cards.total, c.viewport.cards.browsing = 0, 0, false
		c.viewport.newOutput = false
		c.viewport.search = nil
		c.viewport.executions.selected = nil
		c.viewport.executions.notice = ""
	}
	c.writeTerminal([]byte("\x1b[r\x1b[?7h"))
	c.surfaceRows = 0
	c.renderedLines = nil
}

// The physical alternate screen belongs to Vuja, not to either shadow model.
// Native applications get the primary terminal back before their own controls
// are forwarded, avoiding nested alternate-screen save/restore semantics.
func (c *terminalCompositor) setViewportScreen(enabled bool) bool {
	if !enabled && c.viewport != nil {
		// Query ownership cannot outlive the visible managed surface.
		c.viewport.search = nil
		c.viewport.executions.selected = nil
		c.viewport.executions.notice = ""
		c.viewport.cards.releasePointer()
		c.viewport.cards.focus, c.viewport.cards.focused = nil, nil
	}
	if c.viewportAlternate == enabled {
		return false
	}
	c.invalidatePixelDividers()
	c.dividerPixels.screen++
	if !enabled {
		c.setViewportMouse(false)
	}
	c.viewportAlternate = enabled
	if enabled {
		_, _ = io.WriteString(c.out, "\x1b[?1049h")
		// A native application may have left tracking enabled when it exited.
		// The managed screen starts with a selection-safe pointer state; the
		// optional navigation mode re-enables its own tracking on render.
		_, _ = io.WriteString(c.out, "\x1b[?9l\x1b[?1000l\x1b[?1001l\x1b[?1002l\x1b[?1003l\x1b[?1006l")
		if c.viewport != nil {
			c.viewport.dirty = true
		}
	} else {
		_, _ = io.WriteString(c.out, "\x1b[?1049l")
	}
	return true
}

func (c *terminalCompositor) setViewportMouse(enabled bool) {
	// Xterm mouse tracking reports clicks as well as wheel input. With native
	// selection preferred, leave the pointer to the terminal even while the
	// pinned output viewport owns the screen.
	enabled = enabled && (c.chatboxConfig.OutputMouse == "navigate" || c.interactiveBlockMouse())
	if c.interactiveBlockMouse() && c.viewport != nil && (c.viewport.search != nil || c.viewport.frozen != nil) {
		// Text-only search/legacy snapshots have no card hit targets. Return
		// selection to the terminal instead of capturing and discarding drags.
		enabled = false
	}
	if !enabled {
		c.setViewportMotion(false)
		if c.viewportMouse && c.viewport != nil {
			c.viewport.cards.releasePointer()
		}
	}
	if c.viewportMouse == enabled {
		c.setViewportMotion(enabled && c.viewport != nil && c.viewport.frozen != nil)
		return
	}
	c.viewportMouse = enabled
	if enabled {
		if c.interactiveBlockMouse() {
			// Button-motion reports let ordinary drags select without modifiers.
			_, _ = io.WriteString(c.out, "\x1b[?1002h\x1b[?1006h")
		} else {
			_, _ = io.WriteString(c.out, "\x1b[?1000h\x1b[?1006h")
		}
		c.setViewportMotion(c.viewport != nil && c.viewport.frozen != nil)
	} else {
		_, _ = io.WriteString(c.out, "\x1b[?1002l\x1b[?1000l\x1b[?1006l")
	}
}

func (c *terminalCompositor) setViewportMotion(enabled bool) {
	if !enabled && c.viewport != nil && c.viewport.pointerX != 0 {
		c.viewport.pointerX, c.viewport.pointerY = 0, 0
		c.viewport.dirty = true
	}
	if c.viewportMotion == enabled {
		return
	}
	c.viewportMotion = enabled
	if enabled {
		_, _ = io.WriteString(c.out, "\x1b[?1003h")
	} else {
		_, _ = io.WriteString(c.out, "\x1b[?1003l")
		// Tracking protocols are mutually exclusive in terminals such as iTerm.
		// Leaving all-motion mode must restore normal reporting for wheel input.
		if c.viewportMouse {
			mode := "1000"
			if c.interactiveBlockMouse() {
				mode = "1002"
			}
			_, _ = fmt.Fprintf(c.out, "\x1b[?%sh", mode)
		}
	}
}

func (c *terminalCompositor) viewportBadge() (string, int, int) {
	label := " ↓ Back to bottom · esc "
	if c.viewport.search != nil {
		label = c.viewport.search.label(c.width, c.viewport.frozenTruncated, c.viewport.newOutput, c.viewport.executions.notice)
	} else if execution := c.viewport.executions.label(); execution != "" {
		label = execution
	} else if c.viewport.frozenTruncated {
		label = " Retention limit reached · ↓ Latest · esc "
	}
	if c.viewport.newOutput && c.viewport.search == nil {
		selected := c.viewport.executions.selected
		if c.viewport.frozenTruncated || selected != nil && (selected.truncated || selected.applicationOwned) {
			label += " · New activity"
		} else {
			label = " New activity ·" + label
		}
	}
	label = ansi.Cut(label, 0, c.width)
	width := ansi.StringWidth(label)
	return label, (c.width - width) / 2, width
}

func (c *terminalCompositor) viewportBadgeHovered() bool {
	_, left, width := c.viewportBadge()
	return c.viewport.pointerY == c.viewportBadgeRow() && c.viewport.pointerX > left && c.viewport.pointerX <= left+width
}

func (v *outputViewport) toolbarActive() bool {
	return v.search != nil || v.executions.selected != nil || v.executions.notice != ""
}

func (c *terminalCompositor) viewportContentRows() int {
	rows := c.height - c.surfaceRows
	if c.viewport != nil && c.viewport.toolbarActive() {
		rows--
	}
	return max(rows, 0)
}

// Use the title row when present so the badge touches the input surface.
func (c *terminalCompositor) viewportBadgeRow() int {
	row := c.height - c.surfaceRows
	if c.viewport.toolbarActive() {
		return row
	}
	if c.inputBoxTitleEnabled() && c.inputBoxDecorationRows() > 0 {
		row++
	}
	return row
}

func (c *terminalCompositor) viewportBadgeTitleLine() string {
	if c.viewport.frozen == nil && c.viewport.cards.reading == nil || c.viewport.toolbarActive() {
		return c.inputBoxTitleLine()
	}
	_, start, width := c.viewportBadge()
	left, _, right := splitStatusSegments(c.inputBoxBarSegments(c.chatboxConfig.Title))
	leftText := c.renderStatusSegmentsWithin(left, max(start-terminalInputHorizontalPadding, 0))
	rightText := c.renderStatusSegmentsWithin(right, max(c.width-start-width-terminalInputHorizontalPadding, 0))
	// The temporary navigation badge owns the center; metadata side groups get
	// bounded space rather than being partially overwritten by the badge.
	return terminalDefaultBackground + strings.Repeat(" ", terminalInputHorizontalPadding) +
		leftText + terminalDefaultBackground + strings.Repeat(" ", max(c.width-2*terminalInputHorizontalPadding-ansi.StringWidth(leftText)-ansi.StringWidth(rightText), 0)) +
		rightText + terminalDefaultBackground + strings.Repeat(" ", terminalInputHorizontalPadding)
}

// Fit each side of the frozen viewport badge using the same semantic priorities
// as the ordinary title, rather than truncating away its highest-value suffix.
func (c *terminalCompositor) renderStatusSegmentsWithin(segments []terminalStatusSegment, width int) string {
	if width <= 0 {
		return ""
	}
	for len(segments) > 1 && statusSegmentWidth(segments, c.chatboxConfig.Separator) > width {
		remove := lowestPriorityStatusSegment(segments)
		segments = append(segments[:remove], segments[remove+1:]...)
	}
	return ansi.Truncate(c.renderStatusSegments(segments), width, "…")
}

func (c *terminalCompositor) appendViewportSnapshot() {
	if c.chatboxConfig.Scrollback == "blocks" {
		c.startBlockRows()
		c.recordViewportOutput([]byte(strings.Join(c.blockCommandRows(), "\x1b[0m\r\n") + "\x1b[0m\r\n"))
		c.beginCardOutput()
		c.renderOutputViewport()
		return
	}
	if c.chatboxConfig.Scrollback != "snapshot" {
		return
	}
	var lines []string
	if c.inputBoxTitleEnabled() && c.completedSnapshotMetadataVisible() {
		lines = append(lines, c.completedExecutionHeaderLine())
	}
	if c.inputBoxPadded() {
		lines = append(lines, c.completedInputBoxPaddingLine())
		for _, cells := range c.surfaceContentCells {
			lines = append(lines, c.completedInputBoxContent(c.completedSurfaceContentLine(cells)))
		}
		lines = append(lines, c.completedInputBoxPaddingLine())
	} else {
		lines = append(lines, c.surfaceContentLines...)
	}
	if c.inputBoxStatusEnabled() && c.completedSnapshotMetadataVisible() {
		lines = append(lines, c.completedExecutionContextLines()...)
	}
	c.writeViewportOutput([]byte(strings.Join(lines, "\x1b[0m\r\n") + "\x1b[0m\r\n"))
}

func (c *terminalCompositor) appendViewportOutcome(exitCode *int) {
	if (c.chatboxConfig.Scrollback != "snapshot" && c.chatboxConfig.Scrollback != "blocks") || exitCode == nil || c.completedCommandMode() == "command" {
		return
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	lines := c.completedExecutionOutcomeLines(max(now().Sub(c.commandStartedAt), 0), *exitCode)
	if c.viewport.cards.pending != nil {
		c.viewport.cards.pending.outcome = append([]string(nil), lines...)
		c.viewport.cards.pending.outcomeFirst = c.viewport.cursorLogicalRow() + 1
		c.viewport.cards.pending.outcomeEnd = c.viewport.cards.pending.outcomeFirst + uint64(len(lines))
	}
	c.writeCompletedViewportOutput([]byte("\r\n" + strings.Join(lines, "\x1b[0m\r\n") + "\x1b[0m\r\n"))
}

// Native applications retain display ownership until the next prompt. Their
// completion metadata must reach both that display and the retained history.
func (c *terminalCompositor) writeCompletedViewportOutput(data []byte) {
	if c.viewportPassthrough {
		c.viewport.write(data)
		c.viewport.dirty = true
		c.writeTerminal(data)
		return
	}
	c.writeViewportOutput(data)
}

func viewportLine(model *vt.Emulator, y, width int, history bool) string {
	line := uv.NewLine(width)
	for x := 0; x < width; x++ {
		var cell *uv.Cell
		if history {
			cell = model.ScrollbackCellAt(x, y)
		} else {
			cell = model.CellAt(x, y)
		}
		if cell != nil && cell.Width > 0 {
			line.Set(x, cell)
		}
	}
	return line.Render()
}

func (c *terminalCompositor) renderOutputViewport() {
	if !c.outputViewportEnabled() {
		c.setViewportMouse(false)
		return
	}
	c.setViewportScreen(true)
	c.setViewportMouse(c.viewport != nil && c.viewport.search != nil || c.transientUIVisible == nil || !c.transientUIVisible())
	c.ensureOutputViewport()
	v := c.viewport
	if v.frozen != nil && v.frozenWidth != c.outputContentWidth() {
		v.layoutFrozen(c.outputContentWidth())
		if v.search != nil && v.search.index >= 0 {
			c.revealOutputSearchMatch()
		}
	}
	c.setViewportMotion(c.viewportMouse && v.frozen != nil)
	if !v.dirty {
		return
	}
	v.dirty = false
	rows := c.height - c.surfaceRows
	var frame strings.Builder
	frame.WriteString(terminalSyncStart + "\x1b7\x1b[?25l\x1b[?7l")
	var projected []vt.RetainedViewRow
	cardRows, usingCards := c.layoutOutputCards(c.viewportContentRows())
	var liveRows []uint64
	if !usingCards && v.frozen == nil && c.blockGutter() > 0 {
		projected = v.model.RetainedTail(c.outputContentWidth(), rows)
	} else if !usingCards && v.frozen == nil {
		_, liveRows = v.model.LogicalRowPositions()
	}
	for row := 0; row < rows; row++ {
		line := ""
		state := blockNeutral
		divider := false
		if usingCards {
			if row < len(cardRows) {
				line = c.paintCardRow(cardRows[row], row)
			}
		} else if v.frozen != nil {
			index := v.top + row
			if index < len(v.frozenLayout) && row < c.viewportContentRows() {
				span := v.frozenLayout[index]
				divider = v.frozenDividers[span.logical]
				if span.logical < len(v.frozenStates) {
					state = v.frozenStates[span.logical]
				}
				line = c.outputSearchLine(v.frozen[span.logical], span.logical, span.first, span.last, c.blockGutter() > 0 && state != blockNeutral)
			}
		} else if c.blockGutter() > 0 {
			if row < len(projected) {
				line = projected[row].Line.Render()
				state = v.stateAt(projected[row].Logical)
			}
		} else {
			line = viewportLine(v.model, row, c.width, false)
		}
		switch {
		case row < len(projected):
			divider = v.paneDividerAt(projected[row].Logical, line)
		case row < len(liveRows):
			divider = v.paneDividerAt(liveRows[row], line)
		}
		if divider && !usingCards {
			line = c.blockDividerLine()
		}
		if !usingCards && c.blockGutter() > 0 && !divider {
			line = c.blockStateLine(line, state, c.width)
		}
		if strings.Contains(line, "vuja://codex-resume/") {
			line = bareCodexResumeLink.ReplaceAllString(line, "")
		}
		if strings.Contains(line, "vuja://claude-resume/") {
			line = bareClaudeResumeLink.ReplaceAllString(line, "")
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row+1, line)
	}
	frame.WriteString("\x1b8\x1b[?7h\x1b[?25h" + terminalSyncEnd)
	data := []byte(frame.String())
	if c.viewportCommand || v.cards.restoreInput {
		start, statusLines := c.commandStatusLines()
		copy(c.renderedLines[start:], statusLines)
		// The shell is busy: keep its submitted command and metadata visible,
		// rather than treating command output as a new editable prompt.
		var chrome strings.Builder
		chrome.WriteString(terminalSyncStart + "\x1b7\x1b[?7l")
		for i, line := range c.renderedLines {
			row := rows + i
			if row >= c.height {
				break
			}
			fmt.Fprintf(&chrome, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row+1, ansi.Cut(line, 0, c.width))
		}
		chrome.WriteString("\x1b8\x1b[?7h" + terminalSyncEnd)
		data = append(data, []byte(chrome.String())...)
		v.cards.restoreInput = false
	}
	// Paint after busy chrome too, and restore the unobstructed title at latest.
	var badge strings.Builder
	badge.WriteString(terminalSyncStart + "\x1b7\x1b[?7l")
	badgeRow := c.viewportBadgeRow()
	if c.inputBoxTitleEnabled() && c.inputBoxDecorationRows() > 0 {
		fmt.Fprintf(&badge, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", rows+1, c.viewportBadgeTitleLine())
	}
	if v.frozen != nil || v.cards.reading != nil || v.toolbarActive() {
		label, left, _ := c.viewportBadge()
		if badgeRow == rows {
			fmt.Fprintf(&badge, "\x1b[%d;1H\x1b[0m\x1b[2K", badgeRow)
		}
		foreground, background := terminalTrueColor("38", c.inputBoxTheme.Border), c.inputBoxSurfaceCode
		if c.viewportBadgeHovered() {
			foreground, background = terminalTrueColor("38", c.inputBoxTheme.SurfaceBackground), terminalTrueColor("48", c.inputBoxTheme.Border)
		}
		fmt.Fprintf(&badge, "\x1b[%d;%dH%s%s%s\x1b[0m", badgeRow, left+1, foreground, background, label)
	}
	badge.WriteString("\x1b8\x1b[?7h" + terminalSyncEnd)
	data = append(data, []byte(badge.String())...)
	if selection := v.cards.selection; selection != nil && selection.dragged {
		var inputSelection strings.Builder
		inputSelection.WriteString(terminalSyncStart + "\x1b7")
		for y := rows; y < min(len(selection.frame), c.height); y++ {
			fmt.Fprintf(&inputSelection, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", y+1, c.paintCardRow(selection.frame[y], y))
		}
		inputSelection.WriteString("\x1b8" + terminalSyncEnd)
		data = append(data, []byte(inputSelection.String())...)
	}
	_, _ = c.out.Write(data)
	// Save the visible output for suggestion-overlay restoration, never feed the
	// frozen view back into the live output model.
	c.recordBackdrop(data)
	// Transient UI owns the topmost layer, but never the retained backdrop.
	// A viewport repaint invalidates even otherwise unchanged overlay rows.
	if v.search == nil && c.transientUIVisible != nil && c.transientUIVisible() && c.renderTransientUI != nil {
		if c.setTransientUIGeometry != nil {
			c.setTransientUIGeometry(true, c.surfaceRows)
		}
		_, _ = c.out.Write(c.renderTransientUI())
	}
	c.renderPixelDividers(cardRows, usingCards)
}

// HandleViewportInput consumes only complete dedicated navigation events.
// Paste, mixed input, shell editing and application input retain their owner.
func (c *terminalCompositor) HandleViewportInput(data []byte, suggestions bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.outputViewportEnabled() || bytes.Contains(data, bracketedPasteStart) {
		return false
	}
	if c.handleCardInput(data, suggestions) {
		return true
	}
	if c.viewport != nil && c.viewport.search != nil {
		// A pending suggestion result does not transfer modal pointer ownership.
		suggestions = false
	}
	wheel, mouse := viewportWheel(data, c.width, c.height-c.surfaceRows)
	if mouse && !suggestions && wheel == 0 && c.viewport != nil && c.viewport.frozen != nil {
		button, x, y, pressed, _ := viewportMouseReport(data)
		_, left, width := c.viewportBadge()
		hover := y == c.viewportBadgeRow() && x > left && x <= left+width
		if pressed && button&^28 == 0 && hover {
			c.viewport.frozen = nil
			c.viewport.search = nil
			c.viewport.executions.selected = nil
			c.viewport.executions.notice = ""
			c.viewport.newOutput = false
			c.viewport.pointerX, c.viewport.pointerY = 0, 0
			c.viewport.dirty = true
			c.renderOutputViewport()
		} else if button&32 != 0 && button&64 == 0 {
			wasHovered := c.viewportBadgeHovered()
			c.viewport.pointerX, c.viewport.pointerY = x, y
			if wasHovered != hover {
				c.viewport.dirty = true
				c.renderOutputViewport()
			}
		}
		return true
	}
	if mouse && (suggestions || wheel == 0) {
		// Button reports and wheel over chrome/menu must not become shell input.
		return true
	}
	if suggestions && (c.viewport == nil || c.viewport.search == nil) && string(data) != "\x1b[18~" {
		return false
	}
	c.ensureOutputViewport()
	v := c.viewport
	if !mouse && c.handleOutputExecution(data) {
		return true
	}
	if !mouse && c.handleOutputSearch(data) {
		return true
	}
	page := max(c.height-c.surfaceRows-1, 1)
	key := string(data)
	if wheel != 0 {
		page = 3
		if wheel < 0 {
			key = "\x1b[5~"
		} else {
			key = "\x1b[6~"
		}
	}
	switch key {
	case "\x1b[5~":
		c.freezeOutputViewport()
		v.top = max(v.top-page, 0)
	case "\x1b[6~":
		if v.frozen == nil {
			return mouse
		}
		v.top += page
		if v.top >= max(len(v.frozenLayout)-c.viewportContentRows(), 0) {
			v.frozen = nil
			v.newOutput = false
		}
	case "\x1b", "\x1b[F", "\x1b[4~", "\x1bOF", "\x1b[B", "\x1bOB":
		if v.frozen == nil {
			return false
		}
		v.frozen = nil
		v.newOutput = false
	default:
		return false
	}
	v.dirty = true
	if v.frozen == nil {
		v.search = nil
		v.executions.selected = nil
		v.executions.notice = ""
		v.pointerX, v.pointerY = 0, 0
	}
	c.renderOutputViewport()
	return true
}

// SGR reports are bounded, complete CSI events supplied by the input framer.
func viewportWheel(data []byte, width, rows int) (int, bool) {
	button, x, y, pressed, valid := viewportMouseReport(data)
	if !valid {
		return 0, false
	}
	if x > width || y > rows || !pressed {
		return 0, true
	}
	switch button &^ 28 {
	case 64:
		return -1, true
	case 65:
		return 1, true
	}
	return 0, true
}

func viewportMouseReport(data []byte) (button, x, y int, pressed, valid bool) {
	if !bytes.HasPrefix(data, []byte("\x1b[<")) || len(data) < 9 || (data[len(data)-1] != 'M' && data[len(data)-1] != 'm') {
		return
	}
	parts := strings.Split(string(data[3:len(data)-1]), ";")
	if len(parts) != 3 {
		return
	}
	var err, xerr, yerr error
	button, err = strconv.Atoi(parts[0])
	x, xerr = strconv.Atoi(parts[1])
	y, yerr = strconv.Atoi(parts[2])
	if err != nil || xerr != nil || yerr != nil || button < 0 || x < 1 || y < 1 {
		return
	}
	return button, x, y, data[len(data)-1] == 'M', true
}

// Filter complete navigation tokens without dropping adjacent typed bytes or
// treating a paste payload as terminal controls.
func (c *terminalCompositor) FilterViewportInput(data []byte, suggestions bool) []byte {
	if bytes.Contains(data, bracketedPasteStart) {
		return data
	}
	if string(data) == "\x1b" {
		if c.HandleViewportInput(data, suggestions) {
			return nil
		}
		return data
	}
	var result []byte
	for index := 0; index < len(data); {
		length := 0
		for _, key := range viewportKeys {
			if bytes.HasPrefix(data[index:], []byte(key)) {
				length = len(key)
				break
			}
		}
		if length > 0 && c.HandleViewportInput(data[index:index+length], suggestions) {
			index += length
			continue
		}
		result = append(result, data[index])
		index++
	}
	return result
}
