package integration

import (
	"context"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
)

func TestHistoryHealthReportsSearchWithoutPersistenceClaims(t *testing.T) {
	original := RichHistorySnapshot()
	t.Cleanup(func() { PublishCanonicalHistory(original) })
	PublishCanonicalHistory([]HistoryEntry{{ID: "health-fixture", Command: "ssh fixture-host", StartedAt: time.Now(), Source: "vuja"}})
	for _, mode := range []string{"match", "empty", "deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			query := "ssh"
			switch mode {
			case "empty":
				query = "no-fixture-match"
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := SearchHistoryContext(ctx, query, nil)
			if mode == "cancel" {
				if err == nil || len(observations) != 0 {
					t.Fatalf("cancelled search changed health: %v %+v", err, observations)
				}
				return
			}
			want := health.Healthy
			if mode == "deadline" {
				want = health.TimedOut
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "match" && (len(got) != 1 || got[0].Cmd != "ssh fixture-host") {
				t.Fatalf("history result changed: %+v", got)
			}
			if mode == "empty" && len(got) != 0 {
				t.Fatalf("unexpected empty-search results: %+v", got)
			}
			if len(observations) != 1 || observations[0].Source != health.History || observations[0].Outcome != want {
				t.Fatalf("search outcome was discarded or attributed to persistence: %+v, want %v", observations, want)
			}
		})
	}
}
