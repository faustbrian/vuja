package root

import (
	"bytes"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/vt"
)

func renderedClaudeResumeTarget(wire, id string) string {
	for {
		_, rest, ok := strings.Cut(wire, "\x1b]8;")
		if !ok {
			return ""
		}
		end, terminator := strings.IndexByte(rest, '\a'), 1
		if st := strings.Index(rest, "\x1b\\"); st >= 0 && (end < 0 || st < end) {
			end, terminator = st, 2
		}
		if end < 0 {
			return ""
		}
		_, target, _ := strings.Cut(rest[:end], ";")
		u, err := url.Parse(target)
		if err == nil && u.Host == "claude-resume" && u.Path == "/"+id {
			return target
		}
		wire = rest[end+terminator:]
	}
}

func TestClaudeResumeLinksStyledUUIDAcrossChunks(t *testing.T) {
	styledID := testClaudeResumeID[:8] + "\x1b[0m" + testClaudeResumeID[8:20] + "\x1b[36m" + testClaudeResumeID[20:]
	input := "Resume this session with:\r\nclaude --resume " + styledID + "\r\n"
	for split := 0; split <= len(input); split++ {
		l := newCodexResumeLinkifier(func(command string) string {
			if command != testClaudeResumeCommand {
				t.Fatalf("styling changed command: %q", command)
			}
			return "vuja://claude-resume/" + testClaudeResumeID
		})
		got := string(l.Transform([]byte(input[:split]))) + string(l.Transform([]byte(input[split:]))) + string(l.Flush())
		if renderedClaudeResumeTarget(got, testClaudeResumeID) == "" || !strings.Contains(got, "\x1b\\"+styledID+"\x1b]8;;\x1b\\") || ansi.Strip(got) != ansi.Strip(input) {
			t.Fatalf("split %d lost the styled UUID action: %q", split, got)
		}
	}
	s, _ := resumeFixtureServer(t)
	c, out := blocksFixture(t, "")
	c.SetCodexResumeLinker(s.Observe)
	blocksPrompt(c, "claude")
	c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
	l := newCodexResumeLinkifier(s.Observe)
	c.WritePTY(l.Transform([]byte(input)))
	out.Reset()
	c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
	blocksPrompt(c, "")
	if renderedClaudeResumeTarget(out.String(), testClaudeResumeID) == "" {
		t.Fatal("ordinary compositor lost the styled UUID action")
	}
}

func TestClaudeResumeLinksExcludeC1Strings(t *testing.T) {
	for _, introducer := range []string{"\x90q", "\x9d0;", "\x98", "\x9e", "\x9f"} {
		for _, prefix := range []string{"", "Resume this session with:\n"} {
			input := prefix + introducer + "\nResume this session with:\n" + testClaudeResumeCommand + "\n\x9c"
			for split := 0; split <= len(input); split++ {
				observed := 0
				l := newCodexResumeLinkifier(func(string) string { observed++; return "vuja://claude-resume/" + testClaudeResumeID })
				got := string(l.Transform([]byte(input[:split]))) + string(l.Transform([]byte(input[split:]))) + string(l.Flush())
				for _, wire := range []string{input, got} {
					model := vt.NewEmulator(100, 12)
					_, _ = model.Write([]byte(wire))
					visible := model.String()
					_ = model.Close()
					if strings.Contains(visible, testClaudeResumeID) {
						t.Fatalf("split %d exposed a hidden C1 UUID", split)
					}
				}
				if observed != 0 || got != input {
					t.Fatalf("split %d promoted hidden C1 metadata", split)
				}
				visible := string(l.Transform([]byte("\r\nResume this session with:\r\n" + testClaudeResumeCommand + "\r\n")))
				if observed != 1 || renderedClaudeResumeTarget(visible, testClaudeResumeID) == "" {
					t.Fatal("a terminated C1 string suppressed the following real notice")
				}
			}
		}
	}
	// These valid UTF-8 runes contain C1-shaped continuation bytes, not controls.
	input := "ؐ؝؜😀\r\nResume this session with:\r\n" + testClaudeResumeCommand + "\r\n"
	for split := 0; split <= len(input); split++ {
		l := newCodexResumeLinkifier(func(string) string { return "vuja://claude-resume/" + testClaudeResumeID })
		got := string(l.Transform([]byte(input[:split]))) + string(l.Transform([]byte(input[split:]))) + string(l.Flush())
		if renderedClaudeResumeTarget(got, testClaudeResumeID) == "" || ansi.Strip(got) != ansi.Strip(input) {
			t.Fatalf("split %d treated UTF-8 continuation as C1 control", split)
		}
	}
}

