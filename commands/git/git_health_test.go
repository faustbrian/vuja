package git

import (
	"testing"

	"github.com/faustbrian/vuja/spec"
	"github.com/faustbrian/vuja/tests/providerfixture"
)

func TestGitHealthKeepsActualInventoriesSeparate(t *testing.T) {
	original := spec.GetCWD()
	t.Cleanup(func() { spec.SetShellCWD(original) })
	for _, provider := range []struct {
		name, output, result string
		generate             spec.GeneratorFunc
	}{
		{"git-commits", "abc123 [yesterday] fixture", "abc123", GitCommitGenerator},
		{"git-branches", "fixture", "fixture", GitBranchGenerator},
		{"git-remotes", "origin", "origin", GitRemoteGenerator},
		{"git-tags", "v1.0", "v1.0", GitTagGenerator},
		{"git-stashes", "stash@{0}: fixture", "stash@{0}", GitStashGenerator},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
				t.Run(mode, func(t *testing.T) {
					dir := providerfixture.Environment(t)
					spec.SetShellCWD(dir)
					body := "printf '%s\\n' '" + provider.output + "'"
					if mode == "empty" {
						body = "exit 0"
					} else if mode == "failed" {
						body = "exit 7"
					}
					if mode != "missing" {
						providerfixture.Write(t, dir, "git", body)
					}
					ctx, values := providerfixture.Observe(t, mode)
					results := provider.generate(ctx, []string{"git", "show", ""}, "", "")
					count := 0
					if mode == "inventory" {
						count = 1
					}
					if len(results) != count || count == 1 && results[0].Cmd != provider.result {
						t.Fatalf("Git inventory result changed: %+v", results)
					}
					providerfixture.Check(t, *values, provider.name, providerfixture.Outcome(mode))
				})
			}
		})
	}
}

func TestGitHealthDoesNotCallIntentionalCreateModeARecovery(t *testing.T) {
	dir := providerfixture.Environment(t)
	providerfixture.Write(t, dir, "git", "exit 7")
	ctx, values := providerfixture.Observe(t, "inventory")
	if got := GitBranchGenerator(ctx, []string{"git", "switch", "-c", "fixture"}, "", ""); len(got) != 0 || len(*values) != 0 {
		t.Fatalf("branch creation incorrectly queried/recovered inventory: %+v %+v", got, *values)
	}
}
