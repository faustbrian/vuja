package root

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/v2/internal/config"
)

func blockTestRGB(c color.Color) [3]uint8 {
	if c == nil {
		return [3]uint8{}
	}
	r, g, b, _ := c.RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

func TestOutputBlockStatesDefaultMarksTheWholeFailedBlock(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\nsession-warning='#ffaa00'\n")
	theme := testInputBoxTheme()
	theme.Background = "#000000"
	c.SetInputBoxTheme(theme)
	blocksExecution(c, "failed-state-command", "state-output café 日本語\r\n", "2")
	blocksExecution(c, "successful-state-command", "warning is only text\r\n", "0")
	blocksPrompt(c, "next-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	for _, text := range []string{"failed-state-command", "state-output", "exit 2"} {
		y := terminalLineIndex(lines, text, 0)
		if y < 0 {
			t.Fatalf("missing %q: %q", text, lines)
		}
		if cell := screen.CellAt(0, y); cell == nil || cell.Content != "▌" {
			t.Errorf("failed block row %q has no thick left border", text)
		}
		if cell := screen.CellAt(59, y); cell == nil || cell.Style.Bg == nil {
			t.Errorf("failed block row %q lacks full-width background", text)
		} else if got := blockTestRGB(cell.Style.Bg); got != [3]uint8{51, 0, 0} {
			t.Errorf("wrong error tint: %v", got)
		}
	}
	for _, text := range []string{"successful-state-command", "warning is only text", "next-input"} {
		y := terminalLineIndex(lines, text, 0)
		if y < 0 {
			t.Fatalf("missing neutral row %q", text)
		}
		if cell := screen.CellAt(0, y); cell != nil && cell.Content == "▌" {
			t.Errorf("state leaked into %q", text)
		}
	}
}

func TestOutputBlockStatesClassificationAndConfig(t *testing.T) {
	for _, fixture := range []struct {
		name, config, exit string
		clipped            bool
		want               blockState
	}{
		{"warning", "", "0", true, blockWarning},
		{"error wins", "", "130", true, blockError},
		{"one is failure", "", "1", false, blockError},
		{"silent failure", "", "127", false, blockError},
		{"unknown", "", "bad", false, blockNeutral},
		{"disabled", "block-states=false\n", "2", false, blockNeutral},
		{"independent divider", "block-dividers=false\n", "2", false, blockError},
		{"output mode", "scrollback='output'\n", "2", false, blockNeutral},
		{"snapshot mode", "scrollback='snapshot'\n", "2", false, blockNeutral},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\n"+fixture.config+"[ui.chatbox.colors]\nexit-failure='#ff0000'\nsession-warning='#ffaa00'\n")
			theme := testInputBoxTheme()
			theme.Background = "#000000"
			c.SetInputBoxTheme(theme)
			input := "state-command"
			if fixture.clipped {
				input = strings.Repeat("clipped-input\r\n", 36)
			}
			blocksExecution(c, input, "", fixture.exit)
			blocksPrompt(c, "new-input")
			screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
			found := false
			for y := 0; y < screen.Height(); y++ {
				cell := screen.CellAt(0, y)
				if cell == nil || cell.Content != "▌" {
					continue
				}
				found = true
				want := [3]uint8{255, 0, 0}
				if fixture.want == blockWarning {
					want = [3]uint8{255, 170, 0}
				}
				if fixture.want != blockNeutral && blockTestRGB(cell.Style.Fg) != want {
					t.Fatalf("wrong severity color: %v", blockTestRGB(cell.Style.Fg))
				}
			}
			if found != (fixture.want != blockNeutral) {
				t.Fatalf("wrong state rail presence: %v, want %v", found, fixture.want)
			}
		})
	}
}

