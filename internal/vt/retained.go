package vt

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
)

// retainedRow is a physical row, with its original soft-wrap boundary. A tail
// is retained below the visible canvas when shrinking around an earlier cursor.
// It is SDK state, not a second parser or a transcript of erased input bytes.
type retainedRow struct {
	cells      uv.Line
	wrap, used int
}

const retainedScrollbackBytes = 1024 * 1024

// SetReflow enables primary-screen retained-text reflow. Enable before writing.
// Alternate screens and the default (disabled) preserve upstream canvas resize
// semantics. In this mode scrollback size counts logical lines, with a separate
// 1 MiB text/link/placeholder budget; geometry alone does not consume the line cap.
// Cell structures and their backing allocations are additional bounded memory,
// not a claim of a 1 MiB process heap or RSS limit.
func (e *Emulator) SetReflow(enabled bool) {
	s := &e.scrs[0]
	s.reflow = enabled
	if s.scrollback != nil {
		s.scrollback.logical = enabled
	}
}

// LogicalLines returns a defensive primary-screen text projection, including
// scrollback and any rows hidden by a height shrink. Only actual soft wraps are
// joined. This does not expose the alternate-screen application's canvas.
func (e *Emulator) LogicalLines() []uv.Line {
	s := &e.scrs[0]
	rows, _ := s.retainedRows()
	lines, _ := joinRetainedRows(rows)
	return lines
}

// RetainedOutputTruncated reports actual scrollback eviction, not merely a full
// buffer or a narrower drawing surface. Clearing scrollback resets this flag.
func (e *Emulator) RetainedOutputTruncated() bool {
	return e.scrs[0].scrollback != nil && e.scrs[0].scrollback.truncated
}

// ResizeWithAnchor resizes like Resize and maps one primary-screen cell through
// retained reflow. The caller does not reconstruct wrapping or move the cursor.
// ok is false for non-reflow/alternate screens, an invalid original coordinate,
// or a cell that ends up outside the visible canvas. Mapping does not expose a
// mutable alias and does not add a requirement to the Terminal interface.
func (e *Emulator) ResizeWithAnchor(width, height int, anchor uv.Position) (mapped uv.Position, ok bool) {
	if !e.scrs[0].reflow || e.scr != &e.scrs[0] || anchor.X < 0 || anchor.Y < 0 || anchor.X >= e.Width() || anchor.Y >= e.Height() {
		e.Resize(width, height)
		return uv.Position{}, false
	}
	e.resize(width, height, &anchor)
	return anchor, anchor.X >= 0 && anchor.Y >= 0
}

func resizeMetadata(old []int, height int) []int {
	next := make([]int, height)
	copy(next, old)
	return next
}

func contentWidth(line uv.Line) int {
	for x := len(line) - 1; x >= 0; x-- {
		if cell := line[x]; !cell.IsZero() && !cell.Equal(&uv.EmptyCell) {
			return min(len(line), x+max(cell.Width, 1))
		}
	}
	return 0
}

func (s *Screen) erasedArea(area uv.Rectangle) {
	area = area.Intersect(s.buf.Bounds())
	for y := area.Min.Y; y < area.Max.Y; y++ {
		if area.Min.X == 0 && area.Max.X == s.Width() {
			s.wrap[y], s.used[y] = 0, contentWidth(s.buf.Line(y))
		} else if area.Max.X >= s.used[y] {
			s.used[y] = max(area.Min.X, contentWidth(s.buf.Line(y)))
		}
	}
	if area == s.buf.Bounds() {
		s.tail = nil
	}
}

func (s *Screen) moveRowMetadata(first, end, n int, insert bool) {
	if s.scroll.Min.X != 0 || s.scroll.Max.X != s.Width() {
		for y := first; y < end; y++ {
			s.wrap[y], s.used[y] = 0, contentWidth(s.buf.Line(y))
		}
		return
	}
	for _, values := range [][]int{s.wrap, s.used} {
		if insert {
			copy(values[first+n:end], values[first:end-n])
			clear(values[first : first+n])
		} else {
			copy(values[first:end-n], values[first+n:end])
			clear(values[end-n : end])
		}
	}
}

