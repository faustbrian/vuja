package scoring

import (
	"context"
	"strings"

	"github.com/faustbrian/vuja/internal/workspace"
)

type SignalSet struct {
	Workspace              workspace.WorkspaceInfo
	LocalFrecency          []FrecencyEntry
	ProjectFrecency        []FrecencyEntry
	GlobalFrecency         []FrecencyEntry
	TransitionEntries      []TransitionEntry
	TransitionIsLocal      bool
	ExactTransitionEntries []ExactTransitionEntry
	ExactTransitionIsLocal bool
	Feedback               []FeedbackEntry
	Outcomes               []OutcomeEntry
	Query                  string
	RootCommand            string
	Cwd                    string
}

// CollectSignals gathers environment, workspace, and historical frecency/transition signals for the given query and directory
func CollectSignals(ctx context.Context, cwd, query, rootCmd string, frecency *FrecencyStore, prevCommand, prevCmdSkeleton string) SignalSet {
	ws := workspace.DetectCached(cwd)

	if ctx == nil {
		ctx = context.Background()
	}

	var local, project, global []FrecencyEntry
	var trans []TransitionEntry
	var transIsLocal bool
	var exactTrans []ExactTransitionEntry
	var exactTransIsLocal bool
	var feedback []FeedbackEntry
	var outcomes []OutcomeEntry

	if frecency != nil {
		local, _ = frecency.QueryLocal(ctx, cwd, query, 50)
		if ws.Root != "" {
			project, _ = frecency.QueryProject(ctx, ws.Root, query, 50)
		}
		global, _ = frecency.QueryGlobal(ctx, query, 50)
		if prevCmdSkeleton != "" {
			trans, transIsLocal = frecency.QueryTransitionsWithFallback(ctx, prevCmdSkeleton, cwd)
		}
		if prevCommand != "" {
			exactTrans, exactTransIsLocal = frecency.QueryExactTransitionsWithFallback(ctx, prevCommand, cwd)
		}
		feedback, _ = frecency.QueryFeedback(ctx, cwd, ws.Root, query, 50)
		outcomes, _ = frecency.QueryOutcomes(ctx, cwd, ws.Root, query, 50)
	}

	return SignalSet{
		Workspace:              ws,
		LocalFrecency:          local,
		ProjectFrecency:        project,
		GlobalFrecency:         global,
		TransitionEntries:      trans,
		TransitionIsLocal:      transIsLocal,
		ExactTransitionEntries: exactTrans,
		ExactTransitionIsLocal: exactTransIsLocal,
		Feedback:               feedback,
		Outcomes:               outcomes,
		Query:                  strings.TrimSpace(query),
		RootCommand:            strings.TrimSpace(rootCmd),
		Cwd:                    cwd,
	}
}
