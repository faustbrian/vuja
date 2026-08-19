package root

import (
	"slices"
	"testing"

	"github.com/faustbrian/vuja/internal/config"
)

func TestInputKeybindingsMatchConfiguredActions(t *testing.T) {
	bindings := newInputKeybindings(config.KeybindingsConfig{
		HistorySearch:      "ctrl+g",
		HistorySuccessOnly: "none",
		Accept:             "ctrl+n",
		ToggleOverlay:      "ctrl+t",
		AcceptToken:        []string{"alt+right"},
		NavigationUp:       "ctrl+p",
		NavigationDown:     "ctrl+d",
		NavigationPageUp:   "page-up",
		NavigationPageDown: "page-down",
		NavigationFirst:    "home",
		NavigationLast:     "end",
	})

	tests := []struct {
		action inputAction
		input  string
		want   int
	}{
		{action: inputHistorySearch, input: "\x07", want: 1},
		{action: inputHistorySuccessOnly, input: "\x13", want: 0},
		{action: inputAccept, input: "\x0e", want: 1},
		{action: inputToggleOverlay, input: "\x14", want: 1},
		{action: inputAcceptToken, input: "\x1b[1;3C", want: 6},
		{action: inputAcceptToken, input: "\x1b[1;5C", want: 0},
		{action: inputNavigationUp, input: "\x10", want: 1},
		{action: inputNavigationDown, input: "\x04", want: 1},
		{action: inputNavigationPageUp, input: "\x1b[5~", want: 4},
		{action: inputNavigationPageDown, input: "\x1b[6~", want: 4},
		{action: inputNavigationFirst, input: "\x1b[H", want: 3},
		{action: inputNavigationLast, input: "\x1b[F", want: 3},
	}

	for _, test := range tests {
		if got := bindings.match(test.action, []byte(test.input), 0); got != test.want {
			t.Errorf("%s against %q: expected %d bytes, got %d", test.action, test.input, test.want, got)
		}
	}
}

func TestInputKeybindingsCoalesceContiguousNavigationBurst(t *testing.T) {
	bindings := newInputKeybindings(config.DefaultConfig().Keybindings)
	actions, consumed := bindings.navigationRun([]byte("\x1b[B\x1b[B\x1b[6~typed"), 0)
	want := []inputAction{inputNavigationDown, inputNavigationDown, inputNavigationPageDown}
	if !slices.Equal(actions, want) || consumed != 10 {
		t.Fatalf("expected one three-action navigation burst consuming 10 bytes, actions=%v consumed=%d", actions, consumed)
	}
}

func TestInputKeybindingsResolveViNavigationAndOverrides(t *testing.T) {
	bindings := newInputKeybindings(config.KeybindingsConfig{
		Keymap:        "vi",
		MoveBeginning: "ctrl+b",
	})
	if got := bindings.match(inputMoveBeginning, []byte{0x02}, 0); got != 1 {
		t.Fatalf("expected explicit vi beginning override, got %d", got)
	}
	if got := bindings.match(inputMoveEnd, []byte("\x1b[F"), 0); got != 3 {
		t.Fatalf("expected vi end binding to use terminal End key, got %d", got)
	}
}
