package root

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/spec"
)

// G1: drive the startup subscription boundary used by runWrapper, not a later
// synthetic resize event. The terminal settles before signals are subscribed.
func TestTerminalStartupGeometryReconcilesWithoutAnotherResize(t *testing.T) {
	for _, test := range []struct {
		name                             string
		beforeW, beforeH, afterW, afterH int
	}{
		{"grow", 80, 24, 120, 40},
		{"shrink", 120, 40, 80, 24},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "startup", test.beforeW, test.beforeH)
			t.Cleanup(c.Close)
			overlay := integration.NewOverlay(true)
			overlay.SetTerminalSize(test.beforeW, test.beforeH)
			presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
			width, height := test.beforeW, test.beforeH
			ptyW, ptyH := test.beforeW, test.beforeH
			reconcile := func() {
				resizeManagedTerminal(presenter, c, overlay,
					func() (int, int, error) { return width, height, nil },
					func() error { ptyW, ptyH = width, height; return nil },
				)
			}
			subscribeTerminalGeometry(func() {
				// This pre-subscription resize has no queued notification.
				width, height = test.afterW, test.afterH
			}, reconcile)
			if c.width != test.afterW || c.height != test.afterH ||
				c.emulator.Width() != test.afterW || c.emulator.Height() != test.afterH ||
				overlay.TerminalWidth != test.afterW || overlay.TerminalHeight != test.afterH ||
				ptyW != test.afterW || ptyH != test.afterH {
				t.Fatalf("startup retained stale geometry without a second resize: compositor=%dx%d overlay=%dx%d PTY=%dx%d want=%dx%d",
					c.width, c.height, overlay.TerminalWidth, overlay.TerminalHeight, ptyW, ptyH, test.afterW, test.afterH)
			}
		})
	}
}

// G2: the production refresh owns the sample and its application under the
// presenter lock. A delayed old sample cannot overwrite the next observation.
func TestTerminalStartupGeometryOrdersQueuedResizeAndPresentation(t *testing.T) {
	var out lockedBuffer
	c := newTerminalCompositor(&out, "bottom", "startup", 80, 24)
	t.Cleanup(c.Close)
	overlay := integration.NewOverlay(true)
	overlay.SetTerminalSize(80, 24)
	presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	ptyW, ptyH := 0, 0
	reconcile := func() {
		resizeManagedTerminal(presenter, c, overlay,
			func() (int, int, error) {
				if reads.Add(1) == 1 {
					close(entered)
					<-release
					return 100, 30, nil
				}
				return 120, 40, nil
			},
			func() error { ptyW, ptyH = 120, 40; return nil },
		)
	}
	var workers sync.WaitGroup
	workers.Go(func() { subscribeTerminalGeometry(func() {}, reconcile) })
	<-entered
	started := make(chan struct{})
	workers.Go(func() { close(started); reconcile() })
	<-started
	workers.Go(func() { presenter.Present(func() []byte { return []byte(overlay.Render()) }) })
	close(release)
	workers.Wait()
	if c.width != 120 || c.height != 40 || overlay.TerminalWidth != 120 || overlay.TerminalHeight != 40 || ptyW != 120 || ptyH != 40 {
		t.Fatal("queued startup resize left mixed geometry generations")
	}
	// Verify the two observations converge before any additional notification.
	// A repeated identical event must then leave that final state unchanged.
	reconcile()
	if c.width != 120 || c.height != 40 || overlay.TerminalWidth != 120 || overlay.TerminalHeight != 40 {
		t.Fatal("identical resize changed the settled geometry")
	}
}

// G3/G5: failed stdout observations must not invent UI sizes or prevent the
// independent stdin-to-child adapter from running, even for unmanaged output.
func TestTerminalStartupGeometryFailureAndFallback(t *testing.T) {
	for _, marker := range []string{"startup", ""} {
		t.Run("marker="+marker, func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", marker, 80, 24)
			t.Cleanup(c.Close)
			overlay := integration.NewOverlay(true)
			overlay.SetTerminalSize(80, 24)
			presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
			ptyCalls := 0
			inherit := func() error { ptyCalls++; return errors.New("fixture PTY unavailable") }
			resizeManagedTerminal(presenter, c, overlay,
				func() (int, int, error) { return 0, 0, errors.New("fixture size unavailable") }, inherit)
			if c.width != 80 || c.height != 24 || overlay.TerminalWidth != 80 || overlay.TerminalHeight != 24 || ptyCalls != 1 {
				t.Fatal("read failure fabricated geometry or skipped independent PTY sizing")
			}
			for _, size := range [][2]int{{0, 24}, {80, 0}, {80, 1}, {90, 28}} {
				resizeManagedTerminal(presenter, c, overlay,
					func() (int, int, error) { return size[0], size[1], nil }, inherit)
			}
			if ptyCalls != 5 || overlay.TerminalWidth != 90 || overlay.TerminalHeight != 28 {
				t.Fatal("later valid observation did not recover overlay/PTY sizing")
			}
			if marker == "" {
				if c.Managed() {
					t.Fatal("size refresh enabled markerless managed output")
				}
				c.WritePTY([]byte("literal passthrough"))
				if out.String() != "literal passthrough" {
					t.Fatalf("unmanaged bytes changed: %q", out.String())
				}
				return
			}
			if c.width != 90 || c.height != 28 || c.Enabled() {
				t.Fatal("managed recovery must use final geometry and await a complete prompt")
			}
			c.WritePTY(terminalMarkerBytes(marker, "prompt-start"))
			c.WritePTY([]byte("λ resumed"))
			c.WritePTY(terminalMarkerBytes(marker, "prompt-end"))
			if !c.Enabled() {
				t.Fatal("valid complete prompt did not resume after tiny geometry")
			}
		})
	}
}

