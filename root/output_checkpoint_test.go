package root

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/spf13/cobra"
)

func TestOutputCheckpointRestoreLabelsDisplayOnly(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "old", nil)
	a.WritePTY(terminalMarkerBytes("old", "command-start"))
	a.WritePTY([]byte("saved-result\r\n"))
	a.WritePTY(terminalMarkerBytes("old", "command-end:0"))
	a.Close()
	_, out := recoveryCompositor(t, dir, "pane", "new", nil)
	if !strings.Contains(out.String(), "Output restored; process not resumed") || !strings.Contains(out.String(), "saved-result") {
		t.Fatal("restored output does not distinguish display data from a resumed process")
	}
}

func TestOutputCheckpointRunningPolicySavesBeforeCompletion(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'running'\noutput-checkpoint-interval = '5s'\n"))
	if err != nil {
		t.Fatalf("running-output checkpoint configuration rejected: %v", err)
	}
	a, _ := recoveryCompositor(t, t.TempDir(), "pane", "running", nil)
	a.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.WritePTY(terminalMarkerBytes("running", "command-start"))
	a.WritePTY([]byte("unfinished-result\r\n"))
	now = now.Add(5 * time.Second)
	a.RefreshRunningStatus()
	awaitCheckpointRow(t, a.outputRecovery.path, "unfinished-result")
}

func TestOutputCheckpointOffPolicyDoesNotCreateOutputStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'off'\n"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "off", 100, 14)
	t.Cleanup(c.Close)
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	if err := c.ConfigureOutputRecovery(dir, "pane", nil); err != nil {
		t.Fatal(err)
	}
	c.WritePTY(terminalMarkerBytes("off", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("off", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("off", "command-start"))
	c.WritePTY([]byte("private-live-output\r\n"))
	c.WritePTY(terminalMarkerBytes("off", "command-end:0"))
	c.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("disabled output checkpoints created persistent storage")
	}
}

func TestOutputCheckpointCompletedPolicyDoesNotSaveUnfinishedOutput(t *testing.T) {
	a, _ := recoveryCompositor(t, t.TempDir(), "pane", "completed", nil)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.WritePTY(terminalMarkerBytes("completed", "command-start"))
	a.WritePTY([]byte("completed-only-output\r\n"))
	now = now.Add(time.Minute)
	a.RefreshRunningStatus()
	state, err := a.outputRecovery.read()
	if err != nil || strings.Contains(strings.Join(state.Rows, "\n"), "completed-only-output") {
		t.Fatal("completed-only policy saved unfinished output")
	}
	a.WritePTY(terminalMarkerBytes("completed", "command-end:0"))
	awaitCheckpointRow(t, a.outputRecovery.path, "completed-only-output")
}

func TestOutputCheckpointRunningPolicySkipsUnchangedOutput(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'running'\noutput-checkpoint-interval = '5s'\n"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := recoveryCompositor(t, t.TempDir(), "pane", "unchanged", nil)
	a.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.WritePTY(terminalMarkerBytes("unchanged", "command-start"))
	a.WritePTY([]byte("unchanged-output\r\n"))
	now = now.Add(5 * time.Second)
	a.RefreshRunningStatus()
	awaitCheckpointRow(t, a.outputRecovery.path, "unchanged-output")
	old := time.Unix(1000, 0)
	if err := os.Chtimes(a.outputRecovery.path, old, old); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	a.RefreshRunningStatus()
	a.Close()
	info, err := os.Stat(a.outputRecovery.path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatal("unchanged running output was written again")
	}
}

func awaitCheckpointRow(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		data, err := os.ReadFile(path)
		var state outputRecoveryState
		if err == nil && json.Unmarshal(data, &state) == nil && strings.Contains(strings.Join(state.Rows, "\n"), want) {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("eligible output was not checkpointed before command completion")
		}
	}
}

func awaitCheckpointState(t *testing.T, c *terminalCompositor, ready func(*outputRecovery) bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		c.mu.Lock()
		ok := ready(c.outputRecovery)
		c.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("checkpoint state did not settle")
		}
	}
}

