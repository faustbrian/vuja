package root

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/v2/internal/vt"
)

const retainedExecutionBytes = 1024 * 1024
const executionClipboardBytes = 256 * 1024

// Execution identity belongs to the output stream, not command history. These
// local monotonically increasing IDs neither identify nor resurrect history.
// Only one extra emulator is alive: completed executions are bounded rows.
type outputExecutions struct {
	nextID   uint64
	revision uint64
	active   *outputExecutionCapture
	retained []retainedExecution
	selected *retainedExecution
	notice   string
}

type retainedExecution struct {
	id               uint64
	rows             []string
	newline          bool
	truncated        bool
	applicationOwned bool
}

type outputExecutionCapture struct {
	id               uint64
	model            *vt.Emulator
	done             chan struct{}
	text             outputModelStream
	csiTail          []byte
	limit            int
	truncated        bool
	applicationOwned bool
}

func (c *terminalCompositor) startOutputExecution() {
	c.ensureOutputViewport()
	e := &c.viewport.executions
	c.finishOutputExecution(true)
	e.nextID++
	limit := clamp(c.chatboxConfig.OutputLines, 1, 10000)
	m := vt.NewEmulator(c.width, max(c.height-c.surfaceRows, 1))
	m.SetReflow(true)
	// One extra row makes a full retained limit distinguishable from loss.
	m.SetScrollbackSize(limit + 1)
	a := &outputExecutionCapture{id: e.nextID, model: m, done: make(chan struct{}), limit: limit}
	e.active = a
	go func() { _, _ = io.Copy(io.Discard, m); close(a.done) }()
}

func (c *terminalCompositor) captureExecutionOutput(data []byte) {
	if c.viewport == nil || c.viewport.executions.active == nil || c.viewport.executions.active.applicationOwned {
		return
	}
	a := c.viewport.executions.active
	width, height := max(c.width, 1), max(c.height-c.surfaceRows, 1)
	if a.model.Width() != width || a.model.Height() != height {
		a.text.resize(a.model, width, height)
	}
	data = sanitizeTerminalModelCSI(data, &a.csiTail, a.model.Width(), a.model.Height())
	if requiresOutputPassThrough(data, false) {
		c.excludeOutputExecution()
		return
	}
	a.text.write(a.model, data)
	if !a.model.IsAltScreen() && a.model.RetainedOutputTruncated() {
		a.truncated = true
	}
}

func (a *outputExecutionCapture) snapshot() retainedExecution {
	r := retainedExecution{id: a.id, truncated: a.truncated || a.text.losses > 0, applicationOwned: a.applicationOwned}
	if a.applicationOwned {
		return r
	}
	// A full-screen application's private canvas is never a retained execution.
	if a.model.IsAltScreen() {
		r.truncated = true
		return r
	}
	r.rows = retainedModelRows(a.model)
	// A cursor on the blank line after LF is a terminator, not an extra
	// retained output row. Remove it before applying the row budget.
	if len(r.rows) > 0 && ansi.Strip(r.rows[len(r.rows)-1]) == "" {
		r.rows = r.rows[:len(r.rows)-1]
		r.newline = len(r.rows) > 0
	}
	r.trim(a.limit, retainedExecutionBytes)
	return r
}

// Retained semantic text is independent of the width used for drawing. All
// consumers use this SDK projection; none reconstruct a terminal transcript.
func retainedModelRows(model *vt.Emulator) []string {
	lines := model.LogicalLines()
	rows := make([]string, len(lines))
	for i, line := range lines {
		rows[i] = line.Render()
	}
	return rows
}

// The compositor's existing native-control predicate is authoritative, whether
// an application uses the primary or alternate screen. Neither geometry-only
// suspension nor buffer mode establishes application ownership. Discard the
// complete projection at a native boundary; the ordinary retained pane still
// preserves native exit summaries as before.
func (c *terminalCompositor) excludeOutputExecution() {
	if c.viewport == nil || c.viewport.executions.active == nil {
		return
	}
	a := c.viewport.executions.active
	if a.applicationOwned {
		return
	}
	a.applicationOwned = true
	abandonTerminalModel(a.model, a.done)
	a.model, a.done = nil, nil
	a.text = outputModelStream{}
	a.csiTail = nil
}

func (r *retainedExecution) trim(lines, bytes int) {
	first, size := len(r.rows), 0
	for first > 0 && len(r.rows)-first < lines {
		n := len(r.rows[first-1]) + 1
		if size+n > bytes {
			break
		}
		size += n
		first--
	}
	if first > 0 {
		r.truncated = true
		// Copy to release evicted rows and their backing slice.
		r.rows = append([]string(nil), r.rows[first:]...)
	}
}