func TestTerminalStartupGeometryPreservesInitiallyUnusableFallback(t *testing.T) {
	for _, size := range [][2]int{{0, 24}, {80, 0}, {80, 1}} {
		var out bytes.Buffer
		c := newTerminalCompositor(&out, "bottom", "startup", size[0], size[1])
		t.Cleanup(c.Close)
		overlay := integration.NewOverlay(true)
		presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
		ptySized := false
		subscribeTerminalGeometry(func() {}, func() {
			resizeManagedTerminal(presenter, c, overlay,
				func() (int, int, error) { return 90, 28, nil },
				func() error { ptySized = true; return nil })
		})
		c.WritePTY([]byte("literal fallback"))
		if c.Managed() || !ptySized || out.String() != "literal fallback" {
			t.Fatalf("unusable initial geometry must preserve classic fallback: initial=%v managed=%v output=%q", size, c.Managed(), out.String())
		}
	}
}

// G4: this fixture has one title row, one padding row on each side of the
// one-line input, and one status row. At height 12 they occupy rows 7..11.
func TestTerminalStartupGeometryPinsPromptStatusAndMenuToFinalGrid(t *testing.T) {
	for _, mode := range []string{"terminal", "pinned"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			c := newTerminalCompositor(&out, "bottom", "startup", 100, 30)
			t.Cleanup(c.Close)
			c.SetInputBoxTheme(testInputBoxTheme())
			c.SetChatboxConfig(terminalChatboxConfig{
				Prompt: "λ ", OutputViewport: mode, OutputMouse: "select", OutputLines: 100,
				Title:  terminalChatboxBarConfig{Left: []string{"directory"}},
				Status: terminalChatboxBarConfig{Right: []string{"exit"}},
			})
			c.SetInputBoxPath("/fixture")
			exit := 7
			c.SetStatusSnapshot(statusSnapshot{ExitCode: &exit})
			overlay := integration.NewOverlay(true)
			overlay.SetBottomPrompt(true)
			overlay.SetTerminalSize(100, 30)
			presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
			subscribeTerminalGeometry(func() {}, func() {
				resizeManagedTerminal(presenter, c, overlay,
					func() (int, int, error) { return 60, 12, nil }, func() error { return nil })
			})
			c.WritePTY(terminalMarkerBytes("startup", "prompt-start"))
			c.WritePTY([]byte("λ git"))
			c.WritePTY(terminalMarkerBytes("startup", "prompt-end"))
			overlay.SetPromptRows(5)
			overlay.SetPromptLen(4)
			overlay.SetQueryAndItems("git", []spec.Suggestion{{Cmd: "git status"}, {Cmd: "git switch"}})
			presenter.Present(func() []byte { return []byte(overlay.RenderGhostText("git", false, true)) })
			screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
			if !strings.Contains(screenLine(screen, 7), "/fixture") ||
				!strings.Contains(screenLine(screen, 9), "λ git status") ||
				!strings.Contains(screenLine(screen, 11), "exit 7") || screen.CursorPosition().Y != 9 {
				t.Fatalf("startup prompt/status/ghost used old bounds: %q cursor=%v", terminalScreenLines(screen), screen.CursorPosition())
			}
			presenter.Present(func() []byte { return []byte(overlay.Render()) })
			screen = applyTerminalOutput(t, out.Bytes(), 60, 12)
			menu := -1
			for row := 0; row < 7; row++ {
				if strings.Contains(screenLine(screen, row), "git switch") {
					menu = row
				}
			}
			if menu < 0 || !strings.Contains(screenLine(screen, 9), "λ git") || !strings.Contains(screenLine(screen, 11), "exit 7") {
				t.Fatalf("menu did not stay above final prompt/status rows: %q", terminalScreenLines(screen))
			}
		})
	}
}

// G5: native application ownership and stdin/stdout size authority are separate
// from managed chrome. The fixture adapter records the stdin-derived payload.
func TestTerminalStartupGeometryPreservesNativeApplication(t *testing.T) {
	var out bytes.Buffer
	c := newTerminalCompositor(&out, "bottom", "startup", 80, 24)
	t.Cleanup(c.Close)
	c.WritePTY(terminalMarkerBytes("startup", "command-start"))
	c.WritePTY([]byte("\x1b[?1049h\x1b[Happlication"))
	out.Reset()
	overlay := integration.NewOverlay(true)
	presenter := newTerminalUIPresenter(c.ComposeUI, c.recoverVisualPipeline)
	ptyW, ptyH := 0, 0
	subscribeTerminalGeometry(func() {}, func() {
		resizeManagedTerminal(presenter, c, overlay,
			func() (int, int, error) { return 60, 12, nil },
			func() error { ptyW, ptyH = 59, 11; return nil },
		)
	})
	c.WritePTY([]byte("native tail"))
	if out.String() != "native tail" || ptyW != 59 || ptyH != 11 || overlay.TerminalWidth != 60 || overlay.TerminalHeight != 12 {
		t.Fatalf("refresh stole native output or changed descriptor authority: %q PTY=%dx%d", out.String(), ptyW, ptyH)
	}
	c.WritePTY([]byte("\x1b[?1049l"))
	c.WritePTY(terminalMarkerBytes("startup", "command-end:0"))
	c.WritePTY(terminalMarkerBytes("startup", "prompt-start"))
	c.WritePTY([]byte("λ returned"))
	c.WritePTY(terminalMarkerBytes("startup", "prompt-end"))
	screen := applyTerminalOutput(t, out.Bytes(), 60, 12)
	if !strings.Contains(screenLine(screen, 11), "λ returned") {
		t.Fatalf("native exit returned to stale prompt geometry: %q", terminalScreenLines(screen))
	}
}
