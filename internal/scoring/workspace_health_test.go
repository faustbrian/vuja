package scoring

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/tests/providerfixture"
)

func TestWorkspaceHealthSignalConsumerReceivesActualMetadataOutcome(t *testing.T) {
	for _, mode := range []string{"project", "empty", "missing", "not-directory", "unreadable-head", "cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			cwd := t.TempDir()
			want := health.Healthy
			switch mode {
			case "project", "cancelled", "expired":
				if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				cwd, want = filepath.Join(cwd, "missing"), health.Failed
			case "not-directory":
				cwd = filepath.Join(cwd, "file")
				if err := os.WriteFile(cwd, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = health.Failed
			case "unreadable-head":
				if err := os.MkdirAll(filepath.Join(cwd, ".git", "HEAD"), 0o700); err != nil {
					t.Fatal(err)
				}
				want = health.Failed
			}
			ctx, values := providerfixture.Observe(t, mode)
			switch mode {
			case "cancelled":
				want = health.Unknown
			case "expired":
				want = health.TimedOut
			}
			result := CollectSignalsWithRanking(ctx, cwd, "git", "git", nil, "", "", "balanced")
			if mode == "project" && (!result.Workspace.HasGoProject || result.Workspace.Root != cwd) {
				t.Fatalf("valid project metadata changed: %+v", result.Workspace)
			}
			if mode == "unreadable-head" && (!result.Workspace.HasGit || result.Workspace.Root != cwd) {
				t.Fatalf("useful partial metadata was lost: %+v", result.Workspace)
			}
			providerfixture.Check(t, *values, "workspace", want)
		})
	}
}

func TestWorkspaceHealthCachedMetadataCannotInventRecovery(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "go.mod")
	if err := os.WriteFile(path, []byte("module fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, values := providerfixture.Observe(t, "project")
	read := func(ctx context.Context) {
		result := CollectSignalsWithRanking(ctx, cwd, "git", "git", nil, "", "", "balanced")
		if !result.Workspace.HasGoProject {
			t.Fatal("cached project metadata was lost")
		}
	}
	read(ctx)
	providerfixture.Check(t, *values, "workspace", health.Healthy)
	*values = nil
	read(ctx)
	providerfixture.Check(t, *values, "workspace", health.Unknown)
	// Invalidate with independent explicit metadata, not a sleep threshold.
	info, err := os.Stat(cwd)
	if err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(time.Second)
	if err := os.Chtimes(cwd, changed, changed); err != nil {
		t.Fatal(err)
	}
	read(ctx)
	providerfixture.Check(t, *values, "workspace", health.Healthy)
}

func TestWorkspaceHealthHeadTypeChangeWithPreservedMtimeInvalidatesCache(t *testing.T) {
	cwd := t.TempDir()
	head := filepath.Join(cwd, ".git", "HEAD")
	if err := os.MkdirAll(filepath.Dir(head), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(head, []byte("ref: refs/heads/fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, values := providerfixture.Observe(t, "project")
	result := CollectSignalsWithRanking(ctx, cwd, "git", "git", nil, "", "", "balanced")
	if result.Workspace.GitBranch != "fixture" {
		t.Fatal("valid fixture branch was not acquired")
	}
	providerfixture.Check(t, *values, "workspace", health.Healthy)
	info, err := os.Stat(head)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(head, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(head, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	*values = nil
	_ = CollectSignalsWithRanking(ctx, cwd, "git", "git", nil, "", "", "balanced")
	providerfixture.Check(t, *values, "workspace", health.Failed)
}
