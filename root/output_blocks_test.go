package root

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/vt"
)

func blocksFixture(t *testing.T, document string) (*terminalCompositor, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Decode([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "blocks", 60, 24)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	c.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	return c, &out
}

func blocksPrompt(c *terminalCompositor, command string) {
	c.SetMultilineInput(strings.Contains(command, "\n"))
	c.WritePTY(terminalMarkerBytes("blocks", "prompt-start"))
	c.WritePTY([]byte("› " + command))
	c.WritePTY(terminalMarkerBytes("blocks", "prompt-end"))
}

func blocksExecution(c *terminalCompositor, command, output, exit string) {
	blocksPrompt(c, command)
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte(output))
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:"+exit))
}

func blockEdgeCount(screen *vt.Emulator) int {
	count := 0
	for y := 0; y < screen.Height(); y++ {
		if screenLine(screen, y) == strings.Repeat("─", screen.Width()) {
			count++
		}
	}
	return count
}

func TestOutputBlocksDefaultGroupsCommandsIncludingSilentExecutions(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "first-command", "first-result café 日本語\r\n", "0")
	blocksExecution(c, "silent-command", "", "0")
	blocksExecution(c, "failed-command", "failure-result", "1")
	blocksPrompt(c, "next-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	last := -1
	for _, want := range []string{"› first-command", "first-result", "› silent-command", "› failed-command", "failure-result", "exit 1", "› next-input"} {
		found := terminalLineIndex(lines, want, last+1)
		if found < 0 {
			t.Fatalf("missing ordered block text %q: %q", want, lines)
		}
		last = found
	}
	dividers := blockEdgeCount(screen)
	if dividers != 0 {
		t.Fatalf("pixel boundaries must not consume text rows, got %d: %q", dividers, lines)
	}
	if !strings.Contains(out.String(), "\x1b[?1002h") || strings.Contains(out.String(), "\x1b[?1003h") {
		t.Fatal("interactive blocks require button-motion selection without all-motion capture")
	}
}

func TestOutputBlocksExplicitSelectKeepsNativePointerOwnership(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-mouse='select'\n")
	blocksExecution(c, "command", "output\r\n", "0")
	blocksPrompt(c, "pending-input")
	for _, mode := range []string{"1000", "1002", "1003", "1006"} {
		if strings.Contains(out.String(), "\x1b[?"+mode+"h") {
			t.Fatalf("explicit native selection enabled mouse reporting %s", mode)
		}
	}
}

func TestOutputBlocksConfigurationReachesRendering(t *testing.T) {
	for _, test := range []struct {
		name, document   string
		command, divider bool
	}{
		{"omitted", "", true, false},
		{"disabled dividers", "[ui.chatbox]\nblock-dividers=false\n", true, false},
		{"explicit output", "[ui.chatbox]\nscrollback='output'\n", false, false},
		{"explicit snapshot", "[ui.chatbox]\nscrollback='snapshot'\n", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, out := blocksFixture(t, test.document)
			blocksExecution(c, "submitted-command", "owned-output\r\n", "0")
			blocksPrompt(c, "next-input")
			screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
			if terminalContainsLine(screen, "submitted-command") != test.command || (blockEdgeCount(screen) > 0) != test.divider || !terminalContainsLine(screen, "owned-output") {
				t.Fatalf("wrong configuration-to-renderer behavior: %q", terminalScreenLines(screen))
			}
		})
	}
}

func TestOutputBlocksSpacingAndOutputOnlyCopy(t *testing.T) {
	for _, spacing := range []string{"0", "3"} {
		t.Run(spacing, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\nhistory-spacing="+spacing+"\n")
			blocksExecution(c, "copy-command", "\x1b[31mowned café 日本語\x1b[0m\r\n", "0")
			blocksExecution(c, "neighbor-command", "neighbor-result\r\n", "0")
			blocksPrompt(c, "next-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			command := terminalLineIndex(lines, "› neighbor-command", 0)
			prior := terminalLineIndex(lines, "exit 0", 0)
			wantGap := 1
			if spacing == "3" {
				wantGap = 4
			}
			if command-prior != wantGap {
				t.Fatalf("spacing=%s: command/outcome gap=%d, want %d: %q", spacing, command-prior, wantGap, lines)
			}
			input := &terminalInputFilter{}
			input.FilterInput([]byte("\x1b[21~\x1b[21~"), c, false, true)
			out.Reset()
			input.FilterInput([]byte("\x1b[23~"), c, false, true)
			assertExecutionClipboard(t, out.String(), "owned café 日本語\n")
			// The clipboard assertion above checks output-only bytes. The card
			// remains visible, so its repaint may legitimately contain the label.
		})
	}
}

