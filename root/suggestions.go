package root

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/ai"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
)

const (
	suggestionCoreProviderBudget  = 150 * time.Millisecond
	suggestionLocalProviderBudget = 60 * time.Millisecond
	suggestionSignalBudget        = 100 * time.Millisecond
)

// MergeResults collects and dedupes suggestions for a query and mode
func MergeResults(query string, mode string) []spec.Suggestion {
	results, _ := mergeResultsProfiled(query, mode)
	return results
}

func mergeResultsProfiled(query string, mode string) ([]spec.Suggestion, map[string]time.Duration) {
	return mergeResultsProfiledContext(context.Background(), query, mode)
}

func mergeResultsProfiledContext(ctx context.Context, query string, mode string) ([]spec.Suggestion, map[string]time.Duration) {
	scored, timings := scoreResultsProfiledContext(ctx, query, mode)
	maxSugg := config.Get().UI.MaxSuggestions

	finalResults := make([]spec.Suggestion, 0, min(len(scored), maxSugg))
	for _, result := range scored {
		finalResults = append(finalResults, result.Suggestion)
		if len(finalResults) == maxSugg {
			break
		}
	}
	return finalResults, timings
}

func mergeFastResultsProfiled(query string, mode string) ([]spec.Suggestion, map[string]time.Duration) {
	timings := make(map[string]time.Duration)
	measure := func(name string, operation func()) {
		started := time.Now()
		operation()
		timings[name] += time.Since(started)
	}
	segment := spec.ActiveCommandSegment(query)
	activeQuery := segment.Query
	var commandResults []spec.Suggestion
	measure("fast-spec", func() { commandResults = spec.LookupFast(activeQuery) })
	var historyResults []integration.HistResult
	measure("fast-history", func() {
		historyResults, _ = integration.SearchCachedHistory(activeQuery, spec.GetAliasesCopy())
	})
	candidates := collectSuggestionCandidates(activeQuery, commandResults, historyResults)
	candidates = dedupeSuggestionCandidates(activeQuery, candidates)
	candidates = applySuggestionPolicy(activeQuery, candidates)
	if segment.Prefix != "" {
		for index := range candidates {
			candidates[index].Cmd = segment.Prefix + candidates[index].Cmd
		}
	}
	if mode == "history" {
		for index := range candidates {
			if candidates[index].Source == "history" {
				candidates[index].Confidence = min(candidates[index].Confidence+20, 100)
			}
		}
	}
	var scored []scoring.ScoredSuggestion
	measure("fast-rank", func() {
		scored = scoring.ScoreWithConfig(candidates, scoring.SignalSet{Query: strings.TrimSpace(query), Cwd: spec.GetCWD()}, suggestionScoreConfig())
	})
	limit := config.Get().UI.MaxSuggestions
	results := make([]spec.Suggestion, 0, min(len(scored), limit))
	for _, result := range scored {
		results = append(results, result.Suggestion)
		if len(results) == limit {
			break
		}
	}
	return results, timings
}

func scoreResults(query string, mode string) []scoring.ScoredSuggestion {
	results, _ := scoreResultsProfiled(query, mode)
	return results
}

func scoreResultsProfiled(query string, mode string) ([]scoring.ScoredSuggestion, map[string]time.Duration) {
	return scoreResultsProfiledContext(context.Background(), query, mode)
}

