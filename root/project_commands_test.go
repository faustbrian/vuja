package root

import (
	"fmt"
	"testing"
	"time"
)

func TestProjectCommandCacheIsBounded(t *testing.T) {
	projectCommandCache.Lock()
	original := projectCommandCache.entries
	projectCommandCache.entries = make(map[string]projectCommandCacheEntry)
	for index := 0; index < projectCommandCacheLimit+20; index++ {
		root := fmt.Sprintf("/repo/%d", index)
		projectCommandCache.entries[root] = projectCommandCacheEntry{lastUsed: time.Unix(int64(index+1), 0)}
	}
	pruneProjectCommandCacheLocked("/repo/current")
	size := len(projectCommandCache.entries)
	projectCommandCache.entries = original
	projectCommandCache.Unlock()

	if size > projectCommandCacheLimit {
		t.Fatalf("expected at most %d cached projects, got %d", projectCommandCacheLimit, size)
	}
}
