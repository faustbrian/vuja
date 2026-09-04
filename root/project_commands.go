package root

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/cachemetrics"
	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
)

type projectCommandCacheEntry struct {
	key      string
	commands []spec.Suggestion
	lastUsed time.Time
}

const projectCommandCacheLimit = 128

var projectCommandCache struct {
	sync.Mutex
	entries map[string]projectCommandCacheEntry
}

var projectCommandCacheMetrics = cachemetrics.New("project-commands")

func discoverProjectCommands(ctx context.Context, query string, ws workspace.WorkspaceInfo) []spec.Suggestion {
	if ws.Root == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	key := projectMetadataKey(ws.Root)
	projectCommandCache.Lock()
	if projectCommandCache.entries == nil {
		projectCommandCache.entries = make(map[string]projectCommandCacheEntry)
	}
	entry, ok := projectCommandCache.entries[ws.Root]
	if ok && entry.key == key {
		projectCommandCacheMetrics.Hit()
		entry.lastUsed = time.Now()
		projectCommandCache.entries[ws.Root] = entry
		projectCommandCache.Unlock()
		return filterProjectCommands(query, entry.commands)
	}
	projectCommandCacheMetrics.Miss()
	projectCommandCache.Unlock()

	commands := readProjectCommands(ctx, ws)
	projectCommandCache.Lock()
	entry = projectCommandCacheEntry{key: key, commands: commands, lastUsed: time.Now()}
	projectCommandCache.entries[ws.Root] = entry
	pruneProjectCommandCacheLocked(ws.Root)
	projectCommandCache.Unlock()
	return filterProjectCommands(query, entry.commands)
}

func filterProjectCommands(query string, commands []spec.Suggestion) []spec.Suggestion {
	query = strings.ToLower(strings.TrimSpace(query))
	var results []spec.Suggestion
	for _, suggestion := range commands {
		if query == "" || strings.HasPrefix(strings.ToLower(suggestion.Cmd), query) {
			results = append(results, suggestion)
		}
	}
	return results
}

func pruneProjectCommandCacheLocked(currentRoot string) {
	for len(projectCommandCache.entries) > projectCommandCacheLimit {
		oldestRoot := ""
		var oldest time.Time
		for root, entry := range projectCommandCache.entries {
			if root == currentRoot {
				continue
			}
			if oldestRoot == "" || entry.lastUsed.Before(oldest) {
				oldestRoot, oldest = root, entry.lastUsed
			}
		}
		if oldestRoot == "" {
			return
		}
		delete(projectCommandCache.entries, oldestRoot)
		projectCommandCacheMetrics.Eviction()
	}
}

func projectMetadataKey(root string) string {
	var key strings.Builder
	for _, name := range []string{"package.json", "composer.json", "justfile", "Justfile", "Makefile", "go.mod"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil {
			key.WriteString(name)
			key.WriteString(info.ModTime().String())
			key.WriteString(strconv.FormatInt(info.Size(), 10))
		}
	}
	return key.String()
}

func readProjectCommands(ctx context.Context, ws workspace.WorkspaceInfo) []spec.Suggestion {
	seen := make(map[string]bool)
	var results []spec.Suggestion
	add := func(command, description string) {
		if command == "" || seen[command] {
			return
		}
		seen[command] = true
		results = append(results, spec.Suggestion{Cmd: command, Desc: description, Source: "project", Priority: 75})
	}

	readJSONScripts(ctx, filepath.Join(ws.Root, "package.json"), func(name string) { add("npm run "+name, "package script") })
	readJSONScripts(ctx, filepath.Join(ws.Root, "composer.json"), func(name string) { add("composer "+name, "composer script") })
	readRecipeFile(ctx, filepath.Join(ws.Root, "justfile"), func(name string) { add("just "+name, "just recipe") })
	readRecipeFile(ctx, filepath.Join(ws.Root, "Justfile"), func(name string) { add("just "+name, "just recipe") })
	readMakefile(ctx, filepath.Join(ws.Root, "Makefile"), func(name string) { add("make "+name, "make target") })
	if ws.HasGoProject {
		add("go test ./...", "Go project")
		add("go vet ./...", "Go project")
		add("go mod tidy", "Go project")
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Cmd < results[j].Cmd })
	return results
}

func readJSONScripts(ctx context.Context, path string, add func(string)) {
	if ctx.Err() != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var document struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &document) != nil {
		return
	}
	for name := range document.Scripts {
		add(name)
	}
}

func readRecipeFile(ctx context.Context, path string, add func(string)) {
	if ctx.Err() != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if ctx.Err() != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "_") {
			continue
		}
		if before, ok := strings.CutSuffix(strings.Fields(line)[0], ":"); ok {
			add(before)
		}
	}
}

func readMakefile(ctx context.Context, path string, add func(string)) {
	if ctx.Err() != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if ctx.Err() != nil {
			return
		}
		if line == "" || line[0] == '\t' || strings.HasPrefix(line, ".") {
			continue
		}
		target, _, ok := strings.Cut(line, ":")
		target = strings.TrimSpace(target)
		if ok && target != "" && !strings.ContainsAny(target, " =$%") {
			add(target)
		}
	}
}