func TestOutputCheckpointWriterCoalescesWhileStorageIsBlocked(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var saved []string
	w := newOutputCheckpointWriter(func(snapshot outputCheckpointSnapshot) {
		if snapshot.rows[0] == "first" {
			close(entered)
			<-release
		}
		saved = append(saved, snapshot.rows[0])
	})
	w.submit(outputCheckpointSnapshot{rows: []string{"first"}})
	<-entered
	w.submit(outputCheckpointSnapshot{rows: []string{"superseded"}})
	w.submit(outputCheckpointSnapshot{rows: []string{"latest"}})
	close(release)
	w.stop(true)
	if !slices.Equal(saved, []string{"first", "latest"}) {
		t.Fatalf("slow storage retained an unbounded or stale snapshot queue: %v", saved)
	}
}

func TestOutputCheckpointBlockedStorageDoesNotOwnTheCompositor(t *testing.T) {
	a, _ := recoveryCompositor(t, t.TempDir(), "pane", "blocked", nil)
	w := a.outputRecovery.writer
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	save := w.save
	w.save = func(snapshot outputCheckpointSnapshot) {
		once.Do(func() { close(entered); <-release })
		save(snapshot)
	}
	w.mu.Unlock()
	defer close(release)
	a.WritePTY(terminalMarkerBytes("blocked", "command-start"))
	a.WritePTY([]byte("saved-after-release\r\n"))
	completed := make(chan struct{})
	go func() {
		a.WritePTY(terminalMarkerBytes("blocked", "command-end:0"))
		close(completed)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("checkpoint did not reach the controlled storage barrier")
	}
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("command completion waited for blocked storage")
	}
	progress := make(chan struct{})
	go func() {
		a.WriteNotification([]byte("rendering-while-storage-blocked\r\n"))
		close(progress)
	}()
	select {
	case <-progress:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked storage held the compositor rendering lock")
	}
}

func TestOutputCheckpointRejectsDuplicatePaneConfiguration(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "reconfigure", nil)
	previous := a.outputRecovery.writer
	if err := a.ConfigureOutputRecovery(dir, "pane", nil); err == nil {
		// Release the pre-fix orphan too; a failed proving fixture must not
		// leak its goroutine into another test's process.
		previous.stop(false)
		t.Fatal("a second pane configuration replaced the existing recovery owner")
	}
	if a.outputRecovery.writer != previous {
		t.Fatal("rejected configuration changed the existing writer")
	}
}

func TestOutputCheckpointDisableDrainsOwnershipOutsideTheRenderer(t *testing.T) {
	a, _ := recoveryCompositor(t, t.TempDir(), "pane", "disable", nil)
	w := a.outputRecovery.writer
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	save := w.save
	w.save = func(snapshot outputCheckpointSnapshot) {
		once.Do(func() { close(entered); <-release })
		save(snapshot)
	}
	w.mu.Unlock()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	a.WritePTY(terminalMarkerBytes("disable", "command-start"))
	a.WritePTY([]byte("private-pending-output\r\n"))
	a.WritePTY(terminalMarkerBytes("disable", "command-end:0"))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pending checkpoint never reached storage barrier")
	}
	a.mu.Lock()
	policy := a.chatboxConfig
	a.mu.Unlock()
	policy.OutputCheckpoints = "off"
	disabled := make(chan struct{})
	go func() { a.SetChatboxConfig(policy); close(disabled) }()
	awaitCheckpointState(t, a, func(*outputRecovery) bool { return a.chatboxConfig.OutputCheckpoints == "off" })
	progress := make(chan struct{})
	go func() { a.WriteNotification([]byte("live-during-disable\r\n")); close(progress) }()
	select {
	case <-progress:
	case <-time.After(5 * time.Second):
		t.Fatal("disabling checkpoints held the rendering lock while storage was blocked")
	}
	unblock()
	select {
	case <-disabled:
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not finish after storage was released")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("disabled checkpoint policy left its writer running")
	}
	state, err := a.outputRecovery.read()
	if err != nil || strings.Contains(strings.Join(state.Rows, "\n"), "private-pending-output") {
		t.Fatal("a pending write undid disabled checkpoint recording")
	}
}

func TestOutputCheckpointAgeRetentionUsesSavedTimeAndStrictCutoff(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoint-max-age = '24h'\n"))
			if err != nil {
				t.Fatalf("checkpoint retention configuration rejected: %v", err)
			}
			dir := t.TempDir()
			writeCheckpointFixture(t, dir, "pane", "age-sensitive-output", now.Add(-24*time.Hour+delta))
			_, out := configuredCheckpointCompositor(t, cfg, dir, "pane", "age", now)
			if got, want := strings.Contains(out.String(), "age-sensitive-output"), delta >= 0; got != want {
				t.Fatalf("checkpoint cutoff restore=%v, want %v", got, want)
			}
		})
	}
}

