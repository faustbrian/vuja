package scoring

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

type historyExecutionEvidence struct {
	command, cwd string
	startedAt    time.Time
	duration     time.Duration
	exitCode     sql.NullInt64
}

func historyEvidence(event HistoryEvent, opaque bool) historyExecutionEvidence {
	command := strings.TrimSpace(event.NormalizedCommand)
	if command == "" {
		command = strings.TrimSpace(event.Command)
	}
	cwd := strings.TrimSpace(event.Cwd)
	if opaque {
		command = historyEvidenceHash("command", command)
		if cwd != "" {
			cwd = historyEvidenceHash("directory", cwd)
		}
	}
	return historyExecutionEvidence{command: command, cwd: cwd, startedAt: event.StartedAt,
		duration: event.Duration, exitCode: sql.NullInt64{Int64: int64(event.ExitCode), Valid: event.HasExitCode}}
}

func historyEvidenceHash(kind, value string) string {
	sum := sha256.Sum256([]byte("native-counterpart\x00" + kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// SameHistoryExecution owns the metadata compatibility rule shared by optional
// adapters and durable forgotten-counterpart matching. Missing timestamps are
// not evidence of an execution match; missing directory/outcome/duration may
// legitimately come from lower-fidelity shell history.
func SameHistoryExecution(left, right HistoryEvent) bool {
	return compatibleHistoryEvidence(historyEvidence(left, false), historyEvidence(right, false))
}

func compatibleHistoryEvidence(left, right historyExecutionEvidence) bool {
	if left.command != right.command || left.cwd != "" && right.cwd != "" && left.cwd != right.cwd ||
		left.startedAt.IsZero() || right.startedAt.IsZero() || historyTimeDistance(left.startedAt, right.startedAt) > time.Second {
		return false
	}
	if left.exitCode.Valid && right.exitCode.Valid && left.exitCode.Int64 != right.exitCode.Int64 {
		return false
	}
	if left.duration <= 0 || right.duration <= 0 {
		return true
	}
	if left.duration > right.duration {
		return left.duration-right.duration <= time.Second
	}
	return right.duration-left.duration <= time.Second
}

func historyTimeDistance(left, right time.Time) time.Duration {
	if left.After(right) {
		return left.Sub(right)
	}
	return right.Sub(left)
}

func retainForgottenHistoryCounterpart(ctx context.Context, tx *sql.Tx, event HistoryEvent) error {
	// A representative timestamp cannot identify one external execution.
	// Exact event/provenance exclusions are retained by the forgetting owner.
	slot, eligible := singleExecutionCounterpartSlot(event)
	if !eligible {
		return nil
	}
	evidence := slot.historyExecutionEvidence
	var exitCode any
	if evidence.exitCode.Valid {
		exitCode = evidence.exitCode.Int64
	}
	var sourceHash string
	if event.Imported {
		sourceHash = historyEvidenceHash("source", event.Source)
	}
	// Existing rows are native slots (empty origin). Imported targets use
	// exact provenance in their own source and approximate evidence only
	// across sources, never to remove a distinct same-source repetition.
	_, err := tx.ExecContext(ctx, `INSERT INTO forgotten_native_history
        (identity_hash, command_hash, cwd_hash, started_at, duration_ns, exit_code, origin_source_hash, forgotten_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(identity_hash) DO NOTHING`,
		slot.identity, evidence.command, evidence.cwd,
		canonicalTimestamp(evidence.startedAt), evidence.duration.Nanoseconds(), exitCode, sourceHash, canonicalTimestamp(time.Now()))
	return err
}

type historyCounterpartSlot struct {
	historyExecutionEvidence
	identity         string
	originSourceHash string
	forgotten        bool
	preferred        bool
	nativeCutoff     time.Time
	sources          map[string]historyCounterpartSource
}

// All event-backed slot producers share this boundary. A compact record may
// remain a candidate (with conservative forgotten-scope refusal), but cannot
// claim or reserve the identity of any individual external execution.
func singleExecutionCounterpartSlot(event HistoryEvent) (historyCounterpartSlot, bool) {
	if historyEventIsAggregate(event) {
		return historyCounterpartSlot{}, false
	}
	return historyCounterpartSlot{historyExecutionEvidence: historyEvidence(event, true),
		identity: forgottenHistoryIdentity(event.EventKey)}, true
}

type historyCounterpartCandidate struct {
	slot, event int
	indices     []int
	distance    time.Duration
	startedAt   time.Time
	identity    string
	eventKey    string
}

type historyCounterpartCandidates []historyCounterpartCandidate

func (c historyCounterpartCandidates) Len() int { return len(c) }
func (c historyCounterpartCandidates) Less(i, j int) bool {
	left, right := c[i], c[j]
	if left.distance != right.distance {
		return left.distance < right.distance
	}
	if !left.startedAt.Equal(right.startedAt) {
		return left.startedAt.Before(right.startedAt)
	}
	if left.identity != right.identity {
		return left.identity < right.identity
	}
	return left.eventKey < right.eventKey
}
func (c historyCounterpartCandidates) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c *historyCounterpartCandidates) Push(value any) {
	candidate, ok := value.(historyCounterpartCandidate)
	if !ok {
		panic("history counterpart heap requires a candidate")
	}
	*c = append(*c, candidate)
}
func (c *historyCounterpartCandidates) Pop() any {
	old := *c
	value := old[len(old)-1]
	old[len(old)-1] = historyCounterpartCandidate{}
	*c = old[:len(old)-1]
	return value
}

// filterHistoryCounterparts runs inside the canonical import transaction, not
// against a stale root-side snapshot. Retained native executions and known
// independent imported repetitions reserve their own slots so a forgotten
// neighbor cannot swallow their external copy.
// External adapters prefer native records; a general native import preserves
// independent imported evidence while still honoring forgotten slots.
func filterHistoryCounterparts(ctx context.Context, tx *sql.Tx, events []HistoryEvent, preferNative bool) ([]HistoryEvent, error) {
	events, err := filterExcludedHistoryImports(ctx, tx, events)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT identity_hash, command_hash, cwd_hash, started_at, duration_ns, exit_code, origin_source_hash
        FROM forgotten_native_history ORDER BY started_at, identity_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slots []historyCounterpartSlot
	importedOrigins := make(map[string]bool)
	for rows.Next() {
		slot := historyCounterpartSlot{forgotten: true}
		var startedAt string
		if err := rows.Scan(&slot.identity, &slot.command, &slot.cwd, &startedAt, &slot.duration, &slot.exitCode, &slot.originSourceHash); err != nil {
			_ = rows.Close()
			return nil, err
		}
		slot.startedAt = parseKnownTimestamp(startedAt)
		if slot.originSourceHash != "" {
			importedOrigins[slot.originSourceHash] = true
		}
		slots = append(slots, slot)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(slots) == 0 && !preferNative {
		return events, nil
	}
	rows, err = tx.QueryContext(ctx, `SELECT event_key, command, normalized_command, cwd, CAST(started_at AS TEXT), duration_ns, exit_code, source, imported, occurrences, archive_owned, aggregate
        FROM history_events WHERE imported = 0 OR (archive_owned = 1 AND ?) OR ?`, preferNative, len(importedOrigins) > 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reserved := make(map[string]bool)
	reserve := func(event HistoryEvent, archiveOwned bool) {
		if reserved[event.EventKey] {
			return
		}
		var sourceHash string
		if event.Imported && !archiveOwned {
			sourceHash = historyEvidenceHash("source", event.Source)
			if !importedOrigins[sourceHash] {
				return
			}
		}
		slot, eligible := singleExecutionCounterpartSlot(event)
		if !eligible {
			return
		}
		reserved[event.EventKey] = true
		slot.originSourceHash, slot.preferred = sourceHash, !event.Imported || archiveOwned
		slots = append(slots, slot)
	}
	for rows.Next() {
		var event HistoryEvent
		var startedAt string
		var exitCode sql.NullInt64
		var archiveOwned bool
		if err := rows.Scan(&event.EventKey, &event.Command, &event.NormalizedCommand, &event.Cwd, &startedAt, &event.Duration, &exitCode, &event.Source, &event.Imported, &event.Occurrences, &archiveOwned, &event.Aggregate); err != nil {
			_ = rows.Close()
			return nil, err
		}
		event.StartedAt = parseKnownTimestamp(startedAt)
		event.HasExitCode, event.ExitCode = exitCode.Valid, int(exitCode.Int64)
		reserve(event, preferNative && archiveOwned)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, event := range events {
		// Replacement inputs are all external, even when their caller has
		// not yet set the canonical Imported flag.
		event.Imported = true
		reserve(event, false)
	}
	if err := loadHistoryCounterpartSources(ctx, tx, slots); err != nil {
		return nil, err
	}
	eventSources, err := historyEventCounterpartSources(ctx, tx, events)
	if err != nil {
		return nil, err
	}
	kept, matches, err := matchHistoryCounterpartSlots(ctx, slots, events, preferNative, false, eventSources)
	if err != nil {
		return nil, err
	}
	if err := recordHistoryCounterpartSources(ctx, tx, slots, events, matches); err != nil {
		return nil, err
	}
	return kept, nil
}

// Both snapshot imports and incremental native admission use the same nearest,
// one-to-one assignment. A native cutoff prevents a new execution from being
// mistaken for a delayed representation of an earlier forgotten execution.
// Native reconciliation has a single identity budget across migration cohorts;
// external snapshots may contribute one counterpart from each independent source.
// The caller selects this budget, not Imported on unnormalized adapter inputs.
func matchHistoryCounterpartSlots(ctx context.Context, slots []historyCounterpartSlot, events []HistoryEvent, dropPreferred, singleMatchPerSlot bool, eventSources map[string]map[string]historyCounterpartSource) ([]HistoryEvent, map[int]int, error) {
	sort.Slice(slots, func(i, j int) bool {
		if !slots[i].startedAt.Equal(slots[j].startedAt) {
			return slots[i].startedAt.Before(slots[j].startedAt)
		}
		return slots[i].identity < slots[j].identity
	})
	// Command/source buckets and a timestamp window avoid comparing every
	// external execution with every retained native execution.
	buckets := make(map[string]map[string][]int)
	evidence := make([]historyExecutionEvidence, len(events))
	for index, event := range events {
		evidence[index] = historyEvidence(event, true)
		if evidence[index].startedAt.IsZero() {
			continue
		}
		bySource := buckets[evidence[index].command]
		if bySource == nil {
			bySource = make(map[string][]int)
			buckets[evidence[index].command] = bySource
		}
		bySource[event.Source] = append(bySource[event.Source], index)
	}
	for _, bySource := range buckets {
		for _, indices := range bySource {
			sort.Slice(indices, func(i, j int) bool {
				left, right := indices[i], indices[j]
				if !evidence[left].startedAt.Equal(evidence[right].startedAt) {
					return evidence[left].startedAt.Before(evidence[right].startedAt)
				}
				return events[left].EventKey < events[right].EventKey
			})
		}
	}
	used, drop := make([]bool, len(events)), make([]bool, len(events))
	matchedSlots := make([]bool, len(slots))
	matched := make(map[int]int)
	eligible := func(slot historyCounterpartSlot, index int) bool {
		// A representative is not an individual alias or a consumed source
		// identity. Skip it for retained/preferred owners, but leave it visible
		// to forgotten scope so ambiguous groups cause an atomic refusal.
		if !slot.forgotten && historyEventIsAggregate(events[index]) {
			return false
		}
		if used[index] || !compatibleHistoryEvidence(evidence[index], slot.historyExecutionEvidence) {
			return false
		}
		if member, exists := slot.sources[historyEvidenceHash("source", events[index].Source)]; exists &&
			member.identity != forgottenHistoryIdentity(events[index].EventKey) && member.provenance != historyCounterpartProvenance(events[index]) {
			return false
		}
		for source, member := range eventSources[events[index].EventKey] {
			if owned, exists := slot.sources[source]; exists && owned.identity != member.identity && owned.provenance != member.provenance {
				return false // Observed distinct members cannot be merged by timing.
			}
		}
		submittedAt := events[index].SubmittedAt
		if submittedAt.IsZero() {
			submittedAt = events[index].StartedAt
		}
		return slot.nativeCutoff.IsZero() || !submittedAt.After(slot.nativeCutoff)
	}
	nearest := func(slotIndex int, indices []int) (historyCounterpartCandidate, bool) {
		slot := slots[slotIndex]
		lower, upper := slot.startedAt.Add(-time.Second), slot.startedAt.Add(time.Second)
		start := sort.Search(len(indices), func(i int) bool { return !evidence[indices[i]].startedAt.Before(lower) })
		best := -1
		for _, index := range indices[start:] {
			if evidence[index].startedAt.After(upper) {
				break
			}
			if !eligible(slot, index) {
				continue
			}
			if best == -1 || historyTimeDistance(evidence[index].startedAt, slot.startedAt) < historyTimeDistance(evidence[best].startedAt, slot.startedAt) {
				best = index
			}
		}
		if best < 0 {
			return historyCounterpartCandidate{}, false
		}
		return historyCounterpartCandidate{slot: slotIndex, event: best, indices: indices,
			distance:  historyTimeDistance(evidence[best].startedAt, slot.startedAt),
			startedAt: slot.startedAt, identity: slot.identity, eventKey: events[best].EventKey}, true
	}
	// Give stronger timing evidence first refusal across all native slots.
	// Chronological iteration alone lets an older approximate match reserve
	// an exact forgotten copy in a partial snapshot. Keep only one queued
	// candidate per slot/source rather than constructing every matching pair.
	var candidates historyCounterpartCandidates
	for slotIndex, slot := range slots {
		if slot.startedAt.IsZero() {
			continue
		}
		for source, indices := range buckets[slot.command] {
			if slot.originSourceHash != "" && slot.originSourceHash == historyEvidenceHash("source", source) {
				continue
			}
			if candidate, ok := nearest(slotIndex, indices); ok {
				candidates = append(candidates, candidate)
			}
		}
	}
	heap.Init(&candidates)
	for candidates.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		candidate := candidates[0]
		heap.Pop(&candidates)
		if singleMatchPerSlot && matchedSlots[candidate.slot] {
			continue
		}
		if !eligible(slots[candidate.slot], candidate.event) {
			if next, ok := nearest(candidate.slot, candidate.indices); ok {
				heap.Push(&candidates, next)
			}
			continue
		}
		if historyEventIsAggregate(events[candidate.event]) {
			return nil, nil, ErrHistoryCompactCounterpart
		}
		used[candidate.event] = true
		matchedSlots[candidate.slot] = true
		slot := &slots[candidate.slot]
		if slot.sources == nil {
			slot.sources = make(map[string]historyCounterpartSource)
		}
		// Pairing with a richer representative also consumes its already-known
		// source members. Revalidate queued candidates against this merged owner.
		for source, member := range eventSources[events[candidate.event].EventKey] {
			if _, exists := slot.sources[source]; !exists {
				member.persisted = false
				slot.sources[source] = member
			}
		}
		event := events[candidate.event]
		source := historyEvidenceHash("source", event.Source)
		member := historyCounterpartSource{identity: forgottenHistoryIdentity(event.EventKey), provenance: historyCounterpartProvenance(event)}
		if previous, exists := slot.sources[source]; exists && previous.identity == member.identity && previous.provenance == member.provenance {
			member.persisted = previous.persisted
		}
		slot.sources[source] = member
		drop[candidate.event] = slot.forgotten || dropPreferred && slot.preferred
		matched[candidate.event] = candidate.slot
	}
	kept := make([]HistoryEvent, 0, len(events))
	for index, event := range events {
		if !drop[index] {
			kept = append(kept, event)
		}
	}
	return kept, matched, nil
}

// preferExternalHistoryEvents gives richer Atuin records one lower-fidelity
// counterpart per source, using the same nearest assignment as durable native
// and forgotten counterparts. Matching must precede dropping any shell row:
// chronological first-fit can consume an older execution's only representation.
func preferExternalHistoryEvents(ctx context.Context, tx *sql.Tx, events []HistoryEvent, drop bool) ([]HistoryEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events = normalizeImportedHistoryEvents(events)
	var preferred, candidates []HistoryEvent
	var slots []historyCounterpartSlot
	for _, event := range events {
		if event.Source != "atuin" {
			candidates = append(candidates, event)
			continue
		}
		preferred = append(preferred, event)
		if slot, eligible := singleExecutionCounterpartSlot(event); eligible {
			slot.originSourceHash, slot.preferred = historyEvidenceHash("source", event.Source), true
			slots = append(slots, slot)
		}
	}
	if len(preferred) == 0 {
		return events, nil
	}
	if err := loadHistoryCounterpartSources(ctx, tx, slots); err != nil {
		return nil, err
	}
	eventSources, err := historyEventCounterpartSources(ctx, tx, candidates)
	if err != nil {
		return nil, err
	}
	kept, matches, err := matchHistoryCounterpartSlots(ctx, slots, candidates, true, false, eventSources)
	if err != nil {
		return nil, err
	}
	if err := recordHistoryCounterpartSources(ctx, tx, slots, candidates, matches); err != nil {
		return nil, err
	}
	if !drop {
		return events, nil
	}
	return append(preferred, kept...), nil
}

// Imported-target exclusions also own at most one native identity. Binding that
// alias in the existing exclusion authority prevents sequential replay calls
// from consuming independent nearby native repetitions. Existing imported
// repeats reserve their own stronger matches before an excluded slot can win.
// The caller holds SQLite writer ownership; alias binding, canonical removal
// and rebuilding derived evidence must commit together.
func reconcileForgottenNativeCounterparts(ctx context.Context, tx *sql.Tx, incoming *HistoryEvent, selected []string, preserveLearning func() error) (bool, int64, error) {
	query := `SELECT identity_hash, command_hash, cwd_hash, started_at, duration_ns, exit_code, origin_source_hash, forgotten_at
FROM forgotten_native_history WHERE origin_source_hash <> '' AND native_identity_hash = ''`
	type lookup struct {
		query string
		args  []any
	}
	var lookups []lookup
	if incoming != nil {
		if incoming.StartedAt.IsZero() {
			return false, 0, nil
		}
		lookups = append(lookups, lookup{query + ` AND command_hash = ? AND started_at >= ? AND started_at <= ?`,
			[]any{historyEvidence(*incoming, true).command,
				canonicalTimestamp(incoming.StartedAt.Add(-time.Second)), canonicalTimestamp(incoming.StartedAt.Add(time.Second))}})
	} else {
		for start := 0; start < len(selected); start += 128 {
			batch := selected[start:min(start+128, len(selected))]
			var args []any
			for _, identity := range batch {
				args = append(args, identity)
			}
			lookups = append(lookups, lookup{query + ` AND identity_hash IN (` + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + `)`, args})
		}
	}
	var slots []historyCounterpartSlot
	origins := make(map[string]bool)
	var first, last time.Time
	for _, lookup := range lookups {
		rows, err := tx.QueryContext(ctx, lookup.query, lookup.args...)
		if err != nil {
			return false, 0, err
		}
		defer rows.Close()
		for rows.Next() {
			slot := historyCounterpartSlot{forgotten: true}
			var startedAt, forgottenAt string
			if err := rows.Scan(&slot.identity, &slot.command, &slot.cwd, &startedAt, &slot.duration, &slot.exitCode, &slot.originSourceHash, &forgottenAt); err != nil {
				_ = rows.Close()
				return false, 0, err
			}
			slot.startedAt, slot.nativeCutoff = parseKnownTimestamp(startedAt), parseKnownTimestamp(forgottenAt)
			if slot.startedAt.IsZero() {
				continue
			}
			if first.IsZero() || slot.startedAt.Before(first) {
				first = slot.startedAt
			}
			if slot.startedAt.After(last) {
				last = slot.startedAt
			}
			origins[slot.originSourceHash] = true
			slots = append(slots, slot)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, 0, err
		}
		if err := rows.Close(); err != nil {
			return false, 0, err
		}
	}
	if len(slots) == 0 {
		return false, 0, nil
	}
	query = `SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported, aggregate
FROM history_events WHERE started_at >= ? AND started_at <= ?`
	arguments := []any{canonicalTimestamp(first.Add(-time.Second)), canonicalTimestamp(last.Add(time.Second))}
	if incoming != nil {
		query += ` AND normalized_command = ?`
		arguments = append(arguments, incoming.NormalizedCommand)
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return false, 0, err
	}
	defer rows.Close()
	stored, err := scanHistoryEvents(rows)
	closeErr := rows.Close()
	if err != nil {
		return false, 0, err
	}
	if closeErr != nil {
		return false, 0, closeErr
	}
	var natives []HistoryEvent
	seen := make(map[string]bool)
	for _, event := range stored {
		if !event.Imported {
			natives = append(natives, event)
			seen[event.EventKey] = true
			continue
		}
		sourceHash := historyEvidenceHash("source", event.Source)
		if origins[sourceHash] {
			if slot, eligible := singleExecutionCounterpartSlot(event); eligible {
				slot.originSourceHash = sourceHash
				slots = append(slots, slot)
			}
		}
	}
	if incoming != nil && !seen[incoming.EventKey] {
		natives = append(natives, *incoming)
	}
	if err := loadHistoryCounterpartSources(ctx, tx, slots); err != nil {
		return false, 0, err
	}
	eventSources, err := historyEventCounterpartSources(ctx, tx, natives)
	if err != nil {
		return false, 0, err
	}
	_, matches, err := matchHistoryCounterpartSlots(ctx, slots, natives, false, true, eventSources)
	if err != nil {
		return false, 0, err
	}
	for index, slotIndex := range matches {
		if slots[slotIndex].forgotten && seen[natives[index].EventKey] {
			if err := preserveLearning(); err != nil {
				return false, 0, err
			}
			break
		}
	}
	if err := recordHistoryCounterpartSources(ctx, tx, slots, natives, matches); err != nil {
		return false, 0, err
	}
	var incomingForgotten bool
	var removed int64
	for index, slotIndex := range matches {
		slot, event := slots[slotIndex], natives[index]
		if !slot.forgotten {
			continue
		}
		if seen[event.EventKey] {
			if err := removeLegacyNavigationEvidence(ctx, tx, event.EventKey); err != nil {
				return false, 0, err
			}
			if err := rejectUnattributedHistoryFeedbackRemoval(ctx, tx, event.EventKey); err != nil {
				return false, 0, err
			}
		}
		identity := forgottenHistoryIdentity(event.EventKey)
		if _, err := tx.ExecContext(ctx, `UPDATE forgotten_native_history SET native_identity_hash = ? WHERE identity_hash = ?`, identity, slot.identity); err != nil {
			return false, 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_history_events (identity_hash) VALUES (?) ON CONFLICT DO NOTHING`, identity); err != nil {
			return false, 0, err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM history_events WHERE event_key = ? AND imported = 0`, event.EventKey)
		if err != nil {
			return false, 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, 0, err
		}
		removed += count
		incomingForgotten = incomingForgotten || incoming != nil && event.EventKey == incoming.EventKey
	}
	return incomingForgotten, removed, nil
}

// Native persistence can arrive after an adapter snapshot. Reconcile copies
// already in canonical storage using the same forgotten/retained slots and
// one-to-one evidence rule as future imports, before rebuilding projections.
// This is part of the caller's existing deletion transaction, not a refresh.
func reconcileForgottenHistoryCounterparts(ctx context.Context, tx *sql.Tx, preserveLearning func() error) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
        duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported, aggregate
        FROM history_events WHERE imported = 1 ORDER BY event_key`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	events, err := scanHistoryEvents(rows)
	closeErr := rows.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	kept, err := filterHistoryCounterparts(ctx, tx, events, false)
	if err != nil {
		return 0, err
	}
	retained := make(map[string]bool, len(kept))
	for _, event := range kept {
		retained[event.EventKey] = true
	}
	var removed int64
	for _, event := range events {
		if retained[event.EventKey] {
			continue
		}
		if err := preserveLearning(); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM history_events WHERE event_key = ? AND imported = 1`, event.EventKey)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += count
	}
	return removed, nil
}

// An already-forgotten ID/provenance must not occupy another forgotten native
// execution's matching slot. Query only this input's hashes in bounded batches;
// do not copy a lifetime's exclusion registry into every import request.
func filterExcludedHistoryImports(ctx context.Context, tx *sql.Tx, events []HistoryEvent) ([]HistoryEvent, error) {
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM forgotten_history_events)`).Scan(&present); err != nil {
		return nil, err
	}
	if !present || len(events) == 0 {
		return events, nil
	}
	const batchSize = 128
	kept := make([]HistoryEvent, 0, len(events))
	for start := 0; start < len(events); start += batchSize {
		batch := events[start:min(start+batchSize, len(events))]
		hashes := make([][3]string, len(batch))
		arguments := make([]any, 0, len(batch)*3)
		for index, event := range batch {
			hashes[index] = [3]string{forgottenHistoryIdentity(event.EventKey), forgottenImportedProvenance(event), historyCounterpartProvenance(event)}
			arguments = append(arguments, hashes[index][0], hashes[index][1], hashes[index][2])
		}
		query := `SELECT identity_hash, occurrences FROM forgotten_history_events WHERE identity_hash IN (` + strings.TrimSuffix(strings.Repeat("?,", len(arguments)), ",") + `)`
		rows, err := tx.QueryContext(ctx, query, arguments...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		excluded := make(map[string]int)
		for rows.Next() {
			var hash string
			var occurrences int
			if err := rows.Scan(&hash, &occurrences); err != nil {
				_ = rows.Close()
				return nil, err
			}
			excluded[hash] = max(occurrences, 1)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for index, event := range batch {
			confirmedOccurrences := max(excluded[hashes[index][0]], excluded[hashes[index][1]], excluded[hashes[index][2]])
			if confirmedOccurrences == 0 {
				kept = append(kept, event)
			} else if max(event.Occurrences, 1) > confirmedOccurrences {
				return nil, ErrHistoryCompactCounterpart
			}
		}
	}
	return kept, nil
}
