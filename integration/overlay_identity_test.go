package integration

import (
	"strings"
	"testing"

	"github.com/faustbrian/vuja/spec"
)

// QA scenario 5: an asynchronous refresh must not transfer execution authority
// to a replacement row, even if its label or provider ID resembles the selection.
func TestOverlayRefreshDoesNotSubstituteAnExecutionIdentity(t *testing.T) {
	for _, mode := range []string{"suggestions", "history", "rich-history"} {
		for _, change := range []string{"removed-same-command", "same-id-changed-command", "duplicate-id", "ambiguous-command-fallback"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				o := NewOverlay(false)
				selected := spec.Suggestion{ID: "chosen", Cmd: "deploy production"}
				if change == "ambiguous-command-fallback" {
					selected.ID = ""
				}
				refreshIdentityFixture(o, mode, []spec.Suggestion{selected})
				o.FocusFirst()
				var replacements []spec.Suggestion
				switch change {
				case "removed-same-command":
					replacements = []spec.Suggestion{{ID: "replacement", Cmd: selected.Cmd}}
				case "same-id-changed-command":
					replacements = []spec.Suggestion{{ID: selected.ID, Cmd: "deploy staging"}}
				case "duplicate-id":
					replacements = []spec.Suggestion{selected, selected}
				case "ambiguous-command-fallback":
					replacements = []spec.Suggestion{{ID: "one", Cmd: selected.Cmd}, {ID: "two", Cmd: selected.Cmd}}
				}
				refreshIdentityFixture(o, mode, replacements)
				if got := o.GetCurrentCmd(); got != "" {
					t.Fatalf("refresh substituted execution target %q instead of clearing selection", got)
				}
				if o.GetUserNavigated() {
					t.Fatal("removed selection still grants navigated execution authority")
				}
				// A later enrichment must not silently reselect the replacement.
				refreshIdentityFixture(o, mode, replacements)
				if got := o.GetCurrentCmd(); got != "" {
					t.Fatalf("later enrichment silently selected %q", got)
				}
				if rendered := o.Render(); !strings.Contains(rendered, "Run typed") || strings.Contains(rendered, "Run selected") {
					t.Fatalf("removed selection has misleading execution footer: %q", rendered)
				}
				if got := o.FocusFirst(); got == "" {
					t.Fatal("explicit navigation can no longer select an available replacement")
				}
			})
		}
	}
}

func TestOverlayHistoryRefreshPreservesInitialSelectionWithoutNavigation(t *testing.T) {
	for _, mode := range []string{"history", "rich-history"} {
		t.Run(mode, func(t *testing.T) {
			o := NewOverlay(false)
			first := spec.Suggestion{ID: "first", Cmd: "deploy production"}
			second := spec.Suggestion{ID: "second", Cmd: "deploy staging"}
			refreshIdentityFixture(o, mode, []spec.Suggestion{first, second})
			if got := o.GetCurrentCmd(); got != first.Cmd {
				t.Fatalf("history search did not select its initial result: %q", got)
			}
			refreshIdentityFixture(o, mode, []spec.Suggestion{second, first})
			if got := o.GetCurrentCmd(); got != first.Cmd || o.Items[o.Cursor].ID != first.ID {
				t.Fatalf("refresh transferred the initial history selection: %q", got)
			}
		})
	}
}

func TestOverlayRefreshKeepsAUniqueLegacyCommandFallback(t *testing.T) {
	for _, mode := range []string{"suggestions", "history", "rich-history"} {
		t.Run(mode, func(t *testing.T) {
			o := NewOverlay(false)
			refreshIdentityFixture(o, mode, []spec.Suggestion{{Cmd: "deploy production"}})
			o.FocusFirst()
			refreshIdentityFixture(o, mode, []spec.Suggestion{{Cmd: "deploy staging"}, {ID: "enriched", Cmd: "deploy production"}})
			if got := o.GetCurrentCmd(); got != "deploy production" || o.Cursor != 1 {
				t.Fatalf("unique legacy command lost selection during enrichment: %q", got)
			}
		})
	}
}

func TestOverlayRefreshFollowsIdentityAcrossDuplicateCommandReordering(t *testing.T) {
	for _, mode := range []string{"suggestions", "history", "rich-history"} {
		t.Run(mode, func(t *testing.T) {
			o := NewOverlay(false)
			chosen := spec.Suggestion{ID: "chosen", Cmd: "deploy production"}
			other := spec.Suggestion{ID: "other", Cmd: chosen.Cmd}
			refreshIdentityFixture(o, mode, []spec.Suggestion{chosen, other})
			o.FocusFirst()
			refreshIdentityFixture(o, mode, []spec.Suggestion{other, chosen})
			if got := o.GetCurrentCmd(); got != chosen.Cmd || o.Cursor != 1 || !o.GetUserNavigated() {
				t.Fatalf("reordering transferred explicit selection: command=%q cursor=%d", got, o.Cursor)
			}
		})
	}
}

func refreshIdentityFixture(o *Overlay, mode string, items []spec.Suggestion) {
	switch mode {
	case "suggestions":
		o.RefreshQueryAndItems("deploy", items)
	case "history":
		o.SetHistorySearch("deploy", "global", items)
	case "rich-history":
		results := make([]RichHistoryResult, len(items))
		for i, item := range items {
			results[i].ID, results[i].Command = item.ID, item.Cmd
		}
		o.SetRichHistorySearch("deploy", "global", results)
	}
}
