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