func scoreResultsProfiledContext(ctx context.Context, query string, mode string) ([]scoring.ScoredSuggestion, map[string]time.Duration) {
	if ctx == nil {
		ctx = context.Background()
	}
	timings := make(map[string]time.Duration)
	measure := func(name string, operation func()) {
		started := time.Now()
		operation()
		timings[name] += time.Since(started)
	}
	segment := spec.ActiveCommandSegment(query)
	activeQuery := segment.Query
	normalizedQuery := strings.TrimSpace(activeQuery)
	cwd := spec.GetCWD()

	// Refresh aliases once, then run immutable provider reads in parallel.
	logger.Debugf("Merge Calling Lookup for '%s'", query)
	aliases := spec.RefreshShellAliases()
	type coreProviderResult struct {
		name        string
		suggestions []spec.Suggestion
		history     []integration.HistResult
		workspace   workspace.WorkspaceInfo
		duration    time.Duration
	}
	coreContext, cancelCore := context.WithTimeout(ctx, suggestionCoreProviderBudget)
	// Keep the provider deadline alive after the synchronous providers return.
	// Dynamic completions use this same context to warm their bounded async
	// cache; cancelling it immediately here prevents every cold lookup from
	// completing. The deadline still bounds stale work, while a newer request
	// cancels the parent context.
	defer cancelCore()
	coreResults := make(chan coreProviderResult, 3)
	go func() {
		started := time.Now()
		results := spec.LookupContextPrepared(coreContext, activeQuery)
		coreResults <- coreProviderResult{name: "spec", suggestions: results, duration: time.Since(started)}
	}()
	go func() {
		started := time.Now()
		results, _ := integration.SearchHistoryContext(coreContext, activeQuery, aliases)
		coreResults <- coreProviderResult{name: "history", history: results, duration: time.Since(started)}
	}()
	go func() {
		started := time.Now()
		result := workspace.DetectCached(cwd)
		coreResults <- coreProviderResult{name: "workspace", workspace: result, duration: time.Since(started)}
	}()
	var cmdResults []spec.Suggestion
	var histResults []integration.HistResult
	var ws workspace.WorkspaceInfo
	coreTimedOut := false
	for remaining := 3; remaining > 0; remaining-- {
		select {
		case result := <-coreResults:
			timings[result.name] = result.duration
			switch result.name {
			case "spec":
				cmdResults = result.suggestions
			case "history":
				histResults = result.history
			case "workspace":
				ws = result.workspace
			}
		case <-coreContext.Done():
			coreTimedOut = ctx.Err() == nil
			remaining = 1
		}
	}
	if coreTimedOut {
		timings["core-timeout"] = suggestionCoreProviderBudget
	}
	if ctx.Err() != nil {
		return nil, timings
	}
	deduped := collectSuggestionCandidates(activeQuery, cmdResults, histResults)
	var store *scoring.FrecencyStore
	if !persistentHistoryImporting.Load() {
		store, _ = scoring.GetFrecencyStore()
	}
	type localProviderResult struct {
		name        string
		suggestions []spec.Suggestion
		duration    time.Duration
		arguments   time.Duration
		recovery    time.Duration
	}
	localContext, cancelLocal := context.WithTimeout(ctx, suggestionLocalProviderBudget)
	localResults := make(chan localProviderResult, 4)
	go func() {
		started := time.Now()
		localResults <- localProviderResult{name: "git", suggestions: gitStateSuggestions(activeQuery, ws), duration: time.Since(started)}
	}()
	go func() {
		started := time.Now()
		localResults <- localProviderResult{name: "project", suggestions: discoverProjectCommands(localContext, activeQuery, ws), duration: time.Since(started)}
	}()
	go func() {
		started := time.Now()
		localResults <- localProviderResult{name: "directory", suggestions: indexedDirectorySuggestions(localContext, activeQuery, cwd), duration: time.Since(started)}
	}()
	go func() {
		ctxArguments, cancelArguments := context.WithTimeout(localContext, 20*time.Millisecond)
		started := time.Now()
		argumentResults := learnedArgumentSuggestions(ctxArguments, activeQuery, cwd, ws.Root, store)
		argumentsDuration := time.Since(started)
		cancelArguments()
		ctxRecovery, cancelRecovery := context.WithTimeout(localContext, 20*time.Millisecond)
		started = time.Now()
		recoveryResults := failureRecoverySuggestions(ctxRecovery, activeQuery, cwd, store, spec.NearestExternalCommands)
		recoveryDuration := time.Since(started)
		cancelRecovery()
		localResults <- localProviderResult{
			name: "learned", suggestions: append(argumentResults, recoveryResults...),
			duration: argumentsDuration + recoveryDuration, arguments: argumentsDuration, recovery: recoveryDuration,
		}
	}()
	localStarted := time.Now()
	var gitResults, projectResults, directoryResults, learnedResults []spec.Suggestion
	localTimedOut := false
	for remaining := 4; remaining > 0; remaining-- {
		select {
		case result := <-localResults:
			timings[result.name] = result.duration
			switch result.name {
			case "git":
				gitResults = result.suggestions
			case "project":
				projectResults = result.suggestions
			case "directory":
				directoryResults = result.suggestions
			case "learned":
				learnedResults = result.suggestions
				timings["arguments"] = result.arguments
				timings["recovery"] = result.recovery
			}
		case <-localContext.Done():
			localTimedOut = ctx.Err() == nil
			remaining = 1
		}
	}
	cancelLocal()
	timings["local-sources"] = time.Since(localStarted)
	if localTimedOut {
		timings["local-timeout"] = suggestionLocalProviderBudget
	}
	deduped = append(deduped, gitResults...)
	deduped = append(deduped, projectResults...)
	deduped = append(deduped, directoryResults...)
	deduped = append(deduped, learnedResults...)
	deduped = dedupeSuggestionCandidates(activeQuery, deduped)
	deduped = applySuggestionPolicy(activeQuery, deduped)
	if ctx.Err() != nil {
		return nil, timings
	}
	if segment.Prefix != "" {
		for i := range deduped {
			deduped[i].Cmd = segment.Prefix + deduped[i].Cmd
		}
		normalizedQuery = strings.TrimSpace(segment.Prefix + normalizedQuery)
	}
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

	tokens := spec.Tokenize(activeQuery)
	rootCmd := ""
	if len(tokens) > 0 {
		rootCmd = tokens[0]
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, suggestionSignalBudget)
	defer cancel()
	prevCommand, prevSkeleton := getPrevCommandSignals()
	var signals scoring.SignalSet
	measure("signals", func() {
		signals = scoring.CollectSignals(ctxTimeout, cwd, normalizedQuery, rootCmd, store, prevCommand, prevSkeleton)
	})
	var results []scoring.ScoredSuggestion
	measure("rank", func() {
		results = promoteAISuggestion(scoring.ScoreWithConfig(deduped, signals, suggestionScoreConfig()))
	})
	return results, timings
}

