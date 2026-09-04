package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/spec"
	_ "modernc.org/sqlite"
)

type FrecencyEntry struct {
	Cmd      string
	Cwd      string
	Count    int
	LastUsed time.Time
	RawScore float64
}

type TransitionEntry struct {
	PrevSkeleton string
	NextSkeleton string
	Cwd          string
	Count        int
	LastUsed     time.Time
}

type ExactTransitionEntry struct {
	PrevCommand string
	NextCommand string
	Cwd         string
	Count       int
	LastUsed    time.Time
}

type ImportedHistoryEntry struct {
	Command     string
	Cwd         string
	Count       int
	LastUsed    time.Time
	ExitCode    int
	HasExitCode bool
	Duration    time.Duration
	Source      string
}

type DirectoryImport struct {
	Path        string
	Source      string
	Count       int
	RecentCount int
	LastUsed    time.Time
}

type FeedbackEntry struct {
	Cmd       string
	Cwd       string
	Accepted  int
	Typed     int
	Edited    int
	Dismissed int
}

type OutcomeEntry struct {
	Cmd       string
	Successes int
	Failures  int
}

type ArgumentValueEntry struct {
	Value    string
	Count    int
	LastUsed time.Time
	Affinity int
}

type RecentFailure struct {
	Command  string
	ExitCode int
	FailedAt time.Time
}

type FrecencyStore struct {
	db                      *sql.DB
	dbPath                  string
	writeGate               chan struct{}
	recordSequence          atomic.Uint64
	historySnapshotSequence atomic.Int64
	historyOriginMu         sync.RWMutex
	historyOrigin           string
}

func NewFrecencyStore(dbPath string) (*FrecencyStore, error) {
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dbPath = filepath.Join(home, ".local", "share", "vuja", "history.db")
	}

	dir := filepath.Dir(dbPath)
	if err := config.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("failed to create directory for history.db: %w", err)
	}

	f, err := config.OpenPrivateFile(dbPath, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("failed to protect history.db: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("failed to close history.db: %w", err)
	}
	privateDatabaseFiles := []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
	if err := config.RestrictPrivateFiles(privateDatabaseFiles...); err != nil {
		return nil, err
	}

	databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath)}
	parameters := url.Values{}
	parameters.Add("_pragma", "journal_mode(WAL)")
	parameters.Add("_pragma", "synchronous(FULL)")
	parameters.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = parameters.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	// WAL permits concurrent readers while command recording remains serialized
	// by the context-aware write gate. Suggestion queries do not queue behind one
	// another.
	db.SetMaxOpenConns(4)

	store := &FrecencyStore{db: db, dbPath: dbPath, writeGate: make(chan struct{}, 1)}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := config.RestrictPrivateFiles(privateDatabaseFiles...); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (f *FrecencyStore) acquireWrite(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case f.writeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *FrecencyStore) releaseWrite() { <-f.writeGate }

func (f *FrecencyStore) configureSQLite(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, "PRAGMA journal_mode = WAL; PRAGMA synchronous = FULL; PRAGMA busy_timeout = 5000;")
	return err
}

