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
		{name: "leading empty LF lines", raw: "\n\nprintf 'café 日本語'\nprintf second", want: "printf 'café 日本語'\nprintf second", ok: true},
		{name: "leading CRLF lines", raw: "\r\n\r\nprintf first", want: "printf first", ok: true},
		{name: "leading CR lines", raw: "\r\rprintf first", want: "printf first", ok: true},
		{name: "only empty lines", raw: "\r\n\n"},
		{name: "space after empty lines", raw: "\n printf private"},
		{name: "tab after empty lines", raw: "\r\n\tprintf private"},
		{name: "unicode space after empty lines", raw: "\n\u00a0printf private"},
		{name: "space before empty lines", raw: " \nprintf private"},
		{name: "nonempty whitespace line", raw: "\n \nprintf private"},
		{name: "control after empty lines", raw: "\nprintf ok\x1b[2J"},
		{name: "shell ignored after empty lines", raw: "\nprintf private", shellIgnored: true},
		{name: "sensitive after empty lines", raw: "\ncurl --token secret"},
		{name: "forgetting after empty lines", raw: "\nvuja history forget --command private"},
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
