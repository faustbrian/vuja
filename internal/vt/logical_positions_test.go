package vt

import (
	"strings"
	"testing"
)

func TestRetainedLogicalRowPositionsSurviveReflowAndEviction(t *testing.T) {
	e := retainedFixture(t, 20, 4)
	e.SetScrollbackSize(2)
	for range 10 {
		writeFixture(t, e, []byte("long logical 日本語 line wrapping here\r\n"))
	}
	for _, size := range [][2]int{{3, 2}, {80, 10}, {12, 4}} {
		e.Resize(size[0], size[1])
		first, rows := e.LogicalRowPositions()
		if got := rows[e.CursorPosition().Y]; got != 10 {
			t.Fatalf("resize %v reassigned logical cursor row: %d (first %d)", size, got, first)
		}
	}
	writeFixture(t, e, []byte("next\r\nlast\r\n"))
	_, rows := e.LogicalRowPositions()
	if rows[e.CursorPosition().Y] != 12 {
		t.Fatal("append/eviction reassigned logical row")
	}
}

func TestLogicalCanvasOriginTracksSoftWrappedPrefix(t *testing.T) {
	e := retainedFixture(t, 6, 3)
	if logical, column := e.LogicalCanvasOrigin(); logical != 0 || column != 0 {
		t.Fatalf("empty canvas origin = (%d,%d)", logical, column)
	}
	writeFixture(t, e, []byte(strings.Repeat("abcdef", 10)))
	if logical, column := e.LogicalCanvasOrigin(); logical != 0 || column != 42 {
		t.Fatalf("soft-wrapped canvas origin = (%d,%d), want (0,42)", logical, column)
	}
	e.Resize(4, 3)
	if logical, column := e.LogicalCanvasOrigin(); logical != 0 || column != 48 {
		t.Fatalf("reflowed canvas origin = (%d,%d), want (0,48)", logical, column)
	}
}

func TestRetainedTailDrawingPreservesControlsAndOwnership(t *testing.T) {
	e := retainedFixture(t, 60, 4)
	writeFixture(t, e, []byte(strings.Repeat("a", 59)+"\rZ\r\n"))
	want := "Z" + strings.Repeat("a", 58)
	if got := e.LogicalLines()[0].Render(); got != want {
		t.Fatalf("raw terminal fixture is wrong: %q", got)
	}
	rows := e.RetainedTail(58, 3)
	if len(rows) != 3 || rows[0].Logical != 0 || rows[1].Logical != 0 || rows[2].Logical != 1 || rows[0].Line[0].Content != "Z" || rows[1].Line[0].Content != "a" {
		t.Fatalf("drawing fragments lost logical identity or rightmost text: %+v", rows)
	}
	rows[0].Line[0].Content = "mutated"
	if got := e.LogicalLines()[0].Render(); got != want {
		t.Fatalf("drawing projection mutated authoritative output: %q", got)
	}
	writeFixture(t, e, []byte("\x1b[H\x1b[2Jnew"))
	rows = e.RetainedTail(58, 4)
	if len(rows) != 1 || rows[0].Line.Render() != "new" {
		t.Fatalf("live drawing resurrected ED2 history: %+v", rows)
	}
	if got := e.LogicalLines()[0].Render(); got != want {
		t.Fatal("drawing clear boundary erased stored history")
	}
}

func TestRetainedTailDrawingKeepsWideCellsAndBoundsFragments(t *testing.T) {
	e := retainedFixture(t, 60, 4)
	writeFixture(t, e, []byte("ab界Z"))
	rows := e.RetainedTail(3, 2)
	if len(rows) != 2 || rows[0].Line[0].Content != "a" || rows[0].Line[1].Content != "b" || rows[1].Line[0].Content != "界" || rows[1].Line[0].Width != 2 || rows[1].Line[2].Content != "Z" {
		t.Fatalf("wide-cell drawing boundary is wrong: %+v", rows)
	}
	writeFixture(t, e, []byte(strings.Repeat("x", 20000)))
	rows = e.RetainedTail(3, 2)
	if len(rows) != 2 || len(rows[0].Line) != 3 || len(rows[1].Line) != 3 {
		t.Fatal("one long logical line allocated an unbounded drawing result")
	}
}
