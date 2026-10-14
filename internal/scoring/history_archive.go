package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/faustbrian/vuja/internal/policy"
)

const (
	MaxHistoryArchiveEvents = 100_000
	MaxHistoryArchiveBytes  = 32 << 20
)

type nativeArchiveOrder struct {
	session string
	order   int
}

// PrepareHistoryArchiveEvents defines the portable execution representation.
// Ownership by an archive is local storage metadata, never source provenance.
// Unfinished processes cannot travel: retain their identity/timing, mark them
// interrupted, and never manufacture completion evidence.
func PrepareHistoryArchiveEvents(events []HistoryEvent) ([]HistoryEvent, error) {
	if len(events) > MaxHistoryArchiveEvents {
		return nil, errors.New("history archive exceeds the execution-record limit")
	}
	prepared := make([]HistoryEvent, 0, len(events))
	seen := make(map[string]HistoryEvent, len(events))
	orders := make(map[nativeArchiveOrder]string)
	size := 0
	for _, event := range events {
		for _, value := range []string{event.EventKey, event.Command, event.NormalizedCommand, event.Cwd, event.Source, event.Host, event.SessionID, event.Shell} {
			size += len(value)
			if !utf8.ValidString(value) || size > MaxHistoryArchiveBytes {
				return nil, errors.New("history archive contains invalid or oversized text")
			}
		}
		for _, value := range []string{event.EventKey, event.Cwd, event.Source, event.Host, event.SessionID, event.Shell} {
			if len(value) > 4096 || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return nil, errors.New("history archive contains invalid execution metadata")
			}
		}
		command, allowed := policy.HistoryCommand(event.Command, false)
		if !allowed {
			return nil, errors.New("history archive rejected by history privacy policy")
		}
		if event.NormalizedCommand == "" {
			event.NormalizedCommand = command
		}
		normalized, allowed := policy.HistoryCommand(event.NormalizedCommand, false)
		if !allowed {
			return nil, errors.New("history archive rejected by history privacy policy")
		}
		event.NormalizedCommand = normalized
		event.Aggregate = historyEventIsAggregate(event)
		// Atuin's imported-history defaults use -1 for unknown exit/duration.
		// Older adapters persisted these sentinels literally. Canonicalize only
		// this source's portable representation, including same-ID comparison;
		// do not rewrite the destination's existing records or infer success.
		if event.Imported && event.Source == "atuin" {
			if event.Duration == -1 {
				event.Duration = 0
			}
			if event.HasExitCode && event.ExitCode == -1 && validHistoryEventState(event.State) {
				event.HasExitCode, event.ExitCode, event.State = false, 0, "unknown"
			}
		}
		if event.EventKey == "" || event.Source == "" || !validHistoryEventState(event.State) ||
			event.Duration < 0 || event.HistoryOrder < 0 || event.Occurrences < 1 || event.Occurrences > 1_000_000_000 {
			return nil, errors.New("history archive contains an invalid execution record")
		}
		if !event.Imported {
			// Keep the native allocator's exact integer domain. encoding/json
			// decodes into int, never through a lossy floating-point intermediary.
			if event.HistoryOrder == 0 {
				return nil, errors.New("history archive is missing a native submission order")
			}
			if event.SessionID != "" {
				key := nativeArchiveOrder{event.SessionID, event.HistoryOrder}
				if owner := orders[key]; owner != "" && owner != event.EventKey {
					return nil, errors.New("history archive has conflicting native submission orders")
				}
				orders[key] = event.EventKey
			}
		}
		if event.State == "completed" && (!event.HasExitCode || event.ExitCode != 0) ||
			event.State == "failed" && (!event.HasExitCode || event.ExitCode == 0) ||
			(event.State != "completed" && event.State != "failed") && event.HasExitCode {
			return nil, errors.New("history archive contains inconsistent execution outcome")
		}
		if event.SubmittedAt.IsZero() {
			event.SubmittedAt = event.StartedAt
		}
		event.SubmittedAt, event.StartedAt, event.CompletedAt = event.SubmittedAt.UTC(), event.StartedAt.UTC(), event.CompletedAt.UTC()
		for _, stamp := range []time.Time{event.SubmittedAt, event.StartedAt, event.CompletedAt} {
			// The existing store reserves epoch and earlier values for unknown
			// dates. Do not admit a known portable value that its reader erases,
			// or a year that the persistence/JSON representations cannot retain.
			if !stamp.IsZero() && (!parseKnownTimestamp(canonicalTimestamp(stamp)).Equal(stamp) || stamp.Year() > 9999) {
				return nil, errors.New("history archive timestamp is outside the supported storage representation")
			}
		}
		if !event.HasExitCode {
			event.ExitCode = 0
		}
		if event.State == "running" || event.State == "submitted" {
			event.State = "interrupted"
		}
		if existing, ok := seen[event.EventKey]; ok {
			if existing != event {
				return nil, errors.New("history archive has conflicting payloads for one execution ID")
			}
			continue
		}
		seen[event.EventKey] = event
		prepared = append(prepared, event)
	}
	return prepared, nil
}

