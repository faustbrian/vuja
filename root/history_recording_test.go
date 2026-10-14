package root

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
)

func TestHistoryRecordingPauseFreezesAdmissionAtSubmission(t *testing.T) {
	var session historyRecordingSession
	beforePause := session.capture()
	session.toggle()
	duringPause := session.capture()
	session.toggle()
	for _, test := range []struct {
		name      string
		admission historySubmissionAdmission
		ignored   bool
		want      bool
	}{
		{"already submitted", beforePause, false, true},
		{"paused then resumed", duringPause, false, false},
		{"shell exclusion", beforePause, true, false},
		{"resumed", session.capture(), false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, admitted := test.admission.command("git status", test.ignored)
			if admitted != test.want || admitted && command != "git status" {
				t.Fatalf("submission admission changed after a toggle: admitted=%v", admitted)
			}
		})
	}
	var otherSession historyRecordingSession
	if _, ok := otherSession.capture().command("git status", false); !ok {
		t.Fatal("pause leaked into another session")
	}
}

func TestHistoryRecordingPausedSubmissionHasNoDurabilityOrHealthSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	store, err := scoring.NewFrecencyStore(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var session historyRecordingSession
	session.toggle()
	admission := session.capture()
	session.toggle() // A queued command remains paused after recording resumes.
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	entry := integration.HistoryEntry{ID: "paused:one", Command: "ssh forge@paused", NormalizedCommand: "ssh forge@paused", Source: "vuja", Cwd: "/repo", SubmittedAt: at, StartedAt: at, State: integration.HistoryStateRunning}
	observed := false
	for _, destination := range []*scoring.FrecencyStore{store, nil} {
		persisted, journaled := admission.persist(destination, entry, false, func(bool, bool) { observed = true })
		if persisted || journaled || observed {
			t.Fatal("intentional pause entered persistence, fallback, or failure health")
		}
	}
	events, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(events) != 0 {
		t.Fatalf("paused submission reached canonical history: count=%d error=%v", len(events), err)
	}
	changes, err := store.QueryHistoryChanges(t.Context(), 0, 20)
	if err != nil || len(changes.Changes) != 0 {
		t.Fatal("paused submission created publication changes")
	}
	journal, err := historyRecoveryJournalPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("paused submission created a recovery journal")
	}
	entry.ID, entry.Command, entry.NormalizedCommand = "resumed:one", "git status", "git status"
	if persisted, journaled := session.capture().persist(store, entry, false, nil); !persisted || journaled {
		t.Fatal("resuming did not restore canonical recording")
	}
}

func TestHistoryRecordingControlPreservesNativeAndPasteInput(t *testing.T) {
	bindings := newInputKeybindings(config.DefaultConfig().Keybindings)
	for _, sequence := range []string{"\x1bOS", "\x1b[14~"} {
		var session historyRecordingSession
		for _, input := range []struct{ shell, paste bool }{{false, false}, {true, true}} {
			if n := handleHistoryRecordingControl([]byte(sequence), 0, bindings, &session, input.shell, input.paste); n != 0 || session.paused.Load() {
				t.Fatal("pause stole application-owned or pasted bytes")
			}
		}
		if n := handleHistoryRecordingControl([]byte(sequence), 0, bindings, &session, true, false); n != len(sequence) || !session.paused.Load() {
			t.Fatal("shell pause control did not toggle recording")
		}
		if n := handleHistoryRecordingControl([]byte(sequence), 0, bindings, &session, true, false); n != len(sequence) || session.paused.Load() {
			t.Fatal("shell pause control did not resume recording")
		}
	}
	cfg := config.DefaultConfig()
	cfg.Keybindings.HistoryPause = "none"
	var session historyRecordingSession
	if n := handleHistoryRecordingControl([]byte("\x1bOS"), 0, newInputKeybindings(cfg.Keybindings), &session, true, false); n != 0 || session.paused.Load() {
		t.Fatal("disabled pause binding remained active")
	}
}

