package root

import (
	"os"
	"testing"

	"github.com/faustbrian/vuja/internal/scoring"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	code := m.Run()
	suggestionLatency.Close()
	scoring.CloseGlobalFrecencyStore()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = os.Stderr.WriteString("goleak: " + err.Error() + "\n")
			os.Exit(1)
		}
	}
	os.Exit(code)
}

func TestRootCommandUsesVujaBrand(t *testing.T) {
	if rootCmd.Use != "vuja" {
		t.Fatalf("expected root command to be vuja, got %q", rootCmd.Use)
	}
}
