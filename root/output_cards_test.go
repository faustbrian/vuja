package root

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func cardMouse(t *testing.T, c *terminalCompositor, button, x, y int, pressed bool) {
	t.Helper()
	end := "m"
	if pressed {
		end = "M"
	}
	filter := &terminalInputFilter{}
	if got := filter.FilterInput([]byte(fmt.Sprintf("\x1b[<%d;%d;%d%s", button, x+1, y+1, end)), c, false, true); len(got) != 0 {
		t.Fatalf("mouse event reached shell: %q", got)
	}
}

func TestInteractiveBlocksNativeCardActionsCannotSubstituteOtherOutput(t *testing.T) {
	for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
		t.Run(fmt.Sprintf("action-%q", action), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out := blocksFixture(t, "")
			blocksExecution(c, "native-command", "\x1b[?1049hprivate-canvas\x1b[?1049l", "0")
			blocksExecution(c, "neighbor-command", "neighbor-output\r\n", "0")
			blocksPrompt(c, "pending-input")
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[21~\x1b[21~"), c, false, true)
			if c.viewport.cards.focus == nil || *c.viewport.cards.focus != c.viewport.cards.completed[0].first {
				t.Fatal("native card was not focused")
			}
			out.Reset()
			if got := filter.FilterInput([]byte(action), c, false, true); len(got) != 0 {
				t.Fatalf("unavailable card action reached shell: %q", got)
			}
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done != nil {
				<-done
			}
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("unavailable native card copied neighboring output")
			}
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 0 {
				t.Fatalf("unavailable native card exported other output: %v, %v", paths, err)
			}
			if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "not captured") {
				t.Fatal("unavailable card action lacked an explanation")
			}
		})
	}
}

func TestInteractiveBlocksNarrowResizePreservesUnavailableActionOwnership(t *testing.T) {
	for _, kind := range []string{"native", "mixed"} {
		for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
			t.Run(fmt.Sprintf("%s/%q", kind, action), func(t *testing.T) {
				cache := t.TempDir()
				t.Setenv("XDG_CACHE_HOME", cache)
				c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=1\n")
				if kind == "native" {
					blocksExecution(c, "unavailable", "\x1b[?1049hcanvas\x1b[?1049l", "0")
				} else {
					blocksPrompt(c, "unavailable")
					c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
					c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
					c.WritePTY([]byte("owned-output\r\n"))
					c.WriteNotification([]byte("sideband\r\n"))
					c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
				}
				blocksExecution(c, "neighbor", "other-output\r\n", "0")
				blocksPrompt(c, "input")
				filter := &terminalInputFilter{}
				filter.FilterInput([]byte("\x1b[21~\x1b[21~"), c, false, true)
				if c.viewport.cards.focus == nil || c.viewport.cards.focused != nil {
					t.Fatal("fixture must focus an uncaptured card")
				}
				c.Resize(7, 24)
				out.Reset()
				filter.FilterInput([]byte(action), c, false, true)
				c.mu.Lock()
				done := c.outputExportDone
				c.mu.Unlock()
				if done != nil {
					<-done
				}
				if strings.Contains(out.String(), "\x1b]52;") {
					t.Fatal("narrow resize made unavailable Copy select neighboring output")
				}
				paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
				if err != nil || len(paths) != 0 {
					t.Fatalf("narrow resize made unavailable Save export a different document: %v %v", paths, err)
				}
			})
		}
	}
}

func TestInteractiveBlocksNarrowResizePreservesFrozenReading(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=20\n")
	c.Resize(60, 14)
	blocksPrompt(c, "input")
	for i := 0; i < 60; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("r%02d\r\n", i)))
	}
	blocksExecution(c, "silent", "", "0")
	blocksPrompt(c, "input")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[5~"), c, false, true)
	before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
	anchor := strings.TrimSpace(before[0])
	if !strings.HasPrefix(anchor, "r") || len(anchor) != 3 {
		t.Fatalf("fixture did not browse an old numbered row: %q", before)
	}
	for i := 0; i < 80; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("n%02d\r\n", i)))
	}
	for _, width := range []int{7, 60} {
		out.Reset()
		c.Resize(width, 14)
		after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 14))
		if strings.TrimSpace(after[0]) != anchor {
			t.Fatalf("resize to %d substituted live output for frozen reading: want %q, got %q", width, anchor, after)
		}
	}
	filter.FilterInput([]byte("\x1b[F"), c, false, true)
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 14), "n79") {
		t.Fatal("End did not return from narrow-resized reading to latest")
	}
}

func TestInteractiveBlocksNarrowResizePreservesCapturedActionTarget(t *testing.T) {
	for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
		t.Run(fmt.Sprintf("%q", action), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n")
			blocksExecution(c, "selected", "selected-output\r\n", "0")
			blocksPrompt(c, "input")
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[21~"), c, false, true)
			for i := 0; i < 80; i++ {
				c.WriteNotification([]byte(fmt.Sprintf("new-tail-%03d\r\n", i)))
			}
			for _, width := range []int{8, 7, 8} {
				c.Resize(width, 24)
			}
			out.Reset()
			filter.FilterInput([]byte(action), c, false, true)
			if action == "\x1b[23~" {
				assertExecutionClipboard(t, out.String(), "selected-output\n")
				return
			}
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("selected card export did not start")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("expected one selected-card export: %v %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "selected-output\n" {
				t.Fatalf("resize changed selected-card export: %q %v", data, err)
			}
		})
	}
}

func TestInteractiveBlocksSearchPreservesSelectedActionTarget(t *testing.T) {
	for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
		t.Run(fmt.Sprintf("%q", action), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out := blocksFixture(t, "")
			blocksExecution(c, "selected-command", "selected-output\r\n", "0")
			blocksExecution(c, "neighbor-command", "neighbor-output\r\n", "0")
			blocksPrompt(c, "input")
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[21~\x1b[21~\x1b[18~"), c, false, true)
			out.Reset()
			filter.FilterInput([]byte(action), c, false, true)
			if action == "\x1b[23~" {
				assertExecutionClipboard(t, out.String(), "selected-output\n")
				return
			}
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("selected execution Save did not start after search")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("expected one selected export: %v %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "selected-output\n" {
				t.Fatalf("search replaced the selected export target: %q %v", data, err)
			}
		})
	}
}

func TestInteractiveBlocksReservedDividerURIIsApplicationOutput(t *testing.T) {
	for _, terminator := range []string{"\x1b\\", "\a"} {
		for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
			t.Run(fmt.Sprintf("%q/%q", terminator, action), func(t *testing.T) {
				cache := t.TempDir()
				t.Setenv("XDG_CACHE_HOME", cache)
				c, out := blocksFixture(t, "")
				payload := "\x1b]8;;vuja:block-divider" + terminator + "─\x1b]8;;" + terminator + "\r\nkept\r\n"
				blocksExecution(c, "linked-output", payload, "0")
				blocksPrompt(c, "input")
				lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
				y := terminalLineIndex(lines, "kept", 0)
				if y < 1 || strings.TrimSpace(lines[y-1]) != "─" {
					t.Errorf("program-owned glyph was expanded into structural chrome: %q", lines)
				}
				filter := &terminalInputFilter{}
				filter.FilterInput([]byte("\x1b[21~"), c, false, true)
				out.Reset()
				filter.FilterInput([]byte(action), c, false, true)
				if action == "\x1b[23~" {
					assertExecutionClipboard(t, out.String(), "─\nkept\n")
					return
				}
				c.mu.Lock()
				done := c.outputExportDone
				c.mu.Unlock()
				if done == nil {
					t.Fatal("application-output export did not start")
				}
				<-done
				paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
				if err != nil || len(paths) != 1 {
					t.Fatalf("expected one output export: %v %v", paths, err)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil || string(data) != "─\nkept\n" {
					t.Fatalf("program-owned linked glyph was lost: %q %v", data, err)
				}
			})
		}
	}
}

// VIB-04/08: safe checkpoint conversion must not rewrite application text.
func TestInteractiveBlocksRecoveredDividerLookalikesKeepExactOutput(t *testing.T) {
	for _, terminator := range []string{"\x1b\\", "\a"} {
		for _, styled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/styled-%t", terminator, styled), func(t *testing.T) {
				cache, dir := t.TempDir(), t.TempDir()
				t.Setenv("XDG_CACHE_HOME", cache)
				c, out := blocksFixture(t, "")
				if err := c.ConfigureOutputRecovery(dir, "indented-link", nil); err != nil {
					t.Fatal(err)
				}
				body := "  \x1b]8;;vuja:block-divider" + terminator + "─\x1b]8;;" + terminator
				if styled {
					body = "\x1b[31m" + body + "\x1b[0m"
				}
				blocksExecution(c, "program-output", body+"\r\nkept\r\n", "0")
				for generation := 0; generation < 3; generation++ {
					if generation > 0 {
						c.Close()
						c, out = blocksFixture(t, "")
						if err := c.ConfigureOutputRecovery(dir, "indented-link", nil); err != nil {
							t.Fatal(err)
						}
					}
					blocksPrompt(c, "input")
					filter := &terminalInputFilter{}
					filter.FilterInput([]byte("\x1b[21~"), c, false, true)
					out.Reset()
					filter.FilterInput([]byte("\x1b[23~"), c, false, true)
					assertExecutionClipboard(t, out.String(), "  ─\nkept\n")
					filter.FilterInput([]byte("\x1b[24~"), c, false, true)
					c.mu.Lock()
					done := c.outputExportDone
					c.mu.Unlock()
					if done == nil {
						t.Fatal("card export did not start")
					}
					<-done
					paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
					if err != nil || len(paths) != generation+1 {
						t.Fatalf("expected %d explicit exports: %v %v", generation+1, paths, err)
					}
					for _, path := range paths {
						data, err := os.ReadFile(path)
						if err != nil || string(data) != "  ─\nkept\n" {
							t.Errorf("generation %d changed complete application text: %q %v", generation, data, err)
						}
					}
				}
			})
		}
	}
}

