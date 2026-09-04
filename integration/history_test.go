package integration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordSessionCommandPreservesEventsAndDeduplicatesInlineCandidates(t *testing.T) {
	original := RichHistorySnapshot()
	PublishCanonicalHistory(nil)
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	RecordSessionCommand("git status")
	RecordSessionCommand("npm run dev")
	RecordSessionCommand("npm run dev")
	RecordSessionCommand("printf first\nprintf second")
	RecordSessionCommand("git push origin fix/scoring")

	results, err := SearchHistory("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 4 {
		t.Fatalf("expected four deduplicated session commands in search results, got %d", len(results))
	}

	// newest session command must be results[0]
	if results[0].Cmd != "git push origin fix/scoring" {
		t.Errorf("expected results[0] to be 'git push origin fix/scoring', got %q", results[0].Cmd)
	}
	if results[1].Cmd != "printf first\nprintf second" {
		t.Errorf("expected results[1] to preserve the multiline command, got %q", results[1].Cmd)
	}
	if results[2].Cmd != "npm run dev" || results[3].Cmd != "git status" {
		t.Errorf("expected recency-ordered deduplicated commands, got %+v", results)
	}
	if entries := RichHistorySnapshot(); len(entries) != 5 {
		t.Fatalf("expected every session execution to remain available to rich history, got %+v", entries)
	}

	RecordSessionCommandAt("go test ./...", "/repo/service")
	scoped, err := SearchHistoryWithOptions("go test", nil, HistorySearchOptions{
		Cwd:   "/repo/service",
		Scope: HistoryScopeDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Cmd != "go test ./..." {
		t.Fatalf("expected directory-scoped session history, got %+v", scoped)
	}
}

func TestRecordSessionCommandRejectsLeadingWhitespaceBeforeNormalization(t *testing.T) {
	original := RichHistorySnapshot()
	PublishCanonicalHistory(nil)
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	RecordSessionCommand(" secret command")
	RecordSessionCommand("\tsecret command")

	if entries := RichHistorySnapshot(); len(entries) != 0 {
		t.Fatalf("expected leading-whitespace commands to remain private, got %+v", entries)
	}
}

func TestRecordSessionCommandRejectsSensitiveCommandsAtTheCanonicalBoundary(t *testing.T) {
	original := RichHistorySnapshot()
	PublishCanonicalHistory(nil)
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	RecordSessionCommand("curl --token private-value https://example.test")

	if entries := RichHistorySnapshot(); len(entries) != 0 {
		t.Fatalf("expected sensitive commands to remain outside canonical history, got %+v", entries)
	}
}

func TestShellHistoryImportRejectsLeadingWhitespaceBeforeNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte(" public command\nvisible command\n\tprivate command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	entries, err := loadShellHistory(file, "bash")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Command != "visible command" {
		t.Fatalf("expected only recordable history, got %+v", entries)
	}
}

func TestSearchCachedHistoryKeepsServingPublishedSnapshotDuringReload(t *testing.T) {
	mu.Lock()
	originalHistory, originalIDs := historyCache, idMapCache
	historyCache = []string{"git status"}
	idMapCache = map[string]int{"git status": 1}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		historyCache, idMapCache = originalHistory, originalIDs
		mu.Unlock()
	})

	results, available := SearchCachedHistory("git", nil)
	if !available || len(results) != 1 || results[0].Cmd != "git status" {
		t.Fatalf("expected published cache during reload, available=%v results=%v", available, results)
	}
}

func TestHistoryAliasesKeyIsStableAcrossMapOrder(t *testing.T) {
	left := historyAliasesKey(map[string]string{"g": "git", "d": "docker"})
	right := historyAliasesKey(map[string]string{"d": "docker", "g": "git"})
	if left != right {
		t.Fatalf("expected alias cache key to be deterministic, left=%q right=%q", left, right)
	}
}

