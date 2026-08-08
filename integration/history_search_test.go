package integration

import (
	"testing"
	"time"
)

func TestSearchRichHistoryMatchesAnywhereAndPreservesExecutions(t *testing.T) {
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	entries := []HistoryEntry{
		{
			ID:        "first",
			Command:   "just deploy staging",
			Cwd:       "/repo",
			StartedAt: now.Add(-time.Hour),
			Duration:  45 * time.Second,
			Source:    "atuin",
		},
		{
			ID:        "second",
			Command:   "kubectl rollout status deploy/api",
			Cwd:       "/repo",
			StartedAt: now.Add(-12 * time.Second),
			Duration:  12 * time.Second,
			Source:    "vuja",
		},
		{
			ID:        "third",
			Command:   "just deploy staging",
			Cwd:       "/repo",
			StartedAt: now.Add(-8 * time.Hour),
			Duration:  2 * time.Second,
			Source:    "atuin",
		},
		{ID: "other", Command: "go test ./...", Cwd: "/repo", StartedAt: now.Add(-time.Minute)},
	}

	results := SearchRichHistory(entries, "deploy", RichHistorySearchOptions{Now: now})

	if len(results) != 3 {
		t.Fatalf("expected 3 per-execution matches, got %d: %+v", len(results), results)
	}
	if results[0].Command != "just deploy staging" {
		t.Fatalf("expected compact earlier substring to rank first, got %q", results[0].Command)
	}
	if results[0].RelativeTime != "1h ago" || results[0].Duration != 45*time.Second {
		t.Fatalf("expected execution metadata to be preserved, got %+v", results[0])
	}

	seenIDs := make(map[string]bool)
	for _, result := range results {
		seenIDs[result.ID] = true
		if got := matchedRunes(result.Command, result.MatchRanges); got != "deploy" {
			t.Fatalf("expected exact match ranges for %q, got %q from %+v", result.Command, got, result.MatchRanges)
		}
	}
	if !seenIDs["first"] || !seenIDs["second"] || !seenIDs["third"] {
		t.Fatalf("expected repeated executions to remain distinct, got IDs %+v", seenIDs)
	}
}

func TestRecentRichHistoryIsNewestFirstPreservesRepeatsAndMetadata(t *testing.T) {
	now := time.Date(2026, time.August, 8, 10, 0, 0, 0, time.UTC)
	entries := []HistoryEntry{
		{ID: "older", Command: "git status", Cwd: "/repo/old", StartedAt: now.Add(-time.Hour), Source: "zsh"},
		{ID: "newest", Command: "just test", Cwd: "/repo", StartedAt: now.Add(-time.Second), Duration: 2 * time.Second, ExitCode: 1, HasExitCode: true, Source: "vuja"},
		{ID: "repeat", Command: "git status", Cwd: "/repo", StartedAt: now.Add(-time.Minute), Duration: 50 * time.Millisecond, ExitCode: 0, HasExitCode: true, Source: "atuin"},
	}

	results := RecentRichHistory(entries, now, 3)
	if len(results) != 3 {
		t.Fatalf("expected three executions, got %+v", results)
	}
	if results[0].ID != "newest" || results[1].ID != "repeat" || results[2].ID != "older" {
		t.Fatalf("expected deterministic newest-first order, got %+v", results)
	}
	if results[0].Cwd != "/repo" || results[0].Duration != 2*time.Second || !results[0].HasExitCode || results[0].ExitCode != 1 {
		t.Fatalf("expected execution metadata to survive, got %+v", results[0])
	}
	if results[1].Command != results[2].Command {
		t.Fatalf("expected repeated executions to remain distinct, got %+v", results)
	}
}

func TestRecentRichHistoryUsesFileOrderWhenShellHistoryHasNoTimestamps(t *testing.T) {
	results := RecentRichHistory([]HistoryEntry{
		{ID: "shell:old", Command: "first", Source: "zsh", historyOrder: 1},
		{ID: "shell:new", Command: "second", Source: "zsh", historyOrder: 2},
	}, time.Time{}, 2)
	if len(results) != 2 || results[0].Command != "second" || results[1].Command != "first" {
		t.Fatalf("expected the last shell-history line first, got %+v", results)
	}
}

