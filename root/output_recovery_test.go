package root

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryCompositor(t *testing.T, dir, pane, marker string, linkFor func(string) string) (*terminalCompositor, *bytes.Buffer) {
	t.Helper()
	out := new(bytes.Buffer)
	c := newTerminalCompositor(out, "bottom", marker, 100, 14)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100})
	if err := c.ConfigureOutputRecovery(dir, pane, linkFor); err != nil {
		t.Fatal(err)
	}
	c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	return c, out
}

func TestOutputRecoveryFencesOldInstancesAndLateClose(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	a.WritePTY([]byte("first-result\r\n"))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	b, _ := recoveryCompositor(t, dir, "pane", "b", nil)
	b.WritePTY(terminalMarkerBytes("b", "command-start"))
	b.WritePTY([]byte("newer-result\r\n"))
	b.WritePTY(terminalMarkerBytes("b", "command-end:0"))
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	a.WritePTY([]byte("stale-result\r\n"))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	// Once displaced, the old instance must not reclaim a checkpoint even if
	// retention subsequently removes the newer instance's file.
	if err := os.Remove(b.outputRecovery.path); err != nil {
		t.Fatal(err)
	}
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	a.WritePTY([]byte("displaced-result\r\n"))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	if _, err := os.Stat(b.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("displaced instance reclaimed the evicted pane")
	}
	b.WritePTY(terminalMarkerBytes("b", "command-start"))
	b.WritePTY([]byte("newer-result\r\n"))
	b.WritePTY(terminalMarkerBytes("b", "command-end:0"))
	a.Close()
	a.Close()
	_, out := recoveryCompositor(t, dir, "pane", "c", nil)
	if !strings.Contains(out.String(), "newer-result") || strings.Contains(out.String(), "stale-result") {
		t.Fatal("old instance replaced a newer checkpoint")
	}
}

func TestOutputRecoveryRebindsObservedLinksWithoutPersistingCredentials(t *testing.T) {
	dir := t.TempDir()
	oldLink := "vuja://codex-resume/" + testCodexResumeID + "?socket=old-socket&token=old-secret"
	a, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	l := newCodexResumeLinkifier(func(string) string { return oldLink })
	for _, text := range []string{"To continue this session, run codex resume ", testCodexResumeID, "\r\n"} {
		a.WritePTY(l.Transform([]byte(text)))
	}
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	data, err := os.ReadFile(a.outputRecovery.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("old-secret")) || bytes.Contains(data, []byte("old-socket")) {
		t.Fatal("checkpoint contains expired action credentials")
	}
	_, out := recoveryCompositor(t, dir, "pane", "b", func(id string) string { return "vuja://codex-resume/" + id + "?token=new-secret" })
	if !strings.Contains(out.String(), "\x1b]8;;vuja://codex-resume/"+testCodexResumeID+"?token=new-secret\a") || strings.Contains(out.String(), "old-secret") {
		t.Fatalf("restored wire hyperlink did not use fresh fixture credentials: %q", out.String())
	}
	_, plain := recoveryCompositor(t, dir, "pane", "c", nil)
	if strings.Contains(plain.String(), "vuja://codex-resume/") || !strings.Contains(plain.String(), testCodexResumeID) {
		t.Fatal("missing action server must preserve copyable text without a dead link")
	}
}

func TestOutputRecoveryFreshActionDispatchesOnlyRecoveredID(t *testing.T) {
	actions, err := os.MkdirTemp("/tmp", "vjor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(actions) })
	old, err := newCodexResumeActionServerIn(actions, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Close)
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "a", old.Observe)
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	l := newCodexResumeLinkifier(old.Observe)
	a.WritePTY(l.Transform([]byte("Session ID: " + testCodexResumeID + "\r\n")))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	old.Close()
	fresh, err := newCodexResumeActionServerIn(actions, "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	var action string
	_, out := recoveryCompositor(t, dir, "pane", "b", func(id string) string { action = fresh.Observe(id); return action })
	if action == "" || !strings.Contains(out.String(), action) {
		t.Fatal("restored session was not registered with the new action server")
	}
	if err := dispatchCodexResumeURL(action, actions); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-fresh.Actions():
		if id != testCodexResumeID {
			t.Fatal("wrong resumed session")
		}
	case <-time.After(time.Second):
		t.Fatal("restored action did not dispatch")
	}
}

