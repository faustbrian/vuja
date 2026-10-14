package scoring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/faustbrian/vuja/spec"
)

const DirectoryRecentWindow = 14 * 24 * time.Hour

const directoryProjectionClockMetadata = "history_directory_projection_at"

// HistoryNavigationDestination resolves only literal cd commands. Actual
// native destinations are recorded separately; never evaluate shell text.
func HistoryNavigationDestination(command, cwd string) (string, bool) {
	tokens := spec.Tokenize(strings.TrimSpace(command))
	if len(tokens) == 1 && tokens[0] == "cd" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Clean(home), true
	}
	if len(tokens) == 3 && tokens[0] == "cd" && tokens[1] == "--" {
		tokens = []string{"cd", tokens[2]}
	}
	if len(tokens) != 2 || tokens[0] != "cd" {
		return "", false
	}
	base := filepath.Clean(strings.TrimSpace(cwd))
	if !filepath.IsAbs(base) {
		return "", false
	}
	target := strings.TrimSpace(tokens[1])
	if target == "" || target == "-" || strings.ContainsAny(target, "$`*?{}[]") {
		return "", false
	}
	if target == "~" || strings.HasPrefix(target, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		target = filepath.Join(home, strings.TrimPrefix(target, "~/"))
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return filepath.Clean(target), true
}

// RecordHistoryDirectory attributes the post-command destination to a durable,
// successful native execution. Repeated completion is idempotent; late writes
// cannot restore forgotten/cleared IDs. Only the affected path is aggregated.
func (f *FrecencyStore) RecordHistoryDirectory(ctx context.Context, eventKey, path string) error {
	return f.recordHistoryDirectoryAt(ctx, eventKey, path, time.Now())
}

