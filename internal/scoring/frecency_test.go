package scoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrecencyStore_RecordAndQueryLocal(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	cwd := "/home/user/project"
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git status", cwd, 0)
	_ = store.Record(context.Background(), "git commit -m 'test'", cwd, 0)

	entries, err := store.QueryLocal(context.Background(), cwd, "git", 10)
	if err != nil {
		t.Fatalf("QueryLocal failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Cmd != "git status" || entries[0].Count != 3 {
		t.Errorf("expected top entry to be 'git status' with count 3, got %s (count %d)", entries[0].Cmd, entries[0].Count)
	}
}

func TestFrecencyStore_QueryProjectAggregatesOnlyRepositoryDescendants(t *testing.T) {
	root := t.TempDir()
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.Record(ctx, "go test ./...", filepath.Join(root, "service-a"), 0)
	_ = store.Record(ctx, "go test ./...", filepath.Join(root, "service-b"), 0)
	_ = store.Record(ctx, "go test sibling", root+"-sibling", 0)

	entries, err := store.QueryProject(ctx, root, "go test", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Cmd != "go test ./..." || entries[0].Count != 2 {
		t.Fatalf("expected repository-scoped aggregate, got %+v", entries)
	}
}

func TestFrecencyStore_LearnsSuccessfulArgumentValuesBySlot(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "service")
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.Record(ctx, "git switch feature/payments", cwd, 0)
	_ = store.Record(ctx, "git switch feature/payments", cwd, 0)
	_ = store.Record(ctx, "git switch feature/broken", cwd, 1)

	values, err := store.QueryArgumentValues(ctx, cwd, root, "git switch", 2, "feature/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Value != "feature/payments" {
		t.Fatalf("expected successful branch value, got %+v", values)
	}
}

func TestFrecencyStore_TracksOnlyTheLatestUnrecoveredFailure(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	cwd := "/repo/service"

	if err := store.Record(ctx, "gti status", cwd, 127); err != nil {
		t.Fatal(err)
	}
	failure, ok := store.QueryRecentFailure(ctx, cwd, 5*time.Minute)
	if !ok || failure.Command != "gti status" || failure.ExitCode != 127 {
		t.Fatalf("expected command-not-found failure, got %+v, %v", failure, ok)
	}

	if err := store.Record(ctx, "git status", cwd, 0); err != nil {
		t.Fatal(err)
	}
	if failure, ok := store.QueryRecentFailure(ctx, cwd, 5*time.Minute); ok {
		t.Fatalf("expected successful command to clear recovery, got %+v", failure)
	}
}

func TestFrecencyStore_ReplacesImportedDirectorySourcesIdempotently(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	entries := []DirectoryImport{
		{Path: "/repo/service", Count: 5, LastUsed: time.Now()},
		{Path: "/repo/other", Count: 2, LastUsed: time.Now().Add(-time.Hour)},
	}
	if replaceErr := store.ReplaceDirectorySource(ctx, "atuin", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if replaceErr := store.ReplaceDirectorySource(ctx, "atuin", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	paths, err := store.QueryDirectories(ctx, "service", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].Path != "/repo/service" {
		t.Fatalf("expected one idempotent imported directory, got %v", paths)
	}
}

func TestFrecencyStore_QueryDirectoriesHonorsRankingPreference(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	entries := []DirectoryImport{
		{Path: "/repo/frequent", Count: 100, LastUsed: now.Add(-30 * 24 * time.Hour)},
		{Path: "/repo/recent", Count: 2, LastUsed: now},
	}
	if replaceErr := store.ReplaceDirectorySource(context.Background(), "test", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	frequent, err := store.QueryDirectories(context.Background(), "/repo", 10, "frequent")
	if err != nil {
		t.Fatal(err)
	}
	if len(frequent) != 2 || frequent[0].Path != "/repo/frequent" {
		t.Fatalf("expected frequent directory first, got %v", frequent)
	}
	recent, err := store.QueryDirectories(context.Background(), "/repo", 10, "recent")
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Path != "/repo/recent" {
		t.Fatalf("expected recent directory first, got %v", recent)
	}
}

func TestFrecencyStore_QueryDirectoriesBalancesRecentUseAgainstAncientVolume(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	entries := []DirectoryImport{
		{Path: "/repo/ancient", Count: 1000, RecentCount: 0, LastUsed: now.Add(-180 * 24 * time.Hour)},
		{Path: "/repo/active", Count: 20, RecentCount: 20, LastUsed: now.Add(-35 * 24 * time.Hour)},
	}
	if replaceErr := store.ReplaceDirectorySource(context.Background(), "test", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	balanced, err := store.QueryDirectories(context.Background(), "/repo", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(balanced) != 2 || balanced[0].Path != "/repo/active" {
		t.Fatalf("expected sustained recent use to outrank ancient volume, got %v", balanced)
	}
}

func TestFrecencyStore_BalancedDirectoryRankingUsesRecentWindowFrequency(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	entries := []DirectoryImport{
		{Path: "/repo/lifetime-heavy", Count: 3000, RecentCount: 1, LastUsed: now.Add(-5 * 24 * time.Hour)},
		{Path: "/repo/current", Count: 50, RecentCount: 12, LastUsed: now.Add(-24 * time.Hour)},
	}
	if replaceErr := store.ReplaceDirectorySource(t.Context(), "history", entries); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	balanced, err := store.QueryDirectories(t.Context(), "/repo", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(balanced) != 2 || balanced[0].Path != "/repo/current" {
		t.Fatalf("expected recent-window frequency to outrank lifetime volume, got %+v", balanced)
	}

	frequent, err := store.QueryDirectories(t.Context(), "/repo", 10, "frequent")
	if err != nil {
		t.Fatal(err)
	}
	if frequent[0].Path != "/repo/lifetime-heavy" {
		t.Fatalf("expected explicit frequent mode to retain lifetime frequency, got %+v", frequent)
	}
}

func TestFrecencyStore_QueryLocalKeepsFrequencyAndRecencyFromTheSameSource(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	old := time.Now().Add(-180 * 24 * time.Hour)
	recent := time.Now().Add(-24 * time.Hour)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO history_entries (cmd, cwd, count, last_used) VALUES ('cd api', '/repo', 1000, ?)`,
		canonicalTimestamp(old),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO imported_history_entries (cmd, cwd, count, last_used, source) VALUES ('cd api', '/repo', 2, ?, 'atuin')`,
		canonicalTimestamp(recent),
	); err != nil {
		t.Fatal(err)
	}

	entries, err := store.QueryLocal(ctx, "/repo", "cd", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected independent evidence from two sources, got %+v", entries)
	}
	for _, entry := range entries {
		if entry.Count == 1000 && entry.LastUsed.Equal(recent) {
			t.Fatalf("frequency and recency were combined across sources: %+v", entry)
		}
	}
}

func TestFrecencyStore_QueryDirectoriesDoesNotPairFrequencyAndRecencyAcrossSources(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	ancient := time.Now().Add(-180 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	if err := store.ReplaceDirectorySource(ctx, "frequency", []DirectoryImport{{Path: "/repo/mixed", Count: 1000, LastUsed: ancient}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceDirectorySource(ctx, "recency", []DirectoryImport{{Path: "/repo/mixed", Count: 1, LastUsed: recent}}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.QueryDirectories(ctx, "mixed", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one path, got %+v", entries)
	}
	if entries[0].Count == 1001 || entries[0].LastUsed.Equal(recent) && entries[0].Count == 1000 {
		t.Fatalf("expected coherent source metadata, got %+v", entries[0])
	}
}

func TestFrecencyStore_BalancedDirectoriesDoNotLetLifetimeZoxideFrequencyHideRecentActivity(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	recent := time.Now().Add(-14 * 24 * time.Hour)
	if err := store.ReplaceDirectorySource(ctx, "zoxide", []DirectoryImport{{Path: "/repo/ancient", Count: 10_000}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceDirectorySource(ctx, "history", []DirectoryImport{{Path: "/repo/active", Count: 20, RecentCount: 20, LastUsed: recent}}); err != nil {
		t.Fatal(err)
	}

	entries, err := store.QueryDirectories(ctx, "/repo", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].Path != "/repo/active" {
		t.Fatalf("expected recent sustained activity before lifetime-only frequency, got %+v", entries)
	}
}

func TestFrecencyStore_RawScoreDistribution(t *testing.T) {
	store := &FrecencyStore{}
	now := time.Now()

	oldHeavyScore := store.RawScore(5000, now.Add(-30*24*time.Hour))
	recentLightScore := store.RawScore(5, now.Add(-30*time.Minute))

	if oldHeavyScore <= 0 || recentLightScore <= 0 {
		t.Errorf("expected positive raw scores, got %f and %f", oldHeavyScore, recentLightScore)
	}
	if recentLightScore >= oldHeavyScore {
		t.Logf("recent light score (%f) vs old heavy score (%f)", recentLightScore, oldHeavyScore)
	}
}

func TestFrecencyStoreRecordPublishesACompatibilityWriteToCanonicalHistory(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Record(t.Context(), "ssh forge@api", "/repo", 0); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Command != "ssh forge@api" || events[0].Source != "vuja" ||
		events[0].State != "completed" || !events[0].HasExitCode || events[0].ExitCode != 0 {
		t.Fatalf("expected compatibility recording to use canonical history, got %+v", events)
	}
}

func TestHistoryRankingBalancesRecentUseAgainstAncientFrequency(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-180 * 24 * time.Hour)
	current := now.Add(-35 * 24 * time.Hour)

	if historyRankingScore(3000, ancient, "balanced", now) >= historyRankingScore(50, current, "balanced", now) {
		t.Fatal("expected balanced history ranking to decay ancient lifetime volume")
	}
	if historyRankingScore(3000, ancient, "frequent", now) <= historyRankingScore(50, current, "frequent", now) {
		t.Fatal("expected frequent history ranking to retain lifetime volume")
	}
	if historyRankingScore(3000, ancient, "recent", now) >= historyRankingScore(50, current, "recent", now) {
		t.Fatal("expected recent history ranking to prefer the newer command")
	}
}

func TestQuerySignalSnapshotAppliesConfiguredHistoryRankingBeforeItsLimit(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	for _, entry := range []struct {
		command  string
		count    int
		lastUsed time.Time
	}{
		{command: "ssh forge@ancient", count: 3000, lastUsed: now.Add(-180 * 24 * time.Hour)},
		{command: "ssh forge@current", count: 50, lastUsed: now.Add(-35 * 24 * time.Hour)},
	} {
		if _, err := store.db.ExecContext(t.Context(), `
INSERT INTO history_entries (cmd, cwd, count, last_used) VALUES (?, '/repo', ?, ?)
`, entry.command, entry.count, canonicalTimestamp(entry.lastUsed)); err != nil {
			t.Fatal(err)
		}
	}

	query := func(ranking string) []FrecencyEntry {
		snapshot, err := store.QuerySignalSnapshotRanked(
			t.Context(), "/repo", "/repo", "ssh", 1, "", "", ranking,
		)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Local
	}
	if got := query("balanced"); len(got) != 1 || got[0].Cmd != "ssh forge@current" {
		t.Fatalf("expected balanced mode to retain the active command before limiting, got %+v", got)
	}
	if got := query("frequent"); len(got) != 1 || got[0].Cmd != "ssh forge@ancient" {
		t.Fatalf("expected frequent mode to retain the lifetime-heavy command, got %+v", got)
	}
	if got := query("recent"); len(got) != 1 || got[0].Cmd != "ssh forge@current" {
		t.Fatalf("expected recent mode to retain the newest command, got %+v", got)
	}
}

func TestFrecencyStore_QueryGlobalDedupe(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	_ = store.Record(context.Background(), "make build", "/repo/a", 0)
	_ = store.Record(context.Background(), "make build", "/repo/a", 0)
	_ = store.Record(context.Background(), "make build", "/repo/b", 0)

	entries, err := store.QueryGlobal(context.Background(), "make", 10)
	if err != nil {
		t.Fatalf("QueryGlobal failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 deduplicated entry, got %d", len(entries))
	}
	if entries[0].Count != 3 {
		t.Errorf("expected combined count 3 across workspaces, got %d", entries[0].Count)
	}
}

func TestFrecencyStore_Permissions(t *testing.T) {
	tmpRoot := t.TempDir()
	dbDir := filepath.Join(tmpRoot, "subdir", "vuja")
	dbPath := filepath.Join(dbDir, "history.db")

	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("failed to make pre-existing dir: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write dummy existing db file: %v", err)
	}

	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	dirInfo, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("stat dbDir failed: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("expected directory permissions 0700, got %04o", perm)
	}

	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat dbPath failed: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Errorf("expected database file permissions 0600, got %04o", perm)
	}
}

func TestFrecencyStore_SQLiteConfigurationAndContext(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	var journalMode string
	if qErr := store.db.QueryRowContext(context.Background(), "PRAGMA journal_mode;").Scan(&journalMode); qErr != nil {
		t.Fatalf("failed to query journal_mode: %v", qErr)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode 'wal', got '%s'", journalMode)
	}

	var busyTimeout int
	if qErr := store.db.QueryRowContext(context.Background(), "PRAGMA busy_timeout;").Scan(&busyTimeout); qErr != nil {
		t.Fatalf("failed to query busy_timeout: %v", qErr)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout 5000, got %d", busyTimeout)
	}
	connections := make([]*sql.Conn, 0, 4)
	for range 4 {
		connection, connectionErr := store.db.Conn(t.Context())
		if connectionErr != nil {
			t.Fatal(connectionErr)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index, connection := range connections {
		var connectionBusyTimeout, synchronous int
		if err := connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&connectionBusyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if err := connection.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", index, err)
		}
		if connectionBusyTimeout != 5000 || synchronous != 2 {
			t.Fatalf("connection %d missed durable pragmas: busy_timeout=%d synchronous=%d", index, connectionBusyTimeout, synchronous)
		}
	}

	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.Record(ctxCanceled, "git status", tmpDir, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled from Record with canceled context, got %v", err)
	}
}

func TestFrecencyStore_NilReceiver(t *testing.T) {
	var nilStore *FrecencyStore
	if err := nilStore.Record(context.Background(), "cmd", "cwd", 0); err != nil {
		t.Errorf("expected nil error on nil store Record, got %v", err)
	}
	if entries, err := nilStore.QueryLocal(context.Background(), "cwd", "", 10); err != nil || entries != nil {
		t.Errorf("expected nil entries and nil error on nil store QueryLocal, got %v, %v", entries, err)
	}
	if entries, err := nilStore.QueryGlobal(context.Background(), "", 10); err != nil || entries != nil {
		t.Errorf("expected nil entries and nil error on nil store QueryGlobal, got %v, %v", entries, err)
	}
	if err := nilStore.Close(); err != nil {
		t.Errorf("expected nil error on nil store Close, got %v", err)
	}
}

func TestFrecencyStore_ExitCodeBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	cwd := "/home/user/test"
	_ = store.Record(context.Background(), "grep foo", cwd, 0) // count=1
	_ = store.Record(context.Background(), "grep foo", cwd, 1) // count unchanged (1)

	entries, _ := store.QueryLocal(context.Background(), cwd, "grep", 10)
	if len(entries) != 1 || entries[0].Count != 1 {
		t.Errorf("expected grep count to be 1 after non-zero exit code, got %v", entries)
	}

	_ = store.RecordTransition(context.Background(), "git checkout", "git status", cwd, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "git status", cwd, 1)

	transitions, isLocal := store.QueryTransitionsWithFallback(context.Background(), "git checkout", cwd)
	if !isLocal || len(transitions) != 1 || transitions[0].Count != 1 {
		t.Errorf("expected transition count 1 after non-zero exit code, got %v, isLocal=%v", transitions, isLocal)
	}
}

func TestFrecencyStore_ExactSequencesAndImportedHistory(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	cwd := "/repo"
	if recordErr := store.RecordExactTransition(ctx, "make build", "make test", cwd, 0); recordErr != nil {
		t.Fatal(recordErr)
	}
	if recordErr := store.RecordExactTransition(ctx, "make build", "make test", cwd, 1); recordErr != nil {
		t.Fatal(recordErr)
	}
	exact, local := store.QueryExactTransitionsWithFallback(ctx, "make build", cwd)
	if !local || len(exact) != 1 || exact[0].NextCommand != "make test" || exact[0].Count != 1 {
		t.Fatalf("unexpected exact transitions: %v, local=%v", exact, local)
	}

	imported := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: time.Now()},
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	global, err := store.QueryGlobal(ctx, "git", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].Cmd != "git status" || global[0].Count != 7 {
		t.Fatalf("expected idempotent imported count 7, got %v", global)
	}
}

func TestFrecencyStore_QuerySignalSnapshotReadsAllRankingSignalsTogether(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	cwd := "/repo/service"
	if err := store.Record(ctx, "git status", cwd, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFeedback(ctx, "git status", cwd, "accepted"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTransition(ctx, "git add", "git status", cwd, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExactTransition(ctx, "git add .", "git status", cwd, 0); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.QuerySignalSnapshot(ctx, cwd, "/repo", "git", 50, "git add .", "git add")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Local) == 0 || len(snapshot.Project) == 0 || len(snapshot.Global) == 0 {
		t.Fatalf("expected local, project and global frecency, got %+v", snapshot)
	}
	if len(snapshot.Feedback) != 1 || len(snapshot.Outcomes) != 1 {
		t.Fatalf("expected feedback and outcome signals, got %+v", snapshot)
	}
	if !snapshot.TransitionsLocal || len(snapshot.Transitions) != 1 || !snapshot.ExactTransitionsLocal || len(snapshot.ExactTransitions) != 1 {
		t.Fatalf("expected local transition signals, got %+v", snapshot)
	}
}

func TestFrecencyStore_QuerySignalSnapshotHonorsCancellation(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.QuerySignalSnapshot(ctx, "/repo", "/repo", "git", 50, "", ""); err == nil {
		t.Fatal("expected canceled snapshot query to return an error")
	}
}

func TestFrecencyStore_RewritesImportedHistoryWhenRecencyChanges(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	imported := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: time.Now()},
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	imported[0].LastUsed = imported[0].LastUsed.Add(time.Minute)
	if replaceErr := store.ReplaceImportedHistory(ctx, imported); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT last_used FROM imported_history_entries WHERE cmd = ?`, "git status").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != canonicalTimestamp(imported[0].LastUsed) {
		t.Fatalf("expected changed recency to be persisted, got %q", stored)
	}
}

func TestFrecencyStore_UnknownImportedTimestampRemainsUnknown(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReplaceImportedHistory(t.Context(), []ImportedHistoryEntry{{Command: "cd unknown", Cwd: "/repo", Count: 100}}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.QueryLocal(t.Context(), "/repo", "cd", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].LastUsed.IsZero() {
		t.Fatalf("expected unknown recency to stay unknown, got %+v", entries)
	}
}

func TestImportedHistoryPreservesAtuinDirectoryAndMetadata(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	entry := ImportedHistoryEntry{
		Command:     "go test ./...",
		Cwd:         "/repo/service",
		Count:       3,
		LastUsed:    time.Unix(1720000000, 0),
		ExitCode:    0,
		HasExitCode: true,
		Duration:    250 * time.Millisecond,
		Source:      "atuin",
	}
	if replaceErr := store.ReplaceImportedHistory(ctx, []ImportedHistoryEntry{entry}); replaceErr != nil {
		t.Fatal(replaceErr)
	}

	local, err := store.QueryLocal(ctx, entry.Cwd, "go test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Cmd != entry.Command || local[0].Count != 3 {
		t.Fatalf("expected directory-scoped Atuin history, got %+v", local)
	}
	if !local[0].LastUsed.Equal(entry.LastUsed) {
		t.Fatalf("expected imported recency to survive persistence, got %v want %v", local[0].LastUsed, entry.LastUsed)
	}

	var exitCode, duration int64
	var source, lastUsed string
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT exit_code, duration_ns, source, last_used FROM imported_history_entries WHERE cmd = ?`,
		entry.Command,
	).Scan(&exitCode, &duration, &source, &lastUsed); err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 || duration != entry.Duration.Nanoseconds() || source != "atuin" {
		t.Fatalf("unexpected imported metadata: exit=%d duration=%d source=%q", exitCode, duration, source)
	}
	if lastUsed != canonicalTimestamp(entry.LastUsed) {
		t.Fatalf("expected canonical imported timestamp, got %q", lastUsed)
	}
}

func TestFrecencyStore_MalformedImportedTimestampDoesNotBecomeRecent(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO imported_history_entries (cmd, cwd, count, last_used, source)
VALUES ('cd stale', '/repo', 7, 'not-a-timestamp', 'test')
`); err != nil {
		t.Fatal(err)
	}

	entries, err := store.QueryLocal(context.Background(), "/repo", "cd", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].LastUsed.IsZero() || entries[0].RawScore != 7 {
		t.Fatalf("expected malformed recency to remain unknown, got %+v", entries)
	}
}

func TestParseTimestampAcceptsLegacyGoTimestampWithMonotonicSuffix(t *testing.T) {
	want := time.Date(2026, time.August, 3, 8, 32, 37, 0, time.FixedZone("EEST", 3*60*60))
	got, err := parseTimestamp("2026-08-03 08:32:37 +0300 EEST m=+5.7")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("expected legacy timestamp %v, got %v", want, got)
	}
}

func TestFrecencyStore_DirectorySourcesPersistKnownAndUnknownRecencyCanonically(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	known := time.Now()
	if err := store.ReplaceDirectorySource(context.Background(), "test", []DirectoryImport{
		{Path: "/repo/known", Count: 2, LastUsed: known},
		{Path: "/repo/unknown", Count: 100},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.db.QueryContext(context.Background(), `SELECT path, last_used FROM directory_navigation_sources ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var path, lastUsed string
		if err := rows.Scan(&path, &lastUsed); err != nil {
			t.Fatal(err)
		}
		got[path] = lastUsed
	}
	if got["/repo/known"] != canonicalTimestamp(known) {
		t.Fatalf("expected canonical known recency, got %q", got["/repo/known"])
	}
	if got["/repo/unknown"] != canonicalTimestamp(time.Time{}) {
		t.Fatalf("expected unknown recency sentinel, got %q", got["/repo/unknown"])
	}

	balanced, err := store.QueryDirectories(context.Background(), "/repo", 10, "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(balanced) != 2 || balanced[0].Path != "/repo/known" {
		t.Fatalf("expected known recent use to outrank frequency-only discovery, got %+v", balanced)
	}
}

func TestImportedHistoryFingerprintTracksRelativeRecency(t *testing.T) {
	now := time.Now()
	original := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: now},
		{Command: "go test", Count: 3, LastUsed: now.Add(-time.Second)},
	}
	shifted := []ImportedHistoryEntry{
		{Command: "git status", Count: 7, LastUsed: now.Add(time.Minute)},
		{Command: "go test", Count: 3, LastUsed: now.Add(time.Minute - time.Second)},
	}
	reordered := []ImportedHistoryEntry{
		{Command: "go test", Count: 3, LastUsed: now.Add(time.Minute)},
		{Command: "git status", Count: 7, LastUsed: now.Add(time.Minute - time.Second)},
	}

	if importedHistoryFingerprint(original) != importedHistoryFingerprint(shifted) {
		t.Fatal("absolute timestamp changes should not force an import")
	}
	if importedHistoryFingerprint(original) == importedHistoryFingerprint(reordered) {
		t.Fatal("relative recency changes must force an import")
	}
}

func TestFrecencyStore_TransitionCwdIsolationAndDepthFallback(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "history.db")
	store, err := NewFrecencyStore(dbPath)
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	projectA := "/repo/a"
	projectB := "/repo/b"

	_ = store.RecordTransition(context.Background(), "git checkout", "npm run dev", projectA, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "go test", projectB, 0)
	_ = store.RecordTransition(context.Background(), "git checkout", "go test", projectB, 0)

	// query in project B should return go test (Local) and not npm run dev
	transB, isLocalB := store.QueryTransitionsWithFallback(context.Background(), "git checkout", projectB)
	if !isLocalB || len(transB) != 1 || transB[0].NextSkeleton != "go test" {
		t.Errorf("expected local transition 'go test' for project B, got %v (isLocal=%v)", transB, isLocalB)
	}

	// query in project C (no local data) should fallback to Global (returning both aggregated)
	projectC := "/repo/c"
	transC, isLocalC := store.QueryTransitionsWithFallback(context.Background(), "git checkout", projectC)
	if isLocalC || len(transC) != 2 {
		t.Errorf("expected global transitions for project C, got %v (isLocal=%v)", transC, isLocalC)
	}
	if transC[0].NextSkeleton != "go test" {
		t.Errorf("expected global top transition to be 'go test' (count 2), got %s", transC[0].NextSkeleton)
	}

	// depth fallback test: query deep skeleton with no exact match should fallback to shallower prefix
	_ = store.RecordTransition(context.Background(), "git remote", "git fetch", projectA, 0)
	transDeep, isLocalDeep := store.QueryTransitionsWithFallback(context.Background(), "git remote add", projectA)
	if !isLocalDeep || len(transDeep) != 1 || transDeep[0].NextSkeleton != "git fetch" {
		t.Errorf("expected depth fallback to 'git fetch' from 'git remote', got %v", transDeep)
	}
}

func TestFrecencyStore_PreservesNativeAndImportedHistoryExecutions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("NewFrecencyStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	native := HistoryEvent{
		EventKey:    "vuja:native",
		Command:     "just deploy staging",
		Cwd:         "/repo",
		StartedAt:   time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC),
		Duration:    12 * time.Second,
		ExitCode:    0,
		HasExitCode: true,
		Source:      "vuja",
		SessionID:   "session-a",
	}
	if recordErr := store.RecordHistoryEvent(ctx, native); recordErr != nil {
		t.Fatalf("RecordHistoryEvent failed: %v", recordErr)
	}

	imported := []HistoryEvent{
		{EventKey: "atuin:first", Command: "just deploy staging", Cwd: "/repo", StartedAt: native.StartedAt.Add(-time.Hour), Source: "atuin", Imported: true},
		{EventKey: "atuin:second", Command: "just deploy staging", Cwd: "/repo", StartedAt: native.StartedAt.Add(-2 * time.Hour), Source: "atuin", Imported: true},
	}
	if replaceErr := store.ReplaceImportedHistoryEvents(ctx, imported); replaceErr != nil {
		t.Fatalf("ReplaceImportedHistoryEvents failed: %v", replaceErr)
	}
	if replaceErr := store.ReplaceImportedHistoryEvents(ctx, imported[:1]); replaceErr != nil {
		t.Fatalf("second ReplaceImportedHistoryEvents failed: %v", replaceErr)
	}

	events, err := store.QueryHistoryEvents(ctx, 100)
	if err != nil {
		t.Fatalf("QueryHistoryEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected one native and one imported execution, got %+v", events)
	}
	if events[0].EventKey != native.EventKey || !events[0].StartedAt.Equal(native.StartedAt) ||
		events[0].Duration != native.Duration || events[0].SessionID != native.SessionID {
		t.Fatalf("expected native execution metadata to survive import replacement, got %+v", events[0])
	}
	if events[1].EventKey != "atuin:first" {
		t.Fatalf("expected stale imported execution to be replaced, got %+v", events)
	}
	var aggregateCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count FROM imported_history_entries WHERE cmd = ? AND cwd = ? AND source = ?`,
		"just deploy staging", "/repo", "atuin").Scan(&aggregateCount); err != nil {
		t.Fatal(err)
	}
	if aggregateCount != 1 {
		t.Fatalf("expected imported event replacement to rebuild aggregates in the same transaction, got %d", aggregateCount)
	}
}

func TestImportedHistoryEventsFingerprintIncludesOutcomeMetadata(t *testing.T) {
	base := HistoryEvent{
		EventKey: "atuin:event", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", StartedAt: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC),
		Source: "atuin", State: "completed", ExitCode: 0, HasExitCode: true,
	}
	failed := base
	failed.ExitCode = 255
	failed.State = "failed"
	unknown := base
	unknown.HasExitCode = false
	unknown.State = "unknown"

	baseFingerprint := importedHistoryEventsFingerprint([]HistoryEvent{base})
	if failedFingerprint := importedHistoryEventsFingerprint([]HistoryEvent{failed}); failedFingerprint == baseFingerprint {
		t.Fatal("expected exit-code changes to invalidate the imported event fingerprint")
	}
	if unknownFingerprint := importedHistoryEventsFingerprint([]HistoryEvent{unknown}); unknownFingerprint == baseFingerprint {
		t.Fatal("expected exit-code availability changes to invalidate the imported event fingerprint")
	}
}

func TestFrecencyStoreImportedEventCannotOverwriteNativeEventWithCollidingID(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	native := HistoryEvent{
		EventKey: "shared-id", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja", State: "completed",
		ExitCode: 0, HasExitCode: true,
	}
	if err := store.RecordHistoryEvent(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	imported := HistoryEvent{
		EventKey: "shared-id", Command: "ssh forge@stale", NormalizedCommand: "ssh forge@stale",
		Cwd: "/stale", SubmittedAt: started.Add(-time.Hour), StartedAt: started.Add(-time.Hour),
		Source: "atuin", State: "unknown", Imported: true,
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{imported}); err != nil {
		t.Fatal(err)
	}
	events, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Command != native.Command || events[0].Source != "vuja" || events[0].Imported {
		t.Fatalf("expected native event to survive imported ID collision, got %+v", events)
	}
}

func TestFrecencyStore_AddsHistoryEventsToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := legacy.ExecContext(t.Context(), `CREATE TABLE history_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		cmd TEXT NOT NULL,
		cwd TEXT NOT NULL,
		count INTEGER DEFAULT 1,
		last_used TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(cmd, cwd)
	)`); createErr != nil {
		t.Fatal(createErr)
	}
	if closeErr := legacy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatalf("expected backward-compatible schema initialization, got %v", err)
	}
	defer store.Close()
	event := HistoryEvent{EventKey: "vuja:migrated", Command: "git status", StartedAt: time.Now(), Source: "vuja"}
	if err := store.RecordHistoryEvent(context.Background(), event); err != nil {
		t.Fatalf("expected new history events table to be usable, got %v", err)
	}
}

func TestFrecencyStoreLifecycleMigrationPreservesImportedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), `
CREATE TABLE history_events (
    event_key TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMP NOT NULL,
    duration_ns INTEGER NOT NULL DEFAULT 0,
    exit_code INTEGER,
    source TEXT NOT NULL,
    host TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    imported INTEGER NOT NULL DEFAULT 0
);
INSERT INTO history_events
    (event_key, command, cwd, started_at, duration_ns, exit_code, source, imported)
VALUES ('atuin:legacy', 'ssh forge@api', '/repo', '2026-08-31T12:00:00Z', 1000, 255, 'atuin', 1);
`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != "failed" || !events[0].HasExitCode || events[0].ExitCode != 255 {
		t.Fatalf("expected imported legacy outcome to survive lifecycle migration, got %+v", events)
	}
}

func TestHistoryMigrationBackupPublicationNeverReplacesTheFirstBackup(t *testing.T) {
	directory := t.TempDir()
	backup := filepath.Join(directory, "history.db.pre-lifecycle-v2.bak")
	first := filepath.Join(directory, "first.tmp")
	second := filepath.Join(directory, "second.tmp")
	if err := os.WriteFile(first, []byte("pre-migration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("post-migration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishHistoryMigrationBackup(first, backup); err != nil {
		t.Fatal(err)
	}
	if err := publishHistoryMigrationBackup(second, backup); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pre-migration" {
		t.Fatalf("expected the first complete backup to remain authoritative, got %q", data)
	}
}

func TestFrecencyStoreLifecycleMigrationConvertsLegacyNativeAggregates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), `
CREATE TABLE history_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cmd TEXT NOT NULL,
    cwd TEXT NOT NULL,
    count INTEGER DEFAULT 1,
    last_used TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(cmd, cwd)
);
CREATE TABLE command_outcomes (
    cmd TEXT NOT NULL,
    cwd TEXT NOT NULL,
    successes INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    last_used TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(cmd, cwd)
);
INSERT INTO history_entries (cmd, cwd, count, last_used)
VALUES ('ssh forge@api', '/repo', 3, '2026-08-31T12:00:00Z');
INSERT INTO command_outcomes (cmd, cwd, successes, failures, last_used)
VALUES ('ssh forge@api', '/repo', 3, 2, '2026-08-31T12:00:00Z');
`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reopened.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected compact canonical representatives for legacy aggregates, got %+v", events)
	}
	states := map[string]int{}
	for _, event := range events {
		states[event.State] += event.Occurrences
		if event.Command != "ssh forge@api" || event.Cwd != "/repo" || event.Source != "legacy-vuja" || event.Imported {
			t.Fatalf("unexpected migrated native event: %+v", event)
		}
	}
	if states["completed"] != 3 || states["failed"] != 2 {
		t.Fatalf("expected three successes and two failures, got %v", states)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	events, err = second.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reopenedOccurrences := 0
	for _, event := range events {
		reopenedOccurrences += event.Occurrences
	}
	if len(events) != 2 || reopenedOccurrences != 5 {
		t.Fatalf("expected restart-safe migration without duplicate events, got %+v", events)
	}
}

func TestFrecencyStoreLifecycleMigrationConvertsLegacyImportedAggregates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), `
CREATE TABLE imported_history_entries (
    cmd TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    count INTEGER NOT NULL,
    last_used TEXT NOT NULL,
    exit_code INTEGER,
    duration_ns INTEGER NOT NULL DEFAULT 0,
    source TEXT NOT NULL DEFAULT 'shell',
    UNIQUE(cmd, cwd, source)
);
CREATE TABLE imported_history (
    cmd TEXT PRIMARY KEY,
    count INTEGER NOT NULL,
    last_used TIMESTAMP NOT NULL
);
INSERT INTO imported_history_entries
    (cmd, cwd, count, last_used, exit_code, duration_ns, source)
VALUES ('ssh forge@worker', '/repo', 2, '2026-08-31T12:00:00Z', 255, 3000000000, 'atuin');
INSERT INTO imported_history (cmd, count, last_used)
VALUES ('git status', 2, '2026-08-30T12:00:00Z');
`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected compact canonical representatives for imported aggregates, got %+v", events)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Command] += event.Occurrences
		if !event.Imported {
			t.Fatalf("expected migrated external history to remain imported, got %+v", event)
		}
		if event.Command == "ssh forge@worker" &&
			(event.Source != "atuin" || event.State != "failed" || !event.HasExitCode || event.ExitCode != 255 || event.Duration != 3*time.Second || event.Occurrences != 2) {
			t.Fatalf("expected Atuin metadata to survive aggregate migration, got %+v", event)
		}
	}
	if counts["ssh forge@worker"] != 2 || counts["git status"] != 2 {
		t.Fatalf("expected imported execution counts to survive migration, got %v", counts)
	}
}

func TestFrecencyStoreLifecycleMigrationPurgesHistoryPolicyViolations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	legacy, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO history_events (event_key, command, normalized_command, submitted_at, started_at, source, state) VALUES ('private-normalized-event', 'echo safe', 'curl --token private-value https://example.test', '2026-08-31T12:00:00Z', '2026-08-31T12:00:00Z', 'vuja', 'completed')`,
		`INSERT INTO history_entries (cmd, cwd) VALUES ('curl --token private-value https://example.test', '/repo')`,
		`INSERT INTO command_outcomes (cmd, cwd, successes) VALUES ('curl --token private-value https://example.test', '/repo', 1)`,
		`INSERT INTO imported_history_entries (cmd, cwd, count, last_used, source) VALUES (' ssh forge@private', '/repo', 1, '2026-08-31T12:00:00Z', 'zsh')`,
		`INSERT INTO suggestion_feedback (cmd, cwd, typed) VALUES ('curl --password private-value', '/repo', 1)`,
		`INSERT INTO exact_command_transitions (prev_command, next_command, cwd) VALUES ('git status', 'curl --api-key private-value', '/repo')`,
		`UPDATE metadata SET value = '1' WHERE key = 'history_schema_version'`,
	} {
		if _, err := legacy.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backupPath := path + ".pre-lifecycle-v2.bak"
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("expected a pre-cleanup history backup: %v", err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected owner-only migration backup, got %04o", backupInfo.Mode().Perm())
	}
	for _, table := range []string{
		"history_events", "history_entries", "command_outcomes", "imported_history_entries",
		"suggestion_feedback", "exact_command_transitions",
	} {
		var count int
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected migration to purge excluded history from %s, got %d rows", table, count)
		}
	}
}

