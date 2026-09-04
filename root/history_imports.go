package root

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/logger"
	"github.com/faustbrian/vuja/internal/policy"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
)

func configuredHistoryIntegrationsEnabled() bool {
	return historyIntegrationsEnabled(config.Get().History)
}

func historyIntegrationsEnabled(cfg config.HistoryConfig) bool {
	return cfg.ImportAtuin || cfg.Integrations.Atuin.Enabled || cfg.Integrations.Shell.Import
}

func importConfiguredHistory(store *scoring.FrecencyStore, shellName string) error {
	return importConfiguredHistoryContext(context.Background(), store, shellName)
}

func importConfiguredHistoryContext(parent context.Context, store *scoring.FrecencyStore, shellName string) error {
	if store == nil || !configuredHistoryIntegrationsEnabled() {
		return nil
	}
	cfg := config.Get().History
	return importSelectedHistoryContext(
		parent,
		store,
		shellName,
		cfg.ImportAtuin || cfg.Integrations.Atuin.Enabled,
		cfg.Integrations.Shell.Import,
	)
}

func importSelectedHistory(store *scoring.FrecencyStore, shellName string, importAtuin, importShell bool) error {
	return importSelectedHistoryContext(context.Background(), store, shellName, importAtuin, importShell)
}

func importSelectedHistoryContext(
	parent context.Context,
	store *scoring.FrecencyStore,
	shellName string,
	importAtuin, importShell bool,
) error {
	if store == nil || (!importAtuin && !importShell) {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	defer func() {
		scoring.InvalidateSignalCache()
		spec.NotifyCompletionUpdate()
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	var imported []integration.HistoryEntry
	replacedSources := make(map[string]bool)
	var importErrors []error
	if importAtuin {
		entries, loadErr := integration.LoadAtuinHistoryEntries(ctx, integration.DefaultAtuinHistoryPath(home))
		if loadErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			importErrors = append(importErrors, loadErr)
			recordOptionalHistoryImportFailure(store, "atuin")
		} else {
			imported = append(imported, entries...)
			replacedSources["atuin"] = true
		}
	}
	if importShell {
		shellHistoryPath, pathErr := resolveShellHistoryPath(home, shellName, config.Get().History.Integrations.Shell.Path)
		if pathErr != nil {
			importErrors = append(importErrors, pathErr)
			recordOptionalHistoryImportFailure(store, shellName)
		} else {
			entries, loadErr := integration.LoadShellHistoryEntries(ctx, shellHistoryPath, shellName)
			if loadErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				importErrors = append(importErrors, loadErr)
				recordOptionalHistoryImportFailure(store, shellName)
			} else {
				imported = append(imported, entries...)
				replacedSources[shellName] = true
			}
		}
	}
	if len(replacedSources) == 0 {
		return errors.Join(importErrors...)
	}
	returnAdapterFailure := func(adapterErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for source := range replacedSources {
			recordOptionalHistoryImportFailure(store, source)
		}
		return adapterErr
	}

	stored, err := store.QueryAllHistoryEvents(ctx)
	if err != nil {
		return returnAdapterFailure(err)
	}
	imported = preferExternalExecutions(imported)
	imported = preferVujaExecutions(imported, stored)
	events := make([]scoring.HistoryEvent, 0, len(stored)+len(imported))
	for _, event := range stored {
		if event.Imported && replacedSources[event.Source] {
			continue
		}
		if event.Imported {
			events = append(events, event)
		}
	}
	for _, entry := range imported {
		if policy.IsSensitive(entry.Command) {
			continue
		}
		event := scoringHistoryEvent(entry)
		event.Imported = true
		events = append(events, event)
	}
	if err := store.ReplaceImportedHistoryEvents(ctx, events); err != nil {
		return returnAdapterFailure(err)
	}
	for source := range replacedSources {
		if err := store.RecordHistoryImportFreshness(ctx, source, time.Now()); err != nil {
			return returnAdapterFailure(err)
		}
	}
	historyConfig := config.Get().History
	if historyConfig.Retention == "bounded" && historyConfig.MaxEvents > 0 {
		if _, err := store.PruneHistoryEvents(ctx, historyConfig.MaxEvents); err != nil {
			return returnAdapterFailure(err)
		}
	}
	entries, err := publishCanonicalStoreHistory(ctx, store)
	if err != nil {
		return returnAdapterFailure(err)
	}
	if err := store.ReplaceDirectorySource(ctx, "history", historyNavigationDirectoryImports(entries, time.Now())); err != nil {
		return returnAdapterFailure(err)
	}
	if len(importErrors) > 0 {
		return errors.Join(importErrors...)
	}
	return nil
}

