package root

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/norm"
)

const outputSearchQueryLimit = 256
const outputSearchMatchLimit = 4096

type outputSearchMatch struct {
	row, first, last int // display columns, not UTF-8 byte offsets
}

type outputSearch struct {
	query   []rune
	pending []byte
	matches []outputSearchMatch
	index   int
	limited bool
}

func (c *terminalCompositor) OutputSearchActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outputViewportEnabled() && c.viewport != nil && c.viewport.search != nil
}

func (c *terminalCompositor) PasteOutputSearch(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.outputViewportEnabled() || c.viewport == nil || c.viewport.search == nil {
		return
	}
	c.viewport.search.appendText(data)
	c.refreshOutputSearch()
	c.viewport.dirty = true
	c.renderOutputViewport()
}

func (s *outputSearch) appendText(data []byte) {
	s.pending = append(s.pending, data...)
	for len(s.pending) > 0 && utf8.FullRune(s.pending) {
		r, size := utf8.DecodeRune(s.pending)
		s.pending = s.pending[size:]
		if (unicode.IsPrint(r) || r == '\u200c' || r == '\u200d') && len(s.query) < outputSearchQueryLimit {
			s.query = append(s.query, r)
		}
	}
	if len(s.pending) == 0 {
		s.pending = nil
	} else {
		// Retain only the incomplete UTF-8 tail, not the backing paste buffer.
		tail := make([]byte, len(s.pending))
		copy(tail, s.pending)
		s.pending = tail
	}
}

func (c *terminalCompositor) freezeOutputViewport() {
	v := c.viewport
	if v.frozen != nil {
		return
	}
	var rows []string
	var base uint64
	var states []blockState
	var dividers map[int]bool
	truncated := v.outputTruncated()
	if reading := v.cards.reading; reading != nil {
		// Search and whole-pane export keep the same immutable document
		// that card browsing established, even after live source eviction.
		rows, base, truncated = reading.rows, reading.base, reading.truncated
		dividers = reading.dividers
		states = make([]blockState, len(rows))
		for i := range states {
			states[i] = blockStateAt(reading.states, base+uint64(i))
		}
	} else {
		rows = retainedModelRows(v.model)
		base, _ = v.model.LogicalRowPositions()
		states = v.rowStates(len(rows))
		dividers = v.paneDividerIndexes(base, rows)
	}
	v.setFrozen(rows)
	v.frozenBase = base
	v.frozenPane = true
	v.frozenStates = states
	v.frozenDividers = dividers
	v.frozenTruncated = truncated
	v.layoutFrozen(c.outputContentWidth())
	v.top = max(len(v.frozenLayout)-(c.height-c.surfaceRows), 0)
}

// This mode owns its query only after an explicit output-search action. Its
// input is never sent to the shell, even when it contains Enter/control bytes.
func (c *terminalCompositor) handleOutputSearch(data []byte) bool {
	v := c.viewport
	if string(data) == "\x1b[18~" {
		c.freezeOutputViewport()
		if v.search == nil {
			v.search = &outputSearch{index: -1}
		}
	} else if v.search == nil {
		return false
	} else {
		v.executions.notice = ""
		s := v.search
		switch string(data) {
		case "\x1b", "\x03":
			v.search = nil
		case "\r", "\n", "\x1b[19~", "\x1b[B", "\x1bOB":
			c.advanceOutputSearch(1)
		case "\x1b[19;2~", "\x1b[A", "\x1bOA":
			c.advanceOutputSearch(-1)
		case "\x7f", "\x08":
			if len(s.pending) > 0 {
				s.pending = nil
			} else if len(s.query) > 0 {
				s.query = s.query[:len(s.query)-1]
			}
			c.refreshOutputSearch()
		case "\x15":
			s.query, s.pending = nil, nil
			c.refreshOutputSearch()
		default:
			// Unknown escape sequences are modal controls, never query text.
			if len(data) > 0 && data[0] != '\x1b' {
				s.appendText(data)
				c.refreshOutputSearch()
			}
		}
	}
	v.dirty = true
	c.renderOutputViewport()
	return true
}

