// Command fettle is the CLI for the fettle audit harness.
//
// See FETTLE.md at the repo root for the design.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

var rootFlags struct {
	projectDir string
	json       bool
}

// EnvProjectDir is the project-directory override env var. Same role
// as --project-dir: tell fettle where the project lives without
// having to be inside it.
const EnvProjectDir = "FETTLE_PROJECT_DIR"

var rootCmd = &cobra.Command{
	Use:   "fettle",
	Short: "File-oriented LLM audit harness",
	Long: `fettle runs LLM agents over a codebase to find, review, and
close issues. Each scan lives in a self-contained run folder under
<project>/runs/, with the prompt that produced it snapshotted
alongside the data. The project directory is whatever folder you
named at ` + "`fettle init`" + ` time; it's marked by a fettle.json
file at its root.

See FETTLE.md at the repo root for the full design.`,
	Version:       project.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Command groups for `fettle --help`. The IDs here are referenced
// from each subcommand's GroupID field.
const (
	groupProject = "project"
	groupStages  = "stages"
	groupRecords = "records"
)

// addCmd, listCmd, showCmd are the verb-first parents under which
// every record-shaped subcommand registers itself. finding / review
// / outcome all hang off these.
var addCmd = &cobra.Command{
	Use:     "add",
	Short:   "Append a record (finding, review, outcome)",
	Long:    `add records run output. Each subcommand corresponds to one record kind.`,
	GroupID: groupRecords,
}

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List records in a run, or all runs in the project",
	Long:    `list reads from the project (runs) or from a single run (--run flag) and prints a JSON array.`,
	GroupID: groupRecords,
}

var showCmd = &cobra.Command{
	Use:     "show",
	Short:   "Print one record (finding, review, outcome) or one run",
	Long:    `show prints a single record from a run, or a single run's status. Output is the {"data": ...} envelope.`,
	GroupID: groupRecords,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&rootFlags.projectDir, "project-dir", "", "fettle project directory (overrides $FETTLE_PROJECT_DIR and the upward-walk from cwd)")
	rootCmd.PersistentFlags().BoolVar(&rootFlags.json, "json", false, "emit structured JSON to stdout (envelope: {\"data\": ...})")

	rootCmd.AddGroup(
		&cobra.Group{ID: groupProject, Title: "Project:"},
		&cobra.Group{ID: groupStages, Title: "Stages (agent-driven work):"},
		&cobra.Group{ID: groupRecords, Title: "Records (read/write run data):"},
	)
	rootCmd.AddCommand(addCmd, listCmd, showCmd)
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
// code. Used by `fettle add finding` to distinguish validation (1) from
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

// projectDir resolves an existing fettle project directory via the
// three-source chain: --project-dir flag → $FETTLE_PROJECT_DIR env →
// upward walk from cwd looking for fettle.json. Used by every command
// that reads or writes inside an existing project; `fettle init` does
// NOT call this — init takes its target as a positional argument so
// it can create the project wherever the user names.
func projectDir() (string, error) {
	if rootFlags.projectDir != "" {
		abs, err := filepath.Abs(rootFlags.projectDir)
		if err != nil {
			return "", fmt.Errorf("resolve --project-dir: %w", err)
		}
		if _, err := os.Stat(filepath.Join(abs, project.ConfigName)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("--project-dir %s: no %s found", abs, project.ConfigName)
			}
			return "", fmt.Errorf("stat %s: %w", filepath.Join(abs, project.ConfigName), err)
		}
		return abs, nil
	}
	if envDir := strings.TrimSpace(os.Getenv(EnvProjectDir)); envDir != "" {
		abs, err := filepath.Abs(envDir)
		if err != nil {
			return "", fmt.Errorf("resolve $%s: %w", EnvProjectDir, err)
		}
		if _, err := os.Stat(filepath.Join(abs, project.ConfigName)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("$%s=%s: no %s found", EnvProjectDir, abs, project.ConfigName)
			}
			return "", fmt.Errorf("stat %s: %w", filepath.Join(abs, project.ConfigName), err)
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return project.FindProjectDir(cwd)
}

// resolvePromptSource returns the absolute path of the prompt to use
// for this stage and a project-relative (or absolute) `source_path`
// to record on the run manifest.
//
// override (the value of `--prompt`) wins when non-empty and is
// resolved relative to the caller's cwd, matching shell-CLI
// conventions. It is recorded as project-relative when the file is
// inside the project tree, else as the absolute path.
//
// Falling back, configRel is the project-relative path from
// fettle.json's `instructions.<stage>` field; it's joined with
// projectDir on read. Returns an error if both are empty or if the
// resolved file doesn't exist.
func resolvePromptSource(projectDir, override, configRel string) (absPath, recordPath string, err error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", "", fmt.Errorf("resolve --prompt %q: %w", override, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", "", fmt.Errorf("--prompt %q: %w", override, err)
		}
		rec := abs
		if rel, err := filepath.Rel(projectDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			rec = rel
		}
		return abs, rec, nil
	}
	if configRel == "" {
		return "", "", fmt.Errorf("no prompt source: pass --prompt <path> or set the relevant `instructions.*` field in fettle.json")
	}
	abs := configRel
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, configRel)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", "", fmt.Errorf("prompt %q (from fettle.json): %w", configRel, err)
	}
	return abs, configRel, nil
}