func TestHistoryRecordingPauseRemainsVisibleWithoutConfiguredStatus(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "recording", 48, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› "})
	c.WritePTY(terminalMarkerBytes("recording", "prompt-start"))
	c.WritePTY([]byte("› "))
	c.WritePTY(terminalMarkerBytes("recording", "prompt-end"))
	c.SetHistoryRecordingPaused(true)
	if !c.inputBoxStatusEnabled() || !strings.Contains(ansi.Strip(c.inputBoxStatusLine()), "History paused") {
		t.Fatal("intentional recording pause has no persistent indicator")
	}
	c.SetStatusSnapshot(statusSnapshot{Directory: "/repo", Revision: 5})
	c.SetSessionHealth(healthBadge{Label: "History journal", Revision: 10})
	if !strings.Contains(ansi.Strip(c.inputBoxStatusLine()), "History paused") {
		t.Fatal("status refresh hid intentional pause")
	}
	screen := applyTerminalOutput(t, out.Bytes(), 48, 12)
	visible := false
	for row := range 12 {
		visible = visible || strings.Contains(screenLine(screen, row), "History paused")
	}
	if !visible {
		t.Fatal("pause indicator was not actually painted")
	}
	c.SetHistoryRecordingPaused(false)
	if strings.Contains(ansi.Strip(c.inputBoxStatusLine()), "History paused") {
		t.Fatal("resuming left a stale pause indicator")
	}
}

func TestHistoryRecordingShellRequestsAreIdempotentAndDoNotExecute(t *testing.T) {
	var session historyRecordingSession
	admitted := session.capture()
	for _, request := range []struct{ message, reply string }{
		{"VUJA_HISTORY_RECORDING_PAUSE", "1"},
		{"VUJA_HISTORY_RECORDING_PAUSE", "1"},
		{"VUJA_HISTORY_RECORDING_RESUME", "0"},
		{"VUJA_HISTORY_RECORDING_RESUME", "0"},
	} {
		var response bytes.Buffer
		handled, err := handleHistoryRecordingMessage(request.message, &session, &response)
		if err != nil || !handled || response.String() != request.reply || session.paused.Load() != (request.reply == "1") {
			t.Fatalf("shell control was not acknowledged exactly once: handled=%v response=%q error=%v", handled, response.String(), err)
		}
	}
	if _, ok := admitted.command("git status", false); !ok {
		t.Fatal("shell request changed an earlier admission")
	}
	for _, message := range []string{"VUJA_HISTORY_RECORDING_PAUSE:extra", "echo VUJA_HISTORY_RECORDING_PAUSE", "git status"} {
		var response bytes.Buffer
		if handled, err := handleHistoryRecordingMessage(message, &session, &response); handled || err != nil || response.Len() != 0 || session.paused.Load() {
			t.Fatal("ordinary input gained shell-control authority")
		}
	}
	reader, writer := io.Pipe()
	_ = reader.Close()
	defer writer.Close()
	handled, err := handleHistoryRecordingMessage("VUJA_HISTORY_RECORDING_PAUSE", &session, writer)
	if !handled || !errors.Is(err, io.ErrClosedPipe) || !session.paused.Load() {
		t.Fatal("failed acknowledgement hid a control failure or reopened recording")
	}
}

