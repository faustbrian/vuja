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
	"strings"
	"sync"
	"time"

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
	Path     string
	Count    int
	LastUsed time.Time
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
	db       *sql.DB
	mu       sync.Mutex
	importMu sync.Mutex
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory for history.db: %w", err)
	}
	_ = os.Chmod(dir, 0700)

	if f, err := os.OpenFile(dbPath, os.O_CREATE, 0600); err == nil {
		_ = f.Close()
	}
	_ = os.Chmod(dbPath, 0600)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &FrecencyStore{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(dbPath, 0600)

	return store, nil
}

func (f *FrecencyStore) configureSQLite(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, "PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;")
	return err
}

func (f *FrecencyStore) initSchema(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 2000*time.Millisecond)
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
    last_used     TIMESTAMP NOT NULL,
    exit_code     INTEGER,
    duration_ns   INTEGER NOT NULL DEFAULT 0,
    source        TEXT NOT NULL DEFAULT 'shell',
    UNIQUE(cmd, cwd, source)
);

CREATE INDEX IF NOT EXISTS idx_imported_history_cwd_cmd
ON imported_history_entries(cwd, cmd);

CREATE TABLE IF NOT EXISTS history_events (
    event_key     TEXT PRIMARY KEY,
    command       TEXT NOT NULL,
    cwd           TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMP NOT NULL,
    duration_ns   INTEGER NOT NULL DEFAULT 0,
    exit_code     INTEGER,
    source        TEXT NOT NULL,
    host          TEXT NOT NULL DEFAULT '',
    session_id    TEXT NOT NULL DEFAULT '',
    imported      INTEGER NOT NULL DEFAULT 0
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
	return err
}

