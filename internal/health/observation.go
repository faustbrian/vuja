// Package health carries finite, privacy-safe provider observations through
// existing contexts. It does not own persistence, ranking or provider retries.
package health

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"time"
)

type Source uint8

const (
	HistoryPersistence Source = iota
	Visual
	Files
	Completion
	History
	Workspace
	Project
	Directory
	Arguments
	Recovery
	Signals
	AI
	NpmScripts
	MakeTargets
	JustRecipes
	SourceCount
)

type Outcome uint8

const (
	Unknown Outcome = iota
	Healthy
	Slow
	Failed
	TimedOut
	Skipped
)

type Observation struct {
	Source   Source
	Outcome  Outcome
	Duration time.Duration
	At       time.Time
}

func WithObserver(ctx context.Context, observe func(Observation)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, observerKey{}, observe)
}

type observerKey struct{}

func Report(ctx context.Context, source Source, duration time.Duration, err error) {
	if ctx == nil || source >= SourceCount {
		return
	}
	observe, _ := ctx.Value(observerKey{}).(func(Observation))
	if observe == nil || errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	outcome := Healthy
	var timeout net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout():
		outcome = TimedOut
	case errors.Is(err, exec.ErrNotFound) && source == Completion:
		outcome = Skipped
	case errors.Is(err, errors.ErrUnsupported) && source == Completion:
		outcome = Skipped
	case errors.Is(err, os.ErrNotExist) && (source == Files || source == Project || source == Workspace || source == NpmScripts || source == MakeTargets || source == JustRecipes):
		outcome = Skipped
	case err != nil:
		outcome = Failed
	case duration >= SlowThreshold(source):
		outcome = Slow
	}
	observe(Observation{Source: source, Outcome: outcome, Duration: max(duration, 0), At: time.Now()})
}

// A local source taking a quarter second can delay enrichment noticeably;
// optional AI has a separate allowance for an actual remote model response.
func SlowThreshold(source Source) time.Duration {
	if source == AI {
		return 2 * time.Second
	}
	return 250 * time.Millisecond
}

func (s Source) Name() string {
	names := [...]string{"history-persistence", "visual", "files", "completion", "history", "workspace", "project", "directory", "arguments", "recovery", "signals", "ai", "npm-scripts", "make-targets", "just-recipes"}
	if s >= SourceCount {
		return "unknown"
	}
	return names[s]
}
