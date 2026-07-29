package integration

import (
	"os"
	"path/filepath"
	"testing"

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
	historyCache = nil
	historyStatsCache = nil
	lastModTime = 0
	mu.Unlock()
	originalShell := shell.Current
	shell.Current = nil
	t.Cleanup(func() {
		mu.Lock()
		historyCache = originalCache
		historyStatsCache = originalStats
		lastModTime = originalModTime
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