func resolveShellHistoryPath(home, shellName, configuredPath string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return integration.DefaultShellHistoryPath(home, shellName), nil
	}
	if configuredPath == "~" {
		return filepath.Clean(home), nil
	}
	if after, ok := strings.CutPrefix(configuredPath, "~/"); ok {
		return filepath.Join(home, filepath.FromSlash(after)), nil
	}
	if !filepath.IsAbs(configuredPath) {
		return "", fmt.Errorf("shell history path must be absolute or start with ~/")
	}
	return filepath.Clean(configuredPath), nil
}

func recordOptionalHistoryImportFailure(store *scoring.FrecencyStore, source string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.RecordHistoryImportFailure(ctx, source, time.Now()); err != nil {
		logger.Errorf("failed to persist optional history import failure metadata: %v", err)
	}
}

// preferExternalExecutions preserves repeats within a source while removing
// one lower-fidelity counterpart for each matching execution from a richer
// source. Atuin metadata wins over native shell-file metadata.
func preferExternalExecutions(imported []integration.HistoryEntry) []integration.HistoryEntry {
	ordered := append([]integration.HistoryEntry(nil), imported...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return externalHistoryPriority(ordered[i].Source) > externalHistoryPriority(ordered[j].Source)
	})
	kept := make([]integration.HistoryEntry, 0, len(ordered))
	usedBySource := make(map[string]map[int]bool)
	for _, candidate := range ordered {
		used := usedBySource[candidate.Source]
		if used == nil {
			used = make(map[int]bool)
			usedBySource[candidate.Source] = used
		}
		matched := false
		for index, richer := range kept {
			if used[index] || externalHistoryPriority(richer.Source) <= externalHistoryPriority(candidate.Source) ||
				!sameImportedHistoryExecution(candidate, richer) {
				continue
			}
			used[index] = true
			matched = true
			break
		}
		if !matched {
			kept = append(kept, candidate)
		}
	}
	return kept
}

func externalHistoryPriority(source string) int {
	if source == "atuin" {
		return 2
	}
	return 1
}

func sameImportedHistoryExecution(left, right integration.HistoryEntry) bool {
	return sameHistoryExecution(left, scoringHistoryEvent(right))
}

// preferVujaExecutions removes only one imported counterpart for each richer
// Vuja-owned execution. One-to-one matching preserves legitimate repeats.
func preferVujaExecutions(imported []integration.HistoryEntry, stored []scoring.HistoryEvent) []integration.HistoryEntry {
	native := make([]scoring.HistoryEvent, 0, len(stored))
	for _, event := range stored {
		if !event.Imported {
			native = append(native, event)
		}
	}
	used := make([]bool, len(native))
	kept := make([]integration.HistoryEntry, 0, len(imported))
	for _, candidate := range imported {
		matched := false
		for index, event := range native {
			if used[index] || !sameHistoryExecution(candidate, event) {
				continue
			}
			used[index] = true
			matched = true
			break
		}
		if !matched {
			kept = append(kept, candidate)
		}
	}
	return kept
}

func sameHistoryExecution(imported integration.HistoryEntry, native scoring.HistoryEvent) bool {
	command := strings.TrimSpace(imported.NormalizedCommand)
	if command == "" {
		command = strings.TrimSpace(imported.Command)
	}
	nativeCommand := strings.TrimSpace(native.NormalizedCommand)
	if nativeCommand == "" {
		nativeCommand = strings.TrimSpace(native.Command)
	}
	importedCwd := strings.TrimSpace(imported.Cwd)
	nativeCwd := strings.TrimSpace(native.Cwd)
	if command != nativeCommand || importedCwd != "" && nativeCwd != "" && importedCwd != nativeCwd ||
		imported.StartedAt.IsZero() || native.StartedAt.IsZero() || absDuration(imported.StartedAt.Sub(native.StartedAt)) > time.Second {
		return false
	}
	if imported.HasExitCode && native.HasExitCode && imported.ExitCode != native.ExitCode {
		return false
	}
	return imported.Duration <= 0 || native.Duration <= 0 || absDuration(imported.Duration-native.Duration) <= time.Second
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func startConfiguredHistoryImport(store *scoring.FrecencyStore, shellName string) func() {
	if store == nil || !configuredHistoryIntegrationsEnabled() {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := importConfiguredHistoryContext(ctx, store, shellName); err != nil && ctx.Err() == nil {
			logger.Errorf("optional history import failed: %v", err)
		}
		atuin := config.Get().History.Integrations.Atuin
		if !atuin.Enabled || atuin.Mode != "sync" {
			return
		}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := importConfiguredHistoryContext(ctx, store, shellName); err != nil && ctx.Err() == nil {
					logger.Errorf("optional history synchronization failed: %v", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

type configuredHistoryImportManager struct {
	mu   sync.Mutex
	stop func()
}

func (m *configuredHistoryImportManager) Restart(store *scoring.FrecencyStore, shellName string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stop != nil {
		m.stop()
	}
	m.stop = startConfiguredHistoryImport(store, shellName)
}

func (m *configuredHistoryImportManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	stop := m.stop
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}