func TestOutputCheckpointPaneClaimDoesNotRenewOldOutputAge(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoint-max-age = '24h'\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeCheckpointFixture(t, dir, "pane", "old-output-not-a-new-save", now.Add(-23*time.Hour))
	a, out := configuredCheckpointCompositor(t, cfg, dir, "pane", "first", now)
	if !strings.Contains(out.String(), "old-output-not-a-new-save") {
		t.Fatal("unexpired output was not restored")
	}
	a.Close()
	_, next := configuredCheckpointCompositor(t, cfg, dir, "pane", "next", now.Add(2*time.Hour))
	if strings.Contains(next.String(), "old-output-not-a-new-save") {
		t.Fatal("claiming the pane renewed the age of old output")
	}
}

func TestOutputCheckpointConfiguredPaneCountBoundsStorage(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoint-panes = 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		c, _ := configuredCheckpointCompositor(t, cfg, dir, fmt.Sprint(i), "count", time.Now())
		c.Close()
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("configured checkpoint retention did not bound pane files: %d, %v", len(files), err)
	}
}

func configuredCheckpointCompositor(t *testing.T, cfg *config.Config, dir, pane, marker string, now time.Time) (*terminalCompositor, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", marker, 100, 14)
	t.Cleanup(c.Close)
	c.now = func() time.Time { return now }
	c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
	if err := c.ConfigureOutputRecovery(dir, pane, nil); err != nil {
		t.Fatal(err)
	}
	c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
	return c, &out
}

func writeCheckpointFixture(t *testing.T, dir, pane, text string, savedAt time.Time) {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(pane))))
	// Version-one files predate explicit save timestamps; filesystem mtime is
	// their migration evidence, not an invented fresh time at every restore.
	data, err := json.Marshal(map[string]any{"version": 1, "owner": "previous", "rows": []string{text}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, savedAt, savedAt); err != nil {
		t.Fatal(err)
	}
}

func TestOutputCheckpointClearPreviewsConfirmsAndFencesPendingWriter(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	statePath, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(statePath), "terminal-output")
	a, _ := recoveryCompositor(t, dir, "pane", "clear", nil)
	w := a.outputRecovery.writer
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mu.Lock()
	save := w.save
	w.save = func(snapshot outputCheckpointSnapshot) {
		if strings.Contains(strings.Join(snapshot.rows, "\n"), "pending-before-clear") {
			once.Do(func() { close(entered); <-release })
		}
		save(snapshot)
	}
	w.mu.Unlock()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	a.WritePTY(terminalMarkerBytes("clear", "command-start"))
	a.WritePTY([]byte("saved-before-clear\r\n"))
	a.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
	awaitCheckpointRow(t, a.outputRecovery.path, "saved-before-clear")
	history := filepath.Join(filepath.Dir(statePath), "history.db")
	if err := os.WriteFile(history, []byte("independent-history-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.WritePTY(terminalMarkerBytes("clear", "prompt-start"))
	a.WritePTY([]byte("› "))
	a.WritePTY(terminalMarkerBytes("clear", "prompt-end"))
	a.WritePTY(terminalMarkerBytes("clear", "command-start"))
	a.WritePTY([]byte("pending-before-clear\r\n"))
	a.WritePTY(terminalMarkerBytes("clear", "command-end:0"))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pending writer did not reach its storage barrier")
	}
	command, _, err := rootCmd.Find([]string{"output", "checkpoints", "clear"})
	if err != nil || !strings.HasSuffix(command.CommandPath(), "output checkpoints clear") {
		t.Fatal("explicit output-checkpoint clearing is unavailable")
	}
	oldOut := command.OutOrStdout()
	oldAll, _ := command.Flags().GetBool("all")
	oldRestarted, _ := command.Flags().GetBool("old-sessions-restarted")
	oldConfirm, _ := command.Flags().GetString("confirm")
	t.Cleanup(func() {
		command.SetOut(oldOut)
		_ = command.Flags().Set("all", fmt.Sprint(oldAll))
		_ = command.Flags().Set("old-sessions-restarted", fmt.Sprint(oldRestarted))
		_ = command.Flags().Set("confirm", oldConfirm)
	})
	var preview bytes.Buffer
	command.SetOut(&preview)
	if err := command.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.outputRecovery.path); err != nil {
		t.Fatal("checkpoint preview removed output before confirmation")
	}
	_, confirmation, ok := strings.Cut(preview.String(), "--confirm ")
	if !ok || len(strings.Fields(confirmation)) == 0 {
		t.Fatal("checkpoint preview omitted a usable confirmation")
	}
	if strings.Contains(preview.String(), "saved-before-clear") || strings.Contains(preview.String(), "pending-before-clear") {
		t.Fatal("checkpoint inventory exposed private output")
	}
	if err := command.Flags().Set("confirm", strings.Fields(confirmation)[0]); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("old-sessions-restarted", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("confirmed checkpoint clear did not remove its selected output")
	}
	data, err := os.ReadFile(history)
	if err != nil || string(data) != "independent-history-sentinel" {
		t.Fatal("output checkpoint clear changed command history")
	}
	unblock()
	a.Close()
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("a queued writer recreated confirmed-cleared output")
	}
}

