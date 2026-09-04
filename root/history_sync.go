package root

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

const canonicalHistorySyncInterval = 500 * time.Millisecond

func historySyncStartCursor(store *scoring.FrecencyStore) int64 {
	if store == nil {
		return 0
	}
	return store.HistorySnapshotSequence()
}

func syncCanonicalHistoryOnce(
	ctx context.Context,
	store *scoring.FrecencyStore,
	sessionID string,
	cursor int64,
) (int64, bool, error) {
	batch, err := store.QueryHistoryChanges(ctx, cursor, 256)
	if err != nil {
		return cursor, false, err
	}
	fullRefresh := batch.Gap
	next := cursor
	foreignKeys := make(map[string]bool)
	for _, change := range batch.Changes {
		if change.Sequence > next {
			next = change.Sequence
		}
		if strings.TrimSpace(change.Origin) == strings.TrimSpace(sessionID) {
			continue
		}
		switch strings.TrimSpace(change.Kind) {
		case "upsert":
			if eventKey := strings.TrimSpace(change.EventKey); eventKey != "" {
				foreignKeys[eventKey] = true
			} else {
				fullRefresh = true
			}
		case "reset":
			fullRefresh = true
		default:
			fullRefresh = true
		}
	}
	if !fullRefresh && len(foreignKeys) == 0 {
		return next, false, nil
	}
	if !fullRefresh {
		keys := make([]string, 0, len(foreignKeys))
		for eventKey := range foreignKeys {
			keys = append(keys, eventKey)
		}
		sort.Strings(keys)
		events, queryErr := store.QueryHistoryEventsByKeys(ctx, keys)
		if queryErr != nil {
			return cursor, false, queryErr
		}
		if len(events) == len(keys) {
			canonicalHistoryMutationMu.Lock()
			for _, event := range events {
				integration.PublishCanonicalHistoryEntry(historyEventEntry(event))
			}
			canonicalHistoryMutationMu.Unlock()
			scoring.InvalidateSignalCache()
			spec.NotifyCompletionUpdate()
			return next, true, nil
		}
		// A reset or prune may have removed an event after the feed read. Reload
		// the generation rather than publishing a partial cross-window update.
		fullRefresh = true
	}
	if fullRefresh {
		// Latest was captured before the snapshot load. A commit racing after
		// that boundary remains visible to the next poll instead of being
		// swallowed.
		if _, err := publishCanonicalStoreHistory(ctx, store); err != nil {
			return cursor, false, err
		}
		return batch.Latest, true, nil
	}
	return next, false, nil
}

func startCanonicalHistorySync(store *scoring.FrecencyStore, sessionID string) func() {
	if store == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(canonicalHistorySyncInterval)
		defer ticker.Stop()
		cursor := historySyncStartCursor(store)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
				next, _, err := syncCanonicalHistoryOnce(pollCtx, store, sessionID, cursor)
				pollCancel()
				if err != nil {
					if ctx.Err() == nil {
						logger.Errorf("failed to synchronize canonical history across sessions: %v", err)
					}
					continue
				}
				cursor = next
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

type canonicalHistorySyncManager struct {
	mu   sync.Mutex
	stop func()
}

func (m *canonicalHistorySyncManager) Restart(store *scoring.FrecencyStore, sessionID string) {
	if m == nil {
		return
	}
	next := startCanonicalHistorySync(store, sessionID)
	m.mu.Lock()
	previous := m.stop
	m.stop = next
	m.mu.Unlock()
	if previous != nil {
		previous()
	}
}

func (m *canonicalHistorySyncManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	stop := m.stop
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}
