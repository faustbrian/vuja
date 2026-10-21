package root

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Independent wire oracle: decode the actual transmitted image and placements,
// not the renderer's desired boundary structs. A VT text screen cannot measure
// image geometry. Physical terminal appearance remains a separate acceptance.
var dividerWireToken = regexp.MustCompile("\x1b_G([^\x1b]*)\x1b\\\\|\x1b\\[([0-9]+);([0-9]+)H|\x1b\\[16t|\x1b\\[c|\x1b\\[\\?1049[hl]")

func dividerTextProjection(data []byte) []byte {
	return dividerWireToken.ReplaceAllFunc(data, func(token []byte) []byte {
		if bytes.HasPrefix(token, []byte("\x1b_G")) || string(token) == "\x1b[16t" || string(token) == "\x1b[c" {
			return nil // The independent wire oracle, not VT's reply pipe, owns these.
		}
		return token
	})
}

type dividerWireImage struct {
	w, h int
	data []byte
}
type dividerWirePlacement struct{ x, y, w, h int }
type dividerWire struct {
	read, x, y, serial, uploads, cw, ch int
	images                              map[int]dividerWireImage
	placements                          map[[2]int]dividerWirePlacement
}

func newDividerWire(cw, ch int) *dividerWire {
	return &dividerWire{cw: cw, ch: ch, serial: 100, images: map[int]dividerWireImage{}, placements: map[[2]int]dividerWirePlacement{}}
}

func (w *dividerWire) readOutput(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	data := out.String()[w.read:]
	w.read = out.Len()
	var replies strings.Builder
	for _, token := range dividerWireToken.FindAllStringSubmatch(data, -1) {
		switch {
		case token[2] != "":
			w.y, _ = strconv.Atoi(token[2])
			w.x, _ = strconv.Atoi(token[3])
		case token[0] == "\x1b[16t":
			fmt.Fprintf(&replies, "\x1b[6;%d;%dt", w.ch, w.cw)
		case token[0] == "\x1b[c":
			replies.WriteString("\x1b[?62;4c")
		case strings.HasPrefix(token[0], "\x1b[?1049"):
			clear(w.images)
			clear(w.placements)
		case token[1] != "":
			control, payload, _ := strings.Cut(token[1], ";")
			fields := map[string]string{}
			for _, field := range strings.Split(control, ",") {
				key, value, _ := strings.Cut(field, "=")
				fields[key] = value
			}
			number := func(key string) int { value, _ := strconv.Atoi(fields[key]); return value }
			switch fields["a"] {
			case "q":
				fmt.Fprintf(&replies, "\x1b_Gi=%d;OK\x1b\\", number("i"))
			case "t":
				if number("I") == 0 || fields["i"] != "" || fields["f"] != "24" || fields["o"] != "z" {
					t.Fatalf("upload must request a fresh terminal ID with compressed RGB pixels: %q", control)
				}
				compressed, err := base64.StdEncoding.DecodeString(payload)
				if err != nil || len(payload) > 4096 {
					t.Fatalf("invalid graphics payload/chunk: %v", err)
				}
				decoder, err := zlib.NewReader(bytes.NewReader(compressed))
				if err != nil {
					t.Fatal(err)
				}
				pixels, err := io.ReadAll(io.LimitReader(decoder, 1<<20+1))
				decoder.Close()
				if err != nil || len(pixels) != number("s")*number("v")*3 {
					t.Fatalf("RGB dimensions disagree with pixel bytes: %v", err)
				}
				w.serial++
				w.uploads++
				w.images[w.serial] = dividerWireImage{number("s"), number("v"), pixels}
				fmt.Fprintf(&replies, "\x1b_Gi=%d,I=%d;OK\x1b\\", w.serial, number("I"))
			case "p":
				id, placement := number("i"), number("p")
				image, exists := w.images[id]
				if !exists || placement == 0 || fields["C"] != "1" || fields["q"] != "2" || fields["c"] != "" || fields["r"] != "" || fields["X"] != "" || fields["Y"] != "" {
					t.Fatalf("placement must use native pixels without scaling, offset or cursor movement: %q", control)
				}
				if number("w") > image.w || number("h") != 1 || image.h != 1 {
					t.Fatalf("placement crops outside its one-pixel image: %q", control)
				}
				w.placements[[2]int{id, placement}] = dividerWirePlacement{(w.x - 1) * w.cw, (w.y - 1) * w.ch, number("w"), number("h")}
			case "d":
				id := number("i")
				if id == 0 || fields["I"] != "" || fields["d"] != "i" && fields["d"] != "I" || fields["q"] != "2" {
					t.Fatalf("cleanup must address only a terminal-allocated Vuja image: %q", control)
				}
				if _, exists := w.images[id]; !exists {
					t.Fatalf("cleanup addressed an image not owned by Vuja: %d", id)
				}
				for key := range w.placements {
					if key[0] == id {
						delete(w.placements, key)
					}
				}
				if fields["d"] == "I" {
					delete(w.images, id)
				}
			default:
				t.Fatalf("unexpected graphics operation: %q", control)
			}
		}
	}
	return replies.String()
}

