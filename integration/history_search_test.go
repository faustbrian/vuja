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
