package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	generatorColdBudget = 20 * time.Millisecond
	generatorCacheTTL   = 2 * time.Second
	generatorCacheLimit = 512
)

type generatorCacheEntry struct {
	results    []Suggestion
	updatedAt  time.Time
	ready      chan struct{}
	populated  bool
	refreshing bool
}

var (
	generatorID    atomic.Uint64
	generatorMu    sync.Mutex
	generatorCache = make(map[string]*generatorCacheEntry)
)

func cacheGenerator(generator GeneratorFunc) GeneratorFunc {
	if generator == nil {
		return nil
	}

	id := generatorID.Add(1)
	return func(tokens []string, prefix string, partial string) []Suggestion {
		scope := generatorScope(partial)
		key := generatorKey(id, tokens, prefix, scope)
		now := time.Now()

		generatorMu.Lock()
		entry := generatorCache[key]
		if entry != nil && entry.populated && now.Sub(entry.updatedAt) < generatorCacheTTL {
			results := cloneSuggestions(entry.results)
			generatorMu.Unlock()
			return results
		}

		if entry == nil {
			entry = &generatorCacheEntry{}
			generatorCache[key] = entry
		}
		stale := cloneSuggestions(entry.results)
		populated := entry.populated
		if !entry.refreshing {
			entry.refreshing = true
			entry.ready = make(chan struct{})
			ready := entry.ready
			tokenCopy := append([]string(nil), tokens...)
			go refreshGenerator(key, entry, ready, generator, tokenCopy, prefix, scope)
		}
		ready := entry.ready
		generatorMu.Unlock()

		if populated {
			return stale
		}

		timer := time.NewTimer(generatorColdBudget)
		defer timer.Stop()
		select {
		case <-ready:
			generatorMu.Lock()
			results := cloneSuggestions(entry.results)
			generatorMu.Unlock()
			return results
		case <-timer.C:
			return nil
		}
	}
}

func refreshGenerator(
	key string,
	entry *generatorCacheEntry,
	ready chan struct{},
	generator GeneratorFunc,
	tokens []string,
	prefix string,
	partial string,
) {
	results := generator(tokens, prefix, partial)

	generatorMu.Lock()
	entry.results = cloneSuggestions(results)
	entry.updatedAt = time.Now()
	entry.populated = true
	entry.refreshing = false
	close(ready)
	pruneGeneratorCacheLocked(key)
	generatorMu.Unlock()
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
	if len(generatorCache) <= generatorCacheLimit {
		return
	}
	for key, entry := range generatorCache {
		if key == currentKey || entry.refreshing {
			continue
		}
		if time.Since(entry.updatedAt) > generatorCacheTTL {
			delete(generatorCache, key)
		}
	}
}

func cloneSuggestions(suggestions []Suggestion) []Suggestion {
	return append([]Suggestion(nil), suggestions...)
}

func resetGeneratorCache() {
	generatorMu.Lock()
	defer generatorMu.Unlock()
	generatorCache = make(map[string]*generatorCacheEntry)
}
