package root

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/internal/config"
)

func TestSelectionCopiesOnReleaseByDefault(t *testing.T) {
	for _, gesture := range []string{"forward", "reverse", "release-only"} {
		t.Run(gesture, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			blocksExecution(c, "selection-command", "select-me café 日本語\r\n", "0")
			blocksPrompt(c, "pending-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "select-me", 0)
			if y < 0 {
				t.Fatal("selection fixture is not visible")
			}
			x := ansi.StringWidth(lines[y][:strings.Index(lines[y], "select-me")])
			first, last := x, x+8
			if gesture == "reverse" {
				first, last = last, first
			}
			out.Reset()
			cardMouse(t, c, 0, first, y, true)
			if gesture != "release-only" {
				cardMouse(t, c, 32, last, y, true)
			}
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("selection copied before mouse release")
			}
			cardMouse(t, c, 0, last, y, false)
			assertExecutionClipboard(t, out.String(), "select-me")
			out.Reset()
			cardMouse(t, c, 0, last, y, false)
			filter := &terminalInputFilter{}
			if got := filter.FilterInput([]byte("\x03next"), c, false, true); string(got) != "\x03next" {
				t.Fatalf("automatic-copy mode swallowed interrupt/typing: %q", got)
			}
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("duplicate release or Ctrl+C copied the same gesture again")
			}
		})
	}
}

func TestSelectionCopyConfigurationControlsTheTrigger(t *testing.T) {
	for _, setting := range []struct {
		name, document string
		automatic      bool
	}{
		{"omitted", "", true},
		{"true", "[ui.chatbox]\ncopy-on-select=true\n", true},
		{"false", "[ui.chatbox]\ncopy-on-select=false\n", false},
	} {
		t.Run(setting.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path, err := config.ConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(setting.document), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := config.Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err = config.Load()
			if err != nil {
				t.Fatal(err)
			}
			c, out := blocksFixture(t, "")
			c.SetChatboxConfig(terminalChatboxConfigFromConfig(cfg))
			blocksExecution(c, "command", "selected\r\n", "0")
			blocksPrompt(c, "pending-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "selected", 0)
			if y < 0 {
				t.Fatal("selection fixture is not visible")
			}
			filter := &terminalInputFilter{}
			if got := filter.FilterInput([]byte{3}, c, false, true); string(got) != "\x03" {
				t.Fatal("Ctrl+C without a selection was consumed")
			}
			out.Reset()
			cardMouse(t, c, 0, 2, y, true)
			cardMouse(t, c, 32, 9, y, true)
			cardMouse(t, c, 0, 9, y, false)
			if setting.automatic {
				assertExecutionClipboard(t, out.String(), "selected")
			} else if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("explicit Ctrl+C mode copied on release")
			}
			out.Reset()
			got := filter.FilterInput([]byte("\x03next"), c, false, true)
			if setting.automatic {
				if string(got) != "\x03next" || strings.Contains(out.String(), "\x1b]52;") {
					t.Fatal("automatic mode recopied or consumed Ctrl+C")
				}
			} else {
				if string(got) != "next" {
					t.Fatalf("manual copy changed following input: %q", got)
				}
				assertExecutionClipboard(t, out.String(), "selected")
			}
		})
	}
}

func TestSelectionCopyRequiresAFreshCompletedDrag(t *testing.T) {
	for _, transition := range []string{"click", "orphan", "resize", "clear", "config", "wheel", "native"} {
		t.Run(transition, func(t *testing.T) {
			c, out := blocksFixture(t, "")
			blocksExecution(c, "command", "selected\r\n", "0")
			blocksPrompt(c, "pending-input")
			y := terminalLineIndex(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "selected", 0)
			if y < 0 {
				t.Fatal("selection fixture is not visible")
			}
			out.Reset()
			if transition != "orphan" {
				cardMouse(t, c, 0, 2, y, true)
			}
			last := 2
			if transition != "click" && transition != "orphan" {
				last = 9
				cardMouse(t, c, 32, last, y, true)
			}
			switch transition {
			case "resize":
				c.Resize(45, 24)
			case "clear":
				c.mu.Lock()
				c.clearOutputViewport(3)
				c.renderOutputViewport()
				c.mu.Unlock()
			case "config":
				c.SetChatboxConfig(c.chatboxConfig)
			case "wheel":
				cardMouse(t, c, 64, 2, y, true)
			case "native":
				c.WritePTY(terminalMarkerBytes("blocks", "command-start"))
				c.WritePTY([]byte("\x1b[?1049h"))
			}
			filter := &terminalInputFilter{}
			filter.FilterInput([]byte(fmt.Sprintf("\x1b[<0;%d;%dm", last+1, y+1)), c, false, transition != "native")
			if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("click or canceled/stale gesture changed the clipboard")
			}
		})
	}
}

func TestSelectionCopyUsesFrozenAndRestoredText(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) {
			c, out := blocksFixture(t, "")
			dir := t.TempDir()
			if restored {
				if err := c.ConfigureOutputRecovery(dir, "selection-copy-pane", nil); err != nil {
					t.Fatal(err)
				}
			}
			blocksExecution(c, "command", "snapshot-one\r\n", "0")
			if restored {
				c.Close()
				c, out = blocksFixture(t, "")
				if err := c.ConfigureOutputRecovery(dir, "selection-copy-pane", nil); err != nil {
					t.Fatal(err)
				}
			}
			blocksPrompt(c, "pending-input")
			y := terminalLineIndex(terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24)), "snapshot-one", 0)
			if y < 0 {
				t.Fatal("selection fixture is not visible")
			}
			out.Reset()
			cardMouse(t, c, 0, 2, y, true)
			cardMouse(t, c, 32, 13, y, true)
			c.WriteNotification([]byte("incoming-output\r\n"))
			cardMouse(t, c, 0, 13, y, false)
			assertExecutionClipboard(t, out.String(), "snapshot-one")
		})
	}
}

