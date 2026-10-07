package root

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

const (
	historyRecoveryRetryInterval = 5 * time.Second
	historyRecoveryRetryBatch    = 256
)

// historyRecoveryJournalPending checks only the durable journal boundary. It
// avoids opening SQLite or rewriting an empty checkpoint on every retry tick.
// A concurrent append may become visible on the next bounded tick, but can
// never be acknowledged or removed by this read-only check.
func historyRecoveryJournalPending() (bool, error) {
	path, err := historyRecoveryJournalPath()
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return false, nil
	}
	offset, present, err := readHistoryRecoveryOffset()
	if err != nil {
		return false, err
	}
	return !present || offset < 0 || offset != info.Size(), nil
}

// retryHistoryRecoveryOnce moves one bounded batch from the fsynced fallback
// journal into SQLite and the current recall generation. Previous-session
// records may not have been published locally, and the change feed excludes
// this process's own writes. Publication precedes checkpoint advancement so
// a failed refresh remains eligible for idempotent retry.
func retryHistoryRecoveryOnce(ctx context.Context, store *scoring.FrecencyStore) (bool, error) {
	if store == nil {
		return false, nil
	}
	pending, err := historyRecoveryJournalPending()
	if err != nil || !pending {
		return false, err
	}
	// Keep the same lock order as history clear: generation, then journal.
	canonicalHistoryMutationMu.Lock()
	defer canonicalHistoryMutationMu.Unlock()
	if _, err := replayHistoryRecoveryJournalBatchObserved(ctx, store, historyRecoveryRetryBatch, publishRecoveredHistoryLocked); err != nil {
		return false, err
	}
	scoring.InvalidateSignalCache()
	spec.NotifyCompletionUpdate()
	return true, nil
}

func publishRecoveredHistoryLocked(ctx context.Context, store *scoring.FrecencyStore, keys []string) error {
	events, err := store.QueryHistoryEventsByKeys(ctx, keys)
	if err != nil {
		return err
	}
	unique := make(map[string]bool, len(keys))
	for _, key := range keys {
		unique[key] = true
	}
	if len(events) != len(unique) {
		// Concurrent retention or a reset removed a replayed event. Reload the
		// retained generation instead of resurrecting its journal payload.
		_, err := publishCanonicalStoreHistoryLocked(ctx, store)
		return err
	}
	for _, event := range events {
		integration.PublishCanonicalHistoryEntry(historyEventEntry(event))
	}
	scoring.InvalidateSignalCache()
	spec.NotifyCompletionUpdate()
	return nil
}

func startHistoryRecoveryRetry(store *scoring.FrecencyStore) func() {
	if store == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(historyRecoveryRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				retryCtx, retryCancel := context.WithTimeout(ctx, 2*time.Second)
				_, err := retryHistoryRecoveryOnce(retryCtx, store)
				retryCancel()
				if err != nil && ctx.Err() == nil {
					logger.Errorf("failed to retry canonical history recovery: %v", err)
				}
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

type historyRecoveryRetryManager struct {
	mu   sync.Mutex
	stop func()
}

func (m *historyRecoveryRetryManager) Restart(store *scoring.FrecencyStore) {
	if m == nil {
		return
	}
	next := startHistoryRecoveryRetry(store)
	m.mu.Lock()
	previous := m.stop
	m.stop = next
	m.mu.Unlock()
	if previous != nil {
		previous()
	}
}

func (m *historyRecoveryRetryManager) Close() {
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
