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
	"github.com/charmbracelet/x/vt"
)

// The output model never receives prompt or suggestion frames. Browsing freezes
// rendered rows, so background output and retention eviction cannot move the
// user's reading position. Both the live model and frozen view are bounded.
type outputViewport struct {
	model     *vt.Emulator
	done      chan struct{}
	frozen    []string
	top       int
	newOutput bool
	dirty     bool
	csiTail   []byte
	pointerX  int
	pointerY  int
}

func (v *outputViewport) write(data []byte) {
	data = sanitizeTerminalModelCSI(data, &v.csiTail, v.model.Width(), v.model.Height())
	_, _ = v.model.Write(data)
}

var viewportKeys = []string{"\x1b[5~", "\x1b[6~", "\x1b[4~", "\x1b[F", "\x1bOF", "\x1b[B", "\x1bOB"}

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
		model.SetScrollbackSize(clamp(c.chatboxConfig.OutputLines, 1, 10000))
		v := &outputViewport{model: model, done: make(chan struct{}), dirty: true}
		c.viewport = v
		go func() { _, _ = io.Copy(io.Discard, model); close(v.done) }()
	} else if c.viewport.model.Width() != c.width || c.viewport.model.Height() != rows {
		c.viewport.model.Resize(c.width, rows)
		_, _ = c.viewport.model.Write([]byte("\x1b[r"))
		c.viewport.dirty = true
	}
}

func (c *terminalCompositor) closeOutputViewport() {
	if c.viewport == nil {
		return
	}
	v := c.viewport
	c.viewport = nil
	abandonTerminalModel(v.model, v.done)
}

func (c *terminalCompositor) writeViewportOutput(data []byte) {
	c.ensureOutputViewport()
	if len(data) > 0 {
		c.recordViewportOutput(data)
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
	c.viewport.dirty = true
	if c.viewport.frozen != nil {
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
		c.viewport.frozen = nil
		c.viewport.newOutput = false
	}
	c.writeTerminal([]byte("\x1b[r\x1b[?7h"))
	c.surfaceRows = 0
	c.renderedLines = nil
}

// The physical alternate screen belongs to Vuja, not to either shadow model.
// Native applications get the primary terminal back before their own controls
// are forwarded, avoiding nested alternate-screen save/restore semantics.
func (c *terminalCompositor) setViewportScreen(enabled bool) bool {
	if c.viewportAlternate == enabled {
		return false
	}
	if !enabled {
		c.setViewportMouse(false)
	}
	c.viewportAlternate = enabled
	if enabled {
		_, _ = io.WriteString(c.out, "\x1b[?1049h")
		if c.viewport != nil {
			c.viewport.dirty = true
		}
	} else {
		_, _ = io.WriteString(c.out, "\x1b[?1049l")
	}
	return true
}

func (c *terminalCompositor) setViewportMouse(enabled bool) {
	if !enabled {
		c.setViewportMotion(false)
	}
	if c.viewportMouse == enabled {
		c.setViewportMotion(enabled && c.viewport != nil && c.viewport.frozen != nil)
		return
	}
	c.viewportMouse = enabled
	if enabled {
		_, _ = io.WriteString(c.out, "\x1b[?1000h\x1b[?1006h")
		c.setViewportMotion(c.viewport != nil && c.viewport.frozen != nil)
	} else {
		_, _ = io.WriteString(c.out, "\x1b[?1000l\x1b[?1006l")
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
			_, _ = io.WriteString(c.out, "\x1b[?1000h")
		}
	}
}

func (c *terminalCompositor) viewportBadge() (string, int, int) {
	label := " ↓ Back to bottom · esc "
	if c.viewport.newOutput {
		label = " New activity · ↓ Back to bottom · esc "
	}
	label = ansi.Cut(label, 0, c.width)
	width := ansi.StringWidth(label)
	return label, (c.width - width) / 2, width
}

func (c *terminalCompositor) viewportBadgeHovered() bool {
	_, left, width := c.viewportBadge()
	return c.viewport.pointerY == c.viewportBadgeRow() && c.viewport.pointerX > left && c.viewport.pointerX <= left+width
}

// Use the title row when present so the badge touches the input surface.
func (c *terminalCompositor) viewportBadgeRow() int {
	row := c.height - c.surfaceRows
	if c.inputBoxTitleEnabled() && c.inputBoxDecorationRows() > 0 {
		row++
	}
	return row
}

func (c *terminalCompositor) viewportBadgeTitleLine() string {
	if c.viewport.frozen == nil {
		return c.inputBoxTitleLine()
	}
	_, start, width := c.viewportBadge()
	left, _, right := splitStatusSegments(c.inputBoxBarSegments(c.chatboxConfig.Title))
	leftText := ansi.Truncate(c.renderStatusSegments(left), max(start-terminalInputHorizontalPadding, 0), "…")
	rightText := ansi.Truncate(c.renderStatusSegments(right), max(c.width-start-width-terminalInputHorizontalPadding, 0), "…")
	// The temporary navigation badge owns the center; metadata side groups get
	// bounded space rather than being partially overwritten by the badge.
	return terminalDefaultBackground + strings.Repeat(" ", terminalInputHorizontalPadding) +
		leftText + terminalDefaultBackground + strings.Repeat(" ", max(c.width-2*terminalInputHorizontalPadding-ansi.StringWidth(leftText)-ansi.StringWidth(rightText), 0)) +
		rightText + terminalDefaultBackground + strings.Repeat(" ", terminalInputHorizontalPadding)
}

