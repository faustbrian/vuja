package root

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/faustbrian/vuja/internal/vt"
)

func TestOutputToolsSearchOwnsQueryAndKeepsShellInput(t *testing.T) {
	c, out, input := outputToolsFixture(t, "first needle\r\nother output\r\nsecond needle\r\n")
	if forwarded := input.FilterInput([]byte("\x1b[18~"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("F7 must open output search instead of reaching the shell: %q", forwarded)
	}
	if forwarded := input.FilterInput([]byte("needle"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("output search query leaked into shell input: %q", forwarded)
	}
	if forwarded := input.FilterInput([]byte("\r"), c, false, true); len(forwarded) != 0 {
		t.Fatal("search Enter must not execute shell input")
	}
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !terminalContainsLine(screen, "· needle ·") || !terminalContainsLine(screen, "Find: 2/2") {
		t.Fatalf("search must disclose its query and current match position: %q", terminalScreenLines(screen))
	}
	if !terminalContainsLine(screen, "keep this input") {
		t.Fatal("output search changed the fixed shell input")
	}
	if strings.Contains(out.String(), "\x1b[?1000h") || strings.Contains(out.String(), "\x1b[?1003h") {
		t.Fatal("output search took ordinary native drag selection")
	}
}

func TestOutputToolsSearchMalformedSequencesStayModal(t *testing.T) {
	for _, opener := range []string{"\x1b[", "\x1b]"} {
		for _, length := range []int{terminalModelSequenceLimit - 3, terminalModelSequenceLimit, terminalModelSequenceLimit + 1} {
			for _, split := range []int{0, 1, 2, length / 2, length} {
				t.Run(fmt.Sprintf("%q-%d-%d", opener, length, split), func(t *testing.T) {
					c, out, input := outputToolsFixture(t, "needle\r\n")
					input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
					payload := []byte(opener + strings.Repeat("1", length) + "\r")
					for _, chunk := range [][]byte{payload[:split], payload[split:]} {
						if got := input.FilterEvents(chunk, c, false, true); len(got) != 0 {
							t.Fatalf("malformed modal input reached the shell: %d bytes", len(flattenTerminalInput(got)))
						}
						if len(input.pending) > terminalModelSequenceLimit {
							t.Fatal("malformed modal input exceeded its pending bound")
						}
					}
					if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "keep this input") {
						t.Fatal("malformed modal input changed the shell input surface")
					}
				})
			}
		}
	}
}

func TestOutputToolsMalformedNativeInputRetainsItsOwner(t *testing.T) {
	c, _, input := outputToolsFixture(t, "")
	c.viewportPassthrough = true
	payload := []byte("\x1b[" + strings.Repeat("1", terminalModelSequenceLimit+1) + "\r")
	events := input.FilterEvents(payload, c, false, false)
	if len(events) != 1 || events[0].shell || !bytes.Equal(flattenTerminalInput(events), payload) {
		t.Fatal("malformed application input lost its original owner or bytes")
	}
}

func TestOutputToolsMalformedShellInputRetainsItsOwner(t *testing.T) {
	c, _, input := outputToolsFixture(t, "")
	payload := []byte("\x1b[" + strings.Repeat("1", terminalModelSequenceLimit+1) + "\r")
	events := input.FilterEvents(payload, c, false, true)
	if len(events) != 1 || !events[0].shell || !bytes.Equal(flattenTerminalInput(events), payload) {
		t.Fatal("ordinary malformed shell input lost its owner or bytes")
	}
}

func TestOutputToolsIncompleteModalSequenceKeepsItsOriginalOwner(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(fmt.Sprintf("F7-together-%v", together), func(t *testing.T) {
			c, _, input := outputToolsFixture(t, "needle\r\n")
			prefix := []byte("\x1b[" + strings.Repeat("1", 100))
			if together {
				prefix = append([]byte("\x1b[18~"), prefix...)
			} else {
				input.FilterEvents([]byte("\x1b[18~"), c, false, true)
			}
			if got := input.FilterEvents(prefix, c, false, true); len(got) != 0 {
				t.Fatal("incomplete search control leaked before handoff")
			}
			c.Resize(80, 1) // the modal loses its visible surface
			for _, chunk := range [][]byte{append(bytes.Repeat([]byte("1"), terminalModelSequenceLimit), '\r')} {
				if got := input.FilterEvents(chunk, c, false, false); len(got) != 0 {
					t.Fatal("pending search control leaked after ownership handoff")
				}
			}
			if got := input.FilterInput([]byte("native input"), c, false, false); string(got) != "native input" {
				t.Fatal("new native input was confused with the old modal control")
			}
		})
	}
}

