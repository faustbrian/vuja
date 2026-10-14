package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/faustbrian/vuja/spec"
)

// One guard follows one writer transaction. Call it before the first canonical
// mutation that can require a projection rebuild; counterpart reconciliation
// invokes it lazily only when it will remove an already-stored execution.
// Later removals in that transaction must not probe partially changed events
// against projections that intentionally still describe the original state.
func newHistoryLearningGuard(ctx context.Context, tx *sql.Tx) func() error {
	validated := false
	return func() error {
		if validated {
			return nil
		}
		if err := preflightHistoryLearning(ctx, tx); err != nil {
			return err
		}
		validated = true
		return nil
	}
}

// Probe the shared rebuild before changing canonical evidence, rather than
// guessing attribution or adding another learning authority. Missing projections
// are harmless; every stored semantic row must be exactly reconstructible.
// Always roll the probe back before insertion, removal or privacy reconciliation.
func preflightHistoryLearning(ctx context.Context, tx *sql.Tx) error {
	// Compare a rolling-window cache at its recorded projection clock. The
	// committed rebuild can then expire old visits normally. Refreshing before
	// this snapshot could hide unowned evidence when a missing/rewound clock
	// makes the directory owner fall back to a destructive full rebuild.
	projectedAt := time.Now()
	var projectionClock string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = ?", directoryProjectionClockMetadata).Scan(&projectionClock); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if stored := parseKnownTimestamp(projectionClock); !stored.IsZero() {
		projectedAt = stored
	}
	before, err := historyLearningRows(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT history_learning"); err != nil {
		return err
	}
	if err := rebuildHistoryProjectionsAfterRemovalAt(ctx, tx, projectedAt); err != nil {
		return err // The caller rolls back the complete transaction on failure.
	}
	after, err := historyLearningRows(ctx, tx)
	if err != nil {
		return err
	}
	var legacyClocks map[historyLegacyClockKey]bool
	if missingHistoryLearning(before, after, nil) != "" {
		// Modern and already-normalized projections need no historical
		// compatibility scan or alternative fingerprints on later removals.
		legacyClocks, err = legacyHistoryRecordClocks(ctx, tx)
		if err != nil {
			return err
		}
		if len(legacyClocks) > 0 {
			after, err = historyLearningRowsWithLegacyClocks(ctx, tx, legacyClocks)
			if err != nil {
				return err
			}
		}
	}
	var currentOnly map[string]map[[32]byte]int
	if missingHistoryLearning(before, after, nil) != "" {
		// Lifecycle migration inserts canonical legacy executions without
		// filling every projection. A later modern completion can therefore
		// leave a row describing modern evidence only. Recognize that exact
		// known writer state using the same projector, not guessed counters.
		const legacyEvidence = `imported = 0 AND source = 'legacy-vuja' AND session_id = 'legacy-migration'`
		var legacy bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM history_events WHERE "+legacyEvidence+")").Scan(&legacy); err != nil {
			return err
		}
		if legacy {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO history_learning"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM history_events WHERE "+legacyEvidence); err != nil {
				return err
			}
			if err := rebuildHistoryProjectionsAfterRemovalAt(ctx, tx, projectedAt); err != nil {
				return err
			}
			currentOnly, err = historyLearningRowsWithLegacyClocks(ctx, tx, legacyClocks)
			if err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO history_learning"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE history_learning"); err != nil {
		return err
	}
	if table := missingHistoryLearning(before, after, currentOnly); table != "" {
		return fmt.Errorf("%w: existing unattributed learning in %s cannot be preserved", ErrHistoryUnattributedLearning, table)
	}
	return nil
}

func missingHistoryLearning(before, rebuilt, currentOnly map[string]map[[32]byte]int) string {
	for table, rows := range before {
		for row, count := range rows {
			if rebuilt[table][row] != count && currentOnly[table][row] != count {
				return table
			}
		}
	}
	return ""
}

// Hash typed semantic rows only in memory. Local auto-increment IDs are not
// learning identities and naturally change during reconstruction. This compares
// the shared projection owner without persisting another copy of command text.
func historyLearningRows(ctx context.Context, tx *sql.Tx) (map[string]map[[32]byte]int, error) {
	return historyLearningRowsWithLegacyClocks(ctx, tx, nil)
}

// The old Record producer encoded its nanosecond clock in its opaque ID but
// wrote these four projections with SQLite's independent second clock. Its
// one-second transaction deadline admits that second or the next one. Match
// only this persisted writer shape, never round modern precision globally.
type historyLegacyClockKey struct {
	table, cwd, name, value, stamp string
	position                       int64
}