func (w *dividerWire) settle(t *testing.T, c *terminalCompositor, out *bytes.Buffer) {
	t.Helper()
	var filter terminalInputFilter
	for range 12 {
		reply := w.readOutput(t, out)
		if reply == "" {
			return
		}
		if forwarded := filter.FilterInput([]byte(reply), c, false, true); len(forwarded) != 0 {
			t.Fatalf("owned graphics report reached the shell: %q", forwarded)
		}
	}
	t.Fatal("graphics negotiation/redraw did not converge")
}

func TestInteractiveBlocksPixelDividerWireGeometry(t *testing.T) {
	for _, surface := range []string{"full-width", "content-width"} {
		for _, mode := range []string{"command", "snapshot"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\nsurface-width=%q\ncompleted-command=%q\n", surface, mode))
				c.EnablePixelDividers()
				blocksExecution(c, "bad-command", "bad-output\r\n", "1")
				blocksExecution(c, "next-command", "café 日本語\r\n", "0")
				blocksPrompt(c, "pending-input")
				wire := newDividerWire(9, 19)
				wire.settle(t, c, out)
				for _, geometry := range [][4]int{{60, 24, 9, 19}, {42, 28, 11, 23}, {42, 28, 10, 22}} {
					width, height := geometry[0], geometry[1]
					if geometry != [4]int{60, 24, 9, 19} {
						wire.cw, wire.ch = geometry[2], geometry[3]
						c.Resize(width, height)
						wire.settle(t, c, out)
					}
					lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), width, height))
					y := terminalLineIndex(lines, "next-command", 0)
					if len(wire.images) != 1 || len(wire.placements) != 1 || y < 1 {
						t.Fatalf("want one shared image and one inter-block boundary, got %d/%d: %q", len(wire.images), len(wire.placements), lines)
					}
					for _, image := range wire.images {
						if image.w != width*wire.cw || image.h != 1 {
							t.Fatalf("stale/incorrect physical width: %+v", image)
						}
						for i := 0; i < len(image.data); i += 3 {
							if !bytes.Equal(image.data[i:i+3], []byte{74, 74, 74}) {
								t.Fatalf("strip must be neutral muted grey, not severity tint: %v", image.data[i:i+3])
							}
						}
					}
					wantWidth := width * wire.cw
					if surface == "content-width" {
						wantWidth = 32 * wire.cw
					}
					for _, p := range wire.placements {
						if p != (dividerWirePlacement{0, y * wire.ch, wantWidth, 1}) {
							t.Fatalf("strip must touch the exact top boundary and both edges, got %+v, header y=%d", p, y)
						}
					}
					previous := "bad-output"
					if mode == "snapshot" {
						previous = "exit 1"
					}
					if terminalLineIndex(lines, previous, 0)+1 != y {
						t.Fatalf("graphics introduced a text row or visible padding: %q", lines)
					}
				}
			})
		}
	}
}

func TestDividerReportsFramingAndOwnership(t *testing.T) {
	for _, report := range []string{"\x1b_Gi=555;OK\x1b\\", "\x1b_Gi=777,I=666;OK\x1b\\", "\x1b[6;19;9t", "\x1b[?62;4c"} {
		for split := 0; split <= len(report); split++ {
			c, _ := blocksFixture(t, "")
			c.dividerPixels = pixelDividers{enabled: true, probe: 555, upload: 666}
			var f terminalInputFilter
			got := f.FilterInput([]byte("before"+report[:split]), c, false, true)
			got = append(got, f.FilterInput([]byte(report[split:]+"after"), c, false, false)...)
			if string(got) != "beforeafter" {
				t.Fatalf("split %d of %q leaked/suppressed adjacent input: %q", split, report, got)
			}
		}
	}
	c, _ := blocksFixture(t, "")
	c.dividerPixels = pixelDividers{enabled: true, probe: 555, upload: 666}
	var f terminalInputFilter
	unowned := "\x1b_Gi=999,I=888;OK\x1b\\"
	if got := f.FilterInput([]byte(unowned), c, false, false); string(got) != unowned {
		t.Fatalf("application's graphics response was stolen: %q", got)
	}
	paste := "\x1b[200~\x1b_Gi=555;OK\x1b\\\x1b[6;19;9t\x1b[201~"
	for split := 0; split <= len(paste); split++ {
		var p terminalInputFilter
		got := p.FilterInput([]byte(paste[:split]), c, false, true)
		got = append(got, p.FilterInput([]byte(paste[split:]), c, false, true)...)
		if string(got) != paste {
			t.Fatal("literal pasted protocol was consumed as a terminal response")
		}
	}
	oversized := "\x1b_Gi=555;" + strings.Repeat("x", terminalModelSequenceLimit+1)
	if got := f.FilterInput([]byte(oversized), c, false, true); len(got) != 0 || len(f.pending) != 0 {
		t.Fatal("malformed owned report leaked or grew retained framing")
	}
	if got := f.FilterInput([]byte("typed"), c, false, true); string(got) != "typed" {
		t.Fatal("malformed report swallowed subsequent typing")
	}
}