const archiveHistoryColumns = "event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at, " +
	"duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported, aggregate"

// QueryAdapterHistoryEvents returns only replaceable external acquisitions.
// Canonical recall/export still use QueryHistoryEvents and include archives.
func (f *FrecencyStore) QueryAdapterHistoryEvents(ctx context.Context) ([]HistoryEvent, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := f.db.QueryContext(ctx, "SELECT "+archiveHistoryColumns+" FROM history_events "+
		"WHERE imported = 1 AND archive_owned = 0 ORDER BY started_at DESC, history_order DESC, event_key DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistoryEvents(rows)
}

// ImportHistoryArchiveEvents merges a validated portable snapshot atomically.
// Equal identities are no-ops; conflicting payloads reject the whole archive.
// A retained Imported flag describes evidence semantics, not adapter ownership.
func (f *FrecencyStore) ImportHistoryArchiveEvents(ctx context.Context, events []HistoryEvent) (int, error) {
	if f == nil {
		return 0, errors.New("history store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events, err := PrepareHistoryArchiveEvents(events)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	if err := f.acquireWrite(ctx); err != nil {
		return 0, err
	}
	defer f.releaseWrite()
	tx, finish, err := f.beginWriteTx(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	// Read, privacy reconciliation, ownership, projection and publication all
	// share the same SQLite writer boundary across terminal windows.
	if _, err := tx.ExecContext(ctx, "UPDATE metadata SET value = value WHERE key = 'history_schema_version'"); err != nil {
		return 0, err
	}
	existing := make(map[string]HistoryEvent)
	for start := 0; start < len(events); start += 128 {
		batch := events[start:min(start+128, len(events))]
		args := make([]any, len(batch))
		for index := range batch {
			args[index] = batch[index].EventKey
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+archiveHistoryColumns+" FROM history_events WHERE event_key IN ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
		if err != nil {
			return 0, err
		}
		stored, scanErr := scanHistoryEvents(rows)
		closeErr := rows.Close()
		if scanErr != nil {
			return 0, scanErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		stored, err = PrepareHistoryArchiveEvents(stored)
		if err != nil {
			return 0, errors.New("existing history cannot be compared safely with this archive")
		}
		for _, event := range stored {
			existing[event.EventKey] = event
		}
	}
	for _, event := range events {
		if old, ok := existing[event.EventKey]; ok && old != event {
			retry, err := historyArchiveRetainedRetry(ctx, tx, old, event)
			if err != nil {
				return 0, err
			}
			if !retry {
				return 0, errors.New("history archive conflicts with an existing execution ID; no records imported")
			}
		}
	}
	events, err = filterExcludedHistoryImports(ctx, tx, events)
	if err != nil {
		return 0, err
	}
	hasNewIdentity := false
	for _, event := range events {
		if _, exists := existing[event.EventKey]; !exists {
			hasNewIdentity = true
			break
		}
	}
	preserveLearning := newHistoryLearningGuard(ctx, tx)
	if hasNewIdentity {
		if err := preserveLearning(); err != nil {
			return 0, err
		}
	}
	// Validate same-session order claims against the destination, not just IDs.
	// One scan avoids an input-record x retained-session query amplification.
	rows, err := tx.QueryContext(ctx, `SELECT session_id, history_order, event_key FROM history_events
WHERE imported = 0 AND session_id <> ''`)
	if err != nil {
		return 0, err
	}
	owners := make(map[nativeArchiveOrder]string)
	for rows.Next() {
		var session, key string
		var order int
		if err := rows.Scan(&session, &order, &key); err != nil {
			_ = rows.Close()
			return 0, err
		}
		owners[nativeArchiveOrder{session, order}] = key
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, event := range events {
		if !event.Imported && event.SessionID != "" {
			owner := owners[nativeArchiveOrder{event.SessionID, event.HistoryOrder}]
			if owner != "" && owner != event.EventKey {
				return 0, errors.New("history archive conflicts with an existing native submission order; no records imported")
			}
		}
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO history_events ("+archiveHistoryColumns+", archive_owned, archive_payload_hash) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)")
	if err != nil {
		return 0, err
	}
	defer statement.Close()
	var imported []HistoryEvent
	added, removed, maximumNativeOrder := 0, int64(0), 0
	insert := func(event HistoryEvent) error {
		fingerprint, err := historyArchivePayloadFingerprint(event)
		if err != nil {
			return err
		}
		var exitCode any
		if event.HasExitCode {
			exitCode = event.ExitCode
		}
		_, err = statement.ExecContext(ctx, event.EventKey, event.Command, event.NormalizedCommand, event.Cwd,
			canonicalTimestamp(event.SubmittedAt), canonicalTimestamp(event.StartedAt), canonicalTimestamp(event.CompletedAt),
			event.Duration.Nanoseconds(), exitCode, event.Source, event.Host, event.SessionID, event.Shell, event.State,
			event.HistoryOrder, event.Occurrences, boolInt(event.Imported), boolInt(event.Aggregate), fingerprint)
		if err == nil {
			added++
			if !event.Imported {
				maximumNativeOrder = max(maximumNativeOrder, event.HistoryOrder)
			}
		}
		return err
	}
	for _, event := range events {
		if _, exists := existing[event.EventKey]; exists {
			fingerprint, err := historyArchivePayloadFingerprint(event)
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE history_events SET archive_owned = 1,
archive_payload_hash = CASE WHEN archive_payload_hash = '' THEN ? ELSE archive_payload_hash END
WHERE event_key = ?`, fingerprint, event.EventKey); err != nil {
				return 0, err
			}
			continue
		}
		if event.Imported {
			imported = append(imported, event)
			continue
		}
		forgotten, count, err := reconcileForgottenNativeCounterparts(ctx, tx, &event, nil, preserveLearning)
		if err != nil {
			return 0, err
		}
		removed += count
		if !forgotten {
			if err := insert(event); err != nil {
				return 0, err
			}
		}
	}
	imported, err = filterHistoryCounterparts(ctx, tx, imported, false)
	if err != nil {
		return 0, err
	}
	for _, event := range imported {
		if err := insert(event); err != nil {
			return 0, err
		}
	}
	if hasNewIdentity {
		count, err := reconcileForgottenHistoryCounterparts(ctx, tx, preserveLearning)
		if err != nil {
			return 0, err
		}
		removed += count
	}
	if maximumNativeOrder > 0 {
		if _, err := advanceNativeHistoryOrder(ctx, tx, maximumNativeOrder); err != nil {
			return 0, err
		}
	}
	if added > 0 || removed > 0 {
		if err := rebuildHistoryProjectionsAfterRemoval(ctx, tx); err != nil {
			return 0, err
		}
		if err := recordHistoryPersistenceTimestamp(ctx, tx); err != nil {
			return 0, err
		}
		if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// Partial count retention may shrink an admitted native aggregate without
// changing its identity. Accept only the exact original portable payload;
// never refill pruned occurrences or ignore another changed field. The opaque
// commitment lives with its canonical row, not in a permanent import registry.
func historyArchiveRetainedRetry(ctx context.Context, tx *sql.Tx, stored, incoming HistoryEvent) (bool, error) {
	if stored.Imported || stored.Occurrences >= incoming.Occurrences {
		return false, nil
	}
	retained := incoming
	retained.Occurrences = stored.Occurrences
	if retained != stored {
		return false, nil
	}
	var committed string
	err := tx.QueryRowContext(ctx, `SELECT archive_payload_hash FROM history_events
WHERE event_key = ? AND archive_owned = 1`, incoming.EventKey).Scan(&committed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || committed == "" {
		return false, err
	}
	fingerprint, err := historyArchivePayloadFingerprint(incoming)
	return err == nil && fingerprint == committed, err
}

// Explicit version-1 portable fields keep the commitment independent from
// future internal HistoryEvent fields. No duplicate command text is persisted.
func historyArchivePayloadFingerprint(event HistoryEvent) (string, error) {
	var exitCode any
	if event.HasExitCode {
		exitCode = event.ExitCode
	}
	payload, err := json.Marshal([]any{event.EventKey, event.Command, event.NormalizedCommand, event.Cwd,
		canonicalTimestamp(event.SubmittedAt), canonicalTimestamp(event.StartedAt), canonicalTimestamp(event.CompletedAt),
		int64(event.Duration), exitCode, event.Source, event.Host, event.SessionID, event.Shell,
		event.State, event.HistoryOrder, event.Occurrences, event.Imported, event.Aggregate})
	if err != nil {
		return "", errors.New("history archive payload cannot be represented safely")
	}
	return fmt.Sprintf("v1:%x", sha256.Sum256(payload)), nil
}

// Compatibility APIs can retain learning with no canonical execution owner.
