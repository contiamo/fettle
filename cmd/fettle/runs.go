package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/contiamo/fettle/internal/run"
	"github.com/spf13/cobra"
)

var listRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List all runs in the project with summary counts",
	Long: `list runs walks runs/ and emits one entry per run folder, sorted
by created_at descending (newest first). Each entry has the run's
identity, provenance (input_run / input_runs / target_repo), and a
counts block.

Output is the standard {"data": [...]} envelope.`,
	RunE: runListRuns,
}

var showRunCmd = &cobra.Command{
	Use:   "run PATH",
	Short: "Print one run's summary (status + counts)",
	Long: `show run prints a single run's summary as the {"data": {...}}
envelope — same shape as one entry from ` + "`fettle list runs`" + `.
PATH may be relative to the project directory or absolute.

Scope is records that live in the run folder itself (findings,
groups, reviews, outcomes); downstream runs are separate runs with
their own status.

Exit codes: 0 success, 1 not a run folder, 2 internal error.`,
	Args: cobra.ExactArgs(1),
	RunE: runShowRun,
}

func init() {
	listCmd.AddCommand(listRunsCmd)
	showCmd.AddCommand(showRunCmd)
}

func runListRuns(cmd *cobra.Command, args []string) error {
	dir, err := projectDir()
	if err != nil {
		return internalError(err)
	}
	runsDir := filepath.Join(dir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return printJSON([]run.Summary{})
		}
		return internalError(fmt.Errorf("read runs/: %w", err))
	}

	summaries := make([]run.Summary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := run.Summarize(filepath.Join(runsDir, e.Name()))
		if err != nil {
			// Subdirectory without a valid run.json — silently skip
			// (it's not a fettle run, just something else under runs/).
			continue
		}
		summaries = append(summaries, s)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return printJSON(summaries)
}

func runShowRun(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(args[0])
	if err != nil {
		return err
	}
	s, err := run.Summarize(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("summarize run: %w", err))
	}
	return printJSON(s)
}