func (f *FrecencyStore) initSchema(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Concurrent Vuja windows may serialize briefly on WAL/schema setup, and a
	// one-time lifecycle backup can legitimately exceed an interactive query
	// budget. Initialization is not a keystroke operation; allow the database
	// busy timeout and migration boundary to finish instead of permanently
	// disabling history for the new managed session.
	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := f.configureSQLite(ctxTimeout); err != nil {
		return err
	}

	schema := `
CREATE TABLE IF NOT EXISTS history_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cmd TEXT NOT NULL,
    cwd TEXT NOT NULL,
    count INTEGER DEFAULT 1,
    last_used TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(cmd, cwd)
);

CREATE INDEX IF NOT EXISTS idx_history_cwd_cmd ON history_entries(cwd, cmd);
CREATE INDEX IF NOT EXISTS idx_history_cmd_nocase ON history_entries(cmd COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS command_transitions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    prev_skeleton TEXT NOT NULL,
    next_skeleton TEXT NOT NULL,
    cwd           TEXT NOT NULL,
    count         INTEGER DEFAULT 1,
    last_used     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(prev_skeleton, next_skeleton, cwd)
);

CREATE INDEX IF NOT EXISTS idx_transitions_prev_cwd ON command_transitions(prev_skeleton, cwd);

CREATE TABLE IF NOT EXISTS exact_command_transitions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    prev_command TEXT NOT NULL,
    next_command TEXT NOT NULL,
    cwd          TEXT NOT NULL,
    count        INTEGER DEFAULT 1,
    last_used    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(prev_command, next_command, cwd)
);

CREATE INDEX IF NOT EXISTS idx_exact_transitions_prev_cwd ON exact_command_transitions(prev_command, cwd);

CREATE TABLE IF NOT EXISTS imported_history (
    cmd       TEXT PRIMARY KEY,
    count     INTEGER NOT NULL,
    last_used TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS imported_history_entries (
    cmd           TEXT NOT NULL,
    cwd           TEXT NOT NULL DEFAULT '',
    count         INTEGER NOT NULL,
    last_used     TEXT NOT NULL,
    exit_code     INTEGER,
    duration_ns   INTEGER NOT NULL DEFAULT 0,
    source        TEXT NOT NULL DEFAULT 'shell',
    UNIQUE(cmd, cwd, source)
);

CREATE INDEX IF NOT EXISTS idx_imported_history_cwd_cmd
ON imported_history_entries(cwd, cmd);
CREATE INDEX IF NOT EXISTS idx_imported_history_cmd_nocase
ON imported_history_entries(cmd COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS history_events (
    event_key         TEXT PRIMARY KEY,
    command           TEXT NOT NULL,
    normalized_command TEXT NOT NULL DEFAULT '',
    cwd               TEXT NOT NULL DEFAULT '',
    submitted_at      TIMESTAMP NOT NULL DEFAULT '',
    started_at        TIMESTAMP NOT NULL,
    completed_at      TIMESTAMP NOT NULL DEFAULT '',
    duration_ns       INTEGER NOT NULL DEFAULT 0,
    exit_code         INTEGER,
    source            TEXT NOT NULL,
    host              TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL DEFAULT '',
    shell             TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL DEFAULT 'unknown',
    history_order     INTEGER NOT NULL DEFAULT 0,
    occurrences       INTEGER NOT NULL DEFAULT 1,
    imported          INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_history_events_started_at
ON history_events(started_at DESC);

CREATE TABLE IF NOT EXISTS suggestion_feedback (
    cmd       TEXT NOT NULL,
    cwd       TEXT NOT NULL,
    accepted  INTEGER NOT NULL DEFAULT 0,
    typed     INTEGER NOT NULL DEFAULT 0,
    edited    INTEGER NOT NULL DEFAULT 0,
    dismissed INTEGER NOT NULL DEFAULT 0,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(cmd, cwd)
);

CREATE INDEX IF NOT EXISTS idx_suggestion_feedback_cwd_cmd
ON suggestion_feedback(cwd, cmd);
CREATE INDEX IF NOT EXISTS idx_suggestion_feedback_cmd_nocase
ON suggestion_feedback(cmd COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS command_outcomes (
    cmd       TEXT NOT NULL,
    cwd       TEXT NOT NULL,
    successes INTEGER NOT NULL DEFAULT 0,
    failures  INTEGER NOT NULL DEFAULT 0,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(cmd, cwd)
);

CREATE INDEX IF NOT EXISTS idx_command_outcomes_cwd_cmd
ON command_outcomes(cwd, cmd);
CREATE INDEX IF NOT EXISTS idx_command_outcomes_cmd_nocase
ON command_outcomes(cmd COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS directory_index (
    path      TEXT PRIMARY KEY,
    count     INTEGER NOT NULL DEFAULT 1,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS directory_sources (
    path      TEXT NOT NULL,
    source    TEXT NOT NULL,
    count     INTEGER NOT NULL DEFAULT 1,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(path, source)
);

CREATE INDEX IF NOT EXISTS idx_directory_sources_path
ON directory_sources(path);

-- Navigation evidence is intentionally separate from the legacy directory
-- activity tables. The old tables counted every command executed inside a
-- directory, which made busy but stale repositories look like frequently
-- selected cd targets.
CREATE TABLE IF NOT EXISTS directory_navigation_index (
    path         TEXT PRIMARY KEY,
    count        INTEGER NOT NULL DEFAULT 1,
    recent_count INTEGER NOT NULL DEFAULT 1,
    last_used    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS directory_navigation_sources (
    path         TEXT NOT NULL,
    source       TEXT NOT NULL,
    count        INTEGER NOT NULL DEFAULT 1,
    recent_count INTEGER NOT NULL DEFAULT 0,
    last_used    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(path, source)
);

CREATE INDEX IF NOT EXISTS idx_directory_navigation_sources_path
ON directory_navigation_sources(path);

CREATE TABLE IF NOT EXISTS argument_values (
    scope     TEXT NOT NULL,
    position  INTEGER NOT NULL,
    value     TEXT NOT NULL,
    cwd       TEXT NOT NULL,
    count     INTEGER NOT NULL DEFAULT 1,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(scope, position, value, cwd)
);

CREATE INDEX IF NOT EXISTS idx_argument_values_scope
ON argument_values(scope, position, value);

CREATE TABLE IF NOT EXISTS recent_failures (
    cwd       TEXT PRIMARY KEY,
    cmd       TEXT NOT NULL,
    exit_code INTEGER NOT NULL,
    failed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`
	_, err := f.db.ExecContext(ctxTimeout, schema)
	if err != nil {
		return err
	}
	return f.ensureHistoryEventLifecycleColumns(ctxTimeout)
}