func TestInteractiveBlocksPaneToolsDistinguishDividerOrigin(t *testing.T) {
	for _, variant := range []string{"live", "restored", "legacy cards", "unattributed"} {
		t.Run(variant, func(t *testing.T) {
			cache, dir := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			document := ""
			if variant == "unattributed" {
				document = "[ui.chatbox]\nscrollback='output'\n"
			}
			c, _ := blocksFixture(t, document)
			if err := c.ConfigureOutputRecovery(dir, "divider-origin", nil); err != nil {
				t.Fatal(err)
			}
			blocksExecution(c, "program-link", "\x1b]8;;vuja:block-divider\x1b\\─\x1b]8;;\x1b\\\r\nkept\r\n", "0")
			if variant != "live" {
				path := c.outputRecovery.path
				c.Close()
				if variant == "legacy cards" {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var state outputRecoveryState
					if err := json.Unmarshal(data, &state); err != nil {
						t.Fatal(err)
					}
					for i := range state.Blocks {
						state.Blocks[i].Divider = false
					}
					data, err = json.Marshal(state)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				c, _ = blocksFixture(t, "")
				if err := c.ConfigureOutputRecovery(dir, "divider-origin", nil); err != nil {
					t.Fatal(err)
				}
			}
			blocksPrompt(c, "input")
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[18~"), c, false, true)
			c.PasteOutputSearch([]byte("─"))
			if len(c.viewport.search.matches) != 1 {
				t.Errorf("search must find exactly the program glyph, not Vuja's rule: %d", len(c.viewport.search.matches))
			}
			filter.FilterInput([]byte("\x1b[24~"), c, false, true)
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("pane export did not start")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("expected one pane export: %v %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || strings.Count(string(data), "─") != 1 || !strings.Contains(string(data), "─\nkept\n") {
				t.Fatalf("pane export confused program text and generated decoration: %q %v", data, err)
			}
		})
	}
}

// VIB-08: stale cosmetic metadata cannot hide overwritten checkpoint text.
func TestInteractiveBlocksRestoredDividerOriginRequiresMatchingRow(t *testing.T) {
	for _, cards := range []bool{false, true} {
		t.Run(fmt.Sprintf("cards-%t", cards), func(t *testing.T) {
			cache, dir := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, _ := blocksFixture(t, "")
			if err := c.ConfigureOutputRecovery(dir, "changed-divider", nil); err != nil {
				t.Fatal(err)
			}
			blocksExecution(c, "command", "output\r\n", "0")
			path := c.outputRecovery.path
			c.Close()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var state outputRecoveryState
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Blocks) != 1 || !state.Blocks[0].Divider {
				t.Fatal("fixture lacks an attributed divider")
			}
			state.Rows[state.Blocks[0].First] = "replacement-not-decoration"
			if !cards {
				state.Blocks[0].Card = nil
			}
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			c, out := blocksFixture(t, "")
			if err := c.ConfigureOutputRecovery(dir, "changed-divider", nil); err != nil {
				t.Fatal(err)
			}
			blocksPrompt(c, "input")
			if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "replacement-not-decoration") {
				t.Error("stale divider metadata replaced real text while drawing")
			}
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[18~"), c, false, true)
			c.PasteOutputSearch([]byte("replacement-not-decoration"))
			if len(c.viewport.search.matches) != 1 {
				t.Error("stale divider metadata hid real text from search")
			}
			filter.FilterInput([]byte("\x1b[24~"), c, false, true)
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("pane export did not start")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("expected one pane export: %v %v", paths, err)
			}
			data, err = os.ReadFile(paths[0])
			if err != nil || !strings.Contains(string(data), "replacement-not-decoration\n") {
				t.Fatalf("stale divider metadata hid real text from export: %q %v", data, err)
			}
		})
	}
}

// VIB-06/07: frozen display spans must retain their logical divider identity.
func TestInteractiveBlocksFrozenWrappedTextKeepsItsOwnRows(t *testing.T) {
	c, out := blocksFixture(t, "")
	var payload strings.Builder
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&payload, "token%02d ", i)
	}
	blocksExecution(c, "first-command", payload.String()+"\r\n", "0")
	blocksExecution(c, "second-command", "second-output\r\n", "0")
	blocksPrompt(c, "input")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[18~"), c, false, true)
	c.PasteOutputSearch([]byte("token00"))
	for _, width := range []int{60, 28, 42} {
		out.Reset()
		c.Resize(width, 40)
		lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 40))
		text := strings.Join(strings.Fields(strings.Join(lines, "\n")), "")
		for i := 0; i < 24; i++ {
			if !strings.Contains(text, fmt.Sprintf("token%02d", i)) {
				t.Errorf("frozen drawing replaced program fragment token%02d at width %d: %q", i, width, lines)
			}
		}
		next := terminalLineIndex(lines, "second-command", 0)
		if next < 1 || lines[next-1] != strings.Repeat("─", width) {
			t.Errorf("attributed divider must stay immediately above its own header at width %d: %q", width, lines)
		}
	}
}

func TestInteractiveBlocksContentWidthOutsideClickKeepsHistoryOwnership(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='content-width'\nblock-max-height=2\n")
	for i := 0; i < 8; i++ {
		blocksExecution(c, fmt.Sprintf("older-%d", i), "earlier-output\r\n", "0")
	}
	blocksExecution(c, "short", "row-0\r\nrow-1\r\nrow-2\r\nrow-3\r\nrow-4\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "row-3", 0)
	if y < 0 || c.viewport.cards.frame[y].width >= 59 {
		t.Fatalf("expected narrow content-width card: %q", lines)
	}
	cardMouse(t, c, 0, 59, y, true)
	cardMouse(t, c, 0, 59, y, false)
	if c.viewport.cards.focus != nil {
		t.Fatal("empty columns outside the card activated it")
	}
	cardMouse(t, c, 64, 59, y, true)
	if c.viewport.cards.history == 0 {
		t.Fatal("outside wheel did not scroll enclosing history")
	}
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "row-0", 0) >= 0 {
		t.Fatal("outside wheel scrolled the card crop instead of enclosing history")
	}
}

func TestInteractiveBlocksKeyboardFocusFreezesOutputAcrossRetention(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n")
	c.Resize(60, 40)
	for _, name := range []string{"first", "second", "focused"} {
		blocksExecution(c, name, name+"-one\r\n"+name+"-two\r\n", "0")
	}
	blocksPrompt(c, "pending-input")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[21~"), c, false, true)
	blocksExecution(c, "new-output", "new-1\r\nnew-2\r\nnew-3\r\nnew-4\r\nnew-5\r\n", "0")
	blocksPrompt(c, "pending-input")
	out.Reset()
	filter.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "focused-one\nfocused-two\n")
}

