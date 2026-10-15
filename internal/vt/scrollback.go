package vt

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
)

// DefaultScrollbackSize is the default maximum number of lines in the scrollback buffer.
const DefaultScrollbackSize = 10000

// Scrollback represents a scrollback buffer that stores lines scrolled off the screen.
type Scrollback struct {
	lines              []uv.Line
	maxLines           int
	wrap, used         []int
	logical            bool
	truncated          bool
	rowCosts           []int
	cost, logicalLines int
	// Mutable public aliases cannot notify us of edits. Only callers that ask
	// for one require the conservative recount; defensive projections do not.
	aliases bool
}

// NewScrollback creates a new scrollback buffer with the given maximum number of lines.
func NewScrollback(maxLines int) *Scrollback {
	if maxLines <= 0 {
		maxLines = DefaultScrollbackSize
	}
	return &Scrollback{
		lines:    make([]uv.Line, 0, min(maxLines, 1000)), // Pre-allocate reasonable capacity
		maxLines: maxLines,
	}
}

// Push adds a line to the scrollback buffer.
// If the buffer is full, the oldest line is removed.
func (s *Scrollback) Push(line uv.Line) {
	s.pushRetained(line, 0, contentWidth(line))
}

func (s *Scrollback) pushRetained(line uv.Line, wrap, used int) {
	if s == nil || s.maxLines <= 0 {
		return
	}

	// Find last non-empty cell to trim trailing empty cells.
	// This helps with wrapping and window resizing.
	lastNonEmpty := -1
	for i := len(line) - 1; i >= 0; i-- {
		c := &line[i]
		if !c.IsZero() && !c.Equal(&uv.EmptyCell) {
			lastNonEmpty = i
			break
		}
	}

	// Clone the line content up to and including the last non-empty cell
	last := lastNonEmpty + 1
	if s.logical {
		last = max(last, used, wrap)
	}
	cloned := slices.Clone(line[:min(last, len(line))])
	if s.logical {
		for len(cloned) < last {
			cloned = append(cloned, uv.Cell{})
		}
		s.appendRetained(cloned, wrap, used)
		s.trimRetained()
		return
	}

	if !s.logical && len(s.lines) >= s.maxLines {
		// Remove oldest line and append new one
		s.dropRetained(1)
		s.truncated = true
	}
	if len(s.lines) == 0 || s.wrap[len(s.wrap)-1] == 0 {
		s.logicalLines++
	}
	cost := retainedRowCost(cloned)
	s.cost += cost
	s.rowCosts = append(s.rowCosts, cost)
	s.lines = append(s.lines, cloned)
	s.wrap, s.used = append(s.wrap, wrap), append(s.used, used)
}

// Coalescing only known soft continuations keeps retained storage independent
// of drawing width. A narrow resize must not turn one line into a huge queue of
// tiny row allocations or multiply the cost of every subsequent scroll.
func (s *Scrollback) appendRetained(line uv.Line, wrap, used int) {
	cost := retainedRowCost(line)
	if last := len(s.lines) - 1; last >= 0 && s.wrap[last] > 0 {
		base := len(s.lines[last])
		if wrap == 0 {
			// A completed logical line will not grow again. Exact capacity
			// avoids keeping append's spare styled cells for every stored line;
			// still-growing soft continuations retain amortized append below.
			joined := make(uv.Line, base+len(line))
			copy(joined, s.lines[last])
			copy(joined[base:], line)
			s.lines[last] = joined
		} else {
			s.lines[last] = append(s.lines[last], line...)
		}
		s.used[last] = base + used
		s.wrap[last] = 0
		if wrap > 0 {
			s.wrap[last] = base + wrap
		}
		s.rowCosts[last] += cost
	} else {
		s.lines = append(s.lines, line)
		s.wrap, s.used = append(s.wrap, wrap), append(s.used, used)
		s.rowCosts = append(s.rowCosts, cost)
		s.logicalLines++
	}
	s.cost += cost
}

// PushN adds n lines from the buffer starting at line y to the scrollback.
func (s *Scrollback) PushN(buf *uv.RenderBuffer, y, n int) {
	if s == nil || buf == nil || n <= 0 {
		return
	}

	for i := range min(n, buf.Height()-y) {
		if line := buf.Line(y + i); line != nil {
			s.Push(line)
		}
	}
}

// Len returns the number of lines in the scrollback buffer.
func (s *Scrollback) Len() int {
	if s == nil {
		return 0
	}
	return len(s.lines)
}

// MaxLines returns the maximum number of lines the scrollback buffer can hold.
func (s *Scrollback) MaxLines() int {
	if s == nil {
		return 0
	}
	return s.maxLines
}

// SetMaxLines sets the maximum number of lines in the scrollback buffer.
// If the current number of lines exceeds the new maximum, oldest lines are removed.
func (s *Scrollback) SetMaxLines(maxLines int) {
	if s == nil || maxLines <= 0 {
		return
	}

	s.maxLines = maxLines
	if s.logical {
		s.trimRetained()
	} else if len(s.lines) > maxLines {
		// Remove oldest lines
		s.lines = s.lines[len(s.lines)-maxLines:]
		s.wrap, s.used = s.wrap[len(s.wrap)-maxLines:], s.used[len(s.used)-maxLines:]
		s.recountRetained()
		s.truncated = true
	}
}

