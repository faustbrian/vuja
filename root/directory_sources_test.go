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
	if !entries[0].LastUsed.IsZero() || !entries[1].LastUsed.IsZero() {
		t.Fatalf("expected Zoxide scores not to invent recency, got %+v", entries)
	}
}

func TestGitWorktreeImportsDoNotInventRecency(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}

	entries := gitWorktreeImports(root, nil)
	if len(entries) != 1 || entries[0].Path != root {
		t.Fatalf("expected repository root discovery, got %+v", entries)
	}
	if !entries[0].LastUsed.IsZero() {
		t.Fatalf("expected discovery not to imply recent use, got %v", entries[0].LastUsed)
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

func TestHistoryNavigationDirectoryImportsUseNavigationEventsInsteadOfDirectoryActivity(t *testing.T) {
	now := time.Now()
	entries := historyNavigationDirectoryImports([]integration.HistoryEntry{
		{Command: "git status", Cwd: "/repo/service", StartedAt: now},
		{Command: "cd service", Cwd: "/repo", StartedAt: now.Add(-time.Hour)},
		{Command: "cd service/", Cwd: "/repo", StartedAt: now},
		{Command: "cd legacy", Cwd: "/repo", StartedAt: now.Add(-90 * 24 * time.Hour)},
		{Command: "cd service && pwd", Cwd: "/repo", StartedAt: now},
	}, now)
	if len(entries) != 2 {
		t.Fatalf("expected two navigated directories, got %+v", entries)
	}
	if entries[0].Path != "/repo/service" || entries[0].Count != 2 || entries[0].RecentCount != 2 || !entries[0].LastUsed.Equal(now) {
		t.Fatalf("expected recent navigation evidence, got %+v", entries[0])
	}
	if entries[1].Path != "/repo/legacy" || entries[1].Count != 1 || entries[1].RecentCount != 0 {
		t.Fatalf("expected lifetime-only legacy navigation evidence, got %+v", entries[1])
	}
}