func TestOutputToolsSearchNavigationAndEditingTrace(t *testing.T) {
	labels := []string{"zero needle", "one  needle", "two  needle"}
	c, out, input := outputToolsFixture(t, strings.Join(labels, "\r\n")+"\r\n")
	check := func(key string, row, index int) {
		t.Helper()
		if got := input.FilterInput([]byte(key), c, false, true); len(got) > 0 {
			t.Fatalf("modal key reached shell: %q", got)
		}
		screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
		if !terminalContainsLine(screen, fmt.Sprintf("Find: %d/3", index+1)) {
			t.Fatalf("wrong match position after %q", key)
		}
		selectedVisible := false
		for y := 0; y < c.height-c.surfaceRows-1; y++ {
			for source, label := range labels {
				if !strings.Contains(screenLine(screen, y), label) {
					continue
				}
				selectedVisible = selectedVisible || source == row
				for x := 5; x < 11; x++ {
					cell := screen.CellAt(x, y)
					if cell == nil || (cell.Style.Attrs&uv.AttrReverse != 0) != (source == row) {
						t.Fatalf("wrong highlighted match at (%d,%d) after %q: %#v", x, y, key, cell)
					}
				}
			}
		}
		if !selectedVisible {
			t.Fatalf("navigation did not reveal %q after %q", labels[row], key)
		}
	}
	check("\x1b[18~needle", 0, 0)
	check("\x1b[A", 2, 2)   // backward wrap
	check("\x1b[19~", 0, 0) // F8 forward wrap
	check("\x1b[B", 1, 1)
	check("\x1b[19;2~", 0, 0) // Shift+F8
	input.FilterInput([]byte("x"), c, false, true)
	if len(c.viewport.search.matches) != 0 || c.viewport.search.index != -1 {
		t.Fatal("editing retained stale matches")
	}
	for _, key := range []string{"\r", "\x1b[A"} {
		input.FilterInput([]byte(key), c, false, true)
	}
	check("\x7f", 0, 0)
	input.FilterInput([]byte("\x15\r\x1b[A"), c, false, true)
	if len(c.viewport.search.query) != 0 || len(c.viewport.search.matches) != 0 || c.viewport.search.index != -1 {
		t.Fatal("cleared query retained a stale match or selection")
	}
	check("needle", 0, 0)
	top, rows := c.viewport.top, append([]string(nil), c.viewport.frozen...)
	input.FilterInput([]byte("\x03"), c, false, true)
	if c.viewport.top != top || strings.Join(c.viewport.frozen, "\n") != strings.Join(rows, "\n") {
		t.Fatal("closing search changed the frozen reading position")
	}
}

func TestOutputToolsSearchMixedQueryEnterIsIndependentOfReadBoundaries(t *testing.T) {
	text := []byte("needle\r")
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "first needle\r\nsecond needle\r\n")
			input.FilterInput([]byte("\x1b[18~"), c, false, true)
			for _, chunk := range [][]byte{text[:split], text[split:]} {
				if forwarded := input.FilterInput(chunk, c, false, true); len(forwarded) != 0 {
					t.Fatalf("search input leaked at split %d: %q", split, forwarded)
				}
			}
			screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
			if !terminalContainsLine(screen, "2/2") {
				t.Fatalf("mixed query/Enter changed match at split %d: %q", split, terminalScreenLines(screen))
			}
		})
	}
}

