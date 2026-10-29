package root

import (
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Cards borrow logical ranges from the existing pane. Capture IDs are local
// action targets only; neither shell history nor command-looking text is an ID.
type outputCard struct {
	first, last, bodyFirst, bodyEnd uint64
	outcomeFirst, outcomeEnd        uint64
	captureID                       uint64
	state                           blockState
	outcome                         []string
	newline, native, divider        bool
	truncated                       bool
	paneOnly                        bool
	notices                         []blockSpan
}

type cardRow struct {
	line                string
	source              string
	key, logical        uint64
	column, kind, width int
	end                 int
	copyX, saveX        int // one-based columns; zero means unavailable
	state               blockState
	boundary            bool // pixel decoration at the top edge; never an extra text row
	target              *retainedExecution
	index               int
}

// Input rows use the zero selection key. The plain pane is a separate
// selection document; this local key is never an execution/action identity.
const plainOutputSelectionKey uint64 = 1

type cardPoint struct{ x, y int }
type cardSelection struct {
	first, last cardPoint
	frame       []cardRow
	dragged     bool
}
type cardPress struct {
	point  cardPoint
	action int
	target *retainedExecution
}

// Like the text viewport's frozen pane, a card reading target has its own
// bounded lifetime. Live prefix eviction must not replace rows being read.
type cardReading struct {
	rows      []string
	base, end uint64
	cards     []outputCard
	completed int
	states    []blockSpan
	truncated bool
	dividers  map[int]bool
	targets   map[uint64]*retainedExecution
	anchor    *cardRow
	history   int
}

type outputCards struct {
	completed      []outputCard
	pending        *outputCard
	focus          *uint64
	focused        *retainedExecution
	offsets        map[uint64]int
	history        int // rows above the latest enclosing-history position
	browsing       bool
	total          int
	frame          []cardRow
	selection      *cardSelection
	press          *cardPress
	clearAt        uint64
	reveal         *uint64
	document       []string
	revision       uint64
	layouts        map[uint64]cardBodyLayout
	restoreInput   bool
	targets        map[uint64]*retainedExecution
	targetRevision uint64
	reading        *cardReading
}

type cardBodyLayout struct {
	width, count int
	first, last  string
	rows         []cardRow
}

func (c *terminalCompositor) cardLayoutEnabled() bool {
	return c.chatboxConfig.Scrollback == "blocks" && c.chatboxConfig.BlockMaxHeight > 0
}

func (c *terminalCompositor) cardProjectionAvailable() bool {
	v := c.viewport
	// The narrow initial view uses plain text, but an existing reading target
	// must retain its document and action identity when the window narrows.
	return c.cardLayoutEnabled() && v != nil && (c.width >= 8 || v.cards.reading != nil) && v.search == nil && v.frozen == nil &&
		(len(v.cards.completed) > 0 || v.cards.pending != nil || v.cards.reading != nil)
}

func (c *terminalCompositor) freezeCardReading() {
	v, s := c.viewport, &c.viewport.cards
	if s.reading != nil {
		return
	}
	r := &cardReading{rows: retainedModelRows(v.model), cards: slices.Clone(s.completed), completed: len(s.completed), states: slices.Clone(v.blocks), targets: make(map[uint64]*retainedExecution), history: s.history, truncated: v.outputTruncated()}
	r.base, _ = v.model.LogicalRowPositions()
	r.dividers = v.paneDividerIndexes(r.base, r.rows)
	r.end = r.base + uint64(len(r.rows))
	if v.model.CursorPosition().X == 0 && r.end > r.base {
		r.end--
	}
	if s.pending != nil {
		card := *s.pending
		card.last, card.bodyEnd = v.cursorLogicalRow(), v.cursorLogicalRow()
		if v.model.CursorPosition().X > 0 {
			card.bodyEnd++
		}
		r.cards = append(r.cards, card)
	}
	for i := range r.cards {
		card := &r.cards[i]
		card.notices, card.outcome = slices.Clone(card.notices), slices.Clone(card.outcome)
		card.state = v.stateAt(max(card.first, r.base))
		if s.pending != nil && card.first == s.pending.first && (card.first < r.base || v.text.losses != v.blockLossStart) {
			card.state = blockWarning
		}
		if target := v.cardTarget(*card, r.rows, r.base); target != nil {
			copy := *target
			copy.rows = slices.Clone(copy.rows)
			r.targets[card.first] = &copy
		}
	}
	if len(s.frame) > 0 {
		row := s.frame[0]
		r.anchor = &row
	}
	s.reading = r
}

func (c *terminalCompositor) latestCardReading() {
	s := &c.viewport.cards
	s.releasePointer()
	s.reading, s.focus, s.focused = nil, nil, nil
	s.history, s.total, s.browsing = 0, 0, false
	c.viewport.newOutput = false
	c.viewport.executions.selected, c.viewport.executions.notice = nil, ""
}

func (c *terminalCompositor) plainOutputSelectionAvailable() bool {
	return c.viewport != nil && c.chatboxConfig.Scrollback == "output" && c.viewport.search == nil
}

func (c *terminalCompositor) interactiveOutputMouse() bool {
	// Plain output owns the same drag gestures as cards. Search and legacy
	// snapshots still yield selection to the terminal, not a hidden card.
	return c.chatboxConfig.OutputMouse == "interact" && (c.cardProjectionAvailable() || c.plainOutputSelectionAvailable())
}

func (c *terminalCompositor) beginCardOutput() {
	v := c.viewport
	if v.cards.pending != nil {
		v.cards.pending.bodyFirst = v.cursorLogicalRow()
		v.cards.pending.bodyEnd = v.cards.pending.bodyFirst
		if a := v.executions.active; a != nil {
			v.cards.pending.captureID = a.id
		}
	}
}

func (c *terminalCompositor) endCardOutput() {
	v := c.viewport
	if v == nil || v.cards.pending == nil {
		return
	}
	card := v.cards.pending
	card.bodyEnd = v.cursorLogicalRow()
	if v.model.CursorPosition().X > 0 {
		card.bodyEnd++
	}
	if a := v.executions.active; a != nil {
		card.native = a.applicationOwned
		if !card.native {
			r := a.snapshot()
			card.newline, card.truncated = r.newline, r.truncated
		}
	}
}

func (v *outputViewport) cardTarget(card outputCard, rows []string, base uint64) *retainedExecution {
	if card.native {
		return nil
	}
	if v.cards.reading != nil {
		return v.cards.reading.targets[card.first]
	}
	if v.cards.focus != nil && *v.cards.focus == card.first && v.cards.focused != nil {
		return v.cards.focused
	}
	if v.cards.targets == nil || v.cards.targetRevision != v.executions.revision {
		v.cards.targets = make(map[uint64]*retainedExecution, len(v.executions.retained))
		for i := range v.executions.retained {
			r := &v.executions.retained[i]
			v.cards.targets[r.id] = r
		}
		v.cards.targetRevision = v.executions.revision
	}
	if r := v.cards.targets[card.captureID]; r != nil {
		if r.applicationOwned {
			return nil
		}
		return r
	}
	if a := v.executions.active; a != nil && a.id == card.captureID {
		if a.applicationOwned {
			return nil
		}
		copy := a.snapshot()
		return &copy
	}
	// Sideband text can split a program line in the pane. After its semantic
	// capture is gone, that mixed display cannot reconstruct execution bytes.
	if card.paneOnly {
		return nil
	}
	// A restored/newly pruned display can have valid numeric output boundaries
	// without an execution identity. It is text-only, never an executable action.
	first, end := max(card.bodyFirst, base), min(card.bodyEnd, base+uint64(len(rows)))
	if end < first {
		return nil
	}
	return &retainedExecution{rows: slices.Clone(rows[first-base : end-base]), newline: card.newline, truncated: card.truncated || card.bodyFirst < base}
}

func (card outputCard) checkpoint(first uint64, count int) *checkpointCard {
	clipped := func(row uint64) int { return int(min(max(row, first)-first, uint64(count))) }
	stored := &checkpointCard{BodyFirst: clipped(card.bodyFirst), BodyEnd: clipped(card.bodyEnd), Newline: card.newline, Native: card.native, Truncated: card.truncated || card.bodyFirst < first, PaneOnly: card.paneOnly}
	for _, notice := range card.notices {
		start, end := max(notice.first, first), min(notice.last+1, first+uint64(count))
		if start < end {
			stored.Notices = append(stored.Notices, checkpointTextRange{First: int(start - first), Last: int(end - first - 1)})
		}
	}
	if card.outcomeEnd > 0 {
		stored.OutcomeFirst, stored.OutcomeEnd = clipped(card.outcomeFirst), clipped(card.outcomeEnd)
	}
	return stored
}

func wrapCardRows(rows []string, width int, key uint64, kind int, state blockState) []cardRow {
	var laid []cardRow
	for i, line := range rows {
		first, column := 0, 0
		appendRow := func(last int) {
			// Index the complete logical line once; ANSI slicing is done only
			// for visible fragments, not once for every hidden wrapped row.
			laid = append(laid, cardRow{source: line, key: key, logical: uint64(i), column: first, end: last, kind: kind, width: width + 2, state: state, index: len(laid)})
		}
		for text := ansi.Strip(line); len(text) > 0; {
			cluster, size := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
			if column > first && column-first+size > width {
				appendRow(column)
				first = column
			}
			column += size
			text = text[len(cluster):]
		}
		appendRow(column)
	}
	return laid
}

func (c *terminalCompositor) layoutOutputCards(height int) ([]cardRow, bool) {
	v := c.viewport
	if !c.cardProjectionAvailable() {
		return nil, false
	}
	s := &v.cards
	if s.selection != nil && s.selection.dragged {
		return s.selection.frame, true
	}
	if s.reading == nil && (s.document == nil || s.revision != v.revision) {
		s.document = retainedModelRows(v.model)
		s.revision = v.revision
	}
	rows := s.document
	base, _ := v.model.LogicalRowPositions()
	cards := slices.Clone(s.completed)
	end := base + uint64(len(rows))
	if v.model.CursorPosition().X == 0 && end > base {
		end-- // cursor terminator, not an additional program output row
	}
	states := v.blocks
	dividers := v.paneDividerIndexes(base, rows)
	if s.reading != nil {
		rows, base, end, cards, states = s.reading.rows, s.reading.base, s.reading.end, s.reading.cards, s.reading.states
		dividers = s.reading.dividers
	} else if s.pending != nil {
		card := *s.pending
		card.last = v.cursorLogicalRow()
		card.bodyEnd = card.last
		if v.model.CursorPosition().X > 0 {
			card.bodyEnd++
		}
		cards = append(cards, card)
	}
	var all []cardRow
	width := max(c.width-2, 1)
	valid := make(map[uint64]bool, len(cards))
	next := base
	if !s.browsing {
		next = max(next, s.clearAt)
	}
	appendUnowned := func(end uint64) {
		end = min(end, base+uint64(len(rows)))
		for next < end {
			line := rows[next-base]
			if !dividers[int(next-base)] {
				all = append(all, wrapCardRows([]string{line}, width, next, 0, blockStateAt(states, next))...)
			}
			next++
		}
	}
	previousCard := false
	for _, card := range cards {
		if card.last < base || card.first < s.clearAt && !s.browsing {
			continue
		}
		valid[card.first] = true
		first := max(card.first, base)
		body := min(max(card.bodyFirst, first), base+uint64(len(rows)))
		if s.reading == nil {
			card.state = v.stateAt(first)
		}
		if !c.chatboxConfig.BlockStates {
			card.state = blockNeutral
		}
		if s.reading == nil && s.pending != nil && s.pending.first == card.first {
			if c.chatboxConfig.BlockStates && (card.first < base || v.text.losses != v.blockLossStart) {
				card.state = blockWarning
			}
		}
		if body < first {
			continue
		}
		appendUnowned(first)
		var headers []string
		for i, row := range rows[first-base : body-base] {
			if !dividers[int(first-base)+i] {
				headers = append(headers, row)
			}
		}
		if len(headers) == 0 {
			headers = []string{"Retained output"}
		}
		target := v.cardTarget(card, rows, base)
		if c.chatboxConfig.BlockStates && card.state == blockNeutral && (target != nil && target.truncated || target == nil && card.paneOnly && !card.native) {
			card.state = blockWarning
		}
		var output []string
		if target != nil {
			output = target.rows
		} else {
			start, end := max(card.bodyFirst, base), min(card.bodyEnd, base+uint64(len(rows)))
			if end >= start {
				for row := start; row < end; row++ {
					if !card.noticeAt(row) {
						output = append(output, rows[row-base])
					}
				}
			}
		}
		paintWidth := c.width
		if c.chatboxConfig.SurfaceWidth == "content-width" {
			natural := 30
			for _, row := range headers {
				natural = max(natural, ansi.StringWidth(row))
			}
			for _, row := range output {
				natural = max(natural, ansi.StringWidth(row))
			}
			paintWidth = min(c.width, natural+2)
		}
		bodyWidth := max(paintWidth-2, 1)
		for i, header := range headers {
			row := cardRow{line: header, key: card.first, kind: 1, logical: uint64(i), width: paintWidth, state: card.state, target: target, boundary: i == 0 && previousCard}
			actions, saveOffset := "⧉ Copy  ⇩ Save", 8
			if bodyWidth < 32 {
				actions, saveOffset = "⧉ ⇩", 2
			}
			if i == 0 && target != nil && bodyWidth >= ansi.StringWidth(actions) {
				x := paintWidth - ansi.StringWidth(actions)
				row.line = ansi.Truncate(header, max(x-3, 0), "…")
				row.line += strings.Repeat(" ", max(x-2-ansi.StringWidth(row.line), 0)) + actions
				row.copyX, row.saveX = x+1, x+saveOffset+1
			}
			all = append(all, row)
		}
		var bodyRows []cardRow
		cached, exists := s.layouts[card.first]
		active := s.reading == nil && s.pending != nil && s.pending.first == card.first
		if !active && exists && cached.width == bodyWidth && cached.count == len(output) &&
			(len(output) == 0 || cached.first == output[0] && cached.last == output[len(output)-1]) {
			bodyRows = cached.rows
		} else {
			bodyRows = wrapCardRows(output, bodyWidth, card.first, 2, card.state)
			if !active {
				if s.layouts == nil {
					s.layouts = make(map[uint64]cardBodyLayout)
				}
				layout := cardBodyLayout{width: bodyWidth, count: len(output), rows: bodyRows}
				if len(output) > 0 {
					layout.first, layout.last = output[0], output[len(output)-1]
				}
				s.layouts[card.first] = layout
			}
		}
		cap := min(c.chatboxConfig.BlockMaxHeight, max(height-len(headers)-len(card.outcome)-c.chatboxConfig.HistorySpacing, 1))
		top := max(len(bodyRows)-cap, 0)
		if offset, ok := s.offsets[card.first]; ok {
			top = clamp(offset, 0, top)
			s.offsets[card.first] = top
		}
		for _, row := range bodyRows[top:min(top+cap, len(bodyRows))] {
			row.target = target
			row.state = card.state
			all = append(all, row)
		}
		for _, notice := range card.notices {
			for row := max(notice.first, base); row <= notice.last && row < base+uint64(len(rows)); row++ {
				all = append(all, wrapCardRows([]string{rows[row-base]}, width, row, 0, blockNeutral)...)
			}
		}
		for i, outcome := range card.outcome {
			plain := ansi.Strip(outcome)
			left := ansi.StringWidth(plain) - ansi.StringWidth(strings.TrimLeft(plain, " "))
			right := ansi.StringWidth(strings.TrimRight(plain, " "))
			outcome = ansi.Truncate(ansi.Cut(outcome, left, right), bodyWidth, "…")
			if left > 0 {
				outcome = strings.Repeat(" ", max(bodyWidth-ansi.StringWidth(outcome), 0)) + outcome
			}
			all = append(all, cardRow{line: outcome, key: card.first, kind: 3, logical: uint64(i), width: paintWidth, state: card.state, target: target})
		}
		for i := 0; i < c.chatboxConfig.HistorySpacing; i++ {
			all = append(all, cardRow{key: card.first, logical: uint64(i), width: c.width})
		}
		previousCard = true
		next = max(next, card.last+1+uint64(max(c.chatboxConfig.HistorySpacing, 0)))
	}
	appendUnowned(end)
	for key := range s.offsets {
		if !valid[key] {
			delete(s.offsets, key)
		}
	}
	for key := range s.layouts {
		if !valid[key] {
			delete(s.layouts, key)
		}
	}
	if s.focus != nil && !valid[*s.focus] {
		s.focus, s.focused = nil, nil
	}
	navigation := s.history
	if s.reading != nil {
		navigation -= s.reading.history
	}
	if s.total > 0 && (s.history > 0 || s.focus != nil) {
		s.history += len(all) - s.total
	}
	if r := s.reading; r != nil && r.anchor != nil {
		for i, row := range all {
			anchor := r.anchor
			if row.key == anchor.key && row.kind == anchor.kind && row.logical == anchor.logical &&
				(row.column == anchor.column || row.column <= anchor.column && row.end > anchor.column) {
				// Reflow changes preceding display-row counts, not the logical
				// fragment being read. Apply only the user's navigation delta.
				s.history = len(all) - height - i + navigation
				break
			}
		}
	}
	s.total = len(all)
	if s.reveal != nil {
		for i, row := range all {
			if row.key == *s.reveal && row.kind == 1 {
				s.history = max(len(all)-height-i, 0)
				break
			}
		}
		s.reveal = nil
	}
	s.history = clamp(s.history, 0, max(len(all)-height, 0))
	start := max(len(all)-height-s.history, 0)
	s.frame = slices.Clone(all[start:min(start+height, len(all))])
	if r := s.reading; r != nil {
		r.history = s.history
		if len(s.frame) > 0 {
			row := s.frame[0]
			r.anchor = &row
		}
	}
	return s.frame, true
}

func (c *terminalCompositor) paintCardRow(row cardRow, y int) string {
	state := row.state
	if state == blockNeutral && row.kind != 0 && c.viewport.cards.focus != nil && row.key == *c.viewport.cards.focus {
		state = blockFocused
	}
	line := row.line
	if row.kind != 4 {
		line = c.blockStateLine(cardRowText(row), state, row.width)
	}
	if row.kind == 1 && c.viewport.cards.focus != nil && row.key == *c.viewport.cards.focus {
		line = "\x1b[1m" + line + "\x1b[22m"
	}
	// Erasing with the surface already set lets terminals extend that same
	// background into their side margins, just like the full-width input box.
	if state != blockNeutral && row.width == c.width {
		if end := strings.IndexByte(line, 'm'); end >= 0 {
			line = line[:end+1] + "\x1b[2K" + line[end+1:]
		}
	}
	selection := c.viewport.cards.selection
	selected := selection != nil && selection.dragged
	first, last := cardPoint{}, cardPoint{}
	if selected {
		first, last = orderedCardPoints(selection.first, selection.last)
		selected = y >= first.y && y <= last.y
	}
	if !selected {
		return line
	}
	left, right := 0, c.width
	if y == first.y {
		left = first.x
	}
	if y == last.y {
		right = last.x + 1
	}
	drawing := uv.NewScreenBuffer(c.width, 1)
	drawing.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(drawing, uv.Rect(0, 0, c.width, 1))
	cells := drawing.Line(0)
	for x := 0; x < len(cells); {
		width := max(cells[x].Width, 1)
		if selected && x+width > left && x < right {
			cells[x].Style.Attrs |= uv.AttrReverse
		}
		x += width
	}
	painted := cells.Render()
	if row.width == c.width && len(cells) > 0 && cells[0].Style.Bg != nil {
		background := uv.Style{Bg: cells[0].Style.Bg}
		painted = background.String() + "\x1b[2K" + painted
	}
	return painted
}

func cardRowText(row cardRow) string {
	if row.source != "" {
		return ansi.Cut(row.source, row.column, row.end)
	}
	return row.line
}

func orderedCardPoints(a, b cardPoint) (cardPoint, cardPoint) {
	if a.y > b.y || a.y == b.y && a.x > b.x {
		return b, a
	}
	return a, b
}

func (s *outputCards) readingActive() bool {
	return s.reading != nil || s.focus != nil || s.history > 0 || s.browsing
}

func (c *terminalCompositor) handleCardKey(key string) bool {
	s := &c.viewport.cards
	move := func(delta int) {
		if s.focus == nil {
			if delta < 0 {
				c.freezeCardReading()
			}
			s.browsing = s.browsing || delta < 0
			s.history = max(s.history-delta, 0)
			return
		}
		if s.offsets == nil {
			s.offsets = make(map[uint64]int)
		}
		current, ok := s.offsets[*s.focus]
		if !ok {
			for _, row := range s.frame {
				if row.key == *s.focus && row.kind == 2 {
					current = row.index
					break
				}
			}
		}
		s.offsets[*s.focus] = max(current+delta, 0)
	}
	switch key {
	case "\x1b":
		if !s.readingActive() && c.viewport.executions.notice == "" {
			return false
		}
		c.latestCardReading()
	case "\x1b[5~":
		move(-max(c.height-c.surfaceRows-2, 1))
	case "\x1b[6~":
		if !s.readingActive() {
			return false
		}
		move(max(c.height-c.surfaceRows-2, 1))
		if s.focus == nil && s.history == 0 && s.reading != nil {
			c.latestCardReading()
		}
	case "\x1b[A", "\x1bOA", "\x1b[B", "\x1bOB":
		if s.focus == nil {
			if (s.history > 0 || s.reading != nil) && (key == "\x1b[B" || key == "\x1bOB") {
				c.latestCardReading()
				return true
			}
			return false
		}
		step := 1
		if key == "\x1b[A" || key == "\x1bOA" {
			step = -1
		}
		move(step)
	case "\x1b[F", "\x1bOF", "\x1b[4~":
		if !s.readingActive() {
			return false
		}
		c.latestCardReading()
	case "\x1b[21~", "\x1b[21;2~":
		if len(s.completed) > 0 {
			c.freezeCardReading()
		}
		cards := s.completed
		if s.reading != nil {
			cards = s.reading.cards[:s.reading.completed]
		}
		var chosen *outputCard
		for i := range cards {
			card := &cards[i]
			if s.focus == nil || key == "\x1b[21~" && card.first < *s.focus {
				chosen = card
			} else if key == "\x1b[21;2~" && card.first > *s.focus {
				chosen = card
				break
			}
		}
		if chosen != nil {
			first, _ := c.viewport.model.LogicalRowPositions()
			rows := retainedModelRows(c.viewport.model)
			s.focused = nil
			if target := c.viewport.cardTarget(*chosen, rows, first); target != nil {
				copy := *target
				copy.rows = slices.Clone(copy.rows)
				s.focused = &copy
			}
			id := chosen.first
			s.focus = &id
			s.reveal = &id
			s.browsing = true
			c.viewport.executions.selected = s.focused
		}
		if chosen == nil {
			return len(cards) > 0
		}
	case "\x1b[23~", "\x1b[24~":
		if s.focused == nil {
			if s.focus != nil {
				c.viewport.executions.notice = "Block output is not captured"
				return true
			}
			return false
		}
		if key == "\x1b[23~" {
			c.copyCardOutput(s.focused)
		} else {
			c.exportOutputSnapshot(*s.focused)
		}
	default:
		return false
	}
	return true
}

func (card outputCard) noticeAt(row uint64) bool {
	i := sort.Search(len(card.notices), func(i int) bool { return card.notices[i].last >= row })
	return i < len(card.notices) && card.notices[i].first <= row
}

func (s *outputCards) clearSelection() {
	s.restoreInput = s.restoreInput || s.selection != nil
	s.selection, s.press = nil, nil
}

func (s *outputCards) releasePointer() {
	s.clearSelection()
	s.frame = nil
}

func (s *cardSelection) text() string {
	if !s.dragged || len(s.frame) == 0 {
		return ""
	}
	first, last := orderedCardPoints(s.first, s.last)
	var text strings.Builder
	var previous *cardRow
	for y := max(first.y, 0); y <= last.y && y < len(s.frame); y++ {
		row := &s.frame[y]
		content := cardRowText(*row)
		left, right := 0, ansi.StringWidth(content)
		inset := 2
		if row.kind == 4 {
			inset = 0
		}
		if y == first.y {
			left = max(first.x-inset, 0)
		}
		if y == last.y {
			right = max(last.x+1-inset, 0)
		}
		if previous != nil && (previous.key != row.key || previous.kind != row.kind || previous.logical != row.logical) {
			text.WriteByte('\n')
		}
		column := 0
		for rest := ansi.Strip(content); len(rest) > 0; {
			cluster, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
			if column+width > left && column < right {
				text.WriteString(cluster)
			}
			column += width
			rest = rest[len(cluster):]
		}
		previous = row
	}
	return text.String()
}

func (c *terminalCompositor) copyCardText(text string) {
	e := &c.viewport.executions
	switch {
	case text == "":
		e.notice = "No retained text to copy"
	case len(text) > executionClipboardBytes:
		e.notice = "Copy exceeds clipboard limit · save instead"
	default:
		_, _ = fmt.Fprintf(c.out, "\x1b]52;c;%s\x1b\\", base64.StdEncoding.EncodeToString([]byte(text)))
		e.notice = "Copy requested · terminal permission may be needed"
	}
}

func (c *terminalCompositor) copyCardOutput(target *retainedExecution) {
	if target == nil || target.applicationOwned {
		c.viewport.executions.notice = "Application output is not captured"
		return
	}
	c.copyCardText(target.plainText())
	if target.truncated {
		c.viewport.executions.notice = "Output truncated · " + c.viewport.executions.notice
	}
}

func (c *terminalCompositor) handleOutputInteraction(data []byte, suggestions bool) bool {
	cards, plain := c.cardProjectionAvailable(), c.plainOutputSelectionAvailable()
	if (!cards && !plain) || suggestions {
		return false
	}
	s := &c.viewport.cards
	if !c.chatboxConfig.CopyOnSelect && string(data) == "\x03" && s.selection != nil && s.selection.dragged {
		if text := s.selection.text(); text != "" {
			c.copyCardText(text)
			c.viewport.dirty = true
			c.renderOutputViewport()
			return true
		}
	}
	button, x, y, pressed, mouse := viewportMouseReport(data)
	if !mouse {
		if s.selection != nil {
			s.clearSelection()
			c.viewport.dirty = true
		}
		if !cards {
			return false
		}
		if string(data) == "\x1b[18~" {
			// Search takes over the drawing surface, not the selected execution.
			return false
		}
		if c.handleCardKey(string(data)) {
			c.viewport.dirty = true
			c.renderOutputViewport()
			return true
		}
		s.focus, s.focused = nil, nil
		c.viewport.executions.selected = nil
		return false
	}
	if !c.interactiveOutputMouse() {
		return false
	}
	if plain && s.press == nil && c.viewport.frozen != nil && (button&32 != 0 || y == c.viewportBadgeRow()) {
		return false
	}
	x, y = x-1, y-1
	if button&^28 == 0 && pressed && s.reading != nil {
		_, left, width := c.viewportBadge()
		if y+1 == c.viewportBadgeRow() && x >= left && x < left+width {
			c.latestCardReading()
			c.viewport.dirty = true
			c.renderOutputViewport()
			return true
		}
	}
	if button&64 != 0 {
		wheel, _ := viewportWheel(data, c.width, c.height-c.surfaceRows)
		if wheel == 0 {
			return true
		}
		if !cards {
			s.clearSelection()
			return false
		}
		direction := 3 * wheel
		s.clearSelection()
		if s.focus != nil {
			key := *s.focus
			if s.offsets == nil {
				s.offsets = make(map[uint64]int)
			}
			current, ok := s.offsets[key]
			if !ok {
				for _, row := range s.frame {
					if row.key == key && row.kind == 2 {
						current = row.index
						break
					}
				}
			}
			s.offsets[key] = max(current+direction, 0)
		} else {
			if direction < 0 {
				c.freezeCardReading()
			}
			s.history = max(s.history-direction, 0)
			s.browsing = s.browsing || direction < 0
			if direction > 0 && s.history == 0 && s.reading != nil {
				c.latestCardReading()
			}
		}
	} else if button&3 == 0 && button&28 == 0 {
		point := cardPoint{x, y}
		if button&32 != 0 {
			if s.selection != nil {
				s.selection.last = point
				s.selection.dragged = s.selection.dragged || point != s.selection.first
			}
		} else if pressed {
			s.clearSelection()
			s.focus, s.focused = nil, nil
			c.viewport.executions.selected, c.viewport.executions.notice = nil, ""
			// Freezing the plain model releases pointer state; capture exactly
			// the visible frame before that transition, as card reading does.
			frame := slices.Clone(s.frame)
			if plain && c.viewport.frozen == nil {
				// ED2 can hide retained history, and stored rows can coalesce
				// soft wraps. Freeze at the SDK's actual logical canvas origin.
				logical, column := c.viewport.model.LogicalCanvasOrigin()
				c.freezeOutputViewport()
				for i, span := range c.viewport.frozenLayout {
					if c.viewport.frozenBase+uint64(span.logical) == logical && span.first == column {
						c.viewport.top = i
						break
					}
				}
			}
			s.press = &cardPress{point: point}
			if cards && y >= 0 && y < len(s.frame) {
				row := s.frame[y]
				if row.kind != 0 && x >= 0 && x < row.width {
					c.freezeCardReading()
					key := row.key
					s.focus = &key
					if row.target != nil {
						copy := *row.target
						copy.rows = slices.Clone(copy.rows)
						s.focused = &copy
					}
				}
				if row.copyX > 0 && x+1 >= row.copyX && x+1 < row.saveX {
					s.press.action = 1
				} else if row.saveX > 0 && x+1 >= row.saveX && x < row.width {
					s.press.action = 2
				}
				s.press.target = s.focused
			}
			for len(frame) < c.height-c.surfaceRows {
				frame = append(frame, cardRow{width: c.width})
			}
			for i, line := range c.renderedLines {
				frame = append(frame, cardRow{line: line, kind: 4, logical: uint64(i), width: c.width})
			}
			s.selection = &cardSelection{first: point, last: point, frame: frame}
		} else {
			if s.selection != nil {
				s.selection.last = point
				s.selection.dragged = s.selection.dragged || point != s.selection.first
			}
			if p := s.press; p != nil && s.selection != nil {
				if s.selection.dragged {
					if c.chatboxConfig.CopyOnSelect {
						if text := s.selection.text(); text != "" {
							c.copyCardText(text)
						}
					}
				} else if p.point == point && p.target != nil && p.action == 1 {
					c.copyCardOutput(p.target)
				} else if p.point == point && p.target != nil && p.action == 2 {
					c.exportOutputSnapshot(*p.target)
				}
			}
			s.press = nil
		}
	} else {
		return true
	}
	c.viewport.dirty = true
	c.renderOutputViewport()
	return true
}
