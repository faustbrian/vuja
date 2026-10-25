package vt

import (
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func retainedText(e *Emulator) string {
	lines := e.LogicalLines()
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = line.String()
	}
	return strings.Join(plain, "\n")
}

func retainedFixture(t *testing.T, width, height int) *Emulator {
	t.Helper()
	e := NewEmulator(width, height)
	e.SetReflow(true)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestRetainedResizeKeepsLogicalTextAcrossBothDimensions(t *testing.T) {
	e := retainedFixture(t, 80, 8)
	text := "first abcdefghijklmnopqrstuvwxyz\nsecond  with  spaces\nlast 日本語 👩‍💻"
	writeFixture(t, e, []byte(strings.ReplaceAll(text, "\n", "\r\n")))
	for _, size := range [][2]int{{20, 8}, {1, 1}, {7, 2}, {120, 12}, {80, 8}} {
		e.Resize(size[0], size[1])
		if got := retainedText(e); got != text {
			t.Fatalf("resize %v changed retained text: %q", size, got)
		}
		if e.RetainedOutputTruncated() {
			t.Fatalf("resize %v consumed configured retention", size)
		}
	}
}

func TestRetainedResizePreservesCursorAboveTheTail(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	writeFixture(t, e, []byte("top\r\nmiddle\r\nbottom\x1b[1;1H"))
	e.Resize(8, 1)
	if got := retainedText(e); got != "top\nmiddle\nbottom" {
		t.Fatalf("height shrink discarded the hidden tail: %q", got)
	}
	writeFixture(t, e, []byte("X"))
	e.Resize(40, 5)
	if got := retainedText(e); got != "Xop\nmiddle\nbottom" {
		t.Fatalf("resized cursor did not edit its original logical row: %q", got)
	}
}

func TestRetainedResizePreservesPendingWrap(t *testing.T) {
	for _, width := range []int{10, 20, 40} {
		e := retainedFixture(t, 20, 4)
		writeFixture(t, e, []byte("abcdefghijklmnopqrst"))
		e.Resize(width, 4)
		writeFixture(t, e, []byte("x"))
		if got := retainedText(e); got != "abcdefghijklmnopqrstx" {
			t.Fatalf("pending wrap at new width %d overwrote text: %q", width, got)
		}
	}
}

func TestRetainedResizeUsesSemanticOverwriteAndErasure(t *testing.T) {
	e := retainedFixture(t, 80, 4)
	e.Resize(20, 4)
	writeFixture(t, e, []byte("abcdefghijklmnopqrstuvwxyz\rREPLACED\x1b[K\r\n"))
	if got := retainedText(e); got != "abcdefghijklmnopqrstREPLACED\n" {
		t.Fatalf("projection kept erased bytes or inserted a soft-wrap newline: %q", got)
	}
	e.Resize(80, 4)
	if got := retainedText(e); got != "abcdefghijklmnopqrstREPLACED\n" {
		t.Fatalf("reflow resurrected erased bytes: %q", got)
	}
}

func TestRetainedResizeRespectsDisabledAutowrap(t *testing.T) {
	e := retainedFixture(t, 20, 4)
	writeFixture(t, e, []byte("\x1b[?7labcdefghijklmnopqrstuvwxyz"))
	if got := retainedText(e); got != "abcdefghijklmnopqrsz" {
		t.Fatalf("disabled autowrap did not overwrite the final cell: %q", got)
	}
	e.Resize(10, 4)
	writeFixture(t, e, []byte("Z"))
	if got := retainedText(e); got != "abcdefghijklmnopqrsZ" {
		t.Fatalf("resize altered disabled-autowrap cursor semantics: %q", got)
	}
}

func TestRetainedProjectionKeepsMutationAliasesAndIsDefensive(t *testing.T) {
	e := retainedFixture(t, 6, 2)
	writeFixture(t, e, []byte("abcdefghi"))
	e.CellAt(1, 0).Content = "B"
	e.SetCell(2, 1, &uv.Cell{Content: "I", Width: 1})
	if got := retainedText(e); got != "aBcdefghI" {
		t.Fatalf("projection did not read current mutable screen cells: %q", got)
	}
	lines := e.LogicalLines()
	lines[0][0].Content = "corrupt"
	if got := retainedText(e); got != "aBcdefghI" {
		t.Fatalf("projection aliases live model cells: %q", got)
	}
	writeFixture(t, e, []byte("\r\nnext\r\n"))
	if e.ScrollbackLen() == 0 {
		t.Fatal("fixture never reached scrollback")
	}
	e.Scrollback().Line(0)[0].Content = "A"
	if !strings.HasPrefix(retainedText(e), "ABcdefghI") {
		t.Fatal("projection stopped observing the public scrollback alias")
	}
}

func TestRetainedResizeDoesNotChangeTheDefaultCanvasPolicy(t *testing.T) {
	e := NewEmulator(20, 4)
	t.Cleanup(func() { _ = e.Close() })
	writeFixture(t, e, []byte("abcdefghijklmnopqrstuvwxyz"))
	e.Resize(10, 4)
	if e.Width() != 10 || e.Height() != 4 || e.CellAt(10, 0) != nil {
		t.Fatal("non-opted-in canvas no longer has upstream resize geometry")
	}
}

func TestRetainedScrollbackLimitCountsLogicalLinesNotResizeRows(t *testing.T) {
	e := retainedFixture(t, 80, 1)
	e.SetScrollbackSize(3)
	first, second, third := strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 100)
	writeFixture(t, e, []byte(first+"\r\n"+second+"\r\n"+third+"\r\n"))
	for _, width := range []int{20, 1, 80} {
		e.Resize(width, 1)
		if got := retainedText(e); got != first+"\n"+second+"\n"+third+"\n" {
			t.Fatalf("geometry spent the logical retention budget at width %d: %q", width, got)
		}
		if e.RetainedOutputTruncated() {
			t.Fatal("reshape was falsely reported as eviction")
		}
	}
	writeFixture(t, e, []byte("fourth\r\n"))
	if got := retainedText(e); got != second+"\n"+third+"\nfourth\n" || !e.RetainedOutputTruncated() {
		t.Fatalf("actual retention did not evict exactly the oldest logical line: %q", got)
	}
}

