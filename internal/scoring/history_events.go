package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/spec"
)

type HistoryEvent struct {
	EventKey          string
	Command           string
	NormalizedCommand string
	Cwd               string
	SubmittedAt       time.Time
	StartedAt         time.Time
	CompletedAt       time.Time
	Duration          time.Duration
	ExitCode          int
	HasExitCode       bool
	Source            string
	Host              string
	SessionID         string
	Shell             string
	State             string
	HistoryOrder      int
	Occurrences       int
	Imported          bool
}

type HistoryStoreStats struct {
	Events              int
	DistinctCommands    int
	Unfinished          int
	Imported            int
	MostRecentPersisted time.Time
	States              map[string]int
	Sources             map[string]int
	ImportFreshness     map[string]time.Time
	ImportFailures      map[string]HistoryImportFailure
	SchemaVersion       string
}

type HistoryImportFailure struct {
	Count      int
	LastFailed time.Time
}

const (
	historyChangeRetention       = 8_192
	historyLastPersistedMetadata = "history_last_persisted_at"
)

type HistoryChange struct {
	Sequence int64
	Origin   string
	Kind     string
	EventKey string
}

type HistoryChangeBatch struct {
	Changes []HistoryChange
	Latest  int64
	Gap     bool
}

func (f *FrecencyStore) SetHistoryChangeOrigin(origin string) {
	if f == nil {
		return
	}
	f.historyOriginMu.Lock()
	f.historyOrigin = strings.TrimSpace(origin)
	f.historyOriginMu.Unlock()
}

// SetHistorySnapshotSequence records the change-feed boundary captured before
// a full canonical snapshot was loaded. Changes committed after this boundary
// remain eligible for the first cross-session synchronization poll.
func (f *FrecencyStore) SetHistorySnapshotSequence(sequence int64) {
	if f == nil {
		return
	}
	f.historySnapshotSequence.Store(max(sequence, 0))
}

// HistorySnapshotSequence returns the change-feed boundary represented by the
// most recently published full canonical snapshot.
func (f *FrecencyStore) HistorySnapshotSequence() int64 {
	if f == nil {
		return 0
	}
	return f.historySnapshotSequence.Load()
}

func (f *FrecencyStore) historyChangeOrigin() string {
	if f == nil {
		return ""
	}
	f.historyOriginMu.RLock()
	origin := f.historyOrigin
	f.historyOriginMu.RUnlock()
	return origin
}

func (f *FrecencyStore) recordHistoryChange(ctx context.Context, tx *sql.Tx, kind, eventKey string) error {
	result, err := tx.ExecContext(ctx, `
INSERT INTO history_changes (origin, kind, event_key)
VALUES (?, ?, ?)
`, f.historyChangeOrigin(), strings.TrimSpace(kind), strings.TrimSpace(eventKey))
	if err != nil {
		return err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM history_changes WHERE sequence <= ?`, sequence-historyChangeRetention)
	return err
}

func recordHistoryPersistenceTimestamp(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, historyLastPersistedMetadata, canonicalTimestamp(time.Now()))
	return err
}

func (f *FrecencyStore) QueryHistoryChanges(ctx context.Context, after int64, limit int) (HistoryChangeBatch, error) {
	var batch HistoryChangeBatch
	if f == nil {
		return batch, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 256
	}
	tx, err := f.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback() }()
	var earliest int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MIN(sequence), 0), COALESCE(MAX(sequence), 0)
FROM history_changes
`).Scan(&earliest, &batch.Latest); err != nil {
		return batch, err
	}
	batch.Gap = earliest > 0 && after < earliest-1
	rows, err := tx.QueryContext(ctx, `
SELECT sequence, origin, kind, event_key
FROM history_changes
WHERE sequence > ?
ORDER BY sequence ASC
LIMIT ?
`, after, limit)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	for rows.Next() {
		var change HistoryChange
		if err := rows.Scan(&change.Sequence, &change.Origin, &change.Kind, &change.EventKey); err != nil {
			return batch, err
		}
		batch.Changes = append(batch.Changes, change)
	}
	if err := rows.Err(); err != nil {
		return batch, err
	}
	if err := rows.Close(); err != nil {
		return batch, err
	}
	return batch, tx.Commit()
}

type HistoryStoreHealth struct {
	DatabasePath string
	QuickCheck   string
	FileModes    map[string]os.FileMode
}

type legacyHistoryAggregate struct {
	command  string
	cwd      string
	source   string
	count    int
	lastUsed time.Time
	duration time.Duration
	exitCode int
	hasExit  bool
	state    string
	imported bool
}

type legacyHistoryAggregateKey struct {
	command string
	cwd     string
	source  string
	state   string
}

// migrateLegacyHistoryAggregates converts pre-lifecycle aggregate rows into
// canonical events without inventing more executions than the aggregates
// contain. Deterministic IDs make the migration restart-safe. Existing rich
// events satisfy the corresponding aggregate counts first, so installations
// that previously wrote both models do not receive duplicates.
func migrateLegacyHistoryAggregates(ctx context.Context, tx *sql.Tx) error {
	if err := purgeLegacyHistoryPolicyViolations(ctx, tx); err != nil {
		return err
	}
	existing, err := legacyCanonicalEventCounts(ctx, tx)
	if err != nil {
		return err
	}
	native, err := legacyNativeAggregates(ctx, tx)
	if err != nil {
		return err
	}
	for _, aggregate := range native {
		key := legacyHistoryAggregateKey{
			command: aggregate.command,
			cwd:     aggregate.cwd,
			source:  "native",
			state:   aggregate.state,
		}
		if err := insertMissingLegacyEvents(ctx, tx, aggregate, existing[key]); err != nil {
			return err
		}
		if aggregate.count > existing[key] {
			existing[key] = aggregate.count
		}
	}

	imported, err := legacyImportedAggregates(ctx, tx)
	if err != nil {
		return err
	}
	for _, aggregate := range imported {
		key := legacyHistoryAggregateKey{
			command: aggregate.command,
			cwd:     aggregate.cwd,
			source:  aggregate.source,
			state:   "imported",
		}
		if err := insertMissingLegacyEvents(ctx, tx, aggregate, existing[key]); err != nil {
			return err
		}
		if aggregate.count > existing[key] {
			existing[key] = aggregate.count
		}
	}

	return migrateLegacyImportedSummary(ctx, tx, existing)
}