func promoteAISuggestion(scored []scoring.ScoredSuggestion) []scoring.ScoredSuggestion {
	for index := range scored {
		if scored[index].Source != "ai" {
			continue
		}
		suggestion := scored[index]
		copy(scored[1:index+1], scored[:index])
		scored[0] = suggestion
		break
	}
	return scored
}

func dedupeSuggestionCandidates(query string, suggestions []spec.Suggestion) []spec.Suggestion {
	query = strings.TrimSpace(query)
	positions := make(map[string]int, len(suggestions))
	deduped := make([]spec.Suggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		suggestion.Cmd = strings.TrimSpace(suggestion.Cmd)
		if suggestion.Cmd == "" || suggestion.Cmd == query {
			continue
		}
		key := canonicalDirectorySuggestion(suggestion.Cmd)
		if position, ok := positions[key]; ok {
			existing := &deduped[position]
			if existing.Source == "filesystem" && suggestion.Source == "directory-index" {
				// The filesystem candidate proves the path currently exists, while the
				// directory index carries the recency/frequency rank. Preserve the live
				// command spelling and presentation but merge the ranked evidence.
				existing.Priority = max(existing.Priority, suggestion.Priority)
				existing.Confidence = max(existing.Confidence, suggestion.Confidence)
				existing.Desc = suggestion.Desc
				existing.Source = suggestion.Source
				existing.Live = true
				continue
			}
			if suggestion.Cmd == existing.Cmd && suggestion.Priority > existing.Priority {
				deduped[position] = suggestion
				continue
			}
			if suggestion.Priority > existing.Priority {
				existing.Priority = suggestion.Priority
			}
			if suggestion.Confidence > existing.Confidence {
				existing.Confidence = suggestion.Confidence
			}
			continue
		}
		positions[key] = len(deduped)
		deduped = append(deduped, suggestion)
	}
	return deduped
}

func suggestionScoreConfig() scoring.ScoreConfig {
	cfg := scoring.DefaultScoreConfig
	cfg.DirectoryRanking = config.Get().Suggestions.DirectoryRanking
	return cfg
}

func canonicalDirectorySuggestion(command string) string {
	fields := strings.Fields(command)
	if len(fields) < 2 || (fields[0] != "cd" && fields[0] != "z") {
		return command
	}
	if command == "cd /" || command == "z /" {
		return command
	}
	return strings.TrimSuffix(command, "/")
}

func applySuggestionPolicy(query string, suggestions []spec.Suggestion) []spec.Suggestion {
	cfg := config.Get().Suggestions
	seen := make(map[string]bool, len(suggestions))
	filtered := make([]spec.Suggestion, 0, len(suggestions)+len(cfg.Pins))
	for _, suggestion := range suggestions {
		command := strings.TrimSpace(suggestion.Cmd)
		if command == "" || policy.IsSensitive(command) || policy.Blocked(command) {
			continue
		}
		if cfg.SuppressDestructive && policy.IsDestructive(command) && !policy.IsDestructive(query) {
			continue
		}
		seen[command] = true
		filtered = append(filtered, suggestion)
	}
	for _, command := range cfg.Pins {
		command = strings.TrimSpace(command)
		if command == "" || seen[command] || policy.IsSensitive(command) || policy.Blocked(command) {
			continue
		}
		if query == "" || strings.HasPrefix(strings.ToLower(command), strings.ToLower(query)) {
			filtered = append(filtered, spec.Suggestion{Cmd: command, Desc: "pinned", Source: "pin", Priority: 100})
		}
	}
	return filtered
}

