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

	"github.com/faustbrian/vuja/internal/config"
)

// Independent wire oracle: decode the actual transmitted image and placements,
// not the renderer's desired boundary structs. A VT text screen cannot measure
// image geometry. Physical terminal appearance remains a separate acceptance.
var dividerWireToken = regexp.MustCompile("\x1b_G([^\x1b]*)\x1b\\\\|\x1b\\[([0-9]+);([0-9]+)H|\x1b\\[16t|\x1b\\[14t|\x1b\\]1337;ReportCellSize\x1b\\\\|\x1b\\[c|\x1b\\[\\?1049[hl]")

func dividerTextProjection(data []byte) []byte {
	return dividerWireToken.ReplaceAllFunc(data, func(token []byte) []byte {
		if bytes.HasPrefix(token, []byte("\x1b_G")) || bytes.HasPrefix(token, []byte("\x1b]1337;")) || string(token) == "\x1b[16t" || string(token) == "\x1b[14t" || string(token) == "\x1b[c" {
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
	iterm                               bool
	frameWidth                          int
	images                              map[int]dividerWireImage
	placements                          map[[2]int]dividerWirePlacement
	layers                              map[[2]int]int
}

func newDividerWire(cw, ch int) *dividerWire {
	return &dividerWire{cw: cw, ch: ch, serial: 100, images: map[int]dividerWireImage{}, placements: map[[2]int]dividerWirePlacement{}, layers: map[[2]int]int{}}
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
			if !w.iterm {
				fmt.Fprintf(&replies, "\x1b[6;%d;%dt", w.ch, w.cw)
			}
		case token[0] == "\x1b]1337;ReportCellSize\x1b\\":
			if w.iterm {
				// Captured iTerm2 3.7.3 reply; CSI 16 t is unanswered.
				fmt.Fprintf(&replies, "\x1b]1337;ReportCellSize=%.1f;%.1f;2.0\x1b\\", float64(w.ch)/2, float64(w.cw)/2)
			}
		case token[0] == "\x1b[14t" && w.iterm && w.frameWidth > 0:
			fmt.Fprintf(&replies, "\x1b[4;927;%dt", w.frameWidth)
		case token[0] == "\x1b[c":
			replies.WriteString("\x1b[?62;4c")
		case strings.HasPrefix(token[0], "\x1b[?1049"):
			clear(w.images)
			clear(w.placements)
			clear(w.layers)
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
				_ = decoder.Close()
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
				width, height := number("w"), number("h")
				if w.iterm {
					// iTerm 3.7.3's pixelSize uses uploaded metadata before the
					// source crop (KittyImageController.swift:173-195). The
					// native probe confirmed a 1050px source with w=200 still
					// draws 1050px; a native 200px source draws 200px.
					width, height = image.w, image.h
				}
				w.placements[[2]int{id, placement}] = dividerWirePlacement{(w.x - 1) * w.cw, (w.y - 1) * w.ch, width, height}
				w.layers[[2]int{id, placement}] = number("z")
			case "d":
				id := number("i")
				if id == 0 || fields["I"] != "" || fields["d"] != "i" && fields["d"] != "I" || fields["q"] != "2" {
					t.Fatalf("cleanup must address only a terminal-allocated Vuja image: %q", control)
				}
				if _, exists := w.images[id]; !exists {
					t.Fatalf("cleanup addressed an image not owned by Vuja: %d", id)
				}
				for key := range w.placements {
					if key[0] == id && (number("p") == 0 || key[1] == number("p")) {
						delete(w.placements, key)
						delete(w.layers, key)
					}
				}
				if fields["d"] == "I" || w.iterm && number("p") == 0 {
					// iTerm's image-wide d=i also frees pixels. Captured by the
					// physical compositor probe and its KittyImageController.
					delete(w.images, id)
				}
			default:
				t.Fatalf("unexpected graphics operation: %q", control)
			}
		}
	}
	return replies.String()
}

