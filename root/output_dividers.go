package root

import "sort"

func (v *outputViewport) paneDividerAt(row uint64, line string) bool {
	if !isOutputBlockDivider(line) {
		return false
	}
	i := sort.Search(len(v.blocks), func(i int) bool { return v.blocks[i].first >= row })
	if i < len(v.blocks) && v.blocks[i].first == row && v.blocks[i].divider {
		return true
	}
	return v.cards.pending != nil && v.cards.pending.first == row && v.cards.pending.divider
}

// Divider origin belongs to compositor-written logical rows, not OSC8 text:
// programs may emit the same URI and glyph. Unattributed legacy rows remain
// ordinary text rather than risking silent deletion from search or exports.
func (v *outputViewport) paneDividerIndexes(base uint64, rows []string) map[int]bool {
	var indexes map[int]bool
	add := func(first uint64, divider bool) {
		if divider && first >= base && first-base < uint64(len(rows)) && isOutputBlockDivider(rows[first-base]) {
			if indexes == nil {
				indexes = make(map[int]bool)
			}
			indexes[int(first-base)] = true
		}
	}
	for _, block := range v.blocks {
		add(block.first, block.divider)
	}
	if card := v.cards.pending; card != nil {
		add(card.first, card.divider)
	}
	return indexes
}

func withoutPaneDividers(rows []string, dividers map[int]bool) []string {
	if len(dividers) == 0 {
		return rows
	}
	kept := make([]string, 0, len(rows))
	for i, row := range rows {
		if !dividers[i] {
			kept = append(kept, row)
		}
	}
	return kept
}
