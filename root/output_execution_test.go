package root

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/config"
)

func TestRetainedExecutionNavigationDoesNotReachTheShell(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	for _, output := range []string{"first-only", "second-only", "third-only"} {
		c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
		c.WritePTY([]byte(output + "\r\n"))
		c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
		c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
		c.WritePTY([]byte("› keep this input"))
		c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
	}
	for i := 0; i < 2; i++ {
		if forwarded := input.FilterInput([]byte("\x1b[21~"), c, false, true); len(forwarded) != 0 {
			t.Fatalf("previous-execution action reached the shell: %q", forwarded)
		}
	}
	if forwarded := input.FilterInput([]byte("\x1b[23~"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("copy-execution action reached the shell: %q", forwarded)
	}
	// The clipboard transport is a terminal effect, never a shell command.
	want := base64.StdEncoding.EncodeToString([]byte("second-only\n"))
	if !strings.Contains(out.String(), "\x1b]52;c;"+want) {
		t.Fatal("copy did not isolate the selected second execution's output")
	}
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "keep this input") {
		t.Fatal("execution navigation changed the fixed shell input")
	}
}

func TestRetainedOutputExportIsPrivatePlainTextAndNeverExecutes(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, _, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "neighbor\r\n")
	completeRetainedExecution(t, c, "\x1b[31mselected 世界\x1b[0m\r\n")
	input.FilterInput([]byte("\x1b[21~"), c, false, true)
	if got := input.FilterInput([]byte("\x1b[24~"), c, false, true); len(got) > 0 {
		t.Fatalf("export action reached shell input: %q", got)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 1 {
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "selected 世界\n" {
				t.Fatalf("export did not isolate selected plain output: %q, %v", data, err)
			}
			info, err := os.Stat(paths[0])
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("export file is not owner-only")
			}
			info, err = os.Stat(filepath.Dir(paths[0]))
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("export directory is not owner-only")
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("export never published a completed file")
		case <-tick.C:
		}
	}
}

func TestRetainedExecutionExactLineLimitDoesNotDiscardOutput(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.chatboxConfig.OutputLines = 3
	completeRetainedExecution(t, c, "first\r\nsecond\r\nthird\r\n")
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "first\nsecond\nthird\n")
	if terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "output truncated") {
		t.Fatal("a trailing cursor line falsely consumed the output retention budget")
	}
}

func TestRetainedExecutionCursorAddressingKeepsApplicationOwnership(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "alpha\r\nbeta\x1b[1A\rALPHA")
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	if strings.Contains(out.String(), "\x1b]52;") {
		t.Fatal("cursor-addressed application output became an implicit clipboard capture")
	}
}

func TestRetainedExecutionCopyDisclosesTruncation(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.chatboxConfig.OutputLines = 2
	completeRetainedExecution(t, c, "discarded\r\nsecond\r\nthird\r\n")
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "second\nthird\n")
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "truncated") {
		t.Fatal("copying retained partial output hid its truncation disclosure")
	}
}

func TestRetainedExecutionResizePreservesTextWithoutFalseCroppingNotice(t *testing.T) {
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
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "012345678901234567890123456789\nabcdefghijklmnopqrstuvwxyz\n")
	if terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "truncated") {
		t.Fatal("geometry-only reflow was falsely reported as lost output")
	}
}

func TestRetainedExecutionTinyResizeDoesNotBecomeApplicationOwnership(t *testing.T) {
	for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
		t.Run(fmt.Sprintf("action-%q", action), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out, input := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY([]byte("before\r\n"))
			c.Resize(20, 3)
			c.WritePTY([]byte("during\r\n"))
			c.Resize(80, 14)
			c.WritePTY([]byte("after\r\n"))
			c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
			c.WritePTY([]byte("› keep this input"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
			input.FilterInput([]byte("\x1b[21~"), c, false, true)
			out.Reset()
			input.FilterInput([]byte(action), c, false, true)
			if action == "\x1b[23~" {
				assertExecutionClipboard(t, out.String(), "before\nduring\nafter\n")
				return
			}
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done == nil {
				t.Fatal("ordinary geometric suspension refused execution export")
			}
			<-done
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 1 {
				t.Fatal("ordinary resized execution was not exported")
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "before\nduring\nafter\n" {
				t.Fatalf("resize lost ordinary retained execution output: %q, %v", data, err)
			}
		})
	}
}

func TestRetainedOutputResizePreservesPreviousAndNextExecution(t *testing.T) {
	c, out, _ := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "first before resize\r\n")
	c.Resize(60, 16)
	completeRetainedExecution(t, c, "second after resize\r\n")
	screen := applyTerminalOutput(t, out.Bytes(), 60, 16)
	for _, text := range []string{"first before resize", "second after resize", "keep this input"} {
		if !terminalContainsLine(screen, text) {
			t.Fatalf("resizing the retained pane lost %q", text)
		}
	}
}

