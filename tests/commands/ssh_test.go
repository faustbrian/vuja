package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/faustbrian/vuja/commands/ops"
	"github.com/faustbrian/vuja/spec"
)

func TestSshHostGenerator(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	sshDir := filepath.Join(tmp, ".ssh")
	_ = os.MkdirAll(sshDir, 0700)

	configContent := `
Host prod-server
    HostName 10.0.0.1
    User deploy

Host staging bastion
    HostName staging.example.com
    User ubuntu

Host *.internal
    User admin

Host !forbidden wildcard-test
    HostName test.internal
`
	_ = os.WriteFile(filepath.Join(sshDir, "config"), []byte(configContent), 0600)

	results := productionSSHHosts(t)

	found := make(map[string]bool)
	for _, r := range results {
		found[r.Cmd] = true
	}

	if !found["prod-server"] {
		t.Error("expected prod-server in suggestions")
	}
	if !found["staging"] {
		t.Error("expected staging in suggestions")
	}
	if !found["bastion"] {
		t.Error("expected bastion in suggestions")
	}
	if !found["wildcard-test"] {
		t.Error("expected wildcard-test in suggestions")
	}
	if found["*.internal"] || found["!forbidden"] {
		t.Error("wildcards and negations should be ignored")
	}
}

func productionSSHHosts(t *testing.T) []spec.Suggestion {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	provider := spec.Registry["ssh"].Generator
	for ctx.Err() == nil {
		results := provider(ctx, []string{"ssh", ""}, "ssh", "")
		for _, result := range results {
			if result.Cmd == "prod-server" {
				return results
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("production SSH discovery did not publish the fixture inventory")
	return nil
}