func TestInteractiveBlocksPixelDividerLifecycle(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "first-output\r\n", "1")
	blocksExecution(c, "second", "second-output\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(9, 19)
	wire.settle(t, c, out)
	uploads := wire.uploads
	for range 100 {
		c.mu.Lock()
		c.viewport.dirty = true
		c.renderOutputViewport()
		c.mu.Unlock()
		wire.settle(t, c, out)
	}
	if wire.uploads != uploads || len(wire.images) != 1 || len(wire.placements) != 1 {
		t.Fatal("redraws leaked image resources")
	}
	visible := true
	c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
	c.ComposeUI(func() []byte { return nil })
	wire.settle(t, c, out)
	if len(wire.placements) != 0 {
		t.Fatal("divider floated over the suggestion overlay")
	}
	visible = false
	c.ComposeUI(func() []byte { return nil })
	wire.settle(t, c, out)
	if len(wire.placements) != 1 {
		t.Fatal("closing the overlay failed to restore the boundary")
	}
	var filter terminalInputFilter
	filter.FilterInput([]byte("\x1b[18~"), c, false, true)
	wire.settle(t, c, out)
	if len(wire.placements) != 0 {
		t.Fatal("divider floated over semantic search")
	}
	filter.FilterInput([]byte{3}, c, false, true)
	wire.settle(t, c, out)
	c.SetChatboxConfig(func() terminalChatboxConfig { cfg := c.chatboxConfig; cfg.BlockDividers = false; return cfg }())
	wire.settle(t, c, out)
	if len(wire.images) != 0 || len(wire.placements) != 0 {
		t.Fatal("disabling dividers left graphics behind")
	}
	c.Close()
	wire.settle(t, c, out)
}

func TestInteractiveBlocksPixelDividerUnsupportedGeometryStaysFlush(t *testing.T) {
	for _, response := range []string{"\x1b[?62c", "\x1b[6;0;0t\x1b[?62c", "\x1b[6;65535;65535t\x1b[?62c", "\x1b[6;broken;9t\x1b[?62c"} {
		c, out := blocksFixture(t, "")
		c.EnablePixelDividers()
		blocksExecution(c, "first", "out\r\n", "1")
		blocksExecution(c, "second", "out\r\n", "0")
		blocksPrompt(c, "input")
		d := &c.dividerPixels
		var f terminalInputFilter
		f.FilterInput([]byte(fmt.Sprintf("\x1b_Gi=%d;OK\x1b\\", d.probe)+response), c, false, true)
		for range 10 {
			c.ComposeUI(func() []byte { return nil })
		}
		if strings.Contains(out.String(), "\x1b_Ga=t,") {
			t.Fatal("missing/unsafe geometry allocated an image")
		}
		lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24))
		if terminalLineIndex(lines, "second", 0) != terminalLineIndex(lines, "exit 1", 0)+1 {
			t.Fatal("unsupported terminal fell back to a padded glyph row")
		}
	}
}

func TestInteractiveBlocksPixelDividerLateAllocationOwnership(t *testing.T) {
	for _, transition := range []string{"resize", "native", "close"} {
		t.Run(transition, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			c.EnablePixelDividers()
			blocksExecution(c, "first", "out\r\n", "0")
			blocksExecution(c, "second", "out\r\n", "0")
			blocksPrompt(c, "input")
			var f terminalInputFilter
			f.FilterInput([]byte(fmt.Sprintf("\x1b_Gi=%d;OK\x1b\\\x1b[6;19;9t\x1b[?62c", c.dividerPixels.probe)), c, false, true)
			number := c.dividerPixels.upload
			if number == 0 {
				t.Fatal("fixture did not leave an allocation awaiting its terminal ID")
			}
			switch transition {
			case "resize":
				c.Resize(42, 28)
			case "native":
				c.mu.Lock()
				c.suspendOutputViewport()
				c.mu.Unlock()
			case "close":
				c.Close()
			}
			before := out.Len()
			if got := f.FilterInput([]byte(fmt.Sprintf("\x1b_Gi=101,I=%d;OK\x1b\\", number)), c, false, false); len(got) != 0 {
				t.Fatalf("late allocation reply leaked: %q", got)
			}
			deleted := strings.Contains(out.String()[before:], "\x1b_Ga=d,d=I,i=101,q=2")
			if deleted != (transition == "resize") {
				t.Fatal("same-screen obsolete image must be freed, but a late ID after handoff must not delete an application's reused ID")
			}
		})
	}
}