func TestRetainedScrollbackBoundsLongSessionsAndMutableAliasEdits(t *testing.T) {
	e := retainedFixture(t, 120, 1)
	e.SetScrollbackSize(10000)
	for range 10000 {
		writeFixture(t, e, []byte(strings.Repeat("x", 120)+"\r\n"))
	}
	s := e.Scrollback()
	if !e.RetainedOutputTruncated() || s.cost > retainedScrollbackBytes || s.Len() >= 10000 {
		t.Fatal("long-session output escaped its independent cell/text budget")
	}
	line := s.Line(0)
	line[0].Content = strings.Repeat("x", retainedScrollbackBytes+1)
	writeFixture(t, e, []byte("last\r\n"))
	if s.cost > retainedScrollbackBytes || !strings.HasSuffix(retainedText(e), "last\n") {
		t.Fatal("an outstanding mutable alias defeated later retention or lost newest output")
	}
	e.ClearScrollback()
	if s.Len() != 0 || s.cost != 0 || e.RetainedOutputTruncated() {
		t.Fatal("explicit scrollback clear retained content or stale truncation state")
	}
}

func TestRetainedEraseScreenRetainsHiddenTail(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	writeFixture(t, e, []byte("top\r\nmiddle\r\nbottom\x1b[1;1H"))
	e.Resize(8, 1)
	writeFixture(t, e, []byte("\x1b[2J"))
	got := retainedText(e)
	for _, want := range []string{"top", "middle", "bottom"} {
		if !strings.Contains(got, want) {
			t.Errorf("erase-screen scrollback preservation lost hidden retained %q: %q", want, got)
		}
	}
}

func TestRetainedSafeEmulatorSerializesProjectionWriteAndResize(t *testing.T) {
	e := NewSafeEmulator(20, 5)
	e.SetReflow(true)
	t.Cleanup(func() { _ = e.Close() })
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		for range 500 {
			writeFixture(t, e, []byte("retained 日本語\r\n"))
		}
	}()
	go func() {
		defer workers.Done()
		for i := range 500 {
			if i%2 == 0 {
				e.Resize(1+i%80, 1+i%15)
			} else {
				e.ResizeWithAnchor(1+i%80, 1+i%15, uv.Pos(0, 0))
			}
		}
	}()
	go func() {
		defer workers.Done()
		for range 500 {
			lines := e.LogicalLines()
			if len(lines) > 0 && len(lines[0]) > 0 {
				lines[0][0].Content = "not-live-state"
			}
		}
	}()
	workers.Wait()
	writeFixture(t, e, []byte("final-sentinel\r\n"))
	lines := e.LogicalLines()
	var text strings.Builder
	for _, line := range lines {
		text.WriteString(line.String())
	}
	if !strings.HasSuffix(text.String(), "final-sentinel") || strings.Contains(text.String(), "not-live-state") {
		t.Fatal("concurrent projection/resize changed newest output or exposed model aliases")
	}
}

func TestRetainedPrimaryPendingWrapSurvivesAlternateScreenResize(t *testing.T) {
	e := retainedFixture(t, 20, 4)
	writeFixture(t, e, []byte("abcdefghijklmnopqrst\x1b[?1049happlication-canvas"))
	e.Resize(40, 6)
	if got := retainedText(e); got != "abcdefghijklmnopqrst" {
		t.Fatalf("alternate canvas replaced primary retained text: %q", got)
	}
	writeFixture(t, e, []byte("\x1b[?1049lx"))
	if got := retainedText(e); got != "abcdefghijklmnopqrstx" {
		t.Fatalf("alternate resize lost primary pending wrap: %q", got)
	}
}