func TestOutputRecoveryFailureDoesNotSuppressLiveOutput(t *testing.T) {
	dir := t.TempDir()
	a, out := recoveryCompositor(t, dir, "pane", "a", nil)
	if err := os.Remove(a.outputRecovery.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.outputRecovery.path, 0o700); err != nil {
		t.Fatal(err)
	}
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	a.WritePTY([]byte("live-result\r\n"))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	a.WritePTY(terminalMarkerBytes("a", "prompt-start"))
	a.WritePTY([]byte("› next"))
	a.WritePTY(terminalMarkerBytes("a", "prompt-end"))
	if !strings.Contains(out.String(), "live-result") || !strings.Contains(out.String(), "output recovery unavailable") || !strings.Contains(out.String(), "next") {
		t.Fatal("failed checkpoint suppressed output or the next prompt")
	}
}

func TestOutputRecoveryBoundsPrivateStorageAndRejectsCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	c, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	r := c.outputRecovery
	rows := make([]string, 10000)
	for i := range rows {
		rows[i] = strings.Repeat("bounded-data", 20)
	}
	rows[len(rows)-1] = "newest"
	if err := r.locked(func() error { return r.write(rows) }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > outputRecoveryByteLimit || info.Mode().Perm() != 0o600 {
		t.Fatal("checkpoint is unbounded or not private")
	}
	info, err = os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("recovery directory is not private")
	}
	state, err := r.read()
	if err != nil || state.Rows[len(state.Rows)-1] != "newest" || len(state.Rows) >= len(rows) {
		t.Fatal("byte retention did not preserve newest complete rows")
	}
	if err := os.WriteFile(r.path, []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	b := newTerminalCompositor(&out, "bottom", "b", 100, 14)
	t.Cleanup(b.Close)
	b.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	if err := b.ConfigureOutputRecovery(dir, "pane", nil); err == nil {
		t.Fatal("corrupt checkpoint must not be replayed or silently replaced")
	}
	data, _ := os.ReadFile(r.path)
	if string(data) != "{truncated" {
		t.Fatal("failed restore destroyed existing evidence")
	}
}

func TestOutputRecoveryStripsUnsafeControls(t *testing.T) {
	row := "safe\x1b]52;c;secret\a\x1b[2J\x1b]0;title\a\x1b[31mred\x1b[0m"
	got := recoveryRow(row, nil, false)
	if strings.Contains(got, "\x1b]52") || strings.Contains(got, "[2J") || strings.Contains(got, "secret") || !strings.Contains(got, "\x1b[31mred") {
		t.Fatalf("unsafe control replay: %q", got)
	}
	encoded, _ := json.Marshal(outputRecoveryState{Version: 1, Rows: []string{row}})
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	if err := os.WriteFile(c.outputRecovery.path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := recoveryCompositor(t, dir, "pane", "b", nil)
	if strings.Contains(out.String(), "\x1b]52") || strings.Contains(out.String(), "\x1b]0;title") {
		t.Fatal("disk state bypassed replay sanitization")
	}
}

func TestOutputRecoveryRejectsNonregularCheckpointWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	if err := os.Remove(c.outputRecovery.path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(c.outputRecovery.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.outputRecovery.read(); err == nil {
		t.Fatal("FIFO checkpoint was accepted")
	}
}

func TestOutputRecoveryRemovesInterruptedWritesWithoutLosingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "a", nil)
	a.WritePTY(terminalMarkerBytes("a", "command-start"))
	a.WritePTY([]byte("completed-before-interruption\r\n"))
	a.WritePTY(terminalMarkerBytes("a", "command-end:0"))
	temp, err := os.CreateTemp(dir, ".checkpoint-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := temp.WriteString("unfinished-private-output"); err != nil {
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, ".checkpoint-unrelated")
	link := filepath.Join(dir, ".checkpoint-123456788")
	directory := filepath.Join(dir, ".checkpoint-123456787")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a.outputRecovery.path, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, out := recoveryCompositor(t, dir, "pane", "b", nil)
	if _, err := os.Stat(temp.Name()); !os.IsNotExist(err) {
		t.Fatal("interrupted checkpoint escaped bounded retention")
	}
	if !strings.Contains(out.String(), "completed-before-interruption") || strings.Contains(out.String(), "unfinished-private-output") {
		t.Fatal("interrupted write replaced completed recovery output")
	}
	for _, path := range []string{unrelated, link, directory} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("cleanup removed an unrelated or nonregular entry")
		}
	}
}

