package root

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

// QA 1/3/8: a committed exclusion remains readable even when canonical writes
// are unavailable. Deliberate privacy rejection has no failure/file side effects.
func TestForgottenHistoryDoesNotBecomeFailureWhenWriterUnavailable(t *testing.T) {
	for _, mode := range []string{"default-busy", "default-closed", "default-nil", "custom-busy", "custom-closed"} {
		for _, existingJournal := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/absent-journal", true: "/existing-journal"}[existingJournal], func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				path, err := scoring.DefaultHistoryPath()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "custom-busy" || mode == "custom-closed" {
					path = filepath.Join(t.TempDir(), "custom.db")
				}
				store, err := scoring.NewFrecencyStore(path)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
				entry := integration.HistoryEntry{ID: "forgotten:unavailable-writer", Command: "ssh forge@forgotten-fixture", Source: "vuja", Cwd: "/fixture", SubmittedAt: at, StartedAt: at}
				if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
					t.Fatal(err)
				}
				if removed, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil || removed != 1 {
					t.Fatalf("seed exclusion: removed=%d err=%v", removed, err)
				}
				journal, err := historyRecoveryJournalPath()
				if err != nil {
					t.Fatal(err)
				}
				if existingJournal {
					keeper := entry
					keeper.ID, keeper.Command = "unrelated:pending", "git status"
					if err := appendHistoryRecoveryRecord(historyRecoverySubmission, keeper); err != nil {
						t.Fatal(err)
					}
				}
				beforeJournal, beforeJournalErr := os.ReadFile(journal)
				beforeOffset, beforeOffsetErr := os.ReadFile(journal + ".offset")
				failurePath, err := historyPersistenceFailurePath()
				if err != nil {
					t.Fatal(err)
				}
				beforeFailure, beforeFailureErr := os.ReadFile(failurePath)
				if mode == "default-busy" || mode == "custom-busy" {
					holdHistoryWriterForPrivacyTest(t, path)
				} else if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				selected := store
				if mode == "default-nil" {
					selected = nil
				}
				beforeCount := historyPersistenceFailures.Load()
				observed := false
				persisted, journaled := persistHistorySubmissionDurablyObserved(selected, entry, func(bool, bool) { observed = true })
				if persisted || journaled || observed {
					t.Errorf("forgotten submission reached recovery/health: persisted=%v journaled=%v observed=%v", persisted, journaled, observed)
				}
				if after := historyPersistenceFailures.Load(); after != beforeCount {
					t.Errorf("privacy rejection counted as persistence failure: before=%d after=%d", beforeCount, after)
				}
				beforeCompletion := historyPersistenceFailures.Load()
				observed = false
				entry.CompletedAt, entry.HasExitCode = at.Add(time.Second), true
				completed, journaled := completeHistoryEventDurablyObserved(selected, entry, func(bool, bool) { observed = true })
				if completed || journaled || observed {
					t.Errorf("forgotten completion reached recovery/health: completed=%v journaled=%v observed=%v", completed, journaled, observed)
				}
				if after := historyPersistenceFailures.Load(); after != beforeCompletion {
					t.Errorf("forgotten completion counted as persistence failure: before=%d after=%d", beforeCompletion, after)
				}
				for _, artifact := range []struct {
					path string
					data []byte
					err  error
				}{{journal, beforeJournal, beforeJournalErr}, {journal + ".offset", beforeOffset, beforeOffsetErr}, {failurePath, beforeFailure, beforeFailureErr}} {
					after, err := os.ReadFile(artifact.path)
					if !bytes.Equal(after, artifact.data) || os.IsNotExist(err) != os.IsNotExist(artifact.err) || err != nil && !os.IsNotExist(err) {
						t.Errorf("privacy rejection changed %s: before-error=%v after-error=%v before-bytes=%d after-bytes=%d", filepath.Base(artifact.path), artifact.err, err, len(artifact.data), len(after))
					}
				}
				_, stats, err := scoring.InspectHistory(t.Context(), path)
				if err != nil || stats.Events != 0 {
					t.Errorf("delayed forgotten history changed canonical state: events=%d err=%v", stats.Events, err)
				}
			})
		}
	}
}

