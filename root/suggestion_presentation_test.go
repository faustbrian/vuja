package root

import (
	"testing"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/faustbrian/vuja/v2/internal/policy"
	"github.com/faustbrian/vuja/v2/spec"
)

func TestSuggestionPresentationCannotRestoreCommittedResultsAfterBlock(t *testing.T) {
	before := config.Get()
	config.Init(config.DefaultConfig())
	t.Cleanup(func() { config.Init(before) })
	request := suggestionRequest{query: "git", mode: "spec", cwd: "/example"}
	pipeline := newSuggestionPipeline(4)
	generation := pipeline.begin(request)
	pipeline.commit(generation, request, []spec.Suggestion{{ID: "status", Cmd: "git status"}})
	captured := pipeline.cached(request)
	o := integration.NewOverlay(false)
	o.SetQueryAndItems(request.query, captured)
	o.FocusFirst()
	presenter := newTerminalUIPresenter(func(render func() []byte) { _ = render() })
	ready, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		close(ready)
		<-release
		presentSuggestionGeneration(presenter, pipeline, generation, func(compose func(func() []byte)) {
			compose(func() []byte {
				o.RefreshQueryAndItems(request.query, captured)
				return []byte(o.Render())
			})
		})
	}()
	<-ready
	pipeline.publishPreferences(config.SuggestionsConfig{ExactBlocks: []string{"git status"}})
	presenter.Update(func(_ func(func() []byte)) { o.RefreshPreferences(policy.Blocked) })
	close(release)
	<-finished
	if len(o.Items) != 0 || o.GetCurrentCmd() != "" || o.GetUserNavigated() {
		t.Fatalf("old committed presentation restored a blocked row or selection: %+v", o.Items)
	}
	current := pipeline.begin(request)
	presentSuggestionGeneration(presenter, pipeline, current, func(_ func(func() []byte)) {
		o.SetQueryAndItems(request.query, []spec.Suggestion{{ID: "diff", Cmd: "git diff"}})
	})
	if len(o.Items) != 1 || o.Items[0].Cmd != "git diff" {
		t.Fatal("current-generation presentation was incorrectly rejected")
	}
}
