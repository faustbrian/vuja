package root

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPlainOutputDefaultMouseScrollsWholePane(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	var body strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "plain-row-%02d café 日本語\r\n", i)
	}
	blocksExecution(c, "emit-lines", body.String(), "0")
	blocksPrompt(c, "pending-input")
	if !strings.Contains(out.String(), "\x1b[?1002h\x1b[?1006h") {
		t.Fatal("plain output did not request wheel and drag reports with default mouse settings")
	}
	initial := append([]byte(nil), out.Bytes()...)
	before := applyTerminalOutput(t, initial, 60, 24)
	inputRow := terminalLineIndex(terminalScreenLines(before), "pending-input", 0)
	if inputRow < 0 {
		t.Fatal("fixture input is not visible")
	}
	out.Reset()
	var filter terminalInputFilter
	if got := filter.FilterInput([]byte("\x1b[<64;2;2M"), c, false, true); len(got) != 0 {
		t.Fatalf("wheel reached shell: %q", got)
	}
	up := applyTerminalOutput(t, append(initial, out.Bytes()...), 60, 24)
	if screenLine(before, 0) == screenLine(up, 0) || c.viewport.frozen == nil {
		t.Fatal("wheel did not reveal earlier whole-pane output")
	}
	if !strings.Contains(screenLine(up, inputRow), "pending-input") {
		t.Fatal("scrolling moved or erased the pinned input")
	}
	if got := filter.FilterInput([]byte("\x1b[<65;2;2M"), c, false, true); len(got) != 0 {
		t.Fatalf("downward wheel reached shell: %q", got)
	}
	if c.viewport.frozen != nil {
		t.Fatal("downward wheel did not return to live output")
	}
}

func TestPlainOutputSelectionCopiesWrappedUnicode(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	blocksExecution(c, "wrapped", strings.Repeat("x", 58)+"café 日本語\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, strings.Repeat("x", 58), 0)
	if y < 0 || y+1 >= len(lines) {
		t.Fatalf("wrapped fixture missing: %q", lines)
	}
	for range 2 {
		out.Reset()
		cardMouse(t, c, 0, 58, y, true)
		cardMouse(t, c, 32, 8, y+1, true)
		cardMouse(t, c, 0, 8, y+1, false)
		assertExecutionClipboard(t, out.String(), "café 日本語")
	}
}

func TestPlainOutputReturnRestoresDragReporting(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	blocksPrompt(c, "pending-input")
	for i := 0; i < 40; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("row-%02d\r\n", i)))
	}
	cardMouse(t, c, 64, 2, 2, true)
	out.Reset()
	var filter terminalInputFilter
	filter.FilterInput([]byte("\x1b[F"), c, false, true)
	if strings.LastIndex(out.String(), "\x1b[?1002h") < strings.LastIndex(out.String(), "\x1b[?1003l") {
		t.Fatal("return to live output did not restore button-motion drag reports")
	}
}

func TestPlainOutputSelectionSeparatesPaneFromInput(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	c.SetInputBoxPath("/fixture")
	blocksPrompt(c, "running-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte(strings.Repeat("x", 4000)))
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	rows := 24 - c.surfaceRows
	x := strings.Index(lines[rows], "/fixture")
	if x < 0 {
		t.Fatalf("input title fixture missing: %q", lines)
	}
	want := lines[rows-1] + "\n" + lines[rows][:x+len("/fixture")]
	out.Reset()
	cardMouse(t, c, 0, 0, rows-1, true)
	cardMouse(t, c, 32, x+len("/fixture")-1, rows, true)
	cardMouse(t, c, 0, x+len("/fixture")-1, rows, false)
	assertExecutionClipboard(t, out.String(), want)
}

func TestPlainOutputScrollClampsAndKeepsReadingAnchor(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	var body strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "plain-row-%02d\r\n", i)
	}
	blocksExecution(c, "emit-lines", body.String(), "0")
	blocksPrompt(c, "pending-input")
	for range 30 {
		cardMouse(t, c, 64, 2, 2, true)
	}
	first := screenLine(applyTerminalOutput(t, out.Bytes(), 60, 24), 0)
	if first != "plain-row-00" {
		t.Fatalf("top clamp lost first row: %q", first)
	}
	c.WriteNotification([]byte("new-output\r\n"))
	if got := screenLine(applyTerminalOutput(t, out.Bytes(), 60, 24), 0); got != first {
		t.Fatalf("new output moved reading anchor: %q", got)
	}
	cardMouse(t, c, 65, 2, 2, true)
	if got := screenLine(applyTerminalOutput(t, out.Bytes(), 60, 24), 0); got != "plain-row-03" {
		t.Fatalf("top overscroll swallowed reverse wheel: %q", got)
	}
	for range 30 {
		cardMouse(t, c, 65, 2, 2, true)
	}
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "new-output") {
		t.Fatal("bottom clamp did not reveal new live output")
	}
	cardMouse(t, c, 64, 2, 2, true)
	if c.viewport.frozen == nil {
		t.Fatal("bottom overscroll swallowed reverse wheel")
	}
}

