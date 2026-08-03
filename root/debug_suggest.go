package root

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/spec"
	"github.com/spf13/cobra"
)

type debugSuggestion struct {
	Rank       int                    `json:"rank"`
	PolicyRank int                    `json:"policy_rank,omitempty"`
	Command    string                 `json:"command"`
	Source     string                 `json:"source"`
	Score      float64                `json:"score"`
	Breakdown  scoring.ScoreBreakdown `json:"breakdown"`
}

type debugSuggestOutput struct {
	Query       string            `json:"query"`
	CWD         string            `json:"cwd"`
	Mode        string            `json:"mode"`
	Suggestions []debugSuggestion `json:"suggestions"`
}

func newDebugSuggestCommand() *cobra.Command {
	var cwd string
	var mode string
	var jsonOutput bool

	command := &cobra.Command{
		Use:   "suggest <query>",
		Short: "explain suggestion ranking",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if mode != "spec" && mode != "history" {
				return fmt.Errorf("mode must be spec or history")
			}

			resolvedCWD, err := filepath.Abs(cwd)
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}
			info, err := os.Stat(resolvedCWD)
			if err != nil {
				return fmt.Errorf("inspect working directory: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("working directory is not a directory: %s", resolvedCWD)
			}

			originalCWD, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("read current working directory: %w", err)
			}
			if err := os.Chdir(resolvedCWD); err != nil {
				return fmt.Errorf("change working directory: %w", err)
			}
			defer func() {
				_ = os.Chdir(originalCWD)
			}()

			result := buildDebugSuggestOutput(args[0], resolvedCWD, mode)
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			writeDebugSuggestTable(cmd, result)
			return nil
		},
	}
	command.Flags().StringVar(&cwd, "cwd", ".", "working directory to inspect")
	command.Flags().StringVar(&mode, "mode", "spec", "ranking mode: spec or history")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func buildDebugSuggestOutput(query, cwd, mode string) debugSuggestOutput {
	scored := scoreResults(query, mode)
	limit := min(len(scored), config.Get().UI.MaxSuggestions)
	suggestions := make([]debugSuggestion, 0, limit)
	for index, result := range scored[:limit] {
		suggestions = append(suggestions, debugSuggestion{
			Rank:       index + 1,
			PolicyRank: directoryPolicyRank(result.Suggestion),
			Command:    result.Cmd,
			Source:     result.Source,
			Score:      result.Score,
			Breakdown:  result.Breakdown,
		})
	}
	return debugSuggestOutput{
		Query:       strings.TrimSpace(query),
		CWD:         cwd,
		Mode:        mode,
		Suggestions: suggestions,
	}
}

func writeDebugSuggestTable(cmd *cobra.Command, result debugSuggestOutput) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "RANK\tPOLICY\tSOURCE\tSCORE\tBASE\tCONTEXT\tFRECENCY\tTRANSITION\tMATCH\tDIRECTORY\tCOMMAND")
	for _, suggestion := range result.Suggestions {
		breakdown := suggestion.Breakdown
		policyRank := "-"
		if suggestion.PolicyRank > 0 {
			policyRank = strconv.Itoa(suggestion.PolicyRank)
		}
		_, _ = fmt.Fprintf(
			writer,
			"%d\t%s\t%s\t%.2f\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			suggestion.Rank,
			policyRank,
			suggestion.Source,
			suggestion.Score,
			breakdown.BasePriority,
			breakdown.ContextBonus,
			breakdown.Frecency,
			breakdown.Transition,
			breakdown.MatchQuality,
			breakdown.DirectoryAffinity,
			suggestion.Command,
		)
	}
	_ = writer.Flush()
}

func directoryPolicyRank(suggestion spec.Suggestion) int {
	if suggestion.Source != "directory-index" || suggestion.Priority <= 0 {
		return 0
	}
	return 101 - suggestion.Priority
}

var DebugCmd = &cobra.Command{
	Use:   "debug",
	Short: "inspect Vuja diagnostics",
}

func init() {
	DebugCmd.AddCommand(newDebugSuggestCommand())
	DebugCmd.AddCommand(newDebugLatencyCommand())
	rootCmd.AddCommand(DebugCmd)
}

func newDebugLatencyCommand() *cobra.Command {
	var jsonOutput bool
	var sessionID string
	command := &cobra.Command{
		Use:   "latency",
		Short: "show recorded suggestion latency",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := loadLatencySnapshot(sessionID)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("no suggestion latency has been recorded yet")
				}
				return err
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(snapshot)
			}
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintf(writer, "SESSION\t%s\tPID %d\tUPDATED %s\n", snapshot.SessionID, snapshot.PID, snapshot.UpdatedAt.Format(time.RFC3339Nano))
			_, _ = fmt.Fprintln(writer, "PHASE\tSAMPLES\tP50\tP95")
			phases := make([]string, 0, len(snapshot.SamplesUS))
			for phase := range snapshot.SamplesUS {
				phases = append(phases, phase)
			}
			sort.Strings(phases)
			for _, phase := range phases {
				samples := snapshot.SamplesUS[phase]
				_, _ = fmt.Fprintf(writer, "%s\t%d\t%s\t%s\n", phase, len(samples), latencyPercentile(samples, .50), latencyPercentile(samples, .95))
			}
			cacheTiers := make([]string, 0, len(snapshot.Cache))
			for tier := range snapshot.Cache {
				cacheTiers = append(cacheTiers, tier)
			}
			sort.Strings(cacheTiers)
			for _, tier := range cacheTiers {
				stats := snapshot.Cache[tier]
				_, _ = fmt.Fprintf(writer, "cache-%s\t-\t%d hits\t%d misses\n", tier, stats.Hits, stats.Misses)
			}
			for _, stats := range snapshot.RuntimeCaches {
				_, _ = fmt.Fprintf(writer, "runtime-cache-%s\t-\t%d hits\t%d misses, %d evictions\n", stats.Name, stats.Hits, stats.Misses, stats.Evictions)
			}
			_, _ = fmt.Fprintf(writer, "timeouts\t-\t%d\t-\n", snapshot.Timeouts)
			return writer.Flush()
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().StringVar(&sessionID, "session", "", "read one recorded Vuja session")
	return command
}
