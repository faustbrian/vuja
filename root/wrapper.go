package root

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/integration/shell"
	"github.com/faustbrian/vuja/internal/ai"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/internal/workspace"
	"github.com/faustbrian/vuja/spec"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var (
	prevRecordedCommand string
	prevCmdCwd          string
	prevCmdMu           sync.Mutex
)

func getPrevSkeleton() string {
	prevCmdMu.Lock()
	defer prevCmdMu.Unlock()
	if prevRecordedCommand == "" {
		return ""
	}
	return scoring.ExtractSkeleton(prevRecordedCommand)
}

func getPrevCommandSignals() (string, string) {
	prevCmdMu.Lock()
	defer prevCmdMu.Unlock()
	command := strings.TrimSpace(prevRecordedCommand)
	if command == "" {
		return "", ""
	}
	return command, scoring.ExtractSkeleton(command)
}

func getPrevRecordedInfo() (string, string) {
	prevCmdMu.Lock()
	defer prevCmdMu.Unlock()
	if prevRecordedCommand == "" {
		return "", ""
	}
	return scoring.ExtractSkeleton(prevRecordedCommand), prevCmdCwd
}

func setPrevRecordedInfo(cmd, cwd string) {
	prevCmdMu.Lock()
	defer prevCmdMu.Unlock()
	prevRecordedCommand = cmd
	prevCmdCwd = cwd
}

func loadMode() string {
	mode := config.Get().Core.Mode
	if mode == "last" {
		state := config.LoadState()
		if state.LastMode == "history" || state.LastMode == "spec" {
			return state.LastMode
		}
		return "spec"
	}
	if mode == "history" || mode == "spec" {
		return mode
	}
	return "spec"
}

func saveMode(mode string) {
	state := config.LoadState()
	state.LastMode = mode
	_ = config.SaveState(state)
}

var (
	oldState     *term.State
	oldStateMu   sync.Mutex
	activeMode   string
	activeModeMu sync.RWMutex
)

// restoreTerminal restores the terminal state if needed
func restoreTerminal() {
	oldStateMu.Lock()
	defer oldStateMu.Unlock()
	if oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		oldState = nil
	}
}

