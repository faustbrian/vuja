package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type historyCounterpartSource struct {
	identity, provenance string
	persisted            bool
}

// Unlike explicit imported-source group deletion, an inferred counterpart
// must not exclude a different execution with conflicting outcome/duration.
func historyCounterpartProvenance(event HistoryEvent) string {
	evidence := historyEvidence(event, true)
	sum := sha256.Sum256(fmt.Appendf(nil, "counterpart-source\x00%s\x00%s\x00%s\x00%s\x00%d\x00%t\x00%d",
		event.Source, evidence.command, evidence.cwd, canonicalTimestamp(evidence.startedAt),
		evidence.duration, evidence.exitCode.Valid, evidence.exitCode.Int64))
	return hex.EncodeToString(sum[:])
}

// Membership is part of the existing canonical import/deletion transaction,
// not a replay log. Exact source provenance survives position-derived ID changes.
func loadHistoryCounterpartSources(ctx context.Context, tx *sql.Tx, slots []historyCounterpartSlot) error {
	owners := make(map[string][]int)
	var keys []string
	for index, slot := range slots {
		if _, exists := owners[slot.identity]; !exists {
			keys = append(keys, slot.identity)
		}
		owners[slot.identity] = append(owners[slot.identity], index)
	}
	for start := 0; start < len(keys); start += 128 {
		batch := keys[start:min(start+128, len(keys))]
		args := make([]any, len(batch))
		for index, key := range batch {
			args[index] = key
		}
		rows, err := tx.QueryContext(ctx, `SELECT owner_hash, source_hash, identity_hash, provenance_hash
FROM history_counterpart_sources WHERE owner_hash IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var owner, source string
			var member historyCounterpartSource
			if err := rows.Scan(&owner, &source, &member.identity, &member.provenance); err != nil {
				_ = rows.Close()
				return err
			}
			member.persisted = true
			for _, index := range owners[owner] {
				if slots[index].sources == nil {
					slots[index].sources = make(map[string]historyCounterpartSource)
				}
				slots[index].sources[source] = member
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func historyEventCounterpartSources(ctx context.Context, tx *sql.Tx, events []HistoryEvent) (map[string]map[string]historyCounterpartSource, error) {
	slots := make([]historyCounterpartSlot, len(events))
	for index, event := range events {
		slots[index].identity = forgottenHistoryIdentity(event.EventKey)
	}
	if err := loadHistoryCounterpartSources(ctx, tx, slots); err != nil {
		return nil, err
	}
	result := make(map[string]map[string]historyCounterpartSource)
	for index, slot := range slots {
		if len(slot.sources) > 0 {
			result[events[index].EventKey] = slot.sources
		}
	}
	return result, nil
}

func recordHistoryCounterpartSources(ctx context.Context, tx *sql.Tx, slots []historyCounterpartSlot, events []HistoryEvent, matches map[int]int) error {
	visited := make(map[int]bool)
	for _, slotIndex := range matches {
		if visited[slotIndex] {
			continue
		}
		visited[slotIndex] = true
		slot := slots[slotIndex]
		if !slot.forgotten && !slot.preferred {
			continue // A reservation is not an observed alias.
		}
		for source, member := range slot.sources {
			if !member.persisted {
				if _, err := tx.ExecContext(ctx, `INSERT INTO history_counterpart_sources
(owner_hash, source_hash, identity_hash, provenance_hash) VALUES (?, ?, ?, ?)
ON CONFLICT(owner_hash, source_hash) DO UPDATE SET
identity_hash = excluded.identity_hash, provenance_hash = excluded.provenance_hash
WHERE history_counterpart_sources.identity_hash = excluded.identity_hash
   OR history_counterpart_sources.provenance_hash = excluded.provenance_hash`,
					slot.identity, source, member.identity, member.provenance); err != nil {
					return err
				}
			}
			if slot.forgotten {
				if _, err := tx.ExecContext(ctx, `INSERT INTO forgotten_history_events (identity_hash) VALUES (?), (?) ON CONFLICT DO NOTHING`, member.identity, member.provenance); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func excludeKnownHistoryCounterparts(ctx context.Context, tx *sql.Tx, eventKey string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO forgotten_history_events (identity_hash)
SELECT identity_hash FROM history_counterpart_sources WHERE owner_hash = ?
UNION SELECT provenance_hash FROM history_counterpart_sources WHERE owner_hash = ?
ON CONFLICT DO NOTHING`, forgottenHistoryIdentity(eventKey), forgottenHistoryIdentity(eventKey))
	return err
}

// Non-forgotten owners that leave canonical retention do not need memberships.
// Forgotten owners retain only their opaque source-slot fence.
func removeHistoryCounterpartSources(ctx context.Context, tx *sql.Tx, keys []string) error {
	for start := 0; start < len(keys); start += 128 {
		batch := keys[start:min(start+128, len(keys))]
		args := make([]any, len(batch))
		for index, key := range batch {
			args[index] = forgottenHistoryIdentity(key)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_counterpart_sources
WHERE owner_hash IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
AND NOT EXISTS (SELECT 1 FROM forgotten_history_events WHERE identity_hash = owner_hash)`, args...); err != nil {
			return err
		}
	}
	return nil
}