func TestHistoryRecordingPauseRemainsVisibleAcrossCompactLayouts(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "compact-pause", 48, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› "})
	c.WritePTY(terminalMarkerBytes("compact-pause", "prompt-start"))
	c.WritePTY([]byte("› hi"))
	c.WritePTY(terminalMarkerBytes("compact-pause", "prompt-end"))
	c.SetHistoryRecordingPaused(true)
	for _, size := range []struct{ width, height int }{{48, 5}, {48, 4}, {11, 8}, {12, 8}, {13, 8}, {16, 8}, {17, 5}, {18, 8}, {48, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			out.Reset()
			c.Resize(size.width, size.height)
			screen := applyTerminalOutput(t, out.Bytes(), size.width, size.height)
			if !terminalContainsLine(screen, "paused") && !terminalContainsLine(screen, "PAUSED") {
				t.Fatalf("paused recording became invisible after resize: %q", terminalScreenLines(screen))
			}
			if !terminalContainsLine(screen, "› hi") {
				t.Fatalf("pause warning displaced editable input: %q", terminalScreenLines(screen))
			}
			cursor := screen.CursorPosition()
			if line := screenLine(screen, cursor.Y); !strings.Contains(line, "› hi") {
				t.Fatalf("cursor left editable input for %q", line)
			}
			out.Reset()
			c.SetHistoryRecordingPaused(false)
			if _, err := screen.Write(out.Bytes()); err != nil {
				t.Fatal(err)
			}
			if terminalContainsLine(screen, "paused") || terminalContainsLine(screen, "PAUSED") {
				t.Fatalf("resuming left a stale privacy warning: %q", terminalScreenLines(screen))
			}
			c.SetHistoryRecordingPaused(true)
		})
	}
	// The privacy indicator does not depend on an optional colored surface.
	out.Reset()
	c.SetInputBoxTheme(terminalInputBoxTheme{})
	screen := applyTerminalOutput(t, out.Bytes(), 48, 12)
	if !terminalContainsLine(screen, "paused") && !terminalContainsLine(screen, "PAUSED") {
		t.Fatal("disabling chatbox decoration hid recording pause")
	}
}

func TestHistoryRecordingCompactCommandStartPreservesAllInputCells(t *testing.T) {
	for _, mode := range []string{"output", "snapshot"} {
		for _, decorated := range []bool{true, false} {
			for _, viewport := range []string{"terminal", "pinned"} {
				t.Run(fmt.Sprintf("%s/surface=%v/%s", mode, decorated, viewport), func(t *testing.T) {
					var out bytes.Buffer
					c := newTerminalCompositor(&out, "bottom", "compact-start", 11, 10)
					t.Cleanup(c.Close)
					if decorated {
						c.SetInputBoxTheme(testInputBoxTheme())
					}
					c.SetChatboxConfig(terminalChatboxConfig{Prompt: "> ", Scrollback: mode, OutputViewport: viewport, OutputLines: 100})
					c.WritePTY(terminalMarkerBytes("compact-start", "prompt-start"))
					c.WritePTY([]byte("> 12345678"))
					c.WritePTY(terminalMarkerBytes("compact-start", "prompt-end"))
					c.SetHistoryRecordingPaused(true)
					screen := applyTerminalOutput(t, out.Bytes(), 11, 10)
					if !terminalContainsLine(screen, "> 12345678") {
						t.Fatalf("fixture did not show complete editable input: %q", terminalScreenLines(screen))
					}
					out.Reset()
					c.WritePTY(terminalMarkerBytes("compact-start", "command-start"))
					if _, err := screen.Write(out.Bytes()); err != nil {
						t.Fatal(err)
					}
					if !terminalContainsLine(screen, "> 12345678") {
						t.Fatalf("command-start clipped compact unpadded input: %q", terminalScreenLines(screen))
					}
					if viewport == "pinned" && mode == "snapshot" {
						found := false
						for y := range c.viewport.model.Height() {
							found = found || strings.Contains(screenLine(c.viewport.model, y), "> 12345678")
						}
						if !found {
							t.Fatal("pinned snapshot clipped the retained submitted command")
						}
					}
				})
			}
		}
	}
}

func TestHistoryRecordingCompactRunningResizeUsesUnpaddedWidth(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "compact-resize", 11, 10)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "> ", Scrollback: "output", OutputViewport: "pinned", OutputLines: 100})
	c.WritePTY(terminalMarkerBytes("compact-resize", "prompt-start"))
	c.WritePTY([]byte("> 12345678"))
	c.WritePTY(terminalMarkerBytes("compact-resize", "prompt-end"))
	c.SetHistoryRecordingPaused(true)
	c.WritePTY(terminalMarkerBytes("compact-resize", "command-start"))
	out.Reset()
	c.Resize(11, 9)
	screen := applyTerminalOutput(t, out.Bytes(), 11, 9)
	if !terminalContainsLine(screen, "> 12345678") {
		t.Fatalf("running resize wrapped or clipped input that fits the compact surface: %q", terminalScreenLines(screen))
	}
}

