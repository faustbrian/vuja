package root

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/health"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

const (
	healthWarningInterval   = 30 * time.Second
	healthRecoveryDisplay   = 5 * time.Second
	healthProviderFreshness = time.Minute
	healthHistoryReadBudget = 100 * time.Millisecond
)

type healthBadge struct {
	Label     string
	Critical  bool
	Recovered bool
	Revision  uint64
}

type providerHealthSnapshot struct {
	Outcome    health.Outcome `json:"outcome"`
	ObservedAt time.Time      `json:"observed_at"`
	Failures   uint64         `json:"failures"`
	Timeouts   uint64         `json:"timeouts"`
	Slow       uint64         `json:"slow"`
}

type sessionHealthSnapshot struct {
	Revision          uint64                                     `json:"revision"`
	History           string                                     `json:"history"`
	RecoveryPending   bool                                       `json:"recovery_pending"`
	RetentionDeferred bool                                       `json:"retention_deferred"`
	LostSubmissions   uint64                                     `json:"lost_submissions"`
	LostOutcomes      uint64                                     `json:"lost_outcomes"`
	Providers         [health.SourceCount]providerHealthSnapshot `json:"providers"`
}

// Only the latest unresolved effect is retained, not command text or a second
// history ledger. The reference never enters telemetry or diagnostics.
type healthHistoryTarget struct {
	ID         string
	Completion bool
}

func historyHealthTarget(entry integration.HistoryEntry, completion bool) healthHistoryTarget {
	id := strings.TrimSpace(entry.ID)
	if len(id) == 0 || len(id) > 128 {
		return healthHistoryTarget{}
	}
	return healthHistoryTarget{ID: id, Completion: completion}
}

func (h *sessionHealthProjection) submissionObserver(entry integration.HistoryEntry) func(bool, bool) {
	target := historyHealthTarget(entry, false)
	return func(persisted, journaled bool) { h.observeHistoryWithTarget(persisted, journaled, target) }
}

func (h *sessionHealthProjection) completionObserver(entry integration.HistoryEntry) func(bool, bool) {
	target := historyHealthTarget(entry, true)
	return func(persisted, journaled bool) { h.observeCompletionWithTarget(persisted, journaled, target) }
}

func (h *sessionHealthProjection) recoveryObserver(store *scoring.FrecencyStore) func() func(bool, bool, error) {
	return func() func(bool, bool, error) { return h.beginRecoveryObservationWithStore(store) }
}

func (h *sessionHealthProjection) observeCompletion(persisted, journaled bool) {
	h.observeCompletionWithTarget(persisted, journaled, healthHistoryTarget{})
}

func (h *sessionHealthProjection) observeCompletionWithTarget(persisted, journaled bool, target healthHistoryTarget) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revision++
	h.historyRevision++
	previous := h.historyCondition()
	switch {
	case journaled:
		h.pending = true
		h.fallbackTarget = target
		if h.historyWrite != "unrecorded" {
			h.historyWrite = "journal"
		}
	case persisted:
		if h.historyWrite != "unrecorded" && h.historyWrite != "unavailable" {
			h.historyWrite = "canonical"
		}
	default:
		h.lostOutcomes++
		h.pendingOutcomeWarning = true
		if h.historyWrite != "unrecorded" {
			h.historyWrite = "outcome-unrecorded"
		}
	}
	h.historyRestored(previous)
}

// This projection contains only observed finite outcomes; canonical history,
// journals, models and providers remain the owners of their actual state.
type sessionHealthProjection struct {
	mu                                  sync.Mutex
	now                                 func() time.Time
	historyWrite                        string
	pending, recoveryFailed, visualSeen bool
	visual                              runtimeDiagnosticObservation
	providers                           [health.SourceCount]providerHealthSnapshot
	recoveredUntil                      [health.SourceCount]time.Time
	lastWarning                         [health.SourceCount]time.Time
	warningCritical                     [health.SourceCount]bool
	lastBadge                           healthBadge
	revision                            uint64
	historyRevision                     uint64
	lostSubmissions, lostOutcomes       uint64
	pendingSubmissionWarning            bool
	pendingOutcomeWarning               bool
	fallbackTarget                      healthHistoryTarget
	historyStore                        *scoring.FrecencyStore
	retentionDeferred                   bool
}

