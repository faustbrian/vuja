package root

import (
	"testing"

	"github.com/faustbrian/vuja/spec"
)

func TestSuggestionPipelineReusesAndNarrowsTheLastResultImmediately(t *testing.T) {
	pipeline := newSuggestionPipeline(8)
	request := suggestionRequest{query: "g", mode: "spec", cwd: "/repo"}
	generation := pipeline.begin(request)
	pipeline.commit(generation, request, []spec.Suggestion{
		{Cmd: "git status"},
		{Cmd: "go test ./..."},
		{Cmd: "docker ps"},
	})

	got, hit := pipeline.immediate(suggestionRequest{query: "gi", mode: "spec", cwd: "/repo"})
	if !hit || len(got) != 1 || got[0].Cmd != "git status" {
		t.Fatalf("expected an immediate narrowed cache hit, hit=%v results=%v", hit, got)
	}
}

func TestSuggestionPipelineRejectsStaleCompletions(t *testing.T) {
	pipeline := newSuggestionPipeline(8)
	first := pipeline.begin(suggestionRequest{query: "g", mode: "spec", cwd: "/repo"})
	second := pipeline.begin(suggestionRequest{query: "gi", mode: "spec", cwd: "/repo"})

	if pipeline.commit(first, suggestionRequest{query: "g", mode: "spec", cwd: "/repo"}, []spec.Suggestion{{Cmd: "go test"}}) {
		t.Fatal("expected the older generation to be rejected")
	}
	if !pipeline.commit(second, suggestionRequest{query: "gi", mode: "spec", cwd: "/repo"}, []spec.Suggestion{{Cmd: "git status"}}) {
		t.Fatal("expected the latest generation to commit")
	}
}

func TestSuggestionPipelineSeedsImmediateResultsBeforeEnrichmentCompletes(t *testing.T) {
	pipeline := newSuggestionPipeline(8)
	request := suggestionRequest{query: "git", mode: "spec", cwd: "/repo"}
	generation := pipeline.begin(request)
	if !pipeline.seed(generation, request, []spec.Suggestion{{Cmd: "git status", Source: "history"}}) {
		t.Fatal("expected the current fast result to seed the pipeline")
	}

	got, hit := pipeline.immediate(suggestionRequest{query: "git s", mode: "spec", cwd: "/repo"})
	if !hit || len(got) != 1 || got[0].Cmd != "git status" {
		t.Fatalf("expected the next keystroke to reuse fast results, hit=%v results=%v", hit, got)
	}
}

func TestStabilizeSuggestionOrderKeepsAVisibleTopResultWithoutClearImprovement(t *testing.T) {
	previous := []spec.Suggestion{{Cmd: "git status", Priority: 50}, {Cmd: "git switch", Priority: 49}}
	next := []spec.Suggestion{{Cmd: "git switch", Priority: 52}, {Cmd: "git status", Priority: 50}}

	got := stabilizeSuggestionOrder(previous, next)
	if got[0].Cmd != "git status" {
		t.Fatalf("expected ranking hysteresis to retain the prior top result, got %v", got)
	}
}

func TestSuggestionUndoOnlyRestoresAnUneditedAcceptance(t *testing.T) {
	var undo suggestionUndo
	undo.record("git st", "git status")
	if restored, ok := undo.restore("git status"); !ok || restored != "git st" {
		t.Fatalf("expected accepted text to restore, ok=%v restored=%q", ok, restored)
	}

	undo.record("git st", "git status")
	if _, ok := undo.restore("git status --short"); ok {
		t.Fatal("expected an edited acceptance not to be restored")
	}
}

func TestSameSuggestionResultsDetectsVisibleChanges(t *testing.T) {
	left := []spec.Suggestion{{Cmd: "git status", Source: "history", Desc: "history"}}
	if !sameSuggestionResults(left, append([]spec.Suggestion(nil), left...)) {
		t.Fatal("expected identical visible results to compare equal")
	}
	right := []spec.Suggestion{{Cmd: "git status", Source: "spec", Desc: "show status"}}
	if sameSuggestionResults(left, right) {
		t.Fatal("expected changed source metadata to require a redraw")
	}
}