func TestFrecencyStorePersistsSubmissionBeforeCompletionAndEnrichesSameEvent(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	event := HistoryEvent{
		EventKey: "vuja:session:1", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja", Host: "devbox",
		SessionID: "session-a", Shell: "zsh",
	}
	if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	stored, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].State != "running" || stored[0].HasExitCode {
		t.Fatalf("expected durable running submission, got %+v", stored)
	}

	event.CompletedAt = started.Add(3 * time.Second)
	event.Duration = 3 * time.Second
	event.ExitCode = 0
	event.HasExitCode = true
	completed, err := store.CompleteHistoryEvent(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("expected the running event to transition exactly once")
	}
	stored, err = store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].EventKey != event.EventKey || stored[0].State != "completed" || stored[0].Duration != 3*time.Second {
		t.Fatalf("expected completion to enrich the same event, got %+v", stored)
	}
	values, err := store.QueryArgumentValues(t.Context(), "/repo", "/repo", "ssh", 1, "forge@", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Value != "forge@api" {
		t.Fatalf("expected lifecycle completion to retain argument learning, got %+v", values)
	}
}

func TestFrecencyStoreCompletionDerivesTransitionsFromCanonicalEvents(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	commands := []string{"git checkout feature", "go test ./..."}
	for index, command := range commands {
		event := HistoryEvent{
			EventKey: fmt.Sprintf("vuja:session:%d", index+1), Command: command, NormalizedCommand: command,
			Cwd: "/repo", SubmittedAt: started.Add(time.Duration(index) * time.Second),
			StartedAt: started.Add(time.Duration(index) * time.Second), Source: "vuja", SessionID: "session-a",
		}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		event.CompletedAt = event.StartedAt.Add(500 * time.Millisecond)
		event.Duration = 500 * time.Millisecond
		event.HasExitCode = true
		if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
			t.Fatalf("complete %q: completed=%t err=%v", command, completed, err)
		}
	}

	exact, local := store.QueryExactTransitionsWithFallback(t.Context(), commands[0], "/repo")
	if !local || len(exact) != 1 || exact[0].NextCommand != commands[1] || exact[0].Count != 1 {
		t.Fatalf("expected canonical exact transition, local=%t entries=%+v", local, exact)
	}
	skeletons, local := store.QueryTransitionsWithFallback(t.Context(), ExtractSkeleton(commands[0]), "/repo")
	if !local || len(skeletons) != 1 || skeletons[0].NextSkeleton != ExtractSkeleton(commands[1]) || skeletons[0].Count != 1 {
		t.Fatalf("expected canonical skeleton transition, local=%t entries=%+v", local, skeletons)
	}
}

