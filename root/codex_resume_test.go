package root

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testCodexResumeID = "019fbd44-9268-7963-b8f5-a8aa6165da2e"

func TestCodexResumeLinkifierLinksBothInstructionFormatsAcrossChunks(t *testing.T) {
	input := "To continue this session, run codex resume, then select go-port-ship (" + testCodexResumeID + ")\r\n" +
		"To continue this session, run codex resume " + testCodexResumeID + "\r\n"
	linkifier := newCodexResumeLinkifier(func(id string) string {
		return "vuja://codex-resume/" + id
	})

	var rendered strings.Builder
	for _, chunk := range []string{input[:17], input[17:83], input[83:127], input[127:]} {
		rendered.Write(linkifier.Transform([]byte(chunk)))
	}
	rendered.Write(linkifier.Flush())

	got := rendered.String()
	if strings.Count(got, "vuja://codex-resume/"+testCodexResumeID) != 2 {
		t.Fatalf("expected both resume instructions to link the session ID, got %q", got)
	}
	if visible := stripCodexResumeLinks(got); visible != input {
		t.Fatalf("expected hyperlinks to preserve visible output\nwant: %q\n got: %q", input, visible)
	}
}

func TestCodexResumeLinkifierRecognizesANSIStyledInstructionAcrossChunks(t *testing.T) {
	input := "\x1b[2mTo continue this session,\x1b[0m run \x1b[1mcodex resume\x1b[0m " + testCodexResumeID + "\r\n"
	linkifier := newCodexResumeLinkifier(func(id string) string {
		return "vuja://codex-resume/" + id
	})

	var rendered strings.Builder
	for start := 0; start < len(input); start += 3 {
		end := min(start+3, len(input))
		rendered.Write(linkifier.Transform([]byte(input[start:end])))
	}
	rendered.Write(linkifier.Flush())

	got := rendered.String()
	if !strings.Contains(got, "vuja://codex-resume/"+testCodexResumeID) {
		t.Fatalf("expected the styled resume instruction to link its session ID, got %q", got)
	}
	if visible := stripCodexResumeLinks(got); visible != input {
		t.Fatalf("expected hyperlinks to preserve styled output\nwant: %q\n got: %q", input, visible)
	}
}

func TestCodexResumeLinkifierLeavesUnrelatedOutputUntouched(t *testing.T) {
	input := "build 019fbd44-9268-7963-b8f5-a8aa6165da2e complete\r\n"
	linkifier := newCodexResumeLinkifier(func(id string) string {
		return "vuja://codex-resume/" + id
	})
	got := string(linkifier.Transform([]byte(input))) + string(linkifier.Flush())
	if got != input {
		t.Fatalf("expected unrelated output to remain byte-for-byte unchanged, got %q", got)
	}
}

func TestCodexResumeLinkifierNeverBuffersOrdinaryOutput(t *testing.T) {
	linkifier := newCodexResumeLinkifier(func(id string) string {
		return "vuja://codex-resume/" + id
	})
	for _, chunk := range []string{"T", "o", " ordinary output"} {
		if got := string(linkifier.Transform([]byte(chunk))); got != chunk {
			t.Fatalf("expected ordinary chunk %q to render immediately, got %q", chunk, got)
		}
	}
}

func TestCodexResumeLinkifierDoesNotDelayFollowingTerminalControlTraffic(t *testing.T) {
	linkifier := newCodexResumeLinkifier(func(id string) string {
		return "vuja://codex-resume/" + id
	})
	input := "To continue this session, run codex resume " + testCodexResumeID + "\x1b]777;vuja;prompt-start\a"
	got := string(linkifier.Transform([]byte(input)))
	if visible := stripCodexResumeLinks(got); visible != input {
		t.Fatalf("expected terminal control traffic to flush the resume instruction, got %q", visible)
	}
	if flushed := linkifier.Flush(); len(flushed) != 0 {
		t.Fatalf("expected no buffered output after terminal control traffic, got %q", flushed)
	}
}

