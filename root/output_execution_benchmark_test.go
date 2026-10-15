package root

import (
	"io"
	"strings"
	"testing"
)

// Compare the additional execution projection at the real PTY/render boundary.
// View-only is an isolated counterfactual, not a supported runtime mode. Setup,
// model creation and close are outside these steady-state ingestion samples.
func BenchmarkRetainedExecutionIngestion(b *testing.B) {
	for _, fixture := range []struct{ name, text string }{
		{"ascii", strings.Repeat("output with ordinary words and numbers 12345\r\n", 16)},
		{"unicode", strings.Repeat("世界 Cafe\u0301 👩‍💻 done\r\n", 16)},
	} {
		for _, capture := range []bool{false, true} {
			name := fixture.name + "/view-only"
			if capture {
				name = fixture.name + "/with-execution"
			}
			b.Run(name, func(b *testing.B) {
				c := newTerminalCompositor(io.Discard, "bottom", "output-bench", 120, 30)
				b.Cleanup(c.Close)
				c.SetInputBoxTheme(testInputBoxTheme())
				c.SetChatboxConfig(terminalChatboxConfig{Prompt: "› ", OutputViewport: "pinned", OutputMouse: "select", OutputLines: 2000})
				c.WritePTY(terminalMarkerBytes("output-bench", "prompt-start"))
				c.WritePTY([]byte("› fictional command"))
				c.WritePTY(terminalMarkerBytes("output-bench", "prompt-end"))
				c.WritePTY(terminalMarkerBytes("output-bench", "command-start"))
				if !capture {
					c.finishOutputExecution(false)
				}
				chunk := []byte(fixture.text)
				c.WritePTY(chunk) // warm both retained models and rendering
				b.SetBytes(int64(len(chunk)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					c.WritePTY(chunk)
				}
			})
		}
	}
}