func TestFrecencyStorePruneRebuildsTransitionsFromRetainedCanonicalEvents(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	commands := []string{"git checkout feature", "go test ./...", "git status"}
	for index, command := range commands {
		event := HistoryEvent{
			EventKey: fmt.Sprintf("vuja:prune:%d", index+1), Command: command, NormalizedCommand: command,
			Cwd: "/repo", SubmittedAt: started.Add(time.Duration(index) * time.Second),
			StartedAt: started.Add(time.Duration(index) * time.Second), Source: "vuja", SessionID: "session-a",
		}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		event.CompletedAt = event.StartedAt.Add(500 * time.Millisecond)
		event.Duration = 500 * time.Millisecond
		event.HasExitCode = true
		if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
			t.Fatalf("complete %q: completed=%t err=%v", command, completed, err)
		}
	}

	if removed, err := store.PruneHistoryEvents(t.Context(), 2); err != nil || removed != 1 {
		t.Fatalf("prune canonical events: removed=%d err=%v", removed, err)
	}
	if exact, _ := store.QueryExactTransitionsWithFallback(t.Context(), commands[0], "/repo"); len(exact) != 0 {
		t.Fatalf("expected pruned transition to disappear, got %+v", exact)
	}
	exact, local := store.QueryExactTransitionsWithFallback(t.Context(), commands[1], "/repo")
	if !local || len(exact) != 1 || exact[0].NextCommand != commands[2] || exact[0].Count != 1 {
		t.Fatalf("expected retained transition to be rebuilt, local=%t entries=%+v", local, exact)
	}
}

