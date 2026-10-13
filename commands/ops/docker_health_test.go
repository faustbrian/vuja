package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
)

func TestDockerHealthKeepsInventoryOutcomesIndependent(t *testing.T) {
	originalCWD := spec.GetCWD()
	t.Cleanup(func() { spec.SetShellCWD(originalCWD) })
	for _, provider := range []struct {
		name, output, result string
		generate             spec.GeneratorFunc
	}{
		{"docker-containers-all", "fixture-container", "fixture-container", dockerContainerGenerator},
		{"docker-containers-running", "fixture-running", "fixture-running", dockerRunningContainerGenerator},
		{"docker-images", "example:v1\nexample:v1\n<none>:<none>", "example:v1", dockerImageGenerator},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					spec.SetShellCWD(dir)
					t.Setenv("PATH", dir)
					t.Setenv("HOME", dir)
					t.Setenv("ENV", "")
					t.Setenv("BASH_ENV", "")
					// A fixed non-interactive script, with no external commands,
					// replaces Docker. No daemon or user shell is contacted.
					body := "printf '%s\\n' '" + provider.output + "'\n"
					want, count := health.Healthy, 1
					switch mode {
					case "empty":
						body, count = "exit 0\n", 0
					case "failed":
						body, want, count = "exit 7\n", health.Failed, 0
					case "missing":
						want, count = health.Skipped, 0
					case "cancelled":
						want, count = health.Unknown, 0
					case "expired":
						want, count = health.TimedOut, 0
					}
					if mode != "missing" {
						if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
							t.Fatal(err)
						}
					}
					var observed []health.Observation
					ctx := health.WithObserver(t.Context(), func(value health.Observation) { observed = append(observed, value) })
					if mode == "cancelled" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						cancel()
					} else if mode == "expired" {
						var cancel context.CancelFunc
						ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
						defer cancel()
					}
					results := provider.generate(ctx, nil, "", "")
					if len(results) != count || count == 1 && results[0].Cmd != provider.result {
						t.Fatalf("inventory result policy changed: %+v", results)
					}
					if mode == "cancelled" {
						if len(observed) != 0 {
							t.Fatalf("cancelled inventory changed health: %+v", observed)
						}
						return
					}
					// Successful queries may legitimately be slow under host
					// load; this fixture is not a process-latency benchmark.
					if len(observed) != 1 || observed[0].Source.Name() != provider.name || (observed[0].Outcome != want && !(want == health.Healthy && observed[0].Outcome == health.Slow)) {
						t.Fatalf("actual Docker inventory outcome was discarded or conflated: %+v, want %s/%v", observed, provider.name, want)
					}
				})
			}
		})
	}
}
