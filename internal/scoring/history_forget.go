package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
)

// ErrHistoryForgotten is a deliberate privacy rejection, not a persistence
// failure. Callers must not journal or publish the rejected execution.
var ErrHistoryForgotten = errors.New("history execution was forgotten")

// ErrHistoryUnattributedLearning is an atomic refusal of an unsafe removal,
// not a failed canonical write. No selected data or unrelated learning changes.
var ErrHistoryUnattributedLearning = errors.New("history removal not applied: legacy learning has no execution attribution; no data was removed")

// ErrHistoryCompactCounterpart refuses an implicit whole-row operation when
// legacy aggregation cannot identify which logical repetitions it represents.
var ErrHistoryCompactCounterpart = errors.New("history operation not applied: compact history counterpart contains unconfirmed occurrences; preview an exact command selection including the whole group")

// SQLite's default trim removes only ASCII spaces. Derive its explicit set
// from Go's whitespace table so persisted raw commands obey strings.TrimSpace,
// just like command selectors, pending journal entries and confirmation.
var historyForgetTrimCharacters = func() string {
	var characters strings.Builder
	for _, span := range unicode.White_Space.R16 {
		for point := uint32(span.Lo); point <= uint32(span.Hi); point += uint32(span.Stride) {
			characters.WriteRune(rune(point))
		}
	}
	for _, span := range unicode.White_Space.R32 {
		for point := span.Lo; point <= span.Hi; point += span.Stride {
			characters.WriteRune(rune(point))
		}
	}
	return characters.String()
}()

func forgottenHistoryIdentity(eventKey string) string {
	sum := sha256.Sum256([]byte("event-key\x00" + strings.TrimSpace(eventKey)))
	return hex.EncodeToString(sum[:])
}

func forgottenImportedProvenance(event HistoryEvent) string {
	command := strings.TrimSpace(event.NormalizedCommand)
	if command == "" {
		command = strings.TrimSpace(event.Command)
	}
	// Position-derived shell IDs are not stable under file rotation. This
	// opaque provenance key does not retain the forgotten command or path.
	sum := sha256.Sum256([]byte("imported-execution\x00" + strings.TrimSpace(event.Source) + "\x00" + command + "\x00" + strings.TrimSpace(event.Cwd) + "\x00" + canonicalTimestamp(event.StartedAt)))
	return hex.EncodeToString(sum[:])
}

func historyIdentityForgotten(ctx context.Context, tx *sql.Tx, eventKey string) (bool, error) {
	var forgotten bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM forgotten_history_events WHERE identity_hash = ?)`, forgottenHistoryIdentity(eventKey)).Scan(&forgotten)
	return forgotten, err
}

// HistoryEventForgotten checks the durable exclusion shared by all sessions.
// It distinguishes deliberately discarded recovery from an absent submission.
func (f *FrecencyStore) HistoryEventForgotten(ctx context.Context, eventKey string) (bool, error) {
	if f == nil {
		return false, errors.New("history store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var forgotten bool
	err := f.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM forgotten_history_events WHERE identity_hash = ?)`, forgottenHistoryIdentity(eventKey)).Scan(&forgotten)
	return forgotten, err
}