func TestHistoryRecordingPauseSurvivesPinnedCommandStatus(t *testing.T) {
	for _, width := range []int{12, 17, 48} {
		for _, mode := range []string{"output", "snapshot"} {
			t.Run(fmt.Sprintf("%dx12/%s", width, mode), func(t *testing.T) {
				var out bytes.Buffer
				c := newTerminalCompositor(&out, "bottom", "pause-running", width, 12)
				t.Cleanup(c.Close)
				c.SetInputBoxTheme(testInputBoxTheme())
				c.SetChatboxConfig(terminalChatboxConfig{Prompt: "> ", Scrollback: mode, OutputViewport: "pinned", OutputLines: 100})
				now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
				c.now = func() time.Time { return now }
				c.WritePTY(terminalMarkerBytes("pause-running", "prompt-start"))
				c.WritePTY([]byte("> wait"))
				c.WritePTY(terminalMarkerBytes("pause-running", "prompt-end"))
				c.SetHistoryRecordingPaused(true)
				screen := applyTerminalOutput(t, out.Bytes(), width, 12)
				assertPaused := func(stage string) {
					t.Helper()
					if _, err := screen.Write(out.Bytes()); err != nil {
						t.Fatal(err)
					}
					out.Reset()
					if !terminalContainsLine(screen, "paused") && !terminalContainsLine(screen, "PAUSED") {
						t.Errorf("%s hid recording pause: %q", stage, terminalScreenLines(screen))
					}
				}
				out.Reset()
				c.WritePTY(terminalMarkerBytes("pause-running", "command-start"))
				assertPaused("command start")
				now = now.Add(time.Second)
				c.RefreshRunningStatus()
				assertPaused("silent running tick")
				c.WritePTY([]byte("progress\r\n"))
				assertPaused("output repaint")
				c.WritePTY(terminalMarkerBytes("pause-running", "command-end:0"))
				assertPaused("completion")
				c.WritePTY(terminalMarkerBytes("pause-running", "prompt-start"))
				c.WritePTY([]byte("> next"))
				c.WritePTY(terminalMarkerBytes("pause-running", "prompt-end"))
				assertPaused("next prompt")
				c.SetHistoryRecordingPaused(false)
				if _, err := screen.Write(out.Bytes()); err != nil {
					t.Fatal(err)
				}
				if terminalContainsLine(screen, "paused") || terminalContainsLine(screen, "PAUSED") {
					t.Fatalf("resuming left a stale pause indicator: %q", terminalScreenLines(screen))
				}
			})
		}
	}
}

func TestHistoryRecordingPauseDoesNotPaintOverRunningApplications(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "pause-native", 48, 12)
	t.Cleanup(c.Close)
	c.SetInputBoxTheme(testInputBoxTheme())
	c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› "})
	c.WritePTY(terminalMarkerBytes("pause-native", "prompt-start"))
	c.WritePTY([]byte("› long-command"))
	c.WritePTY(terminalMarkerBytes("pause-native", "prompt-end"))
	c.WritePTY(terminalMarkerBytes("pause-native", "command-start"))
	out.Reset()
	c.SetHistoryRecordingPaused(true)
	if out.Len() != 0 {
		t.Fatal("pause state overwrote a running command's display")
	}
	c.WritePTY([]byte("\x1b[?1049happlication owns this screen"))
	out.Reset()
	c.SetHistoryRecordingPaused(false)
	if out.Len() != 0 {
		t.Fatal("pause state overwrote a native application's display")
	}
}