func (f *FrecencyStore) recordHistoryDirectoryAt(ctx context.Context, eventKey, path string, now time.Time) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	eventKey = strings.TrimSpace(eventKey)
	path = filepath.Clean(strings.TrimSpace(path))
	if eventKey == "" || !filepath.IsAbs(path) {
		return errors.New("recorded navigation requires an execution and absolute destination")
	}
	if err := f.acquireWrite(ctx); err != nil {
		return err
	}
	defer f.releaseWrite()
	tx, finish, err := f.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if forgotten, err := historyIdentityForgotten(ctx, tx, eventKey); err != nil {
		return err
	} else if forgotten {
		return ErrHistoryForgotten
	}
	var command, startedAtRaw string
	var occurrences int
	if err := tx.QueryRowContext(ctx, `SELECT normalized_command, CAST(started_at AS TEXT), occurrences FROM history_events
WHERE event_key = ? AND imported = 0 AND state = 'completed' AND exit_code = 0`, eventKey).Scan(&command, &startedAtRaw, &occurrences); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("navigation execution is absent or not successfully completed")
		}
		return err
	}
	tokens := spec.Tokenize(command)
	if len(tokens) == 0 || tokens[0] != "cd" && tokens[0] != "z" {
		return errors.New("execution is not a directory navigation command")
	}
	if err := expireHistoryDirectoryWindow(ctx, tx, now); err != nil {
		return err
	}
	var previous string
	var inferred bool
	err = tx.QueryRowContext(ctx, `SELECT path, inferred FROM directory_navigation_visits WHERE event_key = ?`, eventKey).Scan(&previous, &inferred)
	if err == nil {
		if !inferred && previous != path {
			return errors.New("recorded navigation destination cannot be rewritten")
		}
		if !inferred {
			return tx.Commit()
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO directory_navigation_visits (event_key, path, inferred) VALUES (?, ?, 0)
ON CONFLICT(event_key) DO UPDATE SET path = excluded.path, inferred = 0`, eventKey, path); err != nil {
		return err
	}
	if previous != path {
		startedAt := parseKnownTimestamp(startedAtRaw)
		if previous != "" {
			if err := adjustHistoryDirectoryAggregate(ctx, tx, "history", previous, -occurrences, startedAt, now); err != nil {
				return err
			}
		}
		if err := adjustHistoryDirectoryAggregate(ctx, tx, "history", path, occurrences, startedAt, now); err != nil {
			return err
		}
	}
	// Actual post-upgrade visits are disjoint from the legacy path-only index.
	// Keep that cohort in the same derived projection owner so reads can add it
	// to unknown legacy activity without also adding inferred literal-cd copies.
	if err := adjustHistoryDirectoryAggregate(ctx, tx, "native-visits", path, occurrences, parseKnownTimestamp(startedAtRaw), now); err != nil {
		return err
	}
	if err := f.recordHistoryChange(ctx, tx, "upsert", eventKey); err != nil {
		return err
	}
	return tx.Commit()
}

// Expire only the cohort that left the rolling window since the last update.
// The canonical start-time index bounds normal completion work independently
// of lifetime visit counts. This is aggregate-cache maintenance, not history.
func expireHistoryDirectoryWindow(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var previousRaw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, directoryProjectionClockMetadata).Scan(&previousRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previous := parseKnownTimestamp(previousRaw)
	if previous.IsZero() || now.Before(previous) {
		return rebuildHistoryDirectorySource(ctx, tx, now)
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.path, SUM(e.occurrences), SUM(CASE WHEN v.inferred = 0 THEN e.occurrences ELSE 0 END)
FROM history_events e INDEXED BY idx_history_events_started_at
JOIN directory_navigation_visits v ON v.event_key = e.event_key
WHERE e.started_at >= ? AND e.started_at < ? GROUP BY v.path`, canonicalTimestamp(previous.Add(-DirectoryRecentWindow)), canonicalTimestamp(now.Add(-DirectoryRecentWindow)))
	if err != nil {
		return err
	}
	type expiredCohort struct{ all, actual int }
	expired := make(map[string]expiredCohort)
	for rows.Next() {
		var path string
		var count, actual int
		if err := rows.Scan(&path, &count, &actual); err != nil {
			_ = rows.Close()
			return err
		}
		expired[path] = expiredCohort{count, actual}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for path, cohort := range expired {
		if _, err := tx.ExecContext(ctx, `UPDATE directory_navigation_sources SET recent_count = MAX(0, recent_count - ?) WHERE source = 'history' AND path = ?`, cohort.all, path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE directory_navigation_sources SET recent_count = MAX(0, recent_count - ?) WHERE source = 'native-visits' AND path = ?`, cohort.actual, path); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, directoryProjectionClockMetadata, canonicalTimestamp(now))
	return err
}

func adjustHistoryDirectoryAggregate(ctx context.Context, tx *sql.Tx, source, path string, delta int, startedAt, now time.Time) error {
	var count, recentCount int
	var lastUsedRaw string
	err := tx.QueryRowContext(ctx, `SELECT count, recent_count, last_used FROM directory_navigation_sources WHERE source = ? AND path = ?`, source, path).Scan(&count, &recentCount, &lastUsedRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	count += delta
	if count < 0 {
		return errors.New("directory projection is inconsistent with its execution membership")
	}
	if count == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM directory_navigation_sources WHERE source = ? AND path = ?`, source, path)
		return err
	}
	if !startedAt.IsZero() && !startedAt.Before(now.Add(-DirectoryRecentWindow)) {
		recentCount += delta
	}
	lastUsed := parseKnownTimestamp(lastUsedRaw)
	if delta < 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.started_at), '') FROM directory_navigation_visits v JOIN history_events e ON e.event_key = v.event_key WHERE v.path = ? AND (? = 'history' OR v.inferred = 0)`, path, source).Scan(&lastUsedRaw); err != nil {
			return err
		}
		lastUsed = parseKnownTimestamp(lastUsedRaw)
	} else if startedAt.After(lastUsed) {
		lastUsed = startedAt
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO directory_navigation_sources (path, source, count, recent_count, last_used)
VALUES (?, ?, ?, ?, ?) ON CONFLICT(path, source) DO UPDATE
SET count = excluded.count, recent_count = excluded.recent_count, last_used = excluded.last_used`, path, source, count, max(0, recentCount), canonicalTimestamp(lastUsed))
	return err
}