func TestHistorySnapshotPreservesFrequency(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Now()
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "1", Command: "git status", StartedAt: now.Add(-time.Minute), Source: "vuja"},
		{ID: "2", Command: "make test", StartedAt: now, Source: "vuja"},
		{ID: "3", Command: "git status", StartedAt: now.Add(time.Minute), Source: "vuja"},
	})
	stats := HistorySnapshot()
	if len(stats) != 2 {
		t.Fatalf("expected two unique commands, got %v", stats)
	}
	for _, stat := range stats {
		if stat.Command == "git status" && stat.Count == 2 {
			return
		}
	}
	t.Fatalf("expected git status count 2, got %v", stats)
}

func TestHistorySnapshotDoesNotFabricateRecencyForTimestampLessHistory(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	PublishCanonicalHistory([]HistoryEntry{{ID: "zsh:1", Command: "cd old-project", Source: "zsh"}})
	stats := HistorySnapshot()
	if len(stats) != 1 || !stats[0].LastUsed.IsZero() {
		t.Fatalf("expected timestamp-less history to have unknown recency, got %+v", stats)
	}
}

func TestSearchHistoryKeepsMatchTiersAttachedWhileSorting(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "newest-prefix", Command: "ssh forge@newest", StartedAt: now, Source: "vuja"},
		{ID: "older-prefix", Command: "ssh forge@older", StartedAt: now.Add(-time.Minute), Source: "vuja"},
		{ID: "exact", Command: "ssh", StartedAt: now.Add(-2 * time.Minute), Source: "vuja"},
	})

	results, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Cmd != "ssh" || results[1].Cmd != "ssh forge@newest" || results[2].Cmd != "ssh forge@older" {
		t.Fatalf("expected exact then prefix matches in recency order, got %+v", results)
	}
}

func TestCanceledHistorySearchCannotReplaceIncrementalSearchState(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "ssh", Command: "ssh forge@api", StartedAt: time.Now(), Source: "vuja"},
		{ID: "git", Command: "git status", StartedAt: time.Now(), Source: "vuja"},
	})
	if _, err := SearchHistory("ssh", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SearchHistoryContext(ctx, "git", nil); err == nil {
		t.Fatal("expected canceled search to stop")
	}
	if status := CurrentHistorySearchStatus(); status.Query != "ssh" {
		t.Fatalf("expected canceled work not to replace incremental state, got %+v", status)
	}
}

func TestParseZshHistoryLinePreservesTimestampAndDuration(t *testing.T) {
	occurrence := parseZshHistoryLine(": 1720000000:3.25;go test ./...")

	if occurrence.Command != "go test ./..." {
		t.Fatalf("unexpected command %q", occurrence.Command)
	}
	if occurrence.Timestamp.Unix() != 1720000000 {
		t.Fatalf("unexpected timestamp %s", occurrence.Timestamp)
	}
	if occurrence.Duration != 3250*time.Millisecond {
		t.Fatalf("unexpected duration %s", occurrence.Duration)
	}
}

func TestLoadAtuinHistoryPreservesDirectoryAndOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `
CREATE TABLE history (
	id TEXT NOT NULL,
	command TEXT NOT NULL,
	timestamp INTEGER NOT NULL,
	duration INTEGER NOT NULL,
	exit INTEGER NOT NULL,
	cwd TEXT NOT NULL,
	hostname TEXT NOT NULL,
	session TEXT NOT NULL,
	deleted_at INTEGER
);
INSERT INTO history (id, command, timestamp, duration, exit, cwd, hostname, session)
VALUES ('event-1', 'go test ./...', 1720000000000000000, 250000000, 0, '/repo/service', 'devbox', 'shell-1');
INSERT INTO history (id, command, timestamp, duration, exit, cwd, hostname, session)
VALUES ('event-2', 'printf first' || char(10) || 'printf second', 1720000001000000000, 1000000, 0, '/repo/service', 'devbox', 'shell-1');
`)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	occurrences, err := loadAtuinHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 2 {
		t.Fatalf("expected both single-line and multiline Atuin entries, got %+v", occurrences)
	}
	entry := occurrences[0]
	if entry.Command != "go test ./..." || entry.Cwd != "/repo/service" {
		t.Fatalf("unexpected Atuin entry %+v", entry)
	}
	if !entry.HasExitCode || entry.ExitCode != 0 || entry.Duration != 250*time.Millisecond {
		t.Fatalf("expected outcome metadata, got %+v", entry)
	}
	if entry.Timestamp.Unix() != 1720000000 {
		t.Fatalf("unexpected timestamp %s", entry.Timestamp)
	}
	if entry.ID != "event-1" || entry.Host != "devbox" || entry.SessionID != "shell-1" {
		t.Fatalf("expected Atuin scope metadata, got %+v", entry)
	}
	if occurrences[1].Command != "printf first\nprintf second" {
		t.Fatalf("expected multiline Atuin command to remain one event, got %+v", occurrences[1])
	}
}