func TestCodexResumeCommandAllowsOnlyAnExactSessionID(t *testing.T) {
	if got := codexResumeCommand(testCodexResumeID); got != "codex resume "+testCodexResumeID {
		t.Fatalf("expected exact resume command, got %q", got)
	}
	for _, invalid := range []string{"", testCodexResumeID + " --dangerous", "$(touch /tmp/nope)"} {
		if got := codexResumeCommand(invalid); got != "" {
			t.Fatalf("expected invalid ID %q to be rejected, got %q", invalid, got)
		}
	}
}

func TestCodexResumeInputExecutesOnlyAtAnIdleEmptyPrompt(t *testing.T) {
	want := "codex resume " + testCodexResumeID + "\r"
	if got := string(codexResumeInput(testCodexResumeID, false, "", 0)); got != want {
		t.Fatalf("expected idle empty prompt to receive %q, got %q", want, got)
	}
	for _, test := range []struct {
		busy   bool
		buffer string
		offset int
	}{
		{busy: true},
		{buffer: "git status"},
		{offset: 1},
	} {
		if got := codexResumeInput(testCodexResumeID, test.busy, test.buffer, test.offset); len(got) != 0 {
			t.Fatalf("expected unsafe activation state to be rejected, got %q", got)
		}
	}
}

func TestCodexResumeActionServerAcceptsOnlyObservedSessionIDs(t *testing.T) {
	actionsDir, err := os.MkdirTemp(os.TempDir(), "vja-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(actionsDir) })
	server, err := newCodexResumeActionServerIn(actionsDir, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)

	actionURL := server.Observe(testCodexResumeID)
	if err := dispatchCodexResumeURL(actionURL, actionsDir); err != nil {
		t.Fatalf("dispatch observed resume URL: %v", err)
	}
	select {
	case got := <-server.Actions():
		if got != testCodexResumeID {
			t.Fatalf("expected observed ID %q, got %q", testCodexResumeID, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observed resume action")
	}

	parsed, err := url.Parse(actionURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/019fbd44-9268-7963-b8f5-a8aa6165da2f"
	if err := dispatchCodexResumeURL(parsed.String(), actionsDir); err != nil {
		t.Fatalf("dispatch well-formed unobserved URL: %v", err)
	}
	select {
	case got := <-server.Actions():
		t.Fatalf("expected unobserved ID to be rejected, got %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatchCodexResumeURLRejectsSocketOutsidePrivateActionDirectory(t *testing.T) {
	actionsDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "attacker.sock")
	actionURL := (&url.URL{
		Scheme: "vuja",
		Host:   "codex-resume",
		Path:   "/" + testCodexResumeID,
		RawQuery: url.Values{
			"socket": []string{outside},
			"token":  []string{"0123456789abcdef0123456789abcdef"},
		}.Encode(),
	}).String()
	if err := dispatchCodexResumeURL(actionURL, actionsDir); err == nil {
		t.Fatal("expected a socket outside the private action directory to be rejected")
	}
}

func TestCodexResumeURLHandlerArtifactsForwardTheClickedURL(t *testing.T) {
	binaryPath := "/tmp/Vuja Binary's/vuja"
	appleScript := codexResumeAppleScript(binaryPath)
	if !strings.Contains(appleScript, "on open location actionURL") ||
		!strings.Contains(appleScript, "open-url") ||
		!strings.Contains(appleScript, "quoted form of actionURL") {
		t.Fatalf("expected AppleScript URL handler to forward the clicked URL, got %q", appleScript)
	}
	desktopEntry := codexResumeDesktopEntry(binaryPath)
	if !strings.Contains(desktopEntry, "MimeType=x-scheme-handler/vuja;") ||
		!strings.Contains(desktopEntry, " open-url %u") {
		t.Fatalf("expected desktop URL handler to register and forward vuja URLs, got %q", desktopEntry)
	}
}

func stripCodexResumeLinks(value string) string {
	for {
		start := strings.Index(value, "\x1b]8;")
		if start < 0 {
			return value
		}
		end := strings.Index(value[start:], "\x1b\\")
		if end < 0 {
			return value
		}
		end += start + len("\x1b\\")
		value = value[:start] + value[end:]
	}
}
