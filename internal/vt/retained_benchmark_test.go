package vt

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// Same retained rows in both modes, below the independent byte budget. Setup
// fills the buffer; measurements cover steady-state admission and eviction.
func BenchmarkRetainedScrollbackBudget(b *testing.B) {
	for _, rows := range []int{64, 512, 1024} {
		for _, logical := range []bool{false, true} {
			b.Run(fmt.Sprintf("rows-%d/logical-%v", rows, logical), func(b *testing.B) {
				s := NewScrollback(rows)
				s.logical = logical
				line := uv.NewLine(4)
				for x := range line {
					line[x].Content = "x"
				}
				for range rows {
					s.pushRetained(line, 0, 4)
				}
				if s.Len() != rows {
					b.Fatal("fixture exceeded its independent byte budget")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					s.pushRetained(line, 0, 4)
				}
			})
		}
	}
}

// Original QA scenario 8: measure the same bounded retained model after an
// increasing amount of input. One operation includes ingestion and collection;
// this is not a per-keystroke latency benchmark. Run with -benchtime=1x so each
// sample has one fresh model. Heap deltas after GC are approximate live Go heap,
// not RSS, peak memory, or the footprint of the full Vuja process.
func BenchmarkRetainedSessionFootprint(b *testing.B) {
	for _, total := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("written-%d", total), func(b *testing.B) {
			line := []byte(strings.Repeat("ordinary output 12345 ", 5) + "session-tail\r\n")
			b.SetBytes(int64(len(line) * total))
			b.ReportAllocs()
			for range b.N {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				e := NewEmulator(120, 12)
				e.SetReflow(true)
				e.SetScrollbackSize(10000)
				for range total {
					_, _ = e.Write(line)
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				if after.HeapAlloc > before.HeapAlloc {
					b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained-heap-B")
				}
				b.ReportMetric(float64(e.Scrollback().cost), "retained-text-B")
				b.ReportMetric(float64(e.ScrollbackLen()), "retained-rows")
				if e.Scrollback().cost > retainedScrollbackBytes || e.ScrollbackLen() > 10000 || !e.RetainedOutputTruncated() {
					b.Fatal("fixture failed its real retention bounds")
				}
				if !strings.HasSuffix(retainedText(e), "session-tail\n") {
					b.Fatal("retention discarded the newest complete output")
				}
				runtime.KeepAlive(e)
				_ = e.Close()
			}
		})
	}
}

// Resize has to preserve semantic text without spending retention again. Setup
// and a final independent text comparison are outside the measured geometry
// cycle. This deliberately includes a one-column pane, not only usual widths.
func BenchmarkRetainedSessionResize(b *testing.B) {
	for _, total := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("rows-%d", total), func(b *testing.B) {
			e := NewEmulator(120, 12)
			e.SetReflow(true)
			e.SetScrollbackSize(10000)
			b.Cleanup(func() { _ = e.Close() })
			line := []byte("retained resize 日本語 👩‍💻 " + strings.Repeat("x", 40) + "\r\n")
			for range total {
				_, _ = e.Write(line)
			}
			want, truncated := retainedText(e), e.RetainedOutputTruncated()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for _, size := range [][2]int{{20, 8}, {1, 1}, {160, 30}, {120, 12}} {
					e.Resize(size[0], size[1])
				}
			}
			b.StopTimer()
			if retainedText(e) != want || e.RetainedOutputTruncated() != truncated {
				b.Fatal("geometry changed retained semantic text or completeness")
			}
		})
	}
}