func TestPlainOutputDragCopySurvivesScrolling(t *testing.T) {
	for _, automatic := range []bool{true, false} {
		for _, browse := range []bool{false, true} {
			t.Run(fmt.Sprintf("automatic=%t/browse=%t", automatic, browse), func(t *testing.T) {
				c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\nscrollback='output'\ncopy-on-select=%t\n", automatic))
				var body strings.Builder
				for i := 0; i < 40; i++ {
					fmt.Fprintf(&body, "row-%02d selected café 日本語\r\n", i)
				}
				blocksExecution(c, "emit-lines", body.String(), "0")
				blocksPrompt(c, "pending-input")
				if browse {
					cardMouse(t, c, 64, 2, 2, true)
				}
				lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
				y := terminalLineIndex(lines, "selected", 0)
				if y < 0 {
					t.Fatalf("selection fixture is not visible: %q", lines)
				}
				x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "selected")])
				out.Reset()
				cardMouse(t, c, 0, x, y, true)
				cardMouse(t, c, 32, x+7, y, true)
				cardMouse(t, c, 0, x+7, y, false)
				if automatic {
					assertExecutionClipboard(t, out.String(), "selected")
				} else if strings.Contains(out.String(), "\x1b]52;") {
					t.Fatal("manual-copy mode copied on release")
				}
				out.Reset()
				var filter terminalInputFilter
				got := filter.FilterInput([]byte("\x03next"), c, false, true)
				if automatic {
					if string(got) != "\x03next" || strings.Contains(out.String(), "\x1b]52;") {
						t.Fatal("automatic copy consumed interrupt or copied again")
					}
				} else {
					if string(got) != "next" {
						t.Fatalf("manual copy changed adjacent input: %q", got)
					}
					assertExecutionClipboard(t, out.String(), "selected")
				}
			})
		}
	}
}

