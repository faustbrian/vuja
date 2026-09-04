package spec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faustbrian/vuja/internal/cachemetrics"
)

const (
	generatorCacheTTL   = 2 * time.Second
	generatorCacheLimit = 512
	generatorRunTimeout = 750 * time.Millisecond
)

type generatorCacheEntry struct {
	results    []Suggestion
	updatedAt  time.Time
	ready      chan struct{}
	populated  bool
	refreshing bool
}

type generatorRefresh struct {
	key    string
	cancel context.CancelFunc
}

var (
	generatorID           atomic.Uint64
	generatorMu           sync.Mutex
	generatorCache        = make(map[string]*generatorCacheEntry)
	generatorCacheMetrics = cachemetrics.New("generator")
	generatorRefreshes    = make(map[uint64]generatorRefresh)
)

func cacheGenerator(generator GeneratorFunc) GeneratorFunc {
	if generator == nil {
		return nil
	}
	if isImmediateGenerator(generator) {
		return generator
	}

	id := generatorID.Add(1)
	return func(ctx context.Context, tokens []string, prefix string, partial string) []Suggestion {
		scope := generatorScope(partial)
		key := generatorKey(id, tokens, prefix, scope)
		now := time.Now()

		generatorMu.Lock()
		entry := generatorCache[key]
		if entry != nil && entry.populated && now.Sub(entry.updatedAt) < generatorCacheTTL {
			generatorCacheMetrics.Hit()
			results := cloneSuggestions(entry.results)
			generatorMu.Unlock()
			return results
		}

		generatorCacheMetrics.Miss()
		if entry == nil {
			pruneGeneratorCacheLocked("")
			if len(generatorCache) >= generatorCacheLimit {
				generatorMu.Unlock()
				return nil
			}
			entry = &generatorCacheEntry{}
			generatorCache[key] = entry
		}
		stale := cloneSuggestions(entry.results)
		populated := entry.populated
		if !entry.refreshing {
			if active, ok := generatorRefreshes[id]; ok {
				active.cancel()
			}
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generatorRunTimeout)
			generatorRefreshes[id] = generatorRefresh{key: key, cancel: cancel}
			entry.refreshing = true
			entry.ready = make(chan struct{})
			ready := entry.ready
			tokenCopy := append([]string(nil), tokens...)
			go refreshGenerator(refreshCtx, id, key, entry, ready, generator, tokenCopy, prefix, scope)
		}
		generatorMu.Unlock()
		if populated {
			return stale
		}
		return nil
	}
}

func refreshGenerator(
	ctx context.Context,
	id uint64,
	key string,
	entry *generatorCacheEntry,
	ready chan struct{},
	generator GeneratorFunc,
	tokens []string,
	prefix string,
	partial string,
) {
	results := generator(ctx, tokens, prefix, partial)

	generatorMu.Lock()
	if active, ok := generatorRefreshes[id]; ok && active.key == key {
		active.cancel()
		delete(generatorRefreshes, id)
	}
	entry.results = cloneSuggestions(results)
	entry.updatedAt = time.Now()
	entry.populated = true
	entry.refreshing = false
	close(ready)
	pruneGeneratorCacheLocked(key)
	generatorMu.Unlock()
	notifyCompletionUpdate()
}

func generatorKey(id uint64, tokens []string, prefix string, scope string) string {
	baseTokens := tokens
	if len(baseTokens) > 0 {
		baseTokens = baseTokens[:len(baseTokens)-1]
	}
	cwd := GetCWD()
	return fmt.Sprintf(
		"%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		id,
		cwd,
		strings.Join(baseTokens, "\x00"),
		prefix,
		scope,
		generatorState(cwd),
	)
}

func generatorScope(partial string) string {
	if index := strings.LastIndexAny(partial, `/\`); index >= 0 {
		return partial[:index+1]
	}
	return ""
}

func generatorState(cwd string) string {
	paths := []string{
		cwd,
		filepath.Join(cwd, ".git", "HEAD"),
		filepath.Join(cwd, ".git", "index"),
		filepath.Join(cwd, "package.json"),
		filepath.Join(cwd, "composer.json"),
		filepath.Join(cwd, "justfile"),
		filepath.Join(cwd, "Justfile"),
		filepath.Join(cwd, "Makefile"),
	}

	var state strings.Builder
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&state, "%s:%d:%d;", path, info.ModTime().UnixNano(), info.Size())
	}
	return state.String()
}

func pruneGeneratorCacheLocked(currentKey string) {
	for len(generatorCache) >= generatorCacheLimit {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range generatorCache {
			if key == currentKey || entry.refreshing {
				continue
			}
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey, oldest = key, entry.updatedAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(generatorCache, oldestKey)
		generatorCacheMetrics.Eviction()
	}
}

func cloneSuggestions(suggestions []Suggestion) []Suggestion {
	return append([]Suggestion(nil), suggestions...)
}

func resetGeneratorCache() {
	generatorMu.Lock()
	defer generatorMu.Unlock()
	generatorCache = make(map[string]*generatorCacheEntry)
	for id, active := range generatorRefreshes {
		active.cancel()
		delete(generatorRefreshes, id)
	}
}
