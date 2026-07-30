package root

import "testing"

func TestHistoryNavigationDoesNotMutateShellUntilAccepted(t *testing.T) {
	var navigation historyNavigation
	navigation.Begin("git st")
	navigation.Select("git status")

	if replacement, ok := navigation.PendingReplacement(); ok || replacement != "" {
		t.Fatalf("navigation changed shell buffer before acceptance: %q", replacement)
	}

	replacement, ok := navigation.Accept()
	if !ok || replacement != "git status" {
		t.Fatalf("expected accepted selection, got %q, %v", replacement, ok)
	}
}

func TestHistoryNavigationCancelRestoresOriginalBuffer(t *testing.T) {
	var navigation historyNavigation
	navigation.Begin("git st")
	navigation.Select("git stash")

	if original := navigation.Cancel(); original != "git st" {
		t.Fatalf("expected original buffer, got %q", original)
	}
	if navigation.Active() {
		t.Fatal("expected navigation to be inactive after cancel")
	}
}
