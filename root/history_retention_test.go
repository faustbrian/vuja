package root

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
	"github.com/spf13/cobra"
)

// QA 1/3: refusing an unsafe legacy-learning reset is a maintenance outcome,
// not evidence that canonical recording or SQLite recovery is unavailable.
func TestHistoryRetentionRefusalKeepsCanonicalRecordingAvailable(t *testing.T) {
	for _, learning := range []string{"feedback", "navigation"} {
		t.Run(learning, func(t *testing.T) {
			store := historyAgeRetentionFixture(t)
			oldImport := config.Get().Suggestions.ImportZoxide
			config.Get().Suggestions.ImportZoxide = false
			t.Cleanup(func() { config.Get().Suggestions.ImportZoxide = oldImport })
			oldCwd := spec.GetCWD()
			spec.SetShellCWD(t.TempDir())
			t.Cleanup(func() { spec.SetShellCWD(oldCwd) })
			seedHistoryRetentionRefusal(t, store, learning)
			failures := historyPersistenceFailureCount()
			ready, err := prepareCanonicalHistory(store, "retention-session")
			if err != nil || ready != store {
				t.Fatalf("retention refusal disabled healthy canonical recording: ready=%t err=%v", ready != nil, err)
			}
			entry := integration.HistoryEntry{ID: "new-durable-command", Command: "git diff", Cwd: "/repo", Source: "vuja", StartedAt: time.Now(), State: integration.HistoryStateRunning}
			if persisted, journaled := persistHistorySubmissionDurably(ready, entry); !persisted || journaled {
				t.Fatal("retention refusal forced an ordinary submission into the journal")
			}
			if events, err := ready.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 2 {
				t.Fatal("maintenance refusal lost an old or newly secured execution")
			}
			if got := historyPersistenceFailureCount(); got != failures {
				t.Fatalf("maintenance refusal was reported as failed persistence: before=%d after=%d", failures, got)
			}
		})
	}
}

func TestHistoryRetentionRefusalHasDistinctHealthWithoutFalseRecoveryFailure(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	seedHistoryRetentionRefusal(t, store, "feedback")
	_, err := applyConfiguredHistoryRetention(t.Context(), store, config.Get().History, time.Now())
	if err == nil {
		t.Fatal("fixture did not exercise the legacy-learning refusal")
	}
	h := newSessionHealthProjection(time.Now)
	h.observeHistory(true, false)
	observe := h.beginRecoveryObservationWithStore(store)
	observe(true, false, err)
	if badge := h.badge(); badge.Label != "History retention not applied" || badge.Critical {
		t.Fatalf("maintenance refusal masqueraded as persistence/recovery failure: %+v", badge)
	}
	if condition := h.snapshot().History; condition == "unavailable" || condition == "recovery-error" {
		t.Fatalf("maintenance refusal changed database health to %q", condition)
	}
	if warning := h.warning(); !strings.Contains(warning, "retention") || strings.Contains(warning, "unavailable") {
		t.Fatal("maintenance warning does not explain the actual problem")
	}
	if h.warning() != "" {
		t.Fatal("maintenance warning bypassed the normal rate limit")
	}
	data, err := json.Marshal(h.snapshot())
	if err != nil || !bytes.Contains(data, []byte(`"retention_deferred":true`)) {
		t.Fatal("sanitized health snapshot omitted the observed maintenance state")
	}
	if err := store.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyConfiguredHistoryRetention(t.Context(), store, config.Get().History, time.Now()); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().RetentionDeferred || h.badge().Label == "History retention not applied" {
		t.Fatal("successful retention retry left a stale maintenance warning")
	}
}

