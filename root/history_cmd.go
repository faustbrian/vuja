package root

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/cobra"
)

var historyCmd = &cobra.Command{Use: "history", Short: "manage Vuja-owned command history"}

func historyCommandStore() (*scoring.FrecencyStore, context.Context, context.CancelFunc, error) {
	store, err := scoring.GetFrecencyStore()
	if err == nil {
		err = replayHistoryRecoveryJournal(store)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	return store, ctx, cancel, err
}

var historyStatsCmd = &cobra.Command{
	Use: "stats", Short: "show sanitized Vuja history statistics",
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		stats, err := store.HistoryStats(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "events %d\ndistinct %d\nunfinished %d\nimported %d\npersistence-failures %d\n",
			stats.Events, stats.DistinctCommands, stats.Unfinished, stats.Imported, historyPersistenceFailureCount())
		states := make([]string, 0, len(stats.States))
		for state := range stats.States {
			states = append(states, state)
		}
		sort.Strings(states)
		for _, state := range states {
			fmt.Fprintf(cmd.OutOrStdout(), "state.%s %d\n", state, stats.States[state])
		}
		if !stats.MostRecentPersisted.IsZero() {
			fmt.Fprintf(cmd.OutOrStdout(), "last-persisted %s\n", stats.MostRecentPersisted.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "schema %s\n", nonEmpty(stats.SchemaVersion, "legacy"))
		return nil
	},
}

var historySourcesCmd = &cobra.Command{
	Use: "sources", Short: "show finite history source counts",
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		stats, err := store.HistoryStats(ctx)
		if err != nil {
			return err
		}
		keySet := make(map[string]bool, len(stats.Sources)+len(stats.ImportFreshness)+len(stats.ImportFailures))
		for source := range stats.Sources {
			keySet[source] = true
		}
		for source := range stats.ImportFreshness {
			keySet[source] = true
		}
		for source := range stats.ImportFailures {
			keySet[source] = true
		}
		keys := make([]string, 0, len(keySet))
		for source := range keySet {
			keys = append(keys, source)
		}
		sort.Strings(keys)
		for _, source := range keys {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %d", source, stats.Sources[source])
			if fresh := stats.ImportFreshness[source]; !fresh.IsZero() {
				fmt.Fprintf(cmd.OutOrStdout(), " imported-at %s", fresh.UTC().Format(time.RFC3339))
			}
			if failure := stats.ImportFailures[source]; failure.Count > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " failures %d", failure.Count)
				if !failure.LastFailed.IsZero() {
					fmt.Fprintf(cmd.OutOrStdout(), " failed-at %s", failure.LastFailed.UTC().Format(time.RFC3339))
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	},
}

var historyDoctorCmd = &cobra.Command{
	Use: "doctor", Short: "inspect Vuja history without launching a managed shell",
	RunE: func(cmd *cobra.Command, _ []string) error {
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		stats, err := store.HistoryStats(ctx)
		if err != nil {
			return err
		}
		cfg := config.Get().History
		health, err := store.HistoryHealth(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "database %s\n", health.QuickCheck)
		paths := make([]string, 0, len(health.FileModes))
		for path := range health.FileModes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(cmd.OutOrStdout(), "permissions %s %04o\n", path, health.FileModes[path])
		}
		fmt.Fprintln(cmd.OutOrStdout(), "canonical-source vuja")
		fmt.Fprintf(cmd.OutOrStdout(), "unfinished %d\n", stats.Unfinished)
		fmt.Fprintf(cmd.OutOrStdout(), "persistence-failures %d\n", historyPersistenceFailureCount())
		importFailures := 0
		for _, failure := range stats.ImportFailures {
			importFailures += failure.Count
		}
		fmt.Fprintf(cmd.OutOrStdout(), "import-failures %d\n", importFailures)
		fmt.Fprintf(cmd.OutOrStdout(), "atuin %s mode=%s\n", enabledLabel(cfg.ImportAtuin || cfg.Integrations.Atuin.Enabled), cfg.Integrations.Atuin.Mode)
		fmt.Fprintf(cmd.OutOrStdout(), "shell-import %s\n", enabledLabel(cfg.Integrations.Shell.Import))
		fmt.Fprintf(cmd.OutOrStdout(), "shell-mirror %s\n", enabledLabel(cfg.Integrations.Shell.Mirror))
		if strings.TrimSpace(cfg.Integrations.Shell.Path) == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "shell-path conventional")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "shell-path configured")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "migration lifecycle-%s\n", nonEmpty(stats.SchemaVersion, "legacy"))
		return nil
	},
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

