package vt

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestCompletedRetainedResizeDoesNotCloneEveryOwnedRow(t *testing.T) {
	allocations := func(count int) float64 {
		rows := make([]retainedRow, count)
		for i := range rows {
			line := uv.NewLine(8)
			for x := range line {
				line[x] = uv.Cell{Content: "x", Width: 1}
			}
			// resizeRetained has already detached these complete logical rows
			// from authoritative cells before rebuilding historical storage.
			rows[i] = retainedRow{cells: line, used: len(line)}
		}
		store := NewScrollback(count)
		return testing.AllocsPerRun(5, func() { store.replaceOwnedRetained(rows) })
	}
	small, large := allocations(128), allocations(256)
	// Doubling rows may grow metadata arrays, but must not add one styled-cell
	// allocation per added row. This is not a wall-clock or process-heap budget.
	if large-small >= 128 {
		t.Fatalf("rebuilding owned whole rows still allocates per row: 128=%g, 256=%g", small, large)
	}
}

func TestCompletedRetainedResizeKeepsCellOwnersIndependent(t *testing.T) {
	e := retainedFixture(t, 80, 2)
	text := "first café 日本語\r\nsecond 👩‍💻\r\nthird\r\nlast"
	writeFixture(t, e, []byte(text))
	want := strings.ReplaceAll(text, "\r\n", "\n")
	old := e.Scrollback().Line(0)
	e.Resize(120, 2)
	old[0].Content = "OLD"
	projection := e.LogicalLines()
	projection[0][0].Content = "PROJECTION"
	if got := retainedText(e); got != want {
		t.Fatalf("old cells or defensive projection changed resized state: %q", got)
	}
	e.Scrollback().Line(0)[0].Content = "F"
	writeFixture(t, e, []byte("!"))
	if got := retainedText(e); got != "F"+want[1:]+"!" {
		t.Fatalf("history alias and live canvas share cells or lost mutable semantics: %q", got)
	}
}
