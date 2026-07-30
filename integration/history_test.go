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