func TestLoadAtuinHistoryOrdersEqualTimestampsDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE history (
    id TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    timestamp INTEGER NOT NULL
);
INSERT INTO history (id, command, timestamp) VALUES
    ('event-b', 'ssh forge@b', 1725000000000000000),
    ('event-a', 'ssh forge@a', 1725000000000000000);
`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := loadAtuinHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadAtuinHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 || first[0].ID != "event-a" || first[1].ID != "event-b" ||
		first[0].ID != second[0].ID || first[1].ID != second[1].ID {
		t.Fatalf("expected stable timestamp/id ordering, first=%+v second=%+v", first, second)
	}
}

func TestLoadAtuinHistoryBoundsTheAdapterToTheNewestExecutions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE history (
    id TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    timestamp INTEGER NOT NULL
);
INSERT INTO history (id, command, timestamp) VALUES
    ('event-1', 'ssh forge@old', 1),
    ('event-2', 'ssh forge@middle', 2),
    ('event-3', 'ssh forge@new', 3);
`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := loadAtuinHistoryContextLimit(t.Context(), path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "event-2" || entries[1].ID != "event-3" {
		t.Fatalf("expected the newest bounded Atuin window in chronological order, got %+v", entries)
	}
}

func TestLoadShellHistoryBoundsMemoryToTheNewestExecutions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte("ssh forge@old\nssh forge@middle\nssh forge@new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	entries, err := loadShellHistoryContextLimit(t.Context(), file, "bash", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Command != "ssh forge@middle" || entries[1].Command != "ssh forge@new" {
		t.Fatalf("expected the newest bounded shell-history window, got %+v", entries)
	}
}

func TestBoundedHistoryBufferEvictsOldestExecutionsByMemory(t *testing.T) {
	example := historyOccurrence{Command: "x"}
	buffer := newBoundedHistoryBuffer(10, historyOccurrenceBytes(example)*2)
	for _, command := range []string{"a", "b", "c"} {
		buffer.Add(historyOccurrence{Command: command})
	}

	entries := buffer.Entries()
	if len(entries) != 2 || entries[0].Command != "b" || entries[1].Command != "c" {
		t.Fatalf("expected the memory boundary to retain the newest executions, got %+v", entries)
	}
}

func TestHistoryOccurrencesToEntriesPreservesEveryExecution(t *testing.T) {
	startedAt := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	occurrences := []historyOccurrence{
		{ID: "atuin-1", Command: "just deploy staging", Timestamp: startedAt, Source: "atuin"},
		{ID: "atuin-2", Command: "just deploy staging", Timestamp: startedAt.Add(-time.Hour), Source: "atuin"},
	}

	entries := historyOccurrencesToEntries(occurrences)

	if len(entries) != 2 || entries[0].ID == entries[1].ID {
		t.Fatalf("expected distinct per-execution entries, got %+v", entries)
	}
	if entries[0].ID != "atuin:atuin-1" || entries[1].ID != "atuin:atuin-2" {
		t.Fatalf("expected stable source-qualified IDs, got %+v", entries)
	}
}

func TestHistoryOccurrencesToEntriesKeepsUnknownRecencyUnknown(t *testing.T) {
	entries := historyOccurrencesToEntries([]historyOccurrence{{Command: "git status", Source: "zsh"}})

	if len(entries) != 1 || !entries[0].StartedAt.IsZero() {
		t.Fatalf("expected timestamp-less rich history to retain unknown recency, got %+v", entries)
	}
	if got := formatRelativeTime(entries[0].StartedAt, time.Now()); got != "" {
		t.Fatalf("expected no relative-time claim for unknown recency, got %q", got)
	}
}
