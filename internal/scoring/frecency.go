package scoring

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	Command  string
	Count    int
	LastUsed time.Time
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
`
	_, err := f.db.ExecContext(ctxTimeout, schema)
	return err
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

	if ctx == nil {
		ctx = context.Background()
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := f.db.BeginTx(ctxTimeout, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctxTimeout, `DELETE FROM imported_history`); execErr != nil {
		return execErr
	}
	stmt, err := tx.PrepareContext(ctxTimeout, `
INSERT INTO imported_history (cmd, count, last_used)
VALUES (?, ?, ?)
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
		if _, err := stmt.ExecContext(ctxTimeout, command, entry.Count, lastUsed); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	_, err := f.db.ExecContext(ctxTimeout, query, cmd, cwd)
	return err
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
	if prefix != "" {
		rows, err = f.db.QueryContext(ctxTimeout, `SELECT cmd, cwd, count, last_used FROM history_entries WHERE cwd = ? AND cmd LIKE ?`, cwd, prefix+"%")
	} else {
		rows, err = f.db.QueryContext(ctxTimeout, `SELECT cmd, cwd, count, last_used FROM history_entries WHERE cwd = ?`, cwd)
	}
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
SELECT cmd, count, last_used FROM imported_history WHERE cmd LIKE ?
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
SELECT cmd, count, last_used FROM imported_history
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