func (f *FrecencyStore) ensureHistoryEventLifecycleColumns(ctx context.Context) error {
	migrationRequired, err := f.historyLifecycleMigrationRequired(ctx)
	if err != nil {
		return err
	}
	if migrationRequired {
		if err := f.backupHistoryDatabaseBeforeLifecycleMigration(ctx); err != nil {
			return err
		}
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if migrationRequired {
		// Acquire SQLite's cross-process writer lock before inspecting the
		// lifecycle schema. Without this, two new terminal windows can both
		// open deferred read transactions and then race while upgrading them.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value)
VALUES ('history_lifecycle_migration_lock', '1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(history_events)`)
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if scanErr := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	additions := []struct {
		name string
		sql  string
	}{
		{"normalized_command", `ALTER TABLE history_events ADD COLUMN normalized_command TEXT NOT NULL DEFAULT ''`},
		{"submitted_at", `ALTER TABLE history_events ADD COLUMN submitted_at TIMESTAMP NOT NULL DEFAULT ''`},
		{"completed_at", `ALTER TABLE history_events ADD COLUMN completed_at TIMESTAMP NOT NULL DEFAULT ''`},
		{"shell", `ALTER TABLE history_events ADD COLUMN shell TEXT NOT NULL DEFAULT ''`},
		{"state", `ALTER TABLE history_events ADD COLUMN state TEXT NOT NULL DEFAULT 'unknown'`},
		{"history_order", `ALTER TABLE history_events ADD COLUMN history_order INTEGER NOT NULL DEFAULT 0`},
		{"occurrences", `ALTER TABLE history_events ADD COLUMN occurrences INTEGER NOT NULL DEFAULT 1`},
	}
	for _, addition := range additions {
		if columns[addition.name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, addition.sql); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE history_events
SET normalized_command = TRIM(command)
WHERE normalized_command = '';
UPDATE history_events
SET submitted_at = started_at
WHERE submitted_at = '';
UPDATE history_events
SET completed_at = started_at,
    state = CASE WHEN exit_code = 0 THEN 'completed' ELSE 'failed' END
WHERE state = 'unknown' AND exit_code IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_history_events_normalized_command
ON history_events(normalized_command COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_history_events_state_session
ON history_events(state, session_id);

CREATE TABLE IF NOT EXISTS history_changes (
    sequence   INTEGER PRIMARY KEY AUTOINCREMENT,
    origin     TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL,
    event_key  TEXT NOT NULL DEFAULT ''
);
`); err != nil {
		return err
	}
	var schemaVersion string
	versionErr := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'history_schema_version'`).Scan(&schemaVersion)
	if versionErr != nil && !errors.Is(versionErr, sql.ErrNoRows) {
		return versionErr
	}
	version, parseErr := strconv.Atoi(schemaVersion)
	if versionErr != nil || parseErr != nil || version < 2 {
		if err := migrateLegacyHistoryAggregates(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO metadata (key, value)
VALUES ('history_schema_version', '2')
ON CONFLICT(key) DO UPDATE SET value = excluded.value;
DELETE FROM metadata WHERE key = 'history_lifecycle_migration_lock';
`); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *FrecencyStore) historyLifecycleMigrationRequired(ctx context.Context) (bool, error) {
	var schemaVersion string
	err := f.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'history_schema_version'`).Scan(&schemaVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	version, parseErr := strconv.Atoi(schemaVersion)
	return err != nil || parseErr != nil || version < 2, nil
}

func (f *FrecencyStore) backupHistoryDatabaseBeforeLifecycleMigration(ctx context.Context) error {
	var historyRows int
	for _, table := range []string{
		"history_events", "history_entries", "imported_history_entries", "imported_history",
		"command_outcomes", "suggestion_feedback", "recent_failures", "exact_command_transitions",
		"command_transitions", "argument_values",
	} {
		var count int
		if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		historyRows += count
	}
	if historyRows == 0 {
		return nil
	}
	backupPath := f.dbPath + ".pre-lifecycle-v2.bak"
	if _, err := os.Stat(backupPath); err == nil {
		return config.RestrictPrivateFiles(backupPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	temporaryPath := fmt.Sprintf("%s.tmp-%d", backupPath, os.Getpid())
	if _, err := os.Stat(temporaryPath); err == nil {
		return fmt.Errorf("history migration backup temporary file already exists: %s", temporaryPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	defer os.Remove(temporaryPath)
	quotedPath := strings.ReplaceAll(temporaryPath, "'", "''")
	if _, err := f.db.ExecContext(ctx, "VACUUM INTO '"+quotedPath+"'"); err != nil {
		return fmt.Errorf("back up history before lifecycle migration: %w", err)
	}
	if err := config.RestrictPrivateFiles(temporaryPath); err != nil {
		return err
	}
	if err := publishHistoryMigrationBackup(temporaryPath, backupPath); err != nil {
		return fmt.Errorf("publish history migration backup: %w", err)
	}
	return config.RestrictPrivateFiles(backupPath)
}

func publishHistoryMigrationBackup(temporaryPath, backupPath string) error {
	if err := os.Link(temporaryPath, backupPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return config.RestrictPrivateFiles(backupPath)
		}
		return err
	}
	return nil
}

func (f *FrecencyStore) RecordFeedback(ctx context.Context, cmd, cwd, event string) error {
	if f == nil {
		return nil
	}
	var recordable bool
	if cmd, recordable = policy.HistoryCommand(cmd, false); !recordable {
		return nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	column := map[string]string{
		"accepted":  "accepted",
		"typed":     "typed",
		"edited":    "edited",
		"dismissed": "dismissed",
	}[event]
	if column == "" {
		return fmt.Errorf("unknown suggestion feedback event %q", event)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	query := fmt.Sprintf(`
INSERT INTO suggestion_feedback (cmd, cwd, %s) VALUES (?, ?, 1)
ON CONFLICT(cmd, cwd) DO UPDATE SET %s = %s + 1, last_used = CURRENT_TIMESTAMP
`, column, column, column)
	_, err := f.db.ExecContext(ctx, query, cmd, cwd)
	return err
}

func (f *FrecencyStore) QueryFeedback(ctx context.Context, cwd, root, prefix string, limit int) ([]FeedbackEntry, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT cmd, cwd, SUM(accepted), SUM(typed), SUM(edited), SUM(dismissed)
FROM suggestion_feedback
WHERE cmd LIKE ? AND (cwd = ? OR (? != '' AND (cwd = ? OR cwd LIKE ?)))
GROUP BY cmd
ORDER BY MAX(last_used) DESC
LIMIT ?
`, strings.TrimSpace(prefix)+"%", cwd, root, root, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []FeedbackEntry
	for rows.Next() {
		var entry FeedbackEntry
		if err := rows.Scan(&entry.Cmd, &entry.Cwd, &entry.Accepted, &entry.Typed, &entry.Edited, &entry.Dismissed); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries, rows.Err()
}

func (f *FrecencyStore) RecordExactTransition(ctx context.Context, prevCommand, nextCommand, cwd string, nextExitCode int) error {
	if f == nil {
		return nil
	}
	var previousRecordable, nextRecordable bool
	prevCommand, previousRecordable = policy.HistoryCommand(prevCommand, false)
	nextCommand, nextRecordable = policy.HistoryCommand(nextCommand, false)
	if !previousRecordable || !nextRecordable {
		return nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	count := 0
	if nextExitCode == 0 {
		count = 1
	}
	_, err := f.db.ExecContext(ctxTimeout, `
INSERT INTO exact_command_transitions (prev_command, next_command, cwd, count, last_used)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(prev_command, next_command, cwd) DO UPDATE SET
    count = count + excluded.count,
    last_used = CURRENT_TIMESTAMP;
`, prevCommand, nextCommand, cwd, count)
	return err
}

func (f *FrecencyStore) QueryExactTransitionsWithFallback(ctx context.Context, prevCommand, cwd string) ([]ExactTransitionEntry, bool) {
	if f == nil {
		return nil, false
	}
	prevCommand = strings.TrimSpace(prevCommand)
	cwd = strings.TrimSpace(cwd)
	if prevCommand == "" {
		return nil, false
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	query := func(local bool) []ExactTransitionEntry {
		var rows *sql.Rows
		var err error
		if local {
			rows, err = f.db.QueryContext(ctxTimeout, `
SELECT prev_command, next_command, cwd, count, last_used
FROM exact_command_transitions
WHERE prev_command = ? AND cwd = ? AND count > 0
ORDER BY count DESC, last_used DESC, next_command ASC
`, prevCommand, cwd)
		} else {
			rows, err = f.db.QueryContext(ctxTimeout, `
SELECT prev_command, next_command, '', SUM(count), MAX(last_used)
FROM exact_command_transitions
WHERE prev_command = ? AND count > 0
GROUP BY next_command
ORDER BY SUM(count) DESC, MAX(last_used) DESC, next_command ASC
`, prevCommand)
		}
		if err != nil {
			return nil
		}
		defer rows.Close()

		var entries []ExactTransitionEntry
		for rows.Next() {
			var entry ExactTransitionEntry
			var lastUsedRaw string
			if err := rows.Scan(&entry.PrevCommand, &entry.NextCommand, &entry.Cwd, &entry.Count, &lastUsedRaw); err != nil {
				continue
			}
			entry.LastUsed = parseKnownTimestamp(lastUsedRaw)
			entries = append(entries, entry)
		}
		return entries
	}

	if entries := query(true); len(entries) > 0 {
		return entries, true
	}
	return query(false), false
}

func (f *FrecencyStore) ReplaceImportedHistory(ctx context.Context, entries []ImportedHistoryEntry) error {
	if f == nil {
		return nil
	}
	type aggregateKey struct {
		command string
		cwd     string
		source  string
	}
	aggregates := make(map[aggregateKey]ImportedHistoryEntry, len(entries))
	for _, entry := range entries {
		command, recordable := policy.HistoryCommand(entry.Command, false)
		if !recordable || entry.Count <= 0 {
			continue
		}
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			source = "shell"
		}
		key := aggregateKey{command: command, cwd: strings.TrimSpace(entry.Cwd), source: source}
		aggregate := aggregates[key]
		aggregate.Command = command
		aggregate.Cwd = key.cwd
		aggregate.Source = source
		aggregate.Count += entry.Count
		if aggregate.LastUsed.IsZero() || entry.LastUsed.After(aggregate.LastUsed) {
			aggregate.LastUsed = entry.LastUsed
			aggregate.ExitCode = entry.ExitCode
			aggregate.HasExitCode = entry.HasExitCode
			aggregate.Duration = entry.Duration
		}
		aggregates[key] = aggregate
	}
	keys := make([]aggregateKey, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		if keys[i].command != keys[j].command {
			return keys[i].command < keys[j].command
		}
		return keys[i].cwd < keys[j].cwd
	})
	events := make([]HistoryEvent, 0, len(keys))
	for index, key := range keys {
		aggregate := aggregates[key]
		startedAt := aggregate.LastUsed
		if startedAt.IsZero() {
			startedAt = time.Unix(0, 0)
		}
		state := "unknown"
		completedAt := time.Time{}
		if aggregate.HasExitCode {
			state = "completed"
			if aggregate.ExitCode != 0 {
				state = "failed"
			}
			completedAt = startedAt.Add(max(aggregate.Duration, 0))
		}
		sum := sha256.Sum256([]byte(key.source + "\x00" + key.command + "\x00" + key.cwd))
		events = append(events, HistoryEvent{
			EventKey: "imported-aggregate:" + hex.EncodeToString(sum[:16]), Command: key.command,
			NormalizedCommand: key.command, Cwd: key.cwd, SubmittedAt: startedAt, StartedAt: startedAt,
			CompletedAt: completedAt, Duration: aggregate.Duration, ExitCode: aggregate.ExitCode,
			HasExitCode: aggregate.HasExitCode, Source: key.source, State: state, HistoryOrder: index + 1,
			Occurrences: aggregate.Count, Imported: true,
		})
	}
	return f.ReplaceImportedHistoryEvents(ctx, events)
}

func importedHistoryFingerprint(entries []ImportedHistoryEntry) string {
	return importedHistoryFingerprintWithTimestamps(entries, false)
}

func importedHistoryPersistenceFingerprint(entries []ImportedHistoryEntry) string {
	return importedHistoryFingerprintWithTimestamps(entries, true)
}

func importedHistoryFingerprintWithTimestamps(entries []ImportedHistoryEntry, includeTimestamps bool) string {
	normalized := make([]ImportedHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Command = strings.TrimSpace(entry.Command)
		if entry.Command != "" && entry.Count > 0 {
			normalized = append(normalized, entry)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].LastUsed.Equal(normalized[j].LastUsed) {
			if normalized[i].Cwd != normalized[j].Cwd {
				return normalized[i].Cwd < normalized[j].Cwd
			}
			if normalized[i].Command == normalized[j].Command {
				return normalized[i].Count < normalized[j].Count
			}
			return normalized[i].Command < normalized[j].Command
		}
		return normalized[i].LastUsed.After(normalized[j].LastUsed)
	})

	var value strings.Builder
	value.WriteString("relative-recency-v3\n")
	for rank, entry := range normalized {
		timestamp := ""
		if includeTimestamps {
			timestamp = canonicalTimestamp(entry.LastUsed)
		}
		_, _ = fmt.Fprintf(
			&value,
			"%d:%d:%s:%d:%s:%s:%d:%t:%d:%s\n",
			rank,
			len(entry.Command),
			entry.Command,
			entry.Count,
			entry.Cwd,
			timestamp,
			entry.ExitCode,
			entry.HasExitCode,
			entry.Duration.Nanoseconds(),
			entry.Source,
		)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

func (f *FrecencyStore) Record(ctx context.Context, cmd, cwd string, exitCode int) error {
	if f == nil {
		return nil
	}
	var recordable bool
	if cmd, recordable = policy.HistoryCommand(cmd, false); !recordable {
		return nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()
	recordedAt := time.Now()

	var query string
	if exitCode == 0 {
		query = `
INSERT INTO history_entries (cmd, cwd, count, last_used)
VALUES (?, ?, 1, CURRENT_TIMESTAMP)
ON CONFLICT(cmd, cwd) DO UPDATE SET
    count = count + 1,
    last_used = CURRENT_TIMESTAMP;
`
	} else {
		query = `
INSERT INTO history_entries (cmd, cwd, count, last_used)
VALUES (?, ?, 1, CURRENT_TIMESTAMP)
ON CONFLICT(cmd, cwd) DO UPDATE SET
	count = count + 1,
    last_used = CURRENT_TIMESTAMP;
`
	}
	tx, err := f.db.BeginTx(ctxTimeout, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctxTimeout, query, cmd, cwd); err != nil {
		return err
	}
	success, failure := 0, 1
	if exitCode == 0 {
		success, failure = 1, 0
	}
	if _, err = tx.ExecContext(ctxTimeout, `
INSERT INTO command_outcomes (cmd, cwd, successes, failures)
VALUES (?, ?, ?, ?)
ON CONFLICT(cmd, cwd) DO UPDATE SET
    successes = successes + excluded.successes,
    failures = failures + excluded.failures,
    last_used = CURRENT_TIMESTAMP
`, cmd, cwd, success, failure); err != nil {
		return err
	}
	if exitCode == 0 {
		if _, err = tx.ExecContext(ctxTimeout, `DELETE FROM recent_failures WHERE cwd = ?`, cwd); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctxTimeout, `
INSERT INTO recent_failures (cwd, cmd, exit_code)
VALUES (?, ?, ?)
ON CONFLICT(cwd) DO UPDATE SET
    cmd = excluded.cmd,
    exit_code = excluded.exit_code,
    failed_at = CURRENT_TIMESTAMP
`, cwd, cmd, exitCode); err != nil {
			return err
		}
	}
	if exitCode == 0 {
		tokens := spec.Tokenize(cmd)
		for position := 1; position < len(tokens); position++ {
			value := strings.TrimSpace(tokens[position])
			if value == "" || strings.HasPrefix(value, "-") {
				continue
			}
			scope := strings.TrimSpace(strings.Join(tokens[:position], " "))
			if scope == "" || len(scope) > 512 || len(value) > 512 {
				continue
			}
			if _, err = tx.ExecContext(ctxTimeout, `
INSERT INTO argument_values (scope, position, value, cwd)
VALUES (?, ?, ?, ?)
ON CONFLICT(scope, position,value,cwd) DO UPDATE SET
    count = count + 1,
    last_used = CURRENT_TIMESTAMP
`, scope, position, value, cwd); err != nil {
				return err
			}
		}
	}
	state := "failed"
	if exitCode == 0 {
		state = "completed"
	}
	eventKey := fmt.Sprintf("vuja:compat:%d:%d:%d", os.Getpid(), recordedAt.UnixNano(), f.recordSequence.Add(1))
	if _, err = tx.ExecContext(ctxTimeout, `
INSERT INTO history_events
    (event_key, command, normalized_command, cwd, submitted_at, started_at, completed_at,
     duration_ns, exit_code, source, state, history_order, occurrences, imported)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, 'vuja', ?, 0, 1, 0)
`, eventKey, cmd, cmd, cwd, canonicalTimestamp(recordedAt), canonicalTimestamp(recordedAt),
		canonicalTimestamp(recordedAt), exitCode, state); err != nil {
		return err
	}
	if err := f.recordHistoryChange(ctxTimeout, tx, "upsert", eventKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *FrecencyStore) QueryRecentFailure(ctx context.Context, cwd string, maxAge time.Duration) (RecentFailure, bool) {
	if f == nil || strings.TrimSpace(cwd) == "" {
		return RecentFailure{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var failure RecentFailure
	var failedAtRaw string
	err := f.db.QueryRowContext(ctx, `
SELECT cmd, exit_code, failed_at
FROM recent_failures
WHERE cwd = ?
`, cwd).Scan(&failure.Command, &failure.ExitCode, &failedAtRaw)
	if err != nil {
		return RecentFailure{}, false
	}
	failure.FailedAt, _ = parseTimestamp(failedAtRaw)
	if maxAge > 0 && time.Since(failure.FailedAt) > maxAge {
		return RecentFailure{}, false
	}
	return failure, true
}

func (f *FrecencyStore) QueryArgumentValues(
	ctx context.Context,
	cwd string,
	root string,
	scope string,
	position int,
	partial string,
	limit int,
) ([]ArgumentValueEntry, error) {
	if f == nil || strings.TrimSpace(scope) == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT value, cwd, count, last_used
FROM argument_values
WHERE scope = ? AND position = ? AND lower(value) LIKE lower(?)
`, strings.TrimSpace(scope), position, strings.TrimSpace(partial)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byValue := make(map[string]ArgumentValueEntry)
	for rows.Next() {
		var value, entryCwd, lastUsedRaw string
		var count int
		if err := rows.Scan(&value, &entryCwd, &count, &lastUsedRaw); err != nil {
			continue
		}
		affinity := 1
		if entryCwd == cwd {
			affinity = 3
		} else if pathWithinRoot(entryCwd, root) {
			affinity = 2
		}
		lastUsed := parseKnownTimestamp(lastUsedRaw)
		entry := byValue[value]
		entry.Value = value
		entry.Count += count
		if affinity > entry.Affinity {
			entry.Affinity = affinity
		}
		if lastUsed.After(entry.LastUsed) {
			entry.LastUsed = lastUsed
		}
		byValue[value] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	entries := make([]ArgumentValueEntry, 0, len(byValue))
	for _, entry := range byValue {
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Affinity != entries[j].Affinity {
			return entries[i].Affinity > entries[j].Affinity
		}
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].LastUsed.After(entries[j].LastUsed)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func pathWithinRoot(path string, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (f *FrecencyStore) QueryOutcomes(ctx context.Context, cwd, root, prefix string, limit int) ([]OutcomeEntry, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := f.db.QueryContext(ctx, `
SELECT cmd, SUM(successes), SUM(failures)
FROM command_outcomes
WHERE cmd LIKE ? AND (cwd = ? OR (? != '' AND (cwd = ? OR cwd LIKE ?)))
GROUP BY cmd
ORDER BY MAX(last_used) DESC
LIMIT ?
`, strings.TrimSpace(prefix)+"%", cwd, root, root, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []OutcomeEntry
	for rows.Next() {
		var entry OutcomeEntry
		if err := rows.Scan(&entry.Cmd, &entry.Successes, &entry.Failures); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries, rows.Err()
}

func (f *FrecencyStore) RecordDirectory(ctx context.Context, path string) error {
	if f == nil {
		return nil
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	_, err := f.db.ExecContext(ctx, `
INSERT INTO directory_navigation_index (path) VALUES (?)
ON CONFLICT(path) DO UPDATE SET
    count = count + 1,
    recent_count = recent_count + 1,
    last_used = CURRENT_TIMESTAMP
`, path)
	return err
}

func (f *FrecencyStore) ReplaceDirectorySource(ctx context.Context, source string, entries []DirectoryImport) error {
	if f == nil {
		return nil
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return errors.New("directory source is required")
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
	if _, execErr := tx.ExecContext(ctx, `DELETE FROM directory_navigation_sources WHERE source = ?`, source); execErr != nil {
		return execErr
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO directory_navigation_sources (path, source, count, recent_count, last_used)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(path, source) DO UPDATE SET
    count = excluded.count,
	recent_count = excluded.recent_count,
    last_used = excluded.last_used
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, entry := range entries {
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if path == "." || !filepath.IsAbs(path) {
			continue
		}
		count := max(entry.Count, 1)
		lastUsed := entry.LastUsed
		if lastUsed.IsZero() {
			lastUsed = time.Unix(0, 0)
		}
		if _, err := stmt.ExecContext(ctx, path, source, count, max(entry.RecentCount, 0), canonicalTimestamp(lastUsed)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *FrecencyStore) QueryDirectories(ctx context.Context, fragment string, limit int, ranking string) ([]DirectoryImport, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 20
	}
	shortlist := min(max(limit*10, 200), 1000)
	shortlistPerSignal := max(shortlist/2, limit)
	query := `
WITH candidates AS (
	SELECT path, 'vuja' AS source, count, recent_count, last_used FROM directory_navigation_index
	UNION ALL
	SELECT path, source, count, recent_count, last_used FROM directory_navigation_sources
), frequent_candidates AS (
	SELECT path, source, count, recent_count, last_used
	FROM candidates
	WHERE lower(path) LIKE ?
	ORDER BY count DESC, last_used DESC, path ASC, source ASC
	LIMIT ?
), recent_candidates AS (
	SELECT path, source, count, recent_count, last_used
	FROM candidates
	WHERE lower(path) LIKE ?
	ORDER BY last_used DESC, count DESC, path ASC, source ASC
	LIMIT ?
), balanced_candidates AS (
	SELECT path, source, count, recent_count, last_used
	FROM candidates
	WHERE lower(path) LIKE ?
	ORDER BY recent_count DESC, last_used DESC, path ASC, source ASC
	LIMIT ?
)
SELECT path, source, count, recent_count, last_used
FROM frequent_candidates
UNION
SELECT path, source, count, recent_count, last_used
FROM recent_candidates
UNION
SELECT path, source, count, recent_count, last_used
FROM balanced_candidates
`
	pattern := "%" + strings.ToLower(strings.TrimSpace(fragment)) + "%"
	rows, err := f.db.QueryContext(
		ctx,
		query,
		pattern,
		shortlistPerSignal,
		pattern,
		shortlistPerSignal,
		pattern,
		shortlistPerSignal,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []DirectoryImport
	for rows.Next() {
		var entry DirectoryImport
		var lastUsedRaw string
		if rows.Scan(&entry.Path, &entry.Source, &entry.Count, &entry.RecentCount, &lastUsedRaw) == nil {
			entry.LastUsed = parseKnownTimestamp(lastUsedRaw)
			if !entry.LastUsed.After(time.Unix(0, 0)) {
				entry.LastUsed = time.Time{}
			}
			entries = append(entries, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	type directoryEvidence struct {
		activity  float64
		zoxide    float64
		discovery float64
	}
	evidenceByPath := make(map[string]directoryEvidence, len(entries))
	best := make(map[string]DirectoryImport, len(entries))
	for _, entry := range entries {
		score := directoryEvidenceScore(entry, ranking, now)
		evidence := evidenceByPath[entry.Path]
		switch entry.Source {
		case "zoxide":
			evidence.zoxide = max(evidence.zoxide, score)
		case "git-worktrees":
			evidence.discovery = max(evidence.discovery, score)
		default:
			// Shell imports and Vuja's live directory index can describe the
			// same executions. Take the strongest coherent activity source
			// instead of double-counting overlapping evidence.
			evidence.activity = max(evidence.activity, score)
		}
		evidenceByPath[entry.Path] = evidence
		current, exists := best[entry.Path]
		if !exists || score > directoryEvidenceScore(current, ranking, now) {
			best[entry.Path] = entry
		}
	}
	entries = entries[:0]
	for _, entry := range best {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		leftEvidence, rightEvidence := evidenceByPath[left.Path], evidenceByPath[right.Path]
		leftScore := leftEvidence.activity + leftEvidence.zoxide + leftEvidence.discovery
		rightScore := rightEvidence.activity + rightEvidence.zoxide + rightEvidence.discovery
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if !left.LastUsed.Equal(right.LastUsed) {
			return left.LastUsed.After(right.LastUsed)
		}
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		return left.Path < right.Path
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func directoryEvidenceScore(entry DirectoryImport, ranking string, now time.Time) float64 {
	switch entry.Source {
	case "git-worktrees":
		if ranking == "recent" || ranking == "frequent" {
			return 0
		}
		return 0.02
	case "zoxide":
		if ranking == "recent" {
			return 0
		}
		weight := 0.05
		if ranking == "frequent" {
			weight = 0.25
		}
		return weight * math.Log1p(float64(max(entry.Count, 0)))
	default:
		count := entry.Count
		if ranking == "balanced" && entry.Source == "vuja" {
			// Live navigation gives immediate feedback before the next persistent
			// history refresh. Cap it so a long-running installation cannot rebuild
			// the lifetime-frequency dominance the rolling history window removes.
			count = min(count, 20)
		} else if ranking == "balanced" && (entry.RecentCount > 0 || entry.Source == "history") {
			count = entry.RecentCount
		}
		return directoryRankingScore(count, entry.LastUsed, directoryFrequencyOnlyScore(count), ranking, now)
	}
}

func (f *FrecencyStore) RecordTransition(ctx context.Context, prevSkeleton, nextSkeleton, cwd string, nextExitCode int) error {
	if f == nil {
		return nil
	}
	var previousRecordable, nextRecordable bool
	prevSkeleton, previousRecordable = policy.HistoryCommand(prevSkeleton, false)
	nextSkeleton, nextRecordable = policy.HistoryCommand(nextSkeleton, false)
	if !previousRecordable || !nextRecordable {
		return nil
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	var query string
	if nextExitCode == 0 {
		query = `
INSERT INTO command_transitions (prev_skeleton, next_skeleton, cwd, count, last_used)
VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
ON CONFLICT(prev_skeleton, next_skeleton, cwd) DO UPDATE SET
    count = count + 1,
    last_used = CURRENT_TIMESTAMP;
`
	} else {
		query = `
INSERT INTO command_transitions (prev_skeleton, next_skeleton, cwd, count, last_used)
VALUES (?, ?, ?, 0, CURRENT_TIMESTAMP)
ON CONFLICT(prev_skeleton, next_skeleton, cwd) DO UPDATE SET
    last_used = CURRENT_TIMESTAMP;
`
	}
	_, err := f.db.ExecContext(ctxTimeout, query, prevSkeleton, nextSkeleton, cwd)
	return err
}

func (f *FrecencyStore) QueryTransitionsWithFallback(ctx context.Context, prevSkeleton, cwd string) ([]TransitionEntry, bool) {
	if f == nil {
		return nil, false
	}
	prevSkeleton = strings.TrimSpace(prevSkeleton)
	cwd = strings.TrimSpace(cwd)
	if prevSkeleton == "" {
		return nil, false
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	// Phase 1: Local query with depth fallback
	parts := strings.Fields(prevSkeleton)
	for len(parts) > 0 {
		key := strings.Join(parts, " ")
		var loopEntries []TransitionEntry
		func() {
			rows, err := f.db.QueryContext(ctxTimeout, `
SELECT prev_skeleton, next_skeleton, cwd, count, last_used
FROM command_transitions
WHERE prev_skeleton = ? AND cwd = ? AND count > 0
ORDER BY count DESC, last_used DESC, next_skeleton ASC
`, key, cwd)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var prev, next, rCwd string
					var count int
					var lastUsedRaw string
					if err := rows.Scan(&prev, &next, &rCwd, &count, &lastUsedRaw); err == nil {
						t := parseKnownTimestamp(lastUsedRaw)
						loopEntries = append(loopEntries, TransitionEntry{
							PrevSkeleton: prev,
							NextSkeleton: next,
							Cwd:          rCwd,
							Count:        count,
							LastUsed:     t,
						})
					}
				}
			}
		}()
		if len(loopEntries) > 0 {
			return loopEntries, true
		}
		parts = parts[:len(parts)-1]
	}

	// Phase 2: Global query with depth fallback
	parts = strings.Fields(prevSkeleton)
	for len(parts) > 0 {
		key := strings.Join(parts, " ")
		var loopEntries []TransitionEntry
		func() {
			rows, err := f.db.QueryContext(ctxTimeout, `
SELECT prev_skeleton, next_skeleton, SUM(count) as total_count, MAX(last_used) as max_last_used
FROM command_transitions
WHERE prev_skeleton = ? AND count > 0
GROUP BY next_skeleton
ORDER BY total_count DESC, max_last_used DESC, next_skeleton ASC
`, key)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var prev, next string
					var count int
					var lastUsedRaw string
					if err := rows.Scan(&prev, &next, &count, &lastUsedRaw); err == nil {
						t := parseKnownTimestamp(lastUsedRaw)
						loopEntries = append(loopEntries, TransitionEntry{
							PrevSkeleton: prev,
							NextSkeleton: next,
							Cwd:          "",
							Count:        count,
							LastUsed:     t,
						})
					}
				}
			}
		}()
		if len(loopEntries) > 0 {
			return loopEntries, false
		}
		parts = parts[:len(parts)-1]
	}

	return nil, false
}

func (f *FrecencyStore) RawScore(count int, lastUsed time.Time) float64 {
	return historyRankingScore(count, lastUsed, "balanced", time.Now())
}

func historyRankingScore(count int, lastUsed time.Time, ranking string, now time.Time) float64 {
	if count <= 0 {
		return 0
	}
	if lastUsed.IsZero() {
		if ranking == "frequent" {
			return float64(count)
		}
		return 0
	}
	age := max(now.Sub(lastUsed), 0)
	switch ranking {
	case "frequent":
		return float64(count)
	case "recent":
		return 1_000_000 / (1 + age.Hours())
	default:
		const halfLife = 45 * 24 * time.Hour
		frequency := math.Log1p(float64(count))
		decay := math.Exp(-math.Ln2 * float64(age) / float64(halfLife))
		return frequency * decay
	}
}

func (f *FrecencyStore) QueryLocal(ctx context.Context, cwd, prefix string, limit int) ([]FrecencyEntry, error) {
	if f == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	var rows *sql.Rows
	var err error
	query := `
WITH candidates AS (
    SELECT cmd, cwd, count, last_used FROM history_entries WHERE cwd = ?
    UNION ALL
    SELECT cmd, cwd, count, last_used FROM imported_history_entries WHERE cwd = ?
)
SELECT cmd, cwd, SUM(count), MAX(last_used)
FROM candidates
`
	args := []any{cwd, cwd}
	if prefix != "" {
		query += " WHERE cmd LIKE ?"
		args = append(args, prefix+"%")
	}
	query += " GROUP BY cmd, cwd"
	rows, err = f.db.QueryContext(ctxTimeout, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []FrecencyEntry
	for rows.Next() {
		var cmd, rCwd string
		var count int
		var lastUsedRaw string
		if err := rows.Scan(&cmd, &rCwd, &count, &lastUsedRaw); err != nil {
			continue
		}
		t := parseKnownTimestamp(lastUsedRaw)
		entries = append(entries, FrecencyEntry{
			Cmd:      cmd,
			Cwd:      rCwd,
			Count:    count,
			LastUsed: t,
			RawScore: f.RawScore(count, t),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return frecencyEntryLess(entries[i], entries[j])
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (f *FrecencyStore) QueryProject(ctx context.Context, root, prefix string, limit int) ([]FrecencyEntry, error) {
	if f == nil || strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	query := `
WITH candidates AS (
    SELECT cmd, cwd, count, last_used FROM history_entries
    UNION ALL
    SELECT cmd, cwd, count, last_used FROM imported_history_entries
), aggregated AS (
    SELECT cmd, SUM(count) AS count, MAX(last_used) AS last_used
    FROM candidates
    WHERE (cwd = ? OR substr(cwd, 1, length(?) + 1) = ? || ?)
    GROUP BY cmd
)
SELECT cmd, count, last_used
FROM aggregated
`
	args := []any{root, root, root, string(os.PathSeparator)}
	if prefix != "" {
		query += " WHERE cmd LIKE ?"
		args = append(args, prefix+"%")
	}

	rows, err := f.db.QueryContext(ctxTimeout, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []FrecencyEntry
	for rows.Next() {
		var cmd string
		var count int
		var lastUsedRaw string
		if err := rows.Scan(&cmd, &count, &lastUsedRaw); err != nil {
			continue
		}
		lastUsed := parseKnownTimestamp(lastUsedRaw)
		entries = append(entries, FrecencyEntry{
			Cmd:      cmd,
			Cwd:      root,
			Count:    count,
			LastUsed: lastUsed,
			RawScore: f.RawScore(count, lastUsed),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return frecencyEntryLess(entries[i], entries[j])
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (f *FrecencyStore) QueryGlobal(ctx context.Context, prefix string, limit int) ([]FrecencyEntry, error) {
	if f == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	query := `
WITH candidates AS (
    SELECT cmd, count, last_used FROM history_entries
    UNION ALL
    SELECT cmd, count, last_used FROM imported_history_entries
)
SELECT cmd, SUM(count), MAX(last_used)
FROM candidates
`
	var args []any
	if prefix != "" {
		query += " WHERE cmd LIKE ?"
		args = append(args, prefix+"%")
	}
	query += " GROUP BY cmd"
	rows, err := f.db.QueryContext(ctxTimeout, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []FrecencyEntry
	for rows.Next() {
		var cmd string
		var count int
		var lastUsedRaw string
		if err := rows.Scan(&cmd, &count, &lastUsedRaw); err != nil {
			continue
		}
		t := parseKnownTimestamp(lastUsedRaw)
		score := f.RawScore(count, t)
		entries = append(entries, FrecencyEntry{Cmd: cmd, Cwd: "", Count: count, LastUsed: t, RawScore: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return frecencyEntryLess(entries[i], entries[j])
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func frecencyEntryLess(left, right FrecencyEntry) bool {
	if left.RawScore != right.RawScore {
		return left.RawScore > right.RawScore
	}
	if !left.LastUsed.Equal(right.LastUsed) {
		return left.LastUsed.After(right.LastUsed)
	}
	if left.Count != right.Count {
		return left.Count > right.Count
	}
	return left.Cmd < right.Cmd
}

func (f *FrecencyStore) Close() error {
	if f == nil {
		return nil
	}
	if err := f.acquireWrite(context.Background()); err != nil {
		return err
	}
	defer f.releaseWrite()
	if f.db != nil {
		return f.db.Close()
	}
	return nil
}

func parseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if index := strings.Index(s, " m=+"); index >= 0 {
		s = s[:index]
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(databaseTimestampLayout, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

func parseKnownTimestamp(value string) time.Time {
	parsed, err := parseTimestamp(value)
	if err != nil || !parsed.After(time.Unix(0, 0)) {
		return time.Time{}
	}
	return parsed
}

func canonicalTimestamp(value time.Time) string {
	if value.IsZero() {
		value = time.Unix(0, 0)
	}
	return value.Round(0).UTC().Format(databaseTimestampLayout)
}

const databaseTimestampLayout = "2006-01-02 15:04:05.999999999"

var (
	globalFrecencyStore *FrecencyStore
	globalFrecencyMu    sync.RWMutex
)

func GetFrecencyStore() (*FrecencyStore, error) {
	globalFrecencyMu.Lock()
	defer globalFrecencyMu.Unlock()

	if globalFrecencyStore != nil {
		return globalFrecencyStore, nil
	}

	store, err := NewFrecencyStore("")
	if err != nil {
		return nil, err
	}
	globalFrecencyStore = store
	return globalFrecencyStore, nil
}

// LoadedFrecencyStore returns the already initialized process store without
// performing filesystem or SQLite work. Interactive suggestion paths use this
// accessor so a transient startup failure cannot turn every keystroke into a
// database-open retry.
func LoadedFrecencyStore() *FrecencyStore {
	globalFrecencyMu.RLock()
	defer globalFrecencyMu.RUnlock()
	return globalFrecencyStore
}

// CloseGlobalFrecencyStore safely closes the singleton database connection.
// This is primarily used in testing to prevent goroutine leaks from the DB connectionOpener.
func CloseGlobalFrecencyStore() {
	globalFrecencyMu.Lock()
	defer globalFrecencyMu.Unlock()

	if globalFrecencyStore != nil {
		_ = globalFrecencyStore.Close()
		globalFrecencyStore = nil
	}
}