func (f *FrecencyStore) RecordFeedback(ctx context.Context, cmd, cwd, event string) error {
	if f == nil {
		return nil
	}
	cmd, cwd = strings.TrimSpace(cmd), strings.TrimSpace(cwd)
	if cmd == "" || cwd == "" {
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	prevCommand = strings.TrimSpace(prevCommand)
	nextCommand = strings.TrimSpace(nextCommand)
	cwd = strings.TrimSpace(cwd)
	if prevCommand == "" || nextCommand == "" || cwd == "" {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
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

	f.mu.Lock()
	defer f.mu.Unlock()

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
ORDER BY count DESC
`, prevCommand, cwd)
		} else {
			rows, err = f.db.QueryContext(ctxTimeout, `
SELECT prev_command, next_command, '', SUM(count), MAX(last_used)
FROM exact_command_transitions
WHERE prev_command = ? AND count > 0
GROUP BY next_command
ORDER BY SUM(count) DESC
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
			entry.LastUsed, _ = parseTimestamp(lastUsedRaw)
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

	f.importMu.Lock()
	defer f.importMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	fingerprint := importedHistoryFingerprint(entries)
	var storedFingerprint string
	err := f.db.QueryRowContext(
		ctxTimeout,
		`SELECT value FROM metadata WHERE key = 'imported_history_fingerprint'`,
	).Scan(&storedFingerprint)
	if err == nil && storedFingerprint == fingerprint {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	tx, err := f.db.BeginTx(ctxTimeout, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctxTimeout, `DELETE FROM imported_history_entries`); execErr != nil {
		return execErr
	}
	stmt, err := tx.PrepareContext(ctxTimeout, `
INSERT INTO imported_history_entries
    (cmd, cwd, count, last_used, exit_code, duration_ns, source)
VALUES (?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, entry := range entries {
		command := strings.TrimSpace(entry.Command)
		if command == "" || entry.Count <= 0 {
			continue
		}
		lastUsed := entry.LastUsed
		if lastUsed.IsZero() {
			lastUsed = now
		}
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			source = "shell"
		}
		var exitCode any
		if entry.HasExitCode {
			exitCode = entry.ExitCode
		}
		if _, err := stmt.ExecContext(
			ctxTimeout,
			command,
			strings.TrimSpace(entry.Cwd),
			entry.Count,
			lastUsed,
			exitCode,
			entry.Duration.Nanoseconds(),
			source,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctxTimeout, `
INSERT INTO metadata (key, value)
VALUES ('imported_history_fingerprint', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}

func importedHistoryFingerprint(entries []ImportedHistoryEntry) string {
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
	for _, entry := range normalized {
		_, _ = fmt.Fprintf(
			&value,
			"%d:%s:%d:%s:%d:%t:%d:%s\n",
			len(entry.Command),
			entry.Command,
			entry.Count,
			entry.Cwd,
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
	cmd = strings.TrimSpace(cmd)
	cwd = strings.TrimSpace(cwd)
	if cmd == "" || cwd == "" {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

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
VALUES (?, ?, 0, CURRENT_TIMESTAMP)
ON CONFLICT(cmd, cwd) DO UPDATE SET
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
	return tx.Commit()
}

func (f *FrecencyStore) QueryRecentFailure(ctx context.Context, cwd string, maxAge time.Duration) (RecentFailure, bool) {
	if f == nil || strings.TrimSpace(cwd) == "" {
		return RecentFailure{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !f.mu.TryLock() {
		return RecentFailure{}, false
	}
	defer f.mu.Unlock()
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
	if !f.mu.TryLock() {
		return nil, nil
	}
	defer f.mu.Unlock()
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
		lastUsed, _ := parseTimestamp(lastUsedRaw)
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.db.ExecContext(ctx, `
INSERT INTO directory_index (path) VALUES (?)
ON CONFLICT(path) DO UPDATE SET count = count + 1, last_used = CURRENT_TIMESTAMP
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

	f.importMu.Lock()
	defer f.importMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, execErr := tx.ExecContext(ctx, `DELETE FROM directory_sources WHERE source = ?`, source); execErr != nil {
		return execErr
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO directory_sources (path, source, count, last_used)
VALUES (?, ?, ?, ?)
ON CONFLICT(path, source) DO UPDATE SET
    count = excluded.count,
    last_used = excluded.last_used
`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now()
	for _, entry := range entries {
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if path == "." || !filepath.IsAbs(path) {
			continue
		}
		count := max(entry.Count, 1)
		lastUsed := entry.LastUsed
		if lastUsed.IsZero() {
			lastUsed = now
		}
		if _, err := stmt.ExecContext(ctx, path, source, count, lastUsed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *FrecencyStore) QueryDirectories(ctx context.Context, fragment string, limit int) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 20
	}
	if !f.mu.TryLock() {
		return nil, nil
	}
	defer f.mu.Unlock()
	rows, err := f.db.QueryContext(ctx, `
WITH candidates AS (
    SELECT path, count, last_used FROM directory_index
    UNION ALL
    SELECT path, count, last_used FROM directory_sources
)
SELECT path
FROM candidates
WHERE lower(path) LIKE ?
GROUP BY path
ORDER BY SUM(count) DESC, MAX(last_used) DESC
LIMIT ?
`, "%"+strings.ToLower(strings.TrimSpace(fragment))+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if rows.Scan(&path) == nil {
			paths = append(paths, path)
		}
	}
	return paths, rows.Err()
}

func (f *FrecencyStore) RecordTransition(ctx context.Context, prevSkeleton, nextSkeleton, cwd string, nextExitCode int) error {
	if f == nil {
		return nil
	}
	prevSkeleton = strings.TrimSpace(prevSkeleton)
	nextSkeleton = strings.TrimSpace(nextSkeleton)
	cwd = strings.TrimSpace(cwd)
	if prevSkeleton == "" || nextSkeleton == "" || cwd == "" {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
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

	f.mu.Lock()
	defer f.mu.Unlock()

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
ORDER BY count DESC
`, key, cwd)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var prev, next, rCwd string
					var count int
					var lastUsedRaw string
					if err := rows.Scan(&prev, &next, &rCwd, &count, &lastUsedRaw); err == nil {
						t, _ := parseTimestamp(lastUsedRaw)
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
ORDER BY total_count DESC
`, key)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var prev, next string
					var count int
					var lastUsedRaw string
					if err := rows.Scan(&prev, &next, &count, &lastUsedRaw); err == nil {
						t, _ := parseTimestamp(lastUsedRaw)
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
	if count <= 0 {
		return 0
	}
	age := max(time.Since(lastUsed), 0)

	var weight float64
	switch {
	case age <= time.Hour:
		weight = 100.0
	case age <= 24*time.Hour:
		weight = 50.0
	case age <= 7*24*time.Hour:
		weight = 20.0
	case age <= 30*24*time.Hour:
		weight = 5.0
	default:
		weight = 1.0
	}

	return float64(count) * weight
}

func (f *FrecencyStore) QueryLocal(ctx context.Context, cwd, prefix string, limit int) ([]FrecencyEntry, error) {
	if f == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	f.mu.Lock()
	defer f.mu.Unlock()

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
SELECT cmd, cwd, MAX(count), MAX(last_used)
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
		t, err := parseTimestamp(lastUsedRaw)
		if err != nil {
			t = time.Now()
		}
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
		return entries[i].RawScore > entries[j].RawScore
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
	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	query := `
WITH candidates AS (
    SELECT cmd, cwd, count, last_used, 0 AS source_kind FROM history_entries
    UNION ALL
    SELECT cmd, cwd, count, last_used, 1 AS source_kind FROM imported_history_entries
),
per_source AS (
    SELECT cmd, source_kind, SUM(count) AS count, MAX(last_used) AS last_used
    FROM candidates
    WHERE (cwd = ? OR substr(cwd, 1, length(?) + 1) = ? || ?)
    GROUP BY cmd, source_kind
)
SELECT cmd, MAX(count), MAX(last_used)
FROM per_source
`
	args := []any{root, root, root, string(os.PathSeparator)}
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
		lastUsed, parseErr := parseTimestamp(lastUsedRaw)
		if parseErr != nil {
			lastUsed = time.Now()
		}
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
		return entries[i].RawScore > entries[j].RawScore
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
	f.mu.Lock()
	defer f.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	var rows *sql.Rows
	var err error
	if prefix != "" {
		rows, err = f.db.QueryContext(ctxTimeout, `
WITH recorded AS (
    SELECT cmd, SUM(count) AS count, MAX(last_used) AS last_used
    FROM history_entries
    WHERE cmd LIKE ?
    GROUP BY cmd
)
SELECT cmd, count, last_used FROM recorded
UNION ALL
SELECT cmd, SUM(count), MAX(last_used)
FROM imported_history_entries
WHERE cmd LIKE ?
GROUP BY cmd
`, prefix+"%", prefix+"%")
	} else {
		rows, err = f.db.QueryContext(ctxTimeout, `
WITH recorded AS (
    SELECT cmd, SUM(count) AS count, MAX(last_used) AS last_used
    FROM history_entries
    GROUP BY cmd
)
SELECT cmd, count, last_used FROM recorded
UNION ALL
SELECT cmd, SUM(count), MAX(last_used)
FROM imported_history_entries
GROUP BY cmd
`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dedupe := make(map[string]*FrecencyEntry)
	for rows.Next() {
		var cmd string
		var count int
		var lastUsedRaw string
		if err := rows.Scan(&cmd, &count, &lastUsedRaw); err != nil {
			continue
		}
		t, err := parseTimestamp(lastUsedRaw)
		if err != nil {
			t = time.Now()
		}
		score := f.RawScore(count, t)
		if existing, found := dedupe[cmd]; found {
			if count > existing.Count {
				existing.Count = count
			}
			if score > existing.RawScore {
				existing.RawScore = score
			}
			if t.After(existing.LastUsed) {
				existing.LastUsed = t
			}
		} else {
			dedupe[cmd] = &FrecencyEntry{
				Cmd:      cmd,
				Cwd:      "",
				Count:    count,
				LastUsed: t,
				RawScore: score,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var entries []FrecencyEntry
	for _, entry := range dedupe {
		entries = append(entries, *entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].RawScore > entries[j].RawScore
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (f *FrecencyStore) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.db != nil {
		return f.db.Close()
	}
	return nil
}

func parseTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

var (
	globalFrecencyStore *FrecencyStore
	globalFrecencyMu    sync.Mutex
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
