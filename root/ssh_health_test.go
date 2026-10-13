package root

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
)

func TestSessionHealthSSHDiscoveryReachesBadgeAndSanitizedDiagnostics(t *testing.T) {
	home := diagnosticFixture(t)
	original := spec.GetCWD()
	spec.SetShellCWD(home)
	t.Cleanup(func() { spec.SetShellCWD(original) })
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "bad-input"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host fixture-health-alias\nInclude bad-input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := newSessionHealthProjection(time.Now)
	recorder := newLatencyRecorderWithSession("ssh-health", 42)
	t.Cleanup(recorder.Close)
	request := observedProviderContext(t.Context(), projection, recorder, func() bool { return true })
	for len(spec.CompletionUpdates()) > 0 {
		<-spec.CompletionUpdates()
	}
	provider := spec.Registry["ssh"].Generator
	_ = provider(request, []string{"ssh", ""}, "", "")
	select {
	case <-spec.CompletionUpdates():
	case <-time.After(3 * time.Second):
		t.Fatal("SSH acquisition was not published")
	}
	before := projection.snapshot()
	if state := before.Providers[health.SSHHosts]; state.Outcome != health.Failed || state.Failures != 1 {
		t.Fatalf("partial acquisition concealed the failure: %+v", state)
	}
	if badge := projection.badge(); badge.Label != "SSH hosts failed" || badge.Recovered {
		t.Fatalf("actual failure was not actionable: %+v", badge)
	}
	results := provider(request, []string{"ssh", ""}, "", "")
	found := false
	for _, result := range results {
		found = found || result.Cmd == "fixture-health-alias"
	}
	if !found || projection.snapshot().Revision != before.Revision {
		t.Fatal("cached partial aliases were lost or invented recovery")
	}
	if err := persistLatencySnapshot(recorder.snapshotCopy()); err != nil {
		t.Fatal(err)
	}
	providers := runDiagnosticFixture(t)["health"].(map[string]any)["providers"].(map[string]any)
	state, ok := providers["ssh-hosts"].(map[string]any)
	if !ok || state["last_outcome"] != "failed" || state["failures"] != float64(1) {
		t.Fatalf("sanitized diagnostics omitted actual SSH failure: %+v", providers)
	}
}
