package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type HistoryEvent struct {
	EventKey    string
	Command     string
	Cwd         string
	StartedAt   time.Time
	Duration    time.Duration
	ExitCode    int
	HasExitCode bool
	Source      string
	Host        string
	SessionID   string
	Imported    bool
}

func (f *FrecencyStore) RecordHistoryEvent(ctx context.Context, event HistoryEvent) error {
	if f == nil {
		return nil
	}
	event.EventKey = strings.TrimSpace(event.EventKey)
	event.Command = strings.TrimSpace(event.Command)
	event.Source = strings.TrimSpace(event.Source)
	if event.EventKey == "" || event.Command == "" || event.Source == "" {
		return errors.New("history event key, command, and source are required")
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
	_, err := f.db.ExecContext(ctx, `
INSERT INTO history_events
    (event_key, command, cwd, started_at, duration_ns, exit_code, source, host, session_id, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(event_key) DO NOTHING
	`, event.EventKey, event.Command, strings.TrimSpace(event.Cwd), canonicalTimestamp(event.StartedAt),
		event.Duration.Nanoseconds(), exitCode, event.Source, strings.TrimSpace(event.Host),
		strings.TrimSpace(event.SessionID))
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
    (event_key, command, cwd, started_at, duration_ns, exit_code, source, host, session_id, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(event_key) DO UPDATE SET
    command = excluded.command,
    cwd = excluded.cwd,
    started_at = excluded.started_at,
    duration_ns = excluded.duration_ns,
    exit_code = excluded.exit_code,
    source = excluded.source,
    host = excluded.host,
    session_id = excluded.session_id,
    imported = 1
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, event := range events {
		event.EventKey = strings.TrimSpace(event.EventKey)
		event.Command = strings.TrimSpace(event.Command)
		event.Source = strings.TrimSpace(event.Source)
		if event.EventKey == "" || event.Command == "" || event.Source == "" {
			continue
		}
		var exitCode any
		if event.HasExitCode {
			exitCode = event.ExitCode
		}
		if _, err := stmt.ExecContext(ctx, event.EventKey, event.Command, strings.TrimSpace(event.Cwd),
			canonicalTimestamp(event.StartedAt), event.Duration.Nanoseconds(), exitCode, event.Source,
			strings.TrimSpace(event.Host), strings.TrimSpace(event.SessionID)); err != nil {
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
			"%d:%s:%d:%s:%d:%d:%d:%t:%d:%s:%s:%s\n",
			len(event.EventKey),
			event.EventKey,
			len(event.Command),
			event.Command,
			event.StartedAt.UnixNano(),
			event.Duration.Nanoseconds(),
			event.ExitCode,
			event.HasExitCode,
			len(event.Cwd),
			event.Cwd,
			event.Source,
			event.Host+"\x00"+event.SessionID,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
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
SELECT event_key, command, cwd, started_at, duration_ns, exit_code, source, host, session_id, imported
FROM history_events
ORDER BY started_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []HistoryEvent
	for rows.Next() {
		var event HistoryEvent
		var startedAtRaw string
		var durationNS int64
		var exitCode *int
		var imported int
		if err := rows.Scan(&event.EventKey, &event.Command, &event.Cwd, &startedAtRaw, &durationNS,
			&exitCode, &event.Source, &event.Host, &event.SessionID, &imported); err != nil {
			return nil, err
		}
		event.StartedAt = parseKnownTimestamp(startedAtRaw)
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
