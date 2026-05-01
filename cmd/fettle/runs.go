package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/spf13/cobra"
)

// runSummary is the per-run shape emitted by `fettle list runs` and
// `fettle show run`. It pulls a small set of identity + provenance
// fields off the manifest plus a counts block summarizing records
// in the run folder.
type runSummary struct {
	Name        string     `json:"name"`
	Stage       string     `json:"stage"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	TargetRepo  string     `json:"target_repo,omitempty"`
	InputRun    string     `json:"input_run,omitempty"`
	InputRuns   []string   `json:"input_runs,omitempty"`
	Counts      runCounts  `json:"counts"`
}

// runCounts breaks counts out by record kind. Findings and Groups
// are stage-specific (find/merge/dedupe carry findings; group
// carries groups), so they use pointers and omit when not applicable.
// Reviews and Outcomes attach to either kind, so they're always
// emitted (zero is meaningful).
type runCounts struct {
	Findings *int `json:"findings,omitempty"`
	Groups   *int `json:"groups,omitempty"`
	Reviews  int  `json:"reviews"`
	Outcomes int  `json:"outcomes"`
}

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
			return printJSON([]runSummary{})
		}
		return internalError(fmt.Errorf("read runs/: %w", err))
	}

	summaries := make([]runSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := summarizeRun(filepath.Join(runsDir, e.Name()))
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
	s, err := summarizeRun(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("summarize run: %w", err))
	}
	return printJSON(s)
}

// summarizeRun reads one run's manifest and counts its record files.
// Counts use countLines (defined in run_dedupe.go), which tolerates
// missing files (returns 0) so partial runs report cleanly.
func summarizeRun(runDir string) (runSummary, error) {
	rp, err := run.Open(runDir)
	if err != nil {
		return runSummary{}, err
	}
	m, err := rp.Manifest()
	if err != nil {
		return runSummary{}, err
	}

	outcomes, err := countLines(filepath.Join(runDir, "outcomes.jsonl"))
	if err != nil {
		return runSummary{}, fmt.Errorf("count outcomes: %w", err)
	}

	reviews, err := countReviews(runDir)
	if err != nil {
		return runSummary{}, fmt.Errorf("count reviews: %w", err)
	}

	counts := runCounts{Reviews: reviews, Outcomes: outcomes}
	switch m.Stage {
	case "find", "merge", "dedupe":
		n, err := countLines(filepath.Join(runDir, "findings.jsonl"))
		if err != nil {
			return runSummary{}, fmt.Errorf("count findings: %w", err)
		}
		counts.Findings = &n
	case "group":
		n, err := countLines(filepath.Join(runDir, "groups.jsonl"))
		if err != nil {
			return runSummary{}, fmt.Errorf("count groups: %w", err)
		}
		counts.Groups = &n
	}

	return runSummary{
		Name:        m.Name,
		Stage:       m.Stage,
		CreatedAt:   m.CreatedAt,
		CompletedAt: m.CompletedAt,
		TargetRepo:  m.TargetRepo,
		InputRun:    m.InputRun,
		InputRuns:   m.InputRuns,
		Counts:      counts,
	}, nil
}

// countReviews sums lines across every reviews_<author>.jsonl in
// runDir. Missing files contribute zero.
func countReviews(runDir string) (int, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "reviews_") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		n, err := countLines(filepath.Join(runDir, name))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
