// Package providerfixture contains isolated process-boundary fixtures for tests.
// It is not imported by Vuja's runtime.
package providerfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
)

func Environment(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	return dir
}

// Write installs an authored, non-interactive fixture containing only shell
// builtins. Callers must not pass user input or invoke external commands.
func Write(t *testing.T, dir, name, body string) {
	t.Helper()
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		t.Fatal("fixture executable must be a literal basename")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func Observe(t *testing.T, mode string) (context.Context, *[]health.Observation) {
	t.Helper()
	var values []health.Observation
	ctx := health.WithObserver(t.Context(), func(value health.Observation) { values = append(values, value) })
	if mode == "cancelled" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	} else if mode == "expired" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
		t.Cleanup(cancel)
	}
	return ctx, &values
}

func Check(t *testing.T, values []health.Observation, name string, want health.Outcome) {
	t.Helper()
	var matching []health.Observation
	for _, value := range values {
		if value.Source.Name() == name {
			matching = append(matching, value)
		}
	}
	if want == health.Unknown {
		if len(matching) != 0 {
			t.Fatalf("unattempted/cancelled provider changed health: %+v", matching)
		}
		return
	}
	// The fixture does not impose a machine-dependent startup latency budget.
	if len(matching) != 1 || matching[0].Outcome != want && !(want == health.Healthy && matching[0].Outcome == health.Slow) {
		t.Fatalf("actual %s outcome was discarded or conflated: %+v, want %v", name, values, want)
	}
}

func Outcome(mode string) health.Outcome {
	switch mode {
	case "failed":
		return health.Failed
	case "missing":
		return health.Skipped
	case "cancelled":
		return health.Unknown
	case "expired":
		return health.TimedOut
	default:
		return health.Healthy
	}
}
