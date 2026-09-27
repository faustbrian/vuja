package root

import (
	"bytes"
	"fmt"
	"io"
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
}

var viewportKeys = []string{"\x1b[5~", "\x1b[6~", "\x1b[4~", "\x1b[F", "\x1bOF"}

// Key reports may be split by stdin reads. Hold only a bounded navigation-key
// prefix, with the wrapper's short timer distinguishing a lone Escape.
type viewportInputStager struct{ pending []byte }

func (s *viewportInputStager) stage(data []byte) []byte {
	joined := append(append([]byte(nil), s.pending...), data...)
	s.pending = nil
	if bytes.Contains(joined, bracketedPasteStart) {
		return joined
	}
	for index := len(joined) - 1; index >= max(len(joined)-5, 0); index-- {
		tail := joined[index:]
		for _, key := range viewportKeys {
			if len(tail) < len(key) && bytes.HasPrefix([]byte(key), tail) {
				s.pending = append(s.pending, tail...)
				return joined[:index]
			}
		}
	}
	return joined
}

func (s *viewportInputStager) flush() []byte { result := s.pending; s.pending = nil; return result }

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
		c.viewport.dirty = true
		_, _ = c.viewport.model.Write(data)
		if c.viewport.frozen != nil {
			c.viewport.newOutput = true
		}
	}
	c.renderOutputViewport()
}

func (c *terminalCompositor) clearViewportOverlay() {
	var frame strings.Builder
	c.clearAndDisableTransientUI(&frame)
	_, _ = io.WriteString(c.out, frame.String())
}

func (c *terminalCompositor) suspendOutputViewport() {
	c.viewportPassthrough = true
	if c.viewport != nil {
		c.viewport.frozen = nil
		c.viewport.newOutput = false
	}
	c.writeTerminal([]byte("\x1b[r\x1b[?7h"))
	c.surfaceRows = 0
	c.renderedLines = nil
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
	c.writeViewportOutput([]byte("\r\n" + strings.Join(lines, "\x1b[0m\r\n") + "\x1b[0m\r\n"))
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
		return
	}
	c.ensureOutputViewport()
	v := c.viewport
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
	if v.frozen != nil {
		label := "↓ Back to bottom · End / Esc"
		if v.newOutput {
			label = "New output · " + label
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[0m\x1b[2K%s%s\x1b[0m", rows, c.chatboxColorCodes["directory"], ansi.Cut(label, 0, c.width))
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
	_, _ = c.out.Write(data)
	// Save the visible output for suggestion-overlay restoration, never feed the
	// frozen view back into the live output model.
	c.recordBackdrop(data)
}

// HandleViewportInput consumes only complete dedicated navigation events.
// Paste, mixed input, shell editing and application input retain their owner.
func (c *terminalCompositor) HandleViewportInput(data []byte, suggestions bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.outputViewportEnabled() || suggestions || bytes.Contains(data, bracketedPasteStart) {
		return false
	}
	c.ensureOutputViewport()
	v := c.viewport
	page := max(c.height-c.surfaceRows-1, 1)
	switch string(data) {
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
			return false
		}
		v.top += page
		if v.top >= max(len(v.frozen)-(c.height-c.surfaceRows), 0) {
			v.frozen = nil
			v.newOutput = false
		}
	case "\x1b", "\x1b[F", "\x1b[4~", "\x1bOF":
		if v.frozen == nil {
			return false
		}
		v.frozen = nil
		v.newOutput = false
	default:
		return false
	}
	v.dirty = true
	c.renderOutputViewport()
	return true
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
