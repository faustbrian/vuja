package workspace

import (
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/tests/providerfixture"
)

func TestWorkspaceHealthFailedGitRefreshPreservesLastKnownMetadata(t *testing.T) {
	dir := providerfixture.Environment(t)
	providerfixture.Write(t, dir, "git", "exit 7")
	gitStatusCache.Lock()
	if gitStatusCache.entries == nil {
		gitStatusCache.entries = make(map[string]gitStatus)
	}
	gitStatusCache.entries[dir] = gitStatus{dirty: true, staged: true, ahead: 2}
	gitStatusCache.Unlock()
	t.Cleanup(func() {
		gitStatusCache.Lock()
		delete(gitStatusCache.entries, dir)
		gitStatusCache.Unlock()
	})
	_ = WithGitStatusCached(WorkspaceInfo{Root: dir, HasGit: true})
	deadline := time.Now().Add(3 * time.Second)
	for {
		gitStatusCache.Lock()
		state := gitStatusCache.entries[dir]
		gitStatusCache.Unlock()
		if !state.refreshing {
			if !state.dirty || !state.staged || state.ahead != 2 {
				t.Fatalf("failed refresh replaced usable previous metadata with false-clean state: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated fixture refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkspaceHealthActualGitStatusOutcomesAndCacheReuse(t *testing.T) {
	for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			dir := providerfixture.Environment(t)
			body := "printf '%s\\n' '# branch.ab +2 -1' '1 M. fixture'"
			switch mode {
			case "empty":
				body = "exit 0"
			case "failed":
				body = "exit 7"
			}
			if mode != "missing" {
				providerfixture.Write(t, dir, "git", body)
			}
			gitStatusCache.Lock()
			if gitStatusCache.entries == nil {
				gitStatusCache.entries = make(map[string]gitStatus)
			}
			gitStatusCache.Unlock()
			t.Cleanup(func() {
				gitStatusCache.Lock()
				delete(gitStatusCache.entries, dir)
				gitStatusCache.Unlock()
			})
			ctx, observations := providerfixture.Observe(t, mode)
			refreshGitStatusContext(ctx, dir)
			providerfixture.Check(t, *observations, "workspace-git-status", providerfixture.Outcome(mode))
			gitStatusCache.Lock()
			state := gitStatusCache.entries[dir]
			gitStatusCache.Unlock()
			if mode == "inventory" && (!state.staged || state.ahead != 2 || state.behind != 1) {
				t.Fatalf("valid Git status facts changed: %+v", state)
			}
			if mode == "inventory" || mode == "empty" || mode == "failed" || mode == "missing" {
				gitStatusCache.Lock()
				warm := gitStatusCache.entries[dir]
				warm.updatedAt = time.Now().Add(time.Minute)
				gitStatusCache.entries[dir] = warm
				gitStatusCache.Unlock()
				*observations = nil
				_ = WithGitStatusCachedContext(ctx, WorkspaceInfo{Root: dir, HasGit: true})
				providerfixture.Check(t, *observations, "workspace-git-status", health.Unknown)
			}
		})
	}
}
