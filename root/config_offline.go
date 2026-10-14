package root

import (
	"io"

	"github.com/spf13/cobra"
)

// This finite entry precedes migration, runtime config/env resolution,
// logging and reload handling. It parses a fresh command, never executes the
// live root, inherited pre-run hooks, shell or watchdog. Other config commands
// retain their established startup behavior.
func executeConfigToolsInvocation(args []string, input io.Reader, output io.Writer) (bool, error) {
	selected, remaining, err := rootCmd.Find(args)
	if err != nil {
		return false, nil
	}
	var command *cobra.Command
	switch selected {
	case ConfigPreviewCmd:
		command = newConfigPreviewCommand()
	case ConfigSetupCmd:
		command = newConfigSetupCommand()
	default:
		return false, nil
	}
	// Preserve the supported global flag grammar without applying runtime
	// overrides or enabling a logger for these offline commands.
	parent := &cobra.Command{Use: "vuja"}
	parent.PersistentFlags().StringP("shell", "s", "", "shell override (not applied to offline configuration tools)")
	parent.PersistentFlags().BoolP("debug", "d", false, "debug override (no runtime log is created by offline configuration tools)")
	group := &cobra.Command{Use: "config"}
	parent.AddCommand(group)
	group.AddCommand(command)
	command.SetIn(input)
	command.SetOut(output)
	command.SetErr(output)
	command.InitDefaultHelpFlag()
	if err := command.ParseFlags(remaining); err != nil {
		return true, err
	}
	if help, _ := command.Flags().GetBool("help"); help {
		return true, command.Help()
	}
	positional := command.Flags().Args()
	if err := command.Args(command, positional); err != nil {
		return true, err
	}
	return true, command.RunE(command, positional)
}
