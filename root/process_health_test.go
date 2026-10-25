package root

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
	"github.com/faustbrian/vuja/tests/providerfixture"
)

func TestSessionHealthDockerInspectPreservesIndependentFailuresAndCacheSilence(t *testing.T) {
	diagnosticFixture(t)
	dir := providerfixture.Environment(t)
	original := spec.GetCWD()
	spec.SetShellCWD(dir)
	t.Cleanup(func() { spec.SetShellCWD(original) })
	providerfixture.Write(t, dir, "docker", `case "$1" in
ps) exit 7 ;;
images) printf '%s\n' 'example:v1' ;;
*) exit 8 ;;
esac`)
	projection := newSessionHealthProjection(time.Now)
	recorder := newLatencyRecorderWithSession("process-health", 42)
	t.Cleanup(recorder.Close)
	request := func() context.Context {
		return observedProviderContext(t.Context(), projection, recorder, func() bool { return true })
	}
	var generate spec.GeneratorFunc
	for _, command := range spec.Registry["docker"].Subcommands {
		if command.Name == "inspect" {
			generate = command.Generator
		}
	}
	if generate == nil {
		t.Fatal("Docker inspect has no inventory generator")
	}
	for len(spec.CompletionUpdates()) > 0 {
		<-spec.CompletionUpdates()
	}
	_ = generate(request(), []string{"docker", "inspect", ""}, "", "")
	select {
	case <-spec.CompletionUpdates():
	case <-time.After(3 * time.Second):
		t.Fatal("actual aggregate inventory was not published")
	}
	before := projection.snapshot()
	if before.Providers[health.DockerContainersAll].Outcome != health.Failed || before.Providers[health.DockerContainersAll].Failures != 1 {
		t.Fatalf("successful images concealed the container failure: %+v", before)
	}
	image := before.Providers[health.DockerImages].Outcome
	if image != health.Healthy && image != health.Slow {
		t.Fatalf("actual image inventory outcome was lost: %+v", before)
	}
	if badge := projection.badge(); badge.Label != "Docker containers failed" || badge.Recovered {
		t.Fatalf("independent inventories falsely restored each other: %+v", badge)
	}
	results := generate(request(), []string{"docker", "inspect", ""}, "", "")
	if len(results) != 1 || results[0].Cmd != "example:v1" {
		t.Fatalf("failed containers hid useful cached image results: %+v", results)
	}
	if after := projection.snapshot(); after.Revision != before.Revision {
		t.Fatalf("cache reuse invented a health attempt/recovery: before=%+v after=%+v", before, after)
	}
	if err := persistLatencySnapshot(recorder.snapshotCopy()); err != nil {
		t.Fatal(err)
	}
	providers := diagnosticSection(t, diagnosticSection(t, runDiagnosticFixture(t), "health"), "providers")
	container, ok := providers["docker-containers-all"].(map[string]any)
	if !ok || container["last_outcome"] != "failed" || container["failures"] != float64(1) {
		t.Fatalf("sanitized diagnostics lost the actual container failure: %+v", providers)
	}
	if _, ok := providers["docker-images"]; !ok {
		t.Fatalf("sanitized diagnostics lost the independent image outcome: %+v", providers)
	}
}

func TestSessionHealthReadsPreProcessProviderTelemetryWithoutMovingIdentities(t *testing.T) {
	// A snapshot written before process sources existed had fifteen slots.
	// Index eleven was AI; new unobserved sources must remain unknown.
	var legacy [15]providerHealthSnapshot
	legacy[11] = providerHealthSnapshot{Outcome: health.Failed, Failures: 2, ObservedAt: time.Now()}
	data, err := json.Marshal(map[string]any{"health": map[string]any{"revision": 4, "providers": legacy}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded latencySnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Health == nil || decoded.Health.Providers[health.AI].Failures != 2 || decoded.Health.Providers[health.AI].Outcome != health.Failed {
		t.Fatalf("older recorded provider identity moved: %+v", decoded.Health)
	}
	for source := health.GitCommits; source < health.SourceCount; source++ {
		if decoded.Health.Providers[source].Outcome != health.Unknown {
			t.Fatalf("older telemetry fabricated new provider health: %+v", decoded.Health)
		}
	}
}
