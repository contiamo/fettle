package main

import "github.com/spf13/cobra"

// runCmd is the parent of the agent-driven stage subcommands:
// `fettle run find`, `fettle run review`, etc.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run an agent-driven stage (find, review, dedupe, group)",
	Long: `Stage runners. find, dedupe, and group create new run folders;
review operates on an existing run via --run.`,
}

func init() {
	rootCmd.AddCommand(runCmd)
}