func (c *terminalCompositor) finishOutputExecution(interrupted bool) bool {
	if c.viewport == nil || c.viewport.executions.active == nil {
		return false
	}
	e := &c.viewport.executions
	a := e.active
	e.active = nil
	r := a.snapshot()
	r.truncated = r.truncated || interrupted
	abandonTerminalModel(a.model, a.done)
	e.retained = append(e.retained, r)
	e.revision++
	// Completed execution rows share one global cap, not a per-command cap.
	// Empty executions cost one slot, so silent commands cannot grow forever.
	lines, bytes := clamp(c.chatboxConfig.OutputLines, 1, 10000), retainedExecutionBytes
	first := len(e.retained)
	for first > 0 && lines > 0 && bytes > 0 {
		r := &e.retained[first-1]
		r.trim(lines, bytes)
		cost := 0
		for _, row := range r.rows {
			cost += len(row) + 1
		}
		lines -= max(len(r.rows), 1)
		bytes -= cost
		first--
	}
	if first > 0 {
		e.retained = append([]retainedExecution(nil), e.retained[first:]...)
	}
	return len(e.retained) > 0 && e.retained[len(e.retained)-1].truncated && !r.applicationOwned
}

func (c *terminalCompositor) selectOutputExecution(direction int) bool {
	e := &c.viewport.executions
	var selected *retainedExecution
	// The first previous action starts at the latest completed execution. A
	// later command or retention eviction cannot silently change an open copy.
	for i := range e.retained {
		r := &e.retained[i]
		if e.selected == nil {
			selected = r
		} else if direction < 0 && r.id < e.selected.id {
			selected = r
		} else if direction > 0 && r.id > e.selected.id {
			selected = r
			break
		}
	}
	if selected == nil {
		if e.selected != nil {
			return true // clamp at either end, never select a neighbor by index
		}
		e.notice = "No retained execution · esc latest"
		c.viewport.setFrozen([]string{})
		return true
	}
	copy := *selected
	copy.rows = append([]string(nil), selected.rows...)
	e.selected, e.notice = &copy, ""
	c.viewport.setFrozen(append([]string{}, copy.rows...))
	c.viewport.top = 0
	c.viewport.search = nil
	return true
}

func (r retainedExecution) plainText() string {
	if r.applicationOwned || len(r.rows) == 0 {
		return ""
	}
	var out strings.Builder
	// Execution rows are program-owned. Pane callers remove their separately
	// attributed chrome before conversion; a hyperlink URI is not provenance.
	first := true
	for _, row := range r.rows {
		if !first {
			out.WriteByte('\n')
		}
		first = false
		out.WriteString(strings.TrimRight(ansi.Strip(row), " "))
	}
	if r.newline {
		out.WriteByte('\n')
	}
	return out.String()
}

func (c *terminalCompositor) handleOutputExecution(data []byte) bool {
	v := c.viewport
	switch string(data) {
	case "\x1b[21~": // F10: previous execution
		c.selectOutputExecution(-1)
	case "\x1b[21;2~": // Shift+F10: next execution
		c.selectOutputExecution(1)
	case "\x1b[24~": // F12: explicit private plain-text export
		if r := v.executions.selected; r != nil && r.applicationOwned {
			v.executions.notice = "Export unavailable"
		} else {
			c.exportRetainedOutput()
		}
	case "\x1b[23~": // F11: copy selected (or explicitly reveal latest)
		if v.search != nil && v.executions.selected == nil {
			v.executions.notice = "Select execution with F10 before copying"
		} else {
			if v.executions.selected == nil {
				c.selectOutputExecution(-1)
			}
			if r := v.executions.selected; r != nil {
				text := r.plainText()
				switch {
				case r.applicationOwned:
					v.executions.notice = "Copy unavailable"
				case len(text) == 0:
					v.executions.notice = "No retained output to copy"
				case len(text) > executionClipboardBytes:
					// Do not silently substitute a prefix or clear the clipboard.
					v.executions.notice = "Copy exceeds clipboard limit · export instead"
				default:
					_, _ = fmt.Fprintf(c.out, "\x1b]52;c;%s\x1b\\", base64.StdEncoding.EncodeToString([]byte(text)))
					v.executions.notice = "Copy requested · terminal permission may be needed"
				}
			}
		}
	default:
		return false
	}
	v.dirty = true
	c.renderOutputViewport()
	return true
}

func (e *outputExecutions) label() string {
	if e.selected == nil {
		if e.notice != "" {
			return " " + e.notice + " "
		}
		return ""
	}
	caption := fmt.Sprintf("Execution #%d", e.selected.id)
	if e.selected.id == 0 {
		caption = "Retained block"
	}
	if e.selected.applicationOwned {
		caption = "not captured · " + caption + " · application output"
	} else if e.selected.truncated {
		caption = "Output truncated · " + caption
	}
	if e.notice != "" {
		return " " + caption + " · " + e.notice + " "
	}
	return " " + caption + " · F10 prev · Shift+F10 next · F11 copy · F12 export · esc latest "
}
