package root

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/scoring"
)

func TestLearnedArgumentSuggestionsCompleteCurrentSlot(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "service")
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Record(context.Background(), "git switch feature/payments", cwd, 0); err != nil {
		t.Fatal(err)
	}

	suggestions := learnedArgumentSuggestions(context.Background(), "git switch fe", cwd, root, store)
	if len(suggestions) != 1 || suggestions[0].Cmd != "git switch feature/payments" {
		t.Fatalf("unexpected learned suggestions: %+v", suggestions)
	}
}