func purgeLegacyHistoryPolicyViolations(ctx context.Context, tx *sql.Tx) error {
	tables := []struct {
		name    string
		first   string
		second  string
		combine bool
	}{
		{name: "history_events", first: "command", second: "normalized_command"},
		{name: "history_entries", first: "cmd"},
		{name: "imported_history_entries", first: "cmd"},
		{name: "imported_history", first: "cmd"},
		{name: "command_outcomes", first: "cmd"},
		{name: "suggestion_feedback", first: "cmd"},
		{name: "recent_failures", first: "cmd"},
		{name: "exact_command_transitions", first: "prev_command", second: "next_command"},
		{name: "command_transitions", first: "prev_skeleton", second: "next_skeleton"},
		{name: "argument_values", first: "scope", second: "value", combine: true},
	}
	for _, table := range tables {
		second := "''"
		if table.second != "" {
			second = table.second
		}
		rows, err := tx.QueryContext(ctx, "SELECT rowid, "+table.first+", "+second+" FROM "+table.name)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var rejected []int64
		for rows.Next() {
			var rowID int64
			var first, secondValue string
			if err := rows.Scan(&rowID, &first, &secondValue); err != nil {
				return err
			}
			recordable := true
			if table.combine {
				_, recordable = policy.HistoryCommand(strings.TrimSpace(first)+" "+strings.TrimSpace(secondValue), false)
			} else {
				for _, command := range []string{first, secondValue} {
					if strings.TrimSpace(command) == "" {
						continue
					}
					if _, ok := policy.HistoryCommand(command, false); !ok {
						recordable = false
						break
					}
				}
			}
			if !recordable {
				rejected = append(rejected, rowID)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, rowID := range rejected {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table.name+" WHERE rowid = ?", rowID); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyCanonicalEventCounts(ctx context.Context, tx *sql.Tx) (map[legacyHistoryAggregateKey]int, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT normalized_command, cwd, source, state, imported, SUM(occurrences)
FROM history_events
GROUP BY normalized_command, cwd, source, state, imported
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[legacyHistoryAggregateKey]int)
	for rows.Next() {
		var command, cwd, source, state string
		var imported, count int
		if err := rows.Scan(&command, &cwd, &source, &state, &imported, &count); err != nil {
			return nil, err
		}
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		key := legacyHistoryAggregateKey{command: command, cwd: strings.TrimSpace(cwd), state: state}
		if imported != 0 {
			key.source = strings.TrimSpace(source)
			key.state = "imported"
		} else {
			key.source = "native"
		}
		counts[key] += count
	}
	return counts, rows.Err()
}

func legacyNativeAggregates(ctx context.Context, tx *sql.Tx) ([]legacyHistoryAggregate, error) {
	rows, err := tx.QueryContext(ctx, `
WITH keys AS (
    SELECT cmd, cwd FROM history_entries
    UNION
    SELECT cmd, cwd FROM command_outcomes
)
SELECT keys.cmd, keys.cwd,
       COALESCE(history_entries.count, 0), COALESCE(history_entries.last_used, ''),
       COALESCE(command_outcomes.successes, 0), COALESCE(command_outcomes.failures, 0),
       COALESCE(command_outcomes.last_used, '')
FROM keys
LEFT JOIN history_entries
       ON history_entries.cmd = keys.cmd AND history_entries.cwd = keys.cwd
LEFT JOIN command_outcomes
       ON command_outcomes.cmd = keys.cmd AND command_outcomes.cwd = keys.cwd
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aggregates []legacyHistoryAggregate
	for rows.Next() {
		var command, cwd, historyLastUsed, outcomeLastUsed string
		var historyCount, successes, failures int
		if err := rows.Scan(&command, &cwd, &historyCount, &historyLastUsed, &successes, &failures, &outcomeLastUsed); err != nil {
			return nil, err
		}
		normalized, recordable := policy.HistoryCommand(command, false)
		if !recordable {
			continue
		}
		lastUsed := laterHistoryTimestamp(parseKnownTimestamp(historyLastUsed), parseKnownTimestamp(outcomeLastUsed))
		successes = max(successes, historyCount)
		if successes > 0 {
			aggregates = append(aggregates, legacyHistoryAggregate{
				command: normalized, cwd: strings.TrimSpace(cwd), source: "legacy-vuja",
				count: successes, lastUsed: lastUsed, exitCode: 0, hasExit: true, state: "completed",
			})
		}
		if failures > 0 {
			aggregates = append(aggregates, legacyHistoryAggregate{
				command: normalized, cwd: strings.TrimSpace(cwd), source: "legacy-vuja",
				count: failures, lastUsed: lastUsed, exitCode: 1, hasExit: true, state: "failed",
			})
		}
	}
	return aggregates, rows.Err()
}

func legacyImportedAggregates(ctx context.Context, tx *sql.Tx) ([]legacyHistoryAggregate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT cmd, cwd, count, last_used, exit_code, duration_ns, source
FROM imported_history_entries
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aggregates []legacyHistoryAggregate
	for rows.Next() {
		var command, cwd, lastUsed, source string
		var count int
		var exitCode *int
		var durationNS int64
		if err := rows.Scan(&command, &cwd, &count, &lastUsed, &exitCode, &durationNS, &source); err != nil {
			return nil, err
		}
		normalized, recordable := policy.HistoryCommand(command, false)
		if !recordable || count <= 0 {
			continue
		}
		aggregate := legacyHistoryAggregate{
			command: normalized, cwd: strings.TrimSpace(cwd), source: strings.TrimSpace(source),
			count: count, lastUsed: parseKnownTimestamp(lastUsed), duration: time.Duration(durationNS),
			state: "unknown", imported: true,
		}
		if aggregate.source == "" {
			aggregate.source = "shell"
		}
		if exitCode != nil {
			aggregate.exitCode = *exitCode
			aggregate.hasExit = true
			aggregate.state = "failed"
			if *exitCode == 0 {
				aggregate.state = "completed"
			}
		}
		aggregates = append(aggregates, aggregate)
	}
	return aggregates, rows.Err()
}

func migrateLegacyImportedSummary(
	ctx context.Context,
	tx *sql.Tx,
	existing map[legacyHistoryAggregateKey]int,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT cmd, count, last_used FROM imported_history`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type summary struct {
		command  string
		count    int
		lastUsed time.Time
	}
	var summaries []summary
	for rows.Next() {
		var command, lastUsed string
		var count int
		if err := rows.Scan(&command, &count, &lastUsed); err != nil {
			return err
		}
		if normalized, recordable := policy.HistoryCommand(command, false); recordable && count > 0 {
			summaries = append(summaries, summary{command: normalized, count: count, lastUsed: parseKnownTimestamp(lastUsed)})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range summaries {
		present := 0
		for key, count := range existing {
			if key.state == "imported" && key.command == item.command {
				present += count
			}
		}
		aggregate := legacyHistoryAggregate{
			command: item.command, source: "legacy-shell", count: item.count,
			lastUsed: item.lastUsed, state: "unknown", imported: true,
		}
		if err := insertMissingLegacyEvents(ctx, tx, aggregate, present); err != nil {
			return err
		}
		if item.count > present {
			key := legacyHistoryAggregateKey{command: item.command, source: aggregate.source, state: "imported"}
			existing[key] += item.count - present
		}
	}
	return nil
}

func insertMissingLegacyEvents(
	ctx context.Context,
	tx *sql.Tx,
	aggregate legacyHistoryAggregate,
	existing int,
) error {
	if aggregate.count <= existing {
		return nil
	}
	timestamp := aggregate.lastUsed
	if timestamp.IsZero() {
		timestamp = time.Unix(0, 0)
	}
	var exitCode any
	if aggregate.hasExit {
		exitCode = aggregate.exitCode
	}
	missing := aggregate.count - existing
	eventKey := legacyHistoryEventKey(aggregate, existing)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO history_events
    (event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
     duration_ns, exit_code, source, session_id, state, history_order, occurrences, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'legacy-migration', ?, ?, ?, ?)
ON CONFLICT(event_key) DO NOTHING
`, eventKey, aggregate.command, aggregate.command, aggregate.cwd, canonicalTimestamp(timestamp),
		canonicalTimestamp(timestamp), canonicalTimestamp(timestamp), aggregate.duration.Nanoseconds(), exitCode,
		aggregate.source, aggregate.state, existing+1, missing, boolInt(aggregate.imported)); err != nil {
		return err
	}
	return nil
}

func legacyHistoryEventKey(aggregate legacyHistoryAggregate, index int) string {
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t\x00%d", aggregate.source, aggregate.command,
		aggregate.cwd, aggregate.state, aggregate.imported, index)
	sum := sha256.Sum256([]byte(value))
	return "legacy:" + hex.EncodeToString(sum[:16])
}

func laterHistoryTimestamp(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (f *FrecencyStore) RecordHistoryEvent(ctx context.Context, event HistoryEvent) error {
	if f == nil {
		return nil
	}
	event.EventKey = strings.TrimSpace(event.EventKey)
	exactCommand, recordable := policy.HistoryCommand(event.Command, false)
	if !recordable {
		return errors.New("history event rejected by privacy policy")
	}
	event.NormalizedCommand = strings.TrimSpace(event.NormalizedCommand)
	if event.NormalizedCommand == "" {
		event.NormalizedCommand = exactCommand
	}
	if event.SubmittedAt.IsZero() {
		event.SubmittedAt = event.StartedAt
	}
	if event.State == "" {
		event.State = historyEventState(event)
	}
	event.Source = strings.TrimSpace(event.Source)
	if event.EventKey == "" || strings.TrimSpace(event.Command) == "" || event.Source == "" {
		return errors.New("history event key, command, and source are required")
	}
	if normalized, ok := policy.HistoryCommand(event.NormalizedCommand, false); !ok {
		return errors.New("history event rejected by privacy policy")
	} else {
		event.NormalizedCommand = normalized
	}
	if !validHistoryEventState(event.State) {
		return fmt.Errorf("invalid history event state %q", event.State)
	}
	if event.Occurrences <= 0 {
		event.Occurrences = 1
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var exitCode any
	if event.HasExitCode {
		exitCode = event.ExitCode
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
INSERT INTO history_events
    (event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
     duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(event_key) DO NOTHING
	`, event.EventKey, event.Command, event.NormalizedCommand, strings.TrimSpace(event.Cwd),
		canonicalTimestamp(event.SubmittedAt), canonicalTimestamp(event.StartedAt), canonicalTimestamp(event.CompletedAt),
		event.Duration.Nanoseconds(), exitCode, event.Source, strings.TrimSpace(event.Host),
		strings.TrimSpace(event.SessionID), strings.TrimSpace(event.Shell), event.State, event.HistoryOrder, event.Occurrences)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != 1 {
		return fmt.Errorf("history event key %q already exists", event.EventKey)
	}
	if err := recordHistoryPersistenceTimestamp(ctx, tx); err != nil {
		return err
	}
	if err := f.recordHistoryChange(ctx, tx, "upsert", event.EventKey); err != nil {
		return err
	}
	return tx.Commit()
}

func historyEventState(event HistoryEvent) string {
	if !event.HasExitCode {
		return "unknown"
	}
	if event.ExitCode == 0 {
		return "completed"
	}
	return "failed"
}

func validHistoryEventState(state string) bool {
	switch state {
	case "submitted", "running", "completed", "failed", "interrupted", "unknown":
		return true
	default:
		return false
	}
}

func (f *FrecencyStore) RecordHistorySubmission(ctx context.Context, event HistoryEvent) error {
	event.HasExitCode = false
	event.CompletedAt = time.Time{}
	event.Duration = 0
	event.State = "running"
	return f.RecordHistoryEvent(ctx, event)
}

// CompleteHistoryEvent transitions a submitted event exactly once. The boolean
// reports whether this call performed the transition; repeated completion
// notifications are successful no-ops. Command identity and directory are
// always loaded from the durable submission so completion cannot rewrite or
// derive signals from untrusted caller metadata.
func (f *FrecencyStore) CompleteHistoryEvent(ctx context.Context, event HistoryEvent) (bool, error) {
	if f == nil {
		return false, nil
	}
	event.EventKey = strings.TrimSpace(event.EventKey)
	if event.EventKey == "" {
		return false, errors.New("history event key is required")
	}
	if !event.HasExitCode {
		return false, errors.New("completed history event requires an exit code")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return false, err
	}
	defer f.releaseWrite()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var currentRowID int64
	var command, normalizedCommand, cwd, state, startedAtRaw, sessionID string
	if err := tx.QueryRowContext(ctx, `
SELECT rowid, command, normalized_command, cwd, state, started_at, session_id
FROM history_events
WHERE event_key = ? AND imported = 0
`, event.EventKey).Scan(&currentRowID, &command, &normalizedCommand, &cwd, &state, &startedAtRaw, &sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("history event %q was not found", event.EventKey)
		}
		return false, err
	}
	if state == "completed" || state == "failed" {
		return false, tx.Commit()
	}
	if state != "submitted" && state != "running" {
		return false, fmt.Errorf("history event %q cannot complete from state %q", event.EventKey, state)
	}
	normalizedCommand = strings.TrimSpace(normalizedCommand)
	if normalizedCommand == "" {
		normalizedCommand = strings.TrimSpace(command)
	}
	if _, ok := policy.HistoryCommand(command, false); !ok {
		return false, fmt.Errorf("history event %q was rejected by privacy policy", event.EventKey)
	}
	if normalized, ok := policy.HistoryCommand(normalizedCommand, false); !ok {
		return false, fmt.Errorf("history event %q was rejected by privacy policy", event.EventKey)
	} else {
		normalizedCommand = normalized
	}
	cwd = strings.TrimSpace(cwd)
	startedAt := parseKnownTimestamp(startedAtRaw)
	if event.CompletedAt.IsZero() {
		event.CompletedAt = startedAt.Add(event.Duration)
	}
	event.State = historyEventState(event)
	result, err := tx.ExecContext(ctx, `
UPDATE history_events
SET completed_at = ?, duration_ns = ?, exit_code = ?, state = ?
WHERE event_key = ? AND imported = 0 AND state IN ('submitted', 'running')
`, canonicalTimestamp(event.CompletedAt), event.Duration.Nanoseconds(), event.ExitCode, event.State, event.EventKey)
	if err != nil {
		return false, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		if affectedErr != nil {
			return false, affectedErr
		}
		return false, fmt.Errorf("history event %q changed while completing", event.EventKey)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO history_entries (cmd, cwd, count, last_used)
VALUES (?, ?, ?, ?)
ON CONFLICT(cmd, cwd) DO UPDATE SET
    count = count + excluded.count,
    last_used = excluded.last_used
	`, normalizedCommand, cwd, 1, canonicalTimestamp(event.CompletedAt)); err != nil {
		return false, err
	}
	successes, failures := 0, 1
	if event.ExitCode == 0 {
		successes, failures = 1, 0
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO command_outcomes (cmd, cwd, successes, failures, last_used)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(cmd, cwd) DO UPDATE SET
    successes = successes + excluded.successes,
    failures = failures + excluded.failures,
    last_used = excluded.last_used
	`, normalizedCommand, cwd, successes, failures, canonicalTimestamp(event.CompletedAt)); err != nil {
		return false, err
	}
	if event.ExitCode == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recent_failures WHERE cwd = ?`, cwd); err != nil {
			return false, err
		}
	} else if _, err := tx.ExecContext(ctx, `
INSERT INTO recent_failures (cwd, cmd, exit_code, failed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(cwd) DO UPDATE SET
    cmd = excluded.cmd,
    exit_code = excluded.exit_code,
    failed_at = excluded.failed_at
	`, cwd, normalizedCommand, event.ExitCode, canonicalTimestamp(event.CompletedAt)); err != nil {
		return false, err
	}
	if event.ExitCode == 0 {
		tokens := spec.Tokenize(normalizedCommand)
		for position := 1; position < len(tokens); position++ {
			value := strings.TrimSpace(tokens[position])
			if value == "" || strings.HasPrefix(value, "-") {
				continue
			}
			scope := strings.TrimSpace(strings.Join(tokens[:position], " "))
			if scope == "" || len(scope) > 512 || len(value) > 512 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO argument_values (scope, position, value, cwd, last_used)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(scope, position, value, cwd) DO UPDATE SET
    count = count + 1,
    last_used = excluded.last_used
			`, scope, position, value, cwd, canonicalTimestamp(event.CompletedAt)); err != nil {
				return false, err
			}
		}
	}
	if err := deriveCanonicalHistoryTransition(
		ctx,
		tx,
		currentRowID,
		sessionID,
		normalizedCommand,
		cwd,
		event.ExitCode,
		event.CompletedAt,
	); err != nil {
		return false, err
	}
	if err := recordHistoryPersistenceTimestamp(ctx, tx); err != nil {
		return false, err
	}
	if err := f.recordHistoryChange(ctx, tx, "upsert", event.EventKey); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// deriveCanonicalHistoryTransition updates both transition projections inside
// the same transaction that completes the canonical event. A process exit can
// therefore no longer persist completion while silently losing its transition
// evidence. Row order is submission order for Vuja-owned events and avoids
// depending on wall-clock precision when two commands begin together.
func deriveCanonicalHistoryTransition(
	ctx context.Context,
	tx *sql.Tx,
	currentRowID int64,
	sessionID, nextCommand, cwd string,
	nextExitCode int,
	completedAt time.Time,
) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || currentRowID <= 0 {
		return nil
	}
	var previousCommand string
	err := tx.QueryRowContext(ctx, `
SELECT normalized_command
FROM history_events
WHERE rowid < ?
  AND imported = 0
  AND session_id = ?
  AND state IN ('completed', 'failed')
ORDER BY rowid DESC
LIMIT 1
`, currentRowID, sessionID).Scan(&previousCommand)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	previousCommand = strings.TrimSpace(previousCommand)
	nextCommand = strings.TrimSpace(nextCommand)
	if previousCommand == "" || nextCommand == "" {
		return nil
	}
	count := 0
	if nextExitCode == 0 {
		count = 1
	}
	lastUsed := canonicalTimestamp(completedAt)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO exact_command_transitions (prev_command, next_command, cwd, count, last_used)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(prev_command, next_command, cwd) DO UPDATE SET
    count = count + excluded.count,
    last_used = excluded.last_used
`, previousCommand, nextCommand, cwd, count, lastUsed); err != nil {
		return err
	}
	previousSkeleton := ExtractSkeleton(previousCommand)
	nextSkeleton := ExtractSkeleton(nextCommand)
	if previousSkeleton == "" || nextSkeleton == "" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO command_transitions (prev_skeleton, next_skeleton, cwd, count, last_used)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(prev_skeleton, next_skeleton, cwd) DO UPDATE SET
    count = count + excluded.count,
    last_used = excluded.last_used
`, previousSkeleton, nextSkeleton, cwd, count, lastUsed)
	return err
}

func (f *FrecencyStore) ReconcileInterruptedHistory(ctx context.Context, activeSessionID string, otherLiveSessionIDs ...string) (int64, error) {
	if f == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return 0, err
	}
	defer f.releaseWrite()
	protectedSessions := make([]string, 0, len(otherLiveSessionIDs)+1)
	seen := make(map[string]bool, len(otherLiveSessionIDs)+1)
	for _, sessionID := range append([]string{activeSessionID}, otherLiveSessionIDs...) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID != "" && !seen[sessionID] {
			seen[sessionID] = true
			protectedSessions = append(protectedSessions, sessionID)
		}
	}
	query := `
UPDATE history_events
SET state = 'interrupted'
WHERE imported = 0
  AND state IN ('submitted', 'running')
`
	arguments := make([]any, 0, len(protectedSessions))
	if len(protectedSessions) > 0 {
		query += "  AND session_id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(protectedSessions)), ",") + ")\n"
		for _, sessionID := range protectedSessions {
			arguments = append(arguments, sessionID)
		}
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if removed == 0 {
		return 0, tx.Commit()
	}
	if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