func TestInteractiveBlocksOuterReadingSurvivesIncomingOutputAndEviction(t *testing.T) {
	for _, navigation := range []string{"page", "wheel"} {
		t.Run(navigation, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=20\n")
			c.Resize(60, 14)
			blocksPrompt(c, "input")
			for i := 0; i < 60; i++ {
				c.WriteNotification([]byte(fmt.Sprintf("reading-row-%03d café 日本語\r\n", i)))
			}
			blocksExecution(c, "silent-card", "", "0")
			blocksPrompt(c, "input")
			filter := &terminalInputFilter{}
			if navigation == "page" {
				filter.FilterInput([]byte("\x1b[5~"), c, false, true)
			} else {
				cardMouse(t, c, 64, 1, 1, true)
			}
			before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
			y, anchor := -1, ""
			for i, line := range before {
				if strings.Contains(line, "reading-row-") {
					y, anchor = i, strings.TrimSpace(line)
					break
				}
			}
			if y < 0 {
				t.Fatalf("fixture did not browse a numbered reading row: %q", before)
			}
			for _, count := range []int{3, 80} {
				for i := 0; i < count; i++ {
					c.WriteNotification([]byte(fmt.Sprintf("new-tail-%03d\r\n", i)))
				}
				after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
				if strings.TrimSpace(after[y]) != anchor {
					t.Fatalf("incoming output moved frozen reading target after %d rows: want row %d %q, got %q", count, y, anchor, after)
				}
			}
			for _, width := range []int{20, 60} {
				c.Resize(width, 14)
				reflowed := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 14))
				if !strings.Contains(reflowed[y], strings.Fields(anchor)[0]) {
					t.Fatalf("resize to %d moved the logical reading anchor: %q", width, reflowed)
				}
			}
			filter.FilterInput([]byte("\x1b[F"), c, false, true)
			latest := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
			if terminalLineIndex(latest, "new-tail-079", 0) < 0 || terminalLineIndex(latest, anchor, 0) >= 0 {
				t.Fatalf("End did not release old reading target for live output: %q", latest)
			}
		})
	}
}

func TestInteractiveBlocksFrozenHistoryBadgeReturnsToLatest(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n")
	for i := 0; i < 8; i++ {
		blocksExecution(c, fmt.Sprintf("old-command-%d", i), "old-result\r\n", "0")
	}
	blocksPrompt(c, "input")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[5~"), c, false, true)
	c.WriteNotification([]byte("latest-notice\r\n"))
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "New activity", 0)
	if y < 0 {
		t.Fatalf("frozen card history lacks a latest indicator: %q", lines)
	}
	x := strings.Index(lines[y], "New activity")
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 0, x, y, false)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "latest-notice", 0) < 0 || terminalLineIndex(lines, "New activity", 0) >= 0 {
		t.Fatalf("latest indicator did not release frozen card history: %q", lines)
	}
}

func TestInteractiveBlocksFrozenReadingReleasesAfterTypingAndEscape(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "old-command", "frozen-origin\r\n", "0")
	blocksPrompt(c, "input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "frozen-origin", 0)
	if y < 0 {
		t.Fatal("fixture output missing")
	}
	cardMouse(t, c, 0, 3, y, true)
	cardMouse(t, c, 0, 3, y, false)
	filter := &terminalInputFilter{}
	if got := filter.FilterInput([]byte("x"), c, false, true); string(got) != "x" {
		t.Fatal("typing was not forwarded to shell")
	}
	c.WriteNotification([]byte("new-live-output\r\n"))
	if !c.HandleViewportInput([]byte("\x1b"), false) {
		t.Fatal("Escape leaked to shell instead of leaving frozen card reading")
	}
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "new-live-output", 0) < 0 || terminalLineIndex(lines, "New activity", 0) >= 0 {
		t.Fatalf("Escape did not restore latest output: %q", lines)
	}
}

func TestInteractiveBlocksLatestNavigationLeavesShellEditingKeysUntouched(t *testing.T) {
	for _, key := range []string{"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[6~"} {
		t.Run(fmt.Sprintf("key-%q", key), func(t *testing.T) {
			c, _ := blocksFixture(t, "")
			blocksExecution(c, "finished-command", "result\r\n", "0")
			blocksPrompt(c, "editable-input")
			filter := &terminalInputFilter{}
			if got := filter.FilterInput([]byte(key), c, false, true); string(got) != key {
				t.Fatalf("live output with no reading target consumed shell editing key %q", key)
			}
		})
	}
}

func TestInteractiveBlocksFrozenReadingSurvivesSuspendedGeometry(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=20\n")
	c.Resize(60, 14)
	blocksPrompt(c, "long-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	for i := 0; i < 60; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("suspended-row-%03d\r\n", i)))
	}
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[5~"), c, false, true)
	before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
	anchor := strings.TrimSpace(before[0])
	if !strings.Contains(anchor, "suspended-row-") {
		t.Fatalf("fixture did not browse older running output: %q", before)
	}
	c.Resize(60, 1)
	for i := 0; i < 80; i++ {
		c.WritePTY([]byte(fmt.Sprintf("later-running-%03d\r\n", i)))
	}
	c.Resize(60, 14)
	after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
	if strings.TrimSpace(after[0]) != anchor {
		t.Fatalf("temporary geometry suspension discarded frozen reading: want %q got %q", anchor, after)
	}
}

func TestInteractiveBlocksFrozenPaneToolsUseTheReadingSnapshot(t *testing.T) {
	for _, tool := range []string{"search", "export"} {
		t.Run(tool, func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=6\n")
			blocksExecution(c, "old-command", "frozen-origin\r\n", "0")
			blocksPrompt(c, "input")
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[5~"), c, false, true)
			for i := 0; i < 80; i++ {
				c.WriteNotification([]byte(fmt.Sprintf("new-tail-%03d\r\n", i)))
			}
			if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "frozen-origin") {
				t.Fatal("fixture lost frozen reading target before tool entry")
			}
			if tool == "search" {
				filter.FilterInput([]byte("\x1b[18~"), c, false, true)
				c.PasteOutputSearch([]byte("frozen-origin"))
				if len(c.viewport.search.matches) != 1 || !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "frozen-origin") {
					t.Fatal("search replaced the frozen reading document with newer live output")
				}
				return
			}
			filter.FilterInput([]byte("\x1b[24~"), c, false, true)
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("pane export was not started")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("expected one private pane export: %v %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || !strings.Contains(string(data), "frozen-origin") || strings.Contains(string(data), "new-tail-079") {
				t.Fatalf("pane export did not use frozen reading text: %q %v", data, err)
			}
		})
	}
}

func TestInteractiveBlocksFrozenPaneExportKeepsReadingPosition(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=20\n")
	c.Resize(60, 14)
	blocksPrompt(c, "input")
	for i := 0; i < 60; i++ {
		c.WriteNotification([]byte(fmt.Sprintf("export-reading-row-%03d\r\n", i)))
	}
	blocksExecution(c, "silent", "", "0")
	blocksPrompt(c, "input")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b[5~"), c, false, true)
	before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
	anchor := strings.TrimSpace(before[0])
	if !strings.Contains(anchor, "export-reading-row-") {
		t.Fatalf("fixture did not browse a numbered row: %q", before)
	}
	filter.FilterInput([]byte("\x1b[24~"), c, false, true)
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("pane export was not started")
	}
	<-done
	after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 14))
	if strings.TrimSpace(after[0]) != anchor {
		t.Fatalf("export moved the card-history reading position: want %q got %q", anchor, after)
	}
}

