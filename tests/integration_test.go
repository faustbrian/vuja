package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/faustbrian/vuja/commands"
	"github.com/faustbrian/vuja/spec"
)

func TestIntegration_ZoxideMultiWord(t *testing.T) {
	// Create mock directory with spaces
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "My Awesome Project")
	_ = os.MkdirAll(targetDir, 0755)

	// Mock shell environment
	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmp)
	defer func() { _ = os.Chdir(oldWd) }()

	// Mock zoxide binary
	mockBinDir := t.TempDir()
	mockZoxide := filepath.Join(mockBinDir, "zoxide")
	// Create mock zoxide binary
	script := "#!/bin/sh\necho \"" + targetDir + "\""
	_ = os.WriteFile(mockZoxide, []byte(script), 0755)
	t.Setenv("PATH", mockBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Run("z matches multi-word folder without quotes", func(t *testing.T) {
		// Simulating user typing "z My Awe"
		input := "z My Awe"
		deadline := time.Now().Add(time.Second)
		for {
			results := spec.Lookup(input)
			for _, result := range results {
				if !strings.Contains(result.Cmd, "My Awesome Project") {
					continue
				}
				if strings.Contains(result.Cmd, "My My") {
					t.Errorf("Word duplication detected in suggestion: %s", result.Cmd)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for 'My Awesome Project' in suggestions for %q", input)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}