func TestInteractiveBlocksPixelDividerDrawsAboveFocusedRail(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "first-output\r\n", "1")
	blocksExecution(c, "second", "second-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	wire := newDividerWire(10, 33)
	wire.settle(t, c, out)
	lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24))
	y := terminalLineIndex(lines, "second-output", 0)
	if y < 0 {
		t.Fatal("fixture must contain the second card")
	}
	cardMouse(t, c, 0, 2, y, true)
	cardMouse(t, c, 0, 2, y, false)
	wire.settle(t, c, out)
	if len(wire.placements) != 1 || c.viewport.cards.focus == nil {
		t.Fatal("fixture must focus a card with one boundary")
	}
	for key, placement := range wire.placements {
		if placement.h != 1 || wire.layers[key] <= 0 {
			t.Fatalf("shared edge must be one pixel above foreground rails, got height=%d layer=%d", placement.h, wire.layers[key])
		}
	}
}

func TestInteractiveBlocksPixelDividerITermGridBounds(t *testing.T) {
	for _, surface := range []string{"full-width", "content-width"} {
		c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='"+surface+"'\n")
		c.EnablePixelDividers()
		blocksExecution(c, "first", "output\r\n", "1")
		blocksExecution(c, strings.Repeat("second", 12), strings.Repeat("x", 60)+"\r\n", "0")
		blocksPrompt(c, "input")
		wire := newDividerWire(20, 66)
		wire.iterm, wire.frameWidth = true, 641
		wire.settle(t, c, out)
		want := 60 * 20
		if len(wire.placements) != 1 {
			t.Fatalf("missing boundary in %s", surface)
		}
		for _, p := range wire.placements {
			if p.w != want {
				t.Fatalf("%s line must stop at the text grid, not the window frame: got %d want %d", surface, p.w, want)
			}
		}
	}
}

