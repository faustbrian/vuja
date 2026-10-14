package root

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
	"golang.org/x/sys/unix"
)

type suggestionPreferenceRequest struct {
	action  inputAction
	command string
}

type suggestionPreferenceResult struct {
	request     suggestionPreferenceRequest
	preferences config.SuggestionsConfig
	err         error
}

// One pending action, one worker and one result. Disk work never holds an
// overlay/render lock or runs on the terminal input loop.
type suggestionPreferenceWorker struct {
	requests chan suggestionPreferenceRequest
	results  chan suggestionPreferenceResult
	done     chan struct{}
	cancel   context.CancelFunc
	busy     atomic.Bool
	closed   atomic.Bool
}

func newSuggestionPreferenceWorker(path string) *suggestionPreferenceWorker {
	return newSuggestionPreferenceWorkerWithWriter(func(ctx context.Context, request suggestionPreferenceRequest) (config.SuggestionsConfig, error) {
		return saveSuggestionPreference(ctx, path, request)
	})
}

func newSuggestionPreferenceWorkerWithWriter(write func(context.Context, suggestionPreferenceRequest) (config.SuggestionsConfig, error)) *suggestionPreferenceWorker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &suggestionPreferenceWorker{
		requests: make(chan suggestionPreferenceRequest, 1), results: make(chan suggestionPreferenceResult, 1),
		done: make(chan struct{}), cancel: cancel,
	}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case request := <-w.requests:
				if ctx.Err() != nil {
					return
				}
				jobCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				preferences, err := write(jobCtx, request)
				stop()
				select {
				case w.results <- suggestionPreferenceResult{request, preferences, err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return w
}

func (w *suggestionPreferenceWorker) enqueue(request suggestionPreferenceRequest) bool {
	if w.closed.Load() || !w.busy.CompareAndSwap(false, true) {
		return false
	}
	select {
	case <-w.done:
		w.busy.Store(false)
		return false
	case w.requests <- request:
		return true
	}
}

func (w *suggestionPreferenceWorker) acknowledge() { w.busy.Store(false) }
func (w *suggestionPreferenceWorker) close() {
	w.closed.Store(true)
	w.cancel()
	<-w.done
}

// Invoked only after native-application and bracketed-paste ownership guards.
// It deliberately has no terminal writer: a preference/explanation key cannot
// insert text, accept ghost text, or submit a shell command.
func handleSuggestionControl(data []byte, index int, bindings inputKeybindings, overlay *integration.Overlay, enqueue func(suggestionPreferenceRequest) bool) int {
	if !overlay.IsVisible() {
		return 0
	}
	for _, action := range []inputAction{inputSuggestionPin, inputSuggestionBlock, inputSuggestionExplain} {
		length := bindings.match(action, data, index)
		if length == 0 {
			continue
		}
		selected, ok := overlay.SelectedSuggestion()
		if !ok {
			overlay.SetControlNotice("Select a row first; Enter still runs typed input")
			return length
		}
		if action == inputSuggestionExplain {
			overlay.ToggleRankingExplanation()
		} else if enqueue(suggestionPreferenceRequest{action, selected.Cmd}) {
			overlay.SetControlNotice("Saving suggestion preference…")
		} else {
			overlay.SetControlNotice("A suggestion preference is still being saved")
		}
		return length
	}
	return 0
}

func saveSuggestionPreference(ctx context.Context, path string, request suggestionPreferenceRequest) (config.SuggestionsConfig, error) {
	if !filepath.IsAbs(path) {
		return config.SuggestionsConfig{}, errors.New("configuration path is unavailable")
	}
	command := strings.TrimSpace(request.command)
	if command == "" || len(command) > 64*1024 || policy.IsSensitive(command) || strings.IndexFunc(command, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	}) >= 0 {
		return config.SuggestionsConfig{}, errors.New("command cannot be saved as a suggestion preference")
	}
	if request.action != inputSuggestionPin && request.action != inputSuggestionBlock {
		return config.SuggestionsConfig{}, errors.New("unsupported suggestion preference")
	}
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return config.SuggestionsConfig{}, err
	}
	lock, err := config.OpenPrivateFile(path+".suggestions.lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return config.SuggestionsConfig{}, err
	}
	defer lock.Close()
	for {
		if err = ctx.Err(); err != nil {
			return config.SuggestionsConfig{}, err
		}
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return config.SuggestionsConfig{}, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return config.SuggestionsConfig{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	original, cfg, err := readSetupConfig(path)
	if err != nil {
		return config.SuggestionsConfig{}, err
	}
	if cfg == nil {
		cfg, err = config.Preset("balanced")
		if err != nil {
			return config.SuggestionsConfig{}, err
		}
	}
	preferences := &cfg.Suggestions
	previousCount := len(preferences.Pins) + len(preferences.Blocks) + len(preferences.ExactBlocks)
	if request.action == inputSuggestionPin {
		if policy.BlockedBy(preferences.Blocks, preferences.ExactBlocks, command) {
			return config.SuggestionsConfig{}, errors.New("blocked commands cannot be pinned")
		}
		if slices.ContainsFunc(preferences.Pins, func(pin string) bool { return strings.TrimSpace(pin) == command }) {
			preferences.Pins = slices.DeleteFunc(preferences.Pins, func(pin string) bool { return strings.TrimSpace(pin) == command })
		} else {
			preferences.Pins = append(preferences.Pins, command)
		}
	} else {
		if !slices.Contains(preferences.ExactBlocks, command) {
			preferences.ExactBlocks = append(preferences.ExactBlocks, command)
		}
		preferences.Pins = slices.DeleteFunc(preferences.Pins, func(pin string) bool { return strings.TrimSpace(pin) == command })
	}
	count := len(preferences.Pins) + len(preferences.Blocks) + len(preferences.ExactBlocks)
	if count > 512 && count >= previousCount {
		return config.SuggestionsConfig{}, errors.New("suggestion preference limit reached")
	}
	if err := config.Validate(cfg); err != nil {
		return config.SuggestionsConfig{}, err
	}
	content, err := config.Render(cfg)
	if err != nil {
		return config.SuggestionsConfig{}, err
	}
	if len(content) > 1024*1024 {
		return config.SuggestionsConfig{}, errors.New("configuration exceeds 1 MiB")
	}
	if err := ctx.Err(); err != nil {
		return config.SuggestionsConfig{}, err
	}
	latest, _, err := readSetupConfig(path)
	if err != nil || !bytes.Equal(latest, original) {
		return config.SuggestionsConfig{}, errors.New("configuration changed while saving the preference")
	}
	if original != nil {
		// A single private predecessor backup, not an unbounded file per keypress.
		if err := writeConfigFile(path+".suggestions.bak", original); err != nil {
			return config.SuggestionsConfig{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return config.SuggestionsConfig{}, err
	}
	if err := writeConfigFile(path, content); err != nil {
		return config.SuggestionsConfig{}, err
	}
	return *preferences, nil
}

func filterBlockedHistorySuggestions(results []integration.RichHistoryResult) []integration.RichHistoryResult {
	visible := make([]integration.RichHistoryResult, 0, len(results))
	for _, result := range results {
		if !policy.Blocked(result.Command) {
			visible = append(visible, result)
		}
	}
	return visible
}
