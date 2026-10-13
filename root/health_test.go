package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/internal/scoring"
	"path/filepath"
)

func TestSessionHealthDistinguishesJournalLostRecordingAndRecovery(t *testing.T) {
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	h := newSessionHealthProjection(func() time.Time { return now })
	h.observeHistory(true, false)
	if h.badge().Label != "" || h.warning() != "" {
		t.Fatal("healthy operation must remain quiet")
	}
	h.observeHistory(true, true)
	if badge := h.badge(); badge.Label != "History journal" || badge.Critical {
		t.Fatalf("journal durability was hidden or called lost: %+v", badge)
	}
	if h.warning() == "" || h.warning() != "" {
		t.Fatal("first fallback must warn once, not on every read")
	}
	now = now.Add(time.Second)
	h.observeHistory(true, true)
	if h.warning() != "" {
		t.Fatal("repeated journal failures flooded warnings")
	}
	h.observeHistory(false, false)
	if badge := h.badge(); badge.Label != "History not recorded" || !badge.Critical {
		t.Fatalf("loss of recording was conflated with a durable journal: %+v", badge)
	}
	if h.warning() == "" {
		t.Fatal("escalation to lost recording was suppressed")
	}
	h.observeHistory(true, false)
	if h.badge().Label != "History recovering" {
		t.Fatal("canonical success falsely hid pending journal data")
	}
	h.observeRecovery(false, true, nil)
	if badge := h.badge(); badge.Label != "History recording restored" || !badge.Recovered {
		t.Fatalf("recovery was not distinguished from an ongoing error: %+v", badge)
	}
	now = now.Add(healthRecoveryDisplay)
	if h.badge().Label != "" {
		t.Fatal("recovered state did not return to a quiet UI")
	}
}

func TestSessionHealthWarningsRepeatOnlyAtRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	h := newSessionHealthProjection(func() time.Time { return now })
	h.observeHistory(true, true)
	if h.warning() == "" {
		t.Fatal("first fallback warning missing")
	}
	now = now.Add(healthWarningInterval - time.Nanosecond)
	if h.warning() != "" {
		t.Fatal("fallback warning repeated before the rate limit")
	}
	now = now.Add(time.Nanosecond)
	if h.warning() == "" {
		t.Fatal("persistent fallback did not warn at its rate limit")
	}
}

