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

func TestEmptyPromptHistoryMirrorsSelectionWithoutExecutingIt(t *testing.T) {
	if got := historyPromptReplacement("just test"); string(got) != "\x15just test" {
		t.Fatalf("expected clear-line plus command without enter, got %q", got)
	}
}

func TestEmptyPromptHistoryMirrorsMultilineSelectionWithoutExecutingIt(t *testing.T) {
	command := "printf first\nprintf second"
	want := append([]byte{0x15}, bracketedPasteSequence(command)...)
	if got := historyPromptReplacement(command); string(got) != string(want) {
		t.Fatalf("expected multiline recall to use bracketed paste, got %q", got)
	}
}