func TestOutputCheckpointClearRejectsReplacedPaneOwner(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	state, _ := config.StatePath()
	dir := filepath.Join(filepath.Dir(state), "terminal-output")
	a, _ := recoveryCompositor(t, dir, "pane", "old", nil)
	command := newOutputCheckpointClearCommand()
	if err := command.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	token := checkpointClearPreview(t, command)
	b, _ := recoveryCompositor(t, dir, "pane", "new", nil)
	if err := command.Flags().Set("confirm", token); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err == nil {
		t.Fatal("stale confirmation cleared a newly claimed pane")
	}
	if _, err := os.Stat(b.outputRecovery.path); err != nil {
		t.Fatal("rejected confirmation removed the new pane's checkpoint")
	}
	a.Close()
}

func TestOutputCheckpointClearCurrentPanePreservesOtherPanes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("ITERM_SESSION_ID", "w1t0p0:"+testCodexResumeID)
	t.Setenv("TMUX", "")
	state, _ := config.StatePath()
	dir := filepath.Join(filepath.Dir(state), "terminal-output")
	a, _ := recoveryCompositor(t, dir, testCodexResumeID, "current", nil)
	b, _ := recoveryCompositor(t, dir, "other-pane", "other", nil)
	interrupted := filepath.Join(dir, ".checkpoint-123")
	if err := os.WriteFile(interrupted, []byte("unattributed-private-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newOutputCheckpointClearCommand()
	token := checkpointClearPreview(t, command)
	var preview bytes.Buffer
	command.SetOut(&preview)
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.String(), "cannot be attributed to a pane; use --all") {
		t.Fatal("current-pane clear silently ignored unattributed interrupted output")
	}
	if err := command.Flags().Set("confirm", token); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("current-pane clear retained its selected checkpoint")
	}
	if _, err := os.Stat(b.outputRecovery.path); err != nil {
		t.Fatal("current-pane clear removed another pane's output")
	}
	if _, err := os.Stat(interrupted); err != nil {
		t.Fatal("current-pane clear silently broadened its scope to unattributed output")
	}
}

func TestOutputCheckpointOffPolicyDoesNotRestoreExistingOutput(t *testing.T) {
	dir := t.TempDir()
	writeCheckpointFixture(t, dir, "pane", "private-output-not-to-restore", time.Now())
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'off'\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, out := configuredCheckpointCompositor(t, cfg, dir, "pane", "off", time.Now())
	if strings.Contains(out.String(), "private-output-not-to-restore") {
		t.Fatal("disabled checkpoint recording restored existing private output")
	}
}

func TestOutputCheckpointRunningPolicyLeavesNativeApplicationCanvasAlone(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'running'\noutput-checkpoint-interval = '5s'\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a, _ := configuredCheckpointCompositor(t, cfg, t.TempDir(), "pane", "native", now)
	a.now = func() time.Time { return now }
	a.WritePTY(terminalMarkerBytes("native", "command-start"))
	a.WritePTY([]byte("owned-before-native\r\n\x1b[?1049hprivate-application-canvas"))
	now = now.Add(time.Minute)
	a.RefreshRunningStatus()
	state, err := a.outputRecovery.read()
	if err != nil || strings.Contains(strings.Join(state.Rows, "\n"), "private-application-canvas") {
		t.Fatal("periodic checkpoint captured native application screen data")
	}
}

