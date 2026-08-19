package root

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/spec"
)

const (
	shellFunctionsBegin = "VUJA_FUNCTIONS_BEGIN"
	shellFunctionPrefix = "VUJA_FUNCTION:"
	shellFunctionsEnd   = "VUJA_FUNCTIONS_END"
	functionDescription = "# vuja:"
)

var functionDeclaration = regexp.MustCompile(`^(?:function[[:space:]]+)?([[:alnum:]_.:+-]+)(?:[[:space:]]*\(\))?[[:space:]]*(?:\{|--.*|$)`)

type shellFunction struct {
	Name        string
	Description string
	Source      string
}

type functionSourceCacheEntry struct {
	modTime      time.Time
	size         int64
	checkedAt    time.Time
	descriptions map[string]string
}

type shellFunctionInventory struct {
	mu           sync.RWMutex
	functions    []shellFunction
	pending      []shellFunction
	collecting   bool
	sourceCache  map[string]functionSourceCacheEntry
	statInterval time.Duration
}

var loadedShellFunctions = newShellFunctionInventory()

func newShellFunctionInventory() *shellFunctionInventory {
	return &shellFunctionInventory{
		sourceCache:  make(map[string]functionSourceCacheEntry),
		statInterval: 500 * time.Millisecond,
	}
}

func (i *shellFunctionInventory) Handle(message string) bool {
	if i == nil {
		return false
	}
	switch message {
	case shellFunctionsBegin:
		i.mu.Lock()
		i.pending = nil
		i.collecting = true
		i.mu.Unlock()
		return true
	case shellFunctionsEnd:
		i.mu.Lock()
		if i.collecting {
			i.functions = normalizeShellFunctions(i.pending)
		}
		i.pending = nil
		i.collecting = false
		i.mu.Unlock()
		return true
	}

	payload, ok := strings.CutPrefix(message, shellFunctionPrefix)
	if !ok {
		return false
	}
	name, source, ok := strings.Cut(payload, "\t")
	if !ok {
		return true
	}
	function, ok := dotfilesShellFunction(name, source)
	if !ok {
		return true
	}
	i.mu.Lock()
	if i.collecting {
		i.pending = append(i.pending, function)
	}
	i.mu.Unlock()
	return true
}

func (i *shellFunctionInventory) Replace(functions []shellFunction) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.functions = normalizeShellFunctions(functions)
	i.pending = nil
	i.collecting = false
	i.mu.Unlock()
}

func (i *shellFunctionInventory) Snapshot() []shellFunction {
	if i == nil {
		return nil
	}
	i.mu.RLock()
	functions := append([]shellFunction(nil), i.functions...)
	i.mu.RUnlock()

	for index := range functions {
		if description := i.description(functions[index].Source, functions[index].Name); description != "" {
			functions[index].Description = description
		}
	}
	return functions
}

func (i *shellFunctionInventory) description(source, name string) string {
	now := time.Now()
	i.mu.RLock()
	cached, cachedOK := i.sourceCache[source]
	i.mu.RUnlock()
	if cachedOK && now.Sub(cached.checkedAt) < i.statInterval {
		return cached.descriptions[name]
	}

	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return ""
	}

	if cachedOK && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		cached.checkedAt = now
		i.mu.Lock()
		i.sourceCache[source] = cached
		i.mu.Unlock()
		return cached.descriptions[name]
	}

	descriptions := readFunctionDescriptions(source)
	i.mu.Lock()
	i.sourceCache[source] = functionSourceCacheEntry{
		modTime: info.ModTime(), size: info.Size(), checkedAt: now, descriptions: descriptions,
	}
	i.mu.Unlock()
	return descriptions[name]
}

func normalizeShellFunctions(functions []shellFunction) []shellFunction {
	byName := make(map[string]shellFunction, len(functions))
	for _, function := range functions {
		function.Name = strings.TrimSpace(function.Name)
		function.Source = strings.TrimSpace(function.Source)
		if function.Name == "" || function.Source == "" {
			continue
		}
		byName[function.Name] = function
	}
	result := make([]shellFunction, 0, len(byName))
	for _, function := range byName {
		result = append(result, function)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result
}

func dotfilesShellFunction(name, source string) (shellFunction, bool) {
	name = strings.TrimSpace(name)
	source = shellFunctionSourcePath(strings.TrimSpace(source))
	if name == "" || source == "" || strings.ContainsAny(name, "\t\r\n") || strings.ContainsAny(source, "\t\r\n") {
		return shellFunction{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return shellFunction{}, false
	}
	dotfiles := filepath.Join(home, ".dotfiles")
	absoluteSource, err := filepath.Abs(source)
	if err != nil {
		return shellFunction{}, false
	}
	canonicalDotfiles, err := filepath.EvalSymlinks(dotfiles)
	if err != nil {
		return shellFunction{}, false
	}
	canonicalSource, err := filepath.EvalSymlinks(absoluteSource)
	if err != nil || !pathWithin(canonicalDotfiles, canonicalSource) {
		return shellFunction{}, false
	}
	return shellFunction{Name: name, Source: filepath.Clean(canonicalSource)}, true
}

func shellFunctionSourcePath(source string) string {
	if _, err := os.Stat(source); err == nil {
		return source
	}
	separator := strings.LastIndexByte(source, ':')
	if separator <= 0 || separator == len(source)-1 {
		return source
	}
	path, line := source[:separator], source[separator+1:]
	for _, character := range line {
		if character < '0' || character > '9' {
			return source
		}
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return source
}

func pathWithin(parent, child string) bool {
	parent, parentErr := filepath.Abs(parent)
	child, childErr := filepath.Abs(child)
	if parentErr != nil || childErr != nil {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readFunctionDescriptions(source string) map[string]string {
	file, err := os.Open(source)
	if err != nil {
		return nil
	}
	defer file.Close()

	descriptions := make(map[string]string)
	pending := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if value, ok := strings.CutPrefix(line, functionDescription); ok {
			pending = strings.TrimSpace(value)
			continue
		}
		if line == "" {
			continue
		}
		match := functionDeclaration.FindStringSubmatch(line)
		if len(match) == 2 && pending != "" {
			descriptions[match[1]] = pending
		}
		pending = ""
	}
	return descriptions
}

func shellFunctionSuggestions(query string, inventory *shellFunctionInventory) []spec.Suggestion {
	segment := spec.ActiveCommandSegment(query)
	active := segment.Query
	if active == "" || strings.TrimSpace(active) != active || len(spec.Tokenize(active)) != 1 {
		return nil
	}

	functions := inventory.Snapshot()
	results := make([]spec.Suggestion, 0, len(functions))
	for _, function := range functions {
		if !strings.HasPrefix(function.Name, active) || function.Name == active {
			continue
		}
		results = append(results, spec.Suggestion{
			Cmd: segment.Prefix + function.Name, Desc: function.Description,
			Source: "function", Confidence: 60,
		})
	}
	return results
}