// RefreshHistoryDirectories uses canonical events and actual destinations,
// preventing imports/refreshes from replacing native navigation with guesses.
func (f *FrecencyStore) RefreshHistoryDirectories(ctx context.Context) error {
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
	tx, finish, err := f.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if err := rebuildHistoryDirectorySource(ctx, tx, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func rebuildHistoryDirectorySource(ctx context.Context, tx *sql.Tx, now time.Time) error {
	for _, statement := range []string{
		`DELETE FROM directory_navigation_visits WHERE inferred = 1 OR NOT EXISTS
         (SELECT 1 FROM history_events WHERE event_key = directory_navigation_visits.event_key AND imported = 0 AND state = 'completed' AND exit_code = 0)`,
		`DELETE FROM directory_navigation_sources WHERE source IN ('history', 'native-visits')`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.event_key, e.command, e.cwd, CAST(e.started_at AS TEXT), e.occurrences, COALESCE(v.path, '')
FROM history_events e LEFT JOIN directory_navigation_visits v ON v.event_key = e.event_key
WHERE e.state NOT IN ('submitted', 'running', 'interrupted', 'failed') AND (e.exit_code IS NULL OR e.exit_code = 0)`)
	if err != nil {
		return err
	}
	byPath := make(map[string]DirectoryImport)
	actualByPath := make(map[string]DirectoryImport)
	inferredVisits := make(map[string]string)
	for rows.Next() {
		var eventKey, command, cwd, timestamp, path string
		var occurrences int
		if err := rows.Scan(&eventKey, &command, &cwd, &timestamp, &occurrences, &path); err != nil {
			_ = rows.Close()
			return err
		}
		actual := path != ""
		if path == "" {
			var ok bool
			path, ok = HistoryNavigationDestination(command, cwd)
			if !ok {
				continue
			}
			inferredVisits[eventKey] = path
		}
		at := parseKnownTimestamp(timestamp)
		entry := byPath[path]
		entry.Path = path
		entry.Count += max(occurrences, 1)
		if !at.IsZero() && !at.Before(now.Add(-DirectoryRecentWindow)) {
			entry.RecentCount += max(occurrences, 1)
		}
		if at.After(entry.LastUsed) {
			entry.LastUsed = at
		}
		byPath[path] = entry
		if actual {
			owned := actualByPath[path]
			owned.Path = path
			owned.Count += max(occurrences, 1)
			if !at.IsZero() && !at.Before(now.Add(-DirectoryRecentWindow)) {
				owned.RecentCount += max(occurrences, 1)
			}
			if at.After(owned.LastUsed) {
				owned.LastUsed = at
			}
			actualByPath[path] = owned
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for eventKey, path := range inferredVisits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO directory_navigation_visits (event_key, path, inferred) VALUES (?, ?, 1)`, eventKey, path); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		entry := byPath[path]
		if _, err := tx.ExecContext(ctx, `INSERT INTO directory_navigation_sources (path, source, count, recent_count, last_used)
VALUES (?, 'history', ?, ?, ?)`, path, entry.Count, entry.RecentCount, canonicalTimestamp(entry.LastUsed)); err != nil {
			return err
		}
		if owned, exists := actualByPath[path]; exists {
			if _, err := tx.ExecContext(ctx, `INSERT INTO directory_navigation_sources (path, source, count, recent_count, last_used)
VALUES (?, 'native-visits', ?, ?, ?)`, path, owned.Count, owned.RecentCount, canonicalTimestamp(owned.LastUsed)); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metadata (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, directoryProjectionClockMetadata, canonicalTimestamp(now))
	return err
}

// Legacy visits have no execution identity. A literal command or equal counts
// cannot establish which path-only increments belong to an execution. Refuse
// changes to unowned navigation while legacy evidence exists; actual keyed
// visits and unrelated commands remain independently removable.
func removeLegacyNavigationEvidence(ctx context.Context, tx *sql.Tx, eventKey string) error {
	var command, state string
	var exitCode sql.NullInt64
	var imported bool
	var inferred bool
	err := tx.QueryRowContext(ctx, `SELECT e.command, e.state, e.exit_code, e.imported, COALESCE(v.inferred, 1)
FROM history_events e LEFT JOIN directory_navigation_visits v ON v.event_key = e.event_key WHERE e.event_key = ?`, eventKey).Scan(&command, &state, &exitCode, &imported, &inferred)
	if err != nil {
		return err
	}
	if imported || state != "completed" || !exitCode.Valid || exitCode.Int64 != 0 {
		return nil
	}
	if !inferred {
		// New attributed writes never increment the legacy index. Its visits
		// belong to other executions and must survive this execution's removal.
		return nil
	}
	tokens := spec.Tokenize(command)
	if len(tokens) > 0 && (tokens[0] == "cd" || tokens[0] == "z") {
		var legacy bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM directory_navigation_index)`).Scan(&legacy); err != nil {
			return err
		}
		if legacy {
			return ErrHistoryUnattributedLearning
		}
	}
	return nil
}
