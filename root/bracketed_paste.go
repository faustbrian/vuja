package root

import (
	"bytes"
	"io"
)

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
)

type pasteActionKind uint8

const (
	pasteActionNormal pasteActionKind = iota
	pasteActionStart
	pasteActionData
	pasteActionEnd
)

type pasteAction struct {
	kind     pasteActionKind
	data     []byte
	consumed int
}

func nextBracketedPasteAction(input []byte, active bool) pasteAction {
	if len(input) == 0 {
		return pasteAction{}
	}
	if !active {
		if bytes.HasPrefix(input, bracketedPasteStart) {
			return pasteAction{kind: pasteActionStart, consumed: len(bracketedPasteStart)}
		}
		return pasteAction{kind: pasteActionNormal, consumed: 1}
	}

	end := bytes.Index(input, bracketedPasteEnd)
	if end < 0 {
		return pasteAction{kind: pasteActionData, data: input, consumed: len(input)}
	}
	if end > 0 {
		return pasteAction{kind: pasteActionData, data: input[:end], consumed: end}
	}
	return pasteAction{kind: pasteActionEnd, consumed: len(bracketedPasteEnd)}
}

func bracketedPasteSequence(payload string) []byte {
	sequence := make([]byte, 0, len(bracketedPasteStart)+len(payload)+len(bracketedPasteEnd))
	sequence = append(sequence, bracketedPasteStart...)
	sequence = append(sequence, payload...)
	sequence = append(sequence, bracketedPasteEnd...)
	return sequence
}

func writeBracketedPaste(writer io.Writer, payload string) error {
	remaining := bracketedPasteSequence(payload)
	for len(remaining) > 0 {
		written, err := writer.Write(remaining)
		if written > 0 {
			remaining = remaining[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func insertPastedText(buffer string, cursorOffset int, pasted string) (string, int) {
	runes := []rune(buffer)
	if cursorOffset < 0 {
		cursorOffset = 0
	}
	if cursorOffset > len(runes) {
		cursorOffset = len(runes)
	}
	position := len(runes) - cursorOffset
	inserted := []rune(pasted)
	result := make([]rune, 0, len(runes)+len(inserted))
	result = append(result, runes[:position]...)
	result = append(result, inserted...)
	result = append(result, runes[position:]...)
	return string(result), cursorOffset
}
