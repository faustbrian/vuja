package root

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"golang.org/x/sys/unix"
)

func diagnosticSection(t *testing.T, document map[string]any, key string) map[string]any {
	t.Helper()
	section, ok := document[key].(map[string]any)
	if !ok {
		t.Fatalf("diagnostic section %q is missing or not an object", key)
	}
	return section
}

func diagnosticFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"),
		"SHELL": "/private/shell-env-sentinel/zsh", "TERM": "terminal-env-sentinel",
		"COLORTERM": "truecolor", "LANG": "locale-env-sentinel.UTF-8",
	} {
		t.Setenv(key, value)
	}
	cfg := config.DefaultConfig()
	cfg.AI.Providers = map[string]config.ProviderConfig{"private-provider": {APIKey: "credential-sentinel"}}
	data, err := config.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("source $HOME/.local/share/vuja/init.zsh\n# output-sentinel\nexport SECRET=credential-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := config.CachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	telemetry := `{"session_id":"session-identity-sentinel","pid":987654321,"samples_us":{"first-paint":[1000,2000,4000],"credential-sentinel":[9999999]},"timeouts":3,"cache":{"credential-sentinel":{"hits":5}},"runtime":{"width":120,"height":40,"model_recoveries":2,"backdrop_recoveries":1,"visual_recoveries":4,"state":"managed"}}`
	if err := os.WriteFile(filepath.Join(cache, "suggestion-latency-sentinel-1.json"), []byte(telemetry), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func runDiagnosticFixture(t *testing.T) map[string]any {
	t.Helper()
	var output bytes.Buffer
	cmd := newDebugDiagnosticsCommand()
	cmd.SetOut(&output)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("diagnostic report failed: %v", err)
	}
	for _, forbidden := range []string{"credential-sentinel", "shell-env-sentinel", "terminal-env-sentinel", "locale-env-sentinel", "command-sentinel", "output-sentinel", "person@example.test", "session-identity-sentinel", "987654321", os.Getenv("HOME")} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("diagnostic report leaked a private sentinel (%q)", forbidden)
		}
	}
	var report map[string]any
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("expected a structured sanitized diagnostic report: %v", err)
	}
	return report
}

func TestDiagnosticsReportsMetadataAndSanitizedLatency(t *testing.T) {
	diagnosticFixture(t)
	report := runDiagnosticFixture(t)
	for _, field := range []string{"versions", "configuration", "shell", "terminal", "history", "latency", "visual"} {
		if report[field] == nil {
			t.Fatalf("diagnostics omitted %s", field)
		}
	}
	if diagnosticSection(t, report, "configuration")["state"] != "valid" {
		t.Fatalf("fixture configuration was not inspected: %+v", report["configuration"])
	}
	latency := diagnosticSection(t, report, "latency")
	paint, ok := diagnosticSection(t, latency, "phases")["first-paint"].(map[string]any)
	if !ok {
		t.Fatalf("known latency fixture was not read: %+v", latency)
	}
	if paint["samples"] != float64(3) || paint["p50_us"] != float64(2000) || paint["p95_us"] != float64(4000) || latency["timeouts"] != float64(3) {
		t.Fatalf("incorrect summary of known telemetry: %+v", latency)
	}
	terminal := diagnosticSection(t, report, "terminal")
	visual := diagnosticSection(t, report, "visual")
	if terminal["width"] != float64(120) || terminal["height"] != float64(40) || visual["model_recoveries"] != float64(2) || visual["backdrop_recoveries"] != float64(1) || visual["visual_recoveries"] != float64(4) {
		t.Fatalf("runtime observations missing: terminal=%+v visual=%+v", terminal, visual)
	}
}

func TestDiagnosticsReadsRealModelCountersAndRecorderSnapshot(t *testing.T) {
	diagnosticFixture(t)
	var output bytes.Buffer
	c := newTerminalCompositor(&output, "bottom", "diagnostic-fixture", 80, 24)
	t.Cleanup(c.Close)
	c.mu.Lock()
	c.recoverModel()
	c.recoverBackdrop()
	c.mu.Unlock()
	observation := c.diagnosticObservation()
	if observation.Width != 80 || observation.Height != 24 || observation.ModelRecoveries != 1 || observation.BackdropRecoveries != 1 {
		t.Fatalf("diagnostics did not read the actual model recovery owners: %+v", observation)
	}
	recorder := newLatencyRecorderWithSession("diagnostic-fixture", 42)
	t.Cleanup(recorder.Close)
	recorder.recordRuntime(observation)
	snapshot := recorder.snapshotCopy()
	if snapshot.Runtime == nil || *snapshot.Runtime != observation {
		t.Fatalf("recorder did not retain the runtime observation: %+v", snapshot.Runtime)
	}
	snapshot.Runtime.ModelRecoveries = 999
	if next := recorder.snapshotCopy(); next.Runtime.ModelRecoveries != 1 {
		t.Fatal("a caller mutated the shared runtime observation")
	}
	if err := persistLatencySnapshot(recorder.snapshotCopy()); err != nil {
		t.Fatal(err)
	}
	report := runDiagnosticFixture(t)
	if diagnosticSection(t, report, "visual")["model_recoveries"] != float64(1) {
		t.Fatalf("persisted runtime observation was not reported: %+v", report["visual"])
	}
}

