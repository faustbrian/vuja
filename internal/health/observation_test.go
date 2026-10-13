package health

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestReportClassifiesActualProviderOutcomesWithoutErrorText(t *testing.T) {
	var observations []Observation
	ctx := WithObserver(context.Background(), func(observation Observation) { observations = append(observations, observation) })
	Report(ctx, Files, time.Millisecond, nil)
	Report(ctx, Completion, time.Millisecond, errors.New("private-command credential-sentinel /private/path"))
	Report(ctx, History, time.Millisecond, context.DeadlineExceeded)
	Report(ctx, Completion, 300*time.Millisecond, nil)
	Report(ctx, AI, 3*time.Second, nil)
	if len(observations) != 5 {
		t.Fatalf("actual provider attempts were not observed: %+v", observations)
	}
	for i, expected := range []Outcome{Healthy, Failed, TimedOut, Slow, Slow} {
		if observations[i].Outcome != expected || observations[i].At.IsZero() {
			t.Fatalf("attempt %d outcome=%+v, expected %v", i, observations[i], expected)
		}
	}
}

func TestReportKeepsOptionalMissesAndCancellationQuiet(t *testing.T) {
	var observations []Observation
	ctx := WithObserver(context.Background(), func(observation Observation) { observations = append(observations, observation) })
	Report(ctx, Files, 0, os.ErrNotExist)
	Report(ctx, Completion, 0, exec.ErrNotFound)
	Report(ctx, Project, 0, os.ErrNotExist)
	Report(ctx, Completion, 0, errors.ErrUnsupported)
	Report(ctx, History, 0, os.ErrNotExist) // Required history is not an optional file.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	Report(cancelled, Completion, time.Second, errors.New("private-error"))
	Report(ctx, Completion, time.Millisecond, context.Canceled)
	Report(ctx, Source(255), 0, errors.New("private-error"))
	if len(observations) != 5 || observations[0].Outcome != Skipped || observations[1].Outcome != Skipped || observations[2].Outcome != Skipped || observations[3].Outcome != Skipped || observations[4].Outcome != Failed {
		t.Fatalf("optional misses/cancellation became failures, or required failure was hidden: %+v", observations)
	}
}

func TestReportUsesRealDeadlineAndSupportsUnobservedCallers(t *testing.T) {
	var observation Observation
	ctx := WithObserver(nil, func(value Observation) { observation = value })
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	Report(expired, Completion, time.Millisecond, nil)
	if observation.Outcome != TimedOut {
		t.Fatal("an expired provider deadline was mistaken for success")
	}
	Report(context.Background(), Completion, time.Second, errors.New("private-error"))
	Report(nil, Completion, 0, nil)
}
