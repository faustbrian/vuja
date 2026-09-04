package scoring

import (
	"path/filepath"
	"testing"
)

func TestHistoryChangeFeedDetectsWhenAReaderFallsBehindRetention(t *testing.T) {
	store, err := NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(t.Context(), `
WITH RECURSIVE changes(value) AS (
    VALUES(1)
    UNION ALL
    SELECT value + 1 FROM changes WHERE value < 8193
)
INSERT INTO history_changes (origin, kind, event_key)
SELECT 'session-b', 'upsert', printf('event-%d', value) FROM changes;
DELETE FROM history_changes WHERE sequence <= 1;
`); err != nil {
		t.Fatal(err)
	}

	batch, err := store.QueryHistoryChanges(t.Context(), 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Gap || batch.Latest != 8193 || len(batch.Changes) != 256 || batch.Changes[0].Sequence != 2 {
		t.Fatalf("expected a bounded-feed gap and retained changes, got %+v", batch)
	}
}
