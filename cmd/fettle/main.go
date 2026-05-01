// Command fettle is the CLI for the fettle audit harness.
//
// See FETTLE.md at the repo root for the design.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootFlags struct {
	dir  string
	json bool
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

// Command groups for `fettle --help` and `fettle run --help`. The
// IDs here are referenced from each subcommand's GroupID field.
const (
	groupProject  = "project"
	groupStages   = "stages"
	groupRecords  = "records"
	groupRunStage = "run-stage"
	groupRunRead  = "run-read"
)

func init() {
	rootCmd.PersistentFlags().StringVar(&rootFlags.dir, "dir", "", "fettle project directory (default: current directory)")
	rootCmd.PersistentFlags().BoolVar(&rootFlags.json, "json", false, "emit structured JSON to stdout (envelope: {\"data\": ...})")

	rootCmd.AddGroup(
		&cobra.Group{ID: groupProject, Title: "Project:"},
		&cobra.Group{ID: groupStages, Title: "Stages (agent-driven work):"},
		&cobra.Group{ID: groupRecords, Title: "Records (read/write run data):"},
	)
}

// printJSON emits v as `{"data": v}` (pretty-printed) to stdout.
// Read commands use this unconditionally; write commands gate it on
// --json. The envelope is forward-compatible — pagination, warnings,
// and other fields land alongside `data` later without breaking
// consumers that already parse `.data`.
func printJSON(v any) error {
	envelope := map[string]any{"data": v}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(envelope)
}

// printRunResult emits the path of a completed stage run. With
// --json, wraps it in the data envelope; without, prints a plain
// path so shell pipelines like `out=$(fettle run find)` keep
// working. Returns the underlying I/O error if any.
func printRunResult(runDir string) error {
	if rootFlags.json {
		return printJSON(map[string]any{"run": runDir})
	}
	_, err := fmt.Println(runDir)
	return err
}

// printAddResult emits the result of an `add` command. With --json,
// emits `{"data": data}`; with the legacy --verbose, prints
// plainText (typically the new id); otherwise silent.
func printAddResult(data map[string]any, verbose bool, plainText string) error {
	if rootFlags.json {
		return printJSON(data)
	}
	if verbose {
		_, err := fmt.Println(plainText)
		return err
	}
	return nil
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
