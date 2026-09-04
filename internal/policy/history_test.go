package policy

import "testing"

func TestHistoryCommandCentralizesPersistentHistoryPrivacy(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		shellIgnored bool
		want         string
		ok           bool
	}{
		{name: "ordinary", raw: "ssh forge@api  ", want: "ssh forge@api", ok: true},
		{name: "multiline", raw: "printf first\nprintf second", want: "printf first\nprintf second", ok: true},
		{name: "internal tab", raw: "printf\tvalue", want: "printf\tvalue", ok: true},
		{name: "leading space", raw: " ssh forge@private"},
		{name: "leading tab", raw: "\tssh forge@private"},
		{name: "shell ignored", raw: "ssh forge@private", shellIgnored: true},
		{name: "sensitive", raw: "curl --token secret"},
		{name: "control data", raw: "printf ok\x00hidden"},
		{name: "terminal escape", raw: "printf ok\x1b[2J"},
		{name: "empty", raw: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := HistoryCommand(test.raw, test.shellIgnored)
			if got != test.want || ok != test.ok {
				t.Fatalf("HistoryCommand(%q, %v) = %q, %v; want %q, %v", test.raw, test.shellIgnored, got, ok, test.want, test.ok)
			}
		})
	}
}
