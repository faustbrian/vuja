package root

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/spf13/cobra"
)

type debugSuggestion struct {
	Rank      int                    `json:"rank"`
	Command   string                 `json:"command"`
	Source    string                 `json:"source"`
	Score     float64                `json:"score"`
	Breakdown scoring.ScoreBreakdown `json:"breakdown"`
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
			Rank:      index + 1,
			Command:   result.Cmd,
			Source:    result.Source,
			Score:     result.Score,
			Breakdown: result.Breakdown,
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
	_, _ = fmt.Fprintln(writer, "RANK\tSOURCE\tSCORE\tBASE\tCONTEXT\tFRECENCY\tTRANSITION\tMATCH\tDIRECTORY\tCOMMAND")
	for _, suggestion := range result.Suggestions {
		breakdown := suggestion.Breakdown
		_, _ = fmt.Fprintf(
			writer,
			"%d\t%s\t%.2f\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			suggestion.Rank,
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

var DebugCmd = &cobra.Command{
	Use:   "debug",
	Short: "inspect Vuja diagnostics",
}

func init() {
	DebugCmd.AddCommand(newDebugSuggestCommand())
	rootCmd.AddCommand(DebugCmd)
}
