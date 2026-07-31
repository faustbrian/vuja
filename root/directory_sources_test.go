package root

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
)

func TestParseZoxideDirectories(t *testing.T) {
	entries := parseZoxideDirectories("12.5 /repo/service\n2 /repo/with spaces\n")
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %+v", entries)
	}
	if entries[0].Path != "/repo/service" || entries[0].Count != 12 {
		t.Fatalf("unexpected first entry %+v", entries[0])
	}
	if entries[1].Path != "/repo/with spaces" {
		t.Fatalf("expected path with spaces, got %+v", entries[1])
	}
}

func TestDiscoverGitWorktreesFromMetadata(t *testing.T) {
	root := t.TempDir()
	linked := filepath.Join(t.TempDir(), "feature")
	metadata := filepath.Join(root, ".git", "worktrees", "feature")
	if err := os.MkdirAll(metadata, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata, "gitdir"), []byte(filepath.Join(linked, ".git")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	paths := discoverGitWorktrees(root)
	if len(paths) != 1 || paths[0] != linked {
		t.Fatalf("expected linked worktree, got %v", paths)
	}
}

func TestHistoryDirectoryImportsAggregateWorkingDirectories(t *testing.T) {
	now := time.Now()
	entries := historyDirectoryImports([]integration.HistoryStat{
		{Cwd: "/repo/service", Count: 2, LastUsed: now.Add(-time.Hour)},
		{Cwd: "/repo/service", Count: 3, LastUsed: now},
		{Cwd: "", Count: 10, LastUsed: now},
	})
	if len(entries) != 1 {
		t.Fatalf("expected one working directory, got %+v", entries)
	}
	if entries[0].Path != "/repo/service" || entries[0].Count != 5 || !entries[0].LastUsed.Equal(now) {
		t.Fatalf("expected aggregated history directory, got %+v", entries[0])
	}
}
