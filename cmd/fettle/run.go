package main

import "github.com/spf13/cobra"

// runCmd is the parent of the agent-driven stage subcommands plus
// run-level inspectors (list, status).
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a stage (find, review, merge, dedupe, group) or inspect existing runs (list, status)",
	Long: `Stage runners create new run folders (find, dedupe, group, merge)
or operate on an existing run (review). list and status read the
runs/ directory.`,
	GroupID: groupStages,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.AddGroup(
		&cobra.Group{ID: groupRunStage, Title: "Stage runners:"},
		&cobra.Group{ID: groupRunRead, Title: "Run inspection:"},
	)
}
