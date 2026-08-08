package spec

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupDoesNotBlockOnSlowGeneratorAndReusesCachedResults(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)

	var calls atomic.Int32
	release := make(chan struct{})
	Register(&Spec{
		Name: "slowgen",
		Generator: func(_ context.Context, _ []string, _ string, _ string) []Suggestion {
			calls.Add(1)
			<-release
			return []Suggestion{{Cmd: "cached-result", Desc: "dynamic"}}
		},
	})

	firstResult := make(chan []Suggestion, 1)
	go func() {
		firstResult <- Lookup("slowgen ")
	}()

	var first []Suggestion
	select {
	case first = <-firstResult:
		close(release)
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-firstResult
		t.Fatal("cold lookup blocked on the generator")
	}
	if hasSuggestion(first, "slowgen cached-result") {
		t.Fatal("cold lookup unexpectedly waited for the slow generator")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		results := Lookup("slowgen ")
		if hasSuggestion(results, "slowgen cached-result") {
			if calls.Load() != 1 {
				t.Fatalf("generator called %d times while populating one cache entry", calls.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("cached generator result did not become available")
}

func TestLookupReusesCompleteGeneratorResultsWhileQueryChanges(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)

	var calls atomic.Int32
	Register(&Spec{
		Name: "cachedfilter",
		Generator: func(_ context.Context, _ []string, _ string, partial string) []Suggestion {
			calls.Add(1)
			candidates := []Suggestion{{Cmd: "alpha"}, {Cmd: "beta"}}
			if partial == "" {
				return candidates
			}
			var filtered []Suggestion
			for _, candidate := range candidates {
				if strings.HasPrefix(candidate.Cmd, partial) {
					filtered = append(filtered, candidate)
				}
			}
			return filtered
		},
	})

	if !hasSuggestion(waitForGeneratorSuggestion(t, "cachedfilter a"), "cachedfilter alpha") {
		t.Fatal("expected alpha suggestion")
	}
	if !hasSuggestion(waitForGeneratorSuggestion(t, "cachedfilter b"), "cachedfilter beta") {
		t.Fatal("expected beta suggestion from the same complete cache entry")
	}
	if calls.Load() != 1 {
		t.Fatalf("generator called %d times for one completion scope", calls.Load())
	}
}

func TestGeneratorCacheCancelsStaleDirectoryRefresh(t *testing.T) {
	ResetRegistry()
	t.Cleanup(func() {
		SetShellCWD("")
		ResetRegistry()
	})

	started := make(chan struct{}, 2)
	canceled := make(chan struct{}, 1)
	Register(&Spec{
		Name: "cancelgen",
		Generator: func(ctx context.Context, _ []string, _ string, _ string) []Suggestion {
			started <- struct{}{}
			<-ctx.Done()
			select {
			case canceled <- struct{}{}:
			default:
			}
			return nil
		},
	})

	SetShellCWD(t.TempDir())
	_ = Lookup("cancelgen ")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first generator refresh did not start")
	}

	SetShellCWD(t.TempDir())
	_ = Lookup("cancelgen ")
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("new completion scope did not cancel the stale generator refresh")
	}
}

func waitForGeneratorSuggestion(t *testing.T, query string) []Suggestion {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		results := Lookup(query)
		if len(results) > 0 {
			return results
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func TestGeneratorCacheNeverExceedsItsHardLimit(t *testing.T) {
	resetGeneratorCache()
	t.Cleanup(resetGeneratorCache)

	generatorMu.Lock()
	for index := 0; index < generatorCacheLimit+20; index++ {
		generatorCache[fmt.Sprintf("entry-%d", index)] = &generatorCacheEntry{
			populated: true,
			updatedAt: time.Unix(int64(index+1), 0),
		}
	}
	pruneGeneratorCacheLocked("")
	size := len(generatorCache)
	generatorMu.Unlock()

	if size >= generatorCacheLimit {
		t.Fatalf("expected room below hard limit %d, got %d", generatorCacheLimit, size)
	}
}

func hasSuggestion(suggestions []Suggestion, command string) bool {
	for _, suggestion := range suggestions {
		if strings.TrimSpace(suggestion.Cmd) == command {
			return true
		}
	}
	return false
}