func TestInteractiveBlocksPixelDividerITermCellSize(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "first-output\r\n", "1")
	blocksExecution(c, "second", "second-output\r\n", "0")
	blocksPrompt(c, "pending-input")
	wire := newDividerWire(20, 66)
	wire.iterm = true
	for _, geometry := range [][4]int{{60, 24, 20, 66}, {70, 30, 21, 67}} {
		wire.cw, wire.ch = geometry[2], geometry[3]
		c.Resize(geometry[0], geometry[1])
		wire.settle(t, c, out)
		if len(wire.placements) != 1 {
			t.Fatalf("iTerm's actual cell-size reply must produce a visible boundary, got %d placements", len(wire.placements))
		}
		lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), geometry[0], geometry[1]))
		header := terminalLineIndex(lines, "second", 0)
		for _, p := range wire.placements {
			if p != (dividerWirePlacement{0, header * wire.ch, geometry[0] * wire.cw, 1}) {
				t.Fatalf("iTerm backing scale must preserve the full-width shared pixel boundary: %+v, header %d", p, header)
			}
		}
	}
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
					wantWidth := width * wire.cw
					if surface == "content-width" {
						wantWidth = 32 * wire.cw
					}
					for _, image := range wire.images {
						if image.w != wantWidth || image.h != 1 {
							t.Fatalf("stale/incorrect physical dimensions: got %dx%d want %dx1", image.w, image.h, wantWidth)
						}
						for i := 0; i < len(image.data); i += 3 {
							if !bytes.Equal(image.data[i:i+3], []byte{42, 42, 42}) {
								t.Fatalf("strip must be neutral muted grey, not severity tint: %v", image.data[i:i+3])
							}
						}
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

func TestInteractiveBlocksPixelDividerSourcesMatchMixedCardWidths(t *testing.T) {
	for _, iterm := range []bool{false, true} {
		t.Run(fmt.Sprintf("iterm-%t", iterm), func(t *testing.T) {
			c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='content-width'\ncompleted-command='command'\n")
			theme := testInputBoxTheme()
			theme.Background, theme.Muted = "#000000", "#808080"
			c.SetInputBoxTheme(theme)
			c.EnablePixelDividers()
			blocksExecution(c, "first", "out\r\n", "0")
			blocksExecution(c, "short", "out\r\n", "0")
			blocksExecution(c, "long", strings.Repeat("x", 40)+"\r\n", "0")
			blocksExecution(c, "short-again", "out\r\n", "0")
			blocksPrompt(c, "input")
			wire := newDividerWire(10, 33)
			wire.iterm = iterm
			wire.settle(t, c, out)
			if len(wire.images) != 2 || len(wire.placements) != 3 {
				t.Fatalf("three boundaries need two native source widths, got images=%d placements=%d", len(wire.images), len(wire.placements))
			}
			widths := map[int]int{}
			for key, p := range wire.placements {
				image := wire.images[key[0]]
				if image.w != p.w || image.h != 1 || p.h != 1 {
					t.Fatalf("native source must match destination without cropping/scaling: image=%+v placement=%+v", image, p)
				}
				widths[p.w]++
			}
			if widths[320] != 2 || widths[420] != 1 {
				t.Fatalf("native boundaries did not follow independent short/long card edges: %v", widths)
			}
			uploads := wire.uploads
			for range 5 {
				c.mu.Lock()
				c.viewport.dirty = true
				c.renderOutputViewport()
				c.mu.Unlock()
				wire.settle(t, c, out)
			}
			if wire.uploads != uploads {
				t.Fatal("unchanged card widths reuploaded their native sources")
			}
			visible := true
			c.SetTransientUIReflow(nil, nil, nil, nil, nil, func() bool { return visible })
			c.ComposeUI(func() []byte { return nil })
			wire.settle(t, c, out)
			if len(wire.placements) != 0 || len(wire.images) != 2 {
				t.Fatal("overlay must hide every mixed-width placement without leaking its sources")
			}
			visible = false
			c.ComposeUI(func() []byte { return nil })
			wire.settle(t, c, out)
			if wire.uploads != uploads || len(wire.placements) != 3 {
				t.Fatal("closing overlay must reuse all mixed-width sources")
			}
			theme.Background = "#ffffff"
			c.SetInputBoxTheme(theme)
			wire.settle(t, c, out)
			if wire.uploads != uploads+2 || len(wire.images) != 2 || len(wire.placements) != 3 {
				t.Fatal("palette refresh must replace each distinct-width source once")
			}
			for _, image := range wire.images {
				if !bytes.Equal(image.data[:3], []byte{191, 191, 191}) {
					t.Fatalf("mixed-width source retained stale palette: %v", image.data[:3])
				}
			}
			c.Close()
			wire.settle(t, c, out)
			if len(wire.images) != 0 || len(wire.placements) != 0 {
				t.Fatal("close leaked a mixed-width source or placement")
			}
		})
	}
}

func TestInteractiveBlocksPixelDividerWidthChangeDuringAllocation(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='content-width'\n")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "out\r\n", "0")
	blocksExecution(c, "second", "out\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(10, 33)
	wire.iterm = true
	var filter terminalInputFilter
	filter.FilterInput([]byte(wire.readOutput(t, out)), c, false, true)
	reply := wire.readOutput(t, out)
	if reply == "" || wire.uploads != 1 {
		t.Fatal("fixture must leave a genuine content-width allocation pending")
	}
	cfg := c.chatboxConfig
	cfg.SurfaceWidth = "full-width"
	c.SetChatboxConfig(cfg)
	filter.FilterInput([]byte(reply), c, false, true)
	wire.settle(t, c, out)
	if wire.uploads != 2 || len(wire.images) != 1 || len(wire.placements) != 1 {
		t.Fatal("obsolete width allocation must be freed, not leaked or displayed")
	}
	for _, p := range wire.placements {
		if p.w != 600 || p.h != 1 {
			t.Fatalf("late allocation retained stale content width: %+v", p)
		}
	}
}

func TestInteractiveBlocksPixelDividerVisibleSourcesShareByteBudget(t *testing.T) {
	c, out := blocksFixture(t, "[ui.chatbox]\nsurface-width='content-width'\ncompleted-command='command'\n")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "out\r\n", "0")
	blocksExecution(c, "short", "out\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(5000, 33)
	wire.settle(t, c, out)
	if len(wire.images) != 1 || len(wire.placements) != 1 {
		t.Fatal("single native source below the byte budget must remain usable")
	}
	uploads := wire.uploads
	blocksExecution(c, "long", strings.Repeat("x", 40)+"\r\n", "0")
	blocksPrompt(c, "input")
	wire.settle(t, c, out)
	if wire.uploads != uploads || len(wire.images) != 0 || len(wire.placements) != 0 {
		t.Fatal("visible source diversity exceeded the former single-image RGB budget")
	}
	cfg := c.chatboxConfig
	cfg.SurfaceWidth = "full-width"
	c.SetChatboxConfig(cfg)
	wire.settle(t, c, out)
	if len(wire.images) != 1 || len(wire.placements) != 2 {
		t.Fatal("a later safe width set must recover without permanent disablement")
	}
}

func TestInteractiveBlocksPixelDividerDimAcrossPalettes(t *testing.T) {
	for _, fixture := range []struct {
		name, background string
		defaultRGB       [3]uint8
	}{
		{"night", "#000000", [3]uint8{0, 0, 0}},
		{"day", "#ffffff", [3]uint8{255, 255, 255}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			theme := testInputBoxTheme()
			theme.Background, theme.Muted = fixture.background, "#808080"
			c.SetInputBoxTheme(theme)
			c.EnablePixelDividers()
			blocksExecution(c, "failed", "failed-output\r\n", "1")
			blocksExecution(c, "next", "next-output\r\n", "0")
			blocksPrompt(c, "input")
			wire := newDividerWire(9, 19)
			wire.settle(t, c, out)
			if len(wire.images) != 1 {
				t.Fatal("missing neutral divider image")
			}
			for _, image := range wire.images {
				pixel := image.data[:3]
				if pixel[0] != pixel[1] || pixel[1] != pixel[2] {
					t.Fatalf("divider must stay opaque neutral grey, not inherit state tint: %v", pixel)
				}
				for _, state := range []blockState{blockNeutral, blockError, blockWarning, blockFocused} {
					// Decode the actual state renderer independently of the
					// divider color calculation, rather than guessing tint RGB.
					painted := applyTerminalOutput(t, []byte(c.blockStateLine("surface", state, 60)), 60, 1)
					surface := fixture.defaultRGB
					if color := painted.CellAt(2, 0).Style.Bg; color != nil {
						surface = blockTestRGB(color)
					}
					oldDistance, newDistance := 0, 0
					for channel, background := range surface {
						oldDelta := 128 - int(background)
						newDelta := int(pixel[channel]) - int(background)
						oldDistance += oldDelta * oldDelta
						newDistance += newDelta * newDelta
					}
					if newDistance == 0 || newDistance >= oldDistance {
						t.Fatalf("divider must remain visible but quieter against %v: pixel=%v old/new distance=%d/%d", surface, pixel, oldDistance, newDistance)
					}
				}
			}
			// Only image bytes change: the failed text surface/rail remains.
			screen := applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24)
			y := terminalLineIndex(terminalScreenLines(screen), "failed-output", 0)
			if y < 0 || screen.CellAt(0, y).Content != "▌" || screen.CellAt(59, y).Style.Bg == nil {
				t.Fatal("dimming the rule removed the failed block's full surface or rail")
			}
		})
	}
}