func TestOutputBlocksResizeSearchAndPaneExportDoNotExposeDecorations(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c, out := blocksFixture(t, "")
	blocksExecution(c, "find-command", "\x1b[31msearchable 日本語\x1b[0m\r\n", "0")
	blocksPrompt(c, "keep-input")
	c.HandleViewportInput([]byte("\x1b[18~"), false)
	c.PasteOutputSearch([]byte("─"))
	if len(c.viewport.search.matches) != 0 {
		t.Fatal("decorative rule became searchable output")
	}
	c.HandleViewportInput([]byte("\x15"), false)
	c.PasteOutputSearch([]byte("searchable"))
	if len(c.viewport.search.matches) != 1 {
		t.Fatal("block decoration corrupted output search")
	}
	c.HandleViewportInput([]byte("\x1b"), false)
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	for _, width := range []int{20, 80, 40} {
		out.Reset()
		c.Resize(width, 24)
		physical := applyTerminalOutput(t, out.Bytes(), width, 24)
		if !terminalContainsLine(physical, "searchable") || !terminalContainsLine(physical, "keep-input") {
			t.Fatalf("resize lost frozen output or fixed input: %q", terminalScreenLines(physical))
		}
		dividers := 0
		for _, line := range terminalScreenLines(physical) {
			if strings.Contains(line, strings.Repeat("─", width)) {
				dividers++
			}
		}
		if dividers != 1 {
			t.Fatalf("divider wrapped or duplicated after resize to %d: %q", width, terminalScreenLines(physical))
		}
	}
	c.HandleViewportInput([]byte("\x1b[24~"), false)
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("pane export did not start")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one pane export: %v %v", paths, err)
	}
	path := paths[0]
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(text, []byte("find-command")) || !bytes.Contains(text, []byte("searchable 日本語")) || bytes.Contains(text, []byte("─")) || bytes.Contains(text, []byte(outputBlockDividerTarget)) {
		t.Fatalf("pane export lost text or included decorative metadata: %q", text)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("export permissions changed")
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("export directory permissions changed")
	}
}

func TestOutputBlocksNativeViewportPreservesApplicationOwnership(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='terminal'\n")
	blocksExecution(c, "first-command", "first-result\r\n", "0")
	blocksPrompt(c, "native-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	if !terminalContainsLine(physical, "native-command") || !terminalContainsLine(physical, "────────") {
		t.Fatalf("native command block missing: %q", terminalScreenLines(physical))
	}
	out.Reset()
	native := "\x1b[?1049h\x1b[2;2Hprivate native canvas\x1b[?1049l"
	c.WritePTY([]byte(native))
	if out.String() != native || c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatalf("blocks interfered with native application: %q", out.String())
	}
}

func TestOutputBlocksNativeCommandLabelCannotBecomeADivider(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='terminal'\n")
	blocksPrompt(c, "first-line\n\x1b]8;;vuja:block-divider\x1b\\  ─\x1b]8;;\x1b\\")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	rules, labels := 0, 0
	for _, line := range lines {
		if line == strings.Repeat("─", 60) {
			rules++
		} else if strings.TrimSpace(line) == "─" {
			labels++
		}
	}
	if rules != 1 || labels != 1 {
		t.Fatalf("only the injected boundary may expand; preserve the linked command label: %q", lines)
	}
}

