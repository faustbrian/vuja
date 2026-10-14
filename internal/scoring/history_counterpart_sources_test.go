package scoring

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryCounterpartSourcesReleaseNonForgottenOwners(t *testing.T) {
	for _, operation := range []string{"clear", "age", "source replacement"} {
		t.Run(operation, func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			pair := []HistoryEvent{
				{EventKey: "atuin:owner", Command: "ssh example", Cwd: "/repo", Source: "atuin", StartedAt: at, State: "completed"},
				{EventKey: "zsh:copy", Command: "ssh example", Source: "zsh", StartedAt: at, State: "completed"},
			}
			if err := store.ReplaceAdapterHistoryEvents(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			var memberships int
			if err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM history_counterpart_sources`).Scan(&memberships); err != nil || memberships != 1 {
				t.Fatalf("expected one observed source membership: %d %v", memberships, err)
			}
			switch operation {
			case "clear":
				err = store.ClearHistory(t.Context())
			case "age":
				_, err = store.PruneHistoryEventsWithRetention(t.Context(), 0, at.Add(time.Second))
			case "source replacement":
				err = store.ReplaceAdapterHistoryEvents(t.Context(), nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
				t.Fatalf("owner removal did not reach canonical recall: %+v %v", events, err)
			}
			if err := store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM history_counterpart_sources`).Scan(&memberships); err != nil || memberships != 0 {
				t.Fatalf("removed owners left growing source memberships: %d %v", memberships, err)
			}
			for index := range pair {
				pair[index].StartedAt = at.Add(time.Hour)
			}
			if err := store.ReplaceAdapterHistoryEvents(t.Context(), pair); err != nil {
				t.Fatal(err)
			}
			if events, err := store.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 1 || events[0].EventKey != "atuin:owner" || !events[0].StartedAt.Equal(at.Add(time.Hour)) {
				t.Fatalf("retired memberships interfered with new source data: %+v %v", events, err)
			}
		})
	}
}

func TestHistoryCounterpartSourceMetadataRefreshSurvivesIDRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	native := completedForgetFixture(t, store, "native:first", "ssh example", at)
	copy := HistoryEvent{EventKey: "atuin:first", Command: native.Command, Cwd: "/repo", Source: "atuin", StartedAt: at, State: "unknown"}
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
		t.Fatal(err)
	}
	// The same source identity gains completion metadata before it is forgotten.
	copy.HasExitCode, copy.Duration, copy.State = true, time.Second, "completed"
	if err := store.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetHistoryEvents(t.Context(), []string{native.EventKey}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	copy.EventKey = "atuin:rotated"
	if err := reopened.ReplaceExternalHistoryEvents(t.Context(), []HistoryEvent{copy}); err != nil {
		t.Fatal(err)
	}
	if events, err := reopened.QueryAllHistoryEvents(t.Context()); err != nil || len(events) != 0 {
		t.Fatalf("enriched source metadata resurrected a forgotten execution after ID rotation: %+v %v", events, err)
	}
}