func TestSelectionCopyClipboardBounds(t *testing.T) {
	for _, size := range []int{0, 262143, 262144, 262145} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, out := blocksFixture(t, "")
			blocksExecution(c, "command", "output\r\n", "0")
			blocksPrompt(c, "pending-input")
			text := strings.Repeat("é", size/2) + strings.Repeat("x", size%2)
			out.Reset()
			c.copyCardText(text)
			if size > 0 && size <= 262144 {
				assertExecutionClipboard(t, out.String(), text)
			} else if strings.Contains(out.String(), "\x1b]52;") {
				t.Fatal("empty or oversized copy changed the clipboard")
			}
			if size > 262144 && !strings.Contains(c.viewport.executions.notice, "exceeds clipboard limit") {
				t.Fatal("oversized copy was silently clipped")
			}
		})
	}
}

func TestSelectionCopyDragDoesNotActivateCopyOrSave(t *testing.T) {
	for _, action := range []string{"Copy", "Save"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			c, out := blocksFixture(t, "")
			blocksExecution(c, "command", "whole-output\r\n", "0")
			blocksPrompt(c, "pending-input")
			lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
			y := terminalLineIndex(lines, "Copy", 0)
			if y < 0 || !strings.Contains(lines[y], action) {
				t.Fatal("card actions are not visible")
			}
			x := ansi.StringWidth(lines[y][:strings.Index(lines[y], action)])
			out.Reset()
			cardMouse(t, c, 0, x, y, true)
			cardMouse(t, c, 32, x+3, y, true)
			cardMouse(t, c, 0, x+3, y, false)
			assertExecutionClipboard(t, out.String(), action)
			c.mu.Lock()
			done := c.outputExportDone
			c.mu.Unlock()
			if done != nil {
				<-done
			}
			paths, err := filepath.Glob(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "vuja", "exports", "*.txt"))
			if err != nil || len(paths) != 0 {
				t.Fatal("dragging over an action exported the execution")
			}
		})
	}
}

func TestSelectionCopyDoesNotOwnPasteOrNativeSelection(t *testing.T) {
	for _, mode := range []string{"select", "navigate"} {
		c, out := blocksFixture(t, "[ui.chatbox]\noutput-mouse='"+mode+"'\n")
		blocksExecution(c, "command", "selected\r\n", "0")
		blocksPrompt(c, "pending-input")
		out.Reset()
		filter := &terminalInputFilter{}
		filter.FilterInput([]byte("\x1b[<0;3;2M\x1b[<32;10;2M\x1b[<0;10;2m"), c, false, true)
		if strings.Contains(out.String(), "\x1b]52;") {
			t.Fatal("native-selection/navigation mode acquired managed copy")
		}
	}
	for _, automatic := range []bool{false, true} {
		c, out := blocksFixture(t, fmt.Sprintf("[ui.chatbox]\ncopy-on-select=%t\n", automatic))
		blocksExecution(c, "command", "selected\r\n", "0")
		blocksPrompt(c, "pending-input")
		out.Reset()
		filter := &terminalInputFilter{}
		paste := "\x1b[200~literal\x03\x1b[<0;3;2M\x1b[<32;10;2M\x1b[<0;10;2m\x1b[201~"
		if got := filter.FilterInput([]byte(paste), c, false, true); string(got) != paste || strings.Contains(out.String(), "\x1b]52;") {
			t.Fatal("paste contents triggered selection copy or changed shell bytes")
		}
	}
}

func TestSelectionCopyGestureReadBoundaries(t *testing.T) {
	const want = "selected"
	for split := 0; split < 3; split++ {
		c, out := blocksFixture(t, "")
		blocksExecution(c, "command", want+"\r\n", "0")
		blocksPrompt(c, "pending-input")
		lines := terminalScreenLines(applyTerminalOutput(t, out.Bytes(), 60, 24))
		y := terminalLineIndex(lines, want, 0)
		if y < 0 {
			t.Fatal("selection fixture is not visible")
		}
		x := ansi.StringWidth(lines[y][:strings.Index(lines[y], want)])
		wire := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+len(want), y+1, x+len(want), y+1)
		out.Reset()
		filter := &terminalInputFilter{}
		var forwarded []byte
		if split == 0 {
			forwarded = filter.FilterInput([]byte(wire+"next"), c, false, true)
		} else {
			step := split
			for i := 0; i < len(wire); i += step {
				forwarded = append(forwarded, filter.FilterInput([]byte(wire[i:min(i+step, len(wire))]), c, false, true)...)
			}
			forwarded = append(forwarded, filter.FilterInput([]byte("next"), c, false, true)...)
		}
		if string(forwarded) != "next" {
			t.Fatalf("mouse framing changed following input: %q", forwarded)
		}
		assertExecutionClipboard(t, out.String(), want)
	}
}