func (c *terminalCompositor) appendViewportSnapshot() {
	if c.chatboxConfig.Scrollback != "snapshot" {
		return
	}
	var lines []string
	if c.inputBoxTitleEnabled() && c.completedSnapshotMetadataVisible() {
		lines = append(lines, c.completedExecutionHeaderLine())
	}
	lines = append(lines, c.completedInputBoxPaddingLine())
	for _, cells := range c.surfaceContentCells {
		lines = append(lines, c.completedInputBoxContent(c.completedSurfaceContentLine(cells)))
	}
	lines = append(lines, c.completedInputBoxPaddingLine())
	if c.inputBoxStatusEnabled() && c.completedSnapshotMetadataVisible() {
		lines = append(lines, c.completedExecutionContextLines()...)
	}
	c.writeViewportOutput([]byte(strings.Join(lines, "\x1b[0m\r\n") + "\x1b[0m\r\n"))
}

func (c *terminalCompositor) appendViewportOutcome(exitCode *int) {
	if c.chatboxConfig.Scrollback != "snapshot" || exitCode == nil || c.completedCommandMode() == "command" {
		return
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	lines := c.completedExecutionOutcomeLines(max(now().Sub(c.commandStartedAt), 0), *exitCode)
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
		if cell != nil {
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
	c.setViewportMouse(c.transientUIVisible == nil || !c.transientUIVisible())
	c.ensureOutputViewport()
	v := c.viewport
	c.setViewportMotion(c.viewportMouse && v.frozen != nil)
	if !v.dirty {
		return
	}
	v.dirty = false
	rows := c.height - c.surfaceRows
	var frame strings.Builder
	frame.WriteString(terminalSyncStart + "\x1b7\x1b[?25l\x1b[?7l")
	for row := 0; row < rows; row++ {
		line := ""
		if v.frozen != nil {
			index := v.top + row
			if index < len(v.frozen) {
				line = ansi.Cut(v.frozen[index], 0, c.width)
			}
		} else {
			line = viewportLine(v.model, row, c.width, false)
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row+1, line)
	}
	frame.WriteString("\x1b8\x1b[?7h\x1b[?25h" + terminalSyncEnd)
	data := []byte(frame.String())
	if c.viewportCommand {
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
	}
	// Paint after busy chrome too, and restore the unobstructed title at latest.
	var badge strings.Builder
	badge.WriteString(terminalSyncStart + "\x1b7\x1b[?7l")
	badgeRow := c.viewportBadgeRow()
	if badgeRow > rows {
		fmt.Fprintf(&badge, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", badgeRow, c.viewportBadgeTitleLine())
	}
	if v.frozen != nil {
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
	_, _ = c.out.Write(data)
	// Save the visible output for suggestion-overlay restoration, never feed the
	// frozen view back into the live output model.
	c.recordBackdrop(data)
	// Transient UI owns the topmost layer, but never the retained backdrop.
	// A viewport repaint invalidates even otherwise unchanged overlay rows.
	if c.transientUIVisible != nil && c.transientUIVisible() && c.renderTransientUI != nil {
		if c.setTransientUIGeometry != nil {
			c.setTransientUIGeometry(true, c.surfaceRows)
		}
		_, _ = c.out.Write(c.renderTransientUI())
	}
}

// HandleViewportInput consumes only complete dedicated navigation events.
// Paste, mixed input, shell editing and application input retain their owner.
func (c *terminalCompositor) HandleViewportInput(data []byte, suggestions bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.outputViewportEnabled() || bytes.Contains(data, bracketedPasteStart) {
		return false
	}
	wheel, mouse := viewportWheel(data, c.width, c.height-c.surfaceRows)
	if mouse && !suggestions && wheel == 0 && c.viewport != nil && c.viewport.frozen != nil {
		button, x, y, pressed, _ := viewportMouseReport(data)
		_, left, width := c.viewportBadge()
		hover := y == c.viewportBadgeRow() && x > left && x <= left+width
		if pressed && button&^28 == 0 && hover {
			c.viewport.frozen = nil
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
	if suggestions {
		return false
	}
	c.ensureOutputViewport()
	v := c.viewport
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
		if v.frozen == nil {
			for y := 0; y < v.model.ScrollbackLen(); y++ {
				v.frozen = append(v.frozen, viewportLine(v.model, y, c.width, true))
			}
			for y := 0; y < v.model.Height(); y++ {
				v.frozen = append(v.frozen, viewportLine(v.model, y, c.width, false))
			}
			v.top = max(len(v.frozen)-(c.height-c.surfaceRows), 0)
		}
		v.top = max(v.top-page, 0)
	case "\x1b[6~":
		if v.frozen == nil {
			return mouse
		}
		v.top += page
		if v.top >= max(len(v.frozen)-(c.height-c.surfaceRows), 0) {
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