// QueryHistoryForgetMatches previews a literal identity or exact recorded
// command, never a pattern, prefix, SQL fragment or normalized alias expansion.
func (f *FrecencyStore) QueryHistoryForgetMatches(ctx context.Context, eventKey, command, cwd string) ([]HistoryEvent, error) {
	if f == nil {
		return nil, errors.New("history store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	eventKey, command = strings.TrimSpace(eventKey), strings.TrimSpace(command)
	if (eventKey == "") == (command == "") || eventKey != "" && cwd != "" {
		return nil, errors.New("select exactly one execution ID or exact command with an optional directory")
	}
	where := "event_key = ?"
	arguments := []any{eventKey}
	if command != "" {
		where = "trim(command, ?) = ?"
		arguments = []any{historyForgetTrimCharacters, command}
		if cwd != "" {
			where += " AND cwd = ?"
			arguments = append(arguments, cwd)
		}
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
       duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported, aggregate
FROM history_events WHERE `+where+` ORDER BY event_key ASC`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistoryEvents(rows)
}

// ForgetHistoryEvents removes only existing, explicitly selected identities.
// The caller owns preview/confirmation. Exclusions, canonical removal, derived
// projections and the cross-session reset commit together or not at all.
// Attributed feedback follows its execution. Unattributed native feedback cannot
// be divided accurately by execution: refuse instead of silently resetting
// learning belonging to unselected executions. Imported targets do not own it.
func (f *FrecencyStore) ForgetHistoryEvents(ctx context.Context, eventKeys []string) (int64, error) {
	return f.forgetHistoryEvents(ctx, eventKeys, nil, nil)
}

// ForgetPendingHistoryEvents additionally accepts executions observed in the
// caller's private recovery journal. It records their exclusions without
// inserting command text into canonical history just to delete it. Callers
// must preview and confirm that exact selection. Arbitrary absent keys passed
// to ForgetHistoryEvents retain their existing no-match semantics.
func (f *FrecencyStore) ForgetPendingHistoryEvents(ctx context.Context, eventKeys []string, pending []HistoryEvent) (int64, error) {
	byKey, err := pendingHistoryForgetTargets(pending)
	if err != nil {
		return 0, err
	}
	return f.forgetHistoryEvents(ctx, eventKeys, byKey, nil)
}

// ForgetConfirmedHistoryEvents binds every canonical and pending target to
// the user's preview, checked under the same writer transaction as deletion.
// Completion-only fields may change; identity and logical occurrence scope
// may not. Pending identities are accepted only from the previewed journal.
func (f *FrecencyStore) ForgetConfirmedHistoryEvents(ctx context.Context, confirmed, pending []HistoryEvent) (int64, error) {
	byKey, err := pendingHistoryForgetTargets(pending)
	if err != nil {
		return 0, err
	}
	expected := make(map[string]HistoryEvent, len(confirmed))
	keys := make([]string, 0, len(confirmed))
	for _, event := range confirmed {
		if event.EventKey == "" || strings.TrimSpace(event.Command) == "" {
			return 0, errors.New("confirmation must contain execution identities")
		}
		if previous, exists := expected[event.EventKey]; exists {
			if !sameHistoryForgetTarget(previous, event) {
				return 0, errors.New("confirmation contains conflicting identities")
			}
			continue
		}
		expected[event.EventKey] = event
		keys = append(keys, event.EventKey)
	}
	for key, event := range byKey {
		selected, exists := expected[key]
		if !exists || !sameHistoryForgetTarget(selected, event) {
			return 0, errors.New("pending selection differs from confirmation")
		}
	}
	return f.forgetHistoryEvents(ctx, keys, byKey, expected)
}

func pendingHistoryForgetTargets(pending []HistoryEvent) (map[string]HistoryEvent, error) {
	byKey := make(map[string]HistoryEvent, len(pending))
	for _, event := range pending {
		if strings.TrimSpace(event.EventKey) == "" || event.Imported || strings.TrimSpace(event.Source) != "vuja" || strings.TrimSpace(event.Command) == "" {
			return nil, errors.New("pending selection must contain native execution identities")
		}
		if previous, exists := byKey[event.EventKey]; exists && !sameHistoryForgetTarget(previous, event) {
			return nil, errors.New("pending selection contains conflicting identities")
		}
		byKey[event.EventKey] = event
	}
	return byKey, nil
}

func sameHistoryForgetTarget(a, b HistoryEvent) bool {
	return strings.TrimSpace(a.Command) == strings.TrimSpace(b.Command) && a.Cwd == b.Cwd &&
		a.Source == b.Source && a.StartedAt.Equal(b.StartedAt) && a.Imported == b.Imported &&
		max(a.Occurrences, 1) == max(b.Occurrences, 1) && historyEventIsAggregate(a) == historyEventIsAggregate(b)
}

func (f *FrecencyStore) forgetHistoryEvents(ctx context.Context, eventKeys []string, pending, confirmed map[string]HistoryEvent) (int64, error) {
	if f == nil {
		return 0, errors.New("history store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(eventKeys) == 0 {
		return 0, nil
	}
	if err := f.acquireWrite(ctx); err != nil {
		return 0, err
	}
	defer f.releaseWrite()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// Obtain cross-process writer ownership before reading the selected set.
	// Another client cannot replay/replace a target between selection and its
	// durable exclusion within this transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value = value WHERE key = 'history_events_schema_version'`); err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(eventKeys))
	type target struct {
		event     HistoryEvent
		startedAt string
		pending   bool
	}
	var targets []target
	groups := make(map[string][]target)
	for _, eventKey := range eventKeys {
		eventKey = strings.TrimSpace(eventKey)
		if eventKey == "" || seen[eventKey] {
			continue
		}
		seen[eventKey] = true
		selected := target{event: HistoryEvent{EventKey: eventKey}}
		var exitCode sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT command, normalized_command, cwd, source, CAST(started_at AS TEXT), imported, duration_ns, exit_code, occurrences, aggregate FROM history_events WHERE event_key = ?`, eventKey).Scan(&selected.event.Command, &selected.event.NormalizedCommand, &selected.event.Cwd, &selected.event.Source, &selected.startedAt, &selected.event.Imported, &selected.event.Duration, &exitCode, &selected.event.Occurrences, &selected.event.Aggregate); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				journalEvent, observed := pending[eventKey]
				if !observed {
					if _, expected := confirmed[eventKey]; expected {
						forgotten, err := historyIdentityForgotten(ctx, tx, eventKey)
						if err != nil {
							return 0, err
						}
						if !forgotten {
							return 0, errors.New("execution disappeared during confirmation; preview again")
						}
					}
					continue
				}
				forgotten, err := historyIdentityForgotten(ctx, tx, eventKey)
				if err != nil {
					return 0, err
				}
				if forgotten {
					continue
				}
				selected.event = journalEvent
				selected.startedAt = canonicalTimestamp(journalEvent.StartedAt)
				selected.pending = true
				targets = append(targets, selected)
				continue
			}
			return 0, err
		}
		selected.event.StartedAt = parseKnownTimestamp(selected.startedAt)
		selected.event.HasExitCode, selected.event.ExitCode = exitCode.Valid, int(exitCode.Int64)
		if expected, observed := confirmed[eventKey]; observed && !sameHistoryForgetTarget(selected.event, expected) {
			return 0, errors.New("execution changed during confirmation; preview again")
		}
		if journalEvent, observed := pending[eventKey]; observed && !sameHistoryForgetTarget(selected.event, journalEvent) {
			return 0, errors.New("pending execution changed during confirmation; preview again")
		}
		targets = append(targets, selected)
		if selected.event.Imported {
			key := forgottenImportedProvenance(selected.event)
			groups[key] = append(groups[key], selected)
		}
	}
	// Imported files can omit timestamps or contain indistinguishable repeats.
	// Do not claim reliable per-execution forgetting of an ambiguous subset:
	// require an explicit selection of the whole provenance group instead.
	for _, group := range groups {
		selected := group[0]
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM history_events WHERE imported = 1 AND normalized_command = ? AND cwd = ? AND source = ? AND started_at = ?`, selected.event.NormalizedCommand, selected.event.Cwd, selected.event.Source, selected.startedAt).Scan(&count); err != nil {
			return 0, err
		}
		if count != len(group) {
			return 0, errors.New("imported executions have indistinguishable metadata; preview an exact command selection including the whole group")
		}
	}
	preserveLearning := newHistoryLearningGuard(ctx, tx)
	forgottenCounterpart := false
	for _, selected := range targets {
		event := selected.event
		if !selected.pending {
			if err := preserveLearning(); err != nil {
				return 0, err
			}
			if err := removeLegacyNavigationEvidence(ctx, tx, event.EventKey); err != nil {
				return 0, err
			}
			if err := rejectUnattributedHistoryFeedbackRemoval(ctx, tx, event.EventKey); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_history_events (identity_hash, occurrences) VALUES (?, ?)
ON CONFLICT(identity_hash) DO UPDATE SET occurrences = MAX(occurrences, excluded.occurrences)`, forgottenHistoryIdentity(event.EventKey), max(event.Occurrences, 1)); err != nil {
			return 0, err
		}
		if event.Imported {
			if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_history_events (identity_hash, occurrences) VALUES (?, ?)
ON CONFLICT(identity_hash) DO UPDATE SET occurrences = MAX(occurrences, excluded.occurrences)`, forgottenImportedProvenance(event), max(event.Occurrences, 1)); err != nil {
				return 0, err
			}
		}
		if !historyEventIsAggregate(event) {
			if err := excludeKnownHistoryCounterparts(ctx, tx, event.EventKey); err != nil {
				return 0, err
			}
			if err := retainForgottenHistoryCounterpart(ctx, tx, event); err != nil {
				return 0, err
			}
			forgottenCounterpart = true
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_events WHERE event_key = ?`, event.EventKey); err != nil {
			return 0, err
		}
	}
	removed := int64(len(targets))
	if removed == 0 {
		return 0, tx.Commit()
	}
	canonicalRemoved := false
	for _, selected := range targets {
		canonicalRemoved = canonicalRemoved || !selected.pending
	}
	if forgottenCounterpart {
		var importedTargets []string
		for _, selected := range targets {
			if selected.event.Imported && !historyEventIsAggregate(selected.event) {
				importedTargets = append(importedTargets, forgottenHistoryIdentity(selected.event.EventKey))
			}
		}
		_, nativeRemoved, err := reconcileForgottenNativeCounterparts(ctx, tx, nil, importedTargets, preserveLearning)
		if err != nil {
			return 0, err
		}
		canonicalRemoved = canonicalRemoved || nativeRemoved > 0
		counterpartsRemoved, err := reconcileForgottenHistoryCounterparts(ctx, tx, preserveLearning)
		if err != nil {
			return 0, err
		}
		canonicalRemoved = canonicalRemoved || counterpartsRemoved > 0
	}
	if canonicalRemoved {
		if err := rebuildHistoryProjectionsAfterRemoval(ctx, tx); err != nil {
			return 0, err
		}
	}
	if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// A matching legacy aggregate does not establish ownership by one execution.
// The writer transaction must reject the entire selection before committing
// exclusions, projections or learning changes; no bulk reset is implied.
func rejectUnattributedHistoryFeedbackRemoval(ctx context.Context, tx *sql.Tx, eventKey string) error {
	var unowned bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
            SELECT 1 FROM history_events AS execution
            JOIN legacy_suggestion_feedback AS legacy
              ON legacy.cmd = execution.normalized_command AND legacy.cwd = execution.cwd
            WHERE execution.event_key = ? AND execution.imported = 0
              AND NOT EXISTS (SELECT 1 FROM history_feedback_events WHERE event_key = execution.event_key)
        )`, eventKey).Scan(&unowned)
	if err != nil {
		return err
	}
	if unowned {
		return ErrHistoryUnattributedLearning
	}
	return nil
}

