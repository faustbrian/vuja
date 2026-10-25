package root

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

const testClaudeResumeID = "11111111-2222-4333-8444-555555555555"
const testClaudeResumeCommand = "claude --resume " + testClaudeResumeID

// QA A: forwarding the native instruction is not enough; the next managed
// prompt must still display the complete command from retained semantic rows.
func TestClaudeResumeSurvivesNativeCompletionAndPrompt(t *testing.T) {
	c, out := blocksFixture(t, "")
	blocksPrompt(c, "claude")
	c.WritePTY(terminalMarkerBytes("blocks", "command-public"))
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	c.WritePTY([]byte("owned-prefix\r\n\x1b[2;2Hprivate-canvas\r\n"))
	before := out.Len()
	summary := "Resume this session with:\r\n" + testClaudeResumeCommand + "\r\n"
	c.WritePTY([]byte(summary))
	if out.String()[before:] != summary {
		t.Fatal("retention changed the native application's forwarded bytes")
	}
	if !strings.Contains(out.String(), testClaudeResumeID) {
		t.Fatal("fixture must forward the native exit instruction")
	}
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	blocksPrompt(c, "")
	retained := strings.Join(retainedModelRows(c.viewport.model), "\n")
	if !strings.Contains(retained, testClaudeResumeCommand) || strings.Contains(retained, "private-canvas") || !strings.Contains(retained, "owned-prefix") {
		t.Fatalf("native completion must retain only owned text and the complete exit instruction: %q", retained)
	}
	if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), testClaudeResumeCommand) {
		t.Fatal("next prompt erased the Claude resume instruction")
	}
	blocksExecution(c, "unrelated-native", "\x1b[2;2Hsecond-private-canvas\r\n", "0")
	blocksPrompt(c, "")
	retained = strings.Join(retainedModelRows(c.viewport.model), "\n")
	if strings.Count(retained, testClaudeResumeID) != 1 || strings.Contains(retained, "second-private-canvas") {
		t.Fatal("later native execution inherited the resume notice or retained its canvas")
	}
}

func TestClaudeResumeSurvivesNarrowResize(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.Resize(24, 16)
	blocksExecution(c, "claude", "\x1b[2;2Hprivate-canvas\r\nResume this session with:\r\n"+testClaudeResumeCommand+"\r\n", "0")
	blocksPrompt(c, "")
	c.Resize(60, 24)
	if !strings.Contains(strings.Join(retainedModelRows(c.viewport.model), "\n"), testClaudeResumeCommand) ||
		!terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), testClaudeResumeCommand) {
		t.Fatal("display wrapping/resize lost the semantic resume instruction")
	}
}

// QA B/F: the parser must own arbitrary read boundaries, not the PTY reader.
func TestNativeClaudeResumeFragmentation(t *testing.T) {
	for _, command := range []string{testClaudeResumeCommand, "claude --worktree \"feature/my-tree\" --resume " + testClaudeResumeID, "claude -w \"my_tree.2\" -r " + testClaudeResumeID} {
		for _, ending := range []string{"\r\n", ""} {
			stream := []byte("\x1b[2;2Hprivate-canvas café 日本語\r\n\x1b[2mResume this session with:\r\n  " + command + "\x1b[0m" + ending)
			for split := 0; split <= len(stream); split++ {
				tail := newNativeOutputTail(nil)
				got := string(tail.write(stream[:split])) + string(tail.write(nil)) + string(tail.write(stream[split:])) + string(tail.finish())
				if !strings.Contains(got, command) || strings.Count(got, testClaudeResumeID) != 1 || strings.Contains(got, "private-canvas") {
					t.Fatalf("split %d lost or duplicated finite exit metadata: %q", split, got)
				}
			}
			tail := newNativeOutputTail(nil)
			var got strings.Builder
			for _, b := range stream {
				got.Write(tail.write([]byte{b}))
			}
			got.Write(tail.finish())
			if !strings.Contains(got.String(), command) || strings.Count(got.String(), testClaudeResumeID) != 1 {
				t.Fatal("bytewise delivery changed the exit instruction")
			}
		}
	}
}

// QA C: a finite continuation instruction is not permission to retain a canvas.
func TestNativeClaudeResumeRejectsNonInstructions(t *testing.T) {
	for _, candidate := range []string{
		"private canvas " + testClaudeResumeID + "\r\n",
		testClaudeResumeCommand + "\r\n",
		"Quoted: Resume this session with:\r\n" + testClaudeResumeCommand + "\r\n",
		"Resume this session with:\r\nclaude --resume not-a-session\r\n",
		"Resume this session with:\r\n" + testClaudeResumeCommand + "; private-suffix\r\n",
		"Resume this session with:\r\nclaude --resume " + testClaudeResumeID + "0\r\n",
		"Resume this session with:\r\nprivate interposed line\r\n" + testClaudeResumeCommand + "\r\n",
		"Resume this session with:\r\nclaude --resu\x1b[2;2Hme " + testClaudeResumeID + "\r\n",
		"\x1b]0;Resume this session with:\r\n" + testClaudeResumeCommand + "\a",
		"\x1bPResume this session with:\r\n" + testClaudeResumeCommand + "\x1b\\",
	} {
		tail := newNativeOutputTail(nil)
		got := string(tail.write([]byte("\x1b[2;2H"+candidate))) + string(tail.finish())
		if got != "" {
			t.Fatalf("non-instruction became retained output: %q", got)
		}
	}
	tail := newNativeOutputTail(nil)
	got := string(tail.write([]byte("\x1b[2;2HResume this session with:\r\n"+testClaudeResumeCommand+"\r\nprivate-after\r\n"))) + string(tail.finish())
	if strings.Contains(got, "private-after") || !strings.Contains(got, testClaudeResumeCommand) {
		t.Fatal("recognizing a resume instruction admitted surrounding canvas text")
	}
}