func TestDiagnosticsDoesNotReplayOrMutateHistory(t *testing.T) {
	home := diagnosticFixture(t)
	dbPath := filepath.Join(home, ".local", "share", "vuja", "history.db")
	store, err := scoring.NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := scoring.HistoryEvent{EventKey: "retained", Command: "command-sentinel", Cwd: "/private/path-sentinel", SessionID: "private-session", Source: "vuja", SubmittedAt: time.Now(), State: "submitted"}
	if err := store.RecordHistorySubmission(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	entry := integration.HistoryEntry{ID: "pending", Command: "pending-command-sentinel", Cwd: "/private/path-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	journalPath, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	report := runDiagnosticFixture(t)
	history := diagnosticSection(t, report, "history")
	if history["database"] != "readable" || history["recovery_pending"] != true {
		t.Fatalf("expected readable canonical database with separate pending recovery: %+v", history)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("diagnostics rewrote the recovery journal: %v", err)
	}
	stats, err := store.HistoryStats(context.Background())
	if err != nil || stats.Events != 1 {
		t.Fatalf("diagnostics replayed or mutated canonical events: %+v, %v", stats, err)
	}
}

func TestDiagnosticsDoesNotRepairRecoveryOffsetPermissions(t *testing.T) {
	diagnosticFixture(t)
	entry := integration.HistoryEntry{ID: "pending", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if err := appendHistoryRecoveryRecord(historyRecoverySubmission, entry); err != nil {
		t.Fatal(err)
	}
	path, err := historyRecoveryOffsetPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	runDiagnosticFixture(t)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("a support report repaired recovery metadata: info=%v err=%v", info, err)
	}
}

func TestDiagnosticsInvocationIsIsolatedFromRuntimeBootstrap(t *testing.T) {
	home := diagnosticFixture(t)
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	invalid := []byte("credential-sentinel = [\n")
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VUJA_RELOADED", "true")
	for _, args := range [][]string{{"debug", "diagnostics"}, {"--shell", "credential-sentinel", "debug", "diagnostics"}, {"debug", "diagnostics", "credential-sentinel"}} {
		var out bytes.Buffer
		handled, err := executeDiagnosticsInvocation(args, &out)
		if !handled {
			t.Fatalf("diagnostics would fall through runtime bootstrap: %v", args)
		}
		if strings.Contains(out.String(), "credential-sentinel") || strings.Contains(out.String(), home) || err != nil && (strings.Contains(err.Error(), "credential-sentinel") || strings.Contains(err.Error(), home)) {
			t.Fatal("diagnostic entry leaked raw configuration or arguments")
		}
		if len(args) == 2 {
			var report map[string]any
			if err != nil || json.Unmarshal(out.Bytes(), &report) != nil || diagnosticSection(t, report, "configuration")["state"] != "invalid-or-unreadable" {
				t.Fatalf("invalid configuration prevented a sanitized report: %v %q", err, out.String())
			}
		} else if err == nil {
			t.Fatal("unexpected diagnostics arguments were silently accepted")
		}
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, invalid) {
		t.Fatal("diagnostics bootstrap changed invalid configuration")
	}
	if os.Getenv("VUJA_RELOADED") != "true" {
		t.Fatal("diagnostics consumed runtime reload state")
	}
	if handled, err := executeDiagnosticsInvocation([]string{"history", "stats"}, &bytes.Buffer{}); handled || err != nil {
		t.Fatal("diagnostics took ownership of an unrelated command")
	}
}

func TestDiagnosticsRejectsNonregularAndOversizedInputs(t *testing.T) {
	for _, input := range []string{"config-fifo", "cache-fifo", "counter-fifo", "journal-fifo", "oversized-latency"} {
		t.Run(input, func(t *testing.T) {
			diagnosticFixture(t)
			var path string
			switch input {
			case "config-fifo":
				path, _ = config.ConfigPath()
			case "cache-fifo":
				path, _ = config.CachePath()
			case "counter-fifo":
				path, _ = historyPersistenceFailurePath()
			case "journal-fifo":
				path, _ = historyRecoveryJournalPath()
			case "oversized-latency":
				cache, _ := config.CachePath()
				path = filepath.Join(cache, "suggestion-latency-sentinel-1.json")
			}
			if input == "oversized-latency" {
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), diagnosticFileLimit+1), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); err == nil {
					if err := os.Rename(path, path+".fixture-original"); err != nil {
						t.Fatal(err)
					}
				}
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report := runDiagnosticFixture(t)
			switch input {
			case "config-fifo":
				if diagnosticSection(t, report, "configuration")["state"] != "invalid-or-unreadable" {
					t.Fatal("nonregular configuration was reported valid")
				}
			case "cache-fifo", "oversized-latency":
				if diagnosticSection(t, report, "latency")["state"] != "unavailable" {
					t.Fatal("unsafe telemetry was reported available")
				}
			case "counter-fifo":
				if diagnosticSection(t, report, "history")["persistence_failures_state"] != "unavailable" {
					t.Fatal("unsafe failure counter was reported available")
				}
			case "journal-fifo":
				if diagnosticSection(t, report, "history")["recovery_state"] != "unavailable" {
					t.Fatal("unsafe journal was reported empty")
				}
			}
		})
	}
}

