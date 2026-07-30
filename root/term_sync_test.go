package root

import (
	"bytes"
	"testing"
)

func TestTerminalInputFilterDropsTerminalReports(t *testing.T) {
	filter := terminalInputFilter{}
	input := []byte("\x1b]11;rgb:0808/0a0a/0d0d\x1b\\git status\x1b[12;40R")
	got := filter.Filter(input)
	if string(got) != "git status" {
		t.Fatalf("expected only user input, got %q", got)
	}
}

func TestTerminalInputFilterHandlesFragmentedOSCResponse(t *testing.T) {
	filter := terminalInputFilter{}
	if got := filter.Filter([]byte("\x1b]11;rgb:0808")); len(got) != 0 {
		t.Fatalf("expected incomplete response to be buffered, got %q", got)
	}
	got := filter.Filter([]byte("/0a0a/0d0d\x07pwd"))
	if string(got) != "pwd" {
		t.Fatalf("expected command after response, got %q", got)
	}
}

func TestTerminalInputFilterPreservesKeyboardEscapeSequences(t *testing.T) {
	filter := terminalInputFilter{}
	input := []byte("\x1b[A\x1b[B\x1b[C\x1b[D")
	if got := filter.Filter(input); !bytes.Equal(got, input) {
		t.Fatalf("expected arrow keys unchanged, got %q", got)
	}
}

func TestNextTokenAcceptSequenceLength(t *testing.T) {
	for _, input := range []string{"\x1b[1;3C", "\x1b[1;5C", "\x1b[1;9C"} {
		if got := nextTokenAcceptSequenceLength([]byte(input), 0); got != len(input) {
			t.Fatalf("expected %q to be recognized, got length %d", input, got)
		}
	}
	if got := nextTokenAcceptSequenceLength([]byte("\x1b[C"), 0); got != 0 {
		t.Fatalf("plain right arrow must not accept one token, got %d", got)
	}
}

func TestConsumeNextTokenAcceptanceRequiresGhostText(t *testing.T) {
	if consumeNextTokenAcceptance(6, "") {
		t.Fatal("modified right should fall through when there is no ghost text")
	}
	if !consumeNextTokenAcceptance(6, "status ") {
		t.Fatal("modified right should be consumed when it accepts ghost text")
	}
	if consumeNextTokenAcceptance(0, "status ") {
		t.Fatal("unrecognized input should not be consumed")
	}
}

func TestDetectDarkBackgroundUsesColorFGBGWithoutTerminalQuery(t *testing.T) {
	t.Setenv("COLORFGBG", "15;0")
	if !detectDarkBackground() {
		t.Fatal("expected ANSI black background to be dark")
	}

	t.Setenv("COLORFGBG", "0;15")
	if detectDarkBackground() {
		t.Fatal("expected ANSI bright white background to be light")
	}

	t.Setenv("COLORFGBG", "")
	if !detectDarkBackground() {
		t.Fatal("expected dark fallback when the environment has no background hint")
	}
}
