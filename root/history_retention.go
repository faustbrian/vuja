package root

import (
	"context"
	"errors"
	"time"

	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/faustbrian/vuja/v2/internal/logger"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

// The same policy is applied at startup, completion, import refresh and
// journal recovery before recovered entries are published or acknowledged.
func applyConfiguredHistoryRetention(ctx context.Context, store *scoring.FrecencyStore, cfg config.HistoryConfig, now time.Time) (int64, error) {
	count := 0
	if cfg.Retention == "bounded" {
		count = cfg.MaxEvents
	}
	var cutoff time.Time
	if cfg.MaxAge > 0 {
		cutoff = now.Add(-time.Duration(cfg.MaxAge))
	}
	return store.PruneHistoryEventsWithRetention(ctx, count, cutoff)
}

func historyRetentionEnabled(cfg config.HistoryConfig) bool {
	return cfg.MaxAge > 0 || cfg.Retention == "bounded" && cfg.MaxEvents > 0
}

func recordHistoryRetentionFailure(err error) {
	if errors.Is(err, scoring.ErrHistoryUnattributedLearning) {
		logger.Debugf("history retention not applied: legacy learning has no execution attribution; canonical recording remains available")
		return
	}
	recordHistoryDerivedFailure("retention", err)
}