func TestOutputBlockStatesRetainTextAcrossFrozenResizeAndTools(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c, out := blocksFixture(t, "")
	const payload = "café 日本語 genuine ▌ "
	output := payload + strings.Repeat("longer text ", 12) + "end"
	blocksExecution(c, "failed-resize", "\x1b[31m"+output+"\x1b[0m", "1")
	blocksPrompt(c, "next-input")
	before := (retainedExecution{rows: retainedModelRows(c.viewport.model)}).plainText()
	c.HandleViewportInput([]byte("\x1b[5~"), false)
	for _, width := range []int{20, 80, 40} {
		out.Reset()
		c.Resize(width, 24)
		physical := applyTerminalOutput(t, out.Bytes(), width, 24)
		y := terminalLineIndex(terminalScreenLines(physical), "longer", 0)
		if y < 0 || physical.CellAt(0, y).Content != "▌" {
			t.Fatalf("browsed wrapped block lost rail at width %d: %q", width, terminalScreenLines(physical))
		}
		if got := (retainedExecution{rows: retainedModelRows(c.viewport.model)}).plainText(); got != before {
			t.Fatalf("resize changed semantic output: %q", got)
		}
	}
	c.HandleViewportInput([]byte("\x1b[18~"), false)
	c.PasteOutputSearch([]byte("genuine ▌"))
	if len(c.viewport.search.matches) != 1 {
		t.Fatal("genuine border glyph stopped being searchable")
	}
	c.HandleViewportInput([]byte("\x1b"), false)
	input := &terminalInputFilter{}
	input.FilterInput([]byte("\x1b[21~"), c, false, true)
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), output)
	path, err := writeRetainedOutputExport(*c.viewport.executions.selected)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != output {
		t.Fatalf("execution export gained chrome or changed text: %q (%v)", data, err)
	}
}

func TestOutputBlockStatesRestoreAndClearWithoutExecutionIdentity(t *testing.T) {
	dir := t.TempDir()
	a, _ := blocksFixture(t, "")
	if err := a.ConfigureOutputRecovery(dir, "state-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(a, " private-state-secret")
	a.WritePTY(terminalMarkerBytes("blocks", "command-private"))
	a.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	a.WritePTY([]byte("private result\r\n"))
	a.WritePTY(terminalMarkerBytes("blocks", "command-end:1"))
	blocksExecution(a, "public-state", "public result\r\n", "1")
	path := a.outputRecovery.path
	a.Close()
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("private-state-secret")) || !bytes.Contains(data, []byte(`"block_states"`)) {
		t.Fatalf("unsafe or missing checkpoint states: %v", err)
	}
	b, out := blocksFixture(t, "")
	if err := b.ConfigureOutputRecovery(dir, "state-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(b, "new-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	for _, text := range []string{"private result", "public result"} {
		y := terminalLineIndex(terminalScreenLines(screen), text, 0)
		if y < 0 || screen.CellAt(0, y).Content != "▌" {
			t.Fatalf("restored state missing for %q", text)
		}
	}
	if len(b.viewport.executions.retained) != 0 {
		t.Fatal("restore fabricated execution identity")
	}
	blocksExecution(b, "clear", "\x1b[H\x1b[2J\x1b[3J", "0")
	blocksPrompt(b, "cleared")
	for _, block := range b.viewport.blocks {
		if block.state != blockNeutral {
			t.Fatal("clear left stale visual state")
		}
	}
}

func TestOutputBlockStatesSmallRetentionAndNativeCompletion(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=2\n[ui.chatbox.colors]\nsession-warning='#ffaa00'\n")
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("retained-row-%02d", i))
	}
	blocksExecution(c, "truncated-output", strings.Join(lines, "\r\n")+"\r\n", "0")
	blocksPrompt(c, "latest")
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	y := terminalLineIndex(terminalScreenLines(physical), "retained-row-19", 0)
	if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 170, 0} {
		t.Fatal("retention eviction lost warning attribution")
	}
	if len(c.viewport.blocks) > 3 {
		t.Fatal("visual metadata is unbounded")
	}
	n, native := blocksFixture(t, "[ui.chatbox]\noutput-viewport='terminal'\n")
	blocksExecution(n, "native-state", "raw-native-result\r\n", "1")
	if !strings.Contains(ansi.Strip(native.String()), "▌ Failed · exit 1") {
		t.Fatal("native completion indicator missing")
	}
}