// Line returns the line at the given index.
// Index 0 is the oldest line, Len()-1 is the most recent.
// Returns nil if index is out of bounds.
func (s *Scrollback) Line(index int) uv.Line {
	if s == nil || index < 0 || index >= len(s.lines) {
		return nil
	}
	s.aliases = true
	return s.lines[index]
}

// Lines returns all lines in the scrollback buffer.
// Index 0 is the oldest line.
func (s *Scrollback) Lines() []uv.Line {
	if s == nil {
		return nil
	}
	s.aliases = true
	return s.lines
}

// Clear removes all lines from the scrollback buffer.
func (s *Scrollback) Clear() {
	if s == nil {
		return
	}
	s.lines = s.lines[:0]
	s.wrap, s.used = s.wrap[:0], s.used[:0]
	s.rowCosts = s.rowCosts[:0]
	s.cost, s.logicalLines = 0, 0
	s.truncated = false
}

func (s *Scrollback) replaceRetained(rows []retainedRow) {
	s.lines, s.wrap, s.used = nil, nil, nil
	s.rowCosts = s.rowCosts[:0]
	s.cost, s.logicalLines = 0, 0
	for first := 0; first < len(rows); {
		end, size := first, 0
		for {
			row := rows[end]
			size += max(min(len(row.cells), max(contentWidth(row.cells), row.used, row.wrap)), row.used)
			end++
			if row.wrap == 0 || end == len(rows) {
				break
			}
		}
		// Resize already knows all fragments of this logical line. Allocate
		// its final storage once, not a clone and growing append per column
		// when the temporary drawing width happens to be one.
		line := make(uv.Line, size)
		base, used, wrap := 0, 0, 0
		for _, row := range rows[first:end] {
			last := min(len(row.cells), max(contentWidth(row.cells), row.used, row.wrap))
			copy(line[base:], row.cells[:last])
			used, wrap = base+row.used, 0
			if row.wrap > 0 {
				wrap = base + row.wrap
			}
			base += max(last, row.used)
		}
		s.appendRetained(line, wrap, used)
		first = end
	}
	// A resize only reshapes existing state. Do not spend retention again merely
	// because a logical line now occupies more physical rows.
	s.recountRetained()
}

func (s *Scrollback) trimRetained() {
	if s.aliases {
		s.recountRetained()
	}
	logical, bytes := s.logicalLines, s.cost
	first := 0
	for first < len(s.lines) && (logical > s.maxLines || bytes > retainedScrollbackBytes && len(s.lines)-first > 1) {
		bytes -= s.rowCosts[first]
		if s.wrap[first] == 0 || first+1 == len(s.lines) {
			logical--
		}
		first++
	}
	if first > 0 {
		s.truncated = true
		s.dropRetained(first)
	}
	if s.cost > retainedScrollbackBytes && len(s.lines) == 1 {
		// A single unbroken line is bounded too. Drop an amortized prefix, not
		// one cell on every write; preserve a complete newest cluster/tail.
		line := s.lines[0]
		cut, removed := 0, 0
		target := s.cost - retainedScrollbackBytes/2
		for cut < len(line) && removed < target {
			end := min(cut+max(line[cut].Width, 1), len(line))
			removed += retainedRowCost(line[cut:end])
			cut = end
		}
		s.lines[0] = slices.Clone(line[cut:])
		s.used[0] = max(0, s.used[0]-cut)
		s.wrap[0] = max(0, s.wrap[0]-cut)
		s.rowCosts[0] -= removed
		s.cost -= removed
		s.truncated = true
	}
}

func retainedRowCost(line uv.Line) int {
	cost := 0
	for _, cell := range line {
		cost += max(len(cell.Content), 1) + len(cell.Link.URL) + len(cell.Link.Params)
	}
	return cost
}

func (s *Scrollback) recountRetained() {
	s.logicalLines, s.cost = 0, 0
	s.rowCosts = slices.Grow(s.rowCosts[:0], len(s.lines))
	for i, line := range s.lines {
		if i == 0 || s.wrap[i-1] == 0 {
			s.logicalLines++
		}
		cost := retainedRowCost(line)
		s.cost += cost
		s.rowCosts = append(s.rowCosts, cost)
	}
	clear(s.rowCosts[len(s.rowCosts):cap(s.rowCosts)])
}

func (s *Scrollback) dropRetained(first int) {
	for i := 0; i < first; i++ {
		s.cost -= s.rowCosts[i]
		if s.wrap[i] == 0 {
			s.logicalLines--
		}
	}
	s.lines = slices.Delete(s.lines, 0, first)
	s.wrap = slices.Delete(s.wrap, 0, first)
	s.used = slices.Delete(s.used, 0, first)
	s.rowCosts = slices.Delete(s.rowCosts, 0, first)
	if len(s.lines) == 0 {
		s.logicalLines = 0
	}
}

// CellAt returns the cell at the given position in the scrollback buffer.
// x is the column, y is the line index (0 = oldest).
// Returns nil if position is out of bounds.
func (s *Scrollback) CellAt(x, y int) *uv.Cell {
	line := s.Line(y)
	if line == nil || x < 0 || x >= len(line) {
		return nil
	}
	return &line[x]
}