func (s *Screen) retainedRows() (rows []retainedRow, cursorRow int) {
	if sb := s.scrollback; sb != nil {
		for i, line := range sb.lines {
			rows = append(rows, retainedRow{line, sb.wrap[i], sb.used[i]})
		}
	}
	cursorRow = len(rows) + s.cur.Y
	last := max(s.cur.Y, s.saved.Y)
	if len(s.tail) > 0 {
		last = s.Height() - 1
	} else {
		for y := s.Height() - 1; y > last; y-- {
			if contentWidth(s.buf.Line(y)) > 0 || s.used[y] > 0 || s.wrap[y] > 0 {
				last = y
				break
			}
		}
	}
	for y := 0; y <= last && y < s.Height(); y++ {
		rows = append(rows, retainedRow{s.buf.Line(y), s.wrap[y], s.used[y]})
	}
	rows = append(rows, s.tail...)
	return rows, cursorRow
}

// Projection reads mutable UV cells at the time of the call. The public cell
// and scrollback alias APIs keep working; no independently cached text wins.
func joinRetainedRows(rows []retainedRow, anchors ...uv.Position) (lines []uv.Line, positions []uv.Position) {
	lines = append(lines, nil)
	positions = make([]uv.Position, len(anchors))
	for y, row := range rows {
		last := max(contentWidth(row.cells), row.used, row.wrap)
		for i, anchor := range anchors {
			if y == anchor.Y {
				positions[i] = uv.Pos(len(lines[len(lines)-1])+anchor.X, len(lines)-1)
				last = max(last, anchor.X)
			}
		}
		index := len(lines) - 1
		base := len(lines[index])
		// The result owns one defensive copy. Do not clone a physical row and
		// then immediately copy that clone into a second logical allocation.
		if base == 0 {
			lines[index] = slices.Clone(row.cells[:min(last, len(row.cells))])
		} else {
			lines[index] = append(lines[index], row.cells[:min(last, len(row.cells))]...)
		}
		for len(lines[index])-base < last {
			padding := uv.EmptyCell
			for i := len(lines[index]) - 1; i >= base; i-- {
				if lines[index][i].Width > 0 {
					if i+lines[index][i].Width > len(lines[index]) {
						padding = uv.Cell{}
					}
					break
				}
			}
			lines[index] = append(lines[index], padding)
		}
		if row.wrap == 0 && y+1 < len(rows) {
			lines = append(lines, nil)
		}
	}
	return lines, positions
}

func layoutRetainedLine(line uv.Line, width int, offset int) (rows []retainedRow, cursor uv.Position) {
	// line is the newly owned defensive logical projection. Temporary layouts
	// borrow disjoint portions of it; only the visible canvas needs padded,
	// independently mutable rows. Historical rows need no width-sized copies.
	rows = append(rows, retainedRow{})
	x, y, first := 0, 0, 0
	cursorSet := false
	for i := 0; i < len(line); {
		cell := line[i]
		w := max(cell.Width, 1)
		if x > 0 && x+w > width {
			rows[y].wrap = x
			rows = append(rows, retainedRow{})
			x, y, first = 0, y+1, i
		}
		if !cursorSet && offset >= i && offset < i+w {
			cursor, cursorSet = uv.Pos(x+offset-i, y), true
		}
		if w > width {
			rows[y].cells = line[i : i+1 : i+1]
		} else if i+w <= len(line) {
			// Public aliases can change a cell's width. Match UV's Set
			// behavior by normalizing its now-covered placeholder cells.
			clear(line[i+1 : i+w])
			rows[y].cells = line[first : i+w : i+w]
		} else {
			// A malformed/widened final alias can require placeholders beyond
			// the logical slice. Keep UV's existing normalization for it.
			padded := uv.NewLine(width)
			copy(padded, rows[y].cells)
			rows[y].cells = padded
			rows[y].cells.Set(x, &cell)
		}
		x += w
		rows[y].used = x
		i += w
		if !cursorSet && offset == i {
			cursor = uv.Pos(x, y)
		}
		if x >= width && i < len(line) {
			rows[y].wrap = x
			rows = append(rows, retainedRow{})
			x, y, first = 0, y+1, i
		}
	}
	return rows, cursor
}