func (c *terminalCompositor) refreshOutputSearch() {
	v, s := c.viewport, c.viewport.search
	s.matches = nil
	s.index = -1
	s.limited = false
	if len(s.query) == 0 {
		return
	}
	needle := foldedOutputSearchText(string(s.query))
scan:
	for row, line := range v.frozen {
		if v.frozenDividers[row] {
			continue
		}
		text := ansi.Strip(line)
		normalized := foldedOutputSearchText(text)
		if !strings.Contains(normalized, needle) {
			continue
		}
		// Normalize once per row, then map matching byte ranges to complete
		// grapheme columns. Lowercasing can change byte length; width can differ
		// from rune count (combining marks, wide text and joined emoji).
		type columnSpan struct{ end, first, last int }
		var spans []columnSpan
		column, position := 0, 0
		for rest := text; len(rest) > 0; {
			cluster, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
			position += len(foldedOutputSearchText(cluster))
			spans = append(spans, columnSpan{position, column, column + width})
			column += width
			rest = rest[len(cluster):]
		}
		for offset := 0; offset < len(normalized); {
			index := strings.Index(normalized[offset:], needle)
			if index < 0 {
				break
			}
			index += offset
			if len(s.matches) == outputSearchMatchLimit {
				s.limited = true
				break scan
			}
			first := sort.Search(len(spans), func(i int) bool { return spans[i].end > index })
			last := sort.Search(len(spans), func(i int) bool { return spans[i].end >= index+len(needle) })
			s.matches = append(s.matches, outputSearchMatch{row: row, first: spans[first].first, last: spans[last].last})
			_, size := utf8.DecodeRuneInString(normalized[index:])
			offset = index + size
		}
	}
	if len(s.matches) > 0 {
		s.index = 0
		c.revealOutputSearchMatch()
	}
}

func foldedOutputSearchText(text string) string {
	// Search only decoded display text. Canonical folding must never rewrite
	// retained OSC/link payloads or the shell's input bytes.
	return norm.NFC.String(strings.ToLower(text))
}

func (c *terminalCompositor) advanceOutputSearch(direction int) {
	s := c.viewport.search
	if len(s.matches) == 0 {
		return
	}
	s.index = (s.index + direction + len(s.matches)) % len(s.matches)
	c.revealOutputSearchMatch()
}

func (c *terminalCompositor) revealOutputSearchMatch() {
	v := c.viewport
	match := v.search.matches[v.search.index]
	v.layoutFrozen(c.outputContentWidth())
	for i, span := range v.frozenLayout {
		if span.logical == match.row && span.last > match.first {
			v.top = i
			return
		}
	}
}

func (s *outputSearch) label(width int, truncated, newOutput bool, notice string) string {
	limit := ""
	if s.limited {
		limit = "+"
	}
	position := fmt.Sprintf("%d/%d%s", s.index+1, len(s.matches), limit)
	label := " Find: " + position
	if truncated {
		if width < 40 {
			label = position + " truncated"
		} else {
			label += " · output truncated"
		}
	}
	// Search position and loss are critical; secondary messages and long
	// queries may be clipped, but must never displace those first fields.
	if notice != "" {
		label += " · " + notice
	}
	if newOutput {
		label += " · New activity"
	}
	return label + " · " + string(s.query) + " · Enter/↓ next · ↑ prev · esc close "
}

func (c *terminalCompositor) outputSearchLine(line string, row, left, right int, severitySurface bool) string {
	s := c.viewport.search
	if s == nil || s.index < 0 || s.matches[s.index].row != row {
		return ansi.Cut(line, left, right)
	}
	m := s.matches[s.index]
	first, last := max(m.first, left), min(m.last, right)
	if first >= last {
		return ansi.Cut(line, left, right)
	}
	selected := ansi.Cut(line, first, last)
	start, end := "\x1b[7m", "\x1b[27m"
	if severitySurface {
		// Reverse would replace the block's error/warning background. A separate
		// double underline keeps the selected match visible on that owned surface.
		start, end = "\x1b[4:2m", "\x1b[24m"
	}
	var highlight strings.Builder
	highlight.WriteString(start)
	var state byte
	for len(selected) > 0 {
		sequence, _, n, next := ansi.DecodeSequence(selected, state, nil)
		if n == 0 {
			sequence, n, next = selected[:1], 1, ansi.NormalState
		}
		highlight.WriteString(sequence)
		if strings.HasPrefix(sequence, "\x1b[") && strings.HasSuffix(sequence, "m") {
			// Original SGR resets must not erase the selected-match highlight.
			// OSC links and their payloads remain untouched.
			highlight.WriteString(start)
		}
		selected = selected[n:]
		state = next
	}
	highlight.WriteString(end)
	return ansi.Cut(line, left, first) + highlight.String() + ansi.Cut(line, last, right)
}
