package root

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/scoring"
)

func TestFailureRecoveryCorrectsMissingExecutable(t *testing.T) {
	cwd := t.TempDir()
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Record(context.Background(), "gti status", cwd, 127); err != nil {
		t.Fatal(err)
	}

	suggestions := failureRecoverySuggestions(
		context.Background(),
		"",
		cwd,
		store,
		func(input string, _ int) []string {
			if input != "gti" {
				t.Fatalf("expected failed executable gti, got %q", input)
			}
			return []string{"git"}
		},
	)
	if len(suggestions) == 0 || suggestions[0].Cmd != "git status" {
		t.Fatalf("expected corrected executable, got %+v", suggestions)
	}
}

func TestFailureRecoverySurfacesSuccessfulVariant(t *testing.T) {
	cwd := t.TempDir()
	store, err := scoring.NewFrecencyStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	_ = store.Record(ctx, "go test ./...", cwd, 0)
	_ = store.Record(ctx, "go test ./service", cwd, 1)

	suggestions := failureRecoverySuggestions(ctx, "", cwd, store, nil)
	found := false
	for _, suggestion := range suggestions {
		found = found || suggestion.Cmd == "go test ./..."
	}
	if !found {
		t.Fatalf("expected successful variant, got %+v", suggestions)
	}
}