func newSessionHealthProjection(now func() time.Time) *sessionHealthProjection {
	if now == nil {
		now = time.Now
	}
	return &sessionHealthProjection{now: now, historyWrite: "unknown"}
}

func (h *sessionHealthProjection) historyCondition() string {
	if h.historyWrite == "unrecorded" || h.historyWrite == "outcome-unrecorded" || h.historyWrite == "unavailable" {
		return h.historyWrite
	}
	if h.recoveryFailed {
		return "recovery-error"
	}
	if h.pending {
		if h.historyWrite == "canonical" {
			return "recovering"
		}
		return "journal"
	}
	return h.historyWrite
}

func (h *sessionHealthProjection) historyRestored(previous string) {
	if h.historyCondition() != "canonical" {
		h.recoveredUntil[health.HistoryPersistence] = time.Time{}
		return
	}
	if previous != "canonical" && previous != "unknown" {
		h.recoveredUntil[health.HistoryPersistence] = h.now().Add(healthRecoveryDisplay)
	}
}

func (h *sessionHealthProjection) observeHistory(persisted, journaled bool) {
	h.observeHistoryWithTarget(persisted, journaled, healthHistoryTarget{})
}

func (h *sessionHealthProjection) observeHistoryWithTarget(persisted, journaled bool, target healthHistoryTarget) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revision++
	h.historyRevision++
	previous := h.historyCondition()
	switch {
	case journaled:
		h.historyWrite, h.pending = "journal", true
		h.fallbackTarget = target
	case persisted:
		h.historyWrite = "canonical"
	default:
		h.historyWrite = "unrecorded"
		h.lostSubmissions++
		h.pendingSubmissionWarning = true
	}
	h.historyRestored(previous)
}

func (h *sessionHealthProjection) observeHistoryUnavailable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revision++
	h.historyRevision++
	h.historyWrite = "unavailable"
	h.recoveredUntil[health.HistoryPersistence] = time.Time{}
}

func (h *sessionHealthProjection) observeRecovery(pending, recovered bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observeRecoveryLocked(pending, recovered, err)
}

func (h *sessionHealthProjection) observeRecoveryLocked(pending, recovered bool, err error) {
	h.revision++
	h.historyRevision++
	previous := h.historyCondition()
	if errors.Is(err, scoring.ErrHistoryUnattributedLearning) {
		// The journal is deliberately still pending, not failed or lost.
		// A policy refusal proves neither a recovery nor a failed write.
		err, recovered = nil, false
	}
	if err != nil {
		h.recoveryFailed = true
		h.historyRestored(previous)
		return
	}
	h.pending, h.recoveryFailed = pending, false
	if recovered {
		h.historyWrite = "canonical"
	} else if !pending && h.historyWrite == "journal" {
		// Queue emptiness can follow clear/retention or another session. It
		// removes an ongoing queue condition, not prove a canonical write.
		h.historyWrite = "unknown"
	}
	if !pending {
		h.fallbackTarget = healthHistoryTarget{}
	}
	h.historyRestored(previous)
}

func (h *sessionHealthProjection) beginRecoveryObservation() func(bool, bool, error) {
	return h.beginRecoveryObservationWithStore(nil)
}