func TestInteractiveBlocksWheelOutsideOutputKeepsReadingPosition(t *testing.T) {
	for _, focused := range []bool{false, true} {
		t.Run(fmt.Sprintf("focused-%t", focused), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=2\n")
			for i := 0; i < 6; i++ {
				blocksExecution(c, fmt.Sprintf("command-%d", i), "one\r\ntwo\r\nthree\r\nfour\r\n", "0")
			}
			blocksPrompt(c, "input")
			if focused {
				lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
				y := terminalLineIndex(lines, "three", 0)
				if y < 0 {
					t.Fatal("fixture card body missing")
				}
				cardMouse(t, c, 0, 3, y, true)
				cardMouse(t, c, 0, 3, y, false)
			}
			before := strings.Join(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "\n")
			for _, point := range []cardPoint{{3, 23}, {60, 1}, {3, 24}} {
				cardMouse(t, c, 64, point.x, point.y, true)
				after := strings.Join(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "\n")
				if after != before {
					t.Fatalf("wheel outside output at %+v changed visible output", point)
				}
			}
		})
	}
}

func TestInteractiveBlocksInterleavedNoticeStaysVisibleAndOutOfActions(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=1\n")
	c.Resize(60, 40)
	blocksPrompt(c, "notice-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("owned-output\r\n"))
	c.WriteNotification([]byte("[VUJA] fixture-notice\r\n"))
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 40), "fixture-notice") {
		t.Fatal("running card hid an interleaved Vuja notification")
	}
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 40))
	y := terminalLineIndex(lines, "notice-command", 0)
	x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "Copy")])
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 0, x, y, false)
	assertExecutionClipboard(t, out.String(), "owned-output\n")
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b"), c, false, true)
	filter.FlushEscape(c, false)
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	blocksExecution(c, "silent-neighbor", "", "0")
	blocksPrompt(c, "pending-input")
	filter.FilterInput([]byte("\x1b[21~\x1b[21~"), c, false, true)
	out.Reset()
	filter.FilterInput([]byte("\x1b[23~"), c, false, true)
	if strings.Contains(out.String(), "\x1b]52;") {
		t.Fatal("mixed pane text became an execution capture after eviction")
	}
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 40), "not captured") {
		t.Fatal("unavailable execution output was not explained")
	}
}

func TestInteractiveBlocksSemanticLossIsAmberBeforeAndAfterCompletion(t *testing.T) {
	for _, phase := range []string{"running", "later completion"} {
		t.Run(phase, func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=3\n[ui.chatbox.colors]\nsession-warning='#ffaa00'\nexit-failure='#ff0000'\n")
			c.Resize(60, 40)
			if phase == "running" {
				blocksPrompt(c, "loss-command")
				c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
				c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
				c.WritePTY([]byte("lost-0\r\nlost-1\r\nlost-2\r\nkept-last\r\n"))
			} else {
				blocksExecution(c, "loss-command", "lost-1\r\nlost-2\r\nkept-last\r\n", "0")
				blocksExecution(c, "later", "later-output\r\n", "0")
				blocksPrompt(c, "pending-input")
			}
			screen := applyTerminalOutput(t, out.Bytes(), 60, 40)
			y := terminalLineIndex(terminalScreenLines(screen), "kept-last", 0)
			if y < 0 || screen.CellAt(0, y).Content != "▌" {
				t.Fatal("actual semantic retention loss remained visually neutral")
			}
			if got := blockTestRGB(screen.CellAt(0, y).Style.Fg); got != [3]uint8{255, 170, 0} {
				t.Fatalf("semantic loss must be amber, not error red: %v", got)
			}
		})
	}
}

func TestInteractiveBlocksManagedSelectionPreservesHardFooterRows(t *testing.T) {
	c, out := blocksFixture(t, "[ui]\nresponsive=false\n[ui.chatbox]\nstatus-left=['duration']\nstatus-right=['exit']\n")
	c.Resize(26, 30)
	blocksPrompt(c, "bad")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("body\r\n"))
	now := c.now().Add(time.Millisecond)
	c.now = func() time.Time { return now }
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:127"))
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 26, 30))
	y := terminalLineIndex(lines, "1ms", 0)
	y2 := terminalLineIndex(lines, "exit 127 not found", y+1)
	if y < 0 || y2 != y+1 {
		t.Fatalf("expected two hard footer rows: %q", lines)
	}
	x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "1ms")])
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 32, 25, y2, true)
	cardMouse(t, c, 0, 25, y2, false)
	out.Reset()
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte{3}, c, false, true)
	assertExecutionClipboard(t, out.String(), "1ms\n"+strings.Repeat(" ", 6)+"exit 127 not found")
}

func TestInteractiveBlocksManagedSelectionPreservesBlankGapRows(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nhistory-spacing=3\n")
	blocksExecution(c, "gap-command", "body\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "exit 0", 0) + 1
	if y < 1 || y+2 >= len(c.viewport.cards.frame) {
		t.Fatalf("missing three gap rows: %q", lines)
	}
	for row := y; row <= y+2; row++ {
		if strings.TrimSpace(lines[row]) != "" {
			t.Fatalf("expected a real blank spacing row, got %q", lines[row])
		}
	}
	cardMouse(t, c, 0, 2, y, true)
	cardMouse(t, c, 32, 2, y+2, true)
	cardMouse(t, c, 0, 2, y+2, false)
	out.Reset()
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte{3}, c, false, true)
	assertExecutionClipboard(t, out.String(), "\n\n")
}

func TestInteractiveBlocksRunningCheckpointFailureNoticeRemainsVisible(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-checkpoints='running'\noutput-checkpoint-interval='5s'\n")
	c.Resize(60, 40)
	if err := c.ConfigureOutputRecovery(t.TempDir(), "running-notice-fixture", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(c.outputRecovery.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.outputRecovery.path, 0700); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(c, "running-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("owned-output\r\n"))
	c.mu.Lock()
	now := c.now().Add(5 * time.Second)
	c.now = func() time.Time { return now }
	c.mu.Unlock()
	c.RefreshRunningStatus()
	awaitCheckpointState(t, c, func(r *outputRecovery) bool { return r.failed })
	c.mu.Lock()
	defer c.mu.Unlock()
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 40), "output recovery unavailable") {
		t.Fatal("a running card hid the checkpoint writer's failure notification")
	}
}

func TestInteractiveBlocksMixedPaneFallbackIsUnavailableLiveAndRestored(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) {
			dir := t.TempDir()
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out := blocksFixture(t, "[ui.chatbox]\noutput-lines=1\n")
			c.Resize(60, 40)
			if err := c.ConfigureOutputRecovery(dir, "mixed-fixture", nil); err != nil {
				t.Fatal(err)
			}
			blocksPrompt(c, "mixed-command")
			c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
			c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
			c.WritePTY([]byte("owned-output\r\n"))
			c.WriteNotification([]byte("[VUJA] sideband-notice\r\n"))
			c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
			blocksExecution(c, "silent-neighbor", "", "0")
			if restored {
				c.Close()
				c, out = blocksFixture(t, "[ui.chatbox]\noutput-lines=1\n")
				c.Resize(60, 40)
				if err := c.ConfigureOutputRecovery(dir, "mixed-fixture", nil); err != nil {
					t.Fatal(err)
				}
			}
			blocksPrompt(c, "pending-input")
			if len(c.viewport.cards.completed) != 2 || !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 40), "sideband-notice") {
				t.Fatal("mixed-pane card or its notification disappeared")
			}
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[21~\x1b[21~"), c, false, true)
			if c.viewport.cards.focus == nil || *c.viewport.cards.focus != c.viewport.cards.completed[0].first {
				t.Fatal("mixed-pane card was not focused")
			}
			out.Reset()
			filter.FilterInput([]byte("\x1b[23~\x1b[24~"), c, false, true)
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done != nil {
				<-done
			}
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("pane-only notice entered execution Copy through numeric fallback")
			}
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 0 {
				t.Fatalf("mixed-pane card exported an ambiguous execution: %v, %v", paths, err)
			}
		})
	}
}