func TestInteractiveBlocksPixelDividerDoesNotAlterSelectedText(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\ncompleted-command='command'\n")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "first\r\n", "1")
	blocksExecution(c, "second", "copied café 日本語\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(9, 19)
	wire.settle(t, c, out)
	lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24))
	y := terminalLineIndex(lines, "copied", 0)
	cardMouse(t, c, 0, 2, y, true)
	cardMouse(t, c, 32, 21, y, true)
	cardMouse(t, c, 0, 21, y, false)
	wire.settle(t, c, out)
	start := out.Len()
	var filter terminalInputFilter
	if got := filter.FilterInput([]byte{3}, c, false, true); len(got) != 0 {
		t.Fatal("plain-drag selection became an interrupt with graphics enabled")
	}
	assertExecutionClipboard(t, out.String()[start:], "copied café 日本語")
	wire.settle(t, c, out)
	c.mu.Lock()
	c.clearOutputViewport(3)
	c.renderOutputViewport()
	c.mu.Unlock()
	wire.settle(t, c, out)
	if len(wire.images) != 0 || len(wire.placements) != 0 {
		t.Fatal("clear left a graphical boundary floating over the empty pane")
	}
}

func TestInteractiveBlocksPixelDividerOnlyBetweenExecutionCards(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.EnablePixelDividers()
	blocksPrompt(c, "")
	c.mu.Lock()
	c.viewport.write([]byte("startup-not-an-execution\r\n"))
	c.mu.Unlock()
	blocksExecution(c, "first-execution", "out\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(9, 19)
	wire.settle(t, c, out)
	lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24))
	if terminalLineIndex(lines, "startup-not-an-execution", 0) < 0 {
		t.Fatal("fixture lost its unowned startup text")
	}
	if len(wire.placements) != 0 {
		t.Fatal("startup text must not create an inter-execution boundary before the first card")
	}
	blocksExecution(c, "second-execution", "out\r\n", "0")
	blocksPrompt(c, "input")
	wire.settle(t, c, out)
	if len(wire.placements) != 1 {
		t.Fatal("two execution cards must have exactly one boundary")
	}
}

func TestDividerReportAfterSearchClosePreservesAdjacentInput(t *testing.T) {
	for _, next := range []string{"typed\r", "\x1b[200~pasted\r\ntext\x1b[201~"} {
		c, out := blocksFixture(t, "")
		c.EnablePixelDividers()
		blocksExecution(c, "first", "out\r\n", "0")
		blocksExecution(c, "second", "out\r\n", "0")
		blocksPrompt(c, "input")
		var f terminalInputFilter
		probe := fmt.Sprintf("\x1b_Gi=%d;OK\x1b\\\x1b[6;19;9t\x1b[?62c", c.dividerPixels.probe)
		if got := f.FilterInput([]byte(probe), c, false, true); len(got) != 0 || c.dividerPixels.upload == 0 {
			t.Fatal("fixture must leave a genuine upload reply outstanding")
		}
		f.FilterInput([]byte("\x1b[18~"), c, false, true)
		if !c.OutputSearchActive() {
			t.Fatal("fixture did not open modal output search")
		}
		report := fmt.Sprintf("\x1b_Gi=101,I=%d;OK\x1b\\", c.dividerPixels.upload)
		split := len(report) - 3
		if got := f.FilterInput([]byte(report[:split]), c, false, true); len(got) != 0 || !f.pendingSearch {
			t.Fatal("fixture did not retain a search-owned partial report")
		}
		c.Resize(60, 1)
		if c.OutputSearchActive() {
			t.Fatal("geometry handoff failed to close modal search")
		}
		events := f.FilterEvents([]byte(report[split:]+next), c, false, false)
		if got := string(flattenTerminalInput(events)); got != next {
			t.Fatalf("completed report swallowed adjacent input after modal close: got %q want %q", got, next)
		}
		for _, event := range events {
			if event.shell {
				t.Fatal("new native input retained the old search/shell owner")
			}
		}
		if strings.Contains(out.String(), "\x1b]52;") {
			t.Fatal("report handoff unexpectedly copied search text")
		}
	}
}
