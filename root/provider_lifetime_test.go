package root

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/integration/shell"
	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestProviderLifetimeMergeDoesNotCancelCurrentCompletion(t *testing.T) {
	// Spec's process/cache adapter is covered separately with its fake runner.
	// Here replace that provider boundary, retaining the real merge, contexts,
	// projection and recorder. No process or shell is started.
	diagnosticFixture(t)
	previousShell := shell.Current
	shell.Current = nil
	t.Cleanup(func() { shell.Current = previousShell })
	previousCWD := spec.GetCWD()
	spec.SetShellCWD(t.TempDir())
	t.Cleanup(func() { spec.SetShellCWD(previousCWD) })
	previousLookup := lookupPreparedSuggestions
	t.Cleanup(func() { lookupPreparedSuggestions = previousLookup })
	for _, mode := range []string{"current", "cancel", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			recorder := newLatencyRecorderWithSession("provider-lifetime", 42)
			t.Cleanup(recorder.Close)
			projection := newSessionHealthProjection(time.Now)
			var current atomic.Bool
			current.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = observedProviderContext(ctx, projection, recorder, current.Load)
			var providerContext context.Context
			lookupPreparedSuggestions = func(_ context.Context, async context.Context, _ string) []spec.Suggestion {
				providerContext = async
				return nil
			}
			_, _ = mergeResultsProfiledContext(ctx, "vuja-provider-lifetime-fixture ", "auto")
			if providerContext == nil {
				t.Fatal("merge did not reach completion boundary")
			}
			if err := providerContext.Err(); err != nil {
				t.Fatalf("ordinary merge completion cancelled still-current async work: %v", err)
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "superseded" {
				current.Store(false)
			}
			// Release the real observed outcome after the merge has returned.
			health.Report(providerContext, health.Completion, time.Millisecond, errors.New("private completion failure"))
			snapshot := recorder.snapshotCopy()
			if mode == "current" {
				if snapshot.Health == nil || snapshot.Health.Providers[health.Completion].Failures != 1 || projection.badge().Label != "Completion failed" {
					t.Fatalf("post-merge completion failure did not reach UI/telemetry: %+v %+v", snapshot.Health, projection.badge())
				}
			} else if snapshot.Health != nil && snapshot.Health.Providers[health.Completion].Failures != 0 {
				t.Fatalf("cancelled/superseded provider changed health: %+v", snapshot.Health)
			}
		})
	}
}