func (h *sessionHealthProjection) beginRecoveryObservationWithStore(store *scoring.FrecencyStore) func(bool, bool, error) {
	h.mu.Lock()
	if store != nil {
		h.historyStore = store
	}
	revision := h.historyRevision
	h.mu.Unlock()
	return func(pending, recovered bool, err error) {
		h.mu.Lock()
		currentRevision, target := h.historyRevision, h.fallbackTarget
		needsConfirmation := store != nil && err == nil && target.ID != "" &&
			(h.historyWrite == "journal" || h.historyWrite == "canonical") &&
			(recovered || !pending && h.historyWrite == "journal")
		if !needsConfirmation {
			if currentRevision == revision {
				h.observeRecoveryLocked(pending, recovered, err)
			}
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()

		// Both facts are refreshed outside projection/render locks. The existing
		// canonical owner returns only presence/outcome, never command payload.
		freshPending, queueErr := historyRecoveryJournalPending()
		ctx, cancel := context.WithTimeout(context.Background(), healthHistoryReadBudget)
		confirmed, canonicalErr := store.HasCanonicalHistoryEffect(ctx, target.ID, target.Completion)
		cancel()
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.historyRevision != currentRevision || h.fallbackTarget != target {
			return
		}
		if queueErr != nil {
			h.observeRecoveryLocked(h.pending, false, queueErr)
		} else if canonicalErr != nil {
			h.pending = freshPending
			h.observeRecoveryLocked(freshPending, false, canonicalErr)
		} else {
			h.observeRecoveryLocked(freshPending, confirmed, nil)
		}
	}
}

func (h *sessionHealthProjection) setHistoryStore(store *scoring.FrecencyStore) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.historyStore = store
	h.refreshRetentionLocked()
}

func (h *sessionHealthProjection) refreshRetentionLocked() {
	deferred := h.historyStore.HistoryRetentionDeferred()
	if deferred != h.retentionDeferred {
		h.retentionDeferred = deferred
		h.revision++
	}
}

func (h *sessionHealthProjection) observeVisual(observation runtimeDiagnosticObservation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.visualSeen || h.visual != observation {
		h.revision++
	}
	previous, seen := h.visual, h.visualSeen
	h.visual, h.visualSeen = observation, true
	if seen && observation.State == "managed" && (previous.State == "degraded" || previous.ModelRecoveries < observation.ModelRecoveries || previous.BackdropRecoveries < observation.BackdropRecoveries || previous.VisualRecoveries < observation.VisualRecoveries) {
		h.recoveredUntil[health.Visual] = h.now().Add(healthRecoveryDisplay)
	}
}

func unhealthyProvider(outcome health.Outcome) bool {
	return outcome == health.Failed || outcome == health.TimedOut || outcome == health.Slow
}

func (h *sessionHealthProjection) observeProvider(ctx context.Context, observation health.Observation) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil || observation.Source < health.Files || observation.Source >= health.SourceCount || observation.Outcome == health.Skipped || observation.Outcome == health.Unknown {
		return false
	}
	previous := h.providers[observation.Source]
	if observation.At.IsZero() {
		observation.At = h.now()
	}
	if observation.At.Before(previous.ObservedAt) {
		return false
	}
	next := previous
	next.Outcome, next.ObservedAt = observation.Outcome, observation.At
	switch observation.Outcome {
	case health.Failed:
		next.Failures++
	case health.TimedOut:
		next.Timeouts++
	case health.Slow:
		next.Slow++
	case health.Healthy:
		if unhealthyProvider(previous.Outcome) {
			h.recoveredUntil[observation.Source] = h.now().Add(healthRecoveryDisplay)
		}
	default:
		return false
	}
	h.revision++
	h.providers[observation.Source] = next
	return true
}

func providerHealthLabel(source health.Source) string {
	labels := [...]string{"History", "Rendering", "Files", "Completion", "History suggestions", "Workspace", "Project suggestions", "Directories", "Arguments", "Recovery suggestions", "Ranking", "AI", "Npm scripts", "Make targets", "Just recipes",
		"Git commits", "Git branches", "Git remotes", "Git tags", "Git stashes", "Pip packages",
		"Pacman packages", "Yay packages", "Paru packages", "Debian packages", "RPM packages", "Brew packages",
		"Processes", "Docker containers", "Docker running containers", "Docker images", "Zoxide suggestions", "SSH hosts", "Git state"}
	return labels[source]
}

