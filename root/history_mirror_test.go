package root

import (
	"strings"
	"testing"
	"time"
)

func TestNativeHistoryRecordContainsOnlyTheAcceptedCommand(t *testing.T) {
	at := time.Unix(1_725_000_000, 0)
	for _, test := range []struct {
		shell string
		want  string
	}{
		{shell: "zsh", want: ": 1725000000:0;ssh forge@api\n"},
		{shell: "bash", want: "ssh forge@api\n"},
		{shell: "fish", want: "- cmd: ssh forge@api\n  when: 1725000000\n"},
	} {
		t.Run(test.shell, func(t *testing.T) {
			got := nativeHistoryRecord("ssh forge@api", test.shell, at)
			if got != test.want {
				t.Fatalf("nativeHistoryRecord() = %q; want %q", got, test.want)
			}
			if strings.Contains(got, "private") {
				t.Fatalf("mirror record unexpectedly contained unrelated history: %q", got)
			}
		})
	}
}

func TestNativeHistoryMirrorRejectsCommandsBeforeFormatting(t *testing.T) {
	at := time.Unix(1_725_000_000, 0)
	for _, command := range []string{
		" ssh forge@private",
		"curl --token private-value https://example.test",
		"printf '\x1b[31m'",
	} {
		if record, ok := recordableNativeHistoryRecord(command, "zsh", at); ok || record != "" {
			t.Fatalf("expected native mirror to reject %q before formatting, got %q, %v", command, record, ok)
		}
	}

	const accepted = "ssh forge@api  "
	record, ok := recordableNativeHistoryRecord(accepted, "bash", at)
	if !ok || record != accepted+"\n" {
		t.Fatalf("expected accepted exact command to retain trailing whitespace, got %q, %v", record, ok)
	}
}
