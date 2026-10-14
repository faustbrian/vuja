package root

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// QA 2/6/8: the native display remains application-owned, but private canvas
// bytes must not become retained output now or through a later checkpoint.
func TestOutputCheckpointNativeHandoffPreservesOnlyOwnedTextAndExitSummary(t *testing.T) {
	stream := []byte("owned-prefix\r\n\x1b[?1049hprivate-canvas\x1b[?1049l\x1b[?25h\r\n\x1b[32mpublic-exit-summary\x1b[0m\r\n")
	for split := 0; split <= len(stream); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			c, _ := recoveryCompositor(t, t.TempDir(), "pane", "native", nil)
			c.WritePTY(terminalMarkerBytes("native", "command-start"))
			c.WritePTY(stream[:split])
			c.WritePTY(stream[split:])
			c.WritePTY(terminalMarkerBytes("native", "command-end:0"))
			c.Close()
			data, err := os.ReadFile(c.outputRecovery.path)
			if err != nil || bytes.Contains(data, []byte("private-canvas")) || !bytes.Contains(data, []byte("owned-prefix")) || !bytes.Contains(data, []byte("public-exit-summary")) {
				t.Fatal("native handoff retained canvas or discarded owned/exit output")
			}
		})
	}
}

func TestOutputCheckpointRetainsRecognizedCodexResumeMetadataAfterNativePrimaryApp(t *testing.T) {
	dir := t.TempDir()
	c, _ := recoveryCompositor(t, dir, "pane", "native", nil)
	c.WritePTY(terminalMarkerBytes("native", "command-start"))
	c.WritePTY([]byte("owned-prefix\r\n\x1b[2;2Hprivate-primary-canvas\r\n"))
	for _, text := range []string{"To reconnect, run:\r\n", "  codex resume ", testCodexResumeID, "\r\n"} {
		c.WritePTY([]byte(text))
	}
	c.WritePTY(terminalMarkerBytes("native", "command-end:0"))
	c.Close()
	data, err := os.ReadFile(c.outputRecovery.path)
	if err != nil || bytes.Contains(data, []byte("private-primary-canvas")) || !bytes.Contains(data, []byte(testCodexResumeID)) || !bytes.Contains(data, []byte("owned-prefix")) {
		t.Fatal("native resume metadata was lost or included private canvas")
	}
	_, restored := recoveryCompositor(t, dir, "pane", "reopen", func(id string) string { return "vuja://codex-resume/" + id + "?token=fresh" })
	if !strings.Contains(restored.String(), testCodexResumeID) || strings.Contains(restored.String(), "private-primary-canvas") || !strings.Contains(restored.String(), "?token=fresh") {
		t.Fatal("native resume metadata was not restored with a fresh action")
	}
}

func TestOutputCheckpointNativeModeControlsDoNotAdmitAnApplicationCanvas(t *testing.T) {
	for _, control := range []string{"\x1b[?25l", "\x1b[?1000h", "\x1b[?1006h", "\x1b[?2004h", "\x1b[?2026h"} {
		t.Run(fmt.Sprintf("%q", control), func(t *testing.T) {
			c, _ := recoveryCompositor(t, t.TempDir(), "pane", "native", nil)
			c.WritePTY(terminalMarkerBytes("native", "command-start"))
			c.WritePTY([]byte("owned-prefix\r\n" + control + "private-mode-canvas"))
			c.WritePTY(terminalMarkerBytes("native", "command-end:0"))
			c.Close()
			data, err := os.ReadFile(c.outputRecovery.path)
			if err != nil || bytes.Contains(data, []byte("private-mode-canvas")) || !bytes.Contains(data, []byte("owned-prefix")) {
				t.Fatal("native mode change did not establish the application ownership boundary")
			}
		})
	}
}