func TestNativeClaudeResumeBoundsAndRecovery(t *testing.T) {
	for _, length := range []int{codexResumeLineLimit - 1, codexResumeLineLimit, codexResumeLineLimit + 1} {
		command := strings.Repeat(" ", length-len(testClaudeResumeCommand)) + testClaudeResumeCommand
		tail := newNativeOutputTail(nil)
		got := string(tail.write([]byte("\x1b[2;2HResume this session with:\n" + command + "\n")))
		if strings.Contains(got, testClaudeResumeID) != (length <= codexResumeLineLimit) {
			t.Fatalf("candidate length %d did not honor the existing bounded-line budget", length)
		}
	}
	tail := newNativeOutputTail(nil)
	got := string(tail.write([]byte("\x1b[2;2HResume this session with:\n" + strings.Repeat("private", codexResumeLineLimit) + "\nResume this session with:\n" + testClaudeResumeCommand + "\n")))
	if !strings.Contains(got, testClaudeResumeCommand) || strings.Contains(got, "private") {
		t.Fatal("oversized rejected text prevented a later valid instruction")
	}
	got = string(tail.write([]byte("Resume this session with:\n" + testClaudeResumeCommand + "\n")))
	if got != "" {
		t.Fatal("duplicate instruction was retained twice")
	}
	var all strings.Builder
	tail = newNativeOutputTail(nil)
	all.Write(tail.write([]byte("\x1b[2;2H")))
	for i := 0; i < 65; i++ {
		all.Write(tail.write([]byte(fmt.Sprintf("Resume this session with:\nclaude --resume 11111111-2222-4333-8444-%012x\n", i))))
	}
	if strings.Count(all.String(), "claude --resume ") != 64 {
		t.Fatal("native continuation metadata exceeded its existing 64-notice budget")
	}
}

func TestNativeClaudeResumeWorktreeGrammar(t *testing.T) {
	for _, flags := range [][2]string{{"--worktree", "--resume"}, {"-w", "-r"}} {
		for _, name := range []string{"tree", "feature/my-tree_2.0", strings.Repeat("x", 64)} {
			command := fmt.Sprintf("claude %s %q %s %s", flags[0], name, flags[1], testClaudeResumeID)
			tail := newNativeOutputTail(nil)
			got := string(tail.write([]byte("\x1b[2;2HResume this session with:\n"+command+"\n"))) + string(tail.finish())
			if !strings.Contains(got, command) {
				t.Fatalf("producer-valid worktree %q was lost", name)
			}
		}
		for _, name := range []string{"", "private customer notes not a worktree", "café", "日本語", "-tree", strings.Repeat("x", 65), ".", "..", ".git", ".GIT...", "feature/../tree", "feature/.git./tree", "/tree", "tree/", "tree//child", "tree;$private", "tree\\child"} {
			command := fmt.Sprintf("claude %s %q %s %s", flags[0], name, flags[1], testClaudeResumeID)
			tail := newNativeOutputTail(nil)
			got := string(tail.write([]byte("\x1b[2;2HResume this session with:\n"+command+"\n"))) + string(tail.finish())
			if got != "" {
				t.Fatalf("nonproducer worktree %q escaped native-canvas exclusion: %q", name, got)
			}
		}
	}
}

