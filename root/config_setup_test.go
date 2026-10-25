package root

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/v2/internal/config"
)

type setupBoundaryWriter struct {
	bytes.Buffer
	beforeConfirm func()
	once          bool
}

func (w *setupBoundaryWriter) Write(data []byte) (int, error) {
	if !w.once && bytes.Contains(data, []byte("Proposed changes:")) {
		w.once = true
		w.beforeConfirm()
	}
	return w.Buffer.Write(data)
}

func TestConfigSetupRejectsChangedOriginalAndBackupFailure(t *testing.T) {
	for _, failure := range []string{"concurrent edit", "backup cannot be created"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "backup cannot be created" && (os.Geteuid() == 0 || runtime.GOOS == "windows") {
				t.Skip("permission-denied fixture requires a non-root Unix user")
			}
			cfg, _ := config.Preset("balanced")
			original, _ := config.Render(cfg)
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			retained := original
			output := &setupBoundaryWriter{beforeConfirm: func() {
				if failure == "concurrent edit" {
					cfg.UI.Chatbox.Prompt = "another edit> "
					retained, _ = config.Render(cfg)
					if err := os.WriteFile(path, retained, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Chmod(dir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				}
			}}
			err := runConfigSetup(strings.NewReader("minimal\n\n\n\n\nyes\n"), output, path)
			if err == nil || strings.Contains(output.String(), "Saved configuration") {
				t.Fatalf("failed setup reported success: %v", err)
			}
			current, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(current, retained) {
				t.Fatal("failed setup replaced the original or concurrent edit")
			}
			backups, _ := filepath.Glob(path + ".bak.*")
			if len(backups) != 0 {
				t.Fatal("failure before backup write created an artifact")
			}
		})
	}
}

func TestConfigSetupEOFAndInvalidChoiceDoNotWrite(t *testing.T) {
	for _, input := range []string{"", "minimal\n", "unknown\n"} {
		cfg, _ := config.Preset("balanced")
		original, _ := config.Render(cfg)
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, original, 0o640); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := runConfigSetup(strings.NewReader(input), &output, path); err == nil {
			t.Fatal("incomplete or invalid choices were accepted")
		}
		current, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || !bytes.Equal(current, original) || info.Mode().Perm() != 0o640 {
			t.Fatal("cancel or invalid input changed config bytes or permissions")
		}
	}
}

func TestConfigSetupCreatesValidatedNewConfigOnlyAfterConfirmation(t *testing.T) {
	for _, confirm := range []string{"yes", "no"} {
		path := filepath.Join(t.TempDir(), "vuja", "config.toml")
		var output bytes.Buffer
		if err := runConfigSetup(strings.NewReader("\n\n\n\n\n"+confirm+"\n"), &output, path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if confirm == "no" {
			if !os.IsNotExist(err) {
				t.Fatal("cancel created new config")
			}
		} else {
			loaded, decodeErr := config.Decode(data)
			if err != nil || decodeErr != nil || loaded.UI.PromptPosition != "bottom" {
				t.Fatalf("confirmed setup did not create validated balanced configuration: %v/%v", err, decodeErr)
			}
		}
	}
}

// Exercise the command boundary with private configuration files, not a
// registration assertion: the workflow must propose before writing anything.
func TestConfigSetupPreservesUnrelatedSettingsAndBacksUpOriginal(t *testing.T) {
	for _, confirm := range []string{"no", "yes"} {
		t.Run(confirm, func(t *testing.T) {
			cfg, err := config.Preset("balanced")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Core.Debug = true
			cfg.UI.Chatbox.Prompt = "custom> "
			cfg.AI.Providers = map[string]config.ProviderConfig{"private": {APIKey: "fixture-secret-never-preview"}}
			original, err := config.Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			command, remaining, err := ConfigCmd.Find([]string{"setup", path})
			if err != nil || command == ConfigCmd || command.RunE == nil {
				t.Fatalf("guided configuration workflow is unavailable: %v", err)
			}
			var output bytes.Buffer
			command.SetIn(strings.NewReader("minimal\nbottom\ncontent-width\npinned\nserein-day\n" + confirm + "\n"))
			command.SetOut(&output)
			t.Cleanup(func() { command.SetIn(nil); command.SetOut(nil) })
			if err := command.RunE(command, remaining); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "fixture-secret-never-preview") {
				t.Fatal("setup disclosed provider credential")
			}
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(path + ".bak.*")
			if err != nil {
				t.Fatal(err)
			}
			if confirm == "no" {
				if !bytes.Equal(current, original) || len(backups) != 0 {
					t.Fatal("cancel changed original configuration or created a backup")
				}
				return
			}
			if !strings.Contains(output.String(), "ui.density") || !strings.Contains(output.String(), "Proposed") {
				t.Fatal("setup did not show the proposed changes")
			}
			loaded, err := config.Decode(current)
			if err != nil {
				t.Fatal(err)
			}
			if !loaded.Core.Debug || loaded.UI.Chatbox.Prompt != "custom> " || loaded.AI.Providers["private"].APIKey != "fixture-secret-never-preview" {
				t.Fatal("setup overwrote unrelated settings")
			}
			if loaded.UI.Density != "compact" || loaded.UI.Chatbox.SurfaceWidth != "content-width" || loaded.UI.Palette != "serein-day" {
				t.Fatal("setup did not apply the selected choices")
			}
			if len(backups) != 1 {
				t.Fatalf("expected one original backup, got %v", backups)
			}
			backup, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("backup is not the exact original bytes")
			}
			for _, name := range []string{path, backups[0]} {
				info, err := os.Stat(name)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("unsafe saved config or backup: %v", err)
				}
			}
		})
	}
}
