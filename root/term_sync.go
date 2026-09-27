package root

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"github.com/muesli/termenv"
)

func detectDarkBackground() bool {
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if len(parts) > 1 {
		if index, err := strconv.Atoi(parts[len(parts)-1]); err == nil && index >= 0 && index <= 255 {
			_, _, lightness := termenv.ConvertToRGB(termenv.ANSIColor(index)).Hsl()
			return lightness < 0.5
		}
	}
	return true
}

type terminalInputFilter struct {
	pending      []byte
	paste        bool
	pendingOwner terminalInputOwner
	pasteOwner   terminalInputOwner
}

type terminalInputOwner struct {
	shell       bool
	viewport    bool
	suggestions bool
}

type ownedTerminalInput struct {
	data []byte
	terminalInputOwner
}

func flattenTerminalInput(events []ownedTerminalInput) []byte {
	var data []byte
	for _, event := range events {
		data = append(data, event.data...)
	}
	return data
}

func (f *terminalInputFilter) Filter(input []byte) []byte {
	return flattenTerminalInput(f.frame(input, terminalInputOwner{shell: true}, func(data []byte, _ terminalInputOwner) []byte { return stripShellReport(data) }))
}

func stripShellReport(sequence []byte) []byte {
	if bytes.HasPrefix(sequence, []byte("\x1b]")) || (bytes.HasPrefix(sequence, []byte("\x1b[")) && isCursorPositionReport(sequence[2:])) {
		return nil
	}
	return sequence
}

func (f *terminalInputFilter) FilterInput(input []byte, display *terminalCompositor, suggestions, shellOwned bool) []byte {
	return flattenTerminalInput(f.FilterEvents(input, display, suggestions, shellOwned))
}

func (f *terminalInputFilter) FilterEvents(input []byte, display *terminalCompositor, suggestions, shellOwned bool) []ownedTerminalInput {
	owner := terminalInputOwner{shell: shellOwned, viewport: display.ViewportNavigationActive(), suggestions: suggestions}
	return f.frame(input, owner, func(sequence []byte, owner terminalInputOwner) []byte {
		if owner.shell {
			sequence = stripShellReport(sequence)
		}
		if owner.viewport && display.HandleViewportInput(sequence, owner.suggestions) {
			return nil
		}
		return sequence
	})
}

func (f *terminalInputFilter) EscapePending() bool {
	return !f.paste && bytes.Equal(f.pending, []byte("\x1b"))
}

func (f *terminalInputFilter) FlushEscape(display *terminalCompositor, _ bool) []byte {
	if !f.EscapePending() {
		return nil
	}
	f.pending = nil
	if !f.pendingOwner.viewport {
		return []byte("\x1b")
	}
	return display.FilterViewportInput([]byte("\x1b"), f.pendingOwner.suggestions)
}

// One stream boundary recognizes paste regions before report suppression or
// key ownership. Only incomplete framing is retained, never paste contents.
func (f *terminalInputFilter) frame(input []byte, currentOwner terminalInputOwner, normal func([]byte, terminalInputOwner) []byte) []ownedTerminalInput {
	pendingLen, pendingOwner := len(f.pending), f.pendingOwner
	data := make([]byte, 0, len(f.pending)+len(input))
	data = append(data, f.pending...)
	data = append(data, input...)
	f.pending = nil

	var filtered []ownedTerminalInput
	emit := func(data []byte, owner terminalInputOwner) {
		if len(data) == 0 {
			return
		}
		if len(filtered) > 0 && filtered[len(filtered)-1].terminalInputOwner == owner {
			filtered[len(filtered)-1].data = append(filtered[len(filtered)-1].data, data...)
		} else {
			filtered = append(filtered, ownedTerminalInput{append([]byte(nil), data...), owner})
		}
	}
	for i := 0; i < len(data); {
		owner := currentOwner
		if i < pendingLen {
			owner = pendingOwner
		}
		if f.paste {
			if end := bytes.Index(data[i:], bracketedPasteEnd); end >= 0 {
				end += i + len(bracketedPasteEnd)
				emit(data[i:end], f.pasteOwner)
				i = end
				f.paste = false
				continue
			}
			end := len(data)
			for length := min(end-i, len(bracketedPasteEnd)-1); length > 0; length-- {
				if bytes.Equal(data[end-length:], bracketedPasteEnd[:length]) {
					f.pending = append(f.pending, data[end-length:]...)
					f.pendingOwner = f.pasteOwner
					end -= length
					break
				}
			}
			emit(data[i:end], f.pasteOwner)
			return filtered
		}
		if data[i] != '\x1b' {
			end := i + 1
			for end < len(data) && data[end] != '\x1b' {
				end++
			}
			emit(normal(data[i:end], owner), owner)
			i = end
			continue
		}
		end := -1
		if i+1 >= len(data) {
			f.pending = append(f.pending, data[i:]...)
			f.pendingOwner = owner
			return filtered
		}
		switch data[i+1] {
		case ']':
			end = oscSequenceEnd(data, i+2)
		case '[':
			if final := csiSequenceEnd(data, i+2); final >= 0 {
				end = final + 1
			}
		case 'O':
			if i+2 < len(data) {
				end = i + 3
			}
		default:
			end = i + 2
		}
		if end < 0 {
			if len(data)-i > terminalModelSequenceLimit {
				// Malformed input cannot grow retained framing without bound.
				emit(data[i:], owner)
				return filtered
			}
			f.pending = append(f.pending, data[i:]...)
			f.pendingOwner = owner
			return filtered
		}
		sequence := data[i:end]
		if bytes.Equal(sequence, bracketedPasteStart) {
			f.paste = true
			f.pasteOwner = owner
			emit(sequence, owner)
		} else {
			emit(normal(sequence, owner), owner)
		}
		i = end
	}
	return filtered
}

func oscSequenceEnd(data []byte, start int) int {
	for i := start; i < len(data); i++ {
		if data[i] == '\x07' {
			return i + 1
		}
		if data[i] == '\x1b' && i+1 < len(data) && data[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}

func csiSequenceEnd(data []byte, start int) int {
	for i := start; i < len(data); i++ {
		if data[i] >= 0x40 && data[i] <= 0x7e {
			return i
		}
	}
	return -1
}

func isCursorPositionReport(sequence []byte) bool {
	if len(sequence) < 4 || sequence[len(sequence)-1] != 'R' {
		return false
	}
	params := sequence[:len(sequence)-1]
	if !bytes.ContainsRune(params, ';') {
		return false
	}
	for _, value := range params {
		if (value < '0' || value > '9') && value != ';' {
			return false
		}
	}
	return true
}

func consumeNextTokenAcceptance(sequenceLength int, ghostText string) bool {
	return sequenceLength > 0 && ghostText != ""
}
