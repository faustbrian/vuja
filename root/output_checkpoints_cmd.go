package root

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/faustbrian/vuja/v2/internal/config"
	"github.com/spf13/cobra"
)

type outputCheckpointFile struct {
	name, owner string
	size        int64
	interrupted bool
}

func newOutputCommand() *cobra.Command {
	output := &cobra.Command{Use: "output", Short: "manage retained terminal output separately from command history"}
	checkpoints := &cobra.Command{Use: "checkpoints", Short: "inspect and clear private output checkpoints"}
	list := &cobra.Command{
		Use: "list", Short: "show checkpoint counts and sizes without printing output or pane identities", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOutputCheckpointFiles(func(_ string, files []outputCheckpointFile) error {
				var bytes int64
				interrupted := 0
				for _, file := range files {
					bytes += file.size
					if file.interrupted {
						interrupted++
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "checkpoints %d\ninterrupted-output-files %d\nbytes %d\ncommand-history unchanged\n", len(files)-interrupted, interrupted, bytes)
				return nil
			})
		},
	}
	checkpoints.AddCommand(list, newOutputCheckpointClearCommand())
	output.AddCommand(checkpoints)
	return output
}

func newOutputCheckpointClearCommand() *cobra.Command {
	var all bool
	var oldSessionsRestarted bool
	var confirm string
	command := &cobra.Command{
		Use: "clear", Short: "preview or confirm clearing this pane's checkpoints (or all panes with --all)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			current := ""
			if !all {
				pane := outputRecoveryPaneID(os.Getenv)
				if pane == "" {
					return errors.New("no stable pane identity; use --all to explicitly select every checkpoint")
				}
				current = fmt.Sprintf("%x.json", sha256.Sum256([]byte(pane)))
			}
			return withOutputCheckpointFiles(func(dir string, files []outputCheckpointFile) error {
				if !all {
					if slices.ContainsFunc(files, func(file outputCheckpointFile) bool { return file.interrupted }) {
						fmt.Fprintln(cmd.OutOrStdout(), "Interrupted output cannot be attributed to a pane; use --all to include those files.")
					}
					files = slices.DeleteFunc(files, func(file outputCheckpointFile) bool { return file.name != current })
				}
				if len(files) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No selected output checkpoints; command history is unchanged.")
					return nil
				}
				selection := []string{"output-checkpoint-clear-v1"}
				for _, file := range files {
					selection = append(selection, file.name, file.owner)
				}
				encoded, _ := json.Marshal(selection)
				expected := fmt.Sprintf("%x", sha256.Sum256(encoded))
				if confirm == "" {
					scope := ""
					if all {
						scope = " --all"
					}
					fmt.Fprintln(cmd.OutOrStdout(), "Before confirming, restart all pre-update Vuja sessions; older writers can recreate deleted checkpoints.")
					fmt.Fprintf(cmd.OutOrStdout(), "Selected %d output checkpoint file(s); command history and saved exports are unchanged.\nAfter restarting: vuja output checkpoints clear%s --old-sessions-restarted --confirm %s\n", len(files), scope, expected)
					return nil
				}
				if !oldSessionsRestarted {
					return errors.New("restart all pre-update Vuja sessions, then acknowledge with --old-sessions-restarted before confirming clear")
				}
				if confirm != expected {
					return errors.New("checkpoint selection or pane ownership changed; preview again before clearing")
				}
				for _, file := range files {
					if err := os.Remove(filepath.Join(dir, file.name)); err != nil {
						return errors.New("checkpoint clear incomplete; preview remaining files before retrying")
					}
				}
				directory, err := os.Open(dir)
				if err != nil {
					return errors.New("checkpoint files removed but directory durability could not be confirmed")
				}
				defer directory.Close()
				if err := directory.Sync(); err != nil {
					return errors.New("checkpoint files removed but directory durability could not be confirmed")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cleared %d output checkpoint file(s); command history and saved exports are unchanged.\n", len(files))
				return nil
			})
		},
	}
	command.Flags().BoolVar(&all, "all", false, "select every pane, not just this pane")
	command.Flags().BoolVar(&oldSessionsRestarted, "old-sessions-restarted", false, "acknowledge every pre-update Vuja session has been restarted; legacy writers cannot honor deletion fences")
	command.Flags().StringVar(&confirm, "confirm", "", "confirmation from the current preview")
	return command
}

// The same existing directory lock fences live checkpoint workers. A preview
// does not remove interrupted writes or rewrite/delete captured rows.
func withOutputCheckpointFiles(inspect func(string, []outputCheckpointFile) error) error {
	state, err := config.StatePath()
	if err != nil {
		return errors.New("output checkpoint directory is unavailable")
	}
	dir := filepath.Join(filepath.Dir(state), "terminal-output")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return inspect(dir, nil)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe output checkpoint directory")
	}
	r := &outputRecovery{dir: dir}
	return r.withLock(func() error {
		files, err := inspectOutputCheckpointFiles(dir)
		if err != nil {
			return err
		}
		return inspect(dir, files)
	})
}

func inspectOutputCheckpointFiles(dir string) ([]outputCheckpointFile, error) {
	directory, err := os.Open(dir)
	if err != nil {
		return nil, errors.New("output checkpoint inventory is unavailable")
	}
	defer directory.Close()
	var files []outputCheckpointFile
	seen := 0
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			seen++
			if seen > 4096 {
				return nil, errors.New("output checkpoint directory exceeds the bounded inventory limit; no files changed")
			}
			name := entry.Name()
			interrupted := isInterruptedCheckpointName(name)
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !interrupted {
				if len(name) != 69 || !strings.HasSuffix(name, ".json") {
					continue
				}
				if _, err := hex.DecodeString(strings.TrimSuffix(name, ".json")); err != nil {
					continue
				}
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			// The retention target is not an admission bound for clearing.
			// Interrupted pruning can leave more panes; the bounded directory
			// scan above still limits inventory work without trapping their data.
			file := outputCheckpointFile{name: name, size: info.Size(), interrupted: interrupted}
			if interrupted {
				// Incomplete writes have no reliable pane identity. Metadata binds
				// their explicit all-pane preview without reading private content.
				file.owner = fmt.Sprintf("interrupted:%d:%d", info.Size(), info.ModTime().UnixNano())
			} else if state, err := readOutputRecoveryState(filepath.Join(dir, name)); err == nil {
				file.owner = state.Owner
			} else {
				// Corrupt regular checkpoint files remain explicitly clearable.
				// Their bounded metadata identifies the preview without printing
				// private content or silently following a symlink.
				file.owner = fmt.Sprintf("invalid:%d:%d", info.Size(), info.ModTime().UnixNano())
			}
			files = append(files, file)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, errors.New("output checkpoint inventory is incomplete")
		}
	}
	slices.SortFunc(files, func(a, b outputCheckpointFile) int { return strings.Compare(a.name, b.name) })
	return files, nil
}

func init() { rootCmd.AddCommand(newOutputCommand()) }