func TestOutputBlockStatesWarnWhenPaneLosesPrefixButExecutionCopyFits(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=2\n[ui.chatbox.colors]\nsession-warning='#ffaa00'\n")
	c.Resize(60, 6)
	blocksExecution(c, "fits-input", "first\r\nlast\r\n", "0")
	if c.viewport.executions.retained[0].truncated {
		t.Fatal("fixture unexpectedly lost execution-copy text")
	}
	c.Resize(60, 24)
	blocksPrompt(c, "latest-input")
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	y := terminalLineIndex(terminalScreenLines(physical), "exit 0", 0)
	if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 170, 0} {
		t.Fatal("pane-only truncation was not disclosed as a warning")
	}
}

func TestOutputBlockStatesCheckpointMetadataNeverEvictsFittingText(t *testing.T) {
	dir := t.TempDir()
	r := &outputRecovery{dir: dir, path: dir + "/state.json", owner: "fixture"}
	rows := make([]string, 10000)
	blocks := make([]checkpointBlockSpan, len(rows))
	for i := range rows {
		rows[i], blocks[i] = strings.Repeat("x", 101), checkpointBlockSpan{First: i, Last: i, State: blockError}
	}
	if err := r.writeState(outputRecoveryState{Version: 1, Owner: r.owner, Rows: rows, Blocks: blocks}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.path)
	if err != nil || len(data) > outputRecoveryByteLimit {
		t.Fatalf("checkpoint exceeded existing byte cap: %d (%v)", len(data), err)
	}
	saved, err := r.read()
	if err != nil || len(saved.Rows) != len(rows) || saved.Truncated || len(saved.Blocks) != 0 {
		t.Fatalf("cosmetic state evicted fitting transcript: rows=%d blocks=%d truncated=%t err=%v", len(saved.Rows), len(saved.Blocks), saved.Truncated, err)
	}
}

func TestOutputBlockStatesInvalidCheckpointRangesPreserveText(t *testing.T) {
	for _, blocks := range [][]checkpointBlockSpan{
		{{First: -1, Last: 0, State: blockError}}, {{First: 0, Last: 1, State: blockError}}, {{First: 0, Last: 0, State: blockState(99)}},
		{{First: 0, Last: 0, State: blockNeutral}, {First: 0, Last: 0, State: blockError}},
	} {
		path := t.TempDir() + "/state.json"
		data, err := json.Marshal(outputRecoveryState{Version: 1, Owner: "fixture", Rows: []string{"retained-public-text"}, Blocks: blocks})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		saved, err := readOutputRecoveryState(path)
		if err != nil || len(saved.Rows) != 1 || saved.Rows[0] != "retained-public-text" || len(saved.Blocks) != 0 {
			t.Fatalf("invalid display metadata affected text recovery: %+v (%v)", saved, err)
		}
	}
}

