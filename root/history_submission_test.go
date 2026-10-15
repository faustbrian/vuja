package root

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/scoring"
)

func TestHistorySubmissionUsesAcceptedCommandInsteadOfEditorPrefix(t *testing.T) {
	const accepted = `printf 'VUJA_PID=%s\npaused=%s\nrecordable_ack=%s\n' "${VUJA_PID:-unset}" "${_vuja_history_paused:-unset}" "${_vuja_history_recordable:-unset}"`
	for _, fallback := range []string{accepted[:106], "", "printf 'café 日本語'"} {
		t.Run(fallback, func(t *testing.T) {
			// Enter can snapshot an older LBUFFER, including an empty redraw or
			// a cursor-left prefix. Preexec observes what the shell accepted.
			encoded := strings.ReplaceAll(accepted, `\`, `\\`)
			reader := bufio.NewReader(strings.NewReader("VUJA_CMD_START:COMMAND:" + encoded + "\x00"))
			frame, oversized, err := readShellMessage(reader, shellMessageByteLimit)
			if err != nil || oversized {
				t.Fatalf("read accepted submission: oversized=%v err=%v", oversized, err)
			}
			var session historyRecordingSession
			admission := session.capture()
			tracker := newCommandExecutionTracker(func() time.Time { return time.Unix(100, 0) }, "capture", "host")
			entry, recordable := admission.startExecution(tracker, string(frame), fallback, "/repo", "zsh", false)
			if entry.Command != accepted || !recordable {
				t.Fatalf("captured %d bytes, want complete %d-byte accepted command (recordable=%v)", len(entry.Command), len(accepted), recordable)
			}
			completed := tracker.Finish(0)
			if completed.Command != accepted || completed.ID != entry.ID {
				t.Fatal("completion changed accepted text or execution identity")
			}
			next, ok := admission.startExecution(tracker, "VUJA_CMD_START:COMMAND:printf second", accepted, "/repo", "zsh", false)
			if !ok || next.Command != "printf second" || next.ID == entry.ID {
				t.Fatal("next accepted submission reused stale text or identity")
			}
		})
	}
}

func TestHistorySubmissionAcceptedPayloadKeepsPrivacyAndPauseAdmission(t *testing.T) {
	for _, test := range []struct {
		name, raw                                   string
		ignored, pausedAtEnter, pausedAtStart, want bool
	}{
		{name: "ordinary", raw: "git status", want: true},
		{name: "pause after acceptance", raw: "git status", pausedAtStart: true, want: true},
		{name: "resume after acceptance", raw: "git status", pausedAtEnter: true},
		{name: "shell exclusion", raw: "git status", ignored: true},
		{name: "leading whitespace", raw: " printf private"},
		{name: "sensitive omitted suffix", raw: "curl example.test --password secret"},
		{name: "forgetting request", raw: `vuja history forget --command 'private'`},
		{name: "empty accepted payload", raw: ""},
		{name: "embedded delimiter", raw: "printf ok\x00VUJA_HISTORY_RECORDING_RESUME"},
		{name: "literal delimiter escape", raw: `printf '\0'`, want: true},
		{name: "control-looking data", raw: "printf 'VUJA_CMD_START\nVUJA_HISTORY_RECORDING_RESUME'", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			store, err := scoring.NewFrecencyStore(filepath.Join(home, "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			var session historyRecordingSession
			session.paused.Store(test.pausedAtEnter)
			admission := session.capture()
			session.paused.Store(test.pausedAtStart)
			prefix := commandStartMessage
			if test.ignored {
				prefix = commandStartIgnoreMessage
			}
			encoded := strings.NewReplacer(`\`, `\\`, "\x00", `\0`).Replace(test.raw)
			message := prefix + ":COMMAND:" + encoded
			match, ignored := parseCommandStartMessage(message)
			if !match || ignored != test.ignored {
				t.Fatal("accepted command lost its shell-policy decision")
			}
			var ack bytes.Buffer
			if handled, err := handleHistoryRecordingMessage(message, &session, &ack); handled || err != nil || ack.Len() != 0 || session.paused.Load() != test.pausedAtStart {
				t.Fatal("accepted payload gained history-control authority")
			}
			tracker := newCommandExecutionTracker(nil, "privacy", "host")
			entry, recordable := admission.startExecution(tracker, message, "printf stale", "/repo", "zsh", ignored)
			if entry.Command != test.raw || recordable != test.want {
				t.Fatalf("raw text or privacy decision changed: recordable=%v want=%v", recordable, test.want)
			}
			observed := false
			persisted, journaled := admission.persist(store, entry, ignored, func(bool, bool) { observed = true })
			if persisted != test.want || journaled || observed != test.want {
				t.Fatalf("incorrect durability/health: persisted=%v journaled=%v observed=%v", persisted, journaled, observed)
			}
			events, err := store.QueryAllHistoryEvents(t.Context())
			if err != nil || (len(events) == 1) != test.want || len(events) > 1 {
				t.Fatalf("unexpected canonical disclosure: count=%d error=%v", len(events), err)
			}
			changes, err := store.QueryHistoryChanges(t.Context(), 0, 20)
			if err != nil || (len(changes.Changes) > 0) != test.want {
				t.Fatal("publication disagreed with admission")
			}
			journal, err := historyRecoveryJournalPath()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatal("submission unexpectedly created a recovery journal")
			}
		})
	}
}

func TestHistorySubmissionAcceptedMultilineSurvivesReopenAndExactForgetPreview(t *testing.T) {
	original := integration.RichHistorySnapshot()
	t.Cleanup(func() { integration.PublishCanonicalHistory(original) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "history.db")
	store, err := scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const accepted = "\n\nprintf 'café 日本語'\nprintf 'VUJA_CMD_START\\n'  "
	sequence := bracketedPasteSequence(accepted)
	var filter terminalInputFilter
	var pasted bytes.Buffer
	active := false
	for start := 0; start < len(sequence); start += 3 {
		filtered := filter.Filter(sequence[start:min(start+3, len(sequence))])
		for len(filtered) > 0 {
			action := nextBracketedPasteAction(filtered, active)
			switch action.kind {
			case pasteActionStart:
				active = true
			case pasteActionData:
				pasted.Write(action.data)
			case pasteActionEnd:
				active = false
			default:
				t.Fatal("pasted content escaped into normal Enter handling")
			}
			filtered = filtered[action.consumed:]
		}
	}
	if active || pasted.String() != accepted {
		t.Fatal("fragmented paste changed before acceptance")
	}
	var session historyRecordingSession
	admission := session.capture()
	tracker := newCommandExecutionTracker(nil, "multiline", "host")
	message := commandStartMessage + ":COMMAND:" + strings.ReplaceAll(accepted, `\`, `\\`)
	entry, recordable := admission.startExecution(tracker, message, "printf 'café", "/repo", "zsh", false)
	if !recordable {
		t.Fatal("eligible accepted paste rejected")
	}
	if persisted, journaled := admission.persist(store, entry, false, nil); !persisted || journaled {
		t.Fatal("accepted paste was not durable")
	}
	completed := tracker.Finish(0)
	if completed.ID != entry.ID {
		t.Fatal("completion changed identity")
	}
	if ok, err := store.CompleteHistoryEvent(t.Context(), scoringHistoryEvent(completed)); err != nil || !ok {
		t.Fatalf("complete accepted paste: %v %v", ok, err)
	}
	// Adjacent prefix/suffix commands and the same text in another directory
	// must not turn exact forgetting into a broad match.
	for i, command := range []string{"printf 'café", accepted + "suffix", accepted} {
		cwd := "/repo"
		if i == 2 {
			cwd = "/other"
		}
		neighbor := tracker.Start(command, cwd, "zsh")
		if err := store.RecordHistoryEvent(t.Context(), scoringHistoryEvent(neighbor)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = scoring.NewFrecencyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	matches, err := store.QueryHistoryForgetMatches(t.Context(), "", accepted, "/repo")
	if err != nil || len(matches) != 1 || matches[0].EventKey != entry.ID || matches[0].Command != accepted || matches[0].State != "completed" {
		t.Fatalf("exact preview did not find the full completed submission: count=%d error=%v", len(matches), err)
	}
	after, err := store.QueryAllHistoryEvents(t.Context())
	if err != nil || len(after) != len(before) {
		t.Fatal("preview mutated history")
	}
	if _, err := publishCanonicalStoreHistory(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, recalled := range integration.RichHistorySnapshot() {
		if recalled.ID == entry.ID {
			found = recalled.Command == accepted && recalled.State == "completed"
		}
	}
	if !found {
		t.Fatal("reopened canonical publication lost exact multiline execution")
	}
}

func TestHistorySubmissionLegacyStartKeepsOtherShellCapture(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "fish"} {
		for _, message := range []string{commandStartMessage, commandStartIgnoreMessage} {
			match, ignored := parseCommandStartMessage(message)
			if !match {
				t.Fatal("legacy start rejected")
			}
			tracker := newCommandExecutionTracker(nil, shell, "host")
			entry, recordable := (historySubmissionAdmission{}).startExecution(tracker, message, "git status", "/repo", shell, ignored)
			if entry.Command != "git status" || recordable == ignored {
				t.Fatal("legacy capture or shell exclusion changed")
			}
		}
	}
}

func TestHistorySubmissionOversizedStartStillReachesAcknowledgement(t *testing.T) {
	const limit = 64
	for _, prefix := range []string{commandStartMessage + ":COMMAND:", commandStartIgnoreMessage + ":COMMAND:"} {
		for _, length := range []int{limit, limit + 1, shellMessageByteLimit + 1} {
			payload := strings.Repeat("x", length-len(prefix))
			reader := bufio.NewReaderSize(strings.NewReader(prefix+payload+"\x00VUJA_CMD_START:COMMAND:printf next\x00"), 8)
			frame, oversized, err := readShellMessage(reader, limit)
			if err != nil || oversized != (length > limit) {
				t.Fatalf("incorrect bounded frame: oversized=%v error=%v", oversized, err)
			}
			match, ignored := parseCommandStartMessage(string(frame))
			if !match {
				t.Fatal("accepted start was discarded before the acknowledgement path; shell would wait indefinitely")
			}
			tracker := newCommandExecutionTracker(nil, "oversized", "host")
			entry, admitted := (historySubmissionAdmission{}).startExecution(tracker, string(frame), "printf stale", "/repo", "zsh", ignored)
			if oversized && (entry.Command != "" || admitted || !ignored) {
				t.Fatal("oversized submission admitted partial or stale text")
			}
			next, oversized, err := readShellMessage(reader, limit)
			if err != nil || oversized {
				t.Fatal("oversized start consumed the next submission")
			}
			match, ignored = parseCommandStartMessage(string(next))
			if !match || ignored {
				t.Fatal("next command lost its own acknowledgement path")
			}
			second, admitted := (historySubmissionAdmission{}).startExecution(tracker, string(next), "printf stale", "/repo", "zsh", ignored)
			if !admitted || second.Command != "printf next" || second.ID == entry.ID {
				t.Fatal("oversized start corrupted the next accepted submission")
			}
		}
	}
}

func TestHistorySubmissionFeedbackUsesAcceptedCommand(t *testing.T) {
	const accepted = "git status --short"
	for _, test := range []struct {
		name, fallback, offered, selected string
		want                              map[string][4]int // accepted, typed, edited, dismissed
	}{
		{name: "typed prefix", fallback: "git status", want: map[string][4]int{accepted: {0, 1, 0, 0}}},
		{name: "empty editor", want: map[string][4]int{accepted: {0, 1, 0, 0}}},
		{name: "accepted suggestion", fallback: "git status", offered: accepted, selected: accepted, want: map[string][4]int{accepted: {1, 0, 0, 0}}},
		{name: "edited suggestion", fallback: "git status", offered: "git status", selected: "git status", want: map[string][4]int{"git status": {0, 0, 1, 0}}},
		{name: "matching offer", fallback: "git status", offered: accepted, want: map[string][4]int{accepted: {0, 1, 0, 0}}},
		{name: "dismissed offer", fallback: "git status", offered: "git status", want: map[string][4]int{accepted: {0, 1, 0, 0}, "git status": {0, 0, 0, 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			store, err := scoring.NewFrecencyStore(filepath.Join(home, "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var feedback suggestionFeedbackSession
			feedback.offer(test.offered)
			if test.selected != "" {
				feedback.accept(test.selected)
			}
			pending := feedback.capture()
			// These asynchronous interactions belong to a later prompt, not the
			// immutable evidence frozen for the accepted submission.
			feedback.offer("git branch")
			feedback.accept("git branch")
			var session historyRecordingSession
			admission := session.capture()
			tracker := newCommandExecutionTracker(nil, "feedback", "host")
			entry, ok := admission.startExecution(tracker, "VUJA_CMD_START:COMMAND:"+accepted, test.fallback, "/repo", "zsh", false)
			if !ok {
				t.Fatal("eligible accepted command rejected")
			}
			if persisted, journaled := admission.persist(store, entry, false, nil); !persisted || journaled {
				t.Fatal("canonical submission not persisted")
			}
			completed := tracker.Finish(0)
			if ok, err := store.CompleteHistoryEvent(t.Context(), scoringHistoryEvent(completed)); !ok || err != nil {
				t.Fatalf("completion: %v %v", ok, err)
			}
			recordSuggestionFeedbackToStore(t.Context(), store, pending.finish(entry.Command), "/repo", completed.ID)
			rows, err := store.QueryFeedback(t.Context(), "/repo", "", "git", 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(test.want) {
				t.Fatalf("feedback attributed to stale input: got %+v want %+v", rows, test.want)
			}
			for _, row := range rows {
				got := [4]int{row.Accepted, row.Typed, row.Edited, row.Dismissed}
				if want, exists := test.want[row.Cmd]; !exists || got != want {
					t.Fatalf("wrong accepted-command feedback: command=%q got=%v want=%v", row.Cmd, got, want)
				}
			}
		})
	}
}