func TestHistoryRetentionRefusalDoesNotHideDoctorOrInventAnImportFailure(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	seedHistoryRetentionRefusal(t, store, "feedback")
	path := filepath.Join(t.TempDir(), "shell-history")
	text := fmt.Sprintf(": %d:0;git diff\n", time.Now().Unix())
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Get().History.Integrations.Shell.Path = path
	if err := importSelectedHistory(store, "zsh", false, true); !errors.Is(err, scoring.ErrHistoryUnattributedLearning) {
		t.Fatalf("import did not distinguish persisted source data from deferred maintenance: %v", err)
	}
	stats, err := store.HistoryStats(t.Context())
	if err != nil || stats.Events != 2 || stats.ImportFailures["zsh"].Count != 0 {
		t.Fatal("maintenance refusal lost imported data or falsely counted source failure")
	}

	// Use the real finite doctor handler and its private canonical store.
	// This does not dispatch the Vuja executable or a managed shell.
	scoring.CloseGlobalFrecencyStore()
	t.Cleanup(scoring.CloseGlobalFrecencyStore)
	global, err := scoring.GetFrecencyStore()
	if err != nil {
		t.Fatal(err)
	}
	seedHistoryRetentionRefusal(t, global, "feedback")
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	if err := historyDoctorCmd.RunE(command, nil); err != nil {
		t.Fatalf("maintenance refusal made diagnostics unavailable: %v", err)
	}
	if !strings.Contains(out.String(), "database ok") || !strings.Contains(out.String(), "retention not-applied") {
		t.Fatal("doctor did not disclose healthy storage and deferred retention separately")
	}
}