func TestSessionHealthIgnoresCancelledProviderWorkAndDoesNotInventRecovery(t *testing.T) {
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	h := newSessionHealthProjection(func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.observeProvider(ctx, health.Observation{Source: health.AI, Outcome: health.Failed, At: now})
	if h.badge().Label != "" {
		t.Fatal("cancelled request polluted health")
	}
	h.observeProvider(context.Background(), health.Observation{Source: health.AI, Outcome: health.TimedOut, At: now})
	if h.badge().Label != "AI timed out" {
		t.Fatal("actual timeout was not visible")
	}
	now = now.Add(healthProviderFreshness)
	if h.badge().Label != "" || h.badge().Recovered {
		t.Fatal("stale observations were presented as current or recovered")
	}
	if snapshot := h.snapshot(); snapshot.Providers[health.AI].Timeouts != 1 {
		t.Fatal("timeout evidence was lost")
	}
	h.observeProvider(context.Background(), health.Observation{Source: health.AI, Outcome: health.Healthy, At: now})
	if badge := h.badge(); badge.Label != "AI restored" || !badge.Recovered {
		t.Fatalf("actual success did not distinguish recovery: %+v", badge)
	}
}

func TestSessionHealthVisualRecoveryIsNotOngoingDegradation(t *testing.T) {
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	h := newSessionHealthProjection(func() time.Time { return now })
	h.observeVisual(runtimeDiagnosticObservation{State: "managed", ModelRecoveries: 7})
	if h.badge().Label != "" {
		t.Fatal("historical recovery counts caused a new warning")
	}
	h.observeVisual(runtimeDiagnosticObservation{State: "degraded", ModelRecoveries: 8})
	if badge := h.badge(); badge.Label != "Rendering degraded" || !badge.Critical {
		t.Fatalf("degradation was not visible: %+v", badge)
	}
	h.observeVisual(runtimeDiagnosticObservation{State: "managed", ModelRecoveries: 9})
	if badge := h.badge(); badge.Label != "Rendering restored" || !badge.Recovered {
		t.Fatalf("restoration was not distinct: %+v", badge)
	}
}

func TestSessionHealthCompositorShowsLiveBadgeAndRejectsStalePresentation(t *testing.T) {
	var output bytes.Buffer
	c := newTerminalCompositor(&output, "bottom", "health", 80, 24)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(terminalInputBoxTheme{SurfaceBackground: "#242528", StatusText: "#ffffff"})
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	if strings.Contains(c.inputBoxStatusLine(), "History") {
		t.Fatal("healthy surface contains an unsolicited badge")
	}
	c.SetSessionHealth(healthBadge{Label: "History journal", Revision: 2})
	if !strings.Contains(c.inputBoxStatusLine(), "History journal") {
		t.Fatal("live health badge was not rendered")
	}
	c.SetSessionHealth(healthBadge{Revision: 3})
	c.SetSessionHealth(healthBadge{Label: "History journal", Revision: 2})
	if strings.Contains(c.inputBoxStatusLine(), "History journal") {
		t.Fatal("stale presentation overwrote newer health")
	}
}

func TestSessionHealthUsesRealHistoryDurabilityAndNoopCompletion(t *testing.T) {
	diagnosticFixture(t)
	h := newSessionHealthProjection(time.Now)
	entry := integration.HistoryEntry{ID: "health-entry", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	persisted, journaled := persistHistorySubmissionDurablyObserved(nil, entry, h.submissionObserver(entry))
	if !persisted || !journaled || h.snapshot().History != "journal" {
		t.Fatalf("actual fsynced fallback was not observed: persisted=%v journaled=%v health=%+v", persisted, journaled, h.snapshot())
	}
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h.observeHistory(true, false)
	if err := replayHistoryRecoveryJournalWithHealth(store, h.recoveryObserver(store)); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().History != "canonical" || !h.badge().Recovered {
		t.Fatal("actual replay did not restore observed canonical health")
	}
	completed := entry
	completed.HasExitCode = true
	completed.CompletedAt = entry.SubmittedAt.Add(time.Second)
	completed.Duration = time.Second
	completed.State = integration.HistoryStateCompleted
	if done, journaled := completeHistoryEventDurablyObserved(store, completed, h.completionObserver(completed)); !done || journaled {
		t.Fatalf("first completion was not committed: done=%v journaled=%v", done, journaled)
	}
	called := false
	completeHistoryEventDurablyObserved(store, completed, func(bool, bool) { called = true })
	if called {
		t.Fatal("idempotent/noop completion was classified as a persistence failure")
	}
}

func TestSessionHealthRecorderAndDiagnosticShareObservedStates(t *testing.T) {
	diagnosticFixture(t)
	recorder := newLatencyRecorderWithSession("health-report", 42)
	t.Cleanup(recorder.Close)
	h := newSessionHealthProjection(time.Now)
	h.submissionObserver(integration.HistoryEntry{ID: "session-identity-sentinel", Command: "command-sentinel"})(true, true)
	h.observeProvider(context.Background(), health.Observation{Source: health.AI, Outcome: health.TimedOut, At: time.Now()})
	recorder.recordHealth(h.snapshot())
	snapshot := recorder.snapshotCopy()
	if snapshot.Health == nil || snapshot.Health.History != "journal" || snapshot.Health.Providers[health.AI].Timeouts != 1 {
		t.Fatal("diagnostics recorder did not share actual health observations")
	}
	snapshot.Health.History = "credential-sentinel"
	if recorder.snapshotCopy().Health.History != "journal" {
		t.Fatal("caller changed recorder-owned health")
	}
	if err := persistLatencySnapshot(recorder.snapshotCopy()); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(recorder.snapshotCopy().Health)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "session-identity-sentinel") || strings.Contains(string(encoded), "command-sentinel") {
		t.Fatal("health telemetry exposed its private recovery reference")
	}
	report := runDiagnosticFixture(t)
	status, ok := report["health"].(map[string]any)
	if !ok || status["state"] != "recorded" || status["history"] != "journal" {
		t.Fatalf("sanitized diagnostics omitted known health state: %+v", status)
	}
}

func TestSessionHealthDoesNotHideLostRecordingAfterFastBackendRecovery(t *testing.T) {
	h := newSessionHealthProjection(time.Now)
	h.observeHistory(false, false)
	h.observeHistory(true, false)
	if message := h.warning(); !strings.Contains(message, "not recorded") {
		t.Fatalf("a quick backend recovery hid recording loss: %q", message)
	}
}

func TestSessionHealthDistinguishesLostOutcomeFromLostCommand(t *testing.T) {
	h := newSessionHealthProjection(time.Now)
	h.observeHistory(true, false)
	h.observeCompletion(false, false)
	if badge := h.badge(); badge.Label != "History outcome not recorded" || !badge.Critical {
		t.Fatalf("lost outcome was conflated with a lost submission: %+v", badge)
	}
	h.observeHistory(true, false)
	if message := h.warning(); !strings.Contains(message, "outcomes were not recorded") {
		t.Fatalf("fast recovery hid the loss of completion metadata: %q", message)
	}
	if h.warning() != "" {
		t.Fatal("already disclosed outcome loss was reported twice")
	}
}

func TestSessionHealthLostRecordingWarningsCoalesceWithoutLosingPendingNotice(t *testing.T) {
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	h := newSessionHealthProjection(func() time.Time { return now })
	h.observeHistory(false, false)
	if h.warning() == "" {
		t.Fatal("initial loss was hidden")
	}
	h.observeHistory(false, false)
	h.observeHistory(true, false)
	if h.warning() != "" {
		t.Fatal("repeated loss bypassed the warning limit")
	}
	now = now.Add(healthWarningInterval)
	if message := h.warning(); !strings.Contains(message, "not recorded") {
		t.Fatalf("rate-limited pending loss vanished after recovery: %q", message)
	}
	if snapshot := h.snapshot(); snapshot.LostSubmissions != 2 {
		t.Fatalf("diagnostics lost observed failed attempts: %+v", snapshot)
	}
}

func TestSessionHealthCompletionFailureRetainsSubmittedCommand(t *testing.T) {
	diagnosticFixture(t)
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newSessionHealthProjection(time.Now)
	entry := integration.HistoryEntry{ID: "completion-loss", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if persisted, journaled := persistHistorySubmissionDurablyObserved(store, entry, h.observeHistory); !persisted || journaled {
		t.Fatal("fixture submission did not reach canonical history")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	// A non-regular journal is refused even when the test runs with elevated
	// filesystem permissions; the failure does not depend on chmod behavior.
	if err := os.MkdirAll(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	entry.HasExitCode = true
	entry.CompletedAt = entry.SubmittedAt.Add(time.Second)
	if completed, journaled := completeHistoryEventDurablyObserved(store, entry, h.observeCompletion); completed || journaled {
		t.Fatal("closed database/non-regular journal reported a durable outcome")
	}
	if badge := h.badge(); badge.Label != "History outcome not recorded" || !badge.Critical {
		t.Fatalf("actual completion failure was not distinguished: %+v", badge)
	}
	if snapshot := h.snapshot(); snapshot.LostOutcomes != 1 || snapshot.LostSubmissions != 0 {
		t.Fatalf("completion loss incorrectly counted a missing command: %+v", snapshot)
	}
	if message := h.warning(); !strings.Contains(message, "submitted commands remain in history") {
		t.Fatalf("completion warning incorrectly claimed loss of the submission: %q", message)
	}
	reopened, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.QueryHistoryEventsByKeys(context.Background(), []string{entry.ID})
	if err != nil || len(events) != 1 || events[0].Command != entry.Command || events[0].HasExitCode {
		t.Fatalf("failed completion changed the durable submitted command: events=%+v err=%v", events, err)
	}
}

func TestSessionHealthProviderTelemetryUsesOutcomesNotElapsedInference(t *testing.T) {
	diagnosticFixture(t)
	recorder := newLatencyRecorderWithSession("provider-health", 42)
	t.Cleanup(recorder.Close)
	h := newSessionHealthProjection(time.Now)
	current := true
	ctx := observedProviderContext(context.Background(), h, recorder, func() bool { return current })
	health.Report(ctx, health.Arguments, 21*time.Millisecond, nil)
	health.Report(ctx, health.Signals, 101*time.Millisecond, context.DeadlineExceeded)
	recorder.recordSuggestionTimings(ctx, map[string]time.Duration{"arguments": 21 * time.Millisecond, "signals": 101 * time.Millisecond})
	snapshot := recorder.snapshotCopy()
	if snapshot.Timeouts != 1 || snapshot.Health.Providers[health.Signals].Timeouts != 1 {
		t.Fatalf("elapsed timing was counted as a timeout or actual timeout was doubled: %+v", snapshot)
	}
	if h.badge().Label != "Ranking timed out" {
		t.Fatal("provider timeout did not reach the visible health projection")
	}
	current = false
	health.Report(ctx, health.AI, 3*time.Second, context.DeadlineExceeded)
	if recorder.snapshotCopy().Timeouts != 1 {
		t.Fatal("superseded generation changed timeout telemetry")
	}
}

func TestSessionHealthPipelineDeadlinesRemainVisibleWithoutProviderObservation(t *testing.T) {
	for _, phase := range []string{"core-timeout", "local-timeout"} {
		t.Run(phase, func(t *testing.T) {
			diagnosticFixture(t)
			recorder := newLatencyRecorderWithSession("pipeline-deadline", 42)
			t.Cleanup(recorder.Close)
			recorder.recordSuggestionTimings(context.Background(), map[string]time.Duration{phase: time.Millisecond})
			snapshot := recorder.snapshotCopy()
			if snapshot.Timeouts != 1 {
				t.Fatalf("a genuine %s deadline disappeared from telemetry: %d", phase, snapshot.Timeouts)
			}
			if err := persistLatencySnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			report := runDiagnosticFixture(t)
			latency := report["latency"].(map[string]any)
			if latency["timeouts"] != float64(1) {
				t.Fatalf("persisted diagnostics lost the pipeline deadline: %+v", latency)
			}
		})
	}
}

func TestSessionHealthTimeoutSummaryCountsAffectedRequestsOnce(t *testing.T) {
	for _, markerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider-first", true: "deadline-first"}[markerFirst], func(t *testing.T) {
			diagnosticFixture(t)
			recorder := newLatencyRecorderWithSession("overlapping-timeouts", 42)
			t.Cleanup(recorder.Close)
			h := newSessionHealthProjection(time.Now)
			current := true
			parent, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ctx := observedProviderContext(parent, h, recorder, func() bool { return current })
			markers := map[string]time.Duration{"core-timeout": time.Millisecond, "local-timeout": time.Millisecond}
			if markerFirst {
				recorder.recordSuggestionTimings(ctx, markers)
				if got := recorder.snapshotCopy().Timeouts; got != 1 {
					t.Fatalf("deadline evidence was lost before provider completion: %d", got)
				}
			}
			health.Report(ctx, health.Directory, time.Millisecond, context.DeadlineExceeded)
			health.Report(ctx, health.Arguments, time.Millisecond, context.DeadlineExceeded)
			recorder.recordSuggestionTimings(ctx, markers)
			snapshot := recorder.snapshotCopy()
			if snapshot.Timeouts != 1 || snapshot.Health.Providers[health.Directory].Timeouts != 1 || snapshot.Health.Providers[health.Arguments].Timeouts != 1 {
				t.Fatalf("one affected request was inflated or source evidence was lost: %+v", snapshot)
			}
			if err := persistLatencySnapshot(snapshot); err != nil {
				t.Fatal(err)
			}
			if latency := runDiagnosticFixture(t)["latency"].(map[string]any); latency["timeouts"] != float64(1) {
				t.Fatalf("diagnostics inflated overlapping timeout evidence: %+v", latency)
			}
			current = false
			recorder.recordSuggestionTimings(ctx, markers)
			health.Report(ctx, health.AI, time.Millisecond, context.DeadlineExceeded)
			cancel()
			recorder.recordSuggestionTimings(ctx, markers)
			if got := recorder.snapshotCopy().Timeouts; got != 1 {
				t.Fatalf("stale/cancelled work changed accepted timeout count: %d", got)
			}
			// A subsequent independent request must not share the coalescing flag.
			next := observedProviderContext(context.Background(), h, recorder, nil)
			recorder.recordSuggestionTimings(next, markers)
			if got := recorder.snapshotCopy().Timeouts; got != 2 {
				t.Fatalf("a new request's deadline was suppressed: %d", got)
			}
		})
	}
}

func TestSessionHealthPipelineDeadlineRejectsCancelledOrSupersededRequests(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "superseded"}[superseded], func(t *testing.T) {
			diagnosticFixture(t)
			recorder := newLatencyRecorderWithSession("stale-deadline", 42)
			t.Cleanup(recorder.Close)
			parent, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ctx := observedProviderContext(parent, newSessionHealthProjection(time.Now), recorder, func() bool { return !superseded })
			if !superseded {
				cancel()
			}
			recorder.recordSuggestionTimings(ctx, map[string]time.Duration{"core-timeout": time.Millisecond, "local-timeout": time.Millisecond})
			health.Report(ctx, health.Directory, time.Millisecond, context.DeadlineExceeded)
			if got := recorder.snapshotCopy().Timeouts; got != 0 {
				t.Fatalf("inactive work published new timeout evidence: %d", got)
			}
		})
	}
}

