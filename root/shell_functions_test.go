package root

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/spec"
)

func TestShellFunctionInventoryAcceptsOnlyLoadedDotfilesFunctions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dotfiles := filepath.Join(home, ".dotfiles")
	if err := os.MkdirAll(filepath.Join(dotfiles, "zsh"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dotfiles, "zsh", "functions.zsh")
	if err := os.WriteFile(source, []byte("# vuja: Push the current repository\nrepush() { :; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inventory := newShellFunctionInventory()
	for _, message := range []string{
		"VUJA_FUNCTIONS_BEGIN",
		"VUJA_FUNCTION:repush\t" + source + ":17",
		"VUJA_FUNCTION:foreign\t/usr/local/share/plugin.zsh",
		"VUJA_FUNCTIONS_END",
	} {
		if !inventory.Handle(message) {
			t.Fatalf("expected function inventory to consume %q", message)
		}
	}

	functions := inventory.Snapshot()
	if len(functions) != 1 {
		t.Fatalf("expected only the dotfiles function, got %+v", functions)
	}
	if functions[0].Name != "repush" || functions[0].Description != "Push the current repository" {
		t.Fatalf("expected annotated repush function, got %+v", functions[0])
	}
}

func TestShellFunctionDescriptionsRefreshWhenSourceChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dotfiles := filepath.Join(home, ".dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dotfiles, "functions.zsh")
	if err := os.WriteFile(source, []byte("# vuja: First description\nrepush() { :; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inventory := newShellFunctionInventory()
	inventory.statInterval = 0
	inventory.Handle("VUJA_FUNCTIONS_BEGIN")
	inventory.Handle("VUJA_FUNCTION:repush\t" + source)
	inventory.Handle("VUJA_FUNCTIONS_END")
	if got := inventory.Snapshot()[0].Description; got != "First description" {
		t.Fatalf("expected first description, got %q", got)
	}

	if err := os.WriteFile(source, []byte("# vuja: Updated function description\nrepush() { :; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inventory.Snapshot()[0].Description; got != "Updated function description" {
		t.Fatalf("expected changed source to refresh the annotation cache, got %q", got)
	}
}

func TestShellFunctionSuggestionsAppearOnlyInCommandPosition(t *testing.T) {
	inventory := newShellFunctionInventory()
	inventory.Replace([]shellFunction{{Name: "repush", Description: "Push again", Source: "/home/brian/.dotfiles/zsh/functions.zsh"}})

	assertFunction := func(query string, want bool) {
		t.Helper()
		results := shellFunctionSuggestions(query, inventory)
		if !want && len(results) != 0 {
			t.Fatalf("query %q: expected no function suggestions, got %+v", query, results)
		}
		wantCommand := "repush"
		if strings.Contains(query, "&&") {
			wantCommand = "echo ok && repush"
		}
		if want && (len(results) != 1 || results[0] != (spec.Suggestion{
			Cmd: wantCommand, Desc: "Push again", Source: "function", Confidence: 60,
		})) {
			t.Fatalf("query %q: expected the function suggestion, got %+v", query, results)
		}
	}

	assertFunction("rep", true)
	assertFunction("git rep", false)
	assertFunction("echo ok && rep", true)
	assertFunction("rep ", false)
}
