package integration

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestUnavailableExternalHistoryCannotChangeVujaOwnedRecall(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	PublishCanonicalHistory([]HistoryEntry{{
		ID: "vuja:owned", Command: "ssh forge@api", StartedAt: time.Now(), Source: "vuja",
	}})
	before, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAtuinHistoryEntries(t.Context(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("expected the absent optional Atuin source to fail independently")
	}
	after, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("expected optional source failure not to alter canonical recall, before=%v after=%v", before, after)
	}
}

func TestCanonicalHistoryRecallsThirtySSHCommandsAcrossPublishedGenerations(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entries := make([]HistoryEntry, 0, 30)
	for index := range 30 {
		entries = append(entries, HistoryEntry{
			ID:        fmt.Sprintf("vuja:ssh:%02d", index),
			Command:   fmt.Sprintf("ssh forge@host-%02d", index),
			Cwd:       "/repo",
			StartedAt: now.Add(time.Duration(index) * time.Minute),
			Source:    "vuja",
			State:     HistoryStateCompleted,
		})
	}

	PublishCanonicalHistory(entries)
	results, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 30 {
		t.Fatalf("expected all 30 Vuja-owned SSH commands, got %d", len(results))
	}
	for _, result := range results {
		if result.Cmd == "" {
			t.Fatal("expected non-empty canonical history candidate")
		}
	}
}

func TestCanonicalHistoryIncrementalSearchRestartsAfterTruncatedBroadPrefix(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entries := make([]HistoryEntry, 0, 1230)
	for index := range 1200 {
		entries = append(entries, HistoryEntry{
			ID: fmt.Sprintf("vuja:broad:%04d", index), Command: fmt.Sprintf("status-command-%04d", index),
			StartedAt: now.Add(time.Duration(index) * time.Second), Source: "vuja", State: HistoryStateCompleted,
		})
	}
	for index := range 30 {
		entries = append(entries, HistoryEntry{
			ID: fmt.Sprintf("vuja:ssh:%02d", index), Command: fmt.Sprintf("ssh forge@host-%02d", index),
			StartedAt: now.Add(-time.Duration(index+1) * time.Hour), Source: "vuja", State: HistoryStateCompleted,
		})
	}
	PublishCanonicalHistory(entries)

	direct, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	resetIncrementalHistorySearchLocked()
	if _, err := SearchHistory("s", nil); err != nil {
		t.Fatal(err)
	}
	if !CurrentHistorySearchStatus().Truncated {
		t.Fatal("expected the broad prefix to report truncation")
	}
	if _, err := SearchHistory("ss", nil); err != nil {
		t.Fatal(err)
	}
	incremental, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct, incremental) {
		t.Fatalf("expected direct and incremental recall to match\ndirect=%v\nincremental=%v", direct, incremental)
	}
}

func TestCanonicalHistoryPreservesRepeatedExecutionsButDeduplicatesInlineCandidates(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Now()
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "vuja:1", Command: "ssh forge@api", StartedAt: now, Source: "vuja", State: HistoryStateCompleted},
		{ID: "vuja:2", Command: "ssh forge@api", StartedAt: now.Add(time.Minute), Source: "vuja", State: HistoryStateCompleted},
	})

	inline, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 1 {
		t.Fatalf("expected one aggregated inline candidate, got %+v", inline)
	}
	rich := SearchCurrentRichHistory("ssh", RichHistorySearchOptions{Limit: 10})
	if len(rich) != 2 {
		t.Fatalf("expected both execution events in rich history, got %+v", rich)
	}
	counts := ExplainCanonicalHistory("ssh")
	if counts.Events != 2 || counts.Candidates != 1 || counts.Eligible != 1 {
		t.Fatalf("expected explain counts before and after inline deduplication, got %+v", counts)
	}
}

func TestCanonicalHistoryRejectsSensitiveAndLeadingSpaceEntriesAtPublicationBoundary(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })

	PublishCanonicalHistory([]HistoryEntry{
		{ID: "safe", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api", Source: "vuja"},
		{ID: "leading", Command: " ssh forge@private", NormalizedCommand: "ssh forge@private", Source: "vuja"},
		{ID: "sensitive-exact", Command: "curl --token private-value https://example.test", NormalizedCommand: "echo safe", Source: "vuja"},
		{ID: "sensitive-normalized", Command: "echo safe", NormalizedCommand: "curl --token private-value https://example.test", Source: "vuja"},
	})
	PublishCanonicalHistoryEntry(HistoryEntry{
		ID: "incremental-sensitive", Command: "curl --token another-private-value https://example.test",
		NormalizedCommand: "echo safe", Source: "vuja",
	})

	entries := RichHistorySnapshot()
	if len(entries) != 1 || entries[0].ID != "safe" {
		t.Fatalf("expected only the safe canonical event, got %+v", entries)
	}
	results, err := SearchHistory("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Cmd != "ssh forge@api" {
		t.Fatalf("expected only the safe inline candidate, got %+v", results)
	}
}