func holdHistoryWriterForPrivacyTest(t *testing.T, path string) {
	t.Helper()
	databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK") })
}

// A tombstone in a different database is not authority to discard an ordinary
// submission. Unknown authority still requires a complete durable fallback.
func TestSubmissionFallbackDoesNotUseUnrelatedDefaultExclusion(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "readable-authority", true: "missing-authority"}[missing], func(t *testing.T) {
			testSubmissionFallbackWithUnrelatedDefaultExclusion(t, missing)
		})
	}
}

func testSubmissionFallbackWithUnrelatedDefaultExclusion(t *testing.T, missing bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	defaultStore, err := scoring.NewFrecencyStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer defaultStore.Close()
	entry := integration.HistoryEntry{ID: "same-id:different-database", Command: "ssh forge@ordinary-fixture", Source: "vuja", StartedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	if err := defaultStore.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultStore.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	selectedPath := filepath.Join(t.TempDir(), "selected.db")
	selectedStore, err := scoring.NewFrecencyStore(selectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := selectedStore.Close(); err != nil {
		t.Fatal(err)
	}
	if missing {
		if err := os.Rename(selectedPath, selectedPath+".unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	observed := false
	persisted, journaled := persistHistorySubmissionDurablyObserved(selectedStore, entry, func(p, j bool) { observed = p && j })
	if !persisted || !journaled || !observed {
		t.Fatalf("unrelated exclusion discarded ordinary submission: persisted=%v journaled=%v observed=%v", persisted, journaled, observed)
	}
	path, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record historyRecoveryRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Entry.ID != entry.ID || record.Entry.Command != entry.Command {
		t.Fatalf("fallback lacks the exact ordinary submission: record=%+v err=%v", record, err)
	}
	if missing {
		if _, err := os.Stat(selectedPath); !os.IsNotExist(err) {
			t.Fatalf("failed inspection recreated the unavailable database: %v", err)
		}
		if err := os.Rename(selectedPath+".unavailable", selectedPath); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := scoring.NewFrecencyStore(selectedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for range 2 {
		if err := replayHistoryRecoveryJournal(reopened); err != nil {
			t.Fatal(err)
		}
	}
	events, err := reopened.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 1 || events[0].EventKey != entry.ID || events[0].Command != entry.Command || events[0].HasExitCode {
		t.Fatalf("ordinary submission did not recover exactly once: events=%+v err=%v", events, err)
	}
	if forgotten, err := defaultStore.HistoryEventForgotten(t.Context(), entry.ID); err != nil || !forgotten {
		t.Fatalf("recovery changed unrelated default exclusion: forgotten=%v err=%v", forgotten, err)
	}
}

func TestForgottenDirectJournalAppendDoesNotCreateArtifacts(t *testing.T) {
	for _, kind := range []string{historyRecoverySubmission, historyRecoveryCompletion} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			store, err := scoring.NewFrecencyStore("")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			entry := integration.HistoryEntry{ID: "forgotten:direct-append", Command: "git status", Source: "vuja", StartedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
			if err := store.RecordHistorySubmission(t.Context(), scoringHistoryEvent(entry)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ForgetHistoryEvents(t.Context(), []string{entry.ID}); err != nil {
				t.Fatal(err)
			}
			if err := appendHistoryRecoveryRecord(kind, entry); !errors.Is(err, scoring.ErrHistoryForgotten) {
				t.Fatalf("known-forgotten append was not a privacy rejection: %v", err)
			}
			journal, err := historyRecoveryJournalPath()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{journal, journal + ".offset"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("known-forgotten append created %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}