func (f *FrecencyStore) QueryUnfinishedHistoryEvents(ctx context.Context) ([]HistoryEvent, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
       duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported
FROM history_events
WHERE imported = 0 AND state IN ('submitted', 'running')
ORDER BY started_at DESC, event_key DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistoryEvents(rows)
}

func (f *FrecencyStore) PruneHistoryEvents(ctx context.Context, maxEvents int) (int64, error) {
	if f == nil || maxEvents <= 0 {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
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
	rows, err := tx.QueryContext(ctx, `
SELECT event_key, occurrences
FROM history_events
WHERE imported = 0 AND state NOT IN ('submitted', 'running')
ORDER BY started_at DESC, history_order DESC, event_key DESC
`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	remaining := maxEvents
	var removed int64
	var deleteKeys []string
	partialKey := ""
	partialOccurrences := 0
	for rows.Next() {
		var eventKey string
		var occurrences int
		if err := rows.Scan(&eventKey, &occurrences); err != nil {
			return 0, err
		}
		occurrences = max(occurrences, 1)
		switch {
		case remaining <= 0:
			deleteKeys = append(deleteKeys, eventKey)
			removed += int64(occurrences)
		case occurrences <= remaining:
			remaining -= occurrences
		default:
			partialKey = eventKey
			partialOccurrences = remaining
			removed += int64(occurrences - remaining)
			remaining = 0
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if partialKey != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE history_events SET occurrences = ? WHERE event_key = ?`, partialOccurrences, partialKey); err != nil {
			return 0, err
		}
	}
	for _, eventKey := range deleteKeys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_events WHERE event_key = ?`, eventKey); err != nil {
			return 0, err
		}
	}
	if removed == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	for _, statement := range []string{
		`DELETE FROM history_entries`,
		`INSERT INTO history_entries (cmd, cwd, count, last_used)
	     SELECT normalized_command, cwd,
	            SUM(occurrences),
	            MAX(completed_at)
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
                    SUM(occurrences) OVER (
                        PARTITION BY normalized_command, cwd, source
                    ) AS event_count,
                    MAX(started_at) OVER (
                        PARTITION BY normalized_command, cwd, source
                    ) AS most_recent,
                    ROW_NUMBER() OVER (
                        PARTITION BY normalized_command, cwd, source
                        ORDER BY started_at DESC, history_order DESC, event_key DESC
                    ) AS recency_rank
             FROM history_events
             WHERE imported = 1
         )
         SELECT normalized_command, cwd, event_count, most_recent, exit_code, duration_ns, source
         FROM ranked
         WHERE recency_rank = 1`,
		`DELETE FROM imported_history`,
		`INSERT INTO imported_history (cmd, count, last_used)
         SELECT cmd, SUM(count), MAX(last_used)
         FROM imported_history_entries
         GROUP BY cmd`,
		`DELETE FROM command_transitions`,
		`DELETE FROM exact_command_transitions`,
		`DELETE FROM suggestion_feedback
         WHERE NOT EXISTS (
             SELECT 1 FROM history_events
             WHERE imported = 0
               AND normalized_command = suggestion_feedback.cmd
               AND cwd = suggestion_feedback.cwd
         )`,
		`DELETE FROM argument_values`,
		`DELETE FROM recent_failures`,
		`DELETE FROM metadata
         WHERE key = 'imported_history_events_fingerprint'
            OR key = 'imported_history_fingerprint'`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return 0, err
		}
	}
	if err := rebuildRetainedCompletionSignals(ctx, tx); err != nil {
		return 0, err
	}
	if err := rebuildRetainedTransitions(ctx, tx); err != nil {
		return 0, err
	}
	if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

func rebuildRetainedCompletionSignals(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
SELECT normalized_command, cwd, state, exit_code, completed_at, occurrences
FROM history_events
WHERE imported = 0 AND state IN ('completed', 'failed')
ORDER BY completed_at ASC, event_key ASC
`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type argumentKey struct {
		scope    string
		position int
		value    string
		cwd      string
	}
	type argumentAggregate struct {
		count    int
		lastUsed string
	}
	type failureAggregate struct {
		command  string
		exitCode int
		failedAt string
	}
	arguments := make(map[argumentKey]argumentAggregate)
	failures := make(map[string]failureAggregate)
	for rows.Next() {
		var command, cwd, state, completedAt string
		var exitCode *int
		var occurrences int
		if err := rows.Scan(&command, &cwd, &state, &exitCode, &completedAt, &occurrences); err != nil {
			return err
		}
		if state == "failed" && exitCode != nil {
			failures[cwd] = failureAggregate{command: command, exitCode: *exitCode, failedAt: completedAt}
		} else if state == "completed" {
			delete(failures, cwd)
			for position, tokens := 1, spec.Tokenize(command); position < len(tokens); position++ {
				value := strings.TrimSpace(tokens[position])
				if value == "" || strings.HasPrefix(value, "-") {
					continue
				}
				scope := strings.TrimSpace(strings.Join(tokens[:position], " "))
				if scope == "" || len(scope) > 512 || len(value) > 512 {
					continue
				}
				key := argumentKey{scope: scope, position: position, value: value, cwd: cwd}
				aggregate := arguments[key]
				aggregate.count += max(occurrences, 1)
				aggregate.lastUsed = completedAt
				arguments[key] = aggregate
			}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for key, aggregate := range arguments {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO argument_values (scope, position, value, cwd, count, last_used)
VALUES (?, ?, ?, ?, ?, ?)
`, key.scope, key.position, key.value, key.cwd, aggregate.count, aggregate.lastUsed); err != nil {
			return err
		}
	}
	for cwd, failure := range failures {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO recent_failures (cwd, cmd, exit_code, failed_at)
VALUES (?, ?, ?, ?)
`, cwd, failure.command, failure.exitCode, failure.failedAt); err != nil {
			return err
		}
	}
	return nil
}

func rebuildRetainedTransitions(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
SELECT normalized_command, cwd, session_id, state, completed_at
FROM history_events
WHERE imported = 0 AND state IN ('completed', 'failed')
ORDER BY rowid ASC
`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type transitionKey struct {
		previous string
		next     string
		cwd      string
	}
	type transitionAggregate struct {
		count    int
		lastUsed string
	}
	previousBySession := make(map[string]string)
	exact := make(map[transitionKey]transitionAggregate)
	skeletons := make(map[transitionKey]transitionAggregate)
	for rows.Next() {
		var command, cwd, sessionID, state, completedAt string
		if err := rows.Scan(&command, &cwd, &sessionID, &state, &completedAt); err != nil {
			return err
		}
		command = strings.TrimSpace(command)
		sessionID = strings.TrimSpace(sessionID)
		if command == "" || sessionID == "" {
			continue
		}
		previous := previousBySession[sessionID]
		if previous != "" && state == "completed" {
			key := transitionKey{previous: previous, next: command, cwd: strings.TrimSpace(cwd)}
			aggregate := exact[key]
			aggregate.count++
			aggregate.lastUsed = completedAt
			exact[key] = aggregate

			previousSkeleton := ExtractSkeleton(previous)
			nextSkeleton := ExtractSkeleton(command)
			if previousSkeleton != "" && nextSkeleton != "" {
				key = transitionKey{previous: previousSkeleton, next: nextSkeleton, cwd: strings.TrimSpace(cwd)}
				aggregate = skeletons[key]
				aggregate.count++
				aggregate.lastUsed = completedAt
				skeletons[key] = aggregate
			}
		}
		previousBySession[sessionID] = command
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for key, aggregate := range exact {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO exact_command_transitions (prev_command, next_command, cwd, count, last_used)
VALUES (?, ?, ?, ?, ?)
`, key.previous, key.next, key.cwd, aggregate.count, aggregate.lastUsed); err != nil {
			return err
		}
	}
	for key, aggregate := range skeletons {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO command_transitions (prev_skeleton, next_skeleton, cwd, count, last_used)
VALUES (?, ?, ?, ?, ?)
`, key.previous, key.next, key.cwd, aggregate.count, aggregate.lastUsed); err != nil {
			return err
		}
	}
	return nil
}

func (f *FrecencyStore) ClearHistory(ctx context.Context) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{
		"history_events", "history_entries", "imported_history_entries", "imported_history",
		"command_transitions", "exact_command_transitions", "command_outcomes", "suggestion_feedback",
		"argument_values", "recent_failures",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM directory_navigation_sources WHERE source = 'history'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM directory_navigation_index;
INSERT INTO directory_navigation_index (path, count, recent_count, last_used)
SELECT path, SUM(count), SUM(recent_count), MAX(last_used)
FROM directory_navigation_sources
GROUP BY path;
	DELETE FROM metadata
	WHERE key = 'imported_history_events_fingerprint'
		OR key = 'imported_history_fingerprint'
		   OR key = 'history_last_persisted_at'
		   OR key LIKE 'history_imported_at:%'
	   OR key LIKE 'history_import_failure_count:%'
	   OR key LIKE 'history_import_failed_at:%';
`); err != nil {
		return err
	}
	if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *FrecencyStore) HistoryStats(ctx context.Context) (HistoryStoreStats, error) {
	stats := HistoryStoreStats{
		States: make(map[string]int), Sources: make(map[string]int),
		ImportFreshness: make(map[string]time.Time), ImportFailures: make(map[string]HistoryImportFailure),
	}
	if f == nil {
		return stats, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var mostRecent string
	if err := f.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(occurrences), 0), COUNT(DISTINCT normalized_command),
	       COALESCE(SUM(CASE WHEN state IN ('submitted', 'running') THEN 1 ELSE 0 END), 0),
	       COALESCE(SUM(CASE WHEN imported = 1 THEN occurrences ELSE 0 END), 0),
	       COALESCE(MAX(CASE WHEN imported = 0 THEN submitted_at ELSE '' END), '')
FROM history_events
`).Scan(&stats.Events, &stats.DistinctCommands, &stats.Unfinished, &stats.Imported, &mostRecent); err != nil {
		return stats, err
	}
	stats.MostRecentPersisted = parseKnownTimestamp(mostRecent)
	var persistedAt string
	persistedErr := f.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, historyLastPersistedMetadata).Scan(&persistedAt)
	if persistedErr == nil {
		stats.MostRecentPersisted = parseKnownTimestamp(persistedAt)
	} else if !errors.Is(persistedErr, sql.ErrNoRows) {
		return stats, persistedErr
	}
	for _, group := range []struct {
		column string
		target map[string]int
	}{{"state", stats.States}, {"source", stats.Sources}} {
		rows, err := f.db.QueryContext(ctx, "SELECT "+group.column+", SUM(occurrences) FROM history_events GROUP BY "+group.column)
		if err != nil {
			return stats, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var key string
			var count int
			if err := rows.Scan(&key, &count); err != nil {
				return stats, err
			}
			group.target[key] = count
		}
		if err := rows.Close(); err != nil {
			return stats, err
		}
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT key, value FROM metadata
WHERE key LIKE 'history_imported_at:%'
   OR key LIKE 'history_import_failure_count:%'
   OR key LIKE 'history_import_failed_at:%'
`)
	if err != nil {
		return stats, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return stats, err
		}
		switch {
		case strings.HasPrefix(key, "history_imported_at:"):
			stats.ImportFreshness[strings.TrimPrefix(key, "history_imported_at:")] = parseKnownTimestamp(value)
		case strings.HasPrefix(key, "history_import_failure_count:"):
			source := strings.TrimPrefix(key, "history_import_failure_count:")
			failure := stats.ImportFailures[source]
			failure.Count, _ = strconv.Atoi(value)
			stats.ImportFailures[source] = failure
		case strings.HasPrefix(key, "history_import_failed_at:"):
			source := strings.TrimPrefix(key, "history_import_failed_at:")
			failure := stats.ImportFailures[source]
			failure.LastFailed = parseKnownTimestamp(value)
			stats.ImportFailures[source] = failure
		}
	}
	if err := rows.Close(); err != nil {
		return stats, err
	}
	_ = f.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'history_schema_version'`).Scan(&stats.SchemaVersion)
	return stats, nil
}