func TestFrecencyStoreRejectsDuplicateHistoryEventKeysWithoutReopeningCompletion(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	original := HistoryEvent{
		EventKey: "vuja:session:duplicate", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja", SessionID: "session-a",
	}
	if err := store.RecordHistorySubmission(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	completed := original
	completed.CompletedAt = started.Add(time.Second)
	completed.Duration = time.Second
	completed.ExitCode = 0
	completed.HasExitCode = true
	if transitioned, err := store.CompleteHistoryEvent(t.Context(), completed); err != nil || !transitioned {
		t.Fatalf("expected initial completion, transitioned=%t err=%v", transitioned, err)
	}

	reused := original
	reused.Command = "ssh forge@other"
	reused.NormalizedCommand = reused.Command
	reused.StartedAt = started.Add(time.Minute)
	if err := store.RecordHistorySubmission(t.Context(), reused); err == nil {
		t.Fatal("expected a reused event key to be rejected")
	}
	events, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Command != original.Command || events[0].State != "completed" {
		t.Fatalf("expected completed event to remain unchanged, got %+v", events)
	}
}

func TestFrecencyStoreCompletionUsesPersistedSubmissionMetadataAndIsIdempotent(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	submitted := HistoryEvent{
		EventKey: "vuja:session:authoritative", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: started, StartedAt: started, Source: "vuja", State: "running",
	}
	if err := store.RecordHistorySubmission(t.Context(), submitted); err != nil {
		t.Fatal(err)
	}

	completion := submitted
	completion.Command = "curl --token private-value https://example.test"
	completion.NormalizedCommand = completion.Command
	completion.Cwd = "/wrong"
	completion.CompletedAt = started.Add(time.Second)
	completion.Duration = time.Second
	completion.ExitCode = 0
	completion.HasExitCode = true
	completed, err := store.CompleteHistoryEvent(t.Context(), completion)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("expected the first completion to transition the running event")
	}

	completed, err = store.CompleteHistoryEvent(t.Context(), completion)
	if err != nil {
		t.Fatal(err)
	}
	if completed {
		t.Fatal("expected a repeated completion to be an idempotent no-op")
	}

	var command, cwd string
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT cmd, cwd, count FROM history_entries`).Scan(&command, &cwd, &count); err != nil {
		t.Fatal(err)
	}
	if command != submitted.NormalizedCommand || cwd != submitted.Cwd || count != 1 {
		t.Fatalf("expected one aggregate from persisted submission metadata, got command=%q cwd=%q count=%d", command, cwd, count)
	}
	var outcomeCount int
	if err := store.db.QueryRowContext(t.Context(), `SELECT successes + failures FROM command_outcomes`).Scan(&outcomeCount); err != nil {
		t.Fatal(err)
	}
	if outcomeCount != 1 {
		t.Fatalf("expected one outcome after repeated completion, got %d", outcomeCount)
	}
}

func TestFrecencyStoreCompletionCountsFailedExecutionsInCanonicalFrequency(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	event := HistoryEvent{
		EventKey: "vuja:session:failed-frequency", Command: "ssh forge@offline",
		NormalizedCommand: "ssh forge@offline", Cwd: "/repo", SubmittedAt: started,
		StartedAt: started, Source: "vuja", State: "running",
	}
	if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.CompletedAt = started.Add(time.Second)
	event.Duration = time.Second
	event.ExitCode = 255
	event.HasExitCode = true
	if completed, err := store.CompleteHistoryEvent(t.Context(), event); err != nil || !completed {
		t.Fatalf("expected failed execution to complete once, completed=%t err=%v", completed, err)
	}

	entries, err := store.QueryLocal(t.Context(), "/repo", "ssh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Count != 1 {
		t.Fatalf("expected failed execution to contribute one frequency event, got %+v", entries)
	}
}

func TestFrecencyStoreAggregatesCanonicalFrequencyAcrossVujaAndImportedExecutions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO history_entries (cmd, cwd, count, last_used) VALUES (?, ?, ?, ?)`, []any{"ssh forge@api", "/repo", 2, canonicalTimestamp(now)}},
		{`INSERT INTO imported_history_entries (cmd, cwd, count, last_used, source) VALUES (?, ?, ?, ?, ?)`, []any{"ssh forge@api", "/repo", 3, canonicalTimestamp(now.Add(-time.Hour)), "atuin"}},
	} {
		if _, err := store.db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	local, err := store.QueryLocal(t.Context(), "/repo", "ssh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Count != 5 {
		t.Fatalf("expected one canonical local aggregate with five executions, got %+v", local)
	}
	global, err := store.QueryGlobal(t.Context(), "ssh", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].Count != 5 {
		t.Fatalf("expected one canonical global aggregate with five executions, got %+v", global)
	}
	snapshot, err := store.QuerySignalSnapshotRanked(t.Context(), "/repo", "/repo", "ssh", 10, "", "", "balanced")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Local) != 1 || snapshot.Local[0].Count != 5 {
		t.Fatalf("expected scoring to consume the same five-execution aggregate, got %+v", snapshot.Local)
	}
}