func TestSessionHealthDoesNotRepaintNativeApplicationOrClosedSurface(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "closed"}[closed], func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "health", 80, 12)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
			c.WritePTY(terminalMarkerBytes("health", "prompt-start"))
			c.WritePTY([]byte("› app"))
			c.WritePTY(terminalMarkerBytes("health", "prompt-end"))
			c.WritePTY(terminalMarkerBytes("health", "command-start"))
			if closed {
				c.Close()
			} else {
				c.WritePTY([]byte("\x1b[?1049happlication"))
			}
			out.Reset()
			c.SetSessionHealth(healthBadge{Label: "AI failed", Revision: 1})
			if out.Len() != 0 {
				t.Fatalf("health update overwrote an unowned surface: %q", out.String())
			}
		})
	}
}

func TestSessionHealthRepaintsOnlyStatusWhileReadingRunningOutput(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "health", 80, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputLines: 100, Status: terminalChatboxBarConfig{Right: []string{"exit"}}})
	c.WritePTY(terminalMarkerBytes("health", "prompt-start"))
	c.WritePTY([]byte("› slow"))
	c.WritePTY(terminalMarkerBytes("health", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("health", "command-start"))
	c.WritePTY([]byte(strings.Repeat("retained output\r\n", 30)))
	if !c.HandleViewportInput([]byte("\x1b[5~"), false) {
		t.Fatal("PageUp did not freeze output")
	}
	initial := append([]byte(nil), out.Bytes()...)
	before := applyTerminalOutput(t, initial, 80, 12)
	beforeCursor := before.CursorPosition()
	top, frozen := c.viewport.top, append([]string(nil), c.viewport.frozen...)
	out.Reset()
	c.SetSessionHealth(healthBadge{Label: "History journal", Revision: 1})
	after := applyTerminalOutput(t, append(initial, out.Bytes()...), 80, 12)
	if !strings.Contains(screenLine(after, 11), "History journal") {
		t.Fatal("health was not rendered in the reserved status row")
	}
	if after.CursorPosition() != beforeCursor || c.viewport.top != top || !slices.Equal(c.viewport.frozen, frozen) {
		t.Fatal("health update moved the cursor or reading anchor")
	}
	for row := range 11 {
		if screenLine(before, row) != screenLine(after, row) {
			t.Fatalf("health update changed non-status row %d", row)
		}
	}
	for _, mode := range []string{"\x1b[?1000", "\x1b[?1003", "\x1b[?1006", "\x1b[?1049"} {
		if strings.Contains(out.String(), mode) {
			t.Fatal("health update changed native selection or screen ownership")
		}
	}
}

