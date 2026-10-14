package scoring

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type ageHistoryRetentionStore interface {
	PruneHistoryEventsWithRetention(context.Context, int, time.Time) (int64, error)
}

func TestHistoryAgeRetentionRefusesMixedUnattributedLegacyNavigation(t *testing.T) {
	for _, mode := range []string{"age", "count"} {
		t.Run(mode, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now().UTC()
			completedForgetFixture(t, store, "old-cd", "cd /project", now.Add(-4*time.Hour))
			if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"keep-z-1", "keep-z-2", "keep-z-3", "keep-z-4"} {
				completedForgetFixture(t, store, id, "z project", now.Add(-time.Hour))
				if err := store.RecordDirectory(t.Context(), "/project"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
			if err != nil {
				t.Fatal(err)
			}
			maxEvents, cutoff := 0, now.Add(-2*time.Hour)
			if mode == "count" {
				maxEvents, cutoff = 4, time.Time{}
			}
			if removed, err := store.PruneHistoryEventsWithRetention(t.Context(), maxEvents, cutoff); err == nil || removed != 0 {
				t.Fatalf("retention must not erase independent legacy navigation: removed=%d error=%v", removed, err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 5 {
				t.Fatalf("refused retention changed canonical executions: %+v %v", events, err)
			}
			if entries, err := store.QueryDirectories(t.Context(), "/project", 10, "frequent"); err != nil || len(entries) != 1 || entries[0].Count != 5 {
				t.Fatalf("refused retention changed directory evidence: %+v %v", entries, err)
			}
			if changes, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256); err != nil || len(changes.Changes) != 0 {
				t.Fatalf("refused retention published a reset: %+v %v", changes, err)
			}
		})
	}
}

func TestHistoryAgeRetentionRefusesExpiredUnattributedLegacyNavigation(t *testing.T) {
	for _, mode := range []string{"age", "count"} {
		t.Run(mode, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now().UTC()
			completedForgetFixture(t, store, "old-z", "z project", now.Add(-4*time.Hour))
			completedForgetFixture(t, store, "keep", "git status", now.Add(-time.Hour))
			for _, path := range []string{"/project", "/unrelated"} {
				if err := store.RecordDirectory(t.Context(), path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.QueryHistoryChanges(t.Context(), 0, 256)
			if err != nil {
				t.Fatal(err)
			}
			maxEvents, cutoff := 0, now.Add(-2*time.Hour)
			if mode == "count" {
				maxEvents, cutoff = 1, time.Time{}
			}
			if removed, err := store.PruneHistoryEventsWithRetention(t.Context(), maxEvents, cutoff); err == nil || removed != 0 {
				t.Fatalf("unattributed expiry must refuse rather than retain expired learning: removed=%d error=%v", removed, err)
			}
			events, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || len(events) != 2 {
				t.Fatalf("refused retention changed canonical executions: %+v %v", events, err)
			}
			for _, event := range events {
				if event.EventKey != "old-z" && event.EventKey != "keep" {
					t.Fatalf("refused retention changed an identity: %+v", event)
				}
			}
			for _, path := range []string{"/project", "/unrelated"} {
				if entries, err := store.QueryDirectories(t.Context(), path, 10, "frequent"); err != nil || len(entries) != 1 || entries[0].Count != 1 {
					t.Fatalf("refused retention changed directory evidence: %+v %v", entries, err)
				}
			}
			if changes, err := store.QueryHistoryChanges(t.Context(), before.Latest, 256); err != nil || len(changes.Changes) != 0 {
				t.Fatalf("refused retention published a reset: %+v %v", changes, err)
			}
		})
	}
}

// QA scenario3: exact age boundaries, unfinished/unknown protection, imports,
// derived evidence and count policy all use the retained canonical identities.
func TestHistoryAgeRetentionHonorsExactCutoffAndProtectedExecutions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	retention, ok := any(store).(ageHistoryRetentionStore)
	if !ok {
		t.Fatal("history cannot enforce optional age retention")
	}
	cutoff := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	for _, fixture := range []struct {
		id string
		at time.Time
	}{{"native-old", cutoff.Add(-time.Nanosecond)}, {"native-exact", cutoff}, {"native-new", cutoff.Add(time.Nanosecond)}} {
		completedForgetFixture(t, store, fixture.id, "ssh forge@"+fixture.id, fixture.at)
	}
	if err := store.RecordExecutionFeedback(t.Context(), "native-old", "ssh forge@offered", "/repo", "dismissed"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{EventKey: "native-running", Command: "ssh forge@running", Cwd: "/repo", Source: "vuja", StartedAt: cutoff.Add(-30 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{
		{EventKey: "import-old", Command: "ssh forge@import-old", Cwd: "/repo", Source: "zsh", StartedAt: cutoff.Add(-time.Nanosecond), Imported: true, State: "completed"},
		{EventKey: "import-new", Command: "ssh forge@import-new", Cwd: "/repo", Source: "zsh", StartedAt: cutoff.Add(time.Nanosecond), Imported: true, State: "completed"},
		{EventKey: "import-unknown", Command: "ssh forge@unknown", Cwd: "/repo", Source: "zsh", Imported: true, State: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	if removed, err := retention.PruneHistoryEventsWithRetention(t.Context(), 0, cutoff); err != nil || removed != 2 {
		t.Fatalf("age cutoff: removed=%d error=%v", removed, err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"native-exact": true, "native-new": true, "native-running": true, "import-new": true, "import-unknown": true}
	for _, event := range events {
		if !want[event.EventKey] {
			t.Fatalf("age retention kept an expired or unexpected execution: %+v", event)
		}
		delete(want, event.EventKey)
	}
	if len(want) != 0 {
		t.Fatalf("age retention removed protected executions: %v", want)
	}
	feedback, err := store.QueryFeedback(t.Context(), "/repo", "", "ssh forge@offered", 10)
	if err != nil || len(feedback) != 0 {
		t.Fatalf("expired execution left feedback behind: %+v %v", feedback, err)
	}
	if removed, err := retention.PruneHistoryEventsWithRetention(t.Context(), 1, cutoff); err != nil || removed != 1 {
		t.Fatalf("combined age/count retention: removed=%d error=%v", removed, err)
	}
	events, err = store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 4 {
		t.Fatalf("count retention changed imports or running history: %+v %v", events, err)
	}
	for _, event := range events {
		if event.EventKey == "native-exact" {
			t.Fatal("count policy did not keep the newest native execution")
		}
	}
}
