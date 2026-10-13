package spec

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
)

// The process boundary is replaced, not the parser/cache/observer path. No
// completion executable is started by these tests.
func TestCobraHealthReportsActualCompletionOutcome(t *testing.T) {
	original := runCobraCompletion
	t.Cleanup(func() { runCobraCompletion = original; ResetCobraCache() })
	for _, test := range []struct {
		name   string
		output string
		err    error
		want   health.Outcome
	}{
		{"results", "status\tshow status\n:0\n", nil, health.Healthy},
		{"valid empty", ":0\n", nil, health.Healthy},
		{"unsupported", "not a completion protocol\n", nil, health.Skipped},
		{"invalid directive", ":invalid\n", nil, health.Failed},
		{"provider directive error", ":1\n", nil, health.Failed},
		{"missing optional tool", "", exec.ErrNotFound, health.Skipped},
		{"process failed", "", errors.New("private command and credential sentinel"), health.Failed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ResetCobraCache()
			runCobraCompletion = func(context.Context, string, []string) ([]byte, error) {
				return []byte(test.output), test.err
			}
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) {
				// Observers may inspect cache state. Reporting under the cache
				// lock would deadlock the existing diagnostics/publication path.
				if !cobraCacheMu.TryLock() {
					t.Error("completion observation ran under cache lock")
				} else {
					cobraCacheMu.Unlock()
				}
				observations = append(observations, value)
			})
			refreshCobraCompletion(ctx, cobraCacheGeneration.Load(), "health-fixture", "fake-completion", "fake-completion", nil)
			if len(observations) != 1 || observations[0].Source != health.Completion || observations[0].Outcome != test.want {
				t.Fatalf("actual completion outcome was lost: got %+v, want %v", observations, test.want)
			}
		})
	}
}

func TestCobraHealthTimeoutCancellationAndReset(t *testing.T) {
	original := runCobraCompletion
	t.Cleanup(func() { runCobraCompletion = original; ResetCobraCache() })
	for _, mode := range []string{"timeout", "cancel", "reset"} {
		t.Run(mode, func(t *testing.T) {
			ResetCobraCache()
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			runCobraCompletion = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
				switch mode {
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				case "cancel":
					cancel()
					return nil, ctx.Err()
				default:
					ResetCobraCache()
					return []byte(":0\n"), nil
				}
			}
			refreshCobraCompletion(ctx, cobraCacheGeneration.Load(), "health-fixture", "fake-completion", "fake-completion", nil)
			if mode == "timeout" {
				if len(observations) != 1 || observations[0].Outcome != health.TimedOut || observations[0].Duration <= 0 {
					t.Fatalf("real provider deadline was discarded: %+v", observations)
				}
			} else if len(observations) != 0 {
				t.Fatalf("cancelled/superseded work changed health: %+v", observations)
			}
		})
	}
}

