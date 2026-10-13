package root

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/internal/workspace"
)

func TestProjectHealthReportsActualFileReadAndParse(t *testing.T) {
	for _, test := range []struct {
		name     string
		data     string
		missing  bool
		want     health.Outcome
		commands int
	}{
		{"scripts", `{"scripts":{"build":"echo fixture"}}`, false, health.Healthy, 1},
		{"empty scripts", `{"scripts":{}}`, false, health.Healthy, 0},
		{"malformed", `{"scripts":`, false, health.Failed, 0},
		{"optional missing", "", true, health.Skipped, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.json")
			if !test.missing {
				if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			commands := 0
			readJSONScripts(ctx, path, func(string) { commands++ })
			if commands != test.commands {
				t.Fatalf("project commands changed: %d", commands)
			}
			if len(observations) != 1 || observations[0].Source != health.Project || observations[0].Outcome != test.want {
				t.Fatalf("file/parse outcome was discarded: %+v, want %v", observations, test.want)
			}
		})
	}
}

func TestProjectHealthRecipeAndMakeReadFailureAndCancellation(t *testing.T) {
	for _, reader := range []struct {
		name string
		read func(context.Context, string, func(string))
	}{{"recipe", readRecipeFile}, {"make", readMakefile}} {
		t.Run(reader.name, func(t *testing.T) {
			var observations []health.Observation
			ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
			reader.read(ctx, t.TempDir(), func(string) { t.Error("directory became a project command") })
			if len(observations) != 1 || observations[0].Outcome != health.Failed {
				t.Fatalf("read failure was discarded: %+v", observations)
			}
			observations = nil
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			reader.read(ctx, t.TempDir(), func(string) { t.Error("cancelled read generated a command") })
			if len(observations) != 0 {
				t.Fatalf("cancelled read changed health: %+v", observations)
			}
		})
	}
}

func TestProjectHealthCacheHitDoesNotInventRecovery(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		projectCommandCache.Lock()
		delete(projectCommandCache.entries, root)
		projectCommandCache.Unlock()
	})
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build":"echo fixture"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var observations []health.Observation
	ctx := health.WithObserver(context.Background(), func(value health.Observation) { observations = append(observations, value) })
	ws := workspace.WorkspaceInfo{Root: root}
	got := discoverProjectCommands(ctx, "npm", ws)
	if len(got) != 1 || got[0].Cmd != "npm run build" {
		t.Fatalf("unexpected project commands: %+v", got)
	}
	if len(observations) == 0 {
		t.Fatal("actual project read was not observed")
	}
	observations = nil
	got = discoverProjectCommands(ctx, "npm", ws)
	if len(got) != 1 {
		t.Fatal("cache hit lost command")
	}
	if len(observations) != 0 {
		t.Fatalf("cache hit invented a provider recovery: %+v", observations)
	}
}

func TestProjectHealthCancelledDiscoveryCannotCachePartialCommands(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		projectCommandCache.Lock()
		delete(projectCommandCache.entries, root)
		projectCommandCache.Unlock()
	})
	for name, data := range map[string]string{
		"package.json":  `{"scripts":{"build":"echo fixture"}}`,
		"composer.json": `{"scripts":{"check":"echo fixture"}}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel at an actual completed file boundary, not a timing guess.
	ctx = health.WithObserver(ctx, func(value health.Observation) {
		if value.Source == health.Project && value.Outcome == health.Healthy {
			cancel()
		}
	})
	ws := workspace.WorkspaceInfo{Root: root}
	_ = discoverProjectCommands(ctx, "", ws)
	got := discoverProjectCommands(context.Background(), "", ws)
	if len(got) != 2 || got[0].Cmd != "composer check" || got[1].Cmd != "npm run build" {
		t.Fatalf("a cancelled partial scan poisoned later project completion: %+v", got)
	}
}