func TestSessionHealthRecorderRejectsOlderObservationSnapshots(t *testing.T) {
	diagnosticFixture(t)
	recorder := newLatencyRecorderWithSession("health-order", 42)
	t.Cleanup(recorder.Close)
	recorder.recordHealth(sessionHealthSnapshot{Revision: 2, History: "canonical", LostSubmissions: 3})
	recorder.recordHealth(sessionHealthSnapshot{Revision: 1, History: "journal", LostSubmissions: 2})
	if observed := recorder.snapshotCopy().Health; observed == nil || observed.History != "canonical" || observed.LostSubmissions != 3 {
		t.Fatalf("late publication regressed the observed state: %+v", observed)
	}
}

func TestSessionHealthConcurrentObserversPublishLatestSnapshot(t *testing.T) {
	diagnosticFixture(t)
	recorder := newLatencyRecorderWithSession("health-concurrent", 42)
	t.Cleanup(recorder.Close)
	now := time.Now()
	h := newSessionHealthProjection(func() time.Time { return now })
	var workers sync.WaitGroup
	for range 100 {
		workers.Go(func() {
			h.observeProvider(context.Background(), health.Observation{Source: health.Arguments, Outcome: health.Failed, At: now})
			recorder.recordHealth(h.snapshot())
		})
	}
	workers.Wait()
	latest := h.snapshot()
	observed := recorder.snapshotCopy().Health
	if observed == nil || *observed != latest || latest.Providers[health.Arguments].Failures != 100 {
		t.Fatalf("concurrent publication lost accepted health evidence: recorded=%+v latest=%+v", observed, latest)
	}
}