func (s *Screen) resizeRetained(width, height int, pending bool, tracked *uv.Position) bool {
	if width == s.Width() && height == s.Height() {
		s.scroll = s.buf.Bounds()
		return pending
	}
	rows, cursorRow := s.retainedRows()
	x := s.cur.X
	if pending {
		if cell := s.CellAt(x, s.cur.Y); cell != nil {
			x += max(cell.Width, 1)
		} else {
			x++
		}
	}
	// Saved cursor uses the same logical joins, not a physical row number from
	// the pre-resize width. It does not inherit the active pending-wrap flag.
	savedRow := cursorRow - s.cur.Y + s.saved.Y
	sx := s.saved.X
	if s.savedPending {
		if cell := s.CellAt(sx, s.saved.Y); cell != nil {
			sx += max(cell.Width, 1)
		} else {
			sx++
		}
	}
	points := []uv.Position{uv.Pos(x, cursorRow), uv.Pos(sx, savedRow)}
	if tracked != nil {
		points = append(points, uv.Pos(tracked.X, cursorRow-s.cur.Y+tracked.Y))
	}
	logical, anchors := joinRetainedRows(rows, points...)
	currentAnchor, savedAnchor := anchors[0], anchors[1]
	var physical []retainedRow
	var cur, saved, mapped uv.Position
	for y, line := range logical {
		offset := -1
		if y == currentAnchor.Y {
			offset = currentAnchor.X
		}
		laidOut, position := layoutRetainedLine(line, width, offset)
		if y == currentAnchor.Y {
			cur = position.Add(uv.Pos(0, len(physical)))
		}
		if y == savedAnchor.Y {
			_, position = layoutRetainedLine(line, width, savedAnchor.X)
			saved = position.Add(uv.Pos(0, len(physical)))
		}
		if tracked != nil && y == anchors[2].Y {
			_, position = layoutRetainedLine(line, width, anchors[2].X)
			mapped = position.Add(uv.Pos(0, len(physical)))
		}
		physical = append(physical, laidOut...)
	}
	start := min(max(len(physical)-height, 0), cur.Y)
	start = max(start, cur.Y-height+1)
	if sb := s.scrollback; sb != nil {
		sb.replaceRetained(physical[:start])
	}
	s.buf = uv.NewRenderBuffer(width, height)
	s.wrap, s.used = make([]int, height), make([]int, height)
	for y := start; y < min(start+height, len(physical)); y++ {
		copy(s.buf.Lines[y-start], physical[y].cells)
		s.wrap[y-start], s.used[y-start] = physical[y].wrap, physical[y].used
	}
	s.tail = slices.Clone(physical[min(start+height, len(physical)):])
	s.scroll = s.buf.Bounds()
	s.cur.X, s.cur.Y = min(cur.X, width-1), max(0, cur.Y-start)
	s.saved.X, s.saved.Y = min(saved.X, width-1), max(0, min(saved.Y-start, height-1))
	s.savedPending = saved.X >= width
	if tracked != nil {
		*tracked = uv.Pos(mapped.X, mapped.Y-start)
		if tracked.X >= width || tracked.Y < 0 || tracked.Y >= height {
			*tracked = uv.Pos(-1, -1)
		}
	}
	return cur.X >= width
}

func (se *SafeEmulator) SetReflow(enabled bool) {
	se.mu.Lock()
	defer se.mu.Unlock()
	se.Emulator.SetReflow(enabled)
}

func (se *SafeEmulator) LogicalLines() []uv.Line {
	se.mu.RLock()
	defer se.mu.RUnlock()
	return se.Emulator.LogicalLines()
}

func (se *SafeEmulator) RetainedOutputTruncated() bool {
	se.mu.RLock()
	defer se.mu.RUnlock()
	return se.Emulator.RetainedOutputTruncated()
}

func (se *SafeEmulator) ResizeWithAnchor(width, height int, anchor uv.Position) (uv.Position, bool) {
	se.mu.Lock()
	defer se.mu.Unlock()
	return se.Emulator.ResizeWithAnchor(width, height, anchor)
}