// QA 1/2: inspect the wire independently, not just a visible UUID substring.
func TestClaudeResumeLinksInstructionAcrossChunks(t *testing.T) {
	for _, command := range []string{testClaudeResumeCommand, "claude -r " + testClaudeResumeID, "claude --worktree \"feature/my_tree.2\" --resume " + testClaudeResumeID, "claude -w \"tree\" -r " + testClaudeResumeID} {
		for _, ending := range []string{"\r\n", ""} {
			input := "\x1b[2mResume this session with:\r\n  " + command + "\x1b[0m" + ending
			for split := 0; split <= len(input); split++ {
				l := newCodexResumeLinkifier(func(value string) string {
					if value != command {
						t.Fatalf("wrong observed command: %q", value)
					}
					return "vuja://claude-resume/" + testClaudeResumeID + "?token=fixture"
				})
				got := string(l.Transform([]byte(input[:split]))) + string(l.Transform([]byte(input[split:]))) + string(l.Flush())
				target := renderedClaudeResumeTarget(got, testClaudeResumeID)
				if target == "" || ansi.Strip(got) != ansi.Strip(input) {
					t.Fatalf("split %d lost action or changed visible instruction: %q", split, got)
				}
				if !strings.Contains(got, "\x1b\\"+testClaudeResumeID+"\x1b]8;;\x1b\\") {
					t.Fatal("action must enclose only the UUID")
				}
			}
			l := newCodexResumeLinkifier(func(string) string { return "vuja://claude-resume/" + testClaudeResumeID })
			var bytewise strings.Builder
			for i := range input {
				bytewise.Write(l.Transform([]byte(input[i : i+1])))
			}
			bytewise.Write(l.Flush())
			if renderedClaudeResumeTarget(bytewise.String(), testClaudeResumeID) == "" || ansi.Strip(bytewise.String()) != ansi.Strip(input) {
				t.Fatal("bytewise styled instruction lost action or visible text")
			}
		}
	}
}

// QA 4: same UUID must not alias Codex or another Claude worktree.
func TestClaudeResumeActionsKeepProviderAndWorktree(t *testing.T) {
	s, dir := resumeFixtureServer(t)
	commands := []string{testClaudeResumeCommand, "claude --worktree \"feature/one\" --resume " + testClaudeResumeID, "claude -w \"two\" -r " + testClaudeResumeID, "claude --worktree \"" + strings.Repeat("x", 64) + "\" --resume " + testClaudeResumeID, "codex resume " + testClaudeResumeID}
	var targets []string
	for _, command := range commands {
		value := command
		if strings.HasPrefix(command, "codex ") {
			value = testClaudeResumeID
		}
		target := s.Observe(value)
		if target == "" {
			t.Fatalf("no action for %q", command)
		}
		targets = append(targets, target)
	}
	select {
	case <-s.Actions():
		t.Fatal("observing output executed an action")
	default:
	}
	for i, target := range targets {
		var input bytes.Buffer
		if err := dispatchResumeFixture(t, s, target, dir, false, "", 0, &input); err != nil {
			t.Fatal(err)
		}
		if input.String() != commands[i]+"\r" {
			t.Fatalf("wrong provider/worktree: %q", input.String())
		}
		if err := dispatchCodexResumeURL(target, dir); err == nil {
			t.Fatal("consumed action could execute twice")
		}
	}
}