func TestOutputBlockStatesMalformedCheckpointMetadataPreservesText(t *testing.T) {
	for _, metadata := range []string{`"future"`, `[{"first":0,"last":0,"state":"future"}]`, `[{"first":false,"last":0,"state":2}]`} {
		path := t.TempDir() + "/state.json"
		data := []byte(`{"version":1,"owner":"fixture","rows":["retained-public-text"],"block_states":` + metadata + `}`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		saved, err := readOutputRecoveryState(path)
		if err != nil || len(saved.Rows) != 1 || saved.Rows[0] != "retained-public-text" || len(saved.Blocks) != 0 {
			t.Fatalf("malformed optional metadata blocked transcript recovery: %+v (%v)", saved, err)
		}
	}
}

func TestOutputBlockStatesLightThemeKeepsProgramForeground(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\n")
	theme := testInputBoxTheme()
	theme.Background = "#ffffff"
	c.SetInputBoxTheme(theme)
	blocksExecution(c, "light-command", "\x1b[38;2;0;0;255mblue-output\x1b[0m plain\r\n", "1")
	blocksPrompt(c, "next")
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	y := terminalLineIndex(terminalScreenLines(physical), "blue-output", 0)
	if y < 0 || blockTestRGB(physical.CellAt(59, y).Style.Bg) != [3]uint8{255, 204, 204} {
		t.Fatal("light error tint is not theme-aware")
	}
	if blockTestRGB(physical.CellAt(2, y).Style.Fg) != [3]uint8{0, 0, 255} {
		t.Fatal("block tint replaced program foreground")
	}
}

func TestOutputBlockStatesTintOverridesProgramBackgroundOnlyWhenDrawing(t *testing.T) {
	for _, fixture := range []struct {
		sgr, color string
		want       [3]uint8
	}{
		{"42", "#ff0000", [3]uint8{51, 0, 0}},
		{"48;5;22", "#ff0000", [3]uint8{51, 0, 0}},
		{"48;2;0;200;0", "#ff0000", [3]uint8{51, 0, 0}},
		{"48:2::0:200:0", "#ff0000", [3]uint8{51, 0, 0}},
		{"0;38;2;0;0;255;42;7", "#ff0000", [3]uint8{51, 0, 0}},
		{"42", "1", blockTestRGB(ansi.Red)},
	} {
		t.Run(fixture.color+"/"+fixture.sgr, func(t *testing.T) {
			document := "[ui.chatbox.colors]\nexit-failure='" + fixture.color + "'\n"
			if fixture.color == "1" {
				document = ""
			}
			c, out := blocksFixture(t, document)
			if fixture.color == "1" {
				cfg := config.DefaultConfig()
				cfg.UI.Palette = "terminal"
				config.ApplyVisualPolicy(cfg)
				c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
			}
			theme := testInputBoxTheme()
			theme.Background = "#000000"
			c.SetInputBoxTheme(theme)
			const link = "https://example.test/block"
			output := "\x1b[38;2;0;0;255m\x1b[" + fixture.sgr + "mANSI \x1b]8;;" + link + "\x1b\\green\x1b]8;;\x1b\\ café 日本語\x1b[0m plain\r\n"
			blocksExecution(c, "styled-failure", output, "1")
			blocksPrompt(c, "next-input")
			before := strings.Join(retainedModelRows(c.viewport.model), "\n")
			for _, row := range retainedModelRows(c.viewport.model) {
				if !strings.Contains(ansi.Strip(row), "ANSI green") {
					continue
				}
				raw := applyTerminalOutput(t, []byte(row), 60, 1).CellAt(0, 0)
				if raw == nil || raw.Content != "A" || blockTestRGB(raw.Style.Bg)[1] == 0 || (raw.Style.Attrs&uv.AttrReverse != 0) != strings.HasSuffix(fixture.sgr, ";7") {
					t.Fatal("state styling rewrote the retained program background/reverse")
				}
			}
			check := func(search bool) {
				t.Helper()
				physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
				y := terminalLineIndex(terminalScreenLines(physical), "ANSI green", 0)
				if y < 0 {
					t.Fatal("styled output is not visible")
				}
				for x := 0; x < 60; {
					cell := physical.CellAt(x, y)
					if cell == nil || cell.Width < 1 || blockTestRGB(cell.Style.Bg) != fixture.want || cell.Style.Attrs&uv.AttrReverse != 0 {
						t.Fatalf("program SGR defeated the full-width tint at column %d: %#v", x, cell)
					}
					if fixture.color == "1" && cell.Style.Bg != ansi.Red {
						t.Fatal("terminal palette was converted to a fixed RGB color")
					}
					// A wide glyph owns its continuation columns; those SDK
					// placeholders have no independently rendered style.
					x += cell.Width
				}
				cell := physical.CellAt(7, y) // first letter of the linked match
				if blockTestRGB(cell.Style.Fg) != [3]uint8{0, 0, 255} || cell.Link.URL != link {
					t.Fatal("drawing override changed program foreground or hyperlink")
				}
				if search && cell.Style.Underline != uv.UnderlineDouble {
					t.Fatal("search match lost its visible cue on the tinted surface")
				}
			}
			check(false)
			c.HandleViewportInput([]byte("\x1b[5~"), false)
			check(false)
			c.HandleViewportInput([]byte("\x1b[18~"), false)
			c.PasteOutputSearch([]byte("green"))
			check(true)
			if after := strings.Join(retainedModelRows(c.viewport.model), "\n"); after != before {
				t.Fatal("drawing normalization rewrote retained ANSI/text")
			}
			if got := c.viewport.executions.retained[0].plainText(); got != "ANSI green café 日本語 plain\n" {
				t.Fatalf("drawing normalization changed execution-copy text: %q", got)
			}
		})
	}
}

func TestOutputBlockStatesWarnWhenLaterCommandEvictsOlderPrefix(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint("restore=", restore), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n[ui.chatbox.colors]\nsession-warning='#ffaa00'\n")
			if restore {
				dir := t.TempDir()
				if err := c.ConfigureOutputRecovery(dir, "later-loss", nil); err != nil {
					t.Fatal(err)
				}
				blocksExecution(c, "older-command", "older-prefix\r\nolder-middle-1\r\nolder-middle-2\r\nolder-middle-3\r\nolder-tail\r\n", "0")
				c.Close()
				c, out = blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n[ui.chatbox.colors]\nsession-warning='#ffaa00'\n")
				if err := c.ConfigureOutputRecovery(dir, "later-loss", nil); err != nil {
					t.Fatal(err)
				}
				blocksPrompt(c, "restored-input")
			} else {
				blocksExecution(c, "older-command", "older-prefix\r\nolder-middle-1\r\nolder-middle-2\r\nolder-middle-3\r\nolder-tail\r\n", "0")
			}
			c.Resize(60, 10)
			blocksExecution(c, "neighbor-command", "neighbor-complete\r\n", "0")
			blocksPrompt(c, "latest-input")
			text := strings.Join(retainedModelRows(c.viewport.model), "\n")
			if strings.Contains(text, "older-prefix") || !strings.Contains(text, "older-tail") {
				t.Fatalf("fixture did not partially evict older output: %q", text)
			}
			for i := 0; i < 4; i++ {
				c.HandleViewportInput([]byte("\x1b[5~"), false)
			}
			out.Reset()
			c.Resize(60, 24)
			physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
			y := terminalLineIndex(terminalScreenLines(physical), "older-tail", 0)
			if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 170, 0} {
				t.Fatalf("later prefix loss did not give older suffix an amber rail: %q, spans=%+v", terminalScreenLines(physical), c.viewport.blocks)
			}
			y = terminalLineIndex(terminalScreenLines(physical), "neighbor-complete", 0)
			if y < 0 || physical.CellAt(0, y).Content == "▌" {
				t.Fatal("warning leaked into complete neighboring block")
			}
		})
	}
}

