package integration

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration/shell"
)

func TestRecordSessionCommand_MergeAndDeduplicate(t *testing.T) {
	sessionHistoryMu.Lock()
	origSessionHistory := sessionHistory
	sessionHistory = nil
	sessionHistoryMu.Unlock()

	mu.Lock()
	origHistoryCache := historyCache
	origHistoryStatsCache := historyStatsCache
	historyCache = nil
	historyStatsCache = nil
	mu.Unlock()

	t.Cleanup(func() {
		sessionHistoryMu.Lock()
		sessionHistory = origSessionHistory
		sessionHistoryMu.Unlock()

		mu.Lock()
		historyCache = origHistoryCache
		historyStatsCache = origHistoryStatsCache
		mu.Unlock()
	})

	RecordSessionCommand("git status")
	RecordSessionCommand("npm run dev")
	RecordSessionCommand("npm run dev") // duplicate subsequent command should be ignored
	RecordSessionCommand("printf first\nprintf second")
	RecordSessionCommand("git push origin fix/scoring")

	results, err := SearchHistory("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) < 3 {
		t.Fatalf("expected at least 3 session commands in search results, got %d", len(results))
	}

	// newest session command must be results[0]
	if results[0].Cmd != "git push origin fix/scoring" {
		t.Errorf("expected results[0] to be 'git push origin fix/scoring', got %q", results[0].Cmd)
	}
	if results[1].Cmd != "npm run dev" {
		t.Errorf("expected results[1] to be 'npm run dev', got %q", results[1].Cmd)
	}
	if results[2].Cmd != "git status" {
		t.Errorf("expected results[2] to be 'git status', got %q", results[2].Cmd)
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
	sessionHistoryMu.Lock()
	originalSessionHistory := sessionHistory
	sessionHistory = nil
	sessionHistoryMu.Unlock()
	mu.Lock()
	originalHistoryCache := historyCache
	originalHistoryStatsCache := historyStatsCache
	historyCache = nil
	historyStatsCache = nil
	mu.Unlock()
	t.Cleanup(func() {
		sessionHistoryMu.Lock()
		sessionHistory = originalSessionHistory
		sessionHistoryMu.Unlock()
		mu.Lock()
		historyCache = originalHistoryCache
		historyStatsCache = originalHistoryStatsCache
		mu.Unlock()
	})

	RecordSessionCommand(" secret command")
	RecordSessionCommand("\tsecret command")

	sessionHistoryMu.Lock()
	defer sessionHistoryMu.Unlock()
	if len(sessionHistory) != 0 {
		t.Fatalf("expected leading-whitespace commands to remain private, got %+v", sessionHistory)
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
	historyLoadGate <- struct{}{}
	defer func() { <-historyLoadGate }()
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, ".bash_history"),
		[]byte("git status\nmake test\ngit status\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	originalCache := historyCache
	originalStats := historyStatsCache
	originalModTime := lastModTime
	originalAtuinModTime := lastAtuinModTime
	historyCache = nil
	historyStatsCache = nil
	lastModTime = 0
	lastAtuinModTime = 0
	mu.Unlock()
	originalShell := shell.Current
	shell.Current = nil
	t.Cleanup(func() {
		mu.Lock()
		historyCache = originalCache
		historyStatsCache = originalStats
		lastModTime = originalModTime
		lastAtuinModTime = originalAtuinModTime
		mu.Unlock()
		shell.Current = originalShell
	})

	if _, err := SearchHistory("", nil); err != nil {
		t.Fatal(err)
	}
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".bash_history"), []byte("cd old-project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	originalCache, originalStats := historyCache, historyStatsCache
	originalModTime, originalAtuinModTime := lastModTime, lastAtuinModTime
	historyCache, historyStatsCache = nil, nil
	lastModTime, lastAtuinModTime = 0, 0
	mu.Unlock()
	originalShell := shell.Current
	shell.Current = nil
	t.Cleanup(func() {
		mu.Lock()
		historyCache, historyStatsCache = originalCache, originalStats
		lastModTime, lastAtuinModTime = originalModTime, originalAtuinModTime
		mu.Unlock()
		shell.Current = originalShell
	})
	if _, err := SearchHistory("", nil); err != nil {
		t.Fatal(err)
	}
	stats := HistorySnapshot()
	if len(stats) != 1 || !stats[0].LastUsed.IsZero() {
		t.Fatalf("expected timestamp-less history to have unknown recency, got %+v", stats)
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
	if len(occurrences) != 1 {
		t.Fatalf("expected one Atuin entry, got %+v", occurrences)
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
