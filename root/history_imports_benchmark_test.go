package root

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
)

// Same production adapter boundary on both compared revisions. Measure warm,
// unchanged Atuin+zsh refresh, not fixture creation or compilation. The repeated
// snapshots deliberately exercise whether membership persistence adds writes.
func BenchmarkHistoryAdapterRefresh(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			home := b.TempDir()
			b.Setenv("HOME", home)
			b.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			previous := integration.RichHistorySnapshot()
			b.Cleanup(func() { integration.PublishCanonicalHistory(previous) })
			atuinPath := integration.DefaultAtuinHistoryPath(home)
			if err := os.MkdirAll(filepath.Dir(atuinPath), 0o700); err != nil {
				b.Fatal(err)
			}
			db, err := sql.Open("sqlite", atuinPath)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(b.Context(), `CREATE TABLE history
(id TEXT, command TEXT, timestamp INTEGER, duration INTEGER, exit INTEGER, cwd TEXT,
hostname TEXT, session TEXT, deleted_at INTEGER)`); err != nil {
				b.Fatal(err)
			}
			tx, err := db.BeginTx(b.Context(), nil)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			statement, err := tx.PrepareContext(b.Context(), `INSERT INTO history
VALUES (?, ?, ?, 1000000000, 0, '/repo', 'fixture', 'fixture', NULL)`)
			if err != nil {
				b.Fatal(err)
			}
			defer statement.Close()
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			var shell strings.Builder
			for index := range size {
				stamp := at.Add(time.Duration(index) * time.Minute)
				command := fmt.Sprintf("ssh example-%d", index%100)
				if _, err := statement.ExecContext(b.Context(), fmt.Sprintf("id-%d", index), command, stamp.UnixNano()); err != nil {
					b.Fatal(err)
				}
				fmt.Fprintf(&shell, ": %d:1;%s\n", stamp.Unix(), command)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			shellPath := filepath.Join(home, ".zsh_history")
			if err := os.WriteFile(shellPath, []byte(shell.String()), 0o600); err != nil {
				b.Fatal(err)
			}
			cfg := config.Get()
			previousPath := cfg.History.Integrations.Shell.Path
			cfg.History.Integrations.Shell.Path = shellPath
			b.Cleanup(func() { cfg.History.Integrations.Shell.Path = previousPath })
			path := filepath.Join(b.TempDir(), "history.db")
			store, err := scoring.NewFrecencyStore(path)
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			if err := importSelectedHistory(store, "zsh", true, true); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := importSelectedHistory(store, "zsh", true, true); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if events, err := store.QueryAllHistoryEvents(b.Context()); err != nil || len(events) != size {
				b.Fatalf("refresh changed logical execution count: count=%d expected=%d error=%v", len(events), size, err)
			}
			inspection, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			if err != nil {
				b.Fatal(err)
			}
			defer inspection.Close()
			var pages, pageSize int
			if err := inspection.QueryRowContext(b.Context(), `PRAGMA page_count`).Scan(&pages); err != nil {
				b.Fatal(err)
			}
			if err := inspection.QueryRowContext(b.Context(), `PRAGMA page_size`).Scan(&pageSize); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(pages*pageSize), "logical-db-bytes")
		})
	}
}
