package root

import "testing"

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
