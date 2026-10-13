package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/spec"
	"github.com/faustbrian/vuja/tests/providerfixture"
)

func TestZoxideHealthPreservesLocalDirectoriesOnExternalFailure(t *testing.T) {
	original := spec.GetCWD()
	t.Cleanup(func() { spec.SetShellCWD(original) })
	for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			dir := providerfixture.Environment(t)
			spec.SetShellCWD(dir)
			if err := os.Mkdir(filepath.Join(dir, "fixture-local"), 0o700); err != nil {
				t.Fatal(err)
			}
			body := "printf '%s\\n' '/fixture/visited'"
			if mode == "empty" {
				body = "exit 0"
			} else if mode == "failed" {
				body = "exit 7"
			}
			if mode != "missing" {
				providerfixture.Write(t, dir, "zoxide", body)
			}
			ctx, values := providerfixture.Observe(t, mode)
			results := ZoxideGenerator()(ctx, []string{"z", ""}, "", "")
			foundLocal, foundVisited := false, false
			for _, result := range results {
				foundLocal = foundLocal || result.Cmd == "fixture-local/"
				foundVisited = foundVisited || result.Cmd == "/fixture/visited"
			}
			if mode != "cancelled" && mode != "expired" && !foundLocal || foundVisited != (mode == "inventory") {
				t.Fatalf("local/external result preservation changed: %+v", results)
			}
			providerfixture.Check(t, *values, "zoxide-completion", providerfixture.Outcome(mode))
		})
	}
}
