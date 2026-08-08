package root

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/faustbrian/vuja/commands"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	rootCmd = &cobra.Command{
		Use:   "vuja",
		Short: "Predictive completion for your shell",
		Long: `Vuja combines command-aware completion with personalized history
ranking in an inline terminal suggestion menu.`,
		Run: func(cmd *cobra.Command, args []string) {
			defer func() {
				if r := recover(); r != nil {
					WriteCrashLog(r)
					restoreTerminal()
					printCrashNotice()
					startRescueShell()
					os.Exit(2)
				}
			}()
			if pidStr := os.Getenv("VUJA_PID"); pidStr != "" {
				if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
					if logDir, err := config.CachePath(); err == nil {
						if config.EnsurePrivateDir(logDir) == nil {
							argsFile := filepath.Join(logDir, "reload-args")
							_ = config.WritePrivateFile(argsFile, []byte(strings.Join(os.Args[1:], "\n")))
						}
					}
					_ = syscall.Kill(pid, syscall.SIGUSR1)
					fmt.Println("\r\033[K\033[36m[VUJA] Sent reload signal to parent session.\033[0m")
					return
				}
			}
			runWrapper()
		},
	}
	shellFlag string
	debugMode bool
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&shellFlag, "shell", "s", "", "shell to use (bash, zsh, fish)")
	rootCmd.PersistentFlags().BoolVarP(&debugMode, "debug", "d", false, "enable debug logging to vuja.log")

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if shellFlag != "" {
			config.Get().Core.Shell = shellFlag
		}
		logDir, err := config.CachePath()
		if err == nil {
			logger.Init(filepath.Join(logDir, "vuja.log"), debugMode || config.Get().Core.Debug)
			logger.Infof("VUJA session started: os=%s, arch=%s, go=%s, pid=%d", runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getpid())
			cfg := config.Get()
			logger.Debugf("VUJA loaded config: shell=%q, mode=%q, ghost-text=%v, max-suggestions=%d", cfg.Core.Shell, cfg.Core.Mode, cfg.UI.GhostText, cfg.UI.MaxSuggestions)
		}
	}
}

// runWatchdog spawns the watchdog parent process
func runWatchdog() {
	exe, err := os.Executable()
	if err != nil {
		runOriginal()
		return
	}

	// save original terminal settings in parent process
	watchdogOldState, errState := term.MakeRaw(int(os.Stdin.Fd()))
	if errState == nil {
		_ = term.Restore(int(os.Stdin.Fd()), watchdogOldState)
	}

	r, w, err := os.Pipe()
	if err != nil {
		runOriginal()
		return
	}

	cmd := exec.CommandContext(context.Background(), exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "VUJA_IS_CHILD=true")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = w

	err = cmd.Start()
	if err != nil {
		runOriginal()
		return
	}

	_ = w.Close()

	// copy child stderr to both our buffer and the real stderr, filtering out panics
	var stderrBuf bytes.Buffer
	origStderr := os.Stderr
	tempBuf := make([]byte, 1024)
	suppress := false
	for {
		n, errRead := r.Read(tempBuf)
		if n > 0 {
			_, _ = stderrBuf.Write(tempBuf[:n])
			if stderrBuf.Len() > 64*1024 {
				// discard oldest bytes to avoid memory leak
				over := stderrBuf.Len() - 64*1024
				_ = stderrBuf.Next(over)
			}
			if !suppress {
				currentContent := stderrBuf.Bytes()
				searchStart := 0
				if len(currentContent) > n+12 {
					searchStart = len(currentContent) - (n + 12)
				}
				searchSlice := currentContent[searchStart:]
				idxPanic := bytes.Index(searchSlice, []byte("panic:"))
				idxFatal := bytes.Index(searchSlice, []byte("fatal error:"))
				triggerIdx := -1
				if idxPanic != -1 {
					triggerIdx = searchStart + idxPanic
				} else if idxFatal != -1 {
					triggerIdx = searchStart + idxFatal
				}

				if triggerIdx != -1 {
					suppress = true
					printedLen := len(currentContent) - n
					if triggerIdx > printedLen {
						_, _ = origStderr.Write(currentContent[printedLen:triggerIdx])
					}
				} else {
					_, _ = origStderr.Write(tempBuf[:n])
				}
			}
		}
		if errRead != nil {
			break
		}
	}

	// check if child exited abnormally or crashed
	errWait := cmd.Wait()
	if errWait != nil {
		content := stderrBuf.Bytes()
		if bytes.Contains(content, []byte("panic:")) || bytes.Contains(content, []byte("fatal error:")) {
			WriteCrashLog(string(content))
			// restore terminal state if watchdog saved it
			if watchdogOldState != nil {
				_ = term.Restore(int(os.Stdin.Fd()), watchdogOldState)
			}
			printCrashNotice()
			startRescueShell()
			os.Exit(2)
		}

		var exitErr *exec.ExitError
		if errors.As(errWait, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

// runOriginal runs the normal command execution
func runOriginal() {
	if os.Getenv("VUJA_RELOADED") == "true" {
		fmt.Printf("\r\033[K\033[35m[VUJA] reloading...\033[0m\n")
		_ = os.Unsetenv("VUJA_RELOADED")
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func Execute() {
	_ = config.MigrateFromLegacyJSON()
	cfg, err := loadRuntimeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[VUJA] config error: %v\n", err)
	}
	config.Init(cfg)

	if os.Getenv("VUJA_IS_CHILD") != "true" && directExecutionCommand(os.Args[1:]) {
		runOriginal()
		return
	}
	if os.Getenv("VUJA_IS_CHILD") != "true" {
		runWatchdog()
		return
	}

	runOriginal()
}

func directExecutionCommand(args []string) bool {
	command, _, err := rootCmd.Find(args)
	return err == nil && command != rootCmd
}

func loadRuntimeConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.DefaultConfig(), err
	}
	config.ApplyVisualPolicy(cfg)
	return cfg, nil
}
