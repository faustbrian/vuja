package spec

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faustbrian/vuja/internal/cachemetrics"
)

type cobraCacheEntry struct {
	suggestions []Suggestion
	expires     time.Time
	lastUsed    time.Time
	refreshing  bool
}

const (
	cobraCacheLimit       = 256
	cobraCacheTTL         = 30 * time.Second
	cobraNegativeCacheTTL = 2 * time.Second
)

var (
	cobraCache           = map[string]cobraCacheEntry{}
	cobraCacheMu         sync.Mutex
	cobraCacheGeneration atomic.Uint64
	cobraCacheMetrics    = cachemetrics.New("cobra-completion")
	runCobraCompletion   = func(ctx context.Context, binName string, args []string) ([]byte, error) {
		return exec.CommandContext(ctx, binName, args...).Output()
	}
)

func resolveCobraBinary(binName string) (string, string, bool) {
	path, err := exec.LookPath(binName)
	if err != nil {
		return binName, binName, true
	}
	info, err := os.Stat(path)
	if err != nil {
		return path, path, true
	}
	key := path + "|" + info.ModTime().String() + "|" + strconv.FormatInt(info.Size(), 10)
	return path, key, true
}

// parseCobraOutput parses output from `<cmd> __complete <args>`.
// each line is "value\tdesc", last line is ":N" (ShellCompDirective bitmask).
// returns nil if output is not Cobra-style.
func parseCobraOutput(raw string, prefix string) []Suggestion {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}
	lastLine := lines[len(lines)-1]
	if !strings.HasPrefix(lastLine, ":") {
		return nil
	}
	directive, err := strconv.Atoi(lastLine[1:])
	if err != nil {
		return nil
	}
	// ShellCompDirectiveError = 1
	if directive&1 != 0 {
		return nil
	}

	candidates := lines[:len(lines)-1]
	results := make([]Suggestion, 0, len(candidates))
	for _, line := range candidates {
		if line == "" {
			continue
		}
		value, desc, _ := strings.Cut(line, "\t")
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cmd := value
		if prefix != "" {
			cmd = prefix + " " + value
		}
		results = append(results, Suggestion{
			Cmd:        cmd,
			Desc:       desc,
			Source:     "spec-inferred",
			Confidence: 50,
			Priority:   30,
		})
		if len(results) == 1000 {
			break
		}
	}
	return results
}

func buildCobraCacheKey(binKey string, args []string, _ string) string {
	var sb strings.Builder
	sb.WriteString(binKey)
	sb.WriteByte('\x00')
	sb.WriteString(GetCWD())
	for _, arg := range args {
		sb.WriteByte('\x00')
		sb.WriteString(arg)
	}
	return sb.String()
}

// QueryCobraComplete calls `binName __complete <args> <partial>` and returns
// structured suggestions cached per binary mtime, args, and partial.
// returns nil if the binary is not Cobra-based or times out.
func QueryCobraComplete(binName string, args []string, partial string) []Suggestion {
	return QueryCobraCompleteContext(context.Background(), binName, args, partial)
}

// QueryCobraCompleteContext is the cancellable dynamic-completion boundary.
func QueryCobraCompleteContext(ctx context.Context, binName string, args []string, partial string) []Suggestion {
	if strings.ContainsAny(binName, `/\`) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}

	resolvedBinary, binKey, found := resolveCobraBinary(binName)
	if !found {
		return nil
	}
	argKey := buildCobraCacheKey(binKey, args, partial)

	cobraCacheMu.Lock()
	if entry, ok := cobraCache[argKey]; ok {
		if time.Now().Before(entry.expires) {
			cobraCacheMetrics.Hit()
			entry.lastUsed = time.Now()
			cobraCache[argKey] = entry
			cobraCacheMu.Unlock()
			return filterByPartial(entry.suggestions, partial)
		}
		if entry.refreshing {
			cobraCacheMetrics.Hit()
			stale := cloneSuggestions(entry.suggestions)
			cobraCacheMu.Unlock()
			return filterByPartial(stale, partial)
		}
		delete(cobraCache, argKey)
		cobraCacheMetrics.Eviction()
	}
	cobraCacheMetrics.Miss()
	now := time.Now()
	pruneCobraCacheLocked()
	if len(cobraCache) >= cobraCacheLimit {
		cobraCacheMu.Unlock()
		return nil
	}
	cobraCache[argKey] = cobraCacheEntry{expires: now.Add(cobraNegativeCacheTTL), lastUsed: now, refreshing: true}
	generation := cobraCacheGeneration.Load()
	cobraCacheMu.Unlock()

	argsCopy := append([]string(nil), args...)
	go refreshCobraCompletion(ctx, generation, argKey, resolvedBinary, binName, argsCopy)
	return nil
}

func refreshCobraCompletion(ctx context.Context, generation uint64, key, binaryPath, binName string, args []string) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	cmdArgs := append([]string{"__complete"}, args...)
	cmdArgs = append(cmdArgs, "")
	prefixParts := append([]string{binName}, args...)
	out, err := runCobraCompletion(ctx, binaryPath, cmdArgs)
	var suggestions []Suggestion
	if err == nil {
		suggestions = parseCobraOutput(string(out), strings.Join(prefixParts, " "))
	}
	cobraCacheMu.Lock()
	defer cobraCacheMu.Unlock()
	if generation != cobraCacheGeneration.Load() {
		return
	}
	if ctx.Err() != nil && err != nil {
		delete(cobraCache, key)
		return
	}
	ttl := cobraCacheTTL
	if len(suggestions) == 0 {
		ttl = cobraNegativeCacheTTL
	}
	now := time.Now()
	cobraCache[key] = cobraCacheEntry{suggestions: cloneSuggestions(suggestions), expires: now.Add(ttl), lastUsed: now}
	notifyCompletionUpdate()
}

func pruneCobraCacheLocked() {
	now := time.Now()
	for key, entry := range cobraCache {
		if !entry.refreshing && !now.Before(entry.expires) {
			delete(cobraCache, key)
			cobraCacheMetrics.Eviction()
		}
	}
	for len(cobraCache) >= cobraCacheLimit {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range cobraCache {
			if entry.refreshing {
				continue
			}
			if oldestKey == "" || entry.lastUsed.Before(oldest) {
				oldestKey, oldest = key, entry.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		delete(cobraCache, oldestKey)
		cobraCacheMetrics.Eviction()
	}
}

func filterByPartial(suggestions []Suggestion, partial string) []Suggestion {
	if partial == "" {
		return suggestions
	}
	filtered := make([]Suggestion, 0, len(suggestions))
	for _, s := range suggestions {
		lastWord := s.Cmd
		if idx := strings.LastIndex(s.Cmd, " "); idx >= 0 {
			lastWord = s.Cmd[idx+1:]
		}
		if HasPrefix(lastWord, partial) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// ResetCobraCache clears the completion cache — use in tests only
func ResetCobraCache() {
	cobraCacheMu.Lock()
	cobraCacheGeneration.Add(1)
	cobraCache = map[string]cobraCacheEntry{}
	cobraCacheMu.Unlock()
}