// QA 1/6: live and restored links use owner capabilities, never disk secrets.
func TestClaudeResumeLinkedCheckpointRebinds(t *testing.T) {
	old, actions := resumeFixtureServer(t)
	dir := t.TempDir()
	c, out := recoveryCompositor(t, dir, "claude-pane", "claude-old", old.Observe)
	const command = "claude --worktree \"feature/restore\" --resume " + testClaudeResumeID
	c.WritePTY(terminalMarkerBytes("claude-old", "command-start"))
	l := newCodexResumeLinkifier(old.Observe)
	c.WritePTY(l.Transform([]byte("\x1b[2;2Hprivate-canvas\r\nResume this session with:\r\n" + command + "\r\n")))
	c.WritePTY(terminalMarkerBytes("claude-old", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("claude-old", "prompt-start"))
	c.WritePTY(terminalMarkerBytes("claude-old", "prompt-end"))
	c.Resize(24, 16)
	out.Reset()
	c.Resize(100, 24)
	stale := renderedClaudeResumeTarget(out.String(), testClaudeResumeID)
	if stale == "" || !strings.Contains(ansi.Strip(strings.Join(retainedModelRows(c.viewport.model), "\n")), command) {
		t.Fatal("next prompt/resize lost the clickable Claude command")
	}
	c.Close()
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil || bytes.Contains(data, []byte(old.token)) || bytes.Contains(data, []byte(old.socketPath)) || bytes.Contains(data, []byte("private-canvas")) {
		t.Fatal("checkpoint lost metadata or leaked credentials/canvas")
	}
	old.Close()
	fresh, err := newCodexResumeActionServerIn(actions, "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	_, restored := recoveryCompositor(t, dir, "claude-pane", "claude-new", fresh.Observe)
	target := renderedClaudeResumeTarget(restored.String(), testClaudeResumeID)
	if target == "" || target == stale {
		t.Fatal("restore failed to bind a fresh Claude capability")
	}
	select {
	case <-fresh.Actions():
		t.Fatal("restoring a checkpoint executed an action")
	default:
	}
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, fresh, target, actions, false, "", 0, &input); err != nil || input.String() != command+"\r" {
		t.Fatalf("restored action lost Claude/worktree: %q, %v", input.String(), err)
	}
	if err := dispatchCodexResumeURL(stale, actions); err == nil {
		t.Fatal("stale capability was accepted")
	}
	_, plain := recoveryCompositor(t, dir, "claude-pane", "claude-plain", nil)
	if renderedClaudeResumeTarget(plain.String(), testClaudeResumeID) != "" || !strings.Contains(ansi.Strip(plain.String()), testClaudeResumeID) {
		t.Fatal("no-owner restore lost readable text or advertised a dead link")
	}
}

func TestClaudeResumeLinksSurviveOwnershipVariants(t *testing.T) {
	for _, prefix := range []string{"", "\x1b[2;2Hprivate-canvas\r\n", "\x1b[?1049hprivate-canvas\x1b[?1049l"} {
		s, _ := resumeFixtureServer(t)
		c, out := blocksFixture(t, "")
		c.SetCodexResumeLinker(s.Observe)
		blocksPrompt(c, "claude")
		c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
		l := newCodexResumeLinkifier(s.Observe)
		c.WritePTY(l.Transform([]byte(prefix + "\r\nResume this session with:\r\n" + testClaudeResumeCommand + "\r\n")))
		out.Reset()
		c.WritePTY(terminalMarkerBytes("blocks", "command-end:0"))
		blocksPrompt(c, "")
		if renderedClaudeResumeTarget(out.String(), testClaudeResumeID) == "" {
			t.Fatal("ordinary/primary/alternate handoff lost the actionable UUID")
		}
		select {
		case <-s.Actions():
			t.Fatal("rendering submitted an action")
		default:
		}
	}
}

// QA 5: the real generated Zsh hook must execute Claude, not a Go-only fake.
func TestClaudeResumeShellUsesDisplayedCommand(t *testing.T) {
	for _, fixture := range []struct{ command, event string }{
		{testClaudeResumeCommand, "FIXTURE_CLAUDE:2:--resume|" + testClaudeResumeID},
		{"claude --worktree \"feature/tree\" --resume " + testClaudeResumeID, "FIXTURE_CLAUDE:4:--worktree|feature/tree|--resume|" + testClaudeResumeID},
	} {
		f := newResumeShellFixture(t, `claude() { print -u 3 -N -r -- "FIXTURE_CLAUDE:$#:${(j:|:)@}"; }`)
		s, dir := resumeFixtureServer(t)
		target := s.Observe(fixture.command)
		if target == "" {
			t.Fatal("Claude action unavailable")
		}
		if err := dispatchResumeFixture(t, s, target, dir, false, "", 0, f.editor); err != nil {
			t.Fatal(err)
		}
		f.wait(t, fixture.event)
		f.wait(t, "FIXTURE_EDITOR_READY")
		f.snapshot(t, "")
	}
}

// QA 3: arbitrary text and string-control payloads are not action metadata.
func TestClaudeResumeLinksRejectUnrelatedAndControlledText(t *testing.T) {
	for _, input := range []string{
		testClaudeResumeCommand + "\n",
		"Quoted: Resume this session with:\n" + testClaudeResumeCommand + "\n",
		"Resume this session with: extra\n" + testClaudeResumeCommand + "\n",
		"Resume this session with:\nprivate line\n" + testClaudeResumeCommand + "\n",
		"Resume this session with:\n" + testClaudeResumeCommand + "; printf bad\n",
		"Resume this session with:\nclaude --worktree \"private notes\" --resume " + testClaudeResumeID + "\n",
		"Resume this session with:\nclaude --resu\x1b[2Hme " + testClaudeResumeID + "\n",
		"\x1b]0;Resume this session with:\n" + testClaudeResumeCommand + "\a",
		"\x1bPResume this session with:\n" + testClaudeResumeCommand + "\x1b\\",
		"Resume this session with:\n\x1bPResume this session with:\n" + testClaudeResumeCommand + "\n\x1b\\",
		"Resume this \x1b]0;caption\asession with:\n" + testClaudeResumeCommand + "\n",
	} {
		for split := 0; split <= len(input); split++ {
			observed := 0
			l := newCodexResumeLinkifier(func(string) string { observed++; return "vuja://claude-resume/" + testClaudeResumeID })
			got := string(l.Transform([]byte(input[:split]))) + string(l.Transform([]byte(input[split:]))) + string(l.Flush())
			if observed != 0 || got != input {
				t.Fatalf("split %d promoted non-instruction: %q", split, got)
			}
		}
	}
}

func TestClaudeResumeLinksBoundControlPayloads(t *testing.T) {
	observed := 0
	l := newCodexResumeLinkifier(func(string) string {
		observed++
		return "vuja://claude-resume/" + testClaudeResumeID
	})
	chunks := []string{"Resume this session with:\n\x1b]0;", strings.Repeat("x", codexResumeLineLimit*2), "\nResume this session with:\n" + testClaudeResumeCommand + "\n", "\a\nResume this session with:\n" + testClaudeResumeCommand + "\n"}
	for i, chunk := range chunks {
		l.Transform([]byte(chunk))
		if len(l.line) > codexResumeLineLimit {
			t.Fatal("unterminated control payload bypassed the bounded resume buffer")
		}
		if i < len(chunks)-1 && observed != 0 {
			t.Fatal("hidden control-string payload became actionable")
		}
	}
	if observed != 1 {
		t.Fatal("valid instruction after oversized control did not recover")
	}
}

func TestClaudeResumeRejectsTamperedAndUnobservedTargets(t *testing.T) {
	s, dir := resumeFixtureServer(t)
	command := "claude --worktree \"tree\" --resume " + testClaudeResumeID
	target := s.Observe(command)
	if target == "" {
		t.Fatal("Claude action unavailable")
	}
	for _, mutate := range []func(*url.URL){
		func(u *url.URL) { u.Host = "codex-resume" },
		func(u *url.URL) { u.Host = "unknown-resume" },
		func(u *url.URL) { u.Path = "/" + testCodexResumeID },
		func(u *url.URL) {
			q := u.Query()
			q.Set("command", testClaudeResumeCommand+"; printf bad")
			u.RawQuery = q.Encode()
		},
		func(u *url.URL) {
			q := u.Query()
			q.Set("command", "claude --worktree \"other\" --resume "+testClaudeResumeID)
			u.RawQuery = q.Encode()
		},
		func(u *url.URL) { q := u.Query(); q.Set("token", strings.Repeat("f", 32)); u.RawQuery = q.Encode() },
		func(u *url.URL) { q := u.Query(); q.Set("socket", "/outside/actions.sock"); u.RawQuery = q.Encode() },
	} {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		mutate(u)
		if err := dispatchCodexResumeURL(u.String(), dir); err == nil {
			t.Fatal("tampered or unobserved action was accepted")
		}
		select {
		case <-s.Actions():
			t.Fatal("rejected target reached shell consumer")
		default:
		}
	}
	var input bytes.Buffer
	if err := dispatchResumeFixture(t, s, target, dir, false, "", 0, &input); err != nil || input.String() != command+"\r" {
		t.Fatal("invalid requests poisoned the valid Claude capability")
	}
}

// QA 5: admission still belongs to the complete real editor, not parent state.
func TestClaudeResumeShellRejectsEditorTextAndAmbiguousReplay(t *testing.T) {
	f := newResumeShellFixture(t, `claude() { print -u 3 -N -r -- "FIXTURE_CLAUDE:$#:${(j:|:)@}"; }`)
	s, dir := resumeFixtureServer(t)
	target := s.Observe(testClaudeResumeCommand)
	if _, err := f.terminal.Write([]byte("; printf untouched\x01")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "; printf untouched")
	if err := dispatchResumeFixture(t, s, target, dir, false, "", 0, f.editor); err == nil {
		t.Fatal("hidden editor suffix was accepted")
	}
	f.snapshot(t, "; printf untouched")
	if _, err := f.terminal.Write([]byte("\x05\x15")); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, "")
	f.dropSubmitted.Store(true)
	if err := dispatchResumeFixture(t, s, target, dir, false, "", 0, f.editor); err == nil {
		t.Fatal("lost acknowledgement falsely confirmed execution")
	}
	f.wait(t, "FIXTURE_CLAUDE:2:--resume|"+testClaudeResumeID)
	if err := dispatchCodexResumeURL(target, dir); err == nil {
		t.Fatal("ambiguous Claude action was rearmed")
	}
	f.wait(t, "FIXTURE_EDITOR_READY")
	f.snapshot(t, "")
	for _, shell := range []string{"bash", "fish"} {
		a := &codexResumeAction{id: testClaudeResumeID, command: testClaudeResumeCommand, deadline: time.Now().Add(time.Second), result: make(chan codexResumeResult, 1)}
		if got := submitCodexResumeAction(a, false, &shellResumeEditor{shell: shell}, nil); got.err == nil || got.consumed {
			t.Fatal("unsupported editor accepted Claude action")
		}
	}
}
