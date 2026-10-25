package root

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/internal/scoring"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
	"golang.org/x/sys/unix"
)

const diagnosticFileLimit = 1 << 20

func executeDiagnosticsInvocation(args []string, out io.Writer) (bool, error) {
	command, remaining, err := rootCmd.Find(args)
	if err != nil || command.Name() != "diagnostics" || command.Parent() != DebugCmd {
		return false, nil
	}
	// Resolve this finite command before runtime configuration migration,
	// logging, reload notices or shell/watchdog startup. Never echo arguments.
	if len(remaining) != 0 {
		if len(remaining) == 1 && (remaining[0] == "--help" || remaining[0] == "-h") {
			_, err := fmt.Fprintln(out, "Usage: vuja debug diagnostics\nEmit a sanitized diagnostic report without replaying history.")
			return true, err
		}
		return true, errors.New("diagnostics accepts no arguments or configuration overrides")
	}
	cmd := newDebugDiagnosticsCommand()
	cmd.SetOut(out)
	return true, cmd.RunE(cmd, nil)
}

type runtimeDiagnosticObservation struct {
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	ModelRecoveries    uint64 `json:"model_recoveries"`
	BackdropRecoveries uint64 `json:"backdrop_recoveries"`
	VisualRecoveries   uint64 `json:"visual_recoveries"`
	State              string `json:"state"`
}

func (c *terminalCompositor) diagnosticObservation() runtimeDiagnosticObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := "managed"
	switch {
	case !c.enabled || c.closed:
		state = "unmanaged"
	case c.emulator == nil:
		state = "degraded"
	case c.viewportPassthrough:
		state = "application"
	case !c.layout:
		state = "suspended"
	}
	return runtimeDiagnosticObservation{
		Width: c.width, Height: c.height, State: state,
		ModelRecoveries: c.modelRecoveries, BackdropRecoveries: c.backdropRecoveries,
		VisualRecoveries: c.visualRecoveries,
	}
}

// Sampling does no filesystem work and never holds the renderer lock while
// enqueueing telemetry. Stop joins the observer before the compositor closes.
func startRuntimeDiagnostics(c *terminalCompositor, recorder *latencyRecorder) func() {
	return startRuntimeDiagnosticsObserved(c, recorder, nil)
}

func startRuntimeDiagnosticsObserved(c *terminalCompositor, recorder *latencyRecorder, observe func(runtimeDiagnosticObservation)) func() {
	ticker := time.NewTicker(time.Second)
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	sample := func() {
		observation := c.diagnosticObservation()
		recorder.recordRuntime(observation)
		if observe != nil {
			observe(observation)
		}
	}
	sample()
	go func() {
		defer close(done)
		for {
			select {
			case <-ticker.C:
				sample()
			case <-stop:
				return
			}
		}
	}()
	return func() {
		once.Do(func() { ticker.Stop(); close(stop) })
		<-done
	}
}

type diagnosticLatencyPhase struct {
	Samples int   `json:"samples"`
	P50US   int64 `json:"p50_us"`
	P95US   int64 `json:"p95_us"`
}

type diagnosticReport struct {
	FormatVersion int               `json:"format_version"`
	Versions      map[string]string `json:"versions"`
	Configuration map[string]any    `json:"configuration"`
	Shell         map[string]any    `json:"shell"`
	Terminal      map[string]any    `json:"terminal"`
	History       map[string]any    `json:"history"`
	Latency       struct {
		State    string                            `json:"state"`
		Timeouts uint64                            `json:"timeouts"`
		Phases   map[string]diagnosticLatencyPhase `json:"phases"`
	} `json:"latency"`
	Visual map[string]any `json:"visual"`
	Health map[string]any `json:"health"`
}

func newDebugDiagnosticsCommand() *cobra.Command {
	return &cobra.Command{
		Use: "diagnostics", Short: "emit a sanitized diagnostic report without replaying history",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("diagnostics accepts no arguments")
			}
			return nil
		},
		PersistentPreRun: func(*cobra.Command, []string) {},
		RunE: func(cmd *cobra.Command, _ []string) error {
			parent := cmd.Context()
			if parent == nil {
				parent = context.Background()
			}
			ctx, cancel := context.WithTimeout(parent, 2*time.Second)
			defer cancel()
			report := collectDiagnosticReport(ctx)
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		},
	}
}