func (h *sessionHealthProjection) sourceBadge(source health.Source, now time.Time) healthBadge {
	switch source {
	case health.HistoryPersistence:
		if h.retentionDeferred {
			switch h.historyCondition() {
			case "unknown", "canonical", "recovering":
				return healthBadge{Label: "History retention not applied"}
			}
		}
		switch h.historyCondition() {
		case "unrecorded":
			return healthBadge{Label: "History not recorded", Critical: true}
		case "outcome-unrecorded":
			return healthBadge{Label: "History outcome not recorded", Critical: true}
		case "unavailable":
			return healthBadge{Label: "History unavailable", Critical: true}
		case "journal":
			return healthBadge{Label: "History journal"}
		case "recovering":
			return healthBadge{Label: "History recovering"}
		case "recovery-error":
			return healthBadge{Label: "History recovery failed"}
		}
	case health.Visual:
		if h.visual.State == "degraded" {
			return healthBadge{Label: "Rendering degraded", Critical: true}
		}
	default:
		observation := h.providers[source]
		if now.Sub(observation.ObservedAt) < healthProviderFreshness {
			switch observation.Outcome {
			case health.Failed:
				return healthBadge{Label: providerHealthLabel(source) + " failed"}
			case health.TimedOut:
				return healthBadge{Label: providerHealthLabel(source) + " timed out"}
			case health.Slow:
				return healthBadge{Label: providerHealthLabel(source) + " slow"}
			}
		}
	}
	if now.Before(h.recoveredUntil[source]) {
		label := providerHealthLabel(source) + " restored"
		if source == health.HistoryPersistence {
			label = "History recording restored"
		}
		return healthBadge{Label: label, Recovered: true}
	}
	return healthBadge{}
}

func badgePriority(badge healthBadge) int {
	if badge.Label == "" {
		return 0
	}
	if badge.Critical {
		return 3
	}
	if badge.Recovered {
		return 1
	}
	return 2
}

func (h *sessionHealthProjection) badgeLocked() (healthBadge, health.Source) {
	h.refreshRetentionLocked()
	var selected healthBadge
	var source health.Source
	now := h.now()
	for i := health.Source(0); i < health.SourceCount; i++ {
		candidate := h.sourceBadge(i, now)
		if badgePriority(candidate) > badgePriority(selected) {
			selected, source = candidate, i
		}
	}
	if selected.Label != h.lastBadge.Label || selected.Critical != h.lastBadge.Critical || selected.Recovered != h.lastBadge.Recovered {
		selected.Revision = h.lastBadge.Revision + 1
		h.lastBadge = selected
	} else {
		selected.Revision = h.lastBadge.Revision
	}
	return selected, source
}

func (h *sessionHealthProjection) badge() healthBadge {
	h.mu.Lock()
	defer h.mu.Unlock()
	badge, _ := h.badgeLocked()
	return badge
}

func (h *sessionHealthProjection) snapshot() sessionHealthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refreshRetentionLocked()
	return sessionHealthSnapshot{Revision: h.revision, History: h.historyCondition(), RecoveryPending: h.pending, RetentionDeferred: h.retentionDeferred, LostSubmissions: h.lostSubmissions, LostOutcomes: h.lostOutcomes, Providers: h.providers}
}

