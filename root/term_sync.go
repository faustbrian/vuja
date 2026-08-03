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
	pending []byte
}

func (f *terminalInputFilter) Filter(input []byte) []byte {
	data := make([]byte, 0, len(f.pending)+len(input))
	data = append(data, f.pending...)
	data = append(data, input...)
	f.pending = nil

	filtered := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != '\x1b' || i+1 >= len(data) {
			filtered = append(filtered, data[i])
			i++
			continue
		}

		switch data[i+1] {
		case ']':
			end := oscSequenceEnd(data, i+2)
			if end == -1 {
				f.pending = append(f.pending, data[i:]...)
				return filtered
			}
			i = end
		case '[':
			end := csiSequenceEnd(data, i+2)
			if end == -1 {
				f.pending = append(f.pending, data[i:]...)
				return filtered
			}
			if isCursorPositionReport(data[i+2 : end+1]) {
				i = end + 1
				continue
			}
			filtered = append(filtered, data[i:end+1]...)
			i = end + 1
		default:
			filtered = append(filtered, data[i])
			i++
		}
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