func TestCachedAndFullHistoryUseTheSameCommandEligibilityRules(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "1", Command: "ssh forge@api", StartedAt: now, Source: "vuja"},
		{ID: "2", Command: "show ssh configuration", StartedAt: now.Add(-time.Second), Source: "vuja"},
		{ID: "3", Command: "git checkout main", StartedAt: now.Add(-2 * time.Second), Source: "vuja"},
		{ID: "4", Command: "git cherry-pick deadbeef", StartedAt: now.Add(-3 * time.Second), Source: "vuja"},
	})

	for _, query := range []string{"ssh", "git ch"} {
		cached, available := SearchCachedHistory(query, nil)
		if !available {
			t.Fatalf("expected cached history for %q", query)
		}
		full, err := SearchHistory(query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(historyResultCommands(cached), historyResultCommands(full)) {
			t.Fatalf("expected cached and full eligibility to agree for %q, cached=%v full=%v", query, cached, full)
		}
	}
	if counts := ExplainCanonicalHistory("ssh"); counts.Eligible != 1 {
		t.Fatalf("expected explain to count the same one eligible command, got %+v", counts)
	}
}

func historyResultCommands(results []HistResult) []string {
	commands := make([]string, len(results))
	for index := range results {
		commands[index] = results[index].Cmd
	}
	return commands
}

func TestStaleImportedExecutionCannotReplaceNewerVujaMetadata(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "atuin:stale", Command: "ssh forge@api", StartedAt: now.Add(-24 * time.Hour), Source: "atuin", State: HistoryStateUnknown},
		{ID: "vuja:new", Command: "ssh forge@api", StartedAt: now, Duration: 3 * time.Second, ExitCode: 0, HasExitCode: true, Source: "vuja", State: HistoryStateCompleted},
	})

	entries := RichHistorySnapshot()
	if len(entries) != 2 || entries[0].ID != "vuja:new" || entries[0].Duration != 3*time.Second {
		t.Fatalf("expected newer Vuja metadata to remain authoritative, got %+v", entries)
	}
	inline, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inline) != 1 || inline[0].Cmd != "ssh forge@api" {
		t.Fatalf("expected one deduplicated inline candidate, got %+v", inline)
	}
}

func TestCanonicalHistoryFeedsInlineRecentAndRichSearchDeterministically(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	entries := []HistoryEntry{
		{ID: "older", Command: "ssh forge@api", StartedAt: now.Add(-time.Minute), Source: "vuja"},
		{ID: "newer", Command: "ssh forge@web", StartedAt: now, Source: "vuja"},
	}
	PublishCanonicalHistory(entries)
	first, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SearchHistory("ssh", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected deterministic inline ordering, first=%v second=%v", first, second)
	}
	recent := CurrentRecentRichHistory(now, 10)
	rich := SearchCurrentRichHistory("ssh", RichHistorySearchOptions{Now: now, Limit: 10})
	if len(first) != 2 || len(recent) != 2 || len(rich) != 2 || recent[0].ID != "newer" {
		t.Fatalf("expected one canonical generation across surfaces, inline=%v recent=%v rich=%v", first, recent, rich)
	}
}

func TestCanonicalHistoryPublicationOwnsItsImmutableGeneration(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	entries := []HistoryEntry{{ID: "owned", Command: "ssh forge@api", Source: "vuja"}}

	PublishCanonicalHistory(entries)
	entries[0].Command = "mutated by caller"
	entries = append(entries, HistoryEntry{ID: "late", Command: "echo late", Source: "vuja"})
	if len(entries) != 2 {
		t.Fatalf("expected caller-owned fixture to contain two entries, got %d", len(entries))
	}

	snapshot := RichHistorySnapshot()
	if len(snapshot) != 1 || snapshot[0].Command != "ssh forge@api" {
		t.Fatalf("expected publication to own an immutable generation, got %+v", snapshot)
	}
}

