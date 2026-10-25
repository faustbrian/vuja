package vt

import (
	"strings"
	"testing"
)

func TestRetainedClearKeepsHistoryOutsideResizedCanvas(t *testing.T) {
	e := NewEmulator(20, 6)
	defer e.Close()
	e.SetReflow(true)
	writeFixture(t, e, []byte("old-history\r\n\x1b[H\x1b[2Jfresh"))
	for _, size := range [][2]int{{15, 10}, {10, 3}, {30, 12}} {
		e.Resize(size[0], size[1])
		if strings.Contains(e.String(), "old-history") || !strings.Contains(e.String(), "fresh") {
			t.Fatal("resize exposed erased history or lost post-clear output")
		}
	}
	if !strings.Contains(retainedText(e), "old-history") {
		t.Fatal("ED2 discarded explicitly browsable history")
	}
	e.SetScrollbackSize(2)
	for i := 0; i < 30; i++ {
		writeFixture(t, e, []byte("\r\nnew-output"))
	}
	e.Resize(40, 20)
	if !strings.Contains(e.String(), "new-output") {
		t.Fatal("evicted clear boundary hid subsequent output")
	}
}

func TestRetainedClearBoundarySurvivesWrappedHistoryAndLaterClears(t *testing.T) {
	e := NewEmulator(10, 4)
	defer e.Close()
	e.SetReflow(true)
	e.SetScrollbackSize(3)
	for i := 0; i < 5; i++ {
		writeFixture(t, e, []byte(strings.Repeat("prior-text-", 8)+"\r\n"))
		writeFixture(t, e, []byte("\x1b[H\x1b[2Jfresh"))
		for _, width := range []int{5, 30, 10} {
			e.Resize(width, 9)
			if strings.Contains(e.String(), "prior") || !strings.Contains(e.String(), "fresh") {
				t.Fatal("wrapped/evicted history crossed the clear boundary")
			}
		}
	}
	writeFixture(t, e, []byte("\x1b[H\x1b[3Jlast"))
	e.Resize(40, 12)
	if strings.Contains(retainedText(e), "prior") || !strings.Contains(e.String(), "last") {
		t.Fatal("ED3 failed to clear history and retire the old boundary")
	}
}
