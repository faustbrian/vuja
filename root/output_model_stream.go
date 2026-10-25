package root

import (
	"fmt"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/vt"
)

const outputModelGraphemeLimit = 512
const outputModelGraphemeNotice = "[Unicode cluster truncated]"

// x/vt flushes a grapheme at each Write boundary. This adapter joins only
// contiguous printable text to the preceding model cell. The SDK remains the
// owner of controls, cursor movement, styles, hyperlinks and scrollback; the
// shared ANSI decoder classifies tokens without retaining control payloads.
type outputModelStream struct {
	state     byte
	tail      []byte
	last      outputModelTextCell
	anchored  bool
	shortened bool
	losses    uint64
}

type outputModelTextCell struct {
	cell   uv.Cell
	place  uv.Position
	cursor uv.Position
}

// The SDK owns reflow, scroll margins and pending wrap. Sending DECSTBM/CUP
// after resize would destroy its retained logical cursor and pending wrap.
func (s *outputModelStream) resize(model *vt.Emulator, width, height int) {
	last, shortened := s.last, s.shortened
	anchored := s.anchored && model.CursorPosition() == last.cursor
	if anchored {
		cell := model.CellAt(last.place.X, last.place.Y)
		anchored = cell != nil && cell.Equal(&last.cell)
	}
	s.clearAnchor()
	s.shortened = shortened
	if !anchored {
		model.Resize(width, height)
		return
	}
	place, ok := model.ResizeWithAnchor(width, height, last.place)
	if ok {
		if cell := model.CellAt(place.X, place.Y); cell != nil && cell.Equal(&last.cell) {
			s.last = outputModelTextCell{cell: *cell, place: place, cursor: model.CursorPosition()}
			s.anchored = true
		}
	}
}

func (s *outputModelStream) write(model *vt.Emulator, data []byte) {
	if len(s.tail) > 0 {
		data = append(s.tail, data...)
	}
	s.tail = trailingIncompleteUTF8(data)
	data = data[:len(data)-len(s.tail)]
	for len(data) > 0 {
		if s.state == ansi.NormalState && data[0] >= ' ' && data[0] < 0x7f {
			// Keep ASCII bulk writes on the SDK's fast path. Only the final
			// character needs an anchor for a subsequent Unicode extension.
			n := 1
			for n < len(data) && data[n] >= ' ' && data[n] < 0x7f {
				n++
			}
			if n > 1 {
				_, _ = model.Write(data[:n-1])
			}
			s.writeText(model, data[n-1:n], 1)
			data = data[n:]
			continue
		}
		sequence, width, n, state := ansi.DecodeSequence(data, s.state, nil)
		if n == 0 {
			// Invalid UTF-8 still belongs to the SDK; never stall the stream.
			sequence, n, state = data[:1], 1, ansi.NormalState
		}
		text := s.state == ansi.NormalState && sequence[0] >= utf8.RuneSelf && utf8.Valid(sequence)
		s.state = state
		if text {
			n = s.writeText(model, sequence, width)
		} else {
			_, _ = model.Write(sequence)
			// SGR changes the pen, not the text position. A following mark
			// still belongs to the preceding grapheme and its base-cell style.
			if !ansi.HasCsiPrefix(sequence) || sequence[len(sequence)-1] != 'm' {
				s.clearAnchor()
			}
		}
		data = data[n:]
	}
}

func (s *outputModelStream) writeText(model *vt.Emulator, text []byte, width int) int {
	if s.shortened && width == 0 {
		// Zero-width continuations of a shortened cell must not accumulate
		// again or erase the disclosure. Controls and positive-width text
		// end this state; the original bytes still belong to native output.
		return len(text)
	}
	s.shortened = false
	if last := s.last; s.anchored && model.CursorPosition() == last.cursor {
		cell := model.CellAt(last.place.X, last.place.Y)
		if cell != nil && cell.Equal(&last.cell) {
			// Segment only a bounded probe, even when this read contains one
			// enormous cluster. Both the cached cell and the probe are bounded.
			probe := min(len(text), outputModelGraphemeLimit)
			for probe < len(text) && !utf8.RuneStart(text[probe]) {
				probe--
			}
			cluster, joinedWidth := ansi.FirstGraphemeCluster(last.cell.Content+string(text[:probe]), ansi.GraphemeWidth)
			if extension := len(cluster) - len(last.cell.Content); extension > 0 {
				if len(cluster) > outputModelGraphemeLimit || extension == probe && probe < len(text) {
					fmt.Fprintf(model, "\x1b[%d;%dH", last.place.Y+1, last.place.X+1)
					model.SetCell(last.place.X, last.place.Y, nil)
					s.writeShortened(model)
					return len(text)
				}
				joined := last.cell
				joined.Content, joined.Width = cluster, joinedWidth
				if joinedWidth == last.cell.Width {
					model.SetCell(last.place.X, last.place.Y, &joined)
					s.last.cell = joined
				} else {
					// Rewriting through the SDK recalculates cursor/wrap geometry.
					// These controls affect only the private shadow model.
					fmt.Fprintf(model, "\x1b[%d;%dH", last.place.Y+1, last.place.X+1)
					model.SetCell(last.place.X, last.place.Y, nil)
					s.clearAnchor()
					s.writeText(model, []byte(cluster), joinedWidth)
					if s.anchored {
						joined.Content = s.last.cell.Content
						model.SetCell(s.last.place.X, s.last.place.Y, &joined)
						s.last.cell = joined
					}
				}
				return extension
			}
		}
	}
	if len(text) > outputModelGraphemeLimit {
		s.writeShortened(model)
		return len(text)
	}
	before := model.CursorPosition()
	_, _ = model.Write(text)
	after := model.CursorPosition()
	place := before
	if after.Y != before.Y || after.X < before.X {
		place = uv.Pos(0, after.Y)
	}
	s.clearAnchor()
	if cell := model.CellAt(place.X, place.Y); cell != nil && cell.Width > 0 && cell.Content == string(text) {
		s.last = outputModelTextCell{cell: *cell, place: place, cursor: after}
		s.anchored = true
	}
	return len(text)
}

func (s *outputModelStream) writeShortened(model *vt.Emulator) {
	s.clearAnchor()
	_, _ = model.Write([]byte(outputModelGraphemeNotice))
	s.shortened = true
	// Unlike the transient grapheme anchor, actual-loss events survive later
	// text and controls so each execution can classify its own truncation.
	s.losses++
}

func (s *outputModelStream) clearAnchor() {
	s.last = outputModelTextCell{}
	s.anchored = false
	s.shortened = false
}
