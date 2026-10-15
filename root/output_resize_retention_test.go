package root

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Original QA scenarios 2/8: geometry changes drawing, not stored text.
func TestRetainedExecutionResizePreservesFullLogicalText(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte("012345678901234567890123456789"))
	c.Resize(20, 14)
	c.WritePTY([]byte("\r\nabcdefghijklmnopqrstuvwxyz\r\n"))
	c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
	c.WritePTY([]byte("› keep this input"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
	c.Resize(80, 14)
	out.Reset()
	if got := input.FilterInput([]byte("\x1b[23~"), c, false, true); len(got) != 0 {
		t.Fatalf("copy reached shell input: %q", got)
	}
	assertExecutionClipboard(t, out.String(), "012345678901234567890123456789\nabcdefghijklmnopqrstuvwxyz\n")
}

func TestRetainedOutputSearchAndExportPreserveFullRowsAfterNarrowing(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, _, input := outputToolsFixture(t, "")
	text := "old-prefix-" + strings.Repeat("x", 30) + "-old-suffix"
	completeRetainedExecution(t, c, text+"\r\n"+strings.Repeat("short\r\n", 20))
	c.Resize(20, 14)
	if got := input.FilterInput([]byte("\x1b[18~old-suffix"), c, false, true); len(got) != 0 {
		t.Fatalf("search reached shell input: %q", got)
	}
	if len(c.viewport.search.matches) != 1 {
		t.Errorf("search started after narrowing lost the retained suffix: %d matches", len(c.viewport.search.matches))
	}
	input.FilterInput([]byte("\x1b[24~"), c, false, true)
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("pane export did not start")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatal("pane export did not publish exactly one file")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), text+"\n") {
		t.Fatalf("export lost or inserted geometry-only line breaks into stored text: %q", data)
	}
}

func TestRetainedExecutionSoftWrapIsNotAHardNewline(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.Resize(20, 14)
	text := "abcdefghij  klmnopqrstuvwxyz  end"
	completeRetainedExecution(t, c, text+"\r\n")
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), text+"\n")
}

func TestRetainedOutputHeightResizeDoesNotDiscardStoredRows(t *testing.T) {
	c, _, input := outputToolsFixture(t, "")
	text := "height-first\r\nheight-second\r\nheight-third\r\nheight-fourth\r\nheight-last\r\n"
	completeRetainedExecution(t, c, text)
	c.Resize(80, c.surfaceRows+1)
	c.Resize(80, 14)
	input.FilterInput([]byte("\x1b[18~height-last"), c, false, true)
	if len(c.viewport.search.matches) != 1 {
		t.Fatal("height shrink/grow discarded the last stored row")
	}
	joined := ansi.Strip(strings.Join(c.viewport.frozen, "\n"))
	for _, line := range []string{"height-first", "height-second", "height-third", "height-fourth", "height-last"} {
		if !strings.Contains(joined, line) {
			t.Errorf("height shrink/grow discarded %q", line)
		}
	}
}

func TestOutputCheckpointResizePreservesFullSemanticText(t *testing.T) {
	c, _ := recoveryCompositor(t, t.TempDir(), "resize-checkpoint-pane", "resize-checkpoint", nil)
	text := "checkpoint-prefix-" + strings.Repeat("x", 30) + "-checkpoint-suffix"
	c.WritePTY(terminalMarkerBytes("resize-checkpoint", "command-start"))
	c.WritePTY([]byte(text + "\r\n" + strings.Repeat("short\r\n", 20)))
	c.Resize(20, 14)
	c.WritePTY(terminalMarkerBytes("resize-checkpoint", "command-end:0"))
	c.Close() // drain the bounded private writer, not a live Vuja process
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil {
		t.Fatal(err)
	}
	var state outputRecoveryState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(strings.Join(state.Rows, "\n")), text) {
		t.Fatalf("checkpoint captured display-width clipping instead of stored text: %q", state.Rows)
	}
}

