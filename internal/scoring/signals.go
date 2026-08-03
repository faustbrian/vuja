package scoring

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/workspace"
)

type signalCacheKey struct {
	store        *FrecencyStore
	cwd          string
	previous     string
	prevSkeleton string
}

type signalCacheEntry struct {
	key       signalCacheKey
	query     string
	signals   SignalSet
	updatedAt time.Time
}

var (
	signalCacheMu sync.Mutex
	signalCache   signalCacheEntry
)

type SignalSet struct {
	Workspace              workspace.WorkspaceInfo
	LocalFrecency          []FrecencyEntry
	ProjectFrecency        []FrecencyEntry
	GlobalFrecency         []FrecencyEntry
	TransitionEntries      []TransitionEntry
	TransitionIsLocal      bool
	ExactTransitionEntries []ExactTransitionEntry
	ExactTransitionIsLocal bool
	Feedback               []FeedbackEntry
	Outcomes               []OutcomeEntry
	Query                  string
	RootCommand            string
	Cwd                    string
}

// CollectSignals gathers environment, workspace, and historical frecency/transition signals for the given query and directory
func CollectSignals(ctx context.Context, cwd, query, rootCmd string, frecency *FrecencyStore, prevCommand, prevCmdSkeleton string) SignalSet {
	query = strings.TrimSpace(query)
	rootCmd = strings.TrimSpace(rootCmd)
	key := signalCacheKey{frecency, cwd, prevCommand, prevCmdSkeleton}
	if frecency != nil {
		signalCacheMu.Lock()
		cached := signalCache
		signalCacheMu.Unlock()
		if cached.key == key && time.Since(cached.updatedAt) < 2*time.Second && strings.HasPrefix(strings.ToLower(query), strings.ToLower(cached.query)) {
			return filterSignals(cached.signals, query, rootCmd)
		}
	}

	ws := workspace.DetectCached(cwd)

	if ctx == nil {
		ctx = context.Background()
	}

	var local, project, global []FrecencyEntry
	var trans []TransitionEntry
	var transIsLocal bool
	var exactTrans []ExactTransitionEntry
	var exactTransIsLocal bool
	var feedback []FeedbackEntry
	var outcomes []OutcomeEntry

	if frecency != nil {
		local, _ = frecency.QueryLocal(ctx, cwd, query, 50)
		if ws.Root != "" {
			project, _ = frecency.QueryProject(ctx, ws.Root, query, 50)
		}
		global, _ = frecency.QueryGlobal(ctx, query, 50)
		if prevCmdSkeleton != "" {
			trans, transIsLocal = frecency.QueryTransitionsWithFallback(ctx, prevCmdSkeleton, cwd)
		}
		if prevCommand != "" {
			exactTrans, exactTransIsLocal = frecency.QueryExactTransitionsWithFallback(ctx, prevCommand, cwd)
		}
		feedback, _ = frecency.QueryFeedback(ctx, cwd, ws.Root, query, 50)
		outcomes, _ = frecency.QueryOutcomes(ctx, cwd, ws.Root, query, 50)
	}

	result := SignalSet{
		Workspace:              ws,
		LocalFrecency:          local,
		ProjectFrecency:        project,
		GlobalFrecency:         global,
		TransitionEntries:      trans,
		TransitionIsLocal:      transIsLocal,
		ExactTransitionEntries: exactTrans,
		ExactTransitionIsLocal: exactTransIsLocal,
		Feedback:               feedback,
		Outcomes:               outcomes,
		Query:                  query,
		RootCommand:            rootCmd,
		Cwd:                    cwd,
	}
	if frecency != nil {
		signalCacheMu.Lock()
		signalCache = signalCacheEntry{key: key, query: query, signals: result, updatedAt: time.Now()}
		signalCacheMu.Unlock()
	}
	return result
}

func filterSignals(signals SignalSet, query, rootCmd string) SignalSet {
	originalQuery := strings.TrimSpace(query)
	query = strings.ToLower(originalQuery)
	matches := func(command string) bool {
		return query == "" || strings.HasPrefix(strings.ToLower(command), query)
	}
	filterFrecency := func(entries []FrecencyEntry) []FrecencyEntry {
		filtered := make([]FrecencyEntry, 0, len(entries))
		for _, entry := range entries {
			if matches(entry.Cmd) {
				filtered = append(filtered, entry)
			}
		}
		return filtered
	}
	filtered := signals
	filtered.Query = originalQuery
	filtered.RootCommand = strings.TrimSpace(rootCmd)
	filtered.LocalFrecency = filterFrecency(signals.LocalFrecency)
	filtered.ProjectFrecency = filterFrecency(signals.ProjectFrecency)
	filtered.GlobalFrecency = filterFrecency(signals.GlobalFrecency)
	filtered.Feedback = nil
	for _, entry := range signals.Feedback {
		if matches(entry.Cmd) {
			filtered.Feedback = append(filtered.Feedback, entry)
		}
	}
	filtered.Outcomes = nil
	for _, entry := range signals.Outcomes {
		if matches(entry.Cmd) {
			filtered.Outcomes = append(filtered.Outcomes, entry)
		}
	}
	return filtered
}

func InvalidateSignalCache() {
	signalCacheMu.Lock()
	signalCache = signalCacheEntry{}
	signalCacheMu.Unlock()
}