func TestPlainOutputOverlayKeyboardAndFramingKeepOwnership(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	blocksPrompt(c, "pending-input")
	for i := 0; i < 40; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("row-%02d selected\r\n", i)))
	}
	visible := false
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	out.Reset()
	c.ComposeUI(func() []byte { visible = true; return []byte("\x1b7\x1b8") })
	if !strings.Contains(out.String(), "\x1b[?1002l") {
		t.Fatal("overlay did not release pointer ownership")
	}
	var filter terminalInputFilter
	const wheel = "\x1b[<64;2;2M"
	if got := filter.FilterInput([]byte(wheel), c, true, true); len(got) != 0 || c.viewport.frozen != nil {
		t.Fatal("suggestions did not retain wheel ownership")
	}
	out.Reset()
	c.ComposeUI(func() []byte { visible = false; return []byte("\x1b7\x1b8") })
	if !strings.Contains(out.String(), "\x1b[?1002h") {
		t.Fatal("overlay close did not restore drag/wheel reporting")
	}
	if got := filter.FilterInput([]byte("\x1b[5~"), c, false, true); len(got) != 0 || c.viewport.frozen == nil {
		t.Fatal("plain Page Up was diverted to hidden cards")
	}
	if got := filter.FilterInput([]byte("\x1b[F"), c, false, true); len(got) != 0 || c.viewport.frozen != nil {
		t.Fatal("End did not return to live pane")
	}
	for split := 0; split <= len(wheel); split++ {
		var fragmented terminalInputFilter
		got := fragmented.FilterInput([]byte("x"+wheel[:split]), c, false, true)
		got = append(got, fragmented.FilterInput([]byte(wheel[split:]+"y"), c, false, true)...)
		if string(got) != "xy" || c.viewport.frozen == nil {
			t.Fatalf("split %d changed input/scroll: %q", split, got)
		}
		fragmented.FilterInput([]byte("\x1b[F"), c, false, true)
	}
	paste := string(bracketedPasteSequence(wheel))
	if got := filter.FilterInput([]byte(paste), c, false, true); string(got) != paste || c.viewport.frozen != nil {
		t.Fatal("pasted mouse bytes acquired pane ownership")
	}
	if got := filter.FilterInput([]byte("\x1b[<64;2;24M"), c, false, true); len(got) != 0 || c.viewport.frozen != nil {
		t.Fatal("wheel over fixed chrome scrolled output")
	}
	cardMouse(t, c, 64, 2, 2, true)
	_, left, _ := c.viewportBadge()
	cardMouse(t, c, 35, left+2, c.viewportBadgeRow()-1, true)
	if !c.viewportBadgeHovered() {
		t.Fatal("plain browse badge did not receive hover")
	}
	cardMouse(t, c, 0, left+2, c.viewportBadgeRow()-1, true)
	if c.viewport.frozen != nil {
		t.Fatal("browse badge did not return to live pane")
	}
	if !strings.Contains(out.String(), "\x1b[?1002h") {
		t.Fatal("return from motion tracking lost button-motion reports")
	}
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "selected", 0)
	if y < 0 {
		t.Fatal("overlay/browse return erased selectable output")
	}
	x := strings.Index(lines[y], "selected")
	out.Reset()
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 32, x+7, y, true)
	cardMouse(t, c, 0, x+7, y, false)
	assertExecutionClipboard(t, out.String(), "selected")
}

func TestPlainOutputWheelDuringCommandKeepsPinnedInput(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	blocksPrompt(c, "running-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	for i := 0; i < 40; i++ {
		c.WritePTY([]byte(fmt.Sprintf("running-row-%02d\r\n", i)))
	}
	before := applyTerminalOutput(t, out.Bytes(), 60, 24)
	y := terminalLineIndex(terminalScreenLines(before), "running-command", 0)
	if y < 0 {
		t.Fatal("running command is not visible")
	}
	cardMouse(t, c, 64, 2, 2, true)
	after := applyTerminalOutput(t, out.Bytes(), 60, 24)
	if screenLine(before, 0) == screenLine(after, 0) || !strings.Contains(screenLine(after, y), "running-command") {
		t.Fatal("busy wheel did not scroll output independently of pinned command")
	}
}