func TestInteractiveBlocksTextFallbackPageUpBrowsesRealOutput(t *testing.T) {
	for _, variant := range []string{"unattributed", "old checkpoint", "narrow"} {
		t.Run(variant, func(t *testing.T) {
			var payload strings.Builder
			for i := 0; i < 30; i++ {
				fmt.Fprintf(&payload, "r%02d\r\n", i)
			}
			c, out := blocksFixture(t, "")
			c.Resize(60, 14)
			width := 60
			switch variant {
			case "unattributed":
				blocksPrompt(c, "input")
				c.WriteNotification([]byte(payload.String()))
			case "old checkpoint":
				dir := t.TempDir()
				old, _ := blocksFixture(t, "[ui.chatbox]\nscrollback='output'\n")
				if err := old.ConfigureOutputRecovery(dir, "legacy-text-fixture", nil); err != nil {
					t.Fatal(err)
				}
				blocksExecution(old, "fixture", payload.String(), "0")
				old.Close()
				if err := c.ConfigureOutputRecovery(dir, "legacy-text-fixture", nil); err != nil {
					t.Fatal(err)
				}
			case "narrow":
				width = 7
				c.Resize(width, 14)
				blocksExecution(c, "x", payload.String(), "0")
			}
			blocksPrompt(c, "input")
			before := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 14))
			if terminalLineIndex(before, "r29", 0) < 0 {
				t.Fatalf("latest fixture output missing before PageUp: %q", before)
			}
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte("\x1b[5~"), c, false, true)
			after := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), width, 14))
			older := false
			for i := 0; i < 29; i++ {
				older = older || terminalLineIndex(after, fmt.Sprintf("r%02d", i), 0) >= 0
			}
			if terminalLineIndex(after, "r29", 0) >= 0 || !older {
				t.Fatalf("PageUp did not browse older text: before=%q after=%q", before, after)
			}
		})
	}
}

func TestInteractiveBlocksRestoreKeepsMiddleNoticeOutsideBodyCap(t *testing.T) {
	dir := t.TempDir()
	document := "[ui.chatbox]\nblock-max-height=2\n"
	c, out := blocksFixture(t, document)
	c.Resize(60, 40)
	if err := c.ConfigureOutputRecovery(dir, "middle-notice-fixture", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(c, "checkpoint-command")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("prefix\r\n"))
	c.WriteNotification([]byte("[VUJA] middle-notice\r\n"))
	c.WritePTY([]byte("later-1\r\nlater-2\r\nlater-3\r\nlater-4\r\n"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 40), "[VUJA] middle-notice") {
		t.Fatal("notice missing before checkpoint")
	}
	c.Close()
	r, restored := blocksFixture(t, document)
	r.Resize(60, 40)
	if err := r.ConfigureOutputRecovery(dir, "middle-notice-fixture", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(r, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, restored.Bytes(), 60, 40))
	if terminalLineIndex(lines, "[VUJA] middle-notice", 0) < 0 || terminalLineIndex(lines, "later-4", 0) < 0 {
		t.Fatalf("restoration hid an interleaved notice behind the body cap: %q", lines)
	}
}

func TestInteractiveBlocksNoticeMetadataRebasesAndRejectsInvalidRegions(t *testing.T) {
	c, _ := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(t.TempDir(), "notice-regions-fixture", nil); err != nil {
		t.Fatal(err)
	}
	original := []checkpointBlockSpan{{First: 1, Last: 6, State: blockWarning, Card: &checkpointCard{
		BodyFirst: 2, BodyEnd: 6, OutcomeFirst: 6, OutcomeEnd: 7, PaneOnly: true,
		Notices: []checkpointTextRange{{First: 3, Last: 3}},
	}}}
	clipped := checkpointBlockSuffix(original, 1, 6)
	if clipped[0].Card.Notices[0].First != 2 || original[0].Card.Notices[0].First != 3 {
		t.Fatal("suffix clipping did not rebase a private copy of notice regions")
	}
	r := c.outputRecovery
	state := outputRecoveryState{Version: 1, Owner: r.owner, Truncated: true,
		Rows: []string{"header", "prefix", "[VUJA] region-notice", "tail-1", "tail-2", "outcome"}, Blocks: clipped}
	if err := r.locked(func() error { return r.writeState(state) }); err != nil {
		t.Fatal(err)
	}
	stored, err := readOutputRecoveryState(r.path)
	if err != nil || len(stored.Blocks) != 1 || stored.Blocks[0].Card == nil || len(stored.Blocks[0].Card.Notices) != 1 {
		t.Fatalf("valid notice provenance was lost: %v", err)
	}
	region := stored.Blocks[0].Card.Notices[0]
	if region.First != 3 || region.Last != 3 || stored.Rows[region.First] != "[VUJA] region-notice" {
		t.Fatal("storage truncation disclosure shifted notice attribution onto another row")
	}
	stored.Blocks[0].Card.Notices[0].First = stored.Blocks[0].Card.BodyEnd
	stored.Blocks[0].Card.Notices[0].Last = stored.Blocks[0].Card.BodyEnd
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	invalid, err := readOutputRecoveryState(r.path)
	if err != nil || len(invalid.Blocks) != 1 || invalid.Blocks[0].Card != nil || strings.Join(invalid.Rows, "\n") != strings.Join(stored.Rows, "\n") {
		t.Fatal("invalid optional attribution either became actionable or discarded transcript text")
	}
}

func TestInteractiveBlocksRestoredPartialBodyRowDisclosesLossOnlyForItsCard(t *testing.T) {
	dir, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	seed, _ := blocksFixture(t, "")
	if err := seed.ConfigureOutputRecovery(dir, "partial-row-fixture", nil); err != nil {
		t.Fatal(err)
	}
	r := seed.outputRecovery
	state := outputRecoveryState{Version: 1, Owner: r.owner, Rows: []string{
		"huge-command", strings.Repeat("<", 180000), "exit 0", "later-command", "intact-later-output", "exit 0", "",
	}, Blocks: []checkpointBlockSpan{
		{First: 0, Last: 2, Card: &checkpointCard{BodyFirst: 1, BodyEnd: 2, OutcomeFirst: 2, OutcomeEnd: 3, Newline: true}},
		{First: 3, Last: 5, Card: &checkpointCard{BodyFirst: 4, BodyEnd: 5, OutcomeFirst: 5, OutcomeEnd: 6, Newline: true}},
	}}
	if err := r.locked(func() error { return r.writeState(state) }); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	c, out := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(dir, "partial-row-fixture", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(c, "input")
	filter := &terminalInputFilter{}
	export := func() {
		t.Helper()
		filter.FilterInput([]byte("\x1b[24~"), c, false, true)
		c.mu.Lock()
		done := c.outputExportDone
		c.mu.Unlock()
		if done == nil {
			t.Fatal("card export was not started")
		}
		<-done
	}
	filter.FilterInput([]byte("\x1b[21~"), c, false, true)
	export()
	filter.FilterInput([]byte("\x1b[21~"), c, false, true)
	out.Reset()
	filter.FilterInput([]byte("\x1b[23~"), c, false, true)
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), "Output truncated") {
		t.Error("partially retained body row copied without loss feedback")
	}
	export()
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("expected two explicit private card exports: %v %v", paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "intact-later-output") {
			if strings.HasPrefix(string(data), "[VUJA: retained output truncated]\n") {
				t.Error("unrelated intact card falsely marked truncated")
			}
		} else if !strings.HasPrefix(string(data), "[VUJA: retained output truncated]\n") || !strings.Contains(string(data), "<<<<") {
			t.Errorf("partial card export lacks loss disclosure (bytes=%d)", len(data))
		}
	}
}

func TestOutputCheckpointPartialRowMetadataSeparatesBodyAndPresentationLoss(t *testing.T) {
	for _, variant := range []string{"body", "failed body", "header", "styling only"} {
		t.Run(variant, func(t *testing.T) {
			original := []string{"header", "0123456789", "outcome", "later-header", "intact", "later-outcome"}
			rows := append([]string(nil), original...)
			state, wantState, wantBodyLoss := blockNeutral, blockWarning, false
			switch variant {
			case "body", "failed body":
				rows = append([]string{"56789"}, original[2:]...)
				wantBodyLoss = true
				if variant == "failed body" {
					state, wantState = blockError, blockError
				}
			case "header":
				rows[0] = "ader"
			case "styling only":
				original[0] = "\x1b[31mheader\x1b[0m"
				wantState = blockNeutral
			}
			blocks := []checkpointBlockSpan{
				{First: 0, Last: 2, State: state, Card: &checkpointCard{BodyFirst: 1, BodyEnd: 2}},
				{First: 3, Last: 5, Card: &checkpointCard{BodyFirst: 4, BodyEnd: 5}},
			}
			kept := checkpointBlocksForRows(blocks, original, rows)
			if len(kept) != 2 || kept[0].State != wantState || kept[0].Card.Truncated != wantBodyLoss {
				t.Fatalf("wrong row-local loss provenance: %+v", kept)
			}
			if kept[1].State != blockNeutral || kept[1].Card.Truncated || blocks[0].Card.Truncated || blocks[0].State != state {
				t.Fatal("cropping mutated original metadata or contaminated an intact neighbor")
			}
		})
	}
}