func TestRetainedPaneExportPreservesTextAcrossResize(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, _, input := outputToolsFixture(t, "")
	text := "prefix-" + strings.Repeat("x", 30) + "-suffix"
	completeRetainedExecution(t, c, text+"\r\n")
	c.Resize(20, 14)
	c.Resize(80, 14)
	if forwarded := input.FilterInput([]byte("\x1b[24~"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("pane export reached shell input: %q", forwarded)
	}
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("pane export did not start")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatal("pane export did not publish one completed file")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(data), "prefix-") {
		t.Fatalf("pane export lost the retained prefix: %q, %v", data, err)
	}
	if !strings.Contains(string(data), text) {
		t.Fatal("pane export lost retained text during resize")
	}
}

func TestRetainedPaneExportDoesNotSilentlyCropOlderRows(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	c, _, input := outputToolsFixture(t, "")
	text := "old-prefix-" + strings.Repeat("x", 30) + "-old-suffix"
	completeRetainedExecution(t, c, text+"\r\n"+strings.Repeat("short\r\n", 20))
	c.Resize(20, 14)
	if forwarded := input.FilterInput([]byte("\x1b[24~"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("pane export reached shell input: %q", forwarded)
	}
	c.mu.Lock()
	done := c.outputExportDone
	c.mu.Unlock()
	if done == nil {
		t.Fatal("pane export did not start")
	}
	<-done
	paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
	if err != nil || len(paths) != 1 {
		t.Fatal("pane export did not publish one completed file")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(data), "old-prefix-") {
		t.Fatalf("pane export lost its older retained row: %q, %v", data, err)
	}
	if !strings.Contains(string(data), text) {
		t.Fatal("pane export cropped an older retained row")
	}
}

func TestRetainedExecutionCopyStripsControlsAndPreservesUnicode(t *testing.T) {
	text := "\x1b[31mCafe\u0301 世界 👩‍💻\x1b[0m\r\n\x1b]52;c;dW5zYWZl\x1b\\last"
	for split := 0; split <= len(text); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, out, input := outputToolsFixture(t, "")
			c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
			c.WritePTY([]byte(text[:split]))
			c.WritePTY([]byte(text[split:]))
			c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
			c.WritePTY([]byte("› keep this input"))
			c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
			out.Reset() // disregard native nonvisual effects, assert this explicit copy
			input.FilterInput([]byte("\x1b[23~"), c, false, true)
			assertExecutionClipboard(t, out.String(), "Cafe\u0301 世界 👩‍💻\nlast")
		})
	}
}

func TestRetainedExecutionNavigationClampsAndKeepsFrozenCopy(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "first\r\n")
	completeRetainedExecution(t, c, "second\r\n")
	for i := 0; i < 4; i++ {
		input.FilterInput([]byte("\x1b[21~"), c, false, true)
	}
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "first\n")
	input.FilterInput([]byte("\x1b[21;2~"), c, false, true)
	completeRetainedExecution(t, c, "third\r\n")
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "second\n")
	input.FilterInput([]byte("\x1b[21;2~"), c, false, true)
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "third\n")
	input.FilterInput([]byte("\x1b[F"), c, false, true)
	if c.viewport.frozen != nil {
		t.Fatal("End did not return from execution browsing to latest output")
	}
}

func TestRetainedExecutionActionsPreserveNativeAndPasteOwnership(t *testing.T) {
	c, _, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "before\r\n")
	input.FilterInput([]byte("\x1b[21~"), c, false, true)
	paste := "\x1b[200~\x1b[21~\x1b[23~\r\x1b[201~"
	if got := string(input.FilterInput([]byte(paste), c, false, true)); got != paste {
		t.Fatal("execution actions consumed a bracketed paste payload")
	}
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte("\x1b[?1049hprivate full-screen pixels"))
	for _, key := range []string{"\x1b[21~", "\x1b[21;2~", "\x1b[23~"} {
		if got := string(input.FilterInput([]byte(key), c, false, false)); got != key {
			t.Fatal("execution action stole application-owned input")
		}
	}
	c.WritePTY([]byte("\x1b[?1049lprimary summary\r\n"))
	c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
	c.WritePTY([]byte("› keep this input"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	if r := c.viewport.executions.selected; r == nil || r.plainText() != "" {
		t.Fatal("application-owned execution was retained as a copyable capture")
	}
}

func TestRetainedExecutionPrimaryNativeCanvasCannotBeCopiedOrExported(t *testing.T) {
	for _, action := range []string{"\x1b[23~", "\x1b[24~"} {
		t.Run(fmt.Sprintf("action-%q", action), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			c, out, input := outputToolsFixture(t, "")
			completeRetainedExecution(t, c, "\x1b[2J\x1b[HPRIVATE-CANVAS")
			input.FilterInput([]byte("\x1b[21~"), c, false, true)
			out.Reset()
			if forwarded := input.FilterInput([]byte(action), c, false, true); len(forwarded) != 0 {
				t.Fatal("unsupported native capture action reached shell input")
			}
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done != nil {
				<-done
			}
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("primary-screen native canvas was copied")
			}
			paths, err := filepath.Glob(filepath.Join(cache, "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 0 {
				t.Fatal("primary-screen native canvas was exported")
			}
			if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "not captured") {
				t.Fatal("unsupported native capture was not visibly explained")
			}
		})
	}
}