func TestSessionHealthRejectsStaleRecoveryAfterNewRecordingOutcome(t *testing.T) {
	for _, next := range []struct {
		name                 string
		persisted, journaled bool
	}{
		{"fallback", true, true}, {"canonical", true, false}, {"lost", false, false},
	} {
		for _, recoveryErr := range []error{nil, errors.New("private-recovery-error")} {
			t.Run(next.name+map[bool]string{false: "/success", true: "/error"}[recoveryErr != nil], func(t *testing.T) {
				h := newSessionHealthProjection(time.Now)
				h.observeHistory(true, true)
				finish := h.beginRecoveryObservation()
				// An actual newer durability outcome reaches the projection after
				// the retry began, but before its old metadata is published.
				h.observeHistory(next.persisted, next.journaled)
				latest := h.snapshot()
				finish(false, true, recoveryErr)
				if after := h.snapshot(); after != latest {
					t.Fatalf("older retry replaced newer durability evidence: before=%+v after=%+v", latest, after)
				}
			})
		}
	}
}

func TestSessionHealthReplayCannotHideANewerRealJournalFallback(t *testing.T) {
	diagnosticFixture(t)
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := newSessionHealthProjection(time.Now)
	first := integration.HistoryEntry{ID: "first-fallback", Command: "first-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, first, h.submissionObserver(first)); !durable || !journaled {
		t.Fatal("first fallback was not durable")
	}
	type observation struct {
		pending, recovered bool
		err                error
	}
	ready := make(chan observation, 1)
	release, finished := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var replayErr error
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); <-finished })
	go func() {
		defer close(finished)
		replayErr = replayHistoryRecoveryJournalWithHealth(store, func() func(bool, bool, error) {
			finish := h.recoveryObserver(store)()
			return func(pending, recovered bool, err error) {
				ready <- observation{pending, recovered, err}
				<-release
				finish(pending, recovered, err)
			}
		})
	}()
	select {
	case result := <-ready:
		if result.pending || !result.recovered || result.err != nil {
			t.Fatalf("fixture did not replay A and observe an empty journal: %+v", result)
		}
	case <-finished:
		t.Fatalf("replay ended without its observation: %v", replayErr)
	}
	second := first
	second.ID, second.Command = "second-fallback", "second-sentinel"
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, second, h.submissionObserver(second)); !durable || !journaled {
		t.Fatal("newer fallback was not durable")
	}
	if pending, err := historyRecoveryJournalPending(); !pending || err != nil {
		t.Fatalf("newer record is not pending: pending=%v err=%v", pending, err)
	}
	releaseOnce.Do(func() { close(release) })
	<-finished
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	if snapshot := h.snapshot(); snapshot.History != "journal" || !snapshot.RecoveryPending || h.badge().Recovered {
		t.Fatalf("older successful replay hid the new durable fallback: history=%s pending=%v badge=%+v", snapshot.History, snapshot.RecoveryPending, h.badge())
	}
	if err := replayHistoryRecoveryJournalWithHealth(store, h.recoveryObserver(store)); err != nil {
		t.Fatal(err)
	}
	if snapshot := h.snapshot(); snapshot.History != "canonical" || snapshot.RecoveryPending || !h.badge().Recovered {
		t.Fatalf("a genuinely subsequent replay did not restore canonical health: %+v", snapshot)
	}
	events, err := store.QueryHistoryEventsByKeys(context.Background(), []string{first.ID, second.ID})
	if err != nil || len(events) != 2 {
		t.Fatalf("the two real submissions were not preserved: events=%+v err=%v", events, err)
	}
}

