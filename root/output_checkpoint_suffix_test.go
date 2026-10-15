package root

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// A logical line can fit live retention while its styled or JSON-escaped
// checkpoint representation exceeds the independent disk budget.
func TestOutputCheckpointKeepsNewestSuffixOfOversizedLogicalLine(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		text      string
		shortened bool
	}{
		{"escaped", strings.Repeat(`"`, 600*1024), true},
		{"styled", strings.Repeat("\x1b[31mx\x1b[32my", 80000), false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			c, _ := recoveryCompositor(t, dir, "suffix-pane", "suffix", nil)
			const sentinel = "TAIL_SENTINEL café 日本語 👩🏽‍💻"
			c.mu.Lock()
			c.viewport.write([]byte("old-prefix-" + fixture.text + "\x1b[0m" + sentinel + "\r\n"))
			live := ansi.Strip(strings.Join(retainedModelRows(c.viewport.model), "\n"))
			if !strings.Contains(live, sentinel) {
				c.mu.Unlock()
				t.Fatal("fixture did not retain the newest text before checkpointing")
			}
			c.checkpointOutputRecovery()
			c.mu.Unlock()
			c.Close() // drain only the private checkpoint worker
			state, err := c.outputRecovery.read()
			if err != nil {
				t.Fatal(err)
			}
			saved := ansi.Strip(strings.Join(state.Rows, "\n"))
			if !strings.Contains(saved, sentinel) {
				t.Fatal("checkpoint discarded the newest suffix of a retained logical line")
			}
			if strings.Contains(saved, "checkpoint output truncated") != fixture.shortened {
				t.Fatal("checkpoint disclosure did not distinguish text loss from removed styling")
			}
			if state.Truncated != fixture.shortened {
				t.Fatal("checkpoint metadata lost its text-loss provenance")
			}
			if !fixture.shortened && saved != live {
				t.Fatal("removing oversized styling changed the checkpoint's plain text")
			}
			if !utf8.ValidString(saved) || len(saved) < 1000 {
				t.Fatal("checkpoint did not retain a useful, valid Unicode suffix")
			}
			info, err := os.Stat(c.outputRecovery.path)
			if err != nil || info.Size() > outputRecoveryByteLimit || info.Mode().Perm() != 0o600 {
				t.Fatal("suffix checkpoint exceeded its private file boundary")
			}
			reopened, _ := recoveryCompositor(t, dir, "suffix-pane", "reopened", nil)
			restored := ansi.Strip(strings.Join(retainedModelRows(reopened.viewport.model), "\n"))
			if !strings.Contains(restored, sentinel) || strings.Contains(restored, "checkpoint output truncated") != fixture.shortened {
				t.Fatal("reopening lost the newest suffix or its truncation disclosure")
			}
		})
	}
}

func TestCheckpointSuffixBudgetMatchesActualJSONEncoding(t *testing.T) {
	text := "head\x01\b\f\n\r\t\"\\<&>\u2028\u2029café日本👩🏽‍💻tail"
	full, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	for budget := 2; budget <= len(full)+1; budget++ {
		suffix := checkpointStringSuffix(text, budget)
		encoded, err := json.Marshal(suffix)
		if err != nil || len(encoded) > budget || !utf8.ValidString(suffix) || !strings.HasSuffix(text, suffix) {
			t.Fatalf("budget %d did not yield an encodable Unicode suffix", budget)
		}
		if budget >= len(full) && suffix != text {
			t.Fatal("fitting text was shortened")
		}
	}
}

func TestOutputCheckpointRowLimitDisclosureSurvivesReclaimAndDisable(t *testing.T) {
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "row-limit-pane", "row-limit", nil)
	r := c.outputRecovery
	rows := make([]string, 10001)
	rows[len(rows)-1] = "newest-row"
	if err := r.locked(func() error { return r.write(rows) }); err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, action := range []func() error{
		func() error {
			reopened, _ := recoveryCompositor(t, dir, "row-limit-pane", "reclaim", nil)
			reopened.Close()
			return nil
		},
		func() error { return fenceDisabledOutputRecovery(dir, "row-limit-pane") },
	} {
		if err := action(); err != nil {
			t.Fatal(err)
		}
		state, err := r.read()
		if err != nil || len(state.Rows) != 10000 || !state.Truncated {
			t.Fatal("checkpoint rewrite broke the readable row limit")
		}
		if state.Rows[0] != "[VUJA] checkpoint output truncated" || state.Rows[len(state.Rows)-1] != "newest-row" {
			t.Fatal("checkpoint rewrite lost newest text or its disclosure")
		}
	}
}

