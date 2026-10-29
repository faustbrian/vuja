package root

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Frozen text stays logical and immutable. This is only a drawing index into
// that snapshot: it cannot erase text or reinterpret terminal controls.
type frozenOutputRow struct {
	logical, first, last int
}

func (v *outputViewport) setFrozen(rows []string) {
	v.cards.releasePointer()
	v.cards.reading = nil
	v.cards.history, v.cards.total, v.cards.browsing = 0, 0, false
	v.cards.focus, v.cards.focused = nil, nil
	v.frozen = rows
	v.frozenPane = false
	v.frozenStates = nil
	v.frozenDividers = nil
	v.frozenLayout = nil
	v.frozenOrigin = nil
	v.frozenColumns = nil
	v.frozenWidth = 0
	v.frozenTruncated = false
}

func (v *outputViewport) layoutFrozen(width int) {
	if v.frozenWidth == width {
		return
	}
	anchor := frozenOutputRow{}
	if len(v.frozenLayout) > 0 {
		anchor = v.frozenLayout[min(v.top, len(v.frozenLayout)-1)]
	}
	v.frozenLayout = nil
	for row, line := range v.frozen {
		first, column := 0, 0
		text := ansi.Strip(line)
		if row < len(v.frozenColumns) {
			// UV Line.Render omits trailing EmptyCell spaces. SDK LogicalLines
			// retains their used/wrap extent; drawing needs those blank columns
			// without padding the immutable search/export document itself.
			text += strings.Repeat(" ", max(v.frozenColumns[row]-ansi.StringWidth(text), 0))
		}
		for len(text) > 0 {
			cluster, size := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
			// SDK prefix eviction is cluster-aligned, not width-aligned
			// (vt/scrollback.go:trimRetained). Keep the live canvas seam
			// addressable without cropping the immutable retained document.
			atOrigin := v.frozenOrigin != nil && v.frozenOrigin.logical == row && v.frozenOrigin.first == column
			if column > first && (column-first+size > width || atOrigin) {
				v.frozenLayout = append(v.frozenLayout, frozenOutputRow{row, first, column})
				first = column
			}
			column += size
			text = text[len(cluster):]
		}
		v.frozenLayout = append(v.frozenLayout, frozenOutputRow{row, first, column})
	}
	v.frozenWidth = width
	for i, row := range v.frozenLayout {
		if row.logical == anchor.logical && (row.last > anchor.first || row.first == anchor.first) {
			v.top = i
			break
		}
	}
}
