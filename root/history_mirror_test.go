package root

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
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
		"\n ssh forge@private",
		"\r\n\tssh forge@private",
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

func TestNativeHistoryMirrorPreservesLeadingEmptyLines(t *testing.T) {
	at := time.Unix(1_725_000_000, 0)
	const raw = "\n\nprintf 'café 日本語\\n'  "
	for _, test := range []struct{ shell, want string }{
		{"zsh", ": 1725000000:0;\\\n\\\nprintf 'café 日本語\\n'  \n"},
		{"bash", "\n\nprintf 'café 日本語\\n'  \n"},
		{"fish", "- cmd: \\n\\nprintf 'café 日本語\\\\n'  \n  when: 1725000000\n"},
	} {
		t.Run(test.shell, func(t *testing.T) {
			record, ok := recordableNativeHistoryRecord(raw, test.shell, at)
			if !ok || record != test.want {
				t.Fatalf("native mirror changed accepted multiline bytes: got %q, %v; want %q", record, ok, test.want)
			}
		})
	}
}

func TestNativeHistoryMirrorZshKeepsTrailingBackslashExecutionsSeparate(t *testing.T) {
	at := time.Unix(1_725_000_000, 0)
	first, ok := recordableNativeHistoryRecord("\nprintf public # trailing\\", "zsh", at)
	if !ok {
		t.Fatal("eligible multiline input was not mirrored")
	}
	second, ok := recordableNativeHistoryRecord("printf next", "zsh", at.Add(time.Minute))
	if !ok {
		t.Fatal("following command was not mirrored")
	}
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := integration.LoadShellHistoryEntries(t.Context(), path, "zsh")
	if err != nil || len(entries) != 2 {
		t.Fatalf("mirror/import merged independent executions: count=%d err=%v", len(entries), err)
	}
	if entries[0].Command != "printf public # trailing\\" || !entries[0].StartedAt.Equal(at) ||
		entries[1].Command != "printf next" || !entries[1].StartedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("mirror/import changed command text or execution metadata: %+v", entries)
	}
	if first != ": 1725000000:0;\\\nprintf public # trailing\\ \n" {
		t.Fatalf("trailing backslash was not terminated using native Zsh framing: %q", first)
	}
}