func TestCurrentRecentRichHistoryMergesPersistedAndLiveExecutionsWithoutCollapsingRepeats(t *testing.T) {
	now := time.Date(2026, time.August, 8, 10, 0, 0, 0, time.UTC)

	richHistoryMu.Lock()
	previousRich := richHistoryEntries
	previousPersistent := persistentHistoryCache
	previousLive := liveHistoryEntries
	persistentHistoryCache = []HistoryEntry{
		{ID: "persisted", Command: "git status", StartedAt: now.Add(-time.Minute), Source: "zsh"},
	}
	liveHistoryEntries = []HistoryEntry{
		{ID: "live", Command: "git status", StartedAt: now.Add(-time.Second), Duration: time.Second, ExitCode: 0, HasExitCode: true, Source: "vuja"},
	}
	richHistoryEntries = append([]HistoryEntry(nil), liveHistoryEntries...)
	richHistoryMu.Unlock()
	t.Cleanup(func() {
		richHistoryMu.Lock()
		richHistoryEntries = previousRich
		persistentHistoryCache = previousPersistent
		liveHistoryEntries = previousLive
		richHistoryMu.Unlock()
	})

	results := CurrentRecentRichHistory(now, 10)
	if len(results) != 2 || results[0].ID != "live" || results[1].ID != "persisted" {
		t.Fatalf("expected merged newest-first executions with repeats preserved, got %+v", results)
	}
}

func TestSearchRichHistoryFuzzyMatchRangesAreUnicodeSafe(t *testing.T) {
	entries := []HistoryEntry{
		{ID: "compact", Command: "déployer production"},
		{ID: "wide", Command: "docker export package release"},
	}

	results := SearchRichHistory(entries, "dpr", RichHistorySearchOptions{})

	if len(results) != 2 {
		t.Fatalf("expected both fuzzy matches, got %+v", results)
	}
	if results[0].ID != "compact" {
		t.Fatalf("expected the compact fuzzy match first, got %+v", results)
	}
	if got := matchedRunes(results[0].Command, results[0].MatchRanges); got != "dpr" {
		t.Fatalf("expected Unicode-safe rune ranges, got %q from %+v", got, results[0].MatchRanges)
	}
	if len(results[0].MatchRanges) != 3 {
		t.Fatalf("expected non-contiguous matches to remain distinct, got %+v", results[0].MatchRanges)
	}
}

func TestSearchRichHistoryUsesTheMostCompactFuzzyWindow(t *testing.T) {
	results := SearchRichHistory(
		[]HistoryEntry{{ID: "window", Command: "d long p long r then dxpyr"}},
		"dpr",
		RichHistorySearchOptions{},
	)

	if len(results) != 1 {
		t.Fatalf("expected a fuzzy match, got %+v", results)
	}
	if results[0].MatchRanges[0].Start < 20 {
		t.Fatalf("expected the compact later fuzzy window, got %+v", results[0].MatchRanges)
	}
}

func TestSearchRichHistoryFiltersIndividualExecutionsByScopeAndOutcome(t *testing.T) {
	entries := []HistoryEntry{
		{ID: "success", Command: "just deploy staging", Cwd: "/repo/service", ExitCode: 0, HasExitCode: true, Host: "devbox", SessionID: "current"},
		{ID: "failure", Command: "just deploy staging", Cwd: "/repo/service", ExitCode: 1, HasExitCode: true},
		{ID: "project", Command: "just deploy production", Cwd: "/repo/worker", ExitCode: 0, HasExitCode: true, Host: "devbox", SessionID: "old"},
		{ID: "global", Command: "just deploy elsewhere", Cwd: "/other", ExitCode: 0, HasExitCode: true},
	}

	directory := SearchRichHistory(entries, "deploy", RichHistorySearchOptions{
		Cwd:            "/repo/service",
		ProjectRoot:    "/repo",
		Scope:          HistoryScopeDirectory,
		SuccessfulOnly: true,
	})
	if len(directory) != 1 || directory[0].ID != "success" {
		t.Fatalf("expected only the successful directory execution, got %+v", directory)
	}

	project := SearchRichHistory(entries, "deploy", RichHistorySearchOptions{
		Cwd:         "/repo/service",
		ProjectRoot: "/repo",
		Scope:       HistoryScopeProject,
	})
	if len(project) != 3 {
		t.Fatalf("expected all project executions, got %+v", project)
	}

	machine := SearchRichHistory(entries, "deploy", RichHistorySearchOptions{
		Scope: HistoryScopeMachine,
		Host:  "devbox",
	})
	if len(machine) != 2 {
		t.Fatalf("expected host-matched executions, got %+v", machine)
	}
	session := SearchRichHistory(entries, "deploy", RichHistorySearchOptions{
		Scope:     HistoryScopeSession,
		SessionID: "current",
	})
	if len(session) != 1 || session[0].ID != "success" {
		t.Fatalf("expected only the current shell session, got %+v", session)
	}
}