func (h *sessionHealthProjection) warning() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	badge, source := h.badgeLocked()
	// A recovered backend cannot undo a lost attempt. Retain one finite,
	// coalesced disclosure until a safe surface can emit it within the limit.
	lostSubmission, lostOutcome := h.pendingSubmissionWarning, h.pendingOutcomeWarning
	if lostSubmission || lostOutcome {
		badge, source = healthBadge{Label: "History loss", Critical: true}, health.HistoryPersistence
	}
	if badge.Label == "" || badge.Recovered {
		return ""
	}
	now := h.now()
	if !h.lastWarning[source].IsZero() && now.Sub(h.lastWarning[source]) < healthWarningInterval && (!badge.Critical || h.warningCritical[source]) {
		return ""
	}
	h.lastWarning[source], h.warningCritical[source] = now, badge.Critical
	if lostSubmission || lostOutcome {
		h.pendingSubmissionWarning, h.pendingOutcomeWarning = false, false
		switch {
		case lostSubmission && lostOutcome:
			return "[VUJA] history persistence failed; one or more commands and command outcomes were not recorded"
		case lostSubmission:
			return "[VUJA] history persistence failed; one or more commands were not recorded"
		default:
			return "[VUJA] history completion failed; one or more command outcomes were not recorded; submitted commands remain in history"
		}
	}
	switch source {
	case health.HistoryPersistence:
		if badge.Label == "History retention not applied" {
			return "[VUJA] history retention was not applied: older learning lacks execution attribution; command recording remains available; use vuja history doctor"
		}
		switch h.historyCondition() {
		case "unrecorded":
			return "[VUJA] history persistence failed; one or more commands were not recorded"
		case "outcome-unrecorded":
			return "[VUJA] history completion failed; one or more command outcomes were not recorded; submitted commands remain in history"
		case "unavailable":
			return "[VUJA] canonical history is unavailable; recording durability is not yet confirmed"
		case "journal":
			return "[VUJA] canonical history unavailable; submissions are secured in the recovery journal"
		case "recovering":
			return "[VUJA] canonical history is available; queued journal records are still recovering"
		default:
			return "[VUJA] history recovery could not be confirmed; use vuja debug diagnostics for a sanitized report"
		}
	default:
		return "[VUJA] " + badge.Label + "; use vuja debug diagnostics for a sanitized report"
	}
}

func (c *terminalCompositor) SetSessionHealth(badge healthBadge) {
	defer c.containVisualFailure()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || badge.Revision < c.sessionHealth.Revision {
		return
	}
	changed := badge.Label != c.sessionHealth.Label || badge.Critical != c.sessionHealth.Critical || badge.Recovered != c.sessionHealth.Recovered
	c.sessionHealth = badge
	c.statusSnapshot.Health = badge
	if !changed {
		return
	}
	c.statusLinesCache = nil
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	} else if c.phase == terminalOutput {
		c.renderCommandStatus()
	}
}

func appendSessionHealthSegment(segments []terminalStatusSegment, badge healthBadge) []terminalStatusSegment {
	if badge.Label == "" {
		return segments
	}
	name := "session-warning"
	if badge.Critical {
		name = "session-critical"
	} else if badge.Recovered {
		name = "exit-success"
	}
	return append(segments, terminalStatusSegment{name: name, text: badge.Label, priority: 120, alignment: terminalStatusLeft})
}

type requestTimeoutObserverKey struct{}

func recordRequestTimeout(ctx context.Context, recorder *latencyRecorder) {
	if ctx != nil {
		if ctx.Err() != nil {
			return
		}
		if observe, ok := ctx.Value(requestTimeoutObserverKey{}).(func()); ok {
			observe()
			return
		}
	}
	recorder.timeout()
}

func observedProviderContext(ctx context.Context, projection *sessionHealthProjection, recorder *latencyRecorder, current func() bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	var mu sync.Mutex
	var worst [health.SourceCount]health.Outcome
	// The summary counts affected requests, not overlapping child/group events.
	// Source-specific attempt counts remain in the finite health snapshot.
	timeoutRecorded := false
	countTimeout := func() {
		if !timeoutRecorded {
			timeoutRecorded = true
			recorder.timeout()
		}
	}
	observed := health.WithObserver(ctx, func(observation health.Observation) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil || current != nil && !current() || observation.Source >= health.SourceCount || observation.Outcome == health.Skipped {
			return
		}
		if observation.Outcome < worst[observation.Source] {
			return
		}
		if !projection.observeProvider(ctx, observation) {
			return
		}
		worst[observation.Source] = observation.Outcome
		recorder.recordHealth(projection.snapshot())
		if observation.Outcome == health.TimedOut {
			countTimeout()
		}
	})
	return context.WithValue(observed, requestTimeoutObserverKey{}, func() {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() == nil && (current == nil || current()) {
			countTimeout()
		}
	})
}

func (c *terminalCompositor) healthWarningSurfaceAvailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && (!c.enabled || c.phase == terminalInput || c.emulator == nil)
}
