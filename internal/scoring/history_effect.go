package scoring

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// HasCanonicalHistoryEffect reads only the finite effect needed by recovery
// health, without loading command text or replaying/publishing history.
func (f *FrecencyStore) HasCanonicalHistoryEffect(ctx context.Context, eventKey string, completed bool) (bool, error) {
	if f == nil || strings.TrimSpace(eventKey) == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var hasOutcome bool
	err := f.db.QueryRowContext(ctx, `SELECT exit_code IS NOT NULL FROM history_events WHERE event_key = ?`, strings.TrimSpace(eventKey)).Scan(&hasOutcome)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !completed || hasOutcome, nil
}
