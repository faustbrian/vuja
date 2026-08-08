package root

import (
	"strings"
	"sync"

	"github.com/faustbrian/vuja/spec"
)

type suggestionRequest struct {
	query string
	mode  string
	cwd   string
}

type suggestionCacheEntry struct {
	request suggestionRequest
	results []spec.Suggestion
}

type suggestionPipeline struct {
	mu         sync.Mutex
	generation uint64
	limit      int
	exact      map[suggestionRequest][]spec.Suggestion
	order      []suggestionRequest
	last       suggestionCacheEntry
}

func newSuggestionPipeline(limit int) *suggestionPipeline {
	if limit < 1 {
		limit = 128
	}
	return &suggestionPipeline{limit: limit, exact: make(map[suggestionRequest][]spec.Suggestion)}
}

func (p *suggestionPipeline) begin(request suggestionRequest) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
	return p.generation
}

func (p *suggestionPipeline) current(generation uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return generation == p.generation
}

func (p *suggestionPipeline) invalidate() {
	p.mu.Lock()
	p.generation++
	p.mu.Unlock()
}

func (p *suggestionPipeline) immediate(request suggestionRequest) ([]spec.Suggestion, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if results, ok := p.exact[request]; ok {
		return clonePipelineSuggestions(results), true
	}
	if request.mode != p.last.request.mode || request.cwd != p.last.request.cwd ||
		!strings.HasPrefix(strings.ToLower(request.query), strings.ToLower(p.last.request.query)) {
		return nil, false
	}
	query := strings.ToLower(strings.TrimSpace(request.query))
	filtered := make([]spec.Suggestion, 0, len(p.last.results))
	for _, result := range p.last.results {
		if suggestionMatchesQuery(result.Cmd, query) {
			filtered = append(filtered, result)
		}
	}
	return filtered, len(filtered) > 0
}

func suggestionMatchesQuery(command, query string) bool {
	if query == "" {
		return true
	}
	command = strings.ToLower(strings.TrimSpace(command))
	if strings.HasPrefix(command, query) {
		return true
	}
	queryFields := strings.Fields(query)
	commandFields := strings.Fields(command)
	if len(queryFields) == 0 || len(commandFields) == 0 || queryFields[0] != commandFields[0] {
		return false
	}
	return strings.Contains(command, query)
}

func (p *suggestionPipeline) commit(generation uint64, request suggestionRequest, results []spec.Suggestion) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return false
	}
	p.storeLocked(request, results)
	return true
}

func (p *suggestionPipeline) seed(generation uint64, request suggestionRequest, results []spec.Suggestion) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return false
	}
	p.storeLocked(request, results)
	return true
}

func (p *suggestionPipeline) storeLocked(request suggestionRequest, results []spec.Suggestion) {
	p.last = suggestionCacheEntry{request: request, results: clonePipelineSuggestions(results)}
	if _, exists := p.exact[request]; !exists {
		p.order = append(p.order, request)
	}
	p.exact[request] = clonePipelineSuggestions(results)
	for len(p.order) > p.limit {
		delete(p.exact, p.order[0])
		p.order = p.order[1:]
	}
}

func (p *suggestionPipeline) cached(request suggestionRequest) []spec.Suggestion {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clonePipelineSuggestions(p.exact[request])
}

func clonePipelineSuggestions(results []spec.Suggestion) []spec.Suggestion {
	return append([]spec.Suggestion(nil), results...)
}

func sameSuggestionResults(left, right []spec.Suggestion) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Cmd != right[index].Cmd || left[index].Source != right[index].Source ||
			left[index].Desc != right[index].Desc || left[index].Icon != right[index].Icon ||
			left[index].Live != right[index].Live ||
			left[index].Confidence != right[index].Confidence ||
			left[index].Priority != right[index].Priority {
			return false
		}
	}
	return true
}

func suggestionsVisibleForQuery(results []spec.Suggestion, query string) bool {
	return len(results) > 0 && !(len(results) == 1 && strings.TrimSpace(results[0].Cmd) == strings.TrimSpace(query) && !strings.HasSuffix(query, " "))
}

type suggestionUndo struct {
	original string
	accepted string
	active   bool
}

func (u *suggestionUndo) record(original, accepted string) {
	u.original, u.accepted, u.active = original, accepted, accepted != original
}

func (u *suggestionUndo) clear() {
	u.original, u.accepted, u.active = "", "", false
}

func (u *suggestionUndo) restore(current string) (string, bool) {
	if !u.active || current != u.accepted {
		u.clear()
		return "", false
	}
	original := u.original
	u.clear()
	return original, true
}