func TestFrecencyStoreRejectsSensitiveCommandsFromPersistentAndDerivedStores(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := "curl --token private-value https://example.test"
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "vuja:private", Command: command, NormalizedCommand: command,
		Cwd: "/repo", StartedAt: time.Now(), Source: "vuja",
	}); err == nil {
		t.Fatal("expected the event store to reject a sensitive submission")
	}
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "vuja:private-exact", Command: command, NormalizedCommand: "echo safe",
		Cwd: "/repo", StartedAt: time.Now(), Source: "vuja",
	}); err == nil {
		t.Fatal("expected exact command privacy to take precedence over a mismatched normalized command")
	}
	if err := store.Record(t.Context(), command, "/repo", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFeedback(t.Context(), command, "/repo", "typed"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExactTransition(t.Context(), "git status", command, "/repo", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{{
		EventKey: "atuin:private", Command: command, Source: "atuin", Imported: true,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"history_events", "history_entries", "command_outcomes", "suggestion_feedback", "exact_command_transitions"} {
		var count int
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected sensitive command not to enter %s, got %d rows", table, count)
		}
	}
}

func TestFrecencyStoreRejectsLeadingSpaceBeforePersistentOrDerivedWrites(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	command := " ssh forge@private"
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "vuja:private", Command: command, NormalizedCommand: strings.TrimSpace(command),
		Cwd: "/repo", StartedAt: time.Now(), Source: "vuja",
	}); err == nil {
		t.Fatal("expected the event store to reject a leading-space submission")
	}
	if err := store.Record(t.Context(), command, "/repo", 0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"history_events", "history_entries", "command_outcomes"} {
		var count int
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected leading-space command not to enter %s, got %d rows", table, count)
		}
	}
}

