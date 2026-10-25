package integration

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The first Err check admits the search; the second pauses matching after
// SearchHistoryContext has pinned its generation and read the narrowing cache.
type pausedHistorySearchContext struct {
	context.Context
	checks atomic.Int32
	pinned chan struct{}
	resume chan struct{}
}

func (ctx *pausedHistorySearchContext) Err() error {
	if ctx.checks.Add(1) == 2 {
		close(ctx.pinned)
		<-ctx.resume
	}
	return ctx.Context.Err()
}

func TestCanonicalHistoryNarrowedTypoFallbackMatchesColdSearch(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "status", Command: "git status", StartedAt: now},
		{ID: "checkout", Command: "git checkout", StartedAt: now.Add(-time.Hour)},
	})
	if _, err := SearchHistory("git sta", nil); err != nil {
		t.Fatal(err)
	}
	warm, err := SearchHistory("git statuz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resetIncrementalHistorySearchLocked()
	cold, err := SearchHistory("git statuz", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git status", "git checkout"}
	if !slices.Equal(historyResultCommands(warm), want) || !slices.Equal(historyResultCommands(cold), want) {
		t.Fatalf("prefix narrowing changed typo fallback: warm=%v cold=%v want=%v", historyResultCommands(warm), historyResultCommands(cold), want)
	}
}

func TestCanonicalHistoryAliasActivationMatchesColdSearch(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	for _, query := range []struct{ prefix, complete string }{{"git sta", "git status"}, {"gs", "gst"}} {
		t.Run(query.complete, func(t *testing.T) {
			now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
			PublishCanonicalHistory([]HistoryEntry{
				{ID: "status", Command: "git status", StartedAt: now},
				{ID: "alias", Command: "gst", StartedAt: now.Add(-time.Hour)},
			})
			aliases := map[string]string{"gst": "git status"}
			if _, err := SearchHistory(query.prefix, aliases); err != nil {
				t.Fatal(err)
			}
			warm, err := SearchHistory(query.complete, aliases)
			if err != nil {
				t.Fatal(err)
			}
			resetIncrementalHistorySearchLocked()
			cold, err := SearchHistory(query.complete, aliases)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"git status", "gst"}
			if !slices.Equal(historyResultCommands(warm), want) || !slices.Equal(historyResultCommands(cold), want) {
				t.Fatalf("alias activation lost eligible commands: warm=%v cold=%v want=%v", historyResultCommands(warm), historyResultCommands(cold), want)
			}
		})
	}
}