func TestSessionHealthNoWorkRetryReconcilesQueueDrainedBySupersededReplay(t *testing.T) {
	diagnosticFixture(t)
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := newSessionHealthProjection(time.Now)
	first := integration.HistoryEntry{ID: "before-replay-a", Command: "first-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, first, h.submissionObserver(first)); !durable || !journaled {
		t.Fatal("first fallback was not durable")
	}
	captured, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var replayErr error
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); <-finished })
	go func() {
		defer close(finished)
		replayErr = replayHistoryRecoveryJournalWithHealth(store, func() func(bool, bool, error) {
			finish := h.recoveryObserver(store)()
			close(captured)
			<-release
			return finish
		})
	}()
	select {
	case <-captured:
	case <-finished:
		t.Fatalf("replay ended before capture: %v", replayErr)
	}
	second := first
	second.ID, second.Command = "before-replay-b", "second-sentinel"
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, second, h.submissionObserver(second)); !durable || !journaled {
		t.Fatal("newer fallback was not durable")
	}
	releaseOnce.Do(func() { close(release) })
	<-finished
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	finish := h.recoveryObserver(store)()
	recovered, err := retryHistoryRecoveryOnce(context.Background(), store)
	if err != nil || recovered {
		t.Fatalf("expected a genuine no-work retry after A+B replay: recovered=%v err=%v", recovered, err)
	}
	pending, err := historyRecoveryJournalPending()
	if err != nil || pending {
		t.Fatalf("replay did not drain both records: pending=%v err=%v", pending, err)
	}
	finish(pending, recovered, err)
	events, err := store.QueryHistoryEventsByKeys(context.Background(), []string{first.ID, second.ID})
	if err != nil || len(events) != 2 {
		t.Fatalf("actual replay lost a submitted record: %+v %v", events, err)
	}
	if snapshot := h.snapshot(); snapshot.RecoveryPending || snapshot.History == "journal" || snapshot.History == "recovering" {
		t.Fatalf("empty-queue reconciliation retained a false ongoing fallback: history=%s pending=%v", snapshot.History, snapshot.RecoveryPending)
	}
	if snapshot := h.snapshot(); snapshot.History != "canonical" || !h.badge().Recovered {
		t.Fatalf("the affected canonical records did not confirm actual recovery: history=%s badge=%+v", snapshot.History, h.badge())
	}
	if badge := h.badge(); badge.Label == "History journal" || badge.Label == "History recovering" {
		t.Fatalf("drained queue still has an ongoing fallback badge: %+v", badge)
	}
	if message := h.warning(); strings.Contains(message, "canonical history unavailable") {
		t.Fatalf("drained queue still warns that canonical history is unavailable: %q", message)
	}
}