func TestFrecencyStoreSubmissionSurvivesImmediateProcessBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "vuja:durable", Command: "ssh forge@long-running", Cwd: "/repo",
		SubmittedAt: started, StartedAt: started, Source: "vuja", SessionID: "terminated-session",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != "vuja:durable" || events[0].State != "running" {
		t.Fatalf("expected the submitted command to survive reopening, got %+v", events)
	}
}

func TestFrecencyStoreReconcilesOnlyUnfinishedEventsFromPreviousSessions(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, event := range []HistoryEvent{
		{EventKey: "old", Command: "ssh forge@old", StartedAt: time.Now(), Source: "vuja", SessionID: "old-session"},
		{EventKey: "active", Command: "ssh forge@active", StartedAt: time.Now(), Source: "vuja", SessionID: "active-session"},
		{EventKey: "other-live", Command: "ssh forge@live", StartedAt: time.Now(), Source: "vuja", SessionID: "other-live-session"},
	} {
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := store.ReconcileInterruptedHistory(t.Context(), "active-session", "other-live-session")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("expected one interrupted event, got %d", changed)
	}
	events, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, event := range events {
		states[event.EventKey] = event.State
	}
	if states["old"] != "interrupted" || states["active"] != "running" || states["other-live"] != "running" {
		t.Fatalf("unexpected reconciled states: %+v", states)
	}
}

