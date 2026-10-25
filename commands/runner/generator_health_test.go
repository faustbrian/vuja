package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestRecipeHealthReportsActualInventoryAndFallback(t *testing.T) {
	originalCWD := spec.GetCWD()
	t.Cleanup(func() { spec.SetShellCWD(originalCWD) })
	for _, command := range []string{"make", "just"} {
		t.Run(command, func(t *testing.T) {
			for _, mode := range []string{"valid", "empty", "missing", "read error", "scanner error", "uppercase fallback", "failed lower missing upper"} {
				if command == "make" && (mode == "uppercase fallback" || mode == "failed lower missing upper") {
					continue
				}
				t.Run(mode, func(t *testing.T) {
					root := t.TempDir()
					spec.SetShellCWD(root)
					name := "justfile"
					if command == "make" {
						name = "Makefile"
					} else if mode == "uppercase fallback" {
						name = "Justfile"
					}
					path := filepath.Join(root, name)
					want := health.Healthy
					data := "# fixture recipe\nbuild:\n\techo fixture\n"
					switch mode {
					case "empty":
						data = "# no recipes\n"
					case "missing":
						want = health.Skipped
					case "read error":
						want = health.Failed
						// Opening a directory succeeds, but reading recipes fails;
						// no user file or command execution is involved.
						if err := os.Mkdir(path, 0o700); err != nil {
							t.Fatal(err)
						}
					case "scanner error":
						want = health.Failed
						data += strings.Repeat("x", 70<<10)
					case "failed lower missing upper":
						want = health.Failed
						if err := os.Symlink("justfile", path); err != nil {
							t.Fatal(err)
						}
					}
					if mode != "missing" && mode != "read error" && mode != "failed lower missing upper" {
						if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					observations := make(chan health.Observation, 4)
					ctx := health.WithObserver(t.Context(), func(value health.Observation) { observations <- value })
					generator := spec.Registry[command].Generator
					for len(spec.CompletionUpdates()) > 0 {
						<-spec.CompletionUpdates()
					}
					results := generator(ctx, []string{command, ""}, "", "")
					select {
					case value := <-observations:
						source := "make-targets"
						if command == "just" {
							source = "just-recipes"
						}
						if value.Source.Name() != source || value.Outcome != want {
							t.Fatalf("inventory outcome conflated or misclassified: %+v, want %s/%v", value, source, want)
						}
					case <-time.After(time.Second):
						t.Fatal("actual recipe read/scan outcome was discarded")
					}
					if command == "make" {
						// The real cache publishes health before notifying consumers.
						select {
						case <-spec.CompletionUpdates():
						case <-time.After(time.Second):
							t.Fatal("recipe results were not published")
						}
						results = generator(ctx, []string{command, ""}, "", "")
					}
					wantResults := 0
					if mode == "valid" || mode == "uppercase fallback" || command == "make" && mode == "scanner error" {
						wantResults = 1
					}
					if len(results) != wantResults || wantResults == 1 && results[0].Cmd != "build" {
						t.Fatalf("health changed existing recipe/fallback results: %+v", results)
					}
				})
			}
		})
	}
}
