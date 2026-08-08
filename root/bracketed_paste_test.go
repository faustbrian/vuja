package root

import (
	"bytes"
	"testing"
)

func TestBracketedPasteActionsKeepMultilinePayloadOutOfNormalInput(t *testing.T) {
	input := []byte("\x1b[200~echo one\necho two\x1b[201~")
	active := false
	var pasted []byte
	var kinds []pasteActionKind
	for len(input) > 0 {
		action := nextBracketedPasteAction(input, active)
		if action.consumed == 0 {
			t.Fatal("expected paste action to consume input")
		}
		kinds = append(kinds, action.kind)
		if action.kind == pasteActionStart {
			active = true
		} else if action.kind == pasteActionData {
			pasted = append(pasted, action.data...)
		} else if action.kind == pasteActionEnd {
			active = false
		}
		input = input[action.consumed:]
	}

	if len(kinds) != 3 || kinds[0] != pasteActionStart || kinds[1] != pasteActionData || kinds[2] != pasteActionEnd {
		t.Fatalf("unexpected action sequence: %#v", kinds)
	}
	if got := string(pasted); got != "echo one\necho two" {
		t.Fatalf("expected multiline paste to remain one payload, got %q", got)
	}
}

func TestTerminalInputFilterBuffersFragmentedPasteMarkers(t *testing.T) {
	var filter terminalInputFilter
	if got := filter.Filter([]byte("\x1b[20")); len(got) != 0 {
		t.Fatalf("expected partial marker to remain buffered, got %q", got)
	}
	got := filter.Filter([]byte("0~first\nsecond\x1b[201~"))
	if action := nextBracketedPasteAction(got, false); action.kind != pasteActionStart {
		t.Fatalf("expected reconstructed paste start, got %#v", action)
	}
}

func TestFragmentedMultilinePasteRemainsOneLiteralPayload(t *testing.T) {
	payload := `
  cd /Users/brian/Developer/vuja

  go test ./root ./integration ./spec ./internal/scoring
  go test -run '^$' \
    -bench 'BenchmarkMergeResults(Warm|FastCached)|BenchmarkSuggestionPipelineImmediateExtension' \
    -benchmem -count=5 ./root
  just test
  just install zsh
`
	sequence := bracketedPasteSequence(payload)
	chunks := [][]byte{
		sequence[:3],
		sequence[3:37],
		sequence[37 : len(sequence)-4],
		sequence[len(sequence)-4:],
	}

	var filter terminalInputFilter
	active := false
	var pasted bytes.Buffer
	var normal bytes.Buffer
	for _, chunk := range chunks {
		filtered := filter.Filter(chunk)
		for len(filtered) > 0 {
			action := nextBracketedPasteAction(filtered, active)
			switch action.kind {
			case pasteActionStart:
				active = true
			case pasteActionData:
				_, _ = pasted.Write(action.data)
			case pasteActionEnd:
				active = false
			case pasteActionNormal:
				_ = normal.WriteByte(filtered[0])
			}
			filtered = filtered[action.consumed:]
		}
	}

	if active {
		t.Fatal("expected completed paste")
	}
	if normal.Len() != 0 {
		t.Fatalf("paste bytes escaped as normal input: %q", normal.String())
	}
	if pasted.String() != payload {
		t.Fatalf("pasted payload changed:\nwant %q\n got %q", payload, pasted.String())
	}
}

func TestInsertPastedTextPreservesCursorAndNewlines(t *testing.T) {
	buffer, offset := insertPastedText("echo tail", 4, "one\ntwo ")
	if buffer != "echo one\ntwo tail" {
		t.Fatalf("unexpected pasted buffer %q", buffer)
	}
	if offset != 4 {
		t.Fatalf("expected cursor offset to remain after inserted text, got %d", offset)
	}
}

func TestBracketedPasteSequenceWritesOneCompleteShellOperation(t *testing.T) {
	got := string(bracketedPasteSequence("first\nsecond"))
	if got != "\x1b[200~first\nsecond\x1b[201~" {
		t.Fatalf("unexpected bracketed paste sequence %q", got)
	}
}

func TestWriteBracketedPasteHandlesShortWrites(t *testing.T) {
	writer := &shortPasteWriter{limit: 4}
	if err := writeBracketedPaste(writer, "first\nsecond"); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != "\x1b[200~first\nsecond\x1b[201~" {
		t.Fatalf("unexpected bracketed paste write %q", got)
	}
}

type shortPasteWriter struct {
	bytes.Buffer
	limit int
}

func (w *shortPasteWriter) Write(data []byte) (int, error) {
	if len(data) > w.limit {
		data = data[:w.limit]
	}
	return w.Buffer.Write(data)
}