func TestRetainedSavedPendingWrapSurvivesResizeAndRestore(t *testing.T) {
	for _, controls := range [][2]string{{"\x1b7", "\x1b8"}, {"\x1b[?1048h", "\x1b[?1048l"}, {"\x1b[s", "\x1b8"}} {
		for _, width := range []int{10, 40} {
			e := retainedFixture(t, 20, 6)
			writeFixture(t, e, []byte("abcdefghijklmnopqrst"+controls[0]+"\rXX"))
			e.Resize(width, 6)
			writeFixture(t, e, []byte(controls[1]+"x"))
			if got := retainedText(e); got != "XXcdefghijklmnopqrstx" {
				t.Fatalf("saved pending wrap %q at width %d restored the wrong insertion point: %q", controls, width, got)
			}
		}
	}
}

func TestRetainedEraseScreenKeepsInteriorBlankLines(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	writeFixture(t, e, []byte("top\r\n\r\nbottom\x1b[2J"))
	if got := retainedText(e); !strings.HasPrefix(got, "top\n\nbottom\n") {
		t.Fatalf("erase-screen scrollback collapsed an actual blank line: %q", got)
	}
}

func TestRetainedResizePreservesSavedCursorBeyondWrittenText(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	writeFixture(t, e, []byte("\x1b[1;15H\x1b7\x1b[1;1Habc"))
	e.Resize(10, 5)
	writeFixture(t, e, []byte("\x1b8X"))
	if got := retainedText(e); got != "abc           X" {
		t.Fatalf("reflow discarded the saved insertion column: %q", got)
	}
}

func TestRetainedReplacingEdgeCellWithWideClusterKeepsLogicalText(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	prefix := strings.Repeat("x", 19)
	writeFixture(t, e, []byte(prefix+"✈"))
	e.SetCell(19, 0, nil)
	writeFixture(t, e, []byte("\x1b[1;20H✈️ done"))
	if got := retainedText(e); got != prefix+"✈️ done" {
		t.Fatalf("replacing the edge cell changed semantic spacing: %q", got)
	}
}

func TestRetainedResizeMapsAnExplicitCellAnchor(t *testing.T) {
	e := retainedFixture(t, 20, 5)
	writeFixture(t, e, []byte("same same same"))
	before := e.CellAt(10, 0).Clone()
	mapped, ok := e.ResizeWithAnchor(4, 5, uv.Pos(10, 0))
	if !ok || mapped != uv.Pos(2, 2) {
		t.Fatalf("resize mapped the wrong repeated cell: %+v %v", mapped, ok)
	}
	if cell := e.CellAt(mapped.X, mapped.Y); cell == nil || !cell.Equal(before) {
		t.Fatal("mapped cell no longer has its original content and style")
	}
}

func TestRetainedResizeAnchorDoesNotGuessOutsideItsOwnedVisibleCanvas(t *testing.T) {
	t.Run("hidden tail", func(t *testing.T) {
		e := retainedFixture(t, 20, 5)
		writeFixture(t, e, []byte("top\r\nmiddle\r\nbottom\x1b[1;1H"))
		if _, ok := e.ResizeWithAnchor(8, 1, uv.Pos(0, 2)); ok {
			t.Fatal("hidden tail was returned as a visible cell")
		}
		if retainedText(e) != "top\nmiddle\nbottom" {
			t.Fatal("rejecting an invisible anchor discarded its stored text")
		}
	})
	t.Run("alternate screen", func(t *testing.T) {
		e := retainedFixture(t, 20, 5)
		writeFixture(t, e, []byte("retained\x1b[?1049happlication"))
		if _, ok := e.ResizeWithAnchor(8, 2, uv.Pos(0, 0)); ok {
			t.Fatal("application canvas supplied a retained-output anchor")
		}
		if e.Width() != 8 || e.Height() != 2 || retainedText(e) != "retained" {
			t.Fatal("alternate-screen resize changed primary retained text")
		}
	})
	t.Run("default canvas", func(t *testing.T) {
		e := NewEmulator(20, 5)
		t.Cleanup(func() { _ = e.Close() })
		writeFixture(t, e, []byte("ordinary canvas"))
		if _, ok := e.ResizeWithAnchor(8, 2, uv.Pos(0, 0)); ok {
			t.Fatal("default canvas opted into retained reflow implicitly")
		}
		if e.Width() != 8 || e.Height() != 2 || e.CellAt(8, 0) != nil {
			t.Fatal("default canvas stopped clipping to its normal geometry")
		}
	})
}

func TestRetainedSafeScrollbackCellReadersSerializeAliasTracking(t *testing.T) {
	e := NewSafeEmulator(20, 2)
	e.SetReflow(true)
	t.Cleanup(func() { _ = e.Close() })
	writeFixture(t, e, []byte("first\r\nsecond\r\nthird\r\n"))
	if e.ScrollbackLen() == 0 {
		t.Fatal("fixture did not reach scrollback")
	}
	start := make(chan struct{})
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for range 2000 {
				if e.ScrollbackCellAt(0, 0) == nil {
					t.Error("safe read lost a retained cell")
					return
				}
			}
		}()
	}
	close(start)
	readers.Wait()
	// Public cells remain aliases; serialization must not replace the API with
	// a detached copy. Callers still own synchronization of later alias edits.
	e.ScrollbackCellAt(0, 0).Content = "F"
	if got := e.LogicalLines()[0].String(); got != "First" {
		t.Fatalf("safe lookup stopped exposing its documented mutable cell: %q", got)
	}
}
