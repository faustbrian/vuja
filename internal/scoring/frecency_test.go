package scoring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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

	events := []HistoryEvent{{EventKey: "zsh:unknown", Command: "git status", Source: "zsh", Imported: true}}
	if err := store.ReplaceImportedHistoryEvents(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	stored, err := store.QueryHistoryEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || !stored[0].StartedAt.IsZero() {
		t.Fatalf("expected unknown imported recency to remain unknown, got %+v", stored)
	}
}
