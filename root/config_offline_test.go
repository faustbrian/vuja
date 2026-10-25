package root

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/config"
)

func TestConfigOfflineEntryOwnsPreviewAndSetupBeforeRuntimeBootstrap(t *testing.T) {
	for _, kind := range []string{"preset", "custom", "cancel", "eof", "help", "invalid flag"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("VUJA_RELOADED", "true")
			canonical, _ := config.ConfigPath()
			if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(canonical, []byte("unrelated-invalid-config = [\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(home, ".vuja"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".vuja", "state.json"), []byte(`{"mode":"history"}`), 0o640); err != nil {
				t.Fatal(err)
			}
			cfg, _ := config.Preset("balanced")
			content, _ := config.Render(cfg)
			target := filepath.Join(home, "chosen.toml")
			if err := os.WriteFile(target, content, 0o640); err != nil {
				t.Fatal(err)
			}
			before := configFixtureTree(t, home)
			args := []string{"config", "preview", "--preset", "minimal", "--width", "60"}
			switch kind {
			case "custom":
				args = []string{"--debug", "config", "preview", target, "--scenario", "multiline"}
			case "cancel", "eof":
				args = []string{"--shell", "fish", "config", "setup", target}
			case "help":
				args = []string{"config", "preview", "--help"}
			case "invalid flag":
				args = []string{"config", "preview", "--unsupported"}
			}
			var output bytes.Buffer
			// Execute itself is prohibited; exercise its production finite
			// early-dispatch boundary and independently review the linkage.
			input := ""
			if kind == "cancel" {
				input = "keep\n\n\n\n\nno\n"
			}
			handled, err := executeConfigToolsInvocation(args, strings.NewReader(input), &output)
			if !handled {
				t.Fatalf("%s falls through to mutating runtime bootstrap", kind)
			}
			if kind != "invalid flag" && err != nil {
				t.Fatal(err)
			}
			if kind == "invalid flag" && err == nil {
				t.Fatal("invalid preview flag silently accepted")
			}
			if !reflect.DeepEqual(before, configFixtureTree(t, home)) {
				t.Fatal("offline command changed config modes/bytes, legacy state, cache or logs")
			}
			if os.Getenv("VUJA_RELOADED") != "true" {
				t.Fatal("offline entry consumed reload state")
			}
			if kind == "preset" && !strings.Contains(output.String(), "minimal") {
				t.Fatal("preset preview missing")
			}
		})
	}
}

func configFixtureTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		result[relative] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[relative] += string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConfigOfflineEntryUsesFreshFlagsAndRejectsUnrelatedCommands(t *testing.T) {
	for _, test := range []struct {
		args      []string
		want      string
		wantError bool
	}{
		{[]string{"config", "preview", "--preset", "minimal", "--width", "40"}, "minimal", false},
		{[]string{"config", "preview"}, "width 120", false},
		{[]string{"config", "preview", "--day", "--night"}, "", true},
		{[]string{"config", "preview", "--preset", "balanced", "config.toml"}, "", true},
		{[]string{"config", "preview", "one.toml", "two.toml"}, "", true},
	} {
		var output bytes.Buffer
		handled, err := executeConfigToolsInvocation(test.args, strings.NewReader(""), &output)
		if !handled || (err != nil) != test.wantError {
			t.Fatalf("wrong offline argument handling: handled=%v err=%v", handled, err)
		}
		if !test.wantError && !strings.Contains(output.String(), test.want) {
			t.Fatalf("wrong preview after flag reuse: %q", output.String())
		}
	}
	for _, args := range [][]string{nil, {"history", "stats"}, {"config", "migrate"}, {"config", "validate"}, {"debug", "diagnostics"}} {
		if handled, err := executeConfigToolsInvocation(args, strings.NewReader(""), &bytes.Buffer{}); handled || err != nil {
			t.Fatal("offline config entry took ownership of an unrelated command")
		}
	}
}