func TestOutputCheckpointNeverPersistsNativeCanvasAtCompletionOrLater(t *testing.T) {
	for _, mode := range []string{"completed", "running"} {
		for _, application := range []string{"primary", "unterminated-alternate"} {
			for _, laterOwnedCommand := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/later-%v", mode, application, laterOwnedCommand), func(t *testing.T) {
					cfg, err := config.Decode([]byte(fmt.Sprintf("[ui.chatbox]\noutput-checkpoints = %q\n", mode)))
					if err != nil {
						t.Fatal(err)
					}
					dir := t.TempDir()
					a, _ := configuredCheckpointCompositor(t, cfg, dir, "pane", "native-end", time.Now())
					a.WritePTY(terminalMarkerBytes("native-end", "command-start"))
					a.WritePTY([]byte("eligible-owned-output\r\n"))
					a.WritePTY(terminalMarkerBytes("native-end", "command-end:0"))
					awaitCheckpointRow(t, a.outputRecovery.path, "eligible-owned-output")
					a.WritePTY(terminalMarkerBytes("native-end", "prompt-start"))
					a.WritePTY([]byte("› "))
					a.WritePTY(terminalMarkerBytes("native-end", "prompt-end"))
					a.WritePTY(terminalMarkerBytes("native-end", "command-start"))
					canvas := "\x1b[2;2Hprivate-native-canvas\r\n"
					if application == "unterminated-alternate" {
						canvas = "\x1b[?1049hprivate-native-canvas"
					}
					a.WritePTY([]byte(canvas))
					a.WritePTY(terminalMarkerBytes("native-end", "command-end:0"))
					if laterOwnedCommand {
						a.WritePTY(terminalMarkerBytes("native-end", "prompt-start"))
						a.WritePTY([]byte("› "))
						a.WritePTY(terminalMarkerBytes("native-end", "prompt-end"))
						a.WritePTY(terminalMarkerBytes("native-end", "command-start"))
						a.WritePTY([]byte("owned-after-application\r\n"))
						a.WritePTY(terminalMarkerBytes("native-end", "command-end:0"))
					}
					a.Close()
					data, err := os.ReadFile(a.outputRecovery.path)
					if err != nil || !bytes.Contains(data, []byte("eligible-owned-output")) || bytes.Contains(data, []byte("private-native-canvas")) {
						t.Fatal("completion checkpoint lost eligible output or persisted a native application canvas")
					}
					if laterOwnedCommand && !bytes.Contains(data, []byte("owned-after-application")) {
						t.Fatal("native exclusion disabled later owned-output recovery")
					}
					reopened, _ := recoveryCompositor(t, dir, "pane", "reopen-native", nil)
					// Block labels can push the oldest saved line above the viewport.
					// Inspect retained text, not only the currently painted rows.
					restored := ansi.Strip(strings.Join(retainedModelRows(reopened.viewport.model), "\n"))
					if !strings.Contains(restored, "eligible-owned-output") || strings.Contains(restored, "private-native-canvas") {
						t.Fatal("reopened pane restored private application canvas or lost owned output")
					}
				})
			}
		}
	}
}

func TestOutputCheckpointExpiredPaneCannotBeResurrectedByPendingWriter(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	writeCheckpointFixture(t, dir, "expired", "expired-output", now.Add(-25*time.Hour))
	a, _ := recoveryCompositor(t, dir, "expired", "old", nil)
	w := a.outputRecovery.writer
	entered, release := make(chan struct{}), make(chan struct{})
	w.mu.Lock()
	save := w.save
	w.save = func(snapshot outputCheckpointSnapshot) {
		close(entered)
		<-release
		save(snapshot)
	}
	w.mu.Unlock()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	a.WritePTY(terminalMarkerBytes("old", "command-start"))
	a.WritePTY([]byte("queued-before-expiry\r\n"))
	a.WritePTY(terminalMarkerBytes("old", "command-end:0"))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pending checkpoint did not reach the storage barrier")
	}
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoint-max-age = '24h'\n"))
	if err != nil {
		t.Fatal(err)
	}
	configuredCheckpointCompositor(t, cfg, dir, "other", "pruner", now)
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("age maintenance did not remove the expired pane")
	}
	unblock()
	a.Close()
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("a queued old writer resurrected expired output")
	}
}