func TestCanonicalHistoryOlderSearchCannotRestoreStaleNarrowingAfterPublication(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	for _, change := range []string{"addition", "replacement", "correction"} {
		t.Run(change, func(t *testing.T) {
			now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
			PublishCanonicalHistory([]HistoryEntry{
				{ID: "old", Command: "ssh forge@old", StartedAt: now},
				{ID: "mutable", Command: "git status", StartedAt: now.Add(-time.Hour)},
			})
			ctx := &pausedHistorySearchContext{Context: t.Context(), pinned: make(chan struct{}), resume: make(chan struct{})}
			finished := make(chan struct{})
			result := make(chan error, 1)
			var resumeOnce sync.Once
			t.Cleanup(func() {
				resumeOnce.Do(func() { close(ctx.resume) })
				<-finished
			})
			go func() {
				defer close(finished)
				_, err := SearchHistoryContext(ctx, "s", nil)
				result <- err
			}()
			<-ctx.pinned
			newEntry := HistoryEntry{ID: "new", Command: "ssh forge@new", StartedAt: now.Add(time.Hour)}
			want := []string{"ssh forge@new", "ssh forge@old"}
			switch change {
			case "replacement":
				PublishCanonicalHistory([]HistoryEntry{newEntry})
				want = []string{"ssh forge@new"}
			case "correction":
				newEntry.ID = "mutable"
				PublishCanonicalHistoryEntry(newEntry)
			default:
				PublishCanonicalHistoryEntry(newEntry)
			}
			resumeOnce.Do(func() { close(ctx.resume) })
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			results, err := SearchHistory("ss", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(historyResultCommands(results), want) {
				t.Fatalf("older search poisoned current prefix recall: got %v, want %v", historyResultCommands(results), want)
			}
		})
	}
}

func TestCanonicalHistoryUnmovedRepeatPublicationAllocationDoesNotScaleWithCandidates(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	measure := func(candidates int) float64 {
		now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
		entries := make([]HistoryEntry, candidates+1)
		for index := range candidates {
			entries[index] = HistoryEntry{ID: fmt.Sprintf("seed-%d", index), Command: fmt.Sprintf("command-%d", index), StartedAt: now}
		}
		entries[candidates] = HistoryEntry{ID: "live-seed", Command: "ssh forge@live", StartedAt: now.Add(time.Hour)}
		PublishCanonicalHistory(entries)
		sequence := 0
		return testing.AllocsPerRun(10, func() {
			sequence++
			PublishCanonicalHistoryEntry(HistoryEntry{
				ID: fmt.Sprintf("live-%d", sequence), Command: "ssh forge@live",
				StartedAt: now.Add(time.Hour + time.Duration(sequence)),
			})
		})
	}
	small, large := measure(128), measure(1024)
	// Publishing a repeat without changing candidate order must not recreate
	// each candidate's search data. Allow constant map-growth/fixture overhead.
	if large > small+16 {
		t.Fatalf("unchanged-order publication allocations scale with candidates: 128=%g, 1024=%g", small, large)
	}
}

func TestCanonicalHistoryUnmovedRepeatPreservesSearchGenerationAndUpdatesEvents(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	for _, position := range []string{"newest", "middle", "oldest"} {
		t.Run(position, func(t *testing.T) {
			now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
			liveTime := now.Add(2 * time.Hour)
			if position == "newest" {
				liveTime = now.Add(4 * time.Hour)
			} else if position == "oldest" {
				liveTime = now
			}
			entries := []HistoryEntry{
				{ID: "upper", Command: "ssh forge@upper", StartedAt: now.Add(3 * time.Hour)},
				{ID: "lower", Command: "ssh forge@lower", StartedAt: now.Add(time.Hour)},
				{ID: "old-live", Command: "ssh forge@live", Cwd: "/repo", StartedAt: liveTime, HasExitCode: true},
			}
			PublishCanonicalHistory(entries)
			before, err := SearchHistory("ssh", nil)
			if err != nil {
				t.Fatal(err)
			}
			mu.RLock()
			pinnedCommands, pinnedIDs := historyCache, idMapCache
			commandsCopy := slices.Clone(pinnedCommands)
			idsCopy := make(map[string]int, len(pinnedIDs))
			for command, id := range pinnedIDs {
				idsCopy[command] = id
			}
			mu.RUnlock()
			PublishCanonicalHistoryEntry(HistoryEntry{
				ID: "new-live", Command: "ssh forge@live", Cwd: "/repo", StartedAt: liveTime.Add(time.Second),
				HasExitCode: true, ExitCode: 7, Duration: 2 * time.Second, Source: "vuja", State: HistoryStateFailed,
			})
			after, err := SearchHistory("ssh forge@", nil)
			if err != nil {
				t.Fatal(err)
			}
			cached, available := SearchCachedHistory("ssh forge@", nil)
			if !available || !slices.Equal(historyResultCommands(before), historyResultCommands(after)) ||
				!slices.Equal(historyResultCommands(before), historyResultCommands(cached)) {
				t.Fatalf("unmoved repeat changed candidate recall: before=%v after=%v cached=%v", before, after, cached)
			}
			if !slices.Equal(pinnedCommands, commandsCopy) || !reflect.DeepEqual(pinnedIDs, idsCopy) {
				t.Fatal("publication mutated a reader's pinned search generation")
			}
			counts := ExplainCanonicalHistory("ssh")
			if counts.Events != 4 || counts.Candidates != 3 || len(RichHistorySnapshot()) != 4 {
				t.Fatalf("repeat lost an execution or duplicated a candidate: %+v", counts)
			}
			for _, stat := range HistorySnapshot() {
				if stat.Command == "ssh forge@live" && (stat.Count != 2 || stat.ExitCode != 7 || !stat.HasExitCode ||
					stat.Duration != 2*time.Second || stat.Source != "vuja" || !stat.LastUsed.Equal(liveTime.Add(time.Second))) {
					t.Fatalf("repeat did not update newest command metadata: %+v", stat)
				}
			}
		})
	}
}

func TestCanonicalHistoryRepeatedPublicationWithConcurrentCachedReaders(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	now := time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)
	PublishCanonicalHistory([]HistoryEntry{
		{ID: "seed-live", Command: "ssh forge@live", StartedAt: now},
		{ID: "seed-old", Command: "ssh forge@old", StartedAt: now.Add(-time.Hour)},
	})
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				results, available := SearchCachedHistory("ssh", nil)
				if available && !slices.Equal(historyResultCommands(results), []string{"ssh forge@live", "ssh forge@old"}) {
					t.Errorf("concurrent reader saw inconsistent candidates: %v", results)
					return
				}
			}
		})
	}
	for index := range 100 {
		PublishCanonicalHistoryEntry(HistoryEntry{ID: fmt.Sprintf("live-%d", index), Command: "ssh forge@live", StartedAt: now.Add(time.Duration(index + 1))})
	}
	workers.Wait()
	if counts := ExplainCanonicalHistory("ssh"); counts.Events != 102 || counts.Candidates != 2 {
		t.Fatalf("concurrent publication lost executions: %+v", counts)
	}
}