func TestInteractiveBlocksPixelDividerTerminalPalette(t *testing.T) {
	for _, fixture := range []struct {
		name, contrast string
		light          bool
		gray           byte
	}{
		{"night", "normal", false, 35},
		{"night-high", "high", false, 35},
		{"day", "normal", true, 162},
		{"day-high", "high", true, 162},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.UI.Palette, cfg.UI.Contrast = "terminal", fixture.contrast
			config.ApplyVisualPolicy(cfg)
			palette := cfg.UI.Colors.Night
			c, out := blocksFixture(t, "")
			theme := testInputBoxTheme()
			theme.LightBackground = fixture.light
			theme.Background, theme.Muted = palette.Background, palette.Muted
			c.SetInputBoxTheme(theme)
			c.EnablePixelDividers()
			blocksExecution(c, "first", "out\r\n", "1")
			blocksExecution(c, "second", "out\r\n", "0")
			blocksPrompt(c, "input")
			wire := newDividerWire(9, 19)
			wire.settle(t, c, out)
			if len(wire.images) != 1 {
				t.Fatal("missing indexed-palette divider")
			}
			for _, image := range wire.images {
				if !bytes.Equal(image.data[:3], []byte{fixture.gray, fixture.gray, fixture.gray}) {
					t.Fatalf("indexed palette must soften the previous RGB70 fallback using its background class: %v", image.data[:3])
				}
			}
		})
	}
}

