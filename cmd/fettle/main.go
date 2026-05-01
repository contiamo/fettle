// Command fettle is the CLI for the fettle audit harness.
//
// See FETTLE.md at the repo root for the design.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootFlags struct {
	dir string
}

var rootCmd = &cobra.Command{
	Use:   "fettle",
	Short: "File-oriented LLM audit harness",
	Long: `fettle runs LLM agents over a codebase to find, review, group,
and close issues. Each scan lives in a self-contained run folder under
runs/, with the prompt that produced it snapshotted alongside the data.

See FETTLE.md at the repo root for the full design.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootFlags.dir, "dir", "", "fettle project directory (default: current directory)")
}

// exitCoder is implemented by errors that want a specific process exit
// code. Used by `fettle find add` to distinguish validation (1) from
// internal failures (2).
type exitCoder interface {
	ExitCode() int
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "fettle: "+err.Error())
		if ec, ok := err.(exitCoder); ok {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}

// projectDir returns the resolved project directory: the --dir flag if
// set, otherwise the current working directory.
func projectDir() (string, error) {
	if rootFlags.dir != "" {
		abs, err := filepath.Abs(rootFlags.dir)
		if err != nil {
			return "", fmt.Errorf("resolve --dir: %w", err)
		}
		return abs, nil
	}
	return os.Getwd()
}