// runWrapper sets up the pty environment, launches the shell,
// and manages the main input loop to provide real-time suggestions
// it handles raw terminal mode to intercept keystrokes and
// coordinates between the shell process and the suggestion overlay
func runWrapper() {
	isDarkBackground := detectDarkBackground()
	lipgloss.SetHasDarkBackground(isDarkBackground)
	var naiveBuffer string
	var lastSubmittedCommand string
	var feedback suggestionFeedbackSession
	var historyNav historyNavigation
	var historySearch historySearchSession
	var tabNav tabNavigation
	inputBindings := newInputKeybindings(config.Get().Keybindings)
	host, _ := os.Hostname()
	executionTracker := newCommandExecutionTracker(
		time.Now,
		fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()),
		host,
	)
	initialWidth, initialHeight, terminalSizeErr := term.GetSize(int(os.Stdout.Fd()))
	terminalMarkerID := ""
	if config.Get().UI.PromptPosition == "bottom" &&
		terminalSizeErr == nil &&
		initialWidth > 0 &&
		initialHeight > 1 &&
		!strings.EqualFold(os.Getenv("TERM"), "dumb") {
		terminalMarkerID = newTerminalMarkerID()
	}
	cursorOffset := 0
	var bufferMu sync.Mutex
	var userNavigated atomic.Bool
	var renderMenuNow func()
	var renderMenuNowMu sync.RWMutex

	r, w, err := os.Pipe() // pipe for ipc communication from shell to vuja
	if err != nil {
		return
	}

	var shellName string
	if active := os.Getenv("VUJA_ACTIVE_SHELL"); active != "" {
		shellName = active
		_ = os.Unsetenv("VUJA_ACTIVE_SHELL")
	} else if shellFlag != "" {
		shellName = shellFlag
	} else {
		shellName = detectShell()
	}

	shell.Init(shellName)
	go importPersistentHistory()
	adapter := shell.Current

	ctx := context.Background()
	c := exec.CommandContext(ctx, adapter.GetShellPath())
	c.ExtraFiles = make([]*os.File, 11)
	// pass write end of pipe to shell as fd 13 (since index 10 maps to 13)
	c.ExtraFiles[10] = w
	c.Env = adapter.GetEnv(13, os.Getpid())
	c.Env = slices.DeleteFunc(c.Env, func(value string) bool {
		return strings.HasPrefix(value, "VUJA_MARKER=")
	})
	if terminalMarkerID != "" {
		c.Env = append(c.Env, "VUJA_MARKER="+terminalMarkerID)
		c.Env = append(c.Env,
			"VUJA_MANAGED_PROMPT=1",
			"VUJA_PROMPT_TEXT="+config.Get().UI.Chatbox.Prompt,
		)
	}

	ptmx, err := pty.Start(c)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "[VUJA] failed to start PTY: %v\n", err)
		return
	}
	defer func() { _ = ptmx.Close() }()

	_ = pty.InheritSize(os.Stdin, ptmx)
	spec.ShellPID = c.Process.Pid

	logger.Infof("PTY child shell started: shell=%s, path=%s, pid=%d", shellName, adapter.GetShellPath(), c.Process.Pid)

	// put terminal in raw mode to intercept every keystroke
	var errMakeRaw error
	oldState, errMakeRaw = term.MakeRaw(int(os.Stdin.Fd()))
	if errMakeRaw != nil {
		logger.Errorf("Failed to set terminal raw mode: %v", errMakeRaw)
		panic(errMakeRaw)
	}
	logger.Debugf("Terminal set to raw mode successfully")
	defer restoreTerminal()

	display := newTerminalCompositor(
		os.Stdout,
		config.Get().UI.PromptPosition,
		terminalMarkerID,
		initialWidth,
		initialHeight,
	)
	inputBoxPalette := config.Get().UI.Colors.Day
	if isDarkBackground {
		inputBoxPalette = config.Get().UI.Colors.Night
	}
	display.SetInputBoxTheme(terminalInputBoxTheme{
		Background:                 inputBoxPalette.Background,
		SurfaceBackground:          inputBoxPalette.SurfaceBackground,
		CompletedSurfaceBackground: inputBoxPalette.CompletedSurfaceBackground,
		StatusBackground:           inputBoxPalette.StatusBackground,
		StatusText:                 inputBoxPalette.StatusText,
		Border:                     inputBoxPalette.Border,
		Accent:                     inputBoxPalette.Accent,
		Muted:                      inputBoxPalette.Muted,
	})
	display.SetChatboxConfig(terminalChatboxConfig{
		Prompt:          config.Get().UI.Chatbox.Prompt,
		Separator:       config.Get().UI.Chatbox.Separator,
		Scrollback:      config.Get().UI.Chatbox.Scrollback,
		PathColorMode:   config.Get().UI.Chatbox.PathColorMode,
		PathMaxSegments: config.Get().UI.Chatbox.PathMaxSegments,
		HistorySpacing:  config.Get().UI.Chatbox.HistorySpacing,
		Title: terminalChatboxBarConfig{
			Left: config.Get().UI.Chatbox.TitleLeft, Center: config.Get().UI.Chatbox.TitleCenter, Right: config.Get().UI.Chatbox.TitleRight,
		},
		Status: terminalChatboxBarConfig{
			Left: config.Get().UI.Chatbox.StatusLeft, Center: config.Get().UI.Chatbox.StatusCenter, Right: config.Get().UI.Chatbox.StatusRight,
		},
		Colors:           terminalChatboxColors(config.Get().UI.Chatbox.Colors),
		CollapseVersions: config.Get().UI.Chatbox.CollapseVersions,
	})
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		display.SetInputBoxPath(cwd)
	}
	defer display.Close()
	statusSegments := config.Get().UI.Chatbox.Segments()
	managedStatus := display.Managed()
	status := newStatusEngine(statusEngineOptions{
		OnUpdate: display.SetStatusSnapshot,
		Metrics: managedStatus &&
			(slices.Contains(statusSegments, "cpu") || slices.Contains(statusSegments, "memory")),
		GitLines:    managedStatus && slices.Contains(statusSegments, "git-lines"),
		Session:     managedStatus && slices.Contains(statusSegments, "session"),
		GitStash:    managedStatus && slices.Contains(statusSegments, "git-stash"),
		Package:     managedStatus && slices.Contains(statusSegments, "package"),
		Contexts:    managedStatus && slices.Contains(statusSegments, "contexts"),
		Environment: managedStatus && slices.Contains(statusSegments, "environment"),
		SkipVersions: managedStatus &&
			!slices.Contains(statusSegments, "versions") && !slices.Contains(statusSegments, "version-mismatch"),
	})
	defer status.Close()
	if managedStatus {
		status.StartMetrics(time.Duration(config.Get().UI.Chatbox.RefreshInterval))
		status.Refresh(spec.GetCWD())
	}
	writeNotification := display.WriteNotification
	overlay := integration.NewOverlay(isDarkBackground)
	overlay.SetBottomPrompt(display.Enabled())
	overlay.SetPromptRows(display.PromptRows())
	display.SetTransientUIReflow(
		func() []byte {
			if !overlay.IsVisible() {
				return nil
			}
			return []byte(overlay.Clear())
		},
		func(bottom bool, promptRows int) {
			overlay.SetBottomPrompt(bottom)
			overlay.SetPromptRows(promptRows)
			overlay.InvalidateGeometry()
		},
		func() []byte {
			if !overlay.IsVisible() {
				return nil
			}
			return []byte(overlay.Render())
		},
		func() []byte { return []byte(overlay.ClearAndDisable()) },
		overlay.BottomRegion,
		overlay.IsVisible,
	)
	uiPresenter := newTerminalUIPresenter(display.ComposeUI)
	presentOverlay := func(render func() string) {
		uiPresenter.Present(func() []byte { return []byte(render()) })
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGWINCH, syscall.SIGUSR1, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				WriteCrashLog(r)
				display.Close()
				restoreTerminal()
				printCrashNotice()
				startRescueShell()
				os.Exit(2)
			}
		}()
		for s := range sigCh {
			switch s {
			case syscall.SIGWINCH:
				logger.Debugf("Received SIGWINCH terminal resize signal")
				if width, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
					display.Resize(width, height)
				}
				_ = pty.InheritSize(os.Stdin, ptmx) // handle terminal window resize
			// this is the core feature of reloading
			// it helps VUJA reload itself that you dont need to restart the shell manually
			// SIGUSR1 is the signal to active reload when you type "just reload"
			case syscall.SIGUSR1:
				// trigger vuja reload by executing itself again
				exe, _ := os.Executable()
				_ = os.Setenv("VUJA_RELOADED", "true")

				innerShell := getActiveInnerShell(c.Process.Pid, shellName)
				if innerShell != "" {
					// to detect which is last shell (bash, zsh, fish)
					_ = os.Setenv("VUJA_ACTIVE_SHELL", innerShell)
				}

				if c.Process != nil {
					cwd, linkErr := os.Readlink(fmt.Sprintf("/proc/%d/cwd", c.Process.Pid))
					if linkErr != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
						out, errCmd := exec.CommandContext(ctx, "lsof", "-p", fmt.Sprintf("%d", c.Process.Pid), "-a", "-d", "cwd", "-F", "n").Output()
						cancel()
						if errCmd == nil {
							for line := range strings.SplitSeq(string(out), "\n") {
								if strings.HasPrefix(line, "n") {
									cwd = strings.TrimSpace(line[1:])
									linkErr = nil
									break
								}
							}
						}
					}
					if linkErr == nil {
						_ = os.Chdir(cwd)
					}
					_ = syscall.Kill(c.Process.Pid, syscall.SIGKILL)
					_ = ptmx.Close()
				}

				display.Close()
				restoreTerminal()
				execArgs := []string{os.Args[0]}
				if logDir, pathErr := config.CachePath(); pathErr == nil {
					argsFile := filepath.Join(logDir, "reload-args")
					if data, readErr := os.ReadFile(argsFile); readErr == nil {
						lines := strings.SplitSeq(string(data), "\n")
						for line := range lines {
							trimmed := strings.TrimSpace(line)
							if trimmed != "" {
								execArgs = append(execArgs, trimmed)
							}
						}
						_ = os.Remove(argsFile)
					} else {
						execArgs = os.Args
					}
				} else {
					execArgs = os.Args
				}
				_ = syscall.Exec(exe, execArgs, os.Environ())
			default:
				display.Close()
				restoreTerminal()
				if c.Process != nil {
					_ = c.Process.Signal(s)
				}
				_ = ptmx.Close()
				os.Exit(128 + int(s.(syscall.Signal)))
			}
		}
	}()

	// start background update check (async)
	pendingUpdate = startBackgroundUpdateCheck()
	updatePrinted := false

	shellPGID, err := unix.Getpgid(spec.ShellPID)
	if err != nil {
		shellPGID = spec.ShellPID
	}
	var isCommandActive atomic.Bool
	isExecuting := func() bool {
		if isCommandActive.Load() {
			return true
		}
		pgrp, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPGRP)
		if err != nil {
			return false
		}
		return pgrp != shellPGID
	}

	// bridge pty output to actual stdout
	go func() {
		defer func() {
			if r := recover(); r != nil {
				WriteCrashLog(r)
				display.Close()
				restoreTerminal()
				printCrashNotice()
				startRescueShell()
				os.Exit(2)
			}
		}()
		var lastPromptBuf []byte
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				if err == io.EOF {
					display.Close()
					restoreTerminal()
					os.Exit(0)
				}
				continue
			}
			display.WritePTY(buf[:n])

			bufferMu.Lock()
			nbEmpty := naiveBuffer == ""
			navigated := userNavigated.Load()
			bufferMu.Unlock()

			if isExecuting() {
				lastPromptBuf = nil
			} else if nbEmpty && !navigated {
				lastPromptBuf = append(lastPromptBuf, buf[:n]...)
				if idx := bytes.LastIndexByte(lastPromptBuf, '\n'); idx >= 0 {
					lastPromptBuf = append([]byte(nil), lastPromptBuf[idx+1:]...)
				}
				pLen := integration.ComputeCursorCol(lastPromptBuf)
				if pLen >= 0 {
					uiPresenter.Update(func(_ func(func() []byte)) {
						overlay.SetPromptLen(pLen)
					})
				}
			}
		}
	}()

	var disableGhostText atomic.Bool
	disableGhostText.Store(!config.Get().UI.GhostText)
	var renderOverlay func()
	var renderHistorySearch func()

	// listen for suggestion requests from shell scripts via the ipc pipe
	go func() {
		defer func() {
			if r := recover(); r != nil {
				WriteCrashLog(r)
				display.Close()
				restoreTerminal()
				printCrashNotice()
				startRescueShell()
				os.Exit(2)
			}
		}()
		scanner := bufio.NewScanner(r)
		scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
			if atEOF && len(data) == 0 {
				return 0, nil, nil
			}
			if i := bytes.IndexByte(data, '\x00'); i >= 0 {
				return i + 1, data[0:i], nil
			}
			if atEOF {
				return len(data), data, nil
			}
			return 0, nil, nil
		})

		for scanner.Scan() {
			query := scanner.Text()

			if update, ok := parseShellStatusMessage(query); ok {
				status.SetShellStatus(update)
				continue
			}

			if handleShellControlMessage(query) {
				display.SetInputBoxPath(spec.GetCWD())
				if managedStatus {
					status.Refresh(spec.GetCWD())
				}
				continue
			}

			if query == "VUJA_CMD_START" {
				executionTracker.Start()
				feedback.reset()
				historyNav.Cancel()
				historySearch.Close()
				isCommandActive.Store(true)
				bufferMu.Lock()
				naiveBuffer = ""
				cursorOffset = 0
				bufferMu.Unlock()
				presentOverlay(overlay.ClearAndDisable)
				SetCurrentAISuggestion(nil)
				continue
			}

			if query == "VUJA_CMD_STOP" || strings.HasPrefix(query, "VUJA_CMD_STOP:") {
				exitCode := 0
				if after, ok := strings.CutPrefix(query, "VUJA_CMD_STOP:"); ok {
					if code, err := strconv.Atoi(after); err == nil {
						exitCode = code
					}
				}
				isCommandActive.Store(false)
				SetCurrentAISuggestion(nil)
				bufferMu.Lock()
				cmdToRecord := lastSubmittedCommand
				lastSubmittedCommand = ""
				bufferMu.Unlock()
				if cmdToRecord != "" {
					cwd := spec.GetCWD()
					historyEntry := executionTracker.Finish(cmdToRecord, cwd, exitCode)
					if managedStatus {
						status.SetCommandResult(exitCode, historyEntry.Duration)
						status.RefreshAfterCommand(cwd)
					}
					if !policy.IsSensitive(cmdToRecord) {
						integration.RecordSessionCommandAt(cmdToRecord, cwd)
						integration.AppendRichHistoryEntry(historyEntry)
						prevCommand, prevSkeleton := getPrevCommandSignals()
						currSkeleton := scoring.ExtractSkeleton(cmdToRecord)
						go func(c, d string, code int, entry integration.HistoryEntry, pCmd, pSkel, cSkel string) {
							defer func() {
								if r := recover(); r != nil {
									WriteCrashLog(r)
								}
							}()
							ctxRecord, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
							defer cancel()
							if store, err := scoring.GetFrecencyStore(); err == nil && store != nil {
								_ = store.Record(ctxRecord, c, d, code)
								_ = store.RecordHistoryEvent(ctxRecord, scoring.HistoryEvent{
									EventKey: entry.ID, Command: entry.Command, Cwd: entry.Cwd,
									StartedAt: entry.StartedAt, Duration: entry.Duration,
									ExitCode: entry.ExitCode, HasExitCode: entry.HasExitCode,
									Source: entry.Source, Host: entry.Host, SessionID: entry.SessionID,
								})
								_ = store.RecordDirectory(ctxRecord, d)
								if root := workspace.DetectCached(d).Root; root != "" && root != d {
									_ = store.RecordDirectory(ctxRecord, root)
								}
								if pSkel != "" && cSkel != "" {
									_ = store.RecordTransition(ctxRecord, pSkel, cSkel, d, code)
								}
								if pCmd != "" {
									_ = store.RecordExactTransition(ctxRecord, pCmd, c, d, code)
								}
							}
						}(cmdToRecord, cwd, exitCode, historyEntry, prevCommand, prevSkeleton, currSkeleton)
						setPrevRecordedInfo(cmdToRecord, cwd)
					}
				}
				// hook: after user executes a command, print the update notice exactly once per session
				if !updatePrinted {
					select {
					case result, ok := <-pendingUpdate:
						if ok && result.hasUpdate {
							writeNotification(updateNotice(result.latestVersion))
							updatePrinted = true
						}
					default:
					}
				}
				continue
			}

			isCommandActive.Store(false)
			display.SetCommandContext(query)

			if historySearch.Active() || overlay.GetUserNavigated() {
				continue
			}

			if query == "" {
				bufferMu.Lock()
				wasEmpty := naiveBuffer == ""
				naiveBuffer = ""
				cursorOffset = 0
				bufferMu.Unlock()
				if !wasEmpty {
					presentOverlay(overlay.ClearAndDisable)
					SetCurrentAISuggestion(nil)
				}
				continue
			}

			bufferMu.Lock()
			if naiveBuffer == query {
				bufferMu.Unlock()
				continue
			}
			naiveBuffer = query
			cursorOffset = 0
			bufferMu.Unlock()

			renderOverlay()
		}
		if err := scanner.Err(); err != nil {
			logger.Errorf("IPC scanner error: %v", err)
		}
	}()

	suggestionsEnabled := true
	activeModeMu.Lock()
	activeMode = loadMode()
	activeModeMu.Unlock()

	presentOverlay(overlay.Clear)

	var renderTimer *time.Timer
	var renderMu sync.Mutex
	var aiTimer *time.Timer
	var aiCancel context.CancelFunc
	var aiMu sync.Mutex

	renderMenuNowMu.Lock()
	renderMenuNow = func() {
		uiPresenter.Update(func(compose func(func() []byte)) {
			if isExecuting() {
				return
			}

			// copy state safely inside timer
			bufferMu.Lock()
			bufCopy := naiveBuffer
			offsetCopy := cursorOffset
			bufferMu.Unlock()

			activeModeMu.RLock()
			modeCopy := activeMode
			activeModeMu.RUnlock()

			navCopy := userNavigated.Load()

			runes := []rune(bufCopy)
			if offsetCopy > 0 && offsetCopy <= len(runes) {
				bufCopy = string(runes[:len(runes)-offsetCopy])
			}

			aiMu.Lock()
			if aiTimer != nil {
				aiTimer.Stop()
			}
			if aiCancel != nil {
				aiCancel()
				aiCancel = nil
			}
			if config.Get().AI.Enabled && bufCopy != "" && !navCopy && offsetCopy == 0 {
				queryTarget := bufCopy
				debounceMS := config.Get().AI.DebounceMS
				if debounceMS <= 0 {
					debounceMS = 500
				}
				aiTimer = time.AfterFunc(time.Duration(debounceMS)*time.Millisecond, func() {
					// Require at least 3 characters to trigger AI completion to save API quota and avoid 6000 TPM limit (Groq api docs)
					if len(strings.TrimSpace(queryTarget)) < 3 {
						return
					}
					aiMu.Lock()
					ctx, cancel := context.WithCancel(context.Background())
					aiCancel = cancel
					aiMu.Unlock()
					defer cancel()

					cwd := spec.GetCWD()
					var recentCmds []string
					var lastCmd string
					if hist, err := integration.SearchHistory("", nil); err == nil {
						// Limit to 3 recent commands to keep prompt concise and reduce token consumption
						for i := 0; i < len(hist) && i < 3; i++ {
							recentCmds = append(recentCmds, hist[i].Cmd)
						}
						if len(recentCmds) > 0 {
							lastCmd = recentCmds[0]
						}
					}
					env := ai.NewEnvSnapshot(cwd, lastCmd, 0, recentCmds)
					sugg, err := GetAIEngine().Suggest(ctx, queryTarget, env, "")
					if err != nil || sugg == nil || ctx.Err() != nil {
						return
					}
					SetCurrentAISuggestion(sugg)
					injected := false
					uiPresenter.Update(func(_ func(func() []byte)) {
						injected = overlay.InjectAISuggestion(*sugg)
					})
					if injected {
						renderOverlay()
					}
				})
			}
			aiMu.Unlock()

			if !navCopy {
				if bufCopy == "" && !overlay.IsVisible() {
					compose(func() []byte { return []byte(overlay.ClearAndDisable()) })
					return
				}
				logger.Debugf("Render query: '%s', mode: %s", bufCopy, modeCopy)
				results := MergeResults(bufCopy, modeCopy)
				logger.Debugf("Render results found: %d", len(results))

				if len(results) == 0 || (len(results) == 1 && strings.TrimSpace(results[0].Cmd) == strings.TrimSpace(bufCopy) && !strings.HasSuffix(bufCopy, " ")) {
					compose(func() []byte { return []byte(overlay.HideMenu(bufCopy)) })
					return
				}

				feedback.offer(results[0].Cmd)
				compose(func() []byte {
					var output strings.Builder
					if overlay.IsVisible() {
						output.WriteString(overlay.ClearForRedraw())
					}
					overlay.SetQueryAndItems(bufCopy, results)
					overlay.SetUserNavigated(navCopy)
					if !disableGhostText.Load() {
						output.WriteString(overlay.RenderGhostText(bufCopy, navCopy, offsetCopy == 0))
					}
					currentCmd := overlay.GetCurrentCmd()
					logger.Debugf("RenderOverlay nav: %v, typedQuery: '%s', currentCmd: '%s'", navCopy, overlay.GetTypedQuery(), currentCmd)
					output.WriteString(overlay.Render())
					return []byte(output.String())
				})
				return
			}

			compose(func() []byte {
				var output strings.Builder
				if overlay.IsVisible() {
					output.WriteString(overlay.ClearForRedraw())
				}
				overlay.SetUserNavigated(navCopy)
				if !disableGhostText.Load() {
					output.WriteString(overlay.RenderGhostText(bufCopy, navCopy, offsetCopy == 0))
				}
				output.WriteString(overlay.Render())
				return []byte(output.String())
			})
		})
	}
	renderMenuNowMu.Unlock()

	renderOverlay = func() {
		renderMu.Lock()
		defer renderMu.Unlock()

		if !suggestionsEnabled || isExecuting() {
			if renderTimer != nil {
				renderTimer.Stop()
				renderTimer = nil
			}
			return
		}

		if userNavigated.Load() {
			return
		}

		if renderTimer != nil {
			renderTimer.Stop()
		}
		renderTimer = time.AfterFunc(25*time.Millisecond, func() {
			renderMu.Lock()
			renderTimer = nil
			renderMu.Unlock()
			renderMenuNow()
		})
	}

	renderHistorySearch = func() {
		uiPresenter.Update(func(compose func(func() []byte)) {
			state := historySearch.State()
			if !state.Active {
				return
			}
			cwd := spec.GetCWD()
			ws := workspace.DetectCached(cwd)
			results := integration.SearchCurrentRichHistory(state.Query, integration.RichHistorySearchOptions{
				Cwd:            cwd,
				ProjectRoot:    ws.Root,
				Scope:          state.Scope,
				SuccessfulOnly: state.SuccessfulOnly,
				Limit:          100,
				Host:           host,
				SessionID:      executionTracker.sessionID,
			})
			if len(results) > 0 {
				historyNav.Select(results[0].Command)
			} else {
				historyNav.Select("")
			}
			compose(func() []byte {
				var output strings.Builder
				if overlay.IsVisible() {
					output.WriteString(overlay.Clear())
				}
				overlay.SetRichHistorySearch(state.Query, state.Label(), results)
				output.WriteString(overlay.Render())
				return []byte(output.String())
			})
		})
	}

	renderOverlay()

	// reads from stdin and decides what to forward or intercept
	inBracketedPaste := false
	var terminalReports terminalInputFilter
	for {
		inputSlice := make([]byte, 128)
		n, err := os.Stdin.Read(inputSlice)
		if err != nil {
			break
		}

		if n > 0 {
			if isExecuting() {
				inBracketedPaste = false
				_, _ = ptmx.Write(inputSlice[:n])
				continue
			}
			inputSlice = terminalReports.Filter(inputSlice[:n])
			n = len(inputSlice)
			if n == 0 {
				continue
			}

			logger.Debugf("Stdin raw input: bytes=%q, hex=%x", inputSlice[:n], inputSlice[:n])

			shouldOverlayDraw := false
			for i := 0; i < n; i++ {
				b := inputSlice[i]
				intercepted := false
				acceptBindingLength := inputBindings.match(inputAccept, inputSlice, i)
				if acceptBindingLength == 0 {
					tabNav.reset()
				}

				if bindingLength := inputBindings.match(inputHistorySearch, inputSlice, i); bindingLength > 0 {
					intercepted = true
					if historySearch.Active() {
						historySearch.CycleScope()
					} else {
						bufferMu.Lock()
						original := naiveBuffer
						bufferMu.Unlock()
						historySearch.Open(original)
						historyNav.Begin(original)
					}
					userNavigated.Store(false)
					renderHistorySearch()
					i += bindingLength - 1
					continue
				}
				if bindingLength := inputBindings.match(inputHistorySuccessOnly, inputSlice, i); historySearch.Active() && bindingLength > 0 {
					intercepted = true
					historySearch.ToggleSuccessfulOnly()
					renderHistorySearch()
					i += bindingLength - 1
					continue
				}
				if bindingLength := inputBindings.match(inputToggleOverlay, inputSlice, i); bindingLength > 0 {
					intercepted = true
					suggestionsEnabled = !suggestionsEnabled
					logger.Debugf("Intercepted overlay toggle, suggestionsEnabled=%v", suggestionsEnabled)
					if !suggestionsEnabled {
						presentOverlay(overlay.ClearAndDisable)
					} else {
						shouldOverlayDraw = true
					}
					i += bindingLength - 1
					continue
				}
				if bindingLength := inputBindings.match(inputAcceptToken, inputSlice, i); bindingLength > 0 && !historySearch.Active() {
					bufferMu.Lock()
					atEnd := cursorOffset == 0
					ghostText := overlay.GetNextGhostText(naiveBuffer, atEnd)
					if ghostText != "" {
						naiveBuffer += ghostText
						cursorOffset = 0
					}
					bufferMu.Unlock()
					if consumeNextTokenAcceptance(bindingLength, ghostText) {
						intercepted = true
						feedback.accept(overlay.GetCurrentCmd())
						_, _ = ptmx.Write([]byte(ghostText))
						shouldOverlayDraw = true
						i += bindingLength - 1
						continue
					}
				}

				if b == '\033' {
					if historyNav.Active() && i+1 >= n {
						intercepted = true
						original := historyNav.Cancel()
						historySearch.Close()
						presentOverlay(overlay.ClearAndDisable)
						bufferMu.Lock()
						naiveBuffer = original
						cursorOffset = 0
						bufferMu.Unlock()
						userNavigated.Store(false)
						continue
					}
					// check for bracketed paste start/end
					if i+5 < n && inputSlice[i+1] == '[' && inputSlice[i+2] == '2' && inputSlice[i+3] == '0' {
						if (inputSlice[i+4] == '0' || inputSlice[i+4] == '1') && inputSlice[i+5] == '~' {
							intercepted = true
							inBracketedPaste = inputSlice[i+4] == '0'
							logger.Debugf("Intercepted bracketed paste event inPaste=%v", inBracketedPaste)
							_, _ = ptmx.Write(inputSlice[i : i+6])
							i += 5
							continue
						}
					}
					// handle escape sequences like arrow keys and functional shortcuts
					if i+2 < n && (inputSlice[i+1] == '[' || inputSlice[i+1] == 'O') {
						if overlay.IsVisible() && (inputSlice[i+2] == 'A' || inputSlice[i+2] == 'B') {
							intercepted = true
							userNavigated.Store(true)

							arrowDir := "down"
							if inputSlice[i+2] == 'A' {
								arrowDir = "up"
							}
							bufferMu.Lock()
							bufCopy := naiveBuffer
							offsetCopy := cursorOffset
							bufferMu.Unlock()

							var moved bool
							var selectedCmd string
							uiPresenter.Present(func() []byte {
								moved, selectedCmd = overlay.MoveCursor(arrowDir)
								if !moved {
									return nil
								}
								var output strings.Builder
								if !disableGhostText.Load() {
									output.WriteString(overlay.RenderGhostText(bufCopy, true, offsetCopy == 0))
								}
								output.WriteString(overlay.Render())
								return []byte(output.String())
							})
							if !moved {
								i += 2
								continue
							}
							if historyNav.Active() {
								historyNav.Select(selectedCmd)
							}

							i += 2
							continue
						} else if historySearch.Active() && (inputSlice[i+2] == 'A' || inputSlice[i+2] == 'B') {
							intercepted = true
							i += 2
							continue
						} else if !overlay.IsVisible() && naiveBuffer == "" && (inputSlice[i+2] == 'A' || inputSlice[i+2] == 'B') { // up/down arrow on empty prompt
							intercepted = true
							activeModeMu.Lock()
							activeMode = "history"
							saveMode(activeMode)
							activeModeMu.Unlock()

							activeModeMu.RLock()
							currentMode := activeMode
							activeModeMu.RUnlock()
							results := MergeResults("", currentMode)
							if len(results) > 0 {
								limit := min(len(results), 100)
								var historyList []spec.Suggestion

								if inputSlice[i+2] == 'A' {
									for j := limit - 1; j >= 0; j-- {
										historyList = append(historyList, results[j])
									}
								} else {
									for j := range limit {
										historyList = append(historyList, results[j])
									}
								}

								var selected string
								uiPresenter.Present(func() []byte {
									selected = overlay.SetHistoryList(historyList, inputSlice[i+2] == 'A')
									if selected == "" {
										return nil
									}
									return []byte(overlay.Render())
								})
								if selected != "" {
									bufferMu.Lock()
									historyNav.Begin(naiveBuffer)
									bufferMu.Unlock()
									historyNav.Select(selected)
									userNavigated.Store(true)
								}
							}
							i += 2
							continue
						} else if !disableGhostText.Load() && inputSlice[i+2] == 'C' { // right arrow
							bufferMu.Lock()
							atEnd := (cursorOffset == 0)
							ghostText := overlay.GetGhostText(naiveBuffer, atEnd)
							bufferMu.Unlock()

							if len(ghostText) > 0 {
								intercepted = true
								logger.Debugf("Intercepted Right Arrow (accepted ghost text: %q)", ghostText)
								bufferMu.Lock()
								naiveBuffer += ghostText
								feedback.accept(naiveBuffer)
								cursorOffset = 0
								bufferMu.Unlock()
								_, _ = ptmx.Write([]byte(ghostText))
								shouldOverlayDraw = true
								i += 2
								continue
							}
						}
					}

					// left/right arrow cursor tracking
					isLeftRightArrow := false
					if i+2 < n && (inputSlice[i+1] == '[' || inputSlice[i+1] == 'O') {
						if inputSlice[i+2] == 'D' {
							bufferMu.Lock()
							isEmptyQuery := naiveBuffer == "" && (!overlay.IsVisible() || overlay.GetTypedQuery() == "")
							bufferMu.Unlock()
							if isEmptyQuery {
								intercepted = true
								i += 2
								continue
							}
							bufferMu.Lock()
							if naiveBuffer != "" || overlay.IsVisible() {
								cursorOffset++
								if cursorOffset > len(naiveBuffer) {
									cursorOffset = len(naiveBuffer)
								}
								shouldOverlayDraw = true
								userNavigated.Store(false)
							}
							bufferMu.Unlock()
							isLeftRightArrow = true
						} else if inputSlice[i+2] == 'C' {
							bufferMu.Lock()
							isEmptyQuery := naiveBuffer == "" && (!overlay.IsVisible() || overlay.GetTypedQuery() == "")
							bufferMu.Unlock()
							if isEmptyQuery {
								intercepted = true
								i += 2
								continue
							}
							bufferMu.Lock()
							if naiveBuffer != "" || overlay.IsVisible() {
								cursorOffset--
								if cursorOffset < 0 {
									cursorOffset = 0
								}
								shouldOverlayDraw = true
								userNavigated.Store(false)
							}
							bufferMu.Unlock()
							isLeftRightArrow = true
						}
					}

					if !intercepted {
						historyNav.Cancel()
						presentOverlay(overlay.ClearAndDisable)
						disableGhostText.Store(true)
						if !isLeftRightArrow {
							bufferMu.Lock()
							naiveBuffer = ""
							cursorOffset = 0
							bufferMu.Unlock()
						}

						_, _ = ptmx.Write([]byte{b})
						// skip remaining bytes of the escape sequence to avoid misinterpretation
						for j := i + 1; j < n; j++ {
							char := inputSlice[j]
							_, _ = ptmx.Write([]byte{char})
							i = j
							if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '~' {
								break
							}
						}
					}
					continue
				}

				if historySearch.Active() && (b == 0x7f || b == 0x08) {
					intercepted = true
					historySearch.Backspace()
					renderHistorySearch()
					continue
				} else if historySearch.Active() && b >= 0x20 {
					intercepted = true
					if b < utf8.RuneSelf {
						historySearch.Append(string(b))
					} else {
						r, size := utf8.DecodeRune(inputSlice[i:n])
						if r != utf8.RuneError || size > 1 {
							historySearch.Append(string(r))
							i += size - 1
						}
					}
					renderHistorySearch()
					continue
				} else if b == 0x0d || b == 0x0a {
					intercepted = true
					logger.Debugf("Intercepted Enter key, navigated=%v", overlay.GetUserNavigated())
					searchWasActive := historySearch.Active()
					historySearch.Close()
					var cmdToSubmit string
					if overlay.IsVisible() && (overlay.GetUserNavigated() || searchWasActive) {
						selected := overlay.GetCurrentCmd()
						if selected != "" {
							cmdToSubmit = selected
							if replacement, ok := historyNav.Accept(); ok {
								cmdToSubmit = replacement
								selected = replacement
							}
							feedback.accept(selected)
							activeModeMu.RLock()
							currentMode := activeMode
							activeModeMu.RUnlock()
							if currentMode == "spec" && !searchWasActive {
								s := strings.TrimSpace(selected)
								if strings.HasSuffix(s, "/") || strings.HasSuffix(s, "\\") {
									selected = s
								} else {
									selected = s + " "
								}
							}
							_, _ = ptmx.Write(append([]byte{0x15}, selected...))
						}
					}
					if searchWasActive && cmdToSubmit == "" {
						historyNav.Cancel()
					}
					presentOverlay(overlay.ClearAndDisable)
					SetCurrentAISuggestion(nil)
					renderMu.Lock()
					if renderTimer != nil {
						renderTimer.Stop()
						renderTimer = nil
					}
					renderMu.Unlock()

					isCommandActive.Store(true)
					_, _ = ptmx.Write([]byte{b})
					bufferMu.Lock()
					if cmdToSubmit == "" {
						cmdToSubmit = naiveBuffer
					}
					lastSubmittedCommand = strings.TrimSpace(cmdToSubmit)
					recordSuggestionFeedback(feedback.finish(lastSubmittedCommand), spec.GetCWD())
					naiveBuffer = ""
					cursorOffset = 0
					bufferMu.Unlock()
					disableGhostText.Store(false)
					shouldOverlayDraw = false
					userNavigated.Store(false)
					continue
				} else if b == 0x03 || b == 0x15 { // ctrl+c, ctrl+u
					feedback.reset()
					historyNav.Cancel()
					historySearch.Close()
					intercepted = true
					presentOverlay(overlay.ClearAndDisable)
					SetCurrentAISuggestion(nil)
					renderMu.Lock()
					if renderTimer != nil {
						renderTimer.Stop()
						renderTimer = nil
					}
					renderMu.Unlock()
					isCommandActive.Store(false)
					_, _ = ptmx.Write([]byte{b})
					bufferMu.Lock()
					naiveBuffer = ""
					cursorOffset = 0
					bufferMu.Unlock()
					disableGhostText.Store(false)
					shouldOverlayDraw = false
					userNavigated.Store(false)
					continue
				} else if acceptBindingLength > 0 {
					intercepted = true
					searchWasActive := historySearch.Active()
					if searchWasActive && overlay.IsVisible() && overlay.GetCurrentCmd() == "" {
						tabNav.reset()
						i += acceptBindingLength - 1
						continue
					}
					switch tabNav.press(time.Now(), overlay.SuggestionCount()) {
					case tabCycleNext:
						var selected string
						uiPresenter.Present(func() []byte {
							selected = overlay.CycleCursor()
							if selected == "" {
								return nil
							}
							return []byte(overlay.Render())
						})
						if historyNav.Active() {
							historyNav.Select(selected)
						}
						userNavigated.Store(true)
						i += acceptBindingLength - 1
						continue
					case tabAcceptFirst:
						var selected string
						uiPresenter.Update(func(_ func(func() []byte)) {
							selected = overlay.FocusFirst()
						})
						historyNav.Select(selected)
					}
					historySearch.Close()
					logger.Debugf("Intercepted accept key, visible=%v", overlay.IsVisible())
					if !overlay.IsVisible() {
						historyNav.Cancel()
						shouldOverlayDraw = true
					} else {
						selected := overlay.GetCurrentCmd()
						if replacement, ok := historyNav.Accept(); ok {
							selected = replacement
						}
						feedback.accept(selected)
						presentOverlay(overlay.ClearAndDisable)

						activeModeMu.RLock()
						currentMode := activeMode
						activeModeMu.RUnlock()
						if currentMode == "spec" && !searchWasActive {
							s := strings.TrimSpace(selected)
							if strings.HasSuffix(s, "/") || strings.HasSuffix(s, "\\") {
								selected = s
							} else {
								selected = s + " "
							}
						}

						bufferMu.Lock()
						naiveBuffer = selected
						cursorOffset = 0
						bufferMu.Unlock()

						_, _ = ptmx.Write(append([]byte{0x15}, selected...))

						overlay.ResetCursor() // this prevents when you tab, it switches between suggestions non-stop

						shouldOverlayDraw = true // <- rerender after tab to choose, if you set to false,
						// when you press tab continually, it will print all folder from menu suggestions
						// and make the cursor jump to next line
						userNavigated.Store(false)
					}
					i += acceptBindingLength - 1
					continue
				}

				if !intercepted {
					if historySearch.Active() {
						historySearch.Close()
						historyNav.Cancel()
						presentOverlay(overlay.ClearAndDisable)
					}
					_, _ = ptmx.Write([]byte{b})
					// we handle line editing keys manually to keep naiveBuffer in sync
					// since terminal is in raw mode, we must update our state for every change
					switch b {
					case 0x01: // ctrl+a: move to beginning of line
						bufferMu.Lock()
						cursorOffset = len(naiveBuffer)
						if naiveBuffer != "" || overlay.IsVisible() {
							shouldOverlayDraw = true
						}
						bufferMu.Unlock()
						userNavigated.Store(false)
					case 0x05: // ctrl+e: move to end of line
						bufferMu.Lock()
						cursorOffset = 0
						if naiveBuffer != "" || overlay.IsVisible() {
							shouldOverlayDraw = true
						}
						bufferMu.Unlock()
						userNavigated.Store(false)

					case 127, 0x08: // backspace: remove character
						bufferMu.Lock()
						wasEmpty := len(naiveBuffer) == 0
						if !wasEmpty {
							runes := []rune(naiveBuffer)
							if cursorOffset <= 0 {
								if len(runes) > 0 {
									naiveBuffer = string(runes[:len(runes)-1])
								}
								cursorOffset = 0
							} else {
								if cursorOffset > len(runes) {
									cursorOffset = len(runes)
								}
								pos := len(runes) - cursorOffset
								if pos > 0 && pos <= len(runes) {
									naiveBuffer = string(append(runes[:pos-1], runes[pos:]...))
								}
							}
						}
						isEmptyNow := len(naiveBuffer) == 0
						bufferMu.Unlock()

						if wasEmpty || isEmptyNow {
							presentOverlay(overlay.ClearAndDisable)
							userNavigated.Store(false)
							continue
						}
						shouldOverlayDraw = true
						userNavigated.Store(false)
					case 0x17: // ctrl+w: delete the last word in the buffer
						bufferMu.Lock()
						wasEmpty := len(naiveBuffer) == 0
						trimBuf := strings.TrimRight(naiveBuffer, " ")
						lastSpace := strings.LastIndex(trimBuf, " ")
						if lastSpace >= 0 {
							naiveBuffer = trimBuf[:lastSpace+1]
						} else {
							naiveBuffer = ""
						}
						cursorOffset = 0
						isEmptyNow := len(naiveBuffer) == 0
						bufferMu.Unlock()

						if wasEmpty || isEmptyNow {
							presentOverlay(overlay.ClearAndDisable)
							userNavigated.Store(false)
							continue
						}
						shouldOverlayDraw = true
						userNavigated.Store(false)
					case 0x0c: // ctrl+l: clear screen but keep buffer and redraw menu
						shouldOverlayDraw = true
						userNavigated.Store(false)
					case '\r', '\n', 0x03, 0x15: // enter, ctrl+c, ctrl+u: clear buffer on line reset
						inBracketedPaste = false
						bufferMu.Lock()
						naiveBuffer = ""
						cursorOffset = 0
						bufferMu.Unlock()
						activeModeMu.Lock()
						activeMode = loadMode()
						activeModeMu.Unlock()
						disableGhostText.Store(false)
						presentOverlay(overlay.ClearAndDisable)
						SetCurrentAISuggestion(nil)
						userNavigated.Store(false)
					default:
						// track normal printable characters in the buffer for matching
						if b >= 32 && b <= 126 {
							// expand alias on space, but only when typing manually (not pasting)
							bufferMu.Lock()
							isSpaceAlias := !inBracketedPaste && b == ' ' && naiveBuffer != "" && !strings.Contains(naiveBuffer, " ")
							var target string
							var ok bool
							if isSpaceAlias {
								target, ok = spec.GetAlias(naiveBuffer)
							}
							bufferMu.Unlock()

							if isSpaceAlias && ok {
								// clear the current alias and replace it with the full command
								_, _ = ptmx.Write(append([]byte{0x15}, target+" "...))
								bufferMu.Lock()
								naiveBuffer = target + " "
								cursorOffset = 0
								bufferMu.Unlock()
								shouldOverlayDraw = true
								continue
							}
							bufferMu.Lock()
							if cursorOffset == 0 {
								naiveBuffer += string(b)
							} else {
								if cursorOffset > len(naiveBuffer) {
									cursorOffset = len(naiveBuffer)
								}
								pos := len(naiveBuffer) - cursorOffset
								if pos >= 0 && pos <= len(naiveBuffer) {
									naiveBuffer = naiveBuffer[:pos] + string(b) + naiveBuffer[pos:]
								} else {
									naiveBuffer += string(b)
									cursorOffset = 0
								}
							}
							bufferMu.Unlock()
							shouldOverlayDraw = true
							userNavigated.Store(false)
						}
					}
				}
			}
			if shouldOverlayDraw {
				renderOverlay()
			}
		}
	}
}

