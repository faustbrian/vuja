package root

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const codexResumeHandlerName = "Vuja URL Handler.app"

func codexResumeURLHandlerSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func installCodexResumeURLHandler(binaryPath string) error {
	if binaryPath == "" {
		return errors.New("Vuja binary path is empty")
	}
	switch runtime.GOOS {
	case "darwin":
		return installDarwinCodexResumeURLHandler(binaryPath)
	case "linux":
		return installLinuxCodexResumeURLHandler(binaryPath)
	default:
		return nil
	}
}

func installDarwinCodexResumeURLHandler(binaryPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	applicationsDir := filepath.Join(home, "Applications")
	if err := os.MkdirAll(applicationsDir, 0o755); err != nil {
		return fmt.Errorf("create user Applications directory: %w", err)
	}
	appPath := filepath.Join(applicationsDir, codexResumeHandlerName)
	if err := os.RemoveAll(appPath); err != nil {
		return fmt.Errorf("replace Codex resume URL handler: %w", err)
	}
	if err := runURLHandlerCommand("/usr/bin/osacompile", "-o", appPath, "-e", codexResumeAppleScript(binaryPath)); err != nil {
		return err
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	urlTypes := `[{"CFBundleURLName":"Vuja Actions","CFBundleURLSchemes":["vuja"]}]`
	if err := runURLHandlerCommand("/usr/bin/plutil", "-insert", "CFBundleURLTypes", "-json", urlTypes, plistPath); err != nil {
		return err
	}
	if err := runURLHandlerCommand("/usr/bin/plutil", "-insert", "LSUIElement", "-bool", "YES", plistPath); err != nil {
		return err
	}
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	return runURLHandlerCommand(lsregister, "-f", appPath)
}

func installLinuxCodexResumeURLHandler(binaryPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	applicationsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(applicationsDir, 0o755); err != nil {
		return fmt.Errorf("create applications directory: %w", err)
	}
	desktopPath := filepath.Join(applicationsDir, "vuja-url-handler.desktop")
	if err := os.WriteFile(desktopPath, []byte(codexResumeDesktopEntry(binaryPath)), 0o644); err != nil {
		return fmt.Errorf("write Vuja URL handler: %w", err)
	}
	if xdgMime, err := exec.LookPath("xdg-mime"); err == nil {
		if err := runURLHandlerCommand(xdgMime, "default", filepath.Base(desktopPath), "x-scheme-handler/vuja"); err != nil {
			return err
		}
	}
	if updateDatabase, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = runURLHandlerCommand(updateDatabase, applicationsDir)
	}
	return nil
}

func uninstallCodexResumeURLHandler(home string) {
	if home == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(home, "Applications", codexResumeHandlerName))
	_ = os.Remove(filepath.Join(home, ".local", "share", "applications", "vuja-url-handler.desktop"))
}

func codexResumeAppleScript(binaryPath string) string {
	command := shellSingleQuote(binaryPath) + " open-url "
	return "on open location actionURL\n\tdo shell script " + appleScriptString(command) + " & quoted form of actionURL\nend open location\n"
}

func codexResumeDesktopEntry(binaryPath string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Vuja URL Handler\n" +
		"NoDisplay=true\n" +
		"Exec=" + desktopExecQuote(binaryPath) + " open-url %u\n" +
		"MimeType=x-scheme-handler/vuja;\n"
}

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func desktopExecQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$")
	return `"` + replacer.Replace(value) + `"`
}

func runURLHandlerCommand(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("register Vuja URL handler: %w: %s", err, message)
		}
		return fmt.Errorf("register Vuja URL handler: %w", err)
	}
	return nil
}
