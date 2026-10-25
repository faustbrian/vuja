package root

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

func TestSessionHealthWorkspaceRequiresReadableHeadBeforeRecovery(t *testing.T) {
	for _, layout := range []string{"repository", "gitdir-pointer"} {
		t.Run(layout, func(t *testing.T) {
			cwd := t.TempDir()
			gitDir := filepath.Join(cwd, ".git")
			if layout == "gitdir-pointer" {
				gitDir = filepath.Join(cwd, "metadata")
				if err := os.WriteFile(filepath.Join(cwd, ".git"), []byte("gitdir: metadata\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			head := filepath.Join(gitDir, "HEAD")
			if err := os.MkdirAll(head, 0o700); err != nil {
				t.Fatal(err)
			}
			h := newSessionHealthProjection(time.Now)
			ctx := health.WithObserver(t.Context(), func(observation health.Observation) {
				h.observeProvider(context.Background(), observation)
			})
			read := func() {
				result := scoring.CollectSignalsWithRanking(ctx, cwd, "git", "git", nil, "", "", "balanced")
				if !result.Workspace.HasGit || result.Workspace.Root != cwd {
					t.Fatal("required metadata failure discarded useful repository facts")
				}
			}
			read()
			if badge := h.badge(); badge.Recovered || badge.Label != "Workspace failed" {
				t.Fatalf("unreadable HEAD must report failure, got %+v", badge)
			}
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			if layout == "gitdir-pointer" {
				if err := os.Remove(gitDir); err != nil {
					t.Fatal(err)
				}
			}
			read()
			if badge := h.badge(); badge.Recovered || badge.Label != "Workspace failed" {
				t.Fatalf("absent required HEAD invented recovery, got %+v", badge)
			}
			if err := os.MkdirAll(gitDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(head, []byte("ref: refs/heads/fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			read()
			if badge := h.badge(); !badge.Recovered || badge.Label != "Workspace restored" {
				t.Fatalf("readable HEAD must restore workspace health, got %+v", badge)
			}
		})
	}
}