func TestRetainedFrozenExportKeepsItsOwnTruncationState(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, _, input := outputToolsFixture(t, "kept-before-freeze\r\n")
	input.FilterInput([]byte("\x1b[18~kept-before-freeze"), c, false, true)
	c.viewport.model.SetScrollbackSize(2)
	c.WritePTY([]byte(strings.Repeat("new output\r\n", 30)))
	if !c.viewport.outputTruncated() {
		t.Fatal("fixture did not evict live output")
	}
	input.FilterInput([]byte("\x1b[24~"), c, false, true)
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("export never started")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatal("export was not published")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kept-before-freeze") || strings.Contains(string(data), "truncated") {
		t.Fatalf("unrelated live eviction altered the frozen export's contents or completeness: %q", data)
	}
}

func TestRetainedFrozenLayoutKeepsReadingAnchorAcrossResizeAndIncomingOutput(t *testing.T) {
	var original strings.Builder
	for i := range 50 {
		original.WriteString(strings.Repeat(string(rune('a'+i%26)), 90) + "\r\n")
	}
	c, out, input := outputToolsFixture(t, original.String())
	input.FilterInput([]byte("\x1b[5~"), c, false, true)
	v := c.viewport
	frozen := strings.Join(v.frozen, "\n")
	anchor := v.frozenLayout[v.top]
	c.Resize(40, 14)
	resized := v.frozenLayout[v.top]
	if resized.logical != anchor.logical || resized.first > anchor.first || resized.last <= anchor.first {
		t.Fatalf("resize moved away from the logical reading anchor: before=%+v after=%+v", anchor, resized)
	}
	top := v.top
	c.WritePTY([]byte(strings.Repeat("incoming\r\n", 120)))
	if v.top != top || strings.Join(v.frozen, "\n") != frozen || !v.newOutput {
		t.Fatal("incoming output moved or replaced the frozen reading snapshot")
	}
	if strings.Contains(out.String(), "\x1b[?1000h") || strings.Contains(out.String(), "\x1b[?1003h") {
		t.Fatal("frozen reflow took ordinary drag selection from the terminal")
	}
	if got := input.FilterInput([]byte("\x1b[F"), c, false, true); len(got) != 0 || v.frozen != nil {
		t.Fatal("return-to-latest changed shell input or retained the frozen snapshot")
	}
}

func TestRetainedOutputResizeBetweenGraphemePartsPreservesCopyAndSearch(t *testing.T) {
	for _, fixture := range []struct {
		name, before, after, query string
		width                      int
	}{
		{"combining", "e", "\u0301 done", "é", 40},
		{"joined emoji", "👩", "\u200d💻 done", "👩‍💻", 40},
		{"pending wrap", strings.Repeat("x", 79) + "e", "\u0301 done", "é", 20},
		{"incomplete UTF-8", "caf\xc3", "\xa9 done", "café", 40},
		{"width change at edge", strings.Repeat("x", 19) + "✈", "\ufe0f done", "✈️", 20},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY([]byte(fixture.before))
			c.Resize(fixture.width, 14)
			c.WritePTY([]byte(fixture.after + "\r\n"))
			c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
			c.WritePTY([]byte("› unchanged"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
			out.Reset()
			input.FilterInput([]byte("\x1b[23~"), c, false, true)
			assertExecutionClipboard(t, out.String(), fixture.before+fixture.after+"\n")
			input.FilterInput([]byte("\x1b"), c, false, true)
			input.FlushEscape(c, false)
			input.FilterInput([]byte("\x1b[18~"+fixture.query), c, false, true)
			if c.viewport.search == nil {
				t.Fatal("pane search did not open")
			}
			if len(c.viewport.search.matches) != 1 {
				t.Fatalf("pane search lost the resized grapheme: %d matches", len(c.viewport.search.matches))
			}
		})
	}
}