// VIB-03: focused wheel does not move its neighbor; keyboard can navigate the
// same cards, rather than switching to an unrelated full-pane output snapshot.
func TestInteractiveBlocksFocusScrollAndKeyboardKeepCardActions(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=2\noutput-mouse='interact'\n")
	c.Resize(60, 40)
	for _, name := range []string{"alpha", "beta"} {
		var payload strings.Builder
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&payload, "%s-%02d\r\n", name, i)
		}
		blocksExecution(c, name+"-command", payload.String(), "0")
	}
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 40))
	y := terminalLineIndex(lines, "alpha-04", 0)
	if y < 0 {
		t.Fatalf("missing alpha card: %q", lines)
	}
	cardMouse(t, c, 0, 4, y, true)
	cardMouse(t, c, 0, 4, y, false)
	cardMouse(t, c, 64, 4, y, true)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 40))
	if terminalLineIndex(lines, "alpha-01", 0) < 0 || terminalLineIndex(lines, "beta-04", 0) < 0 || terminalLineIndex(lines, "alpha-04", 0) >= 0 {
		t.Fatalf("focused wheel moved wrong content: %q", lines)
	}
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte("\x1b"), c, false, true)
	filter.FlushEscape(c, false)
	filter.FilterInput([]byte("\x1b[21~"), c, false, true)
	filter.FilterInput([]byte("\x1b[5~"), c, false, true)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 40))
	if terminalLineIndex(lines, "beta-command", 0) < 0 || terminalLineIndex(lines, "beta-00", 0) < 0 || terminalLineIndex(lines, "Copy", 0) < 0 || terminalLineIndex(lines, "pending-input", 0) < 0 {
		t.Fatalf("keyboard navigation left the card surface: %q", lines)
	}
}

// VIB-04/05: Save is per-card, complete and private even after display clipping.
func TestInteractiveBlocksSaveTargetsCompleteCardAndKeepsPrivateModes(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=1\noutput-mouse='interact'\n")
	blocksExecution(c, "save-command", "first\r\n\r\nlast café 日本語\r\n", "0")
	blocksExecution(c, "neighbor", "wrong-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "save-command", 0)
	if y < 0 {
		t.Fatalf("missing save target: %q", lines)
	}
	i := strings.Index(lines[y], "Save")
	if i < 0 {
		t.Fatalf("missing Save control: %q", lines[y])
	}
	x := ansi.StringWidth(lines[y][:i])
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 0, x, y, false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 1 {
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "first\n\nlast café 日本語\n" {
				t.Fatalf("Save used visible crop/neighbor: %q, %v", data, err)
			}
			for path, mode := range map[string]os.FileMode{paths[0]: 0600, filepath.Dir(paths[0]): 0700} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("wrong private mode for %s: %v", filepath.Base(path), err)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Save did not publish a private export")
}

// VIB-06/08: card layout cannot discard output outside an execution capture.
func TestInteractiveBlocksKeepUnattributedOutput(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksPrompt(c, "pending-input")
	c.WriteNotification([]byte("unattributed-before\r\n"))
	blocksExecution(c, "owned-command", "owned-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	c.WriteNotification([]byte("unattributed-after\r\n"))
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	for _, want := range []string{"unattributed-before", "owned-output", "unattributed-after", "pending-input"} {
		if terminalLineIndex(lines, want, 0) < 0 {
			t.Fatalf("card projection discarded %q: %q", want, lines)
		}
	}
}

// VIB-03/07: stale mouse releases after resize/clear cannot activate an action.
func TestInteractiveBlocksInvalidatePointerTargetsOnResizeAndClear(t *testing.T) {
	for _, transition := range []string{"resize", "clear"} {
		t.Run(transition, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			blocksExecution(c, "stale-command", "old-output\r\n", "0")
			blocksPrompt(c, "pending-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "stale-command", 0)
			i := strings.Index(lines[y], "Copy")
			x := ansi.StringWidth(lines[y][:i])
			cardMouse(t, c, 0, x, y, true)
			if transition == "resize" {
				c.Resize(45, 24)
			} else {
				c.mu.Lock()
				c.clearOutputViewport(3)
				c.renderOutputViewport()
				c.mu.Unlock()
			}
			out.Reset()
			cardMouse(t, c, 0, x, y, false)
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("stale press survived a geometry/history transition")
			}
		})
	}
}

// VIB-05: a copy request is visible, but does not claim terminal permission.
func TestInteractiveBlocksCopyShowsTruthfulFeedback(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "copy-command", "copy-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "copy-command", 0)
	i := strings.Index(lines[y], "Copy")
	x := ansi.StringWidth(lines[y][:i])
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 0, x, y, false)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "Copy requested", 0) < 0 {
		t.Fatalf("missing truthful clipboard feedback: %q", lines)
	}
}

// VIB-06: selecting part of a double-width glyph includes that glyph rather
// than silently replacing it with a space or breaking its UTF-8 encoding.
func TestInteractiveBlocksSelectionUsesWholeWideGraphemes(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "unicode-command", "ab日本語cd\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "ab日本語cd", 0)
	i := strings.Index(lines[y], "日")
	x := ansi.StringWidth(lines[y][:i])
	cardMouse(t, c, 0, x+1, y, true)
	cardMouse(t, c, 32, x+2, y, true)
	cardMouse(t, c, 0, x+2, y, false)
	out.Reset()
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte{3}, c, false, true)
	assertExecutionClipboard(t, out.String(), "日本")
}

// VIB-08: restored cards get text-only actions from numeric output boundaries,
// never by parsing command-looking text or resurrecting execution identities.
func TestInteractiveBlocksRestoreCopyTargetsWithoutExecutionIdentity(t *testing.T) {
	dir := t.TempDir()
	c, _ := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(dir, "cards-fixture-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksExecution(c, "restored-command", "restored-first\r\nrestored-last café 日本語\r\n", "1")
	c.Close()
	r, out := blocksFixture(t, "")
	if err := r.ConfigureOutputRecovery(dir, "cards-fixture-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(r, "new-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "restored-command", 0)
	if y < 0 {
		t.Fatalf("restored card label absent: %q", lines)
	}
	i := strings.Index(lines[y], "Copy")
	if i < 0 {
		t.Fatalf("restored text lacks its own Copy action: %q", lines)
	}
	x := ansi.StringWidth(lines[y][:i])
	cardMouse(t, r, 0, x, y, true)
	cardMouse(t, r, 0, x, y, false)
	assertExecutionClipboard(t, out.String(), "restored-first\nrestored-last café 日本語\n")
	if len(r.viewport.executions.retained) != 0 {
		t.Fatal("display recovery fabricated a historical execution")
	}
}

func TestInteractiveBlocksFooterSurvivesNarrowResizeAndContentWidth(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='content-width'\n")
	blocksExecution(c, "short", "small\r\n", "127")
	blocksPrompt(c, "pending-input")
	c.Resize(42, 24)
	screen := applyTerminalOutput(t, out.Bytes(), 42, 24)
	lines := terminalScreenLines(screen)
	if terminalLineIndex(lines, "exit 127", 0) < 0 {
		t.Fatalf("narrow resize cropped outcome: %q", lines)
	}
	y := terminalLineIndex(lines, "small", 0)
	if y < 0 || screen.CellAt(41, y).Style.Bg != nil {
		t.Fatal("content-width block painted outside its own content surface")
	}
}

func TestInteractiveBlocksInputSurfaceCanStillBeDragSelected(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "some-command", "some-output\r\n", "0")
	blocksPrompt(c, "input-to-select")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "input-to-select", 0)
	x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "input-to-select")])
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 32, x+14, y, true)
	cardMouse(t, c, 0, x+14, y, false)
	out.Reset()
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte{3}, c, false, true)
	assertExecutionClipboard(t, out.String(), "input-to-select")
}