func (f *FrecencyStore) RecordHistoryImportFailure(ctx context.Context, source string, failedAt time.Time) error {
	if f == nil {
		return nil
	}
	source = strings.TrimSpace(source)
	if source == "" || strings.Contains(source, ":") {
		return errors.New("valid history import source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if failedAt.IsZero() {
		failedAt = time.Now()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value) VALUES (?, '1')
ON CONFLICT(key) DO UPDATE SET value = CAST(metadata.value AS INTEGER) + 1
`, "history_import_failure_count:"+source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, "history_import_failed_at:"+source, canonicalTimestamp(failedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *FrecencyStore) HistoryHealth(ctx context.Context) (HistoryStoreHealth, error) {
	health := HistoryStoreHealth{FileModes: make(map[string]os.FileMode)}
	if f == nil {
		return health, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	health.DatabasePath = f.dbPath
	if err := f.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&health.QuickCheck); err != nil {
		return health, err
	}
	for _, path := range []string{
		filepath.Dir(f.dbPath), f.dbPath, f.dbPath + "-wal", f.dbPath + "-shm",
		f.dbPath + ".pre-lifecycle-v2.bak",
	} {
		if info, err := os.Stat(path); err == nil {
			health.FileModes[path] = info.Mode().Perm()
		} else if !os.IsNotExist(err) {
			return health, err
		}
	}
	return health, nil
}

func (f *FrecencyStore) RecordHistoryImportFreshness(ctx context.Context, source string, importedAt time.Time) error {
	if f == nil {
		return nil
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return errors.New("history import source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	_, err := f.db.ExecContext(ctx, `
INSERT INTO metadata (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, "history_imported_at:"+source, canonicalTimestamp(importedAt))
	return err
}

func (f *FrecencyStore) ReplaceImportedHistoryEvents(ctx context.Context, events []HistoryEvent) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()

	fingerprint := importedHistoryEventsFingerprint(events)
	var storedFingerprint string
	fingerprintErr := f.db.QueryRowContext(
		ctx,
		`SELECT value FROM metadata WHERE key = 'imported_history_events_fingerprint'`,
	).Scan(&storedFingerprint)
	if fingerprintErr == nil && storedFingerprint == fingerprint {
		return nil
	}
	if fingerprintErr != nil && !errors.Is(fingerprintErr, sql.ErrNoRows) {
		return fingerprintErr
	}

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM history_events WHERE imported = 1`); deleteErr != nil {
		return deleteErr
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO history_events
    (event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
     duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(event_key) DO NOTHING
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, event := range events {
		event.EventKey = strings.TrimSpace(event.EventKey)
		if _, ok := policy.HistoryCommand(event.Command, false); !ok {
			continue
		}
		event.NormalizedCommand = strings.TrimSpace(event.NormalizedCommand)
		if event.NormalizedCommand == "" {
			event.NormalizedCommand = strings.TrimSpace(event.Command)
		}
		if event.SubmittedAt.IsZero() {
			event.SubmittedAt = event.StartedAt
		}
		if event.State == "" {
			event.State = historyEventState(event)
		}
		event.Source = strings.TrimSpace(event.Source)
		if event.Occurrences <= 0 {
			event.Occurrences = 1
		}
		if event.EventKey == "" || strings.TrimSpace(event.Command) == "" || event.Source == "" {
			continue
		}
		if normalized, ok := policy.HistoryCommand(event.NormalizedCommand, false); !ok {
			continue
		} else {
			event.NormalizedCommand = normalized
		}
		if !validHistoryEventState(event.State) {
			continue
		}
		var exitCode any
		if event.HasExitCode {
			exitCode = event.ExitCode
		}
		if _, err := stmt.ExecContext(ctx, event.EventKey, event.Command, event.NormalizedCommand,
			strings.TrimSpace(event.Cwd), canonicalTimestamp(event.SubmittedAt), canonicalTimestamp(event.StartedAt),
			canonicalTimestamp(event.CompletedAt), event.Duration.Nanoseconds(), exitCode, event.Source,
			strings.TrimSpace(event.Host), strings.TrimSpace(event.SessionID), strings.TrimSpace(event.Shell), event.State,
			event.HistoryOrder, event.Occurrences); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`DELETE FROM imported_history_entries`,
		`INSERT INTO imported_history_entries
         (cmd, cwd, count, last_used, exit_code, duration_ns, source)
         WITH ranked AS (
             SELECT normalized_command, cwd, source, started_at, exit_code, duration_ns,
                    SUM(occurrences) OVER (
                        PARTITION BY normalized_command, cwd, source
                    ) AS event_count,
                    MAX(started_at) OVER (
                        PARTITION BY normalized_command, cwd, source
                    ) AS most_recent,
                    ROW_NUMBER() OVER (
                        PARTITION BY normalized_command, cwd, source
                        ORDER BY started_at DESC, history_order DESC, event_key DESC
                    ) AS recency_rank
             FROM history_events
             WHERE imported = 1
         )
         SELECT normalized_command, cwd, event_count, most_recent, exit_code, duration_ns, source
         FROM ranked
         WHERE recency_rank = 1`,
		`DELETE FROM imported_history`,
		`INSERT INTO imported_history (cmd, count, last_used)
         SELECT cmd, SUM(count), MAX(last_used)
         FROM imported_history_entries
         GROUP BY cmd`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value)
VALUES ('imported_history_events_fingerprint', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, fingerprint); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata WHERE key = 'imported_history_fingerprint'`); err != nil {
		return err
	}
	if err := f.recordHistoryChange(ctx, tx, "reset", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func importedHistoryEventsFingerprint(events []HistoryEvent) string {
	normalized := append([]HistoryEvent(nil), events...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].EventKey < normalized[j].EventKey
	})
	hash := sha256.New()
	for _, event := range normalized {
		_, _ = fmt.Fprintf(
			hash,
			"%d:%s:%d:%s:%d:%s:%d:%d:%d:%d:%d:%t:%d:%s:%s:%s:%s:%s:%d:%d\n",
			len(event.EventKey),
			event.EventKey,
			len(event.Command),
			event.Command,
			len(event.NormalizedCommand),
			event.NormalizedCommand,
			event.SubmittedAt.UnixNano(),
			event.StartedAt.UnixNano(),
			event.CompletedAt.UnixNano(),
			event.Duration.Nanoseconds(),
			event.ExitCode,
			event.HasExitCode,
			len(event.Cwd),
			event.Cwd,
			event.Source,
			event.Host+"\x00"+event.SessionID,
			event.Shell,
			event.State,
			event.HistoryOrder,
			event.Occurrences,
		)
	}
	return "events-v2:" + hex.EncodeToString(hash.Sum(nil))
}