func TestOutputBlockStatesPreserveED2HistoryAndForgetED3(t *testing.T) {
	for _, erase := range []string{"\x1b[H\x1b[2J", "\x1b[H\x1b[2J\x1b[3J"} {
		t.Run(fmt.Sprintf("erase=%q", erase), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\n")
			blocksExecution(c, "failed-before-clear", "retained-failed-output\r\n", "1")
			blocksExecution(c, "clear-view", erase, "0")
			blocksPrompt(c, "next-input")
			c.HandleViewportInput([]byte("\x1b[5~"), false)
			out.Reset()
			c.Resize(60, 40)
			physical := applyTerminalOutput(t, out.Bytes(), 60, 40)
			y := terminalLineIndex(terminalScreenLines(physical), "retained-failed-output", 0)
			if strings.Contains(erase, "3J") {
				if y >= 0 {
					t.Fatal("ED3 retained old block output or state")
				}
				for _, block := range c.viewport.blocks {
					if block.state != blockNeutral {
						t.Fatal("ED3 retained old failure state")
					}
				}
			} else if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 0, 0} {
				t.Fatal("ED2 lost red styling from retained failed history")
			}
		})
	}
}

func TestOutputBlockStatesRetainFailedClearOutcome(t *testing.T) {
	for _, erase := range []string{"\x1b[H\x1b[2J", "\x1b[H\x1b[2J\x1b[3J"} {
		t.Run(fmt.Sprintf("erase=%q", erase), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\n")
			blocksExecution(c, "before-clear", "old-visible-output\r\n", "0")
			blocksExecution(c, "failed-clear", erase, "1")
			blocksPrompt(c, "next-input")
			physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
			y := terminalLineIndex(terminalScreenLines(physical), "exit 1", 0)
			if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 0, 0} {
				t.Fatal("clear erased the known-failed retained outcome's red state")
			}
			if strings.Contains(erase, "3J") && terminalContainsLine(physical, "old-visible-output") {
				t.Fatal("failed clear resurrected removed output")
			}
		})
	}
}