func TestCanonicalHistoryIncrementalPublicationPreservesExecutionRecency(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.October, 7, 11, 0, 0, 0, time.UTC)
	entries := []HistoryEntry{{ID: "newer", Command: "ssh forge@new", StartedAt: now, Source: "vuja"}}
	PublishCanonicalHistory(entries)
	// Prime the incremental query cache before a historical event arrives.
	if _, err := SearchHistory("ssh", nil); err != nil {
		t.Fatal(err)
	}
	updates := []HistoryEntry{
		{ID: "older", Command: "ssh forge@old", StartedAt: now.Add(-time.Hour), Source: "vuja"},
		{ID: "old-repeat", Command: "ssh forge@old", StartedAt: now.Add(-2 * time.Hour), Cwd: "/other", Source: "vuja"},
		{ID: "tie-a", Command: "ssh forge@tie-a", StartedAt: now, HistoryOrder: 1, Source: "vuja"},
		{ID: "tie-b", Command: "ssh forge@tie-b", StartedAt: now, HistoryOrder: 1, Source: "vuja"},
		{ID: "newest", Command: "ssh forge@old", StartedAt: now.Add(time.Hour), Source: "vuja"},
		{ID: "newest", Command: "ssh forge@old", StartedAt: now.Add(-3 * time.Hour), Source: "vuja", State: HistoryStateCompleted},
		{ID: "newest", Command: "ssh forge@corrected", StartedAt: now.Add(2 * time.Hour), Source: "vuja"},
	}
	want := [][]string{
		{"ssh forge@new", "ssh forge@old"},
		{"ssh forge@new", "ssh forge@old"},
		{"ssh forge@tie-a", "ssh forge@new", "ssh forge@old"},
		{"ssh forge@tie-b", "ssh forge@tie-a", "ssh forge@new", "ssh forge@old"},
		{"ssh forge@old", "ssh forge@tie-b", "ssh forge@tie-a", "ssh forge@new"},
		{"ssh forge@tie-b", "ssh forge@tie-a", "ssh forge@new", "ssh forge@old"},
		{"ssh forge@corrected", "ssh forge@tie-b", "ssh forge@tie-a", "ssh forge@new", "ssh forge@old"},
	}
	for i, entry := range updates {
		PublishCanonicalHistoryEntry(entry)
		for _, query := range []string{"", "ssh", "ssh forge@"} {
			results, err := SearchHistory(query, nil)
			if err != nil {
				t.Fatal(err)
			}
			commands := make([]string, len(results))
			for j, result := range results {
				commands[j] = result.Cmd
			}
			if !reflect.DeepEqual(commands, want[i]) {
				t.Fatalf("update %s query %q: execution order=%v want=%v", entry.ID, query, commands, want[i])
			}
		}
	}
}

func TestCanonicalHistoryRecoveredTieCannotReplaceNewerOutcome(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	for _, testCase := range []struct{ higherOrder, unknownTime bool }{{}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(testCase), func(t *testing.T) {
			now := time.Date(2026, time.October, 7, 11, 0, 0, 0, time.UTC)
			if testCase.unknownTime {
				now = time.Time{}
			}
			newer := HistoryEntry{ID: "z-newer", Command: "ssh forge@same", Cwd: "/repo", StartedAt: now, Source: "vuja", HasExitCode: true, ExitCode: 1, Duration: time.Second}
			older := newer
			older.ID, older.ExitCode = "a-older", 0
			older.Duration, older.Source = 2*time.Second, "zsh"
			if testCase.higherOrder {
				newer.ID, older.ID = "a-newer", "z-older"
				newer.HistoryOrder = 2
				older.HistoryOrder = 1
			}
			PublishCanonicalHistory([]HistoryEntry{newer})
			PublishCanonicalHistoryEntry(older)
			stats := HistorySnapshot()
			if len(stats) != 1 || stats[0].Count != 2 || !stats[0].HasExitCode || stats[0].ExitCode != 1 || stats[0].Duration != time.Second || stats[0].Source != "vuja" {
				t.Fatalf("older recovered outcome replaced newer failure: %+v", stats)
			}
			results, err := SearchHistoryWithOptions("ssh", nil, HistorySearchOptions{SuccessfulOnly: true})
			if err != nil || len(results) != 0 {
				t.Fatalf("failed command leaked into successful recall: %+v err=%v", results, err)
			}
			PublishCanonicalHistory([]HistoryEntry{older, newer})
			stats = HistorySnapshot()
			if len(stats) != 1 || stats[0].Count != 2 || stats[0].ExitCode != 1 || stats[0].Duration != time.Second || stats[0].Source != "vuja" {
				t.Fatalf("full publication lost newer outcome: %+v", stats)
			}
			// The real newest event may subsequently receive a known outcome.
			newer.ExitCode = 0
			newer.Duration = 3 * time.Second
			PublishCanonicalHistoryEntry(newer)
			stats = HistorySnapshot()
			if len(stats) != 1 || stats[0].Count != 2 || stats[0].ExitCode != 0 || stats[0].Duration != 3*time.Second {
				t.Fatalf("newest completion did not replace its own outcome: %+v", stats)
			}
		})
	}
}
