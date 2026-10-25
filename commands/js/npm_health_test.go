package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestNpmHealthReportsManifestOutcomeWithoutLosingFallback(t *testing.T) {
	originalCWD := spec.GetCWD()
	t.Cleanup(func() { spec.SetShellCWD(originalCWD) })
	for _, test := range []struct {
		name, data                    string
		missing, directory, cancelled bool
		want                          health.Outcome
		results                       int
	}{
		{"scripts", `{"scripts":{"build":"echo fixture"}}`, false, false, false, health.Healthy, 1},
		{"empty", `{"scripts":{}}`, false, false, false, health.Healthy, 0},
		{"malformed", `{"scripts":`, false, false, false, health.Failed, 0},
		{"missing fallback", "", true, false, false, health.Skipped, 5},
		{"read error fallback", "", false, true, false, health.Failed, 5},
		{"cancelled", `{"scripts":{"build":"echo fixture"}}`, false, false, true, health.Unknown, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			spec.SetShellCWD(root)
			path := filepath.Join(root, "package.json")
			if test.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if !test.missing {
				if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			if test.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got := NpmScriptGenerator(ctx, nil, "", "")
			if len(got) != test.results {
				t.Fatalf("manifest/fallback results changed: %+v", got)
			}
			if test.cancelled {
				if len(observations) != 0 {
					t.Fatalf("cancelled provider changed health: %+v", observations)
				}
				return
			}
			if len(observations) != 1 || observations[0].Source.Name() != "npm-scripts" || observations[0].Outcome != test.want {
				t.Fatalf("actual manifest outcome was lost or conflated with project discovery: %+v, want %v", observations, test.want)
			}
		})
	}
}