func TestOutputBlockStatesGutterPreservesCarriageReturnSemantics(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "progress-command", strings.Repeat("a", 59)+"\rZ\r\n", "1")
	blocksPrompt(c, "next-input")
	want := "Z" + strings.Repeat("a", 58)
	if got := (retainedExecution{rows: retainedModelRows(c.viewport.model)}).plainText(); !strings.Contains(got, want) {
		t.Fatalf("gutter changed raw progress semantics: %q", got)
	}
	if got := c.viewport.executions.retained[0].plainText(); got != want+"\n" {
		t.Fatalf("execution copy disagrees with terminal width: %q", got)
	}
	physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
	y := terminalLineIndex(terminalScreenLines(physical), "Zaaaa", 0)
	if y < 0 || physical.CellAt(2, y).Content != "Z" || physical.CellAt(59, y).Content != "a" || physical.CellAt(2, y+1).Content != "a" {
		t.Fatal("narrow drawing lost the last raw column instead of reflowing it")
	}
	c.HandleViewportInput([]byte("\x1b[18~"), false)
	c.PasteOutputSearch([]byte(want))
	if len(c.viewport.search.matches) != 1 {
		t.Fatal("search does not see the authoritative progress result")
	}
}

func TestOutputBlockStatesCompleteWhileGeometryIsSuspended(t *testing.T) {
	for _, fixture := range []struct {
		name                     string
		native, restoreBeforeEnd bool
	}{
		{"owned", false, false},
		{"native collapsed completion", true, false},
		{"native restored completion", true, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\n")
			if err := c.ConfigureOutputRecovery(dir, "suspended-state", nil); err != nil {
				t.Fatal(err)
			}
			blocksPrompt(c, "suspended-command")
			c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
			c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
			c.WritePTY([]byte("before-collapse\r\n"))
			if fixture.native {
				c.WritePTY([]byte("\x1b[2;1Hprivate-native-canvas"))
			}
			c.Resize(60, 1)
			c.WritePTY([]byte("during-collapse\r\n"))
			if fixture.restoreBeforeEnd {
				c.Resize(60, 24)
			}
			c.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 2, 0, time.UTC) }
			c.WritePTY(terminalMarkerBytes("blocks", "command-end:1"))
			c.Resize(60, 24)
			blocksPrompt(c, "next-input")
			retained := ansi.Strip(strings.Join(retainedModelRows(c.viewport.model), "\n"))
			if !strings.Contains(retained, "exit 1") || !strings.Contains(retained, "2s") || strings.Contains(retained, "private-native-canvas") {
				t.Fatalf("collapsed completion lost its outcome or retained a private canvas: %q", retained)
			}
			physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
			y := terminalLineIndex(terminalScreenLines(physical), "before-collapse", 0)
			if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 0, 0} {
				t.Fatal("completion during suspended geometry lost red state")
			}
			c.Close()
			b, restored := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\n")
			if err := b.ConfigureOutputRecovery(dir, "suspended-state", nil); err != nil {
				t.Fatal(err)
			}
			blocksPrompt(b, "restored-input")
			physical = applyTerminalOutput(t, restored.Bytes(), 60, 24)
			y = terminalLineIndex(terminalScreenLines(physical), "before-collapse", 0)
			if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 0, 0} {
				t.Fatal("checkpoint lost the suspended completion's red state")
			}
			y = terminalLineIndex(terminalScreenLines(physical), "exit 1", 0)
			if y < 0 || physical.CellAt(0, y).Content != "▌" || blockTestRGB(physical.CellAt(0, y).Style.Fg) != [3]uint8{255, 0, 0} {
				t.Fatal("checkpoint lost the suspended completion's red outcome")
			}
		})
	}
}

