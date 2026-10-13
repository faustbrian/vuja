package root

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/faustbrian/vuja/internal/config"
)

// Shared facts serve the existing detailed doctor and the allowlisted support
// report. Raw paths/errors are deliberately confined to the detailed doctor.
type doctorInspection struct {
	cfg                                     *config.Config
	configPath                              string
	configErr                               error
	shell, startupPath, hookPath, hookState string
	startupAvailable, autostart             bool
	conflicts                               []string
	term                                    string
	interactive, truecolor, utf8            bool
	columns                                 int
}

func inspectDoctor() doctorInspection {
	d := doctorInspection{cfg: config.DefaultConfig(), hookState: "unavailable"}
	d.configPath, d.configErr = config.ConfigPath()
	if d.configErr == nil {
		data, err := readDiagnosticFile(d.configPath)
		if err == nil {
			var cfg *config.Config
			cfg, err = config.Decode(data)
			if err == nil {
				d.cfg = cfg
			}
		}
		d.configErr = err
	}
	d.shell = d.cfg.Core.Shell
	if d.shell == "" {
		d.shell = filepath.Base(os.Getenv("SHELL"))
	}
	home, _ := os.UserHomeDir()
	rcNames := map[string]string{"zsh": ".zshrc", "bash": ".bashrc", "fish": filepath.Join(".config", "fish", "config.fish")}
	d.startupPath = filepath.Join(home, rcNames[d.shell])
	if data, err := readDiagnosticFile(d.startupPath); err == nil {
		d.startupAvailable = true
		text := activeShellConfiguration(string(data))
		d.autostart = strings.Contains(text, ".local/share/vuja/init.")
		for name, marker := range map[string]string{"starship": "starship init", "powerlevel10k": "powerlevel10k", "oh-my-posh": "oh-my-posh", "spaceship": "spaceship", "pure": "prompt pure"} {
			if strings.Contains(strings.ToLower(text), marker) {
				d.conflicts = append(d.conflicts, name)
			}
		}
		sort.Strings(d.conflicts)
	}
	d.hookPath = filepath.Join(home, ".local", "share", "vuja", "init."+d.shell)
	if data, err := readDiagnosticFile(d.hookPath); err == nil && len(data) > 0 {
		d.hookState = "stale"
		if strings.Contains(string(data), "VUJA_CMD_START") && strings.Contains(string(data), "prompt-start") {
			d.hookState = "current"
		}
	}
	d.term = os.Getenv("TERM")
	d.interactive = d.term != "" && d.term != "dumb"
	color := strings.ToLower(os.Getenv("COLORTERM"))
	d.truecolor = strings.Contains(color, "truecolor") || strings.Contains(color, "24bit")
	locale := strings.ToLower(os.Getenv("LC_ALL") + " " + os.Getenv("LC_CTYPE") + " " + os.Getenv("LANG"))
	d.utf8 = strings.Contains(locale, "utf-8") || strings.Contains(locale, "utf8")
	d.columns, _ = strconv.Atoi(os.Getenv("COLUMNS"))
	return d
}