func checkpointClearPreview(t *testing.T, command *cobra.Command) string {
	t.Helper()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(out.String(), "--confirm ")
	if !ok || len(strings.Fields(tail)) == 0 {
		t.Fatal("checkpoint clear preview did not produce confirmation")
	}
	// These isolated fixtures contain no pre-update Vuja processes. The
	// explicit refusal case below deliberately does not use this helper.
	if err := command.Flags().Set("old-sessions-restarted", "true"); err != nil {
		t.Fatal(err)
	}
	return strings.Fields(tail)[0]
}

func TestOutputCheckpointDoesNotReclaimDeletedPane(t *testing.T) {
	dir := t.TempDir()
	a, _ := recoveryCompositor(t, dir, "pane", "old", nil)
	if err := os.Remove(a.outputRecovery.path); err != nil {
		t.Fatal(err)
	}
	a.WritePTY(terminalMarkerBytes("old", "command-start"))
	a.WritePTY([]byte("output-after-clear\r\n"))
	a.WritePTY(terminalMarkerBytes("old", "command-end:0"))
	a.Close()
	if _, err := os.Stat(a.outputRecovery.path); !os.IsNotExist(err) {
		t.Fatal("an existing writer recreated a cleared or expired pane checkpoint")
	}
}

func TestOutputCheckpointClearAllRemovesInterruptedOutputOnlyAfterConfirmation(t *testing.T) {
	for _, withSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(withSnapshot), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			state, err := config.StatePath()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(filepath.Dir(state), "terminal-output")
			if err := config.EnsurePrivateDir(dir); err != nil {
				t.Fatal(err)
			}
			if withSnapshot {
				writeCheckpointFixture(t, dir, "pane", "private-saved-output", time.Now())
			}
			temporary := filepath.Join(dir, ".checkpoint-123456789")
			unrelated := filepath.Join(dir, ".checkpoint-unrelated")
			export := filepath.Join(filepath.Dir(state), "saved-output.txt")
			history := filepath.Join(filepath.Dir(state), "history.db")
			for _, path := range []string{temporary, unrelated, export, history} {
				if err := os.WriteFile(path, []byte("private-artifact-sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(dir, ".checkpoint-456")
			if err := os.Symlink(export, link); err != nil {
				t.Fatal(err)
			}
			command := newOutputCheckpointClearCommand()
			if err := command.Flags().Set("all", "true"); err != nil {
				t.Fatal(err)
			}
			token := checkpointClearPreview(t, command)
			if _, err := os.Stat(temporary); err != nil {
				t.Fatal("preview removed interrupted output without confirmation")
			}
			if err := command.Flags().Set("confirm", token); err != nil {
				t.Fatal(err)
			}
			if err := command.RunE(command, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(temporary); !os.IsNotExist(err) {
				t.Fatal("confirmed all-pane clear retained interrupted private output")
			}
			for _, path := range []string{unrelated, export, history, link} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("checkpoint clear removed an unrelated file, export, history, or symlink")
				}
			}
			checkpoints, _ := filepath.Glob(filepath.Join(dir, "*.json"))
			if len(checkpoints) != 0 {
				t.Fatal("confirmed all-pane clear retained a selected snapshot")
			}
		})
	}
}

func TestOutputCheckpointClearHandlesInterruptedPruneAbovePaneLimit(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	state, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(state), "terminal-output")
	if err := config.EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	// Rename is durable before prune. A process stopped between those steps
	// can leave one more snapshot than the normal pane-retention limit.
	for i := 0; i <= outputRecoveryPaneLimit; i++ {
		writeCheckpointFixture(t, dir, fmt.Sprint(i), "interrupted-prune-private-output", time.Now())
	}
	output := newOutputCommand()
	list, _, err := output.Find([]string{"checkpoints", "list"})
	if err != nil || list.RunE == nil {
		t.Fatal("checkpoint inventory is unavailable")
	}
	var inventory bytes.Buffer
	list.SetOut(&inventory)
	if err := list.RunE(list, nil); err != nil {
		t.Fatalf("interrupted pruning made checkpoint inventory unavailable: %v", err)
	}
	if !strings.Contains(inventory.String(), "checkpoints 65\n") || strings.Contains(inventory.String(), "interrupted-prune-private-output") {
		t.Fatal("checkpoint inventory miscounted or exposed interrupted-prune output")
	}
	clear := newOutputCheckpointClearCommand()
	if err := clear.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	token := checkpointClearPreview(t, clear)
	if err := clear.Flags().Set("confirm", token); err != nil {
		t.Fatal(err)
	}
	if err := clear.RunE(clear, nil); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatal("confirmed clearing retained interrupted-prune output")
	}
}

