package spec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/health"
)

func TestFileGeneratorHealthDistinguishesEmptyMissingFailedAndCancelled(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		path    string
		cancel  bool
		outcome health.Outcome
	}{
		{"empty", root, false, health.Healthy},
		{"missing", filepath.Join(root, "absent"), false, health.Skipped},
		{"failed", file, false, health.Failed},
		{"cancelled", root, true, health.Unknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			FileGenerator("/")(ctx, nil, "", test.path+string(filepath.Separator))
			if test.cancel {
				if len(observations) != 0 {
					t.Fatalf("cancelled scan changed health: %+v", observations)
				}
				return
			}
			if len(observations) != 1 || observations[0].Source != health.Files || observations[0].Outcome != test.outcome {
				t.Fatalf("filesystem outcome was discarded or misclassified: %+v", observations)
			}
		})
	}
}