func TestSessionHealthEmptyQueueAloneDoesNotProveCanonicalWriteRecovery(t *testing.T) {
	h := newSessionHealthProjection(time.Now)
	h.observeHistory(true, true)
	h.beginRecoveryObservation()(false, false, nil)
	if snapshot := h.snapshot(); snapshot.History == "canonical" || h.badge().Recovered {
		t.Fatalf("no-work queue metadata invented canonical recovery: history=%s badge=%+v", snapshot.History, h.badge())
	}
}

func TestSessionHealthEmptyQueueDoesNotReviveRecoveryBeforeANewerFailure(t *testing.T) {
	h := newSessionHealthProjection(time.Now)
	h.observeHistory(true, true)
	h.beginRecoveryObservation()(false, true, nil)
	if !h.badge().Recovered {
		t.Fatal("fixture did not establish a prior actual recovery")
	}
	h.observeHistory(true, true)
	h.beginRecoveryObservation()(false, false, nil)
	if h.badge().Recovered {
		t.Fatal("empty queue revived a recovery predating the newer fallback")
	}
}

func TestSessionHealthCanonicalConfirmationDoesNotResurrectClearedOrUncompletedHistory(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleared", true: "uncompleted"}[completion], func(t *testing.T) {
			diagnosticFixture(t)
			store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			h := newSessionHealthProjection(time.Now)
			entry := integration.HistoryEntry{ID: "effect-sentinel", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
			if durable, journaled := persistHistorySubmissionDurablyObserved(store, entry, h.submissionObserver(entry)); !durable || journaled {
				t.Fatal("fixture submission was not canonical")
			}
			kind := historyRecoverySubmission
			observe := h.submissionObserver(entry)
			if completion {
				entry.HasExitCode = true
				entry.CompletedAt = entry.SubmittedAt.Add(time.Second)
				kind = historyRecoveryCompletion
				observe = h.completionObserver(entry)
			}
			if err := appendHistoryRecoveryRecord(kind, entry); err != nil {
				t.Fatal(err)
			}
			observe(true, true)
			if !completion {
				if err := store.ClearHistory(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err := clearHistoryRecoveryJournal(); err != nil {
				t.Fatal(err)
			}
			h.recoveryObserver(store)()(false, false, nil)
			if state := h.snapshot(); state.History != "unknown" || state.RecoveryPending || h.badge().Recovered {
				t.Fatalf("clear/absent completion invented canonical recovery: history=%s pending=%v badge=%+v", state.History, state.RecoveryPending, h.badge())
			}
			events, err := store.QueryHistoryEventsByKeys(t.Context(), []string{entry.ID})
			if err != nil {
				t.Fatal(err)
			}
			if !completion && len(events) != 0 || completion && (len(events) != 1 || events[0].HasExitCode) {
				t.Fatalf("health confirmation mutated the canonical history effect: %+v", events)
			}
		})
	}
}

