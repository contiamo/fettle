package main

import "github.com/spf13/cobra"

// runCmd is the parent of the agent-driven stage subcommands.
// Run inspection (list/show) lives at the top level under listCmd
// and showCmd, not here.
var runCmd = &cobra.Command{
	Use:     "run",
	Short:   "Run a stage (find, review)",
	Long:    `Stage runners. find creates a new run folder; review operates on an existing run via --run.`,
	GroupID: groupStages,
}

func init() {
	rootCmd.AddCommand(runCmd)
}