func TestFrecencyStorePrunesCompletedEventsWithoutRemovingRunningCommands(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for index := range 5 {
		event := HistoryEvent{
			EventKey: fmt.Sprintf("done-%d", index), Command: fmt.Sprintf("ssh forge@host-%d", index),
			Cwd: "/repo", StartedAt: now.Add(time.Duration(index) * time.Minute), Source: "vuja", SessionID: "session-a",
		}
		if err := store.RecordHistorySubmission(t.Context(), event); err != nil {
			t.Fatal(err)
		}
		event.CompletedAt = event.StartedAt.Add(time.Second)
		event.Duration = time.Second
		event.HasExitCode = true
		if _, err := store.CompleteHistoryEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "running", Command: "ssh forge@long-running", Cwd: "/repo", StartedAt: now.Add(10 * time.Minute), Source: "vuja", SessionID: "session-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), []HistoryEvent{{
		EventKey: "atuin:external", Command: "ssh forge@imported", NormalizedCommand: "ssh forge@imported",
		Cwd: "/repo", StartedAt: now.Add(20 * time.Minute), Source: "atuin", State: "completed", Imported: true,
	}}); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneHistoryEvents(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("expected three completed events to be pruned, got %d", removed)
	}
	events, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].EventKey != "atuin:external" || events[1].EventKey != "running" {
		t.Fatalf("expected imports, two completed events, and the running command to remain, got %+v", events)
	}
	var aggregates int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM history_entries`).Scan(&aggregates); err != nil {
		t.Fatal(err)
	}
	if aggregates != 2 {
		t.Fatalf("expected aggregates to be rebuilt from retained events, got %d", aggregates)
	}
}

func TestFrecencyStorePrunesLogicalExecutionsInsideCompactMigratedEvents(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	for _, event := range []HistoryEvent{
		{
			EventKey: "new-compact", Command: "ssh forge@new", NormalizedCommand: "ssh forge@new",
			Cwd: "/repo", SubmittedAt: now, StartedAt: now, CompletedAt: now, Source: "legacy-vuja",
			State: "completed", HasExitCode: true, Occurrences: 4,
		},
		{
			EventKey: "old-compact", Command: "ssh forge@old", NormalizedCommand: "ssh forge@old",
			Cwd: "/repo", SubmittedAt: now.Add(-time.Hour), StartedAt: now.Add(-time.Hour),
			CompletedAt: now.Add(-time.Hour), Source: "legacy-vuja", State: "completed",
			HasExitCode: true, Occurrences: 3,
		},
	} {
		if err := store.RecordHistoryEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.PruneHistoryEvents(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 5 {
		t.Fatalf("expected five logical executions to be pruned, got %d", removed)
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventKey != "new-compact" || events[0].Occurrences != 2 {
		t.Fatalf("expected newest compact event to retain exactly two occurrences, got %+v", events)
	}
	var count int
	if err := store.db.QueryRowContext(t.Context(), `SELECT count FROM history_entries WHERE cmd = ?`, "ssh forge@new").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected rebuilt canonical frequency to retain two occurrences, got %d", count)
	}
}

func TestFrecencyStoreClearHistoryRemovesDerivedHistoryState(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, statement := range []string{
		`INSERT INTO history_events (event_key, command, normalized_command, submitted_at, started_at, source, state) VALUES ('event', 'ssh forge@api', 'ssh forge@api', '2026-08-31 12:00:00', '2026-08-31 12:00:00', 'vuja', 'completed')`,
		`INSERT INTO history_entries (cmd, cwd) VALUES ('ssh forge@api', '/repo')`,
		`INSERT INTO command_transitions (prev_skeleton, next_skeleton, cwd) VALUES ('git status', 'ssh forge@api', '/repo')`,
		`INSERT INTO suggestion_feedback (cmd, cwd) VALUES ('ssh forge@api', '/repo')`,
		`INSERT INTO directory_navigation_sources (path, source) VALUES ('/repo', 'history')`,
		`INSERT INTO metadata (key, value) VALUES ('history_imported_at:atuin', '2026-08-31 12:00:00')`,
		`INSERT INTO metadata (key, value) VALUES ('history_import_failure_count:atuin', '2')`,
		`INSERT INTO metadata (key, value) VALUES ('history_import_failed_at:atuin', '2026-08-31 12:01:00')`,
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"history_events", "history_entries", "command_transitions", "suggestion_feedback"} {
		var count int
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected %s to be empty, got %d rows", table, count)
		}
	}
	stats, err := store.HistoryStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.ImportFreshness) != 0 || len(stats.ImportFailures) != 0 {
		t.Fatalf("expected clear to remove import diagnostics, got freshness=%v failures=%v", stats.ImportFreshness, stats.ImportFailures)
	}
}

func TestFrecencyStoreTracksSanitizedOptionalImportFailures(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	failedAt := time.Date(2026, time.August, 31, 12, 5, 0, 0, time.UTC)
	if err := store.RecordHistoryImportFailure(t.Context(), "atuin", failedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHistoryImportFailure(t.Context(), "atuin", failedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.HistoryStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failure := stats.ImportFailures["atuin"]
	if failure.Count != 2 || !failure.LastFailed.Equal(failedAt.Add(time.Minute)) {
		t.Fatalf("expected sanitized failure count and timestamp, got %+v", failure)
	}
}

func TestFrecencyStoreReportsTheLastSuccessfulPersistenceTime(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	originalSubmission := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := store.RecordHistorySubmission(t.Context(), HistoryEvent{
		EventKey: "vuja:replayed-submission", Command: "ssh forge@api", NormalizedCommand: "ssh forge@api",
		Cwd: "/repo", SubmittedAt: originalSubmission, StartedAt: originalSubmission,
		Source: "vuja", SessionID: "session-a",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.HistoryStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.MostRecentPersisted.After(originalSubmission.Add(24 * time.Hour)) {
		t.Fatalf("expected database write time rather than the original submission time, got %s", stats.MostRecentPersisted)
	}
}

func TestFrecencyStore_SkipsUnchangedImportedHistoryEventWrites(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	events := []HistoryEvent{{
		EventKey:  "atuin:first",
		Command:   "just deploy staging",
		StartedAt: time.Date(2026, time.July, 30, 7, 0, 0, 0, time.UTC),
		Source:    "atuin",
	}}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `
CREATE TRIGGER reject_history_event_rewrite
BEFORE DELETE ON history_events
WHEN OLD.imported = 1
BEGIN
    SELECT RAISE(ABORT, 'unchanged history events were rewritten');
END;
`); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatalf("expected unchanged import to skip writes, got %v", err)
	}
}

func TestFrecencyStore_PreservesUnknownImportedHistoryEventRecency(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	events := []HistoryEvent{
		{EventKey: "zsh:older", Command: "git status", Source: "zsh", HistoryOrder: 1, Imported: true},
		{EventKey: "zsh:newer", Command: "git log", Source: "zsh", HistoryOrder: 2, Imported: true},
	}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	stored, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].EventKey != "zsh:newer" || stored[0].HistoryOrder != 2 || !stored[0].StartedAt.IsZero() {
		t.Fatalf("expected unknown imported recency to remain unknown, got %+v", stored)
	}
}