func TestSessionHealthRecoveryAttemptDoesNotConfirmClearedExecution(t *testing.T) {
	diagnosticFixture(t)
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := newSessionHealthProjection(time.Now)
	entry := integration.HistoryEntry{ID: "cleared-recovery-effect", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, entry, h.submissionObserver(entry)); !durable || !journaled {
		t.Fatal("fixture fallback was not durable")
	}
	finish := h.beginRecoveryObservationWithStore(store)
	wasPending, err := historyRecoveryJournalPending()
	if err != nil || !wasPending {
		t.Fatalf("fixture queue was not pending: %v %v", wasPending, err)
	}
	// A different session replays on opening the command store, then clears
	// history and the journal. It cannot advance this projection's revision.
	other, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if err := replayHistoryRecoveryJournal(other); err != nil {
		t.Fatal(err)
	}
	if err := other.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := clearHistoryRecoveryJournal(); err != nil {
		t.Fatal(err)
	}
	// The existing producer derives attempted recovery from its pre-read and
	// a nil replay error. An empty replay does not prove the effect survived.
	replayErr := replayHistoryRecoveryJournal(store)
	pending, pendingErr := historyRecoveryJournalPending()
	if replayErr != nil || pendingErr != nil || pending {
		t.Fatalf("empty replay failed: %v %v pending=%v", replayErr, pendingErr, pending)
	}
	finish(pending, wasPending && replayErr == nil, pendingErr)
	if state := h.snapshot(); state.History != "unknown" || state.RecoveryPending || h.badge().Recovered {
		t.Fatalf("a cleared execution was falsely recovered: history=%s pending=%v badge=%+v", state.History, state.RecoveryPending, h.badge())
	}
	if present, err := store.HasCanonicalHistoryEffect(t.Context(), entry.ID, false); present || err != nil {
		t.Fatalf("health resurrected a cleared execution: %v %v", present, err)
	}
}

func TestSessionHealthCanonicalConfirmationHandlesUnavailableStore(t *testing.T) {
	diagnosticFixture(t)
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := newSessionHealthProjection(time.Now)
	entry := integration.HistoryEntry{ID: "unavailable-effect", Command: "command-sentinel", SubmittedAt: time.Now(), Source: "vuja", State: integration.HistoryStateSubmitted}
	if durable, journaled := persistHistorySubmissionDurablyObserved(nil, entry, h.submissionObserver(entry)); !durable || !journaled {
		t.Fatal("fixture fallback was not durable")
	}
	if err := replayHistoryRecoveryJournal(store); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	h.recoveryObserver(store)()(false, false, nil)
	if state := h.snapshot(); state.History == "canonical" || state.RecoveryPending || h.badge().Recovered {
		t.Fatalf("unavailable database was called recovered: history=%s pending=%v badge=%+v", state.History, state.RecoveryPending, h.badge())
	}
	if message := h.warning(); !strings.Contains(message, "could not be confirmed") || strings.Contains(message, "records were retained") {
		t.Fatalf("failed confirmation made an unsupported queue claim: %q", message)
	}
}
