package root

import (
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/spec"
)

func TestHandleShellControlMessageUpdatesWorkingDirectory(t *testing.T) {
	shellDir := t.TempDir()
	t.Cleanup(func() {
		spec.SetShellCWD("")
	})

	if !handleShellControlMessage("VUJA_CWD:" + shellDir) {
		t.Fatal("expected working-directory message to be handled")
	}
	if got := spec.GetCWD(); got != filepath.Clean(shellDir) {
		t.Fatalf("expected shell directory %q, got %q", shellDir, got)
	}
	if handleShellControlMessage("cd vuja") {
		t.Fatal("ordinary shell input must not be treated as a control message")
	}
}