func TestInteractiveBlocksPixelDividerThemeBackgroundRefresh(t *testing.T) {
	c, out := blocksFixture(t, "")
	theme := testInputBoxTheme()
	theme.Background, theme.Muted = "#000000", "#808080"
	c.SetInputBoxTheme(theme)
	c.EnablePixelDividers()
	blocksExecution(c, "first", "out\r\n", "1")
	blocksExecution(c, "second", "out\r\n", "0")
	blocksPrompt(c, "input")
	wire := newDividerWire(9, 19)
	wire.settle(t, c, out)
	uploads := wire.uploads
	var before dividerWirePlacement
	for _, placement := range wire.placements {
		before = placement
	}
	theme.Background = "#ffffff"
	c.SetInputBoxTheme(theme)
	c.ComposeUI(func() []byte { return nil })
	wire.settle(t, c, out)
	if wire.uploads != uploads+1 || len(wire.images) != 1 {
		t.Fatal("background-only palette switch must replace, not retain or leak, the old divider image")
	}
	for _, image := range wire.images {
		if !bytes.Equal(image.data[:3], []byte{191, 191, 191}) {
			t.Fatalf("replacement image retained the old background color: %v", image.data[:3])
		}
	}
	if len(wire.placements) != 1 {
		t.Fatal("theme refresh did not restore exactly one boundary")
	}
	for _, placement := range wire.placements {
		if placement != before {
			t.Fatalf("theme refresh moved the boundary: before=%+v after=%+v", before, placement)
		}
	}
}