func TestCobraHealthColdCacheAndHitDoNotInventProviderOutcomes(t *testing.T) {
	ResetCobraCache()
	original := runCobraCompletion
	defer func() { runCobraCompletion = original; ResetCobraCache() }()
	started, release, observed := make(chan struct{}), make(chan struct{}), make(chan health.Observation, 2)
	runCobraCompletion = func(context.Context, string, []string) ([]byte, error) {
		close(started)
		<-release
		return []byte("status\tshow status\n:0\n"), nil
	}
	ctx := health.WithObserver(context.Background(), func(value health.Observation) { observed <- value })
	if got := QueryCobraCompleteContext(ctx, "vuja-fake-completion-health", nil, ""); len(got) != 0 {
		t.Fatalf("cold request unexpectedly returned suggestions: %+v", got)
	}
	<-started
	select {
	case value := <-observed:
		close(release)
		t.Fatalf("cold cache invented health before the provider finished: %+v", value)
	default:
	}
	close(release)
	select {
	case value := <-observed:
		if value.Outcome != health.Healthy {
			t.Fatalf("unexpected outcome: %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("completed provider did not report health")
	}
	if got := QueryCobraCompleteContext(ctx, "vuja-fake-completion-health", nil, ""); len(got) != 1 || got[0].Cmd != "vuja-fake-completion-health status" {
		t.Fatalf("provider reporting changed cached suggestions: %+v", got)
	}
	select {
	case value := <-observed:
		t.Fatalf("cache hit invented a provider recovery: %+v", value)
	default:
	}
}

func TestCobraHealthPreparedLookupOwnsSeparateCompletionLifetime(t *testing.T) {
	ResetCobraCache()
	previous := runCobraCompletion
	defer func() { runCobraCompletion = previous; ResetCobraCache() }()
	const name = "vuja-fake-prepared-completion-health"
	Register(&Spec{Name: name, DynamicCompletion: true})
	t.Cleanup(func() { delete(Registry, name) })
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	observations := make(chan health.Observation, 2)
	runCobraCompletion = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		close(started)
		<-release
		defer close(finished)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("private completion failure")
	}
	request := health.WithObserver(context.Background(), func(value health.Observation) { observations <- value })
	synchronous, cancel := context.WithCancel(request)
	_ = LookupContextPreparedWithCompletionContext(synchronous, request, name+" ")
	<-started
	cancel() // The ordinary synchronous merge is finished; request is current.
	close(release)
	<-finished
	select {
	case value := <-observations:
		if value.Source != health.Completion || value.Outcome != health.Failed {
			t.Fatalf("unexpected completion outcome: %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous merge cancellation discarded current async completion")
	}
}

func TestCobraHealthPreparedLookupPreservesRequestCancellationAndProviderDeadline(t *testing.T) {
	previous := runCobraCompletion
	defer func() { runCobraCompletion = previous; ResetCobraCache() }()
	const name = "vuja-fake-prepared-completion-deadline"
	Register(&Spec{Name: name, DynamicCompletion: true})
	t.Cleanup(func() { delete(Registry, name) })
	for _, mode := range []string{"request cancellation", "provider deadline"} {
		t.Run(mode, func(t *testing.T) {
			ResetCobraCache()
			started := make(chan struct{})
			finished := make(chan error, 1)
			observations := make(chan health.Observation, 1)
			runCobraCompletion = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
				close(started)
				<-ctx.Done()
				finished <- ctx.Err()
				return nil, ctx.Err()
			}
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			request = health.WithObserver(request, func(value health.Observation) { observations <- value })
			synchronous, cancelSync := context.WithCancel(request)
			_ = LookupContextPreparedWithCompletionContext(synchronous, request, name+" ")
			<-started
			cancelSync()
			want := context.DeadlineExceeded
			if mode == "request cancellation" {
				cancelRequest()
				want = context.Canceled
			}
			select {
			case err := <-finished:
				if !errors.Is(err, want) {
					t.Fatalf("wrong lifetime stopped actual provider: %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("provider did not stop at its owned cancellation/deadline")
			}
			if mode == "provider deadline" {
				select {
				case value := <-observations:
					if value.Outcome != health.TimedOut {
						t.Fatalf("deadline was not observed: %+v", value)
					}
				case <-time.After(time.Second):
					t.Fatal("provider deadline health was discarded")
				}
			}
		})
	}
}

func TestCobraHealthOutcomePrecedesNotificationDrivenRefresh(t *testing.T) {
	previous := runCobraCompletion
	defer func() { runCobraCompletion = previous; ResetCobraCache() }()
	for _, mode := range []string{"failed", "healthy"} {
		t.Run(mode, func(t *testing.T) {
			ResetCobraCache()
			for len(completionUpdates) > 0 {
				<-completionUpdates
			}
			runCobraCompletion = func(context.Context, string, []string) ([]byte, error) {
				if mode == "failed" {
					return nil, errors.New("private completion failure")
				}
				return []byte("status\tfixture\n:0\n"), nil
			}
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) {
				// The real update consumer can invalidate/cancel this request.
				// Therefore a queued notification must never precede accepted
				// health, including recovery of a previously failing provider.
				select {
				case <-CompletionUpdates():
					t.Error("completion update can cancel the request before its outcome is published")
				default:
				}
				if !cobraCacheMu.TryLock() {
					t.Error("health callback ran under cache lock")
				} else {
					cobraCacheMu.Unlock()
				}
				observations = append(observations, value)
			})
			refreshCobraCompletion(ctx, cobraCacheGeneration.Load(), "notification-fixture", "fake-completion", "fake-completion", nil)
			want := health.Failed
			if mode == "healthy" {
				want = health.Healthy
			}
			if len(observations) != 1 || observations[0].Outcome != want {
				t.Fatalf("completed outcome disappeared: %+v", observations)
			}
			select {
			case <-CompletionUpdates():
			default:
				t.Fatal("accepted cache update did not notify after health publication")
			}
			cobraCacheMu.Lock()
			entry := cobraCache["notification-fixture"]
			cobraCacheMu.Unlock()
			if mode == "healthy" && (len(entry.suggestions) != 1 || entry.suggestions[0].Cmd != "fake-completion status") {
				t.Fatalf("publication ordering changed cache results: %+v", entry.suggestions)
			}
		})
	}
}