func TestRichHistoryMetadataFormatting(t *testing.T) {
	now := time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		startedAt  time.Time
		duration   time.Duration
		wantAgo    string
		wantLength string
	}{
		{name: "seconds", startedAt: now.Add(-12 * time.Second), duration: 250 * time.Millisecond, wantAgo: "12s ago", wantLength: "250ms"},
		{name: "hours", startedAt: now.Add(-8 * time.Hour), duration: 45 * time.Second, wantAgo: "8h ago", wantLength: "45s"},
		{name: "date", startedAt: now.Add(-8 * 24 * time.Hour), duration: 2*time.Minute + 3*time.Second, wantAgo: "2026-07-22", wantLength: "2m3s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatRelativeTime(test.startedAt, now); got != test.wantAgo {
				t.Fatalf("expected relative time %q, got %q", test.wantAgo, got)
			}
			if got := formatHistoryDuration(test.duration); got != test.wantLength {
				t.Fatalf("expected duration %q, got %q", test.wantLength, got)
			}
		})
	}
}

func TestReplaceRichHistoryEntriesPreservesLiveSessionExecutions(t *testing.T) {
	richHistoryMu.Lock()
	originalEntries := richHistoryEntries
	originalLive := liveHistoryEntries
	originalPersistent := persistentHistoryCache
	richHistoryEntries = nil
	liveHistoryEntries = nil
	persistentHistoryCache = nil
	richHistoryMu.Unlock()
	t.Cleanup(func() {
		richHistoryMu.Lock()
		richHistoryEntries = originalEntries
		liveHistoryEntries = originalLive
		persistentHistoryCache = originalPersistent
		richHistoryMu.Unlock()
	})

	live := HistoryEntry{
		ID:        "vuja:live",
		Command:   "just deploy staging",
		StartedAt: time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC),
		Source:    "vuja",
		SessionID: "current",
	}
	AppendRichHistoryEntry(live)
	ReplaceRichHistoryEntries([]HistoryEntry{{ID: "atuin:old", Command: "git status", Source: "atuin"}})

	entries := RichHistorySnapshot()
	if len(entries) != 2 || entries[0].ID != live.ID {
		t.Fatalf("expected live execution to survive the asynchronous import refresh, got %+v", entries)
	}
}

func matchedRunes(command string, ranges []MatchRange) string {
	runes := []rune(command)
	var matched []rune
	for _, match := range ranges {
		matched = append(matched, runes[match.Start:match.End]...)
	}
	return string(matched)
}

func TestFilterHistoryResultsAppliesDirectoryAndSuccessScope(t *testing.T) {
	results := []HistResult{
		{Cmd: "go test ./..."},
		{Cmd: "go test failing"},
		{Cmd: "npm test"},
	}
	stats := []HistoryStat{
		{Command: "go test ./...", Cwd: "/repo/service", ExitCode: 0, HasExitCode: true},
		{Command: "go test failing", Cwd: "/repo/service", ExitCode: 1, HasExitCode: true},
		{Command: "npm test", Cwd: "/other", ExitCode: 0, HasExitCode: true},
	}

	filtered := filterHistoryResults(results, stats, HistorySearchOptions{
		Cwd:            "/repo/service",
		Scope:          HistoryScopeDirectory,
		SuccessfulOnly: true,
	})
	if len(filtered) != 1 || filtered[0].Cmd != "go test ./..." {
		t.Fatalf("unexpected filtered results: %+v", filtered)
	}
}
