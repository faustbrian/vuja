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
	db, err := openHistoryInspectionDB(path)
	if err != nil {
		return HistoryStoreHealth{}, HistoryStoreStats{}, err
	}
	defer db.Close()
	store := &FrecencyStore{db: db, dbPath: path}
	health, err := store.HistoryHealth(ctx)
	if err != nil {
		return health, HistoryStoreStats{}, err
	}
	stats, err := store.HistoryStats(ctx)
	return health, stats, err
}

// InspectHistoryEventForgotten checks the existing exclusion authority even
// when a writable store could not be initialized. It does not create a store,
// migrate schema, repair permissions, replay recovery or start workers.
func InspectHistoryEventForgotten(ctx context.Context, path, eventKey string) (bool, error) {
	db, err := openHistoryInspectionDB(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	return (&FrecencyStore{db: db}).HistoryEventForgotten(ctx, eventKey)
}

// InspectHistoryEventForgotten reads this store's durable exclusion authority
// independently of its writable connection pool. It remains usable after Close
// or a failed write, without opening an unrelated default database or creating
// schema, workers, or a replacement history authority.
func (f *FrecencyStore) InspectHistoryEventForgotten(ctx context.Context, eventKey string) (bool, error) {
	if f == nil || f.dbPath == "" {
		return false, errors.New("history store path is unavailable")
	}
	return InspectHistoryEventForgotten(ctx, f.dbPath, eventKey)
}

func openHistoryInspectionDB(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("history database is not a regular file")
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(1000)"}}
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
