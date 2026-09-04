package root

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

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
// journal into the canonical SQLite store. The in-memory event was published
// before the shell was allowed to execute, so only derived ranking state must
// be invalidated after replay; other sessions observe the same database writes
// through the canonical change feed.
func retryHistoryRecoveryOnce(ctx context.Context, store *scoring.FrecencyStore) (bool, error) {
	if store == nil {
		return false, nil
	}
	pending, err := historyRecoveryJournalPending()
	if err != nil || !pending {
		return false, err
	}
	if _, err := replayHistoryRecoveryJournalBatch(ctx, store, historyRecoveryRetryBatch); err != nil {
		return false, err
	}
	scoring.InvalidateSignalCache()
	spec.NotifyCompletionUpdate()
	return true, nil
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
