package root

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/scoring"
)

func TestCanonicalHistorySynchronizesExecutionsFromAnotherManagedSession(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	path := filepath.Join(t.TempDir(), "history.db")
	first, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.SetHistoryChangeOrigin("session-a")
	second.SetHistoryChangeOrigin("session-b")

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	firstEvent := scoring.HistoryEvent{
		EventKey: "vuja:a", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		StartedAt: now, SubmittedAt: now, Source: "vuja", SessionID: "session-a", State: "running",
	}
	if err := first.RecordHistorySubmission(t.Context(), firstEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := publishCanonicalStoreHistory(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	integration.PublishCanonicalHistoryEntry(integration.HistoryEntry{
		ID: "session:pending", Command: "ssh forge@pending", NormalizedCommand: "ssh forge@pending",
		StartedAt: now.Add(30 * time.Second), SubmittedAt: now.Add(30 * time.Second),
		Source: "session", SessionID: "session-a", State: integration.HistoryStateRunning,
	})
	baseline, err := first.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}

	secondEvent := scoring.HistoryEvent{
		EventKey: "vuja:b", Command: "ssh forge@web", NormalizedCommand: "ssh forge@web",
		StartedAt: now.Add(time.Minute), SubmittedAt: now.Add(time.Minute), Source: "vuja", SessionID: "session-b", State: "running",
	}
	if err := second.RecordHistorySubmission(t.Context(), secondEvent); err != nil {
		t.Fatal(err)
	}
	next, refreshed, err := syncCanonicalHistoryOnce(t.Context(), first, "session-a", baseline.Latest)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed || next <= baseline.Latest {
		t.Fatalf("expected the foreign execution to refresh the canonical generation, refreshed=%v cursor=%d", refreshed, next)
	}
	results, err := integration.SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected both managed sessions and current-session state in recall, got %+v", results)
	}
}

func TestCanonicalHistorySyncSkipsFullReloadForLocallyPublishedChanges(t *testing.T) {
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetHistoryChangeOrigin("session-a")
	now := time.Now()
	if err := store.RecordHistorySubmission(t.Context(), scoring.HistoryEvent{
		EventKey: "vuja:local", Command: "git status", NormalizedCommand: "git status",
		StartedAt: now, SubmittedAt: now, Source: "vuja", SessionID: "session-a",
	}); err != nil {
		t.Fatal(err)
	}
	cursor, refreshed, err := syncCanonicalHistoryOnce(t.Context(), store, "session-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed || cursor == 0 {
		t.Fatalf("expected local change acknowledgement without a full reload, refreshed=%v cursor=%d", refreshed, cursor)
	}
}

func TestCanonicalHistorySyncStartsAfterThePublishedSnapshotInsteadOfHistoricalBacklog(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	path := filepath.Join(t.TempDir(), "history.db")
	first, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.SetHistoryChangeOrigin("session-a")
	second.SetHistoryChangeOrigin("session-b")

	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if _, err := seed.ExecContext(t.Context(), `
WITH RECURSIVE changes(value) AS (
    VALUES(1)
    UNION ALL
    SELECT value + 1 FROM changes WHERE value < 300
)
INSERT INTO history_changes (origin, kind, event_key)
SELECT 'session-a', 'upsert', printf('historical-%03d', value) FROM changes
`); err != nil {
		t.Fatal(err)
	}
	if _, err := publishCanonicalStoreHistory(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	cursor := historySyncStartCursor(first)
	if cursor < 300 {
		t.Fatalf("expected the published generation to capture the historical change cursor, got %d", cursor)
	}

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	fresh := scoring.HistoryEvent{
		EventKey: "vuja:fresh", Command: "ssh forge@fresh", NormalizedCommand: "ssh forge@fresh",
		StartedAt: now.Add(time.Hour), SubmittedAt: now.Add(time.Hour),
		Source: "vuja", SessionID: "session-b", State: "running",
	}
	if err := second.RecordHistorySubmission(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	next, refreshed, err := syncCanonicalHistoryOnce(t.Context(), first, "session-a", cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed || next <= cursor {
		t.Fatalf("expected the first poll to publish the fresh foreign event, refreshed=%v cursor=%d next=%d", refreshed, cursor, next)
	}
	results, err := integration.SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Cmd != fresh.Command {
		t.Fatalf("expected the fresh command without replaying historical changes, got %+v", results)
	}
}