func TestOutputBlocksRestoreAndClearKeepFlatPrivateCheckpoints(t *testing.T) {
	dir := t.TempDir()
	a, _ := blocksFixture(t, "")
	if err := a.ConfigureOutputRecovery(dir, "pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksExecution(a, "stored-command", "stored-result\r\n", "0")
	a.Close()
	b, out := blocksFixture(t, "")
	if err := b.ConfigureOutputRecovery(dir, "pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(b, "restored-input")
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	if !terminalContainsLine(physical, "stored-command") || !terminalContainsLine(physical, "stored-result") || blockEdgeCount(physical) != 0 {
		t.Fatalf("restored block lost display rows: %q", terminalScreenLines(physical))
	}
	if len(b.viewport.executions.retained) != 0 {
		t.Fatal("restoration fabricated execution identities")
	}
	cfg := b.chatboxConfig
	cfg.BlockDividers = false
	b.SetChatboxConfig(cfg)
	blocksExecution(b, "clear", "\x1b[H\x1b[2J\x1b[3J", "0")
	blocksPrompt(b, "cleared-input")
	b.Close()
	restored, output := blocksFixture(t, "")
	if err := restored.ConfigureOutputRecovery(dir, "pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(restored, "final-input")
	physical = applyTerminalOutput(t, output.Bytes(), 60, 24)
	if terminalContainsLine(physical, "stored-result") || terminalContainsLine(physical, "stored-command") || !terminalContainsLine(physical, "final-input") {
		t.Fatalf("clear resurrected prior blocks: %q", terminalScreenLines(physical))
	}
}

func TestOutputBlocksMultilineAndSplitMarkersDoNotDuplicateBoundaries(t *testing.T) {
	var screens []string
	for _, fragmented := range []bool{false, true} {
		c, out := blocksFixture(t, "")
		blocksPrompt(c, "printf first\r\nsecond-line")
		c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
		start := terminalMarkerBytes("blocks", "command-start")
		if fragmented {
			for _, b := range start {
				c.WritePTY([]byte{b})
			}
		} else {
			c.WritePTY(start)
		}
		c.WritePTY([]byte("result-"))
		c.WritePTY([]byte("tail\r\n"))
		c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
		blocksPrompt(c, "next-input")
		c.SetStatusSnapshot(statusSnapshot{})
		screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
		lines := terminalScreenLines(screen)
		all := strings.Join(lines, "\n")
		dividers := blockEdgeCount(screen)
		if !strings.Contains(all, "printf first") || !strings.Contains(all, "second-line") || !strings.Contains(all, "result-tail") || dividers != 0 {
			t.Fatalf("multiline command or block boundary corrupted: %q", lines)
		}
		screens = append(screens, all)
	}
	if screens[0] != screens[1] {
		t.Fatal("marker fragmentation changed block rendering")
	}
}

func TestOutputBlocksPrivateCommandsNeverEnterCheckpoints(t *testing.T) {
	dir := t.TempDir()
	c, _ := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(dir, "pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(c, " private-command-secret")
	c.WritePTY(terminalMarkerBytes("blocks", "command-private"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("public-result\r\n"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	blocksExecution(c, "public-command", "public-result\r\n", "0")
	// A previous public classification cannot authorize an older/missing hook.
	blocksPrompt(c, "unknown-command-secret")
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("later-result\r\n"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	c.Close()
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private-command-secret")) || bytes.Contains(data, []byte("unknown-command-secret")) || !bytes.Contains(data, []byte("public-command")) || !bytes.Contains(data, []byte("public-result")) {
		t.Fatal("default blocks persisted excluded command text or discarded public output")
	}
}

func TestOutputBlocksSilentRetentionStaysBounded(t *testing.T) {
	c, _ := blocksFixture(t, "[ui.chatbox]\noutput-lines=3\n")
	for range 20 {
		blocksExecution(c, "silent", "", "0")
	}
	blocksPrompt(c, "next")
	if len(retainedModelRows(c.viewport.model)) > 3+c.viewport.model.Height() || !c.viewport.outputTruncated() {
		t.Fatal("silent block display rows bypassed retention or hid loss")
	}
}

func TestOutputBlocksGenuineHyperlinkIsNotDividerMetadata(t *testing.T) {
	for _, dividers := range []string{"true", "false"} {
		t.Run(dividers, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			c, out := blocksFixture(t, "[ui.chatbox]\nblock-dividers="+dividers+"\n")
			blocksExecution(c, "linked-output", "\x1b]8;;https://example.test/vuja:block-divider\x1b\\─\x1b]8;;\x1b\\\r\n", "0")
			blocksPrompt(c, "keep-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			if terminalLineIndex(lines, "─", 0) < 0 {
				t.Error("genuine linked output was hidden")
			}
			genuine := false
			for _, line := range lines {
				if strings.TrimSpace(line) == "─" {
					genuine = true
				}
			}
			if !genuine {
				t.Error("genuine glyph was expanded into decoration or hidden")
			}
			c.HandleViewportInput([]byte("\x1b[18~"), false)
			c.PasteOutputSearch([]byte("─"))
			if len(c.viewport.search.matches) != 1 {
				t.Error("genuine glyph was excluded from output search")
			}
			c.HandleViewportInput([]byte("\x1b"), false)
			input := &terminalInputFilter{}
			input.FilterInput([]byte("\x1b[21~"), c, false, true)
			out.Reset()
			input.FilterInput([]byte("\x1b[23~"), c, false, true)
			assertExecutionClipboard(t, out.String(), "─\n")
			path, err := writeRetainedOutputExport(*c.viewport.executions.selected)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "─\n" {
				t.Fatalf("export discarded genuine linked output: %q (%v)", data, err)
			}
			row := recoveryRow(c.viewport.executions.selected.rows[0], nil, false)
			if ansi.Strip(row) != "─" || strings.Contains(row, outputBlockDividerTarget) {
				t.Fatalf("checkpoint reclassified genuine output: %q", row)
			}
		})
	}
}

func TestOutputBlocksNativeViewportGroupsConsecutiveExecutions(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='terminal'\n")
	c.Resize(60, 40)
	blocksExecution(c, "first-native", "first-result\r\n", "0")
	blocksExecution(c, "silent-native", "", "0")
	blocksExecution(c, "failed-native", "last-result", "1")
	blocksPrompt(c, "next-native")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 40))
	last := -1
	for _, want := range []string{"› first-native", "first-result", "› silent-native", "› failed-native", "last-result", "exit 1", "› next-native"} {
		at := terminalLineIndex(lines, want, last+1)
		if at < 0 {
			t.Fatalf("native block sequence lost %q: %q", want, lines)
		}
		last = at
	}
}

func TestOutputBlocksUnmarkedPromptTransitionDoesNotReusePublicLabel(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "public-first", "result\r\n", "0")
	blocksPrompt(c, " private-fallback-secret")
	c.WritePTY([]byte("\r\n"))
	out.Reset()
	blocksPrompt(c, "next-input")
	if strings.Contains(out.String(), "private-fallback-secret") {
		t.Fatal("unclassified prompt transition reused the previous public label permission")
	}
}

func TestOutputBlocksDiscloseClippedCommandLabelsAcrossResize(t *testing.T) {
	const notice = "[command input truncated to visible rows]"
	for _, mode := range []string{"pinned", "terminal"} {
		for _, resize := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resize=%t", mode, resize), func(t *testing.T) {
				t.Setenv("XDG_CACHE_HOME", t.TempDir())
				c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='"+mode+"'\noutput-lines=200\n")
				c.Resize(60, 10)
				var input []string
				for i := range 36 {
					input = append(input, fmt.Sprintf("input-row-%02d", i))
				}
				blocksPrompt(c, strings.Join(input, "\r\n"))
				if len(c.surfaceContentLines) >= len(input) {
					t.Fatal("fixture did not exceed visible input capacity")
				}
				if resize {
					c.Resize(80, 12)
				}
				out.Reset()
				c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
				c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
				c.WritePTY([]byte("result\r\n"))
				c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
				if mode == "pinned" {
					text := (retainedExecution{rows: retainedModelRows(c.viewport.model), newline: true}).plainText()
					if !strings.Contains(text, notice) || !strings.Contains(text, "input-row-35") {
						t.Fatalf("retained clipped label lacks disclosure or visible suffix: %q", text)
					}
					path, err := writeRetainedOutputExport(retainedExecution{rows: retainedModelRows(c.viewport.model), newline: true})
					if err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(path)
					if err != nil || !bytes.Contains(data, []byte(notice)) {
						t.Fatalf("export lost truncation disclosure: %q (%v)", data, err)
					}
				} else if !strings.Contains(ansi.Strip(out.String()), notice) {
					t.Fatal("native clipped label lacks truncation disclosure")
				}
				blocksPrompt(c, "next-short-command")
				if strings.Contains(ansi.Strip(strings.Join(c.blockCommandRows(), "\n")), notice) {
					t.Fatal("clipping disclosure leaked into the next prompt")
				}
			})
		}
	}
}