func gitStateSuggestions(query string, ws workspace.WorkspaceInfo) []spec.Suggestion {
	if !ws.HasGit || (query != "" && !strings.HasPrefix("git ", strings.ToLower(query)) && !strings.HasPrefix(strings.ToLower(query), "git ")) {
		return nil
	}
	ws = workspace.WithGitStatusCached(ws)
	var commands []string
	switch {
	case ws.GitConflicted:
		commands = []string{"git status", "git diff --name-only --diff-filter=U"}
	case ws.GitRebasing:
		commands = []string{"git rebase --continue", "git rebase --abort"}
	case ws.GitMerging:
		commands = []string{"git merge --continue", "git merge --abort"}
	case ws.GitCherryPicking:
		commands = []string{"git cherry-pick --continue", "git cherry-pick --abort"}
	case ws.GitReverting:
		commands = []string{"git revert --continue", "git revert --abort"}
	default:
		if ws.GitDirty {
			commands = append(commands, "git diff", "git add -A")
		}
		if ws.GitStaged {
			commands = append(commands, "git diff --cached", "git commit")
		}
		if ws.GitAhead > 0 {
			commands = append(commands, "git push")
		}
		if ws.GitBehind > 0 {
			commands = append(commands, "git pull --ff-only")
		}
		if len(commands) == 0 {
			return nil
		}
	}
	results := make([]spec.Suggestion, 0, len(commands))
	for _, command := range commands {
		if query == "" || strings.HasPrefix(command, query) {
			results = append(results, spec.Suggestion{Cmd: command, Desc: "active git operation", Source: "workspace", Priority: 95})
		}
	}
	return results
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
	persistentHistoryImporting.Store(true)
	defer func() {
		persistentHistoryImporting.Store(false)
		scoring.InvalidateSignalCache()
		spec.NotifyCompletionUpdate()
	}()
	if _, err := integration.SearchHistory("", nil); err != nil {
		return
	}
	stats := integration.HistorySnapshot()
	entries := make([]scoring.ImportedHistoryEntry, 0, len(stats))
	for _, stat := range stats {
		if policy.IsSensitive(stat.Command) {
			continue
		}
		entries = append(entries, scoring.ImportedHistoryEntry{
			Command:     stat.Command,
			Cwd:         stat.Cwd,
			Count:       stat.Count,
			LastUsed:    stat.LastUsed,
			ExitCode:    stat.ExitCode,
			HasExitCode: stat.HasExitCode,
			Duration:    stat.Duration,
			Source:      stat.Source,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if store, err := scoring.GetFrecencyStore(); err == nil {
		_ = store.ReplaceImportedHistory(ctx, entries)
		persistentExecutions := integration.PersistentHistoryEntriesSnapshot()
		historyEvents := make([]scoring.HistoryEvent, 0, len(persistentExecutions))
		for _, execution := range persistentExecutions {
			if policy.IsSensitive(execution.Command) {
				continue
			}
			historyEvents = append(historyEvents, scoring.HistoryEvent{
				EventKey:    execution.ID,
				Command:     execution.Command,
				Cwd:         execution.Cwd,
				StartedAt:   execution.StartedAt,
				Duration:    execution.Duration,
				ExitCode:    execution.ExitCode,
				HasExitCode: execution.HasExitCode,
				Source:      execution.Source,
				Host:        execution.Host,
				SessionID:   execution.SessionID,
				Imported:    true,
			})
		}
		_ = store.ReplaceImportedHistoryEvents(ctx, historyEvents)
		if storedEvents, queryErr := store.QueryHistoryEvents(ctx, 10_000); queryErr == nil {
			richEntries := make([]integration.HistoryEntry, 0, len(storedEvents))
			for _, event := range storedEvents {
				richEntries = append(richEntries, integration.HistoryEntry{
					ID:          event.EventKey,
					Command:     event.Command,
					Cwd:         event.Cwd,
					StartedAt:   event.StartedAt,
					Duration:    event.Duration,
					ExitCode:    event.ExitCode,
					HasExitCode: event.HasExitCode,
					Source:      event.Source,
					Host:        event.Host,
					SessionID:   event.SessionID,
				})
			}
			integration.ReplaceRichHistoryEntries(richEntries)
		}
		historyDirectories := historyNavigationDirectoryImports(persistentExecutions, time.Now())
		_ = store.ReplaceDirectorySource(ctx, "history", historyDirectories)
		zoxideCtx, zoxideCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		zoxideDirectories := loadZoxideDirectories(zoxideCtx)
		zoxideCancel()
		_ = store.ReplaceDirectorySource(ctx, "zoxide", zoxideDirectories)
		_ = store.ReplaceDirectorySource(
			ctx,
			"git-worktrees",
			gitWorktreeImports(spec.GetCWD(), historyDirectories),
		)
	}
}

var persistentHistoryImporting atomic.Bool

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