func TestInteractiveBlocksDeselectRepaintsFixedInput(t *testing.T) {
	for _, action := range []string{"wheel", "new click"} {
		t.Run(action, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			blocksExecution(c, "some-command", "some-output\r\n", "0")
			blocksPrompt(c, "input-to-select")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "input-to-select", 0)
			if y < 0 {
				t.Fatal("fixture input absent")
			}
			x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "input-to-select")])
			cardMouse(t, c, 0, x, y, true)
			cardMouse(t, c, 32, x+14, y, true)
			cardMouse(t, c, 0, x+14, y, false)
			selected := applyTerminalOutput(t, out.Bytes(), 60, 24)
			if selected.CellAt(x, y).Style.Attrs&uv.AttrReverse == 0 {
				t.Fatal("fixture did not visually select input text")
			}
			if action == "wheel" {
				cardMouse(t, c, 64, 1, 0, true)
			} else {
				cardMouse(t, c, 0, 1, 0, true)
				cardMouse(t, c, 0, 1, 0, false)
			}
			cleared := applyTerminalOutput(t, out.Bytes(), 60, 24)
			if cleared.CellAt(x, y).Style.Attrs&uv.AttrReverse != 0 || !strings.Contains(terminalScreenLines(cleared)[y], "input-to-select") {
				t.Error("deselection left stale reversed input cells or changed its text")
			}
			out.Reset()
			filter := &terminalInputFilter{}
			if got := filter.FilterInput([]byte{3}, c, false, true); string(got) != "\x03" || strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("cleared selection did not preserve ordinary Ctrl+C interrupt")
			}
		})
	}
}

func TestInteractiveBlocksKeyboardRevealsOffscreenActiveCard(t *testing.T) {
	c, out := blocksFixture(t, "")
	for i := 0; i < 8; i++ {
		blocksExecution(c, fmt.Sprintf("command-%02d", i), fmt.Sprintf("output-%02d\r\n", i), "0")
	}
	blocksPrompt(c, "pending-input")
	filter := &terminalInputFilter{}
	for i := 0; i < 8; i++ {
		filter.FilterInput([]byte("\x1b[21~"), c, false, true)
	}
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "command-00", 0) < 0 || terminalLineIndex(lines, "output-00", 0) < 0 {
		t.Fatalf("keyboard focused an invisible card: %q", lines)
	}
	out.Reset()
	filter.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "output-00\n")
}

func TestInteractiveBlocksWheelClampsWithoutOverscrollDebt(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=2\n")
	var payload strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&payload, "clamped-%02d\r\n", i)
	}
	blocksExecution(c, "clamped-command", payload.String(), "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "clamped-06", 0)
	cardMouse(t, c, 0, 4, y, true)
	cardMouse(t, c, 0, 4, y, false)
	for i := 0; i < 20; i++ {
		cardMouse(t, c, 65, 4, y, true)
	}
	cardMouse(t, c, 64, 4, y, true)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "clamped-03", 0) < 0 {
		t.Fatalf("overscroll debt swallowed the first upward wheel: %q", lines)
	}
}

func TestInteractiveBlocksTypingReturnsWheelOwnershipToHistory(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=2\n")
	blocksExecution(c, "typed-command", "typed-0\r\ntyped-1\r\ntyped-2\r\ntyped-3\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "typed-2", 0)
	cardMouse(t, c, 0, 4, y, true)
	cardMouse(t, c, 0, 4, y, false)
	filter := &terminalInputFilter{}
	if got := filter.FilterInput([]byte("x"), c, false, true); string(got) != "x" {
		t.Fatal("typing in shell was consumed by card focus")
	}
	cardMouse(t, c, 64, 4, y, true)
	lines = terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	if terminalLineIndex(lines, "typed-2", 0) < 0 || terminalLineIndex(lines, "typed-0", 0) >= 0 {
		t.Fatalf("typing left a card owning subsequent wheel input: %q", lines)
	}
}

func TestInteractiveBlocksEvictedHeaderKeepsCappedOutputAndWarning(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=2\noutput-lines=3\n")
	var payload strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&payload, "headless-%02d\r\n", i)
	}
	blocksExecution(c, "evicted-header", payload.String(), "0")
	blocksPrompt(c, "pending-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	count := 0
	for _, line := range lines {
		if strings.Contains(line, "headless-") {
			count++
		}
	}
	y := terminalLineIndex(lines, "headless-39", 0)
	if count != 2 || y < 0 || screen.CellAt(0, y).Content != "▌" || terminalLineIndex(lines, "Retained output", 0) < 0 {
		t.Fatalf("prefix eviction abandoned compact block rendering: %q", lines)
	}
}

func TestInteractiveBlocksMixedCtrlCCopiesThenForwardsFollowingInput(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "mixed-command", "copy-this\r\n", "0")
	blocksPrompt(c, "pending-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "copy-this", 0)
	cardMouse(t, c, 0, 2, y, true)
	cardMouse(t, c, 32, 10, y, true)
	cardMouse(t, c, 0, 10, y, false)
	out.Reset()
	filter := &terminalInputFilter{}
	if got := filter.FilterInput([]byte("\x03next"), c, false, true); string(got) != "next" {
		t.Fatalf("mixed clipboard shortcut reached shell or lost typing: %q", got)
	}
	assertExecutionClipboard(t, out.String(), "copy-this")
}