// QA D: use public search/selection input and inspect their semantic payloads.
func TestClaudeResumePaneConsumers(t *testing.T) {
	for _, automatic := range []bool{true, false} {
		t.Run(fmt.Sprintf("automatic-%t", automatic), func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\ncopy-on-select=%t\n", automatic))
			server, _ := resumeFixtureServer(t)
			c.SetCodexResumeLinker(server.Observe)
			blocksExecution(c, "claude", "owned-prefix\r\n\x1b[2;2Hprivate-canvas\r\nResume this session with:\r\n"+testClaudeResumeCommand+"\r\n", "0")
			blocksPrompt(c, "keep-input")
			y := terminalLineIndex(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), testClaudeResumeCommand, 0)
			if y < 0 {
				t.Fatal("resume command is missing from selectable pane output")
			}
			start := out.Len()
			cardMouse(t, c, 0, 0, y, true)
			cardMouse(t, c, 32, 59, y, true)
			if strings.Contains(out.String()[start:], "\x1b]52;") {
				t.Fatal("resume selection copied before release")
			}
			cardMouse(t, c, 0, 59, y, false)
			if automatic {
				assertExecutionClipboard(t, out.String()[start:], testClaudeResumeCommand)
			} else if strings.Contains(out.String()[start:], "\x1b]52;") {
				t.Fatal("manual resume selection copied on release")
			}
			start = out.Len()
			var input terminalInputFilter
			want := "next"
			if automatic {
				want = "\x03next"
			}
			if got := input.FilterInput([]byte("\x03next"), c, false, true); string(got) != want {
				t.Fatalf("resume selection changed subsequent shell input: got %q, want %q", got, want)
			}
			if automatic {
				if strings.Contains(out.String()[start:], "\x1b]52;") {
					t.Fatal("interrupt copied the resume selection twice")
				}
			} else {
				assertExecutionClipboard(t, out.String()[start:], testClaudeResumeCommand)
			}
			input.FilterInput([]byte("\x1b[18~"+testClaudeResumeID), c, false, true)
			if len(c.viewport.search.matches) != 1 {
				t.Fatal("pane search lost the complete resume ID")
			}
			input.FilterInput([]byte("\x15private-canvas"), c, false, true)
			if len(c.viewport.search.matches) != 0 {
				t.Fatal("pane search retained private canvas contents")
			}
			path, err := writeRetainedOutputExport(retainedExecution{rows: withoutPaneDividers(c.viewport.frozen, c.viewport.frozenDividers), newline: true})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte(testClaudeResumeCommand)) || bytes.Contains(data, []byte("private-canvas")) || bytes.Contains(data, []byte("\x1b")) {
				t.Fatal("pane export lost the resume command or included private canvas/control bytes")
			}
			if _, err := writeRetainedOutputExport(c.viewport.executions.retained[0]); err == nil {
				t.Fatal("resume metadata enabled native per-execution export")
			}
		})
	}
}

// QA E: only finite public rows survive actual checkpoint serialization.
func TestClaudeResumeCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c, _ := blocksFixture(t, "")
	if err := c.ConfigureOutputRecovery(dir, "claude-pane", nil); err != nil {
		t.Fatal(err)
	}
	blocksExecution(c, "claude", "\x1b[2;2Hprivate-canvas\r\nResume this session with:\r\nclaude --worktree \"private customer notes not a worktree\" --resume "+testClaudeResumeID+"\r\nResume this session with:\r\n"+testClaudeResumeCommand, "0")
	blocksPrompt(c, "")
	c.Close()
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil || !bytes.Contains(data, []byte(testClaudeResumeID)) || bytes.Contains(data, []byte("private-canvas")) || bytes.Contains(data, []byte("private customer notes")) || bytes.Contains(data, []byte("vuja://")) {
		t.Fatal("checkpoint lost plain resume metadata or stored private/application action data")
	}
	for i := 0; i < 2; i++ {
		reopened, out := blocksFixture(t, "")
		if err := reopened.ConfigureOutputRecovery(dir, "claude-pane", nil); err != nil {
			t.Fatal(err)
		}
		blocksPrompt(reopened, "")
		rows := strings.Join(retainedModelRows(reopened.viewport.model), "\n")
		if strings.Count(rows, testClaudeResumeID) != 1 || strings.Contains(rows, "private-canvas") || !strings.Contains(out.String(), "process not resumed") {
			t.Fatal("checkpoint restore lost/duplicated the resume instruction or restored canvas")
		}
		if !terminalContainsLine(applyTerminalOutput(t, out.Bytes(), 60, 24), testClaudeResumeCommand) {
			t.Fatal("restored resume instruction is absent from the final screen")
		}
		var input terminalInputFilter
		input.FilterInput([]byte("\x1b[18~"+testClaudeResumeID), reopened, false, true)
		if len(reopened.viewport.search.matches) != 1 {
			t.Fatal("restored resume instruction is not searchable")
		}
		path, err := writeRetainedOutputExport(retainedExecution{rows: withoutPaneDividers(reopened.viewport.frozen, reopened.viewport.frozenDividers), newline: true})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Count(data, []byte(testClaudeResumeID)) != 1 || bytes.Contains(data, []byte("private-canvas")) {
			t.Fatal("restored pane export lost/duplicated metadata or exported canvas")
		}
		reopened.Close()
	}
}

func TestClaudeResumeOwnershipVariants(t *testing.T) {
	for _, prefix := range []string{"", "\x1b[?1049hprivate-canvas\x1b[?1049l", "\x1b[2;2Hprivate-canvas\r\n"} {
		tail := newNativeOutputTail(nil)
		stream := prefix + "Resume this session with:\r\n" + testClaudeResumeCommand + "\r\n"
		got := string(tail.write([]byte(stream))) + string(tail.finish())
		if strings.Count(got, testClaudeResumeID) != 1 || strings.Contains(got, "private-canvas") {
			t.Fatal("ordinary/alternate/primary ownership changed the resume instruction")
		}
	}
	tail := newNativeOutputTail(nil)
	if got := string(tail.write([]byte("\x1b[2;2Hunrelated-private-canvas\r\n"))) + string(tail.finish()); got != "" {
		t.Fatal("new native execution inherited another execution's instruction")
	}
}
