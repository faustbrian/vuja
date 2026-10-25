package root

import (
	"fmt"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/vt"
)

// Measure model ingestion separately from physical rendering, input, setup,
// providers and disk I/O. The SDK baseline receives complete graphemes too.
func BenchmarkOutputModelStream(b *testing.B) {
	for _, workload := range []struct {
		name string
		text string
	}{
		{"ascii", "retained output 0123456789abcdef 0123456789abcdef\r\n"},
		{"unicode", "retained output 界😊 e\u0301 👩‍💻 👍🏽 done\r\n"},
	} {
		for _, mode := range []string{"sdk", "stream"} {
			b.Run(workload.name+"/"+mode, func(b *testing.B) {
				model := vt.NewEmulator(120, 16)
				model.SetReflow(true)
				model.SetScrollbackSize(64)
				b.Cleanup(func() { _ = model.Close() })
				var stream outputModelStream
				data := []byte(workload.text)
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for range b.N {
					if mode == "sdk" {
						_, _ = model.Write(data)
					} else {
						stream.write(model, data)
					}
				}
			})
		}
	}
}

// Each mark arrives in its own read, exposing growing-prefix work hidden by
// complete-grapheme workloads. Setup is excluded; one iteration ingests one
// complete line. Do not turn these noisy timings into unit-test assertions.
func BenchmarkOutputModelStreamFragmentedCluster(b *testing.B) {
	for _, marks := range []int{128, 512, 4096} {
		b.Run(fmt.Sprintf("marks-%d", marks), func(b *testing.B) {
			model := vt.NewEmulator(80, 4)
			model.SetReflow(true)
			model.SetScrollbackSize(8)
			b.Cleanup(func() { _ = model.Close() })
			var stream outputModelStream
			base, mark, end := []byte("a"), []byte("\u0301"), []byte("\r\n")
			b.ReportAllocs()
			b.SetBytes(int64(len(base) + marks*len(mark) + len(end)))
			b.ResetTimer()
			for range b.N {
				stream.write(model, base)
				for range marks {
					stream.write(model, mark)
				}
				stream.write(model, end)
			}
		})
	}
}
