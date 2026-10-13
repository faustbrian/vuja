package scoring

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

func DefaultHistoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "vuja", "history.db"), nil
}

// InspectHistory reuses the store's health/statistics queries without schema
// initialization, permission repair, checkpoint workers, replay or publication.
func InspectHistory(ctx context.Context, path string) (HistoryStoreHealth, HistoryStoreStats, error) {
	info, err := os.Stat(path)
	if err != nil {
		return HistoryStoreHealth{}, HistoryStoreStats{}, err
	}
	if !info.Mode().IsRegular() {
		return HistoryStoreHealth{}, HistoryStoreStats{}, errors.New("history database is not a regular file")
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(1000)"}}
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return HistoryStoreHealth{}, HistoryStoreStats{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := &FrecencyStore{db: db, dbPath: path}
	health, err := store.HistoryHealth(ctx)
	if err != nil {
		return health, HistoryStoreStats{}, err
	}
	stats, err := store.HistoryStats(ctx)
	return health, stats, err
}