func (f *FrecencyStore) QueryHistoryEvents(ctx context.Context, limit int) ([]HistoryEvent, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 10_000
	}

	rows, err := f.db.QueryContext(ctx, `
SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
       duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported
FROM history_events
ORDER BY started_at DESC, history_order DESC, event_key DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanHistoryEvents(rows)
}

func scanHistoryEvents(rows *sql.Rows) ([]HistoryEvent, error) {
	var events []HistoryEvent
	for rows.Next() {
		var event HistoryEvent
		var submittedAtRaw, startedAtRaw, completedAtRaw string
		var durationNS int64
		var exitCode *int
		var imported int
		if err := rows.Scan(&event.EventKey, &event.Command, &event.NormalizedCommand, &event.Cwd,
			&submittedAtRaw, &startedAtRaw, &completedAtRaw, &durationNS, &exitCode, &event.Source,
			&event.Host, &event.SessionID, &event.Shell, &event.State, &event.HistoryOrder, &event.Occurrences, &imported); err != nil {
			return nil, err
		}
		event.SubmittedAt = parseKnownTimestamp(submittedAtRaw)
		event.StartedAt = parseKnownTimestamp(startedAtRaw)
		event.CompletedAt = parseKnownTimestamp(completedAtRaw)
		event.Duration = time.Duration(durationNS)
		if exitCode != nil {
			event.ExitCode = *exitCode
			event.HasExitCode = true
		}
		event.Imported = imported != 0
		events = append(events, event)
	}
	return events, rows.Err()
}

func (f *FrecencyStore) QueryAllHistoryEvents(ctx context.Context) ([]HistoryEvent, error) {
	return f.QueryHistoryEvents(ctx, int(^uint(0)>>1))
}

func (f *FrecencyStore) QueryHistoryEventsByKeys(ctx context.Context, eventKeys []string) ([]HistoryEvent, error) {
	if f == nil || len(eventKeys) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	seen := make(map[string]bool, len(eventKeys))
	keys := make([]string, 0, len(eventKeys))
	for _, eventKey := range eventKeys {
		eventKey = strings.TrimSpace(eventKey)
		if eventKey == "" || seen[eventKey] {
			continue
		}
		seen[eventKey] = true
		keys = append(keys, eventKey)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	arguments := make([]any, len(keys))
	for index := range keys {
		arguments[index] = keys[index]
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
       duration_ns, exit_code, source, host, session_id, shell, state, history_order, occurrences, imported
FROM history_events
WHERE event_key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+`)
ORDER BY started_at ASC, history_order ASC, event_key ASC
`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHistoryEvents(rows)
}