func TestPlainOutputMousePreferencesAndNativeHandoff(t *testing.T) {
	for _, mode := range []string{"select", "navigate", "interact"} {
		t.Run(mode, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\noutput-mouse='"+mode+"'\n")
			blocksExecution(c, "command", "selected\r\n", "0")
			blocksPrompt(c, "pending-input")
			if mode == "select" {
				for _, tracking := range []string{"1000", "1002", "1003", "1006"} {
					if strings.Contains(out.String(), "\x1b[?"+tracking+"h") {
						t.Fatalf("select mode acquired tracking %s", tracking)
					}
				}
			} else if mode == "navigate" && !strings.Contains(out.String(), "\x1b[?1000h") {
				t.Fatal("navigate lost its normal wheel reports")
			}
			c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
			c.WritePTY([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006happ"))
			out.Reset()
			const native = "\x1b[<64;2;2Mtyped"
			var filter terminalInputFilter
			if got := filter.FilterInput([]byte(native), c, false, false); string(got) != native || out.Len() != 0 {
				t.Fatalf("native app lost input or received managed repaint: %q", got)
			}
			c.WritePTY([]byte("\x1b[?1049l"))
			c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
			blocksPrompt(c, "next-input")
			if mode == "interact" && !strings.Contains(out.String(), "\x1b[?1002h\x1b[?1006h") {
				t.Fatal("native exit did not restore plain-pane pointer ownership")
			}
		})
	}
}

func TestPlainOutputOverlayCloseImmediatelyRestoresSelection(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	blocksExecution(c, "command", "selected\r\n", "0")
	blocksPrompt(c, "pending-input")
	y := terminalLineIndex(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "selected", 0)
	if y < 0 {
		t.Fatal("selection fixture missing")
	}
	visible := false
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	c.ComposeUI(func() []byte { visible = true; return []byte("\x1b7\x1b8") })
	c.ComposeUI(func() []byte { visible = false; return []byte("\x1b7\x1b8") })
	out.Reset()
	cardMouse(t, c, 0, 0, y, true)
	cardMouse(t, c, 32, 7, y, true)
	cardMouse(t, c, 0, 7, y, false)
	assertExecutionClipboard(t, out.String(), "selected")
}

func TestPlainOutputWheelOverChromePreservesManualSelection(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\ncopy-on-select=false\n")
	blocksExecution(c, "command", "selected\r\n", "0")
	blocksPrompt(c, "pending-input")
	y := terminalLineIndex(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "selected", 0)
	if y < 0 {
		t.Fatal("selection fixture missing")
	}
	cardMouse(t, c, 0, 0, y, true)
	cardMouse(t, c, 32, 7, y, true)
	cardMouse(t, c, 0, 7, y, false)
	out.Reset()
	cardMouse(t, c, 64, 2, 23, true)
	var filter terminalInputFilter
	if got := filter.FilterInput([]byte{3}, c, false, true); len(got) != 0 {
		t.Fatal("chrome wheel discarded selection and turned copy into an interrupt")
	}
	assertExecutionClipboard(t, out.String(), "selected")
}

func TestPlainOutputSelectionPreservesClearedCanvas(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
	var body strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "old-row-%02d\r\n", i)
	}
	blocksExecution(c, "old", body.String(), "0")
	blocksExecution(c, "clear-only", "\x1b[H\x1b[2J", "0")
	blocksExecution(c, "short", "selected\r\n", "0")
	blocksPrompt(c, "pending-input")
	before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if before[0] != "selected" {
		t.Fatalf("clear fixture did not establish visible canvas: %q", before)
	}
	cardMouse(t, c, 0, 0, 0, true)
	after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if after[0] != before[0] {
		base, rows := c.viewport.model.LogicalRowPositions()
		t.Fatalf("press moved cleared output: before=%q after=%q model-base=%d rows=%v frozen-base=%d top=%d frame=%+v", before[0], after[0], base, rows, c.viewport.frozenBase, c.viewport.top, c.viewport.cards.selection.frame[0])
	}
	cardMouse(t, c, 32, 7, 0, true)
	out.Reset()
	cardMouse(t, c, 0, 7, 0, false)
	assertExecutionClipboard(t, out.String(), "selected")
}

func TestPlainOutputSelectionPreservesSoftWrappedCanvas(t *testing.T) {
	for _, width := range []int{60, 32} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
			blocksPrompt(c, "running-command")
			c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
			var body strings.Builder
			for i := 0; i < 1000; i++ {
				fmt.Fprintf(&body, "%03d|", i)
			}
			c.WritePTY([]byte(body.String()))
			if width != 60 {
				c.Resize(width, 24)
			}
			before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 24))
			want := before[0][:8]
			cardMouse(t, c, 0, 0, 0, true)
			c.WritePTY([]byte("new-tail"))
			after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 24))
			for y := 0; y < 24-c.surfaceRows; y++ {
				if before[y] != after[y] {
					t.Fatalf("selection moved soft-wrapped row %d: %q -> %q", y, before[y], after[y])
				}
			}
			cardMouse(t, c, 32, 7, 0, true)
			out.Reset()
			cardMouse(t, c, 0, 7, 0, false)
			assertExecutionClipboard(t, out.String(), want)
		})
	}
}
