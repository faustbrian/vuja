package root

import (
	"runtime"
	"testing"
)

func TestPercentClampsAndRejectsMissingSamples(t *testing.T) {
	for _, test := range []struct {
		used, total float64
		want        float64
	}{
		{used: 25, total: 100, want: 25},
		{used: 120, total: 100, want: 100},
		{used: 0, total: 100, want: 0},
		{used: 1, total: 0, want: 0},
	} {
		if got := percent(test.used, test.total); got != test.want {
			t.Fatalf("percent(%v, %v)=%v, want %v", test.used, test.total, got, test.want)
		}
	}
}

func TestCPUPercentUsesWholeSystemCounterDeltas(t *testing.T) {
	if got := cpuPercent(100, 40, 200, 65); got != 75 {
		t.Fatalf("expected 75%% CPU use, got %v", got)
	}
	if got := cpuPercent(200, 65, 100, 40); got != 0 {
		t.Fatalf("expected counter reset to return zero, got %v", got)
	}
}

func TestSampleSystemUsageReturnsBoundedNativeMetrics(t *testing.T) {
	cpu, memory := sampleSystemUsage()
	if cpu < 0 || cpu > 100 || memory < 0 || memory > 100 {
		t.Fatalf("unexpected system usage cpu=%v memory=%v", cpu, memory)
	}
	if (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && memory == 0 {
		t.Fatalf("expected native memory usage on %s", runtime.GOOS)
	}
}
