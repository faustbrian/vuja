package root

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/commands/js"
	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
)

func TestSessionHealthManifestFailureSurvivesUnrelatedRecipeSuccess(t *testing.T) {
	diagnosticFixture(t)
	cwd := t.TempDir()
	originalCWD := spec.GetCWD()
	spec.SetShellCWD(cwd)
	t.Cleanup(func() { spec.SetShellCWD(originalCWD) })
	manifest := filepath.Join(cwd, "package.json")
	if err := os.WriteFile(manifest, []byte(`{"scripts":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "justfile"), []byte("build:\n\techo fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := newSessionHealthProjection(time.Now)
	recorder := newLatencyRecorderWithSession("recipe-health", 42)
	t.Cleanup(recorder.Close)
	request := func() context.Context {
		return observedProviderContext(t.Context(), projection, recorder, func() bool { return true })
	}
	ctx := request()
	_ = js.NpmScriptGenerator(ctx, nil, "", "")
	_ = spec.Registry["just"].Generator(ctx, nil, "", "")
	if badge := projection.badge(); badge.Label != "Npm scripts failed" || badge.Recovered {
		t.Fatalf("a successful unrelated recipe falsely restored the manifest: %+v", badge)
	}
	if snapshot := recorder.snapshotCopy().Health; snapshot == nil || snapshot.Providers[health.NpmScripts].Outcome != health.Failed || snapshot.Providers[health.JustRecipes].Outcome != health.Healthy {
		t.Fatalf("distinct inventory outcomes were lost before diagnostics: %+v", snapshot)
	}
	if err := persistLatencySnapshot(recorder.snapshotCopy()); err != nil {
		t.Fatal(err)
	}
	providers := diagnosticSection(t, diagnosticSection(t, runDiagnosticFixture(t), "health"), "providers")
	if diagnosticSection(t, providers, "npm-scripts")["last_outcome"] != "failed" || diagnosticSection(t, providers, "just-recipes")["last_outcome"] != "healthy" {
		t.Fatalf("sanitized diagnostics conflated the inventory sources: %+v", providers)
	}
	if err := os.WriteFile(manifest, []byte(`{"scripts":{"build":"echo fixture"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = js.NpmScriptGenerator(request(), nil, "", "")
	if badge := projection.badge(); badge.Label != "Npm scripts restored" || !badge.Recovered {
		t.Fatalf("a fresh successful manifest read did not restore its own health: %+v", badge)
	}
}
