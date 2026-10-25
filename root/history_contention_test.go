package root

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

// QA scenario 8: a different connection owns SQLite's writer lock, not just
// the same-process publication mutex. The submission must remain durable in
// the real recovery journal until the writer is released, then replay once.
func TestHistorySubmissionSurvivesIndependentSQLiteWriter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := scoring.NewFrecencyStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path, err := scoring.DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	blocker, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	connection, err := blocker.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK") }()

	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{
		ID: "vuja:writer-contention:submission", Command: "ssh forge@fixture",
		NormalizedCommand: "ssh forge@fixture", Cwd: "/fixture",
		SubmittedAt: now, StartedAt: now, Source: "vuja",
		SessionID: "fixture-contention", Shell: "zsh", State: integration.HistoryStateRunning,
	}
	acknowledgementStarted := time.Now()
	persisted, journaled := persistHistorySubmissionDurably(store, entry)
	t.Logf("durable acknowledgement while the writer stayed locked: %s", time.Since(acknowledgementStarted))
	if !persisted || !journaled {
		t.Fatalf("a held independent writer must use durable journal fallback: persisted=%v journaled=%v", persisted, journaled)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 0 {
		t.Fatalf("blocked canonical write must not leave a partial submission: events=%+v err=%v", events, err)
	}
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var record historyRecoveryRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Kind != historyRecoverySubmission || record.Entry.ID != entry.ID || record.Entry.Command != entry.Command {
		t.Fatalf("acknowledgement lacks the actual submission record: record=%+v err=%v", record, err)
	}
	if info, err := os.Stat(journalPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fallback journal must remain owner-only: info=%v err=%v", info, err)
	}
	if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := replayHistoryRecoveryJournal(store); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err = reopened.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 {
		t.Fatalf("replay/reopen must retain exactly one execution: events=%+v err=%v", events, err)
	}
	if got := events[0]; got.EventKey != entry.ID || got.Command != entry.Command || got.NormalizedCommand != entry.NormalizedCommand || got.Cwd != entry.Cwd || got.Source != entry.Source || got.SessionID != entry.SessionID || got.Shell != entry.Shell || !got.StartedAt.Equal(now) || !got.SubmittedAt.Equal(now) || got.HasExitCode {
		t.Fatalf("replay/reopen changed the submitted execution or invented an outcome: %+v", got)
	}
	if info, err := os.Stat(journalPath); err != nil || info.Size() != 0 {
		t.Fatalf("replayed journal was not drained: info=%v err=%v", info, err)
	}
}

// A completion already resolved by a tombstone or committed outcome is a
// read-only no-op. An unrelated writer must not turn it into unhealthy recovery.
func TestResolvedHistoryCompletionDoesNotNeedIndependentWriter(t *testing.T) {
	for _, name := range []string{"forgotten", "completed"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			path, err := scoring.DefaultHistoryPath()
			if err != nil {
				t.Fatal(err)
			}
			store, err := scoring.NewFrecencyStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			entry := integration.HistoryEntry{ID: "resolved:" + name, Command: "ssh forge@fixture", NormalizedCommand: "ssh forge@fixture",
				Cwd: "/fixture", Source: "vuja", SubmittedAt: at, StartedAt: at}
			if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
				t.Fatal(err)
			}
			entry.CompletedAt, entry.HasExitCode, entry.State = at.Add(time.Second), true, integration.HistoryStateCompleted
			if name == "forgotten" {
				if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
					t.Fatal(err)
				}
			} else if changed, err := store.CompleteHistoryEvent(t.Context(), scoringHistoryEvent(entry)); err != nil || !changed {
				t.Fatalf("seed completion: changed=%v err=%v", changed, err)
			}
			databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
			blocker, err := sql.Open("sqlite", databaseURL.String())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			connection, err := blocker.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK") }()
			before := historyPersistenceFailures.Load()
			observed := false
			completed, journaled := completeHistoryEventDurablyObserved(store, entry, func(bool, bool) { observed = true })
			if completed || journaled || observed {
				t.Errorf("resolved completion entered recovery/health: completed=%v journaled=%v observed=%v", completed, journaled, observed)
			}
			if after := historyPersistenceFailures.Load(); after != before {
				t.Errorf("resolved completion counted a persistence failure: before=%d after=%d", before, after)
			}
			journal, err := historyRecoveryJournalPath()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Errorf("resolved completion created recovery output: %v", err)
			}
		})
	}
}