func TestDiagnosticsMissingInputsAreNotCreated(t *testing.T) {
	home := diagnosticFixture(t)
	cache, _ := config.CachePath()
	if err := os.Rename(cache, cache+".fixture-original"); err != nil {
		t.Fatal(err)
	}
	path, _ := config.ConfigPath()
	if err := os.Rename(path, path+".fixture-original"); err != nil {
		t.Fatal(err)
	}
	report := runDiagnosticFixture(t)
	if diagnosticSection(t, report, "configuration")["state"] != "unavailable" || diagnosticSection(t, report, "latency")["state"] != "unavailable" || diagnosticSection(t, report, "history")["database"] != "unavailable" {
		t.Fatal("missing sources were reported available")
	}
	for _, absent := range []string{cache, path, filepath.Join(home, ".local", "share", "vuja", "history.db")} {
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Fatalf("diagnostics created a missing input: %s (%v)", absent, err)
		}
	}
}

func TestDiagnosticsObserverCapturesOwnerAndStopsIdempotently(t *testing.T) {
	diagnosticFixture(t)
	c := newTerminalCompositor(&bytes.Buffer{}, "bottom", "observation", 100, 30)
	t.Cleanup(c.Close)
	recorder := newLatencyRecorderWithSession("observer", 42)
	t.Cleanup(recorder.Close)
	stop := startRuntimeDiagnostics(c, recorder)
	t.Cleanup(stop)
	snapshot := recorder.snapshotCopy()
	if snapshot.Runtime == nil || snapshot.Runtime.Width != 100 || snapshot.Runtime.State != "managed" {
		t.Fatalf("observer did not publish compositor metadata: %+v", snapshot.Runtime)
	}
	stop()
	stop()
	c.mu.Lock()
	c.layout = false
	c.modelRecoveries = 17
	c.mu.Unlock()
	if snapshot := recorder.snapshotCopy(); snapshot.Runtime.ModelRecoveries != 0 {
		t.Fatal("stopped observer continued to sample the compositor")
	}
	observation := c.diagnosticObservation()
	if observation.State != "suspended" || observation.ModelRecoveries != 17 {
		t.Fatal("historical recoveries were conflated with ongoing degradation")
	}
}

func TestDiagnosticsObservationSurvivesConcurrentResizeAndOutput(t *testing.T) {
	diagnosticFixture(t)
	c := newTerminalCompositor(&lockedBuffer{}, "bottom", "observation", 100, 30)
	t.Cleanup(c.Close)
	recorder := newLatencyRecorderWithSession("resize-observation", 42)
	t.Cleanup(recorder.Close)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		for i := range 50 {
			c.Resize(80+i%2*20, 24+i%2*6)
		}
	}()
	go func() {
		defer workers.Done()
		for range 50 {
			c.WritePTY([]byte("\x1b[1;30r\x1b[1;1H\x1bMoutput\r\n"))
		}
	}()
	go func() {
		defer workers.Done()
		for range 50 {
			recorder.recordRuntime(c.diagnosticObservation())
			if snapshot := recorder.snapshotCopy(); snapshot.Runtime != nil {
				snapshot.Runtime.Width = -1 // Returned observations are caller-owned.
			}
		}
	}()
	workers.Wait()
	recorder.recordRuntime(c.diagnosticObservation())
	snapshot := recorder.snapshotCopy()
	if snapshot.Runtime == nil || snapshot.Runtime.Width != 100 || snapshot.Runtime.Height != 30 || snapshot.Runtime.State != "managed" {
		t.Fatalf("concurrent observation lost the final terminal state: %+v", snapshot.Runtime)
	}
}
