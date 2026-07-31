package root

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
)

type projectCommandCacheEntry struct {
	key      string
	commands []spec.Suggestion
}

var projectCommandCache struct {
	sync.Mutex
	entries map[string]projectCommandCacheEntry
}

func discoverProjectCommands(query string, ws workspace.WorkspaceInfo) []spec.Suggestion {
	if ws.Root == "" {
		return nil
	}
	key := projectMetadataKey(ws.Root)
	projectCommandCache.Lock()
	if projectCommandCache.entries == nil {
		projectCommandCache.entries = make(map[string]projectCommandCacheEntry)
	}
	entry, ok := projectCommandCache.entries[ws.Root]
	if !ok || entry.key != key {
		entry = projectCommandCacheEntry{key: key, commands: readProjectCommands(ws)}
		projectCommandCache.entries[ws.Root] = entry
	}
	projectCommandCache.Unlock()

	query = strings.ToLower(strings.TrimSpace(query))
	var results []spec.Suggestion
	for _, suggestion := range entry.commands {
		if query == "" || strings.HasPrefix(strings.ToLower(suggestion.Cmd), query) {
			results = append(results, suggestion)
		}
	}
	return results
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

func readProjectCommands(ws workspace.WorkspaceInfo) []spec.Suggestion {
	seen := make(map[string]bool)
	var results []spec.Suggestion
	add := func(command, description string) {
		if command == "" || seen[command] {
			return
		}
		seen[command] = true
		results = append(results, spec.Suggestion{Cmd: command, Desc: description, Source: "project", Priority: 75})
	}

	readJSONScripts(filepath.Join(ws.Root, "package.json"), func(name string) { add("npm run "+name, "package script") })
	readJSONScripts(filepath.Join(ws.Root, "composer.json"), func(name string) { add("composer "+name, "composer script") })
	readRecipeFile(filepath.Join(ws.Root, "justfile"), func(name string) { add("just "+name, "just recipe") })
	readRecipeFile(filepath.Join(ws.Root, "Justfile"), func(name string) { add("just "+name, "just recipe") })
	readMakefile(filepath.Join(ws.Root, "Makefile"), func(name string) { add("make "+name, "make target") })
	if ws.HasGoProject {
		add("go test ./...", "Go project")
		add("go vet ./...", "Go project")
		add("go mod tidy", "Go project")
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Cmd < results[j].Cmd })
	return results
}

func readJSONScripts(path string, add func(string)) {
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

func readRecipeFile(path string, add func(string)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "_") {
			continue
		}
		if before, ok := strings.CutSuffix(strings.Fields(line)[0], ":"); ok {
			add(before)
		}
	}
}

func readMakefile(path string, add func(string)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
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