func TestOutputBlocksClippedPrivateInputRemainsRedacted(t *testing.T) {
	c, _ := blocksFixture(t, "")
	c.Resize(60, 10)
	blocksPrompt(c, strings.Repeat(" private-long-input\r\n", 36))
	c.WritePTY(terminalMarkerBytes("blocks", "command-private"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	text := (retainedExecution{rows: retainedModelRows(c.viewport.model)}).plainText()
	if !strings.Contains(text, "[private command]") || strings.Contains(text, "private-long-input") || strings.Contains(text, "truncated to visible rows") {
		t.Fatalf("private clipping disclosure bypassed redaction: %q", text)
	}
}

func TestOutputBlocksDiscloseColumnsLostDuringResize(t *testing.T) {
	const notice = "[command input truncated to visible rows]"
	for _, mode := range []string{"pinned", "terminal"} {
		for _, test := range []struct {
			name, command string
			width         int
			clipped       bool
		}{
			{"narrow long", "printf " + strings.Repeat("x", 36), 30, true},
			{"narrow short", "printf x", 30, false},
			{"widen long", "printf " + strings.Repeat("x", 36), 80, false},
			{"inner projection", "printf " + strings.Repeat("x", 50), 60, true},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='"+mode+"'\n")
				blocksPrompt(c, test.command)
				if c.surfaceContentRows != 1 {
					t.Fatal("fixture must fit the original single input row")
				}
				c.Resize(test.width, 24)
				out.Reset()
				c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
				c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
				c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
				text := ansi.Strip(out.String())
				if mode == "pinned" {
					text = (retainedExecution{rows: retainedModelRows(c.viewport.model)}).plainText()
				}
				if strings.Contains(text, notice) != test.clipped {
					t.Fatalf("wrong resize clipping disclosure: %q", text)
				}
			})
		}
	}
}