func TestRetainedExecutionEmptyCopyLeavesClipboardUnchanged(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "")
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	if strings.Contains(out.String(), "\x1b]52;") {
		t.Fatal("empty execution copy cleared or replaced the clipboard")
	}
}

func TestRetainedExecutionOrdinaryLineProgressRemainsCopyable(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "starting\rfinished\x1b[K\r\n")
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "finished\n")
}

func TestRetainedExecutionGlobalSearchDoesNotCopyAnUnselectedCommand(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	completeRetainedExecution(t, c, "needle\r\n")
	input.FilterInput([]byte("\x1b[18~needle"), c, false, true)
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	if strings.Contains(out.String(), "\x1b]52;") {
		t.Fatal("global search silently copied an unrelated implicit execution")
	}
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 80, 14), "Select execution") {
		t.Fatal("search hid the reason that copying requires an execution selection")
	}
}

func TestRetainedExecutionToolbarPreservesFixedTitleMetadata(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(config.DefaultConfig()))
	c.SetStatusSnapshot(statusSnapshot{CodexAccount: "person@example.test"})
	completeRetainedExecution(t, c, "retained result\r\n")
	input.FilterInput([]byte("\x1b[21~"), c, false, true)
	screen := applyTerminalOutput(t, out.Bytes(), 80, 14)
	if !strings.Contains(screenLine(screen, c.height-c.surfaceRows), "Codex person@example.test") {
		t.Fatal("execution toolbar hid the fixed title's account metadata")
	}
	if !terminalContainsLine(screen, "Execution #1") || !terminalContainsLine(screen, "keep this input") {
		t.Fatal("execution navigation lost the visible target or fixed shell input")
	}
}

func TestRetainedExecutionsStayBoundedWithoutChangingAnOpenCopy(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.chatboxConfig.OutputLines = 3
	completeRetainedExecution(t, c, "old selected\r\n")
	input.FilterInput([]byte("\x1b[21~"), c, false, true)
	for i := 0; i < 100; i++ {
		completeRetainedExecution(t, c, fmt.Sprintf("new-%d\r\n", i))
	}
	if len(c.viewport.executions.retained) > 3 {
		t.Fatal("execution records exceeded the shared output retention budget")
	}
	rows, size := 0, 0
	for _, r := range c.viewport.executions.retained {
		rows += max(len(r.rows), 1)
		for _, line := range r.rows {
			size += len(line) + 1
		}
	}
	if rows > 3 || size > retainedExecutionBytes {
		t.Fatal("retained execution rows or text exceeded their global budget")
	}
	out.Reset()
	input.FilterInput([]byte("\x1b[23~"), c, false, true)
	assertExecutionClipboard(t, out.String(), "old selected\n")
	for i := 0; i < 100; i++ {
		completeRetainedExecution(t, c, "")
	}
	if len(c.viewport.executions.retained) != 3 {
		t.Fatal("silent executions do not consume bounded retention slots")
	}
}

func TestRetainedOutputExportDisclosesTruncationAndRejectsSymlinks(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	path, err := writeRetainedOutputExport(retainedExecution{rows: []string{"\x1b[31mremaining\x1b[0m"}, newline: true, truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "[VUJA: retained output truncated]\nremaining\n" {
		t.Fatal("plain-text export omitted its truncation disclosure or retained controls")
	}
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, "vuja"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(other, "vuja", "exports")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", other)
	if _, err := writeRetainedOutputExport(retainedExecution{rows: []string{"private"}}); err == nil {
		t.Fatal("export followed a symlinked output directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe export changed the symlink destination")
	}
}

func completeRetainedExecution(t *testing.T, c *terminalCompositor, text string) {
	t.Helper()
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte(text))
	c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
	c.WritePTY([]byte("› keep this input"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
}

func assertExecutionClipboard(t *testing.T, terminal, want string) {
	t.Helper()
	parts := strings.Split(terminal, "\x1b]52;c;")
	if len(parts) != 2 {
		t.Fatalf("expected exactly one explicit clipboard effect, got %d", len(parts)-1)
	}
	payload, _, complete := strings.Cut(parts[1], "\x1b\\")
	got, err := base64.StdEncoding.DecodeString(payload)
	if !complete || err != nil || string(got) != want {
		t.Fatalf("explicit copy does not equal expected plain execution text %q", want)
	}
}

func TestRetainedExecutionCopyIsAnExplicitNonExecutionAction(t *testing.T) {
	c, out, input := outputToolsFixture(t, "")
	c.WritePTY(terminalMarkerBytes("output-tools", "command-start"))
	c.WritePTY([]byte("only this output\r\n"))
	c.WritePTY(terminalMarkerBytes("output-tools", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-start"))
	c.WritePTY([]byte("› keep this input"))
	c.WritePTY(terminalMarkerBytes("output-tools", "prompt-end"))
	if forwarded := input.FilterInput([]byte("\x1b[23~"), c, false, true); len(forwarded) != 0 {
		t.Fatalf("explicit output-copy key reached shell input: %q", forwarded)
	}
	if !strings.Contains(out.String(), "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("only this output\n"))) {
		t.Fatal("explicit copy did not produce the retained execution text")
	}
}
