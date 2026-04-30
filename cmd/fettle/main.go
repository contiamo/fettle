// Command fettle is the CLI for the fettle audit harness.
//
// See FETTLE.md at the repo root for the design.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "fettle",
	Short: "File-oriented LLM audit harness",
	Long: `fettle runs LLM agents over a codebase to find, review, group,
and resolve issues. Each scan lives in a self-contained run folder under
runs/, with the prompt that produced it snapshotted alongside the data.

See FETTLE.md at the repo root for the full design.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "fettle: "+err.Error())
		os.Exit(1)
	}
}
