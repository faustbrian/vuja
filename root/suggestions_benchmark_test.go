package root

import (
	"fmt"
	"testing"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

func BenchmarkMergeResultsWarm(b *testing.B) {
	MergeResults("git st", "spec")
	b.ResetTimer()
	for range b.N {
		MergeResults("git st", "spec")
	}
}

func BenchmarkMergeResultsFastCached(b *testing.B) {
	mergeFastResultsProfiled("git st", "spec")
	b.ResetTimer()
	for range b.N {
		mergeFastResultsProfiled("git st", "spec")
	}
}

func BenchmarkSuggestionPipelineImmediateExtension(b *testing.B) {
	pipeline := newSuggestionPipeline(128)
	request := suggestionRequest{query: "g", mode: "spec", cwd: "/repo"}
	pipeline.commit(pipeline.begin(request), request, MergeResults("g", "spec"))
	extended := suggestionRequest{query: "gi", mode: "spec", cwd: "/repo"}
	b.ResetTimer()
	for range b.N {
		pipeline.immediate(extended)
	}
}

func BenchmarkSuggestionEnrichmentFixture(b *testing.B) {
	commands := make([]spec.Suggestion, 0, 200)
	history := make([]integration.HistResult, 0, 1_000)
	local := make([]scoring.FrecencyEntry, 0, 100)
	for index := range 200 {
		command := fmt.Sprintf("cd project-%03d/", index)
		commands = append(commands, spec.Suggestion{Cmd: command, Source: "filesystem", Priority: 50})
		if index < 100 {
			local = append(local, scoring.FrecencyEntry{Cmd: command, Count: 100 - index, RawScore: float64(100 - index)})
		}
	}
	for index := range 1_000 {
		history = append(history, integration.HistResult{ID: 1_000 - index, Cmd: fmt.Sprintf("git command-%04d", index)})
	}
	signals := scoring.SignalSet{Query: "cd", LocalFrecency: local}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		candidates := collectSuggestionCandidates("cd", commands, history)
		candidates = dedupeSuggestionCandidates("cd", candidates)
		if results := scoring.Score(candidates, signals); len(results) == 0 {
			b.Fatal("expected ranked fixture suggestions")
		}
	}
}
