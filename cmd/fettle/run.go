package main

import "github.com/spf13/cobra"

// runCmd is the parent of the agent-driven stage subcommands.
// Run inspection (list/show) lives at the top level under listCmd
// and showCmd, not here.
var runCmd = &cobra.Command{
	Use:     "run",
	Short:   "Run a stage (find, review, merge, dedupe, group)",
	Long:    `Stage runners. find / dedupe / group create new run folders. merge concatenates runs (no agent). review operates on an existing run via --run.`,
	GroupID: groupStages,
}

func init() {
	rootCmd.AddCommand(runCmd)
}