func collectDiagnosticReport(ctx context.Context) diagnosticReport {
	version := Version
	if version != "dev" && (!semver.IsValid(version) || len(version) > 128) {
		version = "unknown"
	}
	report := diagnosticReport{
		FormatVersion: 1,
		Versions:      map[string]string{"vuja": version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH},
		Configuration: map[string]any{"state": "unavailable"},
		Shell:         map[string]any{"managed_protocol": "built-in", "startup": "unavailable", "hook": "unavailable"},
		Terminal:      map[string]any{"dimensions": "unavailable"},
		History:       map[string]any{"database": "unavailable", "recording": "not-observed"},
		Visual:        map[string]any{"state": "not-observed"},
		Health:        map[string]any{"state": "not-observed"},
	}
	doctor := inspectDoctor()
	if doctor.configErr == nil {
		cfg := doctor.cfg
		report.Configuration = map[string]any{
			"state": "valid", "schema": cfg.Core.Version, "density": cfg.UI.Density,
			"palette": cfg.UI.Palette, "prompt_position": cfg.UI.PromptPosition,
			"surface_width": cfg.UI.Chatbox.SurfaceWidth, "output_viewport": cfg.UI.Chatbox.OutputViewport,
			"scrollback": cfg.UI.Chatbox.Scrollback,
		}
	} else if !errors.Is(doctor.configErr, os.ErrNotExist) {
		report.Configuration["state"] = "invalid-or-unreadable"
	}
	if doctor.shell == "zsh" || doctor.shell == "bash" || doctor.shell == "fish" {
		report.Shell["shell"] = doctor.shell
	}
	if doctor.startupAvailable {
		report.Shell["startup"] = "present"
		report.Shell["autostart"] = doctor.autostart
		report.Shell["prompt_conflicts"] = doctor.conflicts
	}
	report.Shell["hook"] = doctor.hookState
	report.Terminal["interactive_advertised"] = doctor.interactive
	report.Terminal["truecolor_advertised"] = doctor.truecolor
	report.Terminal["utf8_advertised"] = doctor.utf8
	if pending, err := historyRecoveryJournalPendingWithOffset(inspectHistoryRecoveryOffset); err == nil {
		report.History["recovery_pending"] = pending
	} else {
		report.History["recovery_state"] = "unavailable"
	}
	if path, err := scoring.DefaultHistoryPath(); err == nil {
		health, stats, err := scoring.InspectHistory(ctx, path)
		if err == nil {
			if health.QuickCheck == "ok" {
				report.History["database"] = "readable"
			} else {
				report.History["database"] = "integrity-failed"
			}
			report.History["events"] = stats.Events
			report.History["unfinished"] = stats.Unfinished
			if schema, err := strconv.Atoi(stats.SchemaVersion); err == nil && schema >= 0 && schema <= 100 {
				report.History["schema"] = schema
			}
			private := true
			for _, mode := range health.FileModes {
				private = private && mode.Perm()&0o077 == 0
			}
			report.History["permissions_private"] = private
		}
	}
	if path, err := historyPersistenceFailurePath(); err == nil {
		if data, err := readDiagnosticFile(path); err == nil {
			if count, valid, err := scanHistoryPersistenceFailureCount(bytes.NewReader(data)); err == nil && valid {
				report.History["persistence_failures"] = count
				report.History["persistence_failures_state"] = "recorded"
			} else {
				report.History["persistence_failures_state"] = "unavailable"
			}
		} else if errors.Is(err, os.ErrNotExist) {
			report.History["persistence_failures_state"] = "not-recorded"
		} else {
			report.History["persistence_failures_state"] = "unavailable"
		}
	}
	report.Latency.State = "unavailable"
	report.Latency.Phases = make(map[string]diagnosticLatencyPhase)
	if snapshot, err := loadDiagnosticLatency(); err == nil {
		if observed := snapshot.Health; observed != nil {
			report.Health["state"] = "recorded"
			switch observed.History {
			case "unknown", "canonical", "journal", "unrecorded", "outcome-unrecorded", "unavailable", "recovering", "recovery-error":
				report.Health["history"] = observed.History
			}
			report.Health["recovery_pending"] = observed.RecoveryPending
			report.Health["retention_deferred"] = observed.RetentionDeferred
			report.Health["lost_submissions"] = observed.LostSubmissions
			report.Health["lost_outcomes"] = observed.LostOutcomes
			providers := make(map[string]any)
			for source := health.Files; source < health.SourceCount; source++ {
				provider := observed.Providers[source]
				var outcome string
				switch provider.Outcome {
				case health.Healthy:
					outcome = "healthy"
				case health.Slow:
					outcome = "slow"
				case health.Failed:
					outcome = "failed"
				case health.TimedOut:
					outcome = "timed-out"
				default:
					continue
				}
				age := "unknown"
				if !provider.ObservedAt.IsZero() {
					if time.Since(provider.ObservedAt) < healthProviderFreshness {
						age = "recent"
					} else {
						age = "stale"
					}
				}
				providers[source.Name()] = map[string]any{"last_outcome": outcome, "observation_age": age, "failures": provider.Failures, "timeouts": provider.Timeouts, "slow": provider.Slow}
			}
			report.Health["providers"] = providers
		}
		report.Latency.State = "recorded"
		report.Latency.Timeouts = snapshot.Timeouts
		for _, phase := range []string{"first-paint", "first-suggestion", "full-enrichment", "enrichment", "source-history", "source-spec", "source-signals", "source-rank", "source-arguments", "source-recovery", "source-workspace", "source-local-sources", "source-fast-spec", "source-fast-history", "source-fast-rank"} {
			if samples := snapshot.SamplesUS[phase]; len(samples) > 0 && len(samples) <= latencySampleLimit {
				valid := true
				for _, sample := range samples {
					valid = valid && sample >= 0 && sample <= int64(time.Hour/time.Microsecond)
				}
				if valid {
					report.Latency.Phases[phase] = diagnosticLatencyPhase{len(samples), latencyPercentile(samples, .5).Microseconds(), latencyPercentile(samples, .95).Microseconds()}
				}
			}
		}
		if obs := snapshot.Runtime; obs != nil {
			if obs.Width > 0 && obs.Width <= 5000 && obs.Height > 0 && obs.Height <= 5000 {
				report.Terminal["dimensions"] = "recorded"
				report.Terminal["width"], report.Terminal["height"] = obs.Width, obs.Height
			}
			switch obs.State {
			case "managed", "degraded", "unmanaged", "application", "suspended":
				report.Visual["state"] = "recorded"
				report.Visual["model_state"] = obs.State
			}
			report.Visual["model_recoveries"] = obs.ModelRecoveries
			report.Visual["backdrop_recoveries"] = obs.BackdropRecoveries
			report.Visual["visual_recoveries"] = obs.VisualRecoveries
		}
	}
	return report
}

