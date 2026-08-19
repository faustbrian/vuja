package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/faustbrian/vuja/commands"
	"github.com/faustbrian/vuja/spec"
)

// setupGitRepo creates a real git repo in a temp dir with:
// - local branches: main (HEAD), dev, feature/login, stable
// - remote branches: origin/main, origin/dev (written directly to .git/refs)
// - a tag: v1.0
// - a stash entry
func setupGitRepo(t *testing.T) (tmp string, cleanup func()) {
	t.Helper()

	tmp = t.TempDir()
	ctx := context.Background()

	run := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Logf("git cmd %v: %s", args, out)
		}
	}

	run("git", "-C", tmp, "init", "--initial-branch=main")

	// use fallback for older git that doesn't support --initial-branch
	if _, err := os.Stat(filepath.Join(tmp, ".git", "refs", "heads", "main")); err != nil {
		run("git", "-C", tmp, "init")
	}

	run("git", "-C", tmp, "config", "user.email", "vuja-test@example.com") // this is for ci/cd
	run("git", "-C", tmp, "config", "user.name", "Vuja Test")              // this is for ci/cd

	// initial commit so branches can be created
	if err := os.WriteFile(filepath.Join(tmp, "file.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "-C", tmp, "add", ".")
	run("git", "-C", tmp, "commit", "-m", "initial")

	// local branches (incl. slash branch to test tokenization)
	run("git", "-C", tmp, "branch", "dev")
	run("git", "-C", tmp, "branch", "stable")
	run("git", "-C", tmp, "branch", "feature/login")

	// tag
	run("git", "-C", tmp, "tag", "v1.0")

	// add a real remote in config
	run("git", "-C", tmp, "remote", "add", "origin", "https://github.com/faustbrian/vuja.git")

	// write fake remote refs directly (no need for actual remote server)
	for _, ref := range []string{"main", "dev"} {
		dir := filepath.Join(tmp, ".git", "refs", "remotes", "origin")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		// point them to the same commit as HEAD for simplicity
		headBytes, err := os.ReadFile(filepath.Join(tmp, ".git", "refs", "heads", "main"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ref), headBytes, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// stash entry
	if err := os.WriteFile(filepath.Join(tmp, "dirty.go"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "-C", tmp, "add", ".")
	run("git", "-C", tmp, "stash")

	// chdir into repo so generators can run git commands
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	cleanup = func() { _ = os.Chdir(oldWd) }
	return tmp, cleanup
}

func lookupUntil(
	t *testing.T,
	input string,
	ready func([]spec.Suggestion) bool,
) []spec.Suggestion {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		results := spec.Lookup(input)
		if ready(results) {
			return results
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for suggestions for %q", input)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func containsSuggestion(fragment string) func([]spec.Suggestion) bool {
	return func(suggestions []spec.Suggestion) bool {
		for _, suggestion := range suggestions {
			if strings.Contains(suggestion.Cmd, fragment) {
				return true
			}
		}
		return false
	}
}

func TestGitSuggestions(t *testing.T) {
	tmp, cleanup := setupGitRepo(t)
	defer cleanup()

	t.Run("git top-level", func(t *testing.T) {
		res := spec.Lookup("git ")
		if len(res) < 10 {
			t.Errorf("expected many git subcommands, got %d", len(res))
		}
	})

	t.Run("tag -d shows tags", func(t *testing.T) {
		lookupUntil(t, "git tag -d ", containsSuggestion("v1.0"))
	})

	t.Run("push HEAD options", func(t *testing.T) {
		res := spec.Lookup("git push origin HEAD --")
		found := false
		for _, r := range res {
			if strings.Contains(r.Cmd, "--force") {
				found = true
			}
		}
		if !found {
			t.Error("git push origin HEAD -- should suggest --force")
		}
	})

	t.Run("push -u origin suggests branches", func(t *testing.T) {
		lookupUntil(t, "git push -u origin ", containsSuggestion("dev"))
	})

	t.Run("push origin suggests active branch", func(t *testing.T) {
		ctx := context.Background()
		out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Skip("can't determine HEAD branch")
		}
		activeBranch := strings.TrimSpace(string(out))
		res := lookupUntil(t, "git push origin ", func(suggestions []spec.Suggestion) bool {
			for _, suggestion := range suggestions {
				parts := strings.Fields(suggestion.Cmd)
				if len(parts) > 0 && parts[len(parts)-1] == activeBranch {
					return true
				}
			}
			return false
		})
		if len(res) > 0 {
			parts := strings.Fields(res[0].Cmd)
			if len(parts) == 0 || parts[len(parts)-1] != activeBranch {
				t.Errorf("expected active branch '%s' to be first suggestion, got: %s", activeBranch, res[0].Cmd)
			}
		}
	})

	t.Run("push origin no duplicate branches", func(t *testing.T) {
		res := spec.Lookup("git push origin ")
		seen := make(map[string]int)
		for _, r := range res {
			parts := strings.Fields(r.Cmd)
			if len(parts) == 0 {
				continue
			}
			branch := parts[len(parts)-1]
			seen[branch]++
			if seen[branch] > 1 {
				t.Errorf("duplicate branch suggestion: %s", branch)
			}
		}
	})

	t.Run("branch with slash is suggested correctly", func(t *testing.T) {
		lookupUntil(t, "git checkout ", containsSuggestion("feature/login"))
	})

	t.Run("remote branches suggested for push", func(t *testing.T) {
		res := lookupUntil(t, "git push origin ", containsSuggestion("dev"))
		var cmdStr strings.Builder
		for _, r := range res {
			cmdStr.WriteString(r.Cmd)
			cmdStr.WriteByte(' ')
		}
		// should have at least dev or main from branch list
		if !strings.Contains(cmdStr.String(), "dev") && !strings.Contains(cmdStr.String(), "main") {
			t.Errorf("git push origin should suggest local branches, got: %s", cmdStr.String())
		}
	})

	t.Run("active branch not suggested for checkout", func(t *testing.T) {
		// find actual active branch
		ctx := context.Background()
		out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Skip("can't determine HEAD branch")
		}
		activeBranch := strings.TrimSpace(string(out))

		res := lookupUntil(t, "git checkout ", containsSuggestion("dev"))
		for _, r := range res {
			// the suggestion should not contain the active branch as a standalone word
			parts := strings.FieldsSeq(r.Cmd)
			for p := range parts {
				if p == activeBranch {
					t.Errorf("git checkout should not suggest active branch '%s', got: %s", activeBranch, r.Cmd)
				}
			}
		}
	})

	t.Run("checkout -b no suggest", func(t *testing.T) {
		res := spec.Lookup("git checkout -b ")
		for _, r := range res {
			if strings.Contains(r.Cmd, "dev") {
				t.Error("git checkout -b should not suggest existing branches")
			}
		}
	})

	t.Run("switch -c no suggest", func(t *testing.T) {
		res := spec.Lookup("git switch -c ")
		for _, r := range res {
			if strings.Contains(r.Cmd, "dev") {
				t.Error("git switch -c should not suggest existing branches")
			}
		}
	})

	t.Run("stash variants suggest entries", func(t *testing.T) {
		for _, cmd := range []string{"apply", "drop", "pop"} {
			lookupUntil(t, "git stash "+cmd+" ", containsSuggestion("stash@{0}"))
		}
	})

	t.Run("remote subcommands suggest remotes", func(t *testing.T) {
		for _, cmd := range []string{"remove", "rename", "set-url"} {
			lookupUntil(t, "git remote "+cmd+" ", containsSuggestion("origin"))
		}
	})

	t.Run("not a git repo no crash", func(t *testing.T) {
		emptyDir := t.TempDir()
		_ = os.Chdir(emptyDir)
		defer func() { _ = os.Chdir(tmp) }()
		_ = spec.Lookup("git status ")
	})

	t.Run("reset options", func(t *testing.T) {
		_ = spec.Lookup("git reset --soft origin/main ")
		lookupUntil(t, "git reset HEAD ", containsSuggestion("file.go"))
	})

	t.Run("show suggests tags and commits", func(t *testing.T) {
		lookupUntil(t, "git show ", func(res []spec.Suggestion) bool {
			foundTag := false
			foundCommit := false
			for _, r := range res {
				if strings.Contains(r.Cmd, "v1.0") {
					foundTag = true
				}
				// commit hashes are 7+ hex chars
				parts := strings.Fields(r.Cmd)
				if len(parts) > 0 {
					h := parts[len(parts)-1]
					if len(h) >= 7 {
						foundCommit = true
					}
				}
			}
			return foundTag && foundCommit
		})
	})

	t.Run("cherry-pick suggests commits", func(t *testing.T) {
		res := lookupUntil(t, "git cherry-pick ", containsCommitSuggestion)
		// all suggestions should be short hex hashes
		for _, r := range res {
			parts := strings.Fields(r.Cmd)
			if len(parts) == 0 {
				continue
			}
			h := parts[len(parts)-1]
			if len(h) < 7 {
				t.Errorf("cherry-pick suggestion looks invalid: %s", r.Cmd)
			}
		}
	})

	t.Run("revert suggests commits", func(t *testing.T) {
		lookupUntil(t, "git revert ", containsCommitSuggestion)
	})

	t.Run("global flags don't break subcommand detection", func(t *testing.T) {
		ctx := context.Background()
		out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Skip("can't determine HEAD branch")
		}
		activeBranch := strings.TrimSpace(string(out))

		res := spec.Lookup("git -c core.pager=cat checkout ")
		for _, r := range res {
			parts := strings.FieldsSeq(r.Cmd)
			for p := range parts {
				if p == activeBranch {
					t.Errorf("git -c core.pager=cat checkout should not suggest active branch '%s', got: %s", activeBranch, r.Cmd)
				}
			}
		}
	})
}

func containsCommitSuggestion(suggestions []spec.Suggestion) bool {
	for _, suggestion := range suggestions {
		parts := strings.Fields(suggestion.Cmd)
		if len(parts) > 0 && len(parts[len(parts)-1]) >= 7 {
			return true
		}
	}
	return false
}
