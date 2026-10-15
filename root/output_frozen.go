package root

import "github.com/charmbracelet/x/ansi"

// Frozen text stays logical and immutable. This is only a drawing index into
// that snapshot: it cannot erase text or reinterpret terminal controls.
type frozenOutputRow struct {
	logical, first, last int
}

func (v *outputViewport) setFrozen(rows []string) {
	v.frozen = rows
	v.frozenLayout = nil
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
		for text := ansi.Strip(line); len(text) > 0; {
			cluster, size := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
			if column > first && column-first+size > width {
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
