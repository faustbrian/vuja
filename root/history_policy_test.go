package root

import "testing"

func TestHistoryRecordableCommandPreservesPrivacySignalsUntilTheGate(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		shellIgnored bool
		want         string
		ok           bool
	}{
		{name: "ordinary", raw: "git status", want: "git status", ok: true},
		{name: "trailing whitespace", raw: "git status  ", want: "git status", ok: true},
		{name: "leading space", raw: " export TOKEN=value", ok: false},
		{name: "leading tab", raw: "\tprintf secret", ok: false},
		{name: "shell history pattern", raw: "curl --password value", shellIgnored: true, ok: false},
		{name: "empty", raw: "   ", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := historyRecordableCommand(test.raw, test.shellIgnored)
			if ok != test.ok || got != test.want {
				t.Fatalf("historyRecordableCommand(%q, %v) = %q, %v; want %q, %v", test.raw, test.shellIgnored, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestCommandStartMessageCarriesOnlyTheHistoryDecision(t *testing.T) {
	for _, test := range []struct {
		message string
		match   bool
		ignore  bool
	}{
		{message: "VUJA_CMD_START", match: true},
		{message: "VUJA_CMD_START:IGNORE", match: true, ignore: true},
		{message: "VUJA_CMD_STOP:0"},
	} {
		match, ignored := parseCommandStartMessage(test.message)
		if match != test.match || ignored != test.ignore {
			t.Fatalf("parseCommandStartMessage(%q) = %v, %v; want %v, %v", test.message, match, ignored, test.match, test.ignore)
		}
	}
}
