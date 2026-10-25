package scoring

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/internal/health"
)

func TestSuggestionStoreHealthDistinguishesEmptyFromClosedDatabase(t *testing.T) {
	for _, source := range []health.Source{health.Directory, health.Arguments, health.Recovery, health.Signals} {
		t.Run(source.Name(), func(t *testing.T) {
			store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			InvalidateSignalCache()
			t.Cleanup(InvalidateSignalCache)
			cwd := t.TempDir()
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) {
				// Signal collection also reports workspace discovery. This test
				// owns the selected store source, not unrelated provider reports.
				if value.Source == source {
					observations = append(observations, value)
				}
			})
			query := func(ctx context.Context) {
				switch source {
				case health.Directory:
					_, _ = store.QueryDirectories(ctx, "", 10, "balanced")
				case health.Arguments:
					_, _ = store.QueryArgumentValues(ctx, cwd, cwd, "git switch", 2, "", 10)
				case health.Recovery:
					_, _ = store.QueryRecentFailure(ctx, cwd, time.Minute)
				case health.Signals:
					_ = CollectSignalsWithRanking(ctx, cwd, "git", "git", store, "", "", "balanced")
				}
			}
			query(ctx)
			if len(observations) != 1 || observations[0].Source != source || observations[0].Outcome != health.Healthy {
				t.Fatalf("valid empty source was not observed as healthy: %+v", observations)
			}
			observations = nil
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			InvalidateSignalCache()
			query(ctx)
			if len(observations) != 1 || observations[0].Source != source || observations[0].Outcome != health.Failed {
				t.Fatalf("database failure was silently collapsed to empty: %+v", observations)
			}
			observations = nil
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			InvalidateSignalCache()
			query(cancelled)
			if len(observations) != 0 {
				t.Fatalf("cancelled lookup polluted health: %+v", observations)
			}
		})
	}
}