func TestOutputRecoveryUsesStablePaneGUIDAndDoesNotGuess(t *testing.T) {
	for _, key := range []string{"ITERM_SESSION_ID", "TERM_SESSION_ID"} {
		id := outputRecoveryPaneID(func(name string) string {
			if name == key {
				return "w3t8p2:" + testCodexResumeID
			}
			return ""
		})
		moved := outputRecoveryPaneID(func(name string) string {
			if name == key {
				return "w1t0p0:" + testCodexResumeID
			}
			return ""
		})
		if id != testCodexResumeID || moved != id {
			t.Fatal("moving a pane changed recovery ownership")
		}
	}
	if outputRecoveryPaneID(func(string) string { return "unknown-pane" }) != "" {
		t.Fatal("unknown terminal identity must not select shared history")
	}
}

func TestOutputRecoveryDoesNotUseInheritedTerminalIdentityInTmux(t *testing.T) {
	for _, pane := range []string{"%1", "%2"} {
		id := outputRecoveryPaneID(func(name string) string {
			switch name {
			case "TMUX":
				return "/tmp/tmux-fixture/default,123,0"
			case "TMUX_PANE":
				return pane
			case "ITERM_SESSION_ID", "TERM_SESSION_ID":
				return "w1t0p0:" + testCodexResumeID
			}
			return ""
		})
		if id != "" {
			t.Fatal("tmux pane inherited the outer terminal's recovery ownership")
		}
	}
}

func TestOutputRecoveryPaneRetentionAndUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	first, _ := recoveryCompositor(t, dir, "oldest", "first", nil)
	for i := 0; i <= outputRecoveryPaneLimit; i++ {
		c, _ := recoveryCompositor(t, dir, fmt.Sprintf("pane-%d", i), "a", nil)
		c.Close()
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) != outputRecoveryPaneLimit {
		t.Fatal("pane checkpoint retention is not bounded")
	}
	first.WritePTY(terminalMarkerBytes("first", "command-start"))
	first.WritePTY([]byte("recent-after-eviction\r\n"))
	first.WritePTY(terminalMarkerBytes("first", "command-end:0"))
	_, restored := recoveryCompositor(t, dir, "oldest", "restart", nil)
	if !strings.Contains(restored.String(), "recent-after-eviction") {
		t.Fatal("an evicted live pane cannot save its new completed output")
	}
	entries, _ = filepath.Glob(filepath.Join(dir, "*.json"))
	if len(entries) != outputRecoveryPaneLimit {
		t.Fatal("re-entry exceeded pane retention")
	}
	out := new(bytes.Buffer)
	c := newTerminalCompositor(out, "bottom", "unsafe", 100, 14)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfig{OutputViewport: "pinned", OutputLines: 100})
	unsafe := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, unsafe); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfigureOutputRecovery(unsafe, "pane", nil); err == nil {
		t.Fatal("symlink recovery directory was accepted")
	}
}

func TestOutputRecoverySurvivesRestartWithoutClose(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane-a", "old", nil)
	a.WritePTY(terminalMarkerBytes("old", "command-start"))
	a.WritePTY([]byte("retained-result\r\nTo reconnect, run:\r\n  codex resume " + testCodexResumeID + "\r\n"))
	a.WritePTY(terminalMarkerBytes("old", "command-end:0"))
	// A is deliberately still alive: a shutdown-only save cannot pass.
	b, out := recoveryCompositor(t, dir, "pane-a", "new", func(id string) string { return "vuja://codex-resume/" + id + "?token=fresh" })
	_ = b
	if !strings.Contains(out.String(), "retained-result") || !strings.Contains(out.String(), testCodexResumeID) {
		t.Fatal("restart lost completed output and the Codex resume instruction")
	}
	_, other := recoveryCompositor(t, dir, "pane-b", "other", nil)
	if strings.Contains(other.String(), testCodexResumeID) {
		t.Fatal("another pane inherited the resume instruction")
	}
}