func newTerminalMarkerID() string {
	return newTerminalMarkerIDFrom(rand.Reader)
}

func newTerminalMarkerIDFrom(reader io.Reader) string {
	var token [16]byte
	if _, err := io.ReadFull(reader, token[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(token[:])
}

func terminalChatboxColors(colors config.ChatboxColorsConfig) map[string]string {
	resolved := map[string]string{
		"directory": colors.Directory, "directory-root": colors.DirectoryRoot,
		"directory-read-only": colors.DirectoryReadOnly, "git-branch": colors.GitBranch,
		"git-status": colors.GitStatus, "git-clean": colors.GitClean,
		"git-operation": colors.GitOperation, "git-conflicts": colors.GitConflicts,
		"git-staged": colors.GitStaged, "git-modified": colors.GitModified,
		"git-renamed":   colors.GitRenamed,
		"git-untracked": colors.GitUntracked, "git-ahead": colors.GitAhead,
		"git-behind": colors.GitBehind, "git-added": colors.GitAdded,
		"git-deleted": colors.GitDeleted, "git-stash": colors.GitStash,
		"git-lines-added": colors.GitLinesAdded, "git-lines-deleted": colors.GitLinesDeleted,
		"session": colors.Session, "session-warning": colors.SessionWarning,
		"session-critical": colors.SessionCritical, "contexts": colors.Contexts,
		"environment": colors.Environment, "version-mismatch": colors.VersionMismatch,
		"package": colors.Package, "stale": colors.Stale, "jobs": colors.Jobs,
		"jobs-stopped": colors.JobsStopped, "exit-neutral": colors.ExitNeutral,
		"exit-success": colors.ExitSuccess,
		"exit-failure": colors.ExitFailure, "duration": colors.Duration,
		"duration-fast": colors.DurationFast, "duration-average": colors.DurationAverage,
		"duration-slow": colors.DurationSlow, "load-low": colors.LoadLow,
		"load-average": colors.LoadAverage, "load-high": colors.LoadHigh,
		"load-critical": colors.LoadCritical,
		"laravel":       colors.Laravel, "php": colors.PHP, "composer": colors.Composer,
		"python": colors.Python, "ruby": colors.Ruby, "elixir": colors.Elixir,
		"go": colors.Go, "node": colors.Node, "bun": colors.Bun, "rust": colors.Rust,
		"docker": colors.Docker, "docker-compose": colors.DockerCompose,
		"cpu": colors.CPU, "memory": colors.Memory,
	}
	if colors.UseLegacyCPU {
		for _, level := range []string{"low", "average", "high", "critical"} {
			resolved["cpu-"+level] = colors.CPU
		}
	}
	if colors.UseLegacyMemory {
		for _, level := range []string{"low", "average", "high", "critical"} {
			resolved["memory-"+level] = colors.Memory
		}
	}
	return resolved
}
