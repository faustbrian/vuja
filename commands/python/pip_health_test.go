package python

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
)

func TestPipHealthReflectsTheFinalInventoryFallback(t *testing.T) {
	for _, test := range []struct {
		name, pip, pip3    string
		want               health.Outcome
		results            int
		cancelled, expired bool
	}{
		{"primary", "printf '%s\\n' 'fixture==1.0'", "", health.Healthy, 1, false, false},
		{"empty", "exit 0", "", health.Healthy, 0, false, false},
		{"missing primary fallback", "", "printf '%s\\n' 'fixture==1.0'", health.Healthy, 1, false, false},
		{"failed primary fallback", "exit 7", "printf '%s\\n' 'fixture==1.0'", health.Healthy, 1, false, false},
		{"missing both", "", "", health.Skipped, 0, false, false},
		{"failed both", "exit 7", "exit 9", health.Failed, 0, false, false},
		{"failed primary missing fallback", "exit 7", "", health.Failed, 0, false, false},
		{"cancelled", "exit 0", "exit 0", health.Unknown, 0, true, false},
		{"expired", "exit 0", "exit 0", health.TimedOut, 0, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only these authored, non-interactive fixtures can be discovered.
			// Their interpreter runs builtins, never shell configuration or Vuja.
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("HOME", dir)
			t.Setenv("ENV", "")
			t.Setenv("BASH_ENV", "")
			for name, body := range map[string]string{"pip": test.pip, "pip3": test.pip3} {
				if body != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			var observed []health.Observation
			ctx := health.WithObserver(t.Context(), func(value health.Observation) { observed = append(observed, value) })
			if test.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if test.expired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			results := pipPackageGenerator(ctx, nil, "", "")
			if len(results) != test.results || test.results == 1 && (results[0].Cmd != "fixture" || results[0].Desc != "v1.0") {
				t.Fatalf("fallback inventory changed: %+v", results)
			}
			if test.cancelled {
				if len(observed) != 0 {
					t.Fatalf("cancelled request changed health: %+v", observed)
				}
				return
			}
			// This checks successful outcomes, not the host's process startup
			// speed. A genuinely slow success is a valid classified attempt.
			if len(observed) != 1 || observed[0].Source.Name() != "pip-installed" || (observed[0].Outcome != test.want && !(test.want == health.Healthy && observed[0].Outcome == health.Slow)) {
				t.Fatalf("final installed inventory outcome was discarded or a fallback became false recovery: %+v, want %v", observed, test.want)
			}
		})
	}
}