func inspectHistoryRecoveryOffset() (int64, bool, error) {
	path, err := historyRecoveryOffsetPath()
	if err != nil {
		return 0, false, err
	}
	data, err := readDiagnosticFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return parseHistoryRecoveryOffset(data)
}

// Only bounded regular local files are read; FIFO/device input must not block a
// support report. Errors stay private and become finite report states.
func readDiagnosticFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "diagnostic input")
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > diagnosticFileLimit {
		return nil, fmt.Errorf("diagnostic input is nonregular or oversized")
	}
	data, err := io.ReadAll(io.LimitReader(f, diagnosticFileLimit+1))
	if len(data) > diagnosticFileLimit {
		return nil, fmt.Errorf("diagnostic input exceeded its bound")
	}
	return data, err
}

func loadDiagnosticLatency() (latencySnapshot, error) {
	dir, err := config.CachePath()
	if err != nil {
		return latencySnapshot{}, err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return latencySnapshot{}, err
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 4096 {
		return latencySnapshot{}, errors.New("diagnostic cache unavailable or oversized")
	}
	path := filepath.Join(dir, "suggestion-latency.json")
	var latest time.Time
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "suggestion-latency-") || !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		if info, err := entry.Info(); err == nil && (info.ModTime().After(latest) || info.ModTime().Equal(latest) && entry.Name() < filepath.Base(path)) {
			path, latest = filepath.Join(dir, entry.Name()), info.ModTime()
		}
	}
	data, err := readDiagnosticFile(path)
	if err != nil {
		return latencySnapshot{}, err
	}
	var snapshot latencySnapshot
	err = json.Unmarshal(data, &snapshot)
	return snapshot, err
}