func TestOutputToolsSearchPasteNeverReachesTheShell(t *testing.T) {
	paste := append(append(append([]byte{}, bracketedPasteStart...), []byte("needle")...), bracketedPasteEnd...)
	for split := 0; split <= len(paste); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "first needle\r\nsecond needle\r\n")
			input.FilterInput([]byte("\x1b[18~"), c, false, true)
			for _, chunk := range [][]byte{paste[:split], paste[split:]} {
				if forwarded := input.FilterInput(chunk, c, false, true); len(forwarded) != 0 {
					t.Fatalf("search paste leaked at split %d: %q", split, forwarded)
				}
			}
			input.FilterInput([]byte("\r"), c, false, true)
			screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
			if !terminalContainsLine(screen, "· needle ·") || !terminalContainsLine(screen, "Find: 2/2") {
				t.Fatalf("paste did not become a literal search query at split %d: %q", split, terminalScreenLines(screen))
			}
		})
	}
}

func TestOutputToolsSearchBoundsRepeatedMatches(t *testing.T) {
	c, out, input := outputToolsFixture(t, strings.Repeat(strings.Repeat("a", 75)+"\r\n", 70))
	input.FilterInput([]byte("\x1b[18~a"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !terminalContainsLine(screen, "1/4096+") || len(c.viewport.search.matches) > 4096 {
		t.Fatalf("search must bound matches and disclose incomplete results, got count %d, screen %q", len(c.viewport.search.matches), terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchBoundsQueryAndIncompleteUTF8Retention(t *testing.T) {
	c, _, input := outputToolsFixture(t, "output\r\n")
	input.FilterInput([]byte("\x1b[18~"), c, false, true)
	input.FilterInput(bracketedPasteStart, c, false, true)
	payload := append(bytes.Repeat([]byte("x"), 100000), 0xf0)
	if got := input.FilterInput(payload, c, false, true); len(got) > 0 {
		t.Fatal("oversized search paste leaked into shell input")
	}
	if s := c.viewport.search; len(s.query) > 256 || len(s.pending) != 1 || cap(s.pending) >= utf8.UTFMax {
		t.Fatalf("search retained oversized query/payload: query %d, pending %d/%d", len(s.query), len(s.pending), cap(s.pending))
	}
}

func TestOutputToolsSearchUnicodeAndFrozenOutput(t *testing.T) {
	c, out, input := outputToolsFixture(t, "before 界😊 after\r\n")
	input.FilterInput([]byte("\x1b[18~"), c, false, true)
	for _, value := range []byte("界😊") {
		if got := input.FilterInput([]byte{value}, c, false, true); len(got) > 0 {
			t.Fatal("fragmented Unicode search leaked")
		}
	}
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !terminalContainsLine(screen, "· 界😊 ·") || !terminalContainsLine(screen, "Find: 1/1") {
		t.Fatalf("fragmented Unicode query was corrupted: %q", terminalScreenLines(screen))
	}
	reading := screenLine(screen, 0)
	oldTop := c.viewport.top
	c.WritePTY([]byte("new 界😊 output\r\n"))
	if c.viewport.top != oldTop || screenLine(applyTerminalOutput(t, out.Bytes(), 80, 14), 0) != reading || len(c.viewport.search.matches) != 1 {
		t.Fatal("incoming output moved the searched reading position or changed its frozen matches")
	}
}

func TestOutputToolsRetainsWideGraphemesInLiveAndFrozenOutput(t *testing.T) {
	c, out, input := outputToolsFixture(t, "wide 界😊 e\u0301 👩‍💻 done\r\n")
	check := func() {
		t.Helper()
		screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
		if !terminalContainsLine(screen, "wide 界😊 e\u0301 👩‍💻 done") {
			t.Fatalf("retained output lost complete graphemes: %q", terminalScreenLines(screen))
		}
	}
	check()
	input.FilterInput([]byte("\x1b[5~"), c, false, true)
	check()
}

func TestOutputToolsRestoresWideGraphemesAfterAnOverlay(t *testing.T) {
	c, out, _ := outputToolsFixture(t, "wide 界😊 e\u0301 👩‍💻 done\r\n")
	out.WriteString("\x1b[1;1H\x1b[2Koverlay")
	out.Write(c.restoreBackdropRows(0, 1))
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !terminalContainsLine(screen, "wide 界😊 e\u0301 👩‍💻 done") {
		t.Fatalf("overlay restoration lost wide graphemes: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsRetainsUnicodeAcrossEveryReadBoundary(t *testing.T) {
	text := []byte("wide 界😊 e\u0301 👩‍💻 👍🏽 done\r\n")
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY(text[:split])
			c.WritePTY(text[split:])
			for _, frozen := range []bool{false, true} {
				if frozen {
					input.FilterInput([]byte("\x1b[5~"), c, false, true)
				}
				screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
				if !terminalContainsLine(screen, "wide 界😊 e\u0301 👩‍💻 👍🏽 done") {
					t.Fatalf("Unicode changed at read split %d (frozen %v): %q", split, frozen, terminalScreenLines(screen))
				}
			}
		})
	}
}

func TestOutputToolsBoundsOversizedGraphemesAndDisclosesLoss(t *testing.T) {
	for _, chunk := range []int{1, 2, 31, 512, 32768} {
		t.Run(fmt.Sprintf("chunk-%d", chunk), func(t *testing.T) {
			model := vt.NewEmulator(80, 4)
			t.Cleanup(func() { _ = model.Close() })
			var stream outputModelStream
			payload := []byte("before a" + strings.Repeat("\u0301", 4096) + " after")
			for len(payload) > 0 {
				n := min(chunk, len(payload))
				stream.write(model, payload[:n])
				payload = payload[n:]
			}
			for y := 0; y < model.Height(); y++ {
				for x := 0; x < model.Width(); x++ {
					if cell := model.CellAt(x, y); cell != nil && len(cell.Content) > 512 {
						t.Fatalf("one output cell retained %d bytes from an oversized grapheme", len(cell.Content))
					}
				}
			}
			if got := screenLine(model, 0); got != "before [Unicode cluster truncated] after" {
				t.Fatalf("oversized cluster loss was not disclosed or subsequent text was changed: %q", got)
			}
		})
	}
}

func TestOutputToolsOversizedGraphemeDisclosureIsRetainedAndSearchable(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte("before a" + strings.Repeat("\u0301", 4096) + " after\r\n"))
	input.FilterInput([]byte("\x1b[18~Unicode cluster truncated"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !terminalContainsLine(screen, "before [Unicode cluster truncated] after") || !terminalContainsLine(screen, "Find: 1/1") {
		t.Fatalf("frozen output lost its truncation disclosure: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsGraphemeLimitPreservesOrdinaryBoundaryText(t *testing.T) {
	for _, marks := range []int{0, 1, 255, 256} {
		t.Run(fmt.Sprintf("marks-%d", marks), func(t *testing.T) {
			model := vt.NewEmulator(80, 4)
			t.Cleanup(func() { _ = model.Close() })
			var stream outputModelStream
			text := "a" + strings.Repeat("\u0301", marks)
			for _, value := range []byte(text) {
				stream.write(model, []byte{value})
			}
			if marks <= 255 {
				if cell := model.CellAt(0, 0); cell == nil || cell.Content != text || cell.Width != 1 {
					t.Fatal("ordinary cluster at or below the limit was changed")
				}
			} else if got := screenLine(model, 0); got != "[Unicode cluster truncated]" {
				t.Fatal("first oversized cluster was not explicitly shortened")
			}
		})
	}
}

func TestOutputToolsGraphemeShorteningDoesNotChangeNativeOutput(t *testing.T) {
	c, out, _ := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte("\x1b[?1049h"))
	out.Reset()
	payload := []byte("a" + strings.Repeat("\u0301", 4096) + " tail\r\n")
	c.WritePTY(payload)
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("retained Unicode policy changed physical native-application output")
	}
}

func TestOutputToolsGraphemeShorteningPreservesSubsequentControlsAndLinks(t *testing.T) {
	model := vt.NewEmulator(80, 4)
	t.Cleanup(func() { _ = model.Close() })
	var stream outputModelStream
	stream.write(model, []byte("a"))
	for range 4096 {
		stream.write(model, []byte("\u0301"))
	}
	stream.write(model, []byte("\r\n\x1b[32m\x1b]8;id=cafe\u0301;https://example.test/cafe\u0301\x1b\\next\x1b]8;;\x1b\\"))
	if got := screenLine(model, 0); got != "[Unicode cluster truncated]" {
		t.Fatal("control boundary changed the truncation notice")
	}
	if got := screenLine(model, 1); got != "next" {
		t.Fatal("control boundary or subsequent text was changed")
	}
	cell := model.CellAt(0, 1)
	if cell.Link.URL != "https://example.test/cafe\u0301" || cell.Link.Params != "id=cafe\u0301" || cell.Style.Fg == nil {
		t.Fatalf("subsequent OSC8/SGR lost its wire fields or style: %#v", cell)
	}
}

func TestOutputToolsRetainsUnicodeAcrossByteSizedReadsAndWraps(t *testing.T) {
	for _, prefix := range []string{"", strings.Repeat("x", 79)} {
		t.Run(fmt.Sprintf("prefix-%d", len(prefix)), func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			for _, value := range []byte(prefix + "界😊 e\u0301 👩‍💻 👍🏽 done\r\n") {
				c.WritePTY([]byte{value})
			}
			for _, frozen := range []bool{false, true} {
				if frozen {
					input.FilterInput([]byte("\x1b[5~"), c, false, false)
				}
				screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
				if !terminalContainsLine(screen, "界😊 e\u0301 👩‍💻 👍🏽 done") {
					t.Fatalf("byte-sized reads lost Unicode near wrap (prefix %d, frozen %v): %q", len(prefix), frozen, terminalScreenLines(screen))
				}
			}
		})
	}
}

func TestOutputToolsRetainsCombiningMarksAcrossStyleChanges(t *testing.T) {
	c, out, _ := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	for _, value := range []byte("e\x1b[31m\u0301\x1b[0m done\r\n") {
		c.WritePTY([]byte{value})
	}
	if screen := applyTerminalOutput(t, out.Bytes(), 80, 14); !terminalContainsLine(screen, "e\u0301 done") {
		t.Fatalf("style changes erased a combining mark: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsRetainsChangingWidthClusterAtTheRightEdge(t *testing.T) {
	text := []byte(strings.Repeat("x", 79) + "❤\ufe0f done\r\n")
	for split := 79; split <= len(text); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, out, _ := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY(text[:split])
			c.WritePTY(text[split:])
			screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
			if !terminalContainsLine(screen, "❤\ufe0f done") {
				t.Fatalf("width-changing cluster was lost at split %d: %q", split, terminalScreenLines(screen))
			}
		})
	}
}

func TestOutputToolsRetainsUnnormalizedHyperlinkPayloadAcrossReads(t *testing.T) {
	c, out, _ := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	url := "https://example.com/e\u0301"
	link := "\x1b]8;id=e\u0301;" + url + "\x1b\\linked\x1b]8;;\x1b\\ plain\r\n"
	for _, value := range []byte(link) {
		c.WritePTY([]byte{value})
	}
	if !strings.Contains(out.String(), "\x1b]8;id=e\u0301;"+url+"\a") {
		t.Fatal("retained output normalized or altered a hyperlink payload")
	}
}

func TestOutputToolsSearchPasteOwnershipSurvivesNativeHandoff(t *testing.T) {
	for split := 1; split <= len(bracketedPasteStart); split++ {
		t.Run(fmt.Sprintf("opener-split-%d", split), func(t *testing.T) {
			c, _, input := outputToolsFixture(t, "needle\r\n")
			input.FilterInput([]byte("\x1b[18~"), c, false, true)
			input.FilterInput(bracketedPasteStart[:split], c, false, true)
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY([]byte("\x1b[?1049h"))
			payload := append(append(append([]byte{}, bracketedPasteStart[split:]...), []byte("needle\r")...), bracketedPasteEnd...)
			if got := input.FilterInput(payload, c, false, false); len(got) > 0 {
				t.Fatalf("search-owned paste was redirected to a native application: %q", got)
			}
			if got := input.FilterInput([]byte("native input"), c, false, false); string(got) != "native input" {
				t.Fatal("native application did not regain ordinary input")
			}
			if got := input.FilterInput([]byte("\x1b[18~"), c, false, false); string(got) != "\x1b[18~" {
				t.Fatal("native application search key was intercepted")
			}
		})
	}
}

func TestOutputToolsSearchClosePreservesAdjacentShellText(t *testing.T) {
	c, _, input := outputToolsFixture(t, "needle\r\n")
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	if got := input.FilterInput([]byte("\x03typed after close"), c, false, true); string(got) != "typed after close" {
		t.Fatalf("closing search lost adjacent shell text: %q", got)
	}
	if c.viewport.search != nil || c.viewport.frozen == nil {
		t.Fatal("closing search must retain the output reading position")
	}
}

func TestOutputToolsSearchKeepsOwnershipWhenSuggestionsArrive(t *testing.T) {
	c, out, input := outputToolsFixture(t, "needle\r\n")
	visible := false
	menu := []byte("\x1b[1;1HASYNC-MENU")
	c.SetTransientUIReflow(nil, nil, func() []byte { return menu }, nil, func() (int, int) { return 1, 1 }, func() bool { return visible })
	input.FilterInput([]byte("\x1b[18~"), c, false, true)
	visible = true
	c.ComposeUI(func() []byte { return menu })
	if got := input.FilterInput([]byte("needle\r"), c, true, true); len(got) > 0 {
		t.Fatalf("asynchronous suggestions took the output search query: %q", got)
	}
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if terminalContainsLine(screen, "ASYNC-MENU") || !terminalContainsLine(screen, "· needle ·") || !terminalContainsLine(screen, "Find: 1/1") {
		t.Fatalf("asynchronous UI overwrote output search: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchOpensWhileSuggestionsAreAlreadyVisible(t *testing.T) {
	c, out, input := outputToolsFixture(t, "needle\r\n")
	menu := []byte("\x1b[1;1HASYNC-MENU")
	c.SetTransientUIReflow(nil, nil, func() []byte { return menu }, nil, func() (int, int) { return 1, 1 }, func() bool { return true })
	c.ComposeUI(func() []byte { return menu })
	if got := input.FilterInput([]byte("\x1b[18~needle"), c, true, true); len(got) > 0 {
		t.Fatalf("visible suggestions intercepted the dedicated output-search action: %q", got)
	}
	if screen := applyTerminalOutput(t, out.Bytes(), 80, 14); terminalContainsLine(screen, "ASYNC-MENU") || !terminalContainsLine(screen, "Find: 1/1") {
		t.Fatalf("output-search did not take the visible pane: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchKeepsPointerOwnershipWhenSuggestionsArrive(t *testing.T) {
	c, _, input := outputToolsFixture(t, "needle\r\n")
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputMouse: "navigate", OutputLines: 100})
	visible := false
	menu := []byte("\x1b[1;1HASYNC-MENU")
	c.SetTransientUIReflow(nil, nil, func() []byte { return menu }, nil, func() (int, int) { return 1, 1 }, func() bool { return visible })
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	visible = true
	c.ComposeUI(func() []byte { return menu })
	if !c.viewportMouse {
		t.Fatal("asynchronous suggestions disabled the search-owned pointer")
	}
	_, left, _ := c.viewportBadge()
	click := fmt.Sprintf("\x1b[<0;%d;%dM", left+1, c.viewportBadgeRow())
	if got := input.FilterInput([]byte(click), c, true, true); len(got) > 0 {
		t.Fatal("search badge click leaked into shell input")
	}
	if c.viewport.search != nil || c.viewport.frozen != nil {
		t.Fatal("the search-owned back-to-bottom badge was blocked by asynchronous suggestions")
	}
}

func TestOutputToolsSearchHighlightsAcrossStyleResets(t *testing.T) {
	c, out, input := outputToolsFixture(t, "before \x1b[31mne\x1b[0medle after\r\n")
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	for x := 7; x < 13; x++ {
		if cell := screen.CellAt(x, 0); cell == nil || cell.Style.Attrs&uv.AttrReverse == 0 {
			t.Fatalf("selected match lost highlighting at styled column %d: %#v", x, cell)
		}
	}
	if screen.CellAt(6, 0).Style.Attrs&uv.AttrReverse != 0 || screen.CellAt(13, 0).Style.Attrs&uv.AttrReverse != 0 {
		t.Fatal("match highlighting changed surrounding output")
	}
}

func TestOutputToolsSearchMatchesCanonicalUnicodeText(t *testing.T) {
	c, out, input := outputToolsFixture(t, "café then cafe\u0301\r\n")
	input.FilterInput([]byte("\x1b[18~CAFÉ\r"), c, false, true)
	if screen := applyTerminalOutput(t, out.Bytes(), 80, 14); !terminalContainsLine(screen, "2/2") {
		t.Fatalf("search distinguished canonically equivalent text: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchMatchesJoinedEmojiQueries(t *testing.T) {
	c, out, input := outputToolsFixture(t, "before 👩‍💻 after\r\n")
	input.FilterInput([]byte("\x1b[18~"), c, false, true)
	for _, value := range []byte("👩‍💻") {
		input.FilterInput([]byte{value}, c, false, true)
	}
	if screen := applyTerminalOutput(t, out.Bytes(), 80, 14); !terminalContainsLine(screen, "Find: 1/1") {
		t.Fatalf("search changed a joined-emoji query: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchKeepsPositionVisibleForLongQueries(t *testing.T) {
	query := strings.Repeat("n", 70)
	c, out, input := outputToolsFixture(t, query+"\r\n")
	input.FilterInput([]byte("\x1b[18~"+query), c, false, true)
	if screen := applyTerminalOutput(t, out.Bytes(), 80, 14); !terminalContainsLine(screen, "1/1") {
		t.Fatal("long query hid the current match position")
	}
}

func TestOutputToolsSearchClosesWhenTheViewportLosesItsSurface(t *testing.T) {
	c, _, input := outputToolsFixture(t, "needle\r\n")
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	c.Resize(80, 1)
	if c.viewport.search != nil {
		t.Fatal("hidden search modal survived after returning input to the shell")
	}
	if got := input.FilterInput([]byte("ordinary input"), c, false, true); string(got) != "ordinary input" {
		t.Fatal("unusable viewport retained input ownership")
	}
}

func TestOutputToolsSearchKeepsTheMatchVisibleAfterNarrowResize(t *testing.T) {
	c, out, input := outputToolsFixture(t, strings.Repeat("x", 60)+"needle\r\n")
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	out.Reset()
	c.Resize(40, 14)
	screen.Resize(40, 14)
	if _, err := screen.Write(out.Bytes()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screenLine(screen, 0), "needle") || !terminalContainsLine(screen, "1/1") {
		t.Fatalf("narrow resize hid the selected match: %q", terminalScreenLines(screen))
	}
}

func TestOutputToolsSearchPreservesTheFixedAccountTitle(t *testing.T) {
	c, out, input := outputToolsFixture(t, "needle\r\n")
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputMouse: "select", OutputLines: 100, Title: terminalChatboxBarConfig{Right: []string{"codex-account"}}})
	c.SetStatusSnapshot(statusSnapshot{CodexAccount: "person@example.test"})
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	title := screenLine(screen, c.height-c.surfaceRows)
	if !strings.HasSuffix(title, "Codex person@example.test") {
		t.Fatalf("output search overwrote the fixed account title: %q", title)
	}
}

func outputToolsFixture(t *testing.T, text string) (*terminalCompositor, *bytes.Buffer, *terminalInputFilter) {
	t.Helper()
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "output-tools", 80, 14)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputMouse: "select", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
	c.WritePTY([]byte("› keep this input"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
	c.WritePTY([]byte(text))
	return c, &out, &terminalInputFilter{}
}