func TestOutputCheckpointFirstPromptDisclosesSmallerRestoreLimit(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	dir := t.TempDir()
	previous, _ := recoveryCompositor(t, dir, "smaller-pane", "previous", nil)
	rows := []string{"[VUJA] checkpoint output truncated"}
	for i := 0; i < 40; i++ {
		rows = append(rows, "previous output")
	}
	rows = append(rows, "newest-restored-sentinel")
	if err := previous.outputRecovery.locked(func() error { return previous.outputRecovery.write(rows) }); err != nil {
		t.Fatal(err)
	}
	previous.Close()
	reopen := func(marker string) (*terminalCompositor, *bytes.Buffer) {
		out := new(bytes.Buffer)
		c := newTerminalCompositor(out, "bottom", marker, 80, 14)
		t.Cleanup(c.Close)
		c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 2})
		if err := c.ConfigureOutputRecovery(dir, "smaller-pane", nil); err != nil {
			t.Fatal(err)
		}
		c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
		c.WritePTY([]byte("› "))
		c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
		return c, out
	}
	restored, out := reopen("smaller")
	if !restored.viewport.model.RetainedOutputTruncated() {
		t.Fatal("fixture did not evict output during first-prompt restoration")
	}
	if !restored.HandleViewportInput([]byte("\x1b[18~"), false) {
		t.Fatal("first-prompt search was not available")
	}
	if !strings.Contains(strings.ToLower(ansi.Strip(out.String())), "output truncated") {
		t.Fatal("first-prompt search hid loss during checkpoint restoration")
	}
	restored.Resize(20, 14)
	out.Reset()
	restored.PasteOutputSearch([]byte(strings.Repeat("no-match", 40)))
	if !strings.Contains(ansi.Strip(out.String()), "0/0 truncated") {
		t.Fatal("narrow search clipped its position or loss disclosure behind the query")
	}
	frozen := strings.Join(restored.viewport.frozen, "\n")
	out.Reset()
	restored.mu.Lock()
	restored.recordViewportOutput([]byte("arrived-later\r\n"))
	restored.renderOutputViewport()
	restored.mu.Unlock()
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 20, 14), "0/0 truncated") {
		t.Fatal("incoming output hid the narrow search position or loss disclosure")
	}
	if strings.Join(restored.viewport.frozen, "\n") != frozen {
		t.Fatal("incoming output changed the searched snapshot")
	}
	out.Reset()
	restored.HandleViewportInput([]byte("\x1b[23~"), false)
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 20, 14), "0/0 truncated") {
		t.Fatal("an action notice hid the narrow search position or loss disclosure")
	}
	if !restored.HandleViewportInput([]byte("\x1b[24~"), false) {
		t.Fatal("first-prompt export was not available")
	}
	restored.mu.Lock()
	done := restored.outputExportDone
	restored.mu.Unlock()
	if done == nil {
		t.Fatal("first-prompt export did not start")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatal("first-prompt export did not publish one file")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(data), "retained output truncated") || !strings.Contains(string(data), "newest-restored-sentinel") {
		t.Fatal("first-prompt export lost newest output or truncation disclosure")
	}
	restored.mu.Lock()
	restored.checkpointOutputRecovery()
	restored.mu.Unlock()
	restored.Close()
	state, err := restored.outputRecovery.read()
	if err != nil || !state.Truncated || !strings.Contains(strings.Join(state.Rows, "\n"), "checkpoint output truncated") {
		t.Fatal("recheckpointing discarded inherited truncation disclosure")
	}
	again, _ := reopen("again")
	again.HandleViewportInput([]byte("\x1b[18~"), false)
	if !again.viewport.frozenTruncated {
		t.Fatal("second restoration discarded truncation provenance")
	}
}

func TestOutputTruncationTracksDirectWritesAndPreservesFrozenProvenance(t *testing.T) {
	c, _, _ := outputToolsFixture(t, "")
	c.viewport.model.SetScrollbackSize(2)
	c.viewport.write([]byte(strings.Repeat("direct-owned-output\r\n", 30)))
	c.HandleViewportInput([]byte("\x1b[18~"), false)
	if !c.viewport.frozenTruncated {
		t.Fatal("direct viewport writes bypassed real-eviction disclosure")
	}
	c.viewport.write([]byte("\x1b[3J"))
	if c.viewport.model.RetainedOutputTruncated() || c.viewport.outputTruncated() {
		t.Fatal("explicit clearing became a new retention-loss event")
	}
	if !c.viewport.frozenTruncated {
		t.Fatal("clearing the live model changed an already frozen snapshot's provenance")
	}
}

func TestOutputCheckpointFittingRestoreKeepsLegacyLossProvenance(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(fmt.Sprint(inherited), func(t *testing.T) {
			dir := t.TempDir()
			old, _ := recoveryCompositor(t, dir, "legacy-pane", "old", nil)
			old.Close()
			rows := []string{"kept-legacy-text"}
			if inherited {
				rows = append([]string{"[VUJA] checkpoint output truncated"}, rows...)
			}
			// Version-1 files predating explicit provenance metadata remain
			// readable, including their reserved leading truncation notice.
			data, err := json.Marshal(map[string]any{"version": 1, "owner": "legacy", "rows": rows})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old.outputRecovery.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			c, _ := recoveryCompositor(t, dir, "legacy-pane", "current", nil)
			if c.viewport.model.RetainedOutputTruncated() {
				t.Fatal("fitting restore unexpectedly evicted text")
			}
			c.HandleViewportInput([]byte("\x1b[18~"), false)
			if c.viewport.frozenTruncated != inherited {
				t.Fatal("fitting restore confused inherited loss with new SDK eviction")
			}
		})
	}
}

func TestOutputExecutionLossDisclosuresSurviveNarrowActivity(t *testing.T) {
	for _, fixture := range []struct {
		name, output, warning string
	}{
		{"partial", "discarded\r\nsecond\r\nthird\r\n", "truncated"},
		{"application", "alpha\r\nbeta\x1b[1A\rALPHA", "not captured"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "")
			c.chatboxConfig.OutputLines = 2
			completeRetainedExecution(t, c, fixture.output)
			c.Resize(20, 14)
			out.Reset()
			if forwarded := input.FilterInput([]byte("\x1b[21~"), c, false, true); len(forwarded) != 0 {
				t.Fatal("execution navigation reached shell input")
			}
			selected := c.viewport.executions.selected
			if selected == nil || fixture.name == "partial" && !selected.truncated || fixture.name == "application" && !selected.applicationOwned {
				t.Fatal("fixture did not select the expected partial or application-owned execution")
			}
			c.mu.Lock()
			c.recordViewportOutput([]byte("arrived-later\r\n"))
			c.renderOutputViewport()
			c.mu.Unlock()
			if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 20, 14), fixture.warning) {
				t.Fatal("narrow activity hid the selected execution's loss disclosure")
			}
		})
	}
}