// Count retention and explicit forgetting must rebuild the same authoritative
// projections. Do not maintain a second interpretation of retained evidence.
// Mutation owners must run their learning guard before changing canonical rows;
// the guard itself also uses this raw rebuild inside its rolled-back probe.
func rebuildHistoryProjectionsAfterRemoval(ctx context.Context, tx *sql.Tx) error {
	return rebuildHistoryProjectionsAfterRemovalAt(ctx, tx, time.Now())
}

func rebuildHistoryProjectionsAfterRemovalAt(ctx context.Context, tx *sql.Tx, projectedAt time.Time) error {
	for _, statement := range []string{
		`DELETE FROM history_entries`,
		`INSERT INTO history_entries (cmd, cwd, count, last_used)
         SELECT normalized_command, cwd, SUM(occurrences), MAX(completed_at)
         FROM history_events
         WHERE imported = 0 AND state IN ('completed', 'failed')
         GROUP BY normalized_command, cwd`,
		`DELETE FROM command_outcomes`,
		`INSERT INTO command_outcomes (cmd, cwd, successes, failures, last_used)
         SELECT normalized_command, cwd,
                SUM(CASE WHEN state = 'completed' THEN occurrences ELSE 0 END),
                SUM(CASE WHEN state = 'failed' THEN occurrences ELSE 0 END),
                MAX(completed_at)
         FROM history_events
         WHERE imported = 0 AND state IN ('completed', 'failed')
         GROUP BY normalized_command, cwd`,
		`DELETE FROM imported_history_entries`,
		`INSERT INTO imported_history_entries
         (cmd, cwd, count, last_used, exit_code, duration_ns, source)
         WITH ranked AS (
             SELECT normalized_command, cwd, source, started_at, exit_code, duration_ns,
                    SUM(occurrences) OVER (PARTITION BY normalized_command, cwd, source) AS event_count,
                    MAX(started_at) OVER (PARTITION BY normalized_command, cwd, source) AS most_recent,
                    ROW_NUMBER() OVER (
                        PARTITION BY normalized_command, cwd, source
                        ORDER BY started_at DESC, history_order DESC, event_key DESC
                    ) AS recency_rank
             FROM history_events WHERE imported = 1
         )
         SELECT normalized_command, cwd, event_count, most_recent, exit_code, duration_ns, source
         FROM ranked WHERE recency_rank = 1`,
		`DELETE FROM imported_history`,
		`INSERT INTO imported_history (cmd, count, last_used)
         SELECT cmd, SUM(count), MAX(last_used) FROM imported_history_entries GROUP BY cmd`,
		`DELETE FROM command_transitions`,
		`DELETE FROM exact_command_transitions`,
		`DELETE FROM history_feedback_events
         WHERE NOT EXISTS (SELECT 1 FROM history_events WHERE event_key = history_feedback_events.event_key AND imported = 0)`,
		`DELETE FROM suggestion_feedback`,
		`INSERT INTO suggestion_feedback (cmd, cwd, accepted, typed, edited, dismissed, last_used)
         SELECT cmd, cwd, SUM(accepted), SUM(typed), SUM(edited), SUM(dismissed), MAX(last_used)
         FROM (
             SELECT cmd, cwd, accepted, typed, edited, dismissed, last_used FROM legacy_suggestion_feedback
             UNION ALL
             SELECT cmd, cwd,
                    CASE WHEN kind = 'accepted' THEN 1 ELSE 0 END,
                    CASE WHEN kind = 'typed' THEN 1 ELSE 0 END,
                    CASE WHEN kind = 'edited' THEN 1 ELSE 0 END,
                    CASE WHEN kind = 'dismissed' THEN 1 ELSE 0 END,
                    last_used
             FROM history_feedback_events
         ) GROUP BY cmd, cwd`,
		`DELETE FROM argument_values`,
		`DELETE FROM recent_failures`,
		`DELETE FROM metadata WHERE key = 'imported_history_events_fingerprint' OR key = 'imported_history_fingerprint'`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := rebuildRetainedCompletionSignals(ctx, tx); err != nil {
		return err
	}
	if err := rebuildRetainedTransitions(ctx, tx); err != nil {
		return err
	}
	return rebuildHistoryDirectorySource(ctx, tx, projectedAt)
}