func TestOutputCheckpointClearRequiresOldSessionRestartAcknowledgement(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	state, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(state), "terminal-output")
	if err := config.EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	// A pre-update owner permits ENOENT and could republish after deletion.
	// Neither its lock nor its old guard can establish that it has retired.
	writeCheckpointFixture(t, dir, "legacy-pane", "legacy-private-output", time.Now())
	command := newOutputCheckpointClearCommand()
	if err := command.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	var preview bytes.Buffer
	command.SetOut(&preview)
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(preview.String(), "--confirm ")
	if !ok || len(strings.Fields(tail)) == 0 {
		t.Fatal("checkpoint preview omitted confirmation")
	}
	if err := command.Flags().Set("confirm", strings.Fields(tail)[0]); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err == nil {
		t.Fatal("clear removed legacy-owned output without requiring old-session retirement")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("refused legacy-unsafe clear changed captured output")
	}
	data, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(data), "legacy-private-output") {
		t.Fatal("refused legacy-unsafe clear erased existing rows")
	}
	if !strings.Contains(preview.String(), "pre-update Vuja sessions") {
		t.Fatal("clear preview did not explain its rollout prerequisite")
	}
	if err := command.Flags().Set("old-sessions-restarted", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatalf("explicitly acknowledged clearing failed: %v", err)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("acknowledged clearing did not remove selected output")
	}
}

func TestOutputCheckpointRunningRetriesQuietOutputAfterStorageRecovers(t *testing.T) {
	cfg, err := config.Decode([]byte("[ui.chatbox]\noutput-checkpoints = 'running'\noutput-checkpoint-interval = '5s'\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	a, _ := configuredCheckpointCompositor(t, cfg, t.TempDir(), "pane", "retry", now)
	a.now = func() time.Time { return now }
	path := a.outputRecovery.path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	a.WritePTY(terminalMarkerBytes("retry", "command-start"))
	a.WritePTY([]byte("quiet-running-result\r\n"))
	now = now.Add(5 * time.Second)
	a.RefreshRunningStatus()
	awaitCheckpointState(t, a, func(r *outputRecovery) bool { return r.failed })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(outputRecoveryState{Version: 1, Owner: a.outputRecovery.owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// No new bytes or completion: retry eligibility must survive a failed save.
	now = now.Add(5 * time.Second)
	a.RefreshRunningStatus()
	awaitCheckpointRow(t, path, "quiet-running-result")
}

func TestOutputCheckpointDisabledSessionFencesOlderPaneWriter(t *testing.T) {
	for _, viewport := range []string{"pinned", "terminal"} {
		t.Run(viewport, func(t *testing.T) {
			dir := t.TempDir()
			writeCheckpointFixture(t, dir, "pane", "previously-saved-output", time.Now())
			a, _ := recoveryCompositor(t, dir, "pane", "older", nil)
			w := a.outputRecovery.writer
			entered, release := make(chan struct{}), make(chan struct{})
			w.mu.Lock()
			save := w.save
			w.save = func(snapshot outputCheckpointSnapshot) { close(entered); <-release; save(snapshot) }
			w.mu.Unlock()
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			a.WritePTY(terminalMarkerBytes("older", "command-start"))
			a.WritePTY([]byte("queued-before-disable\r\n"))
			a.WritePTY(terminalMarkerBytes("older", "command-end:0"))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("pending output did not reach the storage barrier")
			}
			cfg, err := config.Decode([]byte(fmt.Sprintf("[ui.chatbox]\noutput-checkpoints = 'off'\noutput-viewport = %q\n", viewport)))
			if err != nil {
				t.Fatal(err)
			}
			configuredCheckpointCompositor(t, cfg, dir, "pane", "disabled", time.Now())
			unblock()
			a.Close()
			state, err := a.outputRecovery.read()
			if err != nil || !strings.Contains(strings.Join(state.Rows, "\n"), "previously-saved-output") {
				t.Fatal("disabling checkpoints erased prior saved output instead of retaining it")
			}
			if strings.Contains(strings.Join(state.Rows, "\n"), "queued-before-disable") {
				t.Fatal("an older session published queued output after the new session disabled capture")
			}
		})
	}
}