func TestHistoryRecordingEditorQueriesDoNotConsumeControlAcknowledgements(t *testing.T) {
	// The generated hook is the editor-side wire contract. It is inspected,
	// never sourced: executing integration is reserved for Brian.
	script := shellInitScript("zsh", "/tmp/vuja")
	wantSender := fmt.Sprintf("print -u $VUJA_FD -N -r -- %q\"$_vuja_editor_query\"", shellEditorQueryPrefix)
	if !strings.Contains(script, wantSender) {
		t.Error("generated Zsh sender still publishes editor text as control messages")
	}
	var session historyRecordingSession
	session.toggle()
	var acknowledgements bytes.Buffer
	texts := []string{"VUJA_HISTORY_RECORDING_RESUME", "VUJA_HISTORY_RECORDING_PAUSE", "VUJA_CMD_START", shellEditorQueryPrefix + "VUJA_HISTORY_RECORDING_RESUME", ""}
	var frames bytes.Buffer
	for _, text := range texts {
		frames.WriteString(shellEditorQueryPrefix + text)
		frames.WriteByte(0)
	}
	frames.WriteString("VUJA_HISTORY_RECORDING_RESUME\x00VUJA_CMD_START\x00")
	reader := bufio.NewReader(&frames)
	for _, text := range texts {
		message, oversized, err := readShellMessage(reader, shellMessageByteLimit)
		if err != nil || oversized {
			t.Fatalf("editor frame was not read intact: oversized=%v error=%v", oversized, err)
		}
		if handled, err := handleHistoryRecordingMessage(string(message), &session, &acknowledgements); handled || err != nil || acknowledgements.Len() != 0 || !session.paused.Load() {
			t.Fatal("editor payload gained control authority or consumed an acknowledgement")
		}
		if start, _ := parseCommandStartMessage(string(message)); start {
			t.Fatal("editor payload started an execution")
		}
		if got := decodeShellEditorQuery(string(message)); got != text {
			t.Errorf("editor query was not decoded exactly once: got %q, want %q", got, text)
		}
	}
	request, _, err := readShellMessage(reader, shellMessageByteLimit)
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := handleHistoryRecordingMessage(string(request), &session, &acknowledgements); !handled || err != nil || acknowledgements.String() != "0" || session.paused.Load() {
		t.Fatal("genuine function request did not receive exactly its own acknowledgement")
	}
	next, _, err := readShellMessage(reader, shellMessageByteLimit)
	if start, ignored := parseCommandStartMessage(string(next)); err != nil || !start || ignored || acknowledgements.String() != "0" {
		t.Fatal("control request consumed the following foreground start or polluted its acknowledgement stream")
	}
}

func TestHistoryRecordingEditorEscapesPreserveDelimitersAsData(t *testing.T) {
	script := shellInitScript("zsh", "/tmp/vuja")
	for _, replacement := range []string{
		`local _vuja_editor_query=${LBUFFER//\\/\\\\}`,
		`_vuja_editor_query=${_vuja_editor_query//$'\0'/'\0'}`,
	} {
		if !strings.Contains(script, replacement) {
			t.Errorf("generated editor sender lacks delimiter escaping: %s", replacement)
		}
	}
	for _, fixture := range []struct{ text, encoded string }{
		{"left\x00VUJA_HISTORY_RECORDING_RESUME", `left\0VUJA_HISTORY_RECORDING_RESUME`},
		{`literal\0`, `literal\\0`},
		{"slash\\\x00tail", `slash\\\0tail`},
		{"line one\nline two λ", "line one\nline two λ"},
	} {
		wire := shellEditorQueryPrefix + fixture.encoded + "\x00VUJA_CMD_START\x00"
		reader := bufio.NewReader(strings.NewReader(wire))
		frame, oversized, err := readShellMessage(reader, shellMessageByteLimit)
		if err != nil || oversized {
			t.Fatalf("encoded editor frame was invalid: oversized=%v error=%v", oversized, err)
		}
		if got := decodeShellEditorQuery(string(frame)); got != fixture.text {
			t.Errorf("editor data changed across framing: got %q, want %q", got, fixture.text)
		}
		var session historyRecordingSession
		var ack bytes.Buffer
		if handled, err := handleHistoryRecordingMessage(string(frame), &session, &ack); handled || err != nil || ack.Len() != 0 {
			t.Fatal("encoded editor data gained history-control authority")
		}
		next, _, err := readShellMessage(reader, shellMessageByteLimit)
		if start, ignored := parseCommandStartMessage(string(next)); err != nil || !start || ignored {
			t.Fatal("an embedded editor delimiter corrupted the following command-start frame")
		}
	}
	if got := decodeShellEditorQuery(`unframed\0`); got != `unframed\0` {
		t.Fatal("legacy unframed text was decoded as the new editor protocol")
	}
}