func seedHistoryRetentionRefusal(t *testing.T, store *scoring.FrecencyStore, learning string) {
	t.Helper()
	command := "git status"
	if learning == "navigation" {
		command = "z project"
	}
	at := time.Now().UTC().Add(-96 * time.Hour)
	if err := store.RecordHistoryEvent(t.Context(), scoring.HistoryEvent{EventKey: "expired-unattributed", Command: command, Cwd: "/repo", Source: "vuja", StartedAt: at, CompletedAt: at.Add(time.Second), HasExitCode: true, State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if learning == "navigation" {
		if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
			t.Fatal(err)
		}
	} else if err := store.RecordFeedback(t.Context(), command, "/repo", "typed"); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryAgeRetentionForgetReconcilesPendingRecoveryBeforeRecall(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	now := time.Now().UTC()
	target := scoring.HistoryEvent{EventKey: "forget-recent", Command: "ssh forge@target", Cwd: "/repo", Source: "vuja", StartedAt: now.Add(-time.Hour), CompletedAt: now, HasExitCode: true, State: "completed"}
	if err := store.RecordHistoryEvent(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	for index, age := range []time.Duration{96 * time.Hour, 24 * time.Hour} {
		id := []string{"expired-plain-recovery", "retained-plain-recovery"}[index]
		at := now.Add(-age)
		entry := integration.HistoryEntry{ID: id, Command: "ssh forge@" + id, Source: "vuja", Cwd: "/repo", StartedAt: at, CompletedAt: at.Add(time.Second), HasExitCode: true, State: integration.HistoryStateCompleted}
		if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, entry); err != nil {
			t.Fatal(err)
		}
	}
	selected, pending, err := queryHistoryForgetSelection(t.Context(), store, target.EventKey, "", "")
	if err != nil || len(selected) != 1 {
		t.Fatalf("preview: %+v %v", selected, err)
	}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := forgetSelectedHistory(t.Context(), store, selected, pending, cmd); err != nil {
		t.Fatal(err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != "retained-plain-recovery" {
		t.Fatalf("forgetting replay acknowledged expired recovery: %+v %v", events, err)
	}
	if recall := integration.RichHistorySnapshot(); len(recall) != 1 || recall[0].ID != "retained-plain-recovery" {
		t.Fatalf("forgetting replay published expired recovery: %+v", recall)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("reconciled recovery was not acknowledged: %v %v", info, err)
	}
}

func TestHistoryAgeRetentionPlainRecoveryDoesNotAcknowledgePolicyFailure(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	at := time.Now().UTC().Add(-96 * time.Hour)
	entry := integration.HistoryEntry{ID: "expired-unowned-recovery", Command: "z project", Source: "vuja", Cwd: "/repo", StartedAt: at, CompletedAt: at.Add(time.Second), HasExitCode: true, State: integration.HistoryStateCompleted}
	if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
		t.Fatal(err)
	}
	if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, entry); err != nil {
		t.Fatal(err)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayHistoryRecoveryJournal(store); err == nil {
		t.Fatal("plain replay acknowledged unreconciled legacy navigation")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed retention discarded recovery bytes: %v", err)
	}
	if pending, err := historyRecoveryJournalPending(); err != nil || !pending {
		t.Fatalf("failed retention acknowledged recovery: pending=%t error=%v", pending, err)
	}
}

func TestHistoryAgeRetentionRecoveryDoesNotPublishExpiredExecutions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	cfg, err := config.Decode([]byte("[history]\nmax-age = '72h'\n"))
	if err != nil {
		t.Fatalf("age retention policy is unavailable: %v", err)
	}
	previous := config.Get().History
	config.Get().History = cfg.History
	t.Cleanup(func() { config.Get().History = previous })
	original := integration.RichHistorySnapshot()
	integration.PublishCanonicalHistory(nil)
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index, age := range []time.Duration{96 * time.Hour, 24 * time.Hour} {
		id := []string{"expired-recovery", "retained-recovery"}[index]
		at := time.Now().UTC().Add(-age)
		entry := integration.HistoryEntry{ID: id, Command: "ssh forge@" + id, Source: "vuja", Cwd: "/repo", StartedAt: at, CompletedAt: at.Add(time.Second), HasExitCode: true, State: integration.HistoryStateCompleted}
		if err := appendHistoryRecoveryRecord(historyRecoveryCompletion, entry); err != nil {
			t.Fatal(err)
		}
	}
	if recovered, err := retryHistoryRecoveryOnce(t.Context(), store); err != nil || !recovered {
		t.Fatalf("bounded recovery: recovered=%v error=%v", recovered, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != "retained-recovery" {
		t.Fatalf("recovery reinserted age-expired history: %+v %v", events, err)
	}
	if entries := integration.RichHistorySnapshot(); len(entries) != 1 || entries[0].ID != "retained-recovery" {
		t.Fatalf("recovery published expired recall: %+v", entries)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("retention prevented durable recovery acknowledgement: %v %v", info, err)
	}
}

func historyAgeRetentionFixture(t *testing.T) *scoring.FrecencyStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	cfg, err := config.Decode([]byte("[history]\nmax-age = '72h'\n"))
	if err != nil {
		t.Fatal(err)
	}
	previous := config.Get().History
	config.Get().History = cfg.History
	t.Cleanup(func() { config.Get().History = previous })
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestHistoryAgeRetentionCompletionRefreshesRetainedRecall(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	now := time.Now().UTC()
	old := scoring.HistoryEvent{EventKey: "expired-at-completion", Command: "ssh forge@old", Cwd: "/repo", Source: "vuja", StartedAt: now.Add(-96 * time.Hour), CompletedAt: now.Add(-95 * time.Hour), HasExitCode: true, State: "completed"}
	if err := store.RecordHistoryEvent(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	entry := integration.HistoryEntry{ID: "new-completion", Command: "git status", Source: "vuja", Cwd: "/repo", StartedAt: now}
	if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	entry.CompletedAt, entry.HasExitCode, entry.State = now.Add(time.Second), true, integration.HistoryStateCompleted
	if completed, err := completeHistoryEvent(store, entry); err != nil || !completed {
		t.Fatalf("actual completion adapter: completed=%v error=%v", completed, err)
	}
	if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != entry.ID {
		t.Fatalf("completion did not apply age retention: %+v %v", events, err)
	}
	if entries := integration.RichHistorySnapshot(); len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("completion retained expired published recall: %+v", entries)
	}
}

func TestHistoryAgeRetentionExternalRefreshCannotRestoreExpiredRecall(t *testing.T) {
	store := historyAgeRetentionFixture(t)
	path := filepath.Join(t.TempDir(), "zsh-history")
	now := time.Now().UTC()
	contents := fmt.Sprintf(": %d:0;ssh forge@expired-import\n: %d:0;ssh forge@recent-import\n", now.Add(-96*time.Hour).Unix(), now.Add(-24*time.Hour).Unix())
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Get().History.Integrations.Shell.Path = path
	for range 2 {
		if err := importSelectedHistory(store, "zsh", false, true); err != nil {
			t.Fatal(err)
		}
		if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].Command != "ssh forge@recent-import" {
			t.Fatalf("source refresh restored expired history: %+v %v", events, err)
		}
		if entries := integration.RichHistorySnapshot(); len(entries) != 1 || entries[0].Command != "ssh forge@recent-import" {
			t.Fatalf("source refresh published expired recall: %+v", entries)
		}
	}
}