func legacyHistoryRecordClocks(ctx context.Context, tx *sql.Tx) (map[historyLegacyClockKey]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_key, normalized_command, cwd, state, CAST(submitted_at AS TEXT), CAST(started_at AS TEXT), CAST(completed_at AS TEXT)
FROM history_events WHERE event_key GLOB 'vuja:compat:[0-9]*'
AND imported = 0 AND source = 'vuja' AND session_id = '' AND host = '' AND shell = ''
AND normalized_command = command
AND occurrences = 1 AND aggregate = 0
AND ((state = 'completed' AND exit_code = 0) OR (state = 'failed' AND exit_code <> 0))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clocks := make(map[historyLegacyClockKey]bool)
	for rows.Next() {
		var key, command, cwd, state, submitted, started, completed string
		if err := rows.Scan(&key, &command, &cwd, &state, &submitted, &started, &completed); err != nil {
			return nil, err
		}
		parts := strings.Split(strings.TrimPrefix(key, "vuja:compat:"), ":")
		if len(parts) != 3 {
			continue
		}
		pid, pidErr := strconv.ParseUint(parts[0], 10, 64)
		nanos, nanoErr := strconv.ParseInt(parts[1], 10, 64)
		sequence, sequenceErr := strconv.ParseUint(parts[2], 10, 64)
		if pidErr != nil || nanoErr != nil || sequenceErr != nil || pid == 0 || sequence == 0 || fmt.Sprintf("%d:%d:%d", pid, nanos, sequence) != strings.Join(parts, ":") {
			continue
		}
		stamp := time.Unix(0, nanos).UTC()
		if !stamp.After(time.Unix(0, 0)) || !parseKnownTimestamp(submitted).Equal(stamp) || !parseKnownTimestamp(started).Equal(stamp) || !parseKnownTimestamp(completed).Equal(stamp) {
			continue
		}
		identity := historyLegacyClockKey{cwd: cwd, name: command, stamp: stamp.Format(time.RFC3339Nano)}
		for _, table := range []string{"history_entries", "command_outcomes"} {
			identity.table = table
			clocks[identity] = true
		}
		if state == "failed" {
			identity.table = "recent_failures"
			clocks[identity] = true
		} else {
			// Qualify row identities from the historical producer's argument
			// shape only. Counts/outcomes still come from the shared projector.
			for position, tokens := 1, spec.Tokenize(command); position < len(tokens); position++ {
				value := strings.TrimSpace(tokens[position])
				scope := strings.TrimSpace(strings.Join(tokens[:position], " "))
				if value == "" || strings.HasPrefix(value, "-") || scope == "" || len(scope) > 512 || len(value) > 512 {
					continue
				}
				clocks[historyLegacyClockKey{table: "argument_values", cwd: cwd, name: scope, value: value, position: int64(position), stamp: identity.stamp}] = true
			}
		}
	}
	return clocks, rows.Err()
}

func historyLearningRowsWithLegacyClocks(ctx context.Context, tx *sql.Tx, legacyClocks map[historyLegacyClockKey]bool) (map[string]map[[32]byte]int, error) {
	result := make(map[string]map[[32]byte]int)
	for _, table := range []string{
		"history_entries", "command_outcomes", "imported_history_entries", "imported_history",
		"command_transitions", "exact_command_transitions", "history_feedback_events",
		"suggestion_feedback", "argument_values", "recent_failures",
		"directory_navigation_visits", "directory_navigation_sources", "directory_navigation_index",
	} {
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		values, targets := make([]any, len(columns)), make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		hashes := make(map[[32]byte]int)
		for rows.Next() {
			if err := rows.Scan(targets...); err != nil {
				_ = rows.Close()
				return nil, err
			}
			semantic := make([]any, 0, len(values))
			legacyIndex := -1
			var legacyStamp time.Time
			for index, column := range columns {
				if column != "id" {
					value := values[index]
					// Legacy TEXT dates and modern TIMESTAMP dates can encode
					// the same instant differently. Compare time semantics,
					// retaining unparseable values exactly rather than guessing.
					if column == "last_used" || column == "failed_at" {
						if stamp, ok := value.(time.Time); ok {
							value = stamp.Round(0).UTC().Format(time.RFC3339Nano)
						} else if text, ok := value.(string); ok {
							if stamp, err := parseTimestamp(text); err == nil {
								value = stamp.Round(0).UTC().Format(time.RFC3339Nano)
							}
						}
					}
					semantic = append(semantic, value)
					if (table == "history_entries" || table == "command_outcomes" || table == "argument_values" || table == "recent_failures") && (column == "last_used" || column == "failed_at") {
						if stamp, ok := value.(string); ok && len(legacyClocks) > 0 && legacyClocks[historyLearningClockRowKey(table, columns, values, stamp)] {
							legacyIndex = len(semantic) - 1
							legacyStamp, _ = time.Parse(time.RFC3339Nano, stamp)
						}
					}
				}
			}
			data, err := json.Marshal(semantic)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			hashes[sha256.Sum256(data)]++
			if legacyIndex >= 0 {
				for _, coarse := range []time.Time{legacyStamp.Truncate(time.Second), legacyStamp.Truncate(time.Second).Add(time.Second)} {
					if coarse.Equal(legacyStamp) {
						continue
					}
					semantic[legacyIndex] = coarse.Format(time.RFC3339Nano)
					encoded, err := json.Marshal(semantic)
					if err != nil {
						_ = rows.Close()
						return nil, err
					}
					hashes[sha256.Sum256(encoded)]++
				}
			}
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result[table] = hashes
	}
	return result, nil
}

func historyLearningClockRowKey(table string, columns []string, values []any, stamp string) historyLegacyClockKey {
	key := historyLegacyClockKey{table: table, stamp: stamp}
	for index, column := range columns {
		switch column {
		case "cmd", "scope":
			key.name, _ = values[index].(string)
		case "cwd":
			key.cwd, _ = values[index].(string)
		case "value":
			key.value, _ = values[index].(string)
		case "position":
			key.position, _ = values[index].(int64)
		}
	}
	return key
}