func TestOutputBlockStatesSuspendedCompletionKeepsBufferedTextWithItsExecution(t *testing.T) {
	c, _ := blocksFixture(t, "")
	blocksPrompt(c, "first-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("first-output\r\ntail-owned\x1b["))
	c.Resize(60, 1)
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:1"))
	first := c.viewport.executions.retained[0].plainText()
	if !strings.Contains(first, "tail-owned") || len(c.viewportControlTail) != 0 {
		t.Fatalf("completion left this execution's output buffered: %q", first)
	}
	c.Resize(60, 24)
	blocksExecution(c, "second-command", "second-output\r\n", "0")
	if got := c.viewport.executions.retained[1].plainText(); strings.Contains(got, "tail-owned") || !strings.Contains(got, "second-output") {
		t.Fatalf("previous execution's buffered output entered the next execution: %q", got)
	}
}

func TestOutputBlockStatesNativeTerminalCompletionSurvivesCollapse(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-viewport='terminal'\n")
	blocksPrompt(c, "native-terminal-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.Resize(60, 1)
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:1"))
	if !strings.Contains(ansi.Strip(out.String()), "▌ Failed · exit 1") {
		t.Fatal("native terminal completion lost its failure indicator during collapse")
	}
}

func TestOutputBlockStatesUseActualUnicodeLossNotNoticeWords(t *testing.T) {
	for _, fixture := range []struct {
		name, output, exit string
		color              [3]uint8
	}{
		{"actual loss", "before a" + strings.Repeat("\u0301", 4096) + " after\r\n", "0", [3]uint8{255, 170, 0}},
		{"failure wins", "before a" + strings.Repeat("\u0301", 4096) + " after\r\n", "1", [3]uint8{255, 0, 0}},
		{"literal notice", "before [Unicode cluster truncated] after\r\n", "0", [3]uint8{}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox.colors]\nexit-failure='#ff0000'\nsession-warning='#ffaa00'\n")
			blocksExecution(c, "cluster-command", fixture.output, fixture.exit)
			blocksPrompt(c, "next-input")
			physical := applyTerminalOutput(t, out.Bytes(), 60, 24)
			y := terminalLineIndex(terminalScreenLines(physical), "[Unicode cluster truncated] after", 0)
			if y < 0 {
				t.Fatal("fixture disclosure text is not visible")
			}
			cell := physical.CellAt(0, y)
			if fixture.color == [3]uint8{} {
				if cell.Content == "▌" {
					t.Fatal("literal notice words invented a warning")
				}
			} else if cell.Content != "▌" || blockTestRGB(cell.Style.Fg) != fixture.color {
				t.Fatal("actual loss did not reach the correct severity")
			}
		})
	}
}
