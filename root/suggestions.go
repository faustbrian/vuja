package root

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/ai"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

// MergeResults collects and dedupes suggestions for a query and mode
func MergeResults(query string, mode string) []spec.Suggestion {
	maxSugg := config.Get().UI.MaxSuggestions
	normalizedQuery := strings.TrimSpace(query)

	// always call lookup to scan aliases and get spec suggestions
	logger.Debugf("Merge Calling Lookup for '%s'", query)
	cmdResults := spec.Lookup(query)
	histResults, _ := integration.SearchHistory(query, spec.GetAliasesCopy())
	deduped := collectSuggestionCandidates(query, cmdResults, histResults)
	if mode == "history" {
		for i := range deduped {
			if deduped[i].Source == "history" {
				deduped[i].Confidence = min(deduped[i].Confidence+20, 100)
			}
		}
	}

	seen := make(map[string]bool, len(deduped))
	for _, suggestion := range deduped {
		seen[strings.TrimSpace(suggestion.Cmd)] = true
	}
	injectAISuggestion(&deduped, seen, normalizedQuery)

	cwd := spec.GetCWD()
	tokens := spec.Tokenize(query)
	rootCmd := ""
	if len(tokens) > 0 {
		rootCmd = tokens[0]
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	store, _ := scoring.GetFrecencyStore()
	prevCommand, prevSkeleton := getPrevCommandSignals()
	signals := scoring.CollectSignals(ctxTimeout, cwd, query, rootCmd, store, prevCommand, prevSkeleton)
	scored := scoring.Score(deduped, signals)

	finalResults := make([]spec.Suggestion, 0, len(scored))
	for _, sc := range scored {
		finalResults = append(finalResults, sc.Suggestion)
	}

	if len(finalResults) > maxSugg {
		return finalResults[:maxSugg]
	}
	return finalResults
}

func collectSuggestionCandidates(query string, commandResults []spec.Suggestion, historyResults []integration.HistResult) []spec.Suggestion {
	normalizedQuery := strings.TrimSpace(query)
	seen := make(map[string]bool)
	candidates := make([]spec.Suggestion, 0, len(commandResults)+len(historyResults))

	add := func(suggestion spec.Suggestion) {
		suggestion.Cmd = strings.TrimSpace(suggestion.Cmd)
		if suggestion.Cmd == "" || suggestion.Cmd == normalizedQuery || seen[suggestion.Cmd] {
			return
		}
		if suggestion.Source == "" {
			suggestion.Source = "spec"
		}
		if suggestion.Confidence == 0 {
			suggestion.Confidence = 50
		}
		seen[suggestion.Cmd] = true
		candidates = append(candidates, suggestion)
	}

	for _, result := range commandResults {
		add(result)
	}
	for i, result := range historyResults {
		add(spec.Suggestion{
			Cmd:        result.Cmd,
			Desc:       "history",
			Icon:       "history",
			Source:     "history",
			Confidence: max(55-(i*2), 40),
		})
	}
	return candidates
}

func importPersistentHistory() {
	if _, err := integration.SearchHistory("", nil); err != nil {
		return
	}
	stats := integration.HistorySnapshot()
	entries := make([]scoring.ImportedHistoryEntry, 0, len(stats))
	for _, stat := range stats {
		entries = append(entries, scoring.ImportedHistoryEntry{
			Command:  stat.Command,
			Count:    stat.Count,
			LastUsed: stat.LastUsed,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if store, err := scoring.GetFrecencyStore(); err == nil {
		_ = store.ReplaceImportedHistory(ctx, entries)
	}
}

func injectAISuggestion(deduped *[]spec.Suggestion, seen map[string]bool, normalizedQuery string) {
	if aiSugg := GetCurrentAISuggestion(); aiSugg != nil {
		normalizedCmd := strings.TrimSpace(aiSugg.Cmd)
		if normalizedCmd != "" && normalizedCmd != normalizedQuery && strings.HasPrefix(strings.ToLower(normalizedCmd), strings.ToLower(normalizedQuery)) {
			if !seen[aiSugg.Cmd] {
				seen[aiSugg.Cmd] = true
				*deduped = append(*deduped, *aiSugg)
			} else {
				for i, item := range *deduped {
					if item.Cmd == aiSugg.Cmd && aiSugg.Confidence > item.Confidence {
						(*deduped)[i].Confidence = aiSugg.Confidence
						if (*deduped)[i].Source == "" || (*deduped)[i].Source == "spec" || (*deduped)[i].Source == "history" {
							(*deduped)[i].Source = "ai"
						}
						break
					}
				}
			}
		}
	}
}

var (
	aiEngine     *ai.AIEngine
	aiEngineOnce sync.Once
)

func GetAIEngine() *ai.AIEngine {
	aiEngineOnce.Do(func() {
		aiEngine = ai.NewAIEngine(nil)
		for _, p := range ai.DefaultProviders {
			aiEngine.RegisterProvider(p)
		}
	})
	return aiEngine
}

var (
	currentAISugg *spec.Suggestion
	aiSuggMu      sync.RWMutex
)

func SetCurrentAISuggestion(sugg *spec.Suggestion) {
	aiSuggMu.Lock()
	defer aiSuggMu.Unlock()
	currentAISugg = sugg
}

func GetCurrentAISuggestion() *spec.Suggestion {
	aiSuggMu.RLock()
	defer aiSuggMu.RUnlock()
	return currentAISugg
}