var historyExplainCmd = &cobra.Command{
	Use: "explain <query>", Args: cobra.ExactArgs(1), Short: "explain canonical history recall for a query",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		if _, err := publishCanonicalStoreHistory(ctx, store); err != nil {
			return err
		}
		results, err := integration.SearchHistory(args[0], nil)
		if err != nil {
			return err
		}
		status := integration.CurrentHistorySearchStatus()
		counts := integration.ExplainCanonicalHistory(args[0])
		returned := min(len(results), max(config.Get().UI.MaxSuggestions, 0))
		fmt.Fprint(cmd.OutOrStdout(), formatHistoryExplanation(counts, status, len(results), returned))
		return nil
	},
}

func formatHistoryExplanation(
	counts integration.CanonicalHistoryCounts,
	status integration.HistorySearchStatus,
	ranked, returned int,
) string {
	return fmt.Sprintf(
		"before-dedup %d\nafter-dedup %d\nafter-filter %d\nafter-ranking %d\nreturned %d\ntruncated %t\n",
		counts.Events, counts.Candidates, counts.Eligible, ranked, returned, status.Truncated,
	)
}

var historyImportCmd = &cobra.Command{
	Use: "import <atuin|shell>", Args: cobra.ExactArgs(1), Short: "import an optional external history source",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		shellName := config.Get().Core.Shell
		if shellName == "" {
			shellName = detectShell()
		}
		switch args[0] {
		case "atuin":
			return importSelectedHistory(store, shellName, true, false)
		case "shell":
			return importSelectedHistory(store, shellName, false, true)
		default:
			return fmt.Errorf("unsupported history source %q", args[0])
		}
	},
}

var historyPruneMax int
var historyPruneCmd = &cobra.Command{
	Use: "prune", Short: "retain only the newest completed history events",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if historyPruneMax <= 0 {
			return fmt.Errorf("--max must be greater than zero")
		}
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		removed, err := store.PruneHistoryEvents(ctx, historyPruneMax)
		if err != nil {
			return err
		}
		entries, err := publishCanonicalStoreHistory(ctx, store)
		if err != nil {
			return err
		}
		if err := store.ReplaceDirectorySource(ctx, "history", historyNavigationDirectoryImports(entries, time.Now())); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %d\n", removed)
		return nil
	},
}

var historyClearConfirm bool
var historyClearCmd = &cobra.Command{
	Use: "clear", Short: "clear Vuja-owned and imported history",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !historyClearConfirm {
			return fmt.Errorf("history clear requires --confirm")
		}
		store, ctx, cancel, err := historyCommandStore()
		defer cancel()
		if err != nil {
			return err
		}
		if err := store.ClearHistory(ctx); err != nil {
			return err
		}
		if err := clearHistoryRecoveryJournal(); err != nil {
			return err
		}
		if _, err := publishCanonicalStoreHistory(ctx, store); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "history cleared")
		return nil
	},
}

func init() {
	historyPruneCmd.Flags().IntVar(&historyPruneMax, "max", 0, "maximum completed events to retain")
	historyClearCmd.Flags().BoolVar(&historyClearConfirm, "confirm", false, "confirm irreversible history deletion")
	historyCmd.AddCommand(historyStatsCmd, historySourcesCmd, historyDoctorCmd, historyExplainCmd, historyImportCmd, historyPruneCmd, historyClearCmd)
	rootCmd.AddCommand(historyCmd)
}