func TestInteractiveBlocksUnknownCheckpointFieldsDoNotDiscardDisplayText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	data := `{"version":1,"owner":"fixture","rows":["kept"],"block_states":[{"first":0,"last":0,"state":2,"card":{"body_first":"future","body_end":1}}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := readOutputRecoveryState(path)
	if err != nil || len(saved.Rows) != 1 || saved.Rows[0] != "kept" {
		t.Fatalf("invalid optional layout discarded valid text: %+v, %v", saved, err)
	}
}

func TestInteractiveBlocksRestoredSeverityHonorsOptOut(t *testing.T) {
	dir := t.TempDir()
	c, _ := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(dir, "state-opt-out-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksExecution(c, "old-failure", "old-output\r\n", "1")
	c.Close()
	r, out := blocksFixture(t, "[ui.chatbox]\nblock-states=false\n")
	if err := r.ConfigureOutputRecovery(dir, "state-opt-out-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksPrompt(r, "new-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	y := terminalLineIndex(lines, "old-output", 0)
	if y < 0 || screen.CellAt(0, y).Content == "▌" || screen.CellAt(59, y).Style.Bg != nil {
		t.Fatalf("restored card bypassed styling opt-out: %q", lines)
	}
}

func TestInteractiveBlocksMultilineInputSelectionKeepsLineBreaks(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "previous-command", "previous-output\r\n", "0")
	blocksPrompt(c, "first-input\r\nsecond-input")
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "first-input", 0)
	y2 := terminalLineIndex(lines, "second-input", 0)
	if y < 0 || y2 != y+1 {
		t.Fatalf("invalid multiline fixture: %q", lines)
	}
	x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "first-input")])
	x2 := ansi.StringWidth(lines[y2][:strings.Index(lines[y2], "second-input")]) + len("second-input") - 1
	cardMouse(t, c, 0, x, y, true)
	cardMouse(t, c, 32, x2, y2, true)
	cardMouse(t, c, 0, x2, y2, false)
	out.Reset()
	filter := &terminalInputFilter{}
	filter.FilterInput([]byte{3}, c, false, true)
	assertExecutionClipboard(t, out.String(), "first-input"+strings.Repeat(" ", 45)+"\n  second-input")
}

// VIB-01/09: a divider is structural chrome, not part of a failed surface.
func TestInteractiveBlocksCompactBoundaryGeometry(t *testing.T) {
	for _, variant := range []string{"live", "resized", "restored"} {
		for _, gap := range []int{-1, 0, 1, 3} {
			t.Run(fmt.Sprintf("%s/gap-%d", variant, gap), func(t *testing.T) {
				document := ""
				wantGap := max(gap, 0)
				if gap >= 0 {
					document = fmt.Sprintf("[ui.chatbox]\nhistory-spacing=%d\n", gap)
				}
				c, out := blocksFixture(t, document)
				dir := t.TempDir()
				if variant == "restored" {
					if err := c.ConfigureOutputRecovery(dir, "compact-boundary", nil); err != nil {
						t.Fatal(err)
					}
				}
				blocksExecution(c, "first-card", "first-output\r\n", "1")
				blocksExecution(c, "second-card", "second-output\r\n", "0")
				blocksPrompt(c, "input")
				width, height := 60, 24
				if variant == "resized" {
					width, height = 42, 28
					out.Reset()
					c.Resize(width, height)
				} else if variant == "restored" {
					c.Close()
					c, out = blocksFixture(t, document)
					if err := c.ConfigureOutputRecovery(dir, "compact-boundary", nil); err != nil {
						t.Fatal(err)
					}
					blocksPrompt(c, "input")
				}
				screen := applyTerminalOutput(t, out.Bytes(), width, height)
				lines := terminalScreenLines(screen)
				previous := terminalLineIndex(lines, "exit 1", 0)
				next := terminalLineIndex(lines, "second-card", 0)
				if previous < 0 || next != previous+wantGap+1 {
					t.Fatalf("want only %d explicit blank rows without divider text rows, got %q", wantGap, lines)
				}
				for x := 0; x < width; x++ {
					cell := screen.CellAt(x, previous)
					if cell == nil || cell.Style.Underline != uv.UnderlineNone {
						t.Fatalf("pixel boundaries must not underline the preceding footer; column %d: %#v", x, cell)
					}
				}
				if strings.Contains(out.String(), "\x1b[48;2;15;17;22m") {
					t.Fatal("card projection must not add a shaded separator band")
				}
			})
		}
	}
}

func TestInteractiveBlocksBoundaryDoesNotInsertAShadedTextBand(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksExecution(c, "bad-command", "bad-output\r\n", "1")
	blocksExecution(c, "next-command", "next-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	y := terminalLineIndex(lines, "exit 1", 0)
	if y < 0 {
		t.Fatal("missing block boundary")
	}
	if next := terminalLineIndex(lines, "next-command", 0); next != y+1 {
		t.Fatalf("boundary inserted a text/background band: %q", lines)
	}
	for x := 0; x < 60; x++ {
		cell := screen.CellAt(x, y+1)
		if cell == nil || cell.Style.Bg != nil || cell.Style.Underline != uv.UnderlineNone {
			t.Fatalf("normal next header inherited a separator band/underline at column %d: %#v", x, cell)
		}
	}
	command := terminalLineIndex(lines, "bad-command", 0)
	result := terminalLineIndex(lines, "bad-output", 0)
	if result != command+1 {
		t.Fatalf("short card acquired internal padding: %q", lines)
	}
}

func TestInteractiveBlocksSelectionBesideDividerCopiesOnlyVisibleText(t *testing.T) {
	for _, last := range []int{3, 14} {
		t.Run(fmt.Sprint(last+1), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\ncompleted-command='command'\n")
			blocksExecution(c, "command", "border-output\r\n", "0")
			blocksPrompt(c, "input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "border-output", 0)
			if y < 0 {
				t.Fatal("border-bearing output row missing before selection")
			}
			cardMouse(t, c, 0, 2, y, true)
			cardMouse(t, c, 32, last, y, true)
			cardMouse(t, c, 0, last, y, false)
			screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
			if screen.CellAt(2, y).Style.Attrs&uv.AttrReverse == 0 || screen.CellAt(last, y).Style.Attrs&uv.AttrReverse == 0 {
				t.Error("selected border-bearing text is not visibly selected")
			}
			if screen.CellAt(59, y).Style.Underline != uv.UnderlineNone || screenLine(screen, y+1) == strings.Repeat("─", 60) {
				t.Error("selection must not acquire a text-row divider or underline")
			}
			if last < 59 && screen.CellAt(last+1, y).Style.Attrs&uv.AttrReverse != 0 {
				t.Error("rule highlight extends beyond the selected columns")
			}
			out.Reset()
			filter := &terminalInputFilter{}
			if got := filter.FilterInput([]byte{3}, c, false, true); len(got) != 0 {
				t.Error("copying selected rule text interrupted the shell")
			}
			assertExecutionClipboard(t, out.String(), "border-output"[:last-1])
		})
	}
}

func TestInteractiveBlocksDividerFollowsLastVisibleCardRow(t *testing.T) {
	for _, mode := range []string{"command", "snapshot"} {
		for _, kind := range []string{"silent", "body", "native"} {
			for _, dividers := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/%t", mode, kind, dividers), func(t *testing.T) {
					c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\ncompleted-command=%q\nblock-dividers=%t\n", mode, dividers))
					output := ""
					if kind == "body" {
						output = "edge-body\r\n"
					} else if kind == "native" {
						output = "\x1b[?1049hprivate-canvas\x1b[?1049l"
					}
					blocksExecution(c, "edge-command", output, "0")
					blocksPrompt(c, "pending-input")
					screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
					lines := terminalScreenLines(screen)
					last := "edge-command"
					if mode == "snapshot" {
						last = "exit 0"
					} else if kind == "body" {
						last = "edge-body"
					}
					y := terminalLineIndex(lines, last, 0)
					if y < 0 {
						t.Fatalf("missing last card row %q: %q", last, lines)
					}
					if blockEdgeCount(screen) != 0 {
						t.Fatalf("pixel edges must not be glyph rows: %q", lines)
					}
					for x := 0; x < 60; x++ {
						if screen.CellAt(x, y).Style.Underline != uv.UnderlineNone {
							t.Fatalf("card text is underlined at column %d", x)
						}
					}
				})
			}
		}
	}
}

// VIB-02/04: display height cannot become a retention/copy limit.
func TestInteractiveBlocksCapOutputAndCopyCompleteHiddenContent(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nblock-max-height=3\noutput-mouse='interact'\n")
	var payload strings.Builder
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&payload, "body-%02d café 日本語\r\n", i)
	}
	blocksExecution(c, "long-command", payload.String(), "0")
	blocksPrompt(c, "pending-input")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 24)
	lines := terminalScreenLines(screen)
	visible := 0
	for _, line := range lines {
		if strings.Contains(line, "body-") {
			visible++
		}
	}
	if visible != 3 {
		t.Fatalf("want exactly three output rows, got %d: %q", visible, lines)
	}
	y := terminalLineIndex(lines, "long-command", 0)
	if y < 0 {
		t.Fatalf("card header missing: %q", lines)
	}
	x := strings.Index(lines[y], "Copy")
	if x < 0 {
		t.Fatalf("missing per-card Copy action: %q", lines[y])
	}
	x = ansi.StringWidth(lines[y][:x])
	filter := &terminalInputFilter{}
	for _, event := range []string{fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1), fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1)} {
		if got := filter.FilterInput([]byte(event), c, false, true); len(got) > 0 {
			t.Fatalf("card action leaked to shell: %q", got)
		}
	}
	want := strings.ReplaceAll(payload.String(), "\r\n", "\n")
	assertExecutionClipboard(t, out.String(), want)
}

// VIB-03/06: ordinary drag is managed without a modifier; Ctrl+C has its
// normal shell meaning when no selected text exists.
func TestInteractiveBlocksPlainDragCopiesWithoutInterruptingTheShell(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\noutput-mouse='interact'\n")
	blocksExecution(c, "selection-command", "select-me café 日本語\r\n", "0")
	blocksPrompt(c, "pending-input")
	filter := &terminalInputFilter{}
	if got := filter.FilterInput([]byte{3}, c, false, true); string(got) != "\x03" {
		t.Fatal("Ctrl+C without selection no longer interrupts")
	}
	lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
	y := terminalLineIndex(lines, "select-me", 0)
	if y < 0 {
		t.Fatalf("missing selection text: %q", lines)
	}
	x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "select-me")])
	for _, event := range []string{
		fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1),
		fmt.Sprintf("\x1b[<32;%d;%dM", x+9, y+1),
		fmt.Sprintf("\x1b[<0;%d;%dm", x+9, y+1),
	} {
		filter.FilterInput([]byte(event), c, false, true)
	}
	out.Reset()
	if got := filter.FilterInput([]byte{3}, c, false, true); len(got) > 0 {
		t.Fatalf("copying drag selection interrupted shell: %q", got)
	}
	assertExecutionClipboard(t, out.String(), "select-me")
	if strings.Contains(ansi.Strip(out.String()), "[private command]") {
		t.Fatal("selection replaced the shell's input")
	}
}