func TestDividerReportsFramingAndOwnership(t *testing.T) {
	for _, report := range []string{"\x1b_Gi=555;OK\x1b\\", "\x1b_Gi=777,I=666;OK\x1b\\", "\x1b[6;19;9t", "\x1b]1337;ReportCellSize=33.0;10.0;1.0\x1b\\", "\x1b]1337;ReportCellSize=33.0;10.0\x07", "\x1b[?62;4c"} {
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
	frame := "\x1b[4;927;881t"
	if got := f.FilterInput([]byte(frame), c, false, false); string(got) != frame {
		t.Fatalf("unrequested application frame report was stolen: %q", got)
	}
	paste := "\x1b[200~\x1b_Gi=555;OK\x1b\\\x1b[6;19;9t\x1b]1337;ReportCellSize=33;10;2\x07\x1b[201~"
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
	responses := []string{"", "\x1b[6;0;0t", "\x1b[6;65535;65535t", "\x1b[6;broken;9t"}
	for _, value := range []string{"NaN;10;2", "33;+Inf;2", "33;10;0", "33;10;-1", "33;10;NaN", "33;10;+Inf", "65535;65535;2", "33;0.1;1", "33;10;1;2", "broken;10;1"} {
		responses = append(responses, "\x1b]1337;ReportCellSize="+value+"\x1b\\")
	}
	for _, response := range responses {
		c, out := blocksFixture(t, "")
		c.EnablePixelDividers()
		blocksExecution(c, "first", "out\r\n", "1")
		blocksExecution(c, "second", "out\r\n", "0")
		blocksPrompt(c, "input")
		d := &c.dividerPixels
		var f terminalInputFilter
		f.FilterInput([]byte(fmt.Sprintf("\x1b_Gi=%d;OK\x1b\\", d.probe)+response+"\x1b[?62c"), c, false, true)
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

func TestDividerITermGeometryPrecedenceAndOwnership(t *testing.T) {
	legacy := "\x1b]1337;ReportCellSize=33.25;10.5;2.0\x1b\\"
	for _, reports := range []string{legacy + "\x1b[6;33;11t", "\x1b[6;33;11t" + legacy} {
		c, _ := blocksFixture(t, "")
		c.dividerPixels = pixelDividers{enabled: true, probe: 555}
		var filter terminalInputFilter
		if got := filter.FilterInput([]byte(reports), c, false, false); len(got) != 0 {
			t.Fatalf("owned geometry reached application input: %q", got)
		}
		if c.dividerPixels.cellWidth != 21 || c.dividerPixels.cellHeight != 67 {
			t.Fatalf("point geometry must use backing pixels regardless of reply order: %+v", c.dividerPixels)
		}
		filter.FilterInput([]byte("\x1b[?64c"), c, false, false)
		if got := filter.FilterInput([]byte(legacy), c, false, false); string(got) != legacy {
			t.Fatalf("unsolicited application geometry was consumed: %q", got)
		}
	}
}

func TestInteractiveBlocksPixelDividerScaleCannotExpandGrid(t *testing.T) {
	c, out := blocksFixture(t, "")
	c.EnablePixelDividers()
	blocksExecution(c, "first", "out\r\n", "0")
	blocksExecution(c, "second", "out\r\n", "0")
	blocksPrompt(c, "input")
	var filter terminalInputFilter
	// Only converted cell pixels matter, not the frame size or backing scale
	// independently. These finite cell dimensions are safe even at this scale.
	reports := fmt.Sprintf("\x1b_Gi=%d;OK\x1b\\\x1b]1337;ReportCellSize=0.000000033;0.00000001;1000000000\x07\x1b[?64c", c.dividerPixels.probe)
	filter.FilterInput([]byte(reports), c, false, true)
	if !strings.Contains(out.String(), ",f=24,s=600,v=1,") {
		t.Fatal("safe cell geometry must allocate exactly the 60-column grid")
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
	for _, automatic := range []bool{true, false} {
		t.Run(fmt.Sprintf("automatic-%t", automatic), func(t *testing.T) {
			c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\ncompleted-command='command'\ncopy-on-select=%t\n", automatic))
			c.EnablePixelDividers()
			blocksExecution(c, "first", "first\r\n", "1")
			blocksExecution(c, "second", "copied café 日本語\r\n", "0")
			blocksPrompt(c, "input")
			wire := newDividerWire(9, 19)
			wire.settle(t, c, out)
			lines := terminalScreenLines(applyTerminalOutput(t, dividerTextProjection(out.Bytes()), 60, 24))
			y := terminalLineIndex(lines, "copied", 0)
			start := out.Len()
			cardMouse(t, c, 0, 2, y, true)
			cardMouse(t, c, 32, 21, y, true)
			if strings.Contains(out.String()[start:], "\x1b]52;") {
				t.Fatal("selection copied before release")
			}
			cardMouse(t, c, 0, 21, y, false)
			wire.settle(t, c, out)
			if automatic {
				assertExecutionClipboard(t, out.String()[start:], "copied café 日本語")
			} else if strings.Contains(out.String()[start:], "\x1b]52;") {
				t.Fatal("manual selection copied on release")
			}
			start = out.Len()
			var filter terminalInputFilter
			want := "next"
			if automatic {
				want = "\x03next"
			}
			if got := filter.FilterInput([]byte("\x03next"), c, false, true); string(got) != want {
				t.Fatalf("selection changed subsequent shell input: got %q, want %q", got, want)
			}
			if automatic {
				if strings.Contains(out.String()[start:], "\x1b]52;") {
					t.Fatal("interrupt copied the selection twice")
				}
			} else {
				assertExecutionClipboard(t, out.String()[start:], "copied café 日本語")
			}
			wire.settle(t, c, out)
			c.mu.Lock()
			c.clearOutputViewport(3)
			c.renderOutputViewport()
			c.mu.Unlock()
			wire.settle(t, c, out)
			if len(wire.images) != 0 || len(wire.placements) != 0 {
				t.Fatal("clear left a graphical boundary floating over the empty pane")
			}
		})
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
