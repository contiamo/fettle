package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

const fettleRunEnv = "FETTLE_RUN"

// findingCmd is the parent of `fettle finding ...` subcommands.
var findingCmd = &cobra.Command{
	Use:   "finding",
	Short: "Operate on findings inside the active fettle run",
}

var findingAddFlags struct {
	file        string
	line        int
	title       string
	description string
	suggestion  string
	severity    string
	labels      []string
	references  []string
	verbose     bool
}

var findingAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Append one finding to the active run's findings.jsonl",
	Long: `add records one finding in the run identified by $FETTLE_RUN.

Intended to be called by the agent fettle has spawned during a find
stage; the harness sets FETTLE_RUN before invoking the agent. Each
invocation appends one row to findings.jsonl under a cross-process
lock, so concurrent agent processes can write safely.

The id is generated server-side. Two findings with identical (file,
line, title) get distinct ids — fettle does not dedupe.

Exit codes: 0 on success, 1 on validation error, 2 on internal error.`,
	RunE: runFindingAdd,
}

func init() {
	findingAddCmd.Flags().StringVar(&findingAddFlags.file, "file", "", "repo-relative path to the file the finding is anchored to (required)")
	findingAddCmd.Flags().IntVar(&findingAddFlags.line, "line", 0, "1-based line number where the finding starts (required, >= 1)")
	findingAddCmd.Flags().StringVar(&findingAddFlags.title, "title", "", "short imperative title (required)")
	findingAddCmd.Flags().StringVar(&findingAddFlags.description, "description", "", "2-5 sentences describing the issue (required)")
	findingAddCmd.Flags().StringVar(&findingAddFlags.suggestion, "suggestion", "", "1-3 sentences with a concrete fix (required)")
	findingAddCmd.Flags().StringVar(&findingAddFlags.severity, "severity", "", "severity (free-form string; e.g. low|medium|high)")
	findingAddCmd.Flags().StringSliceVar(&findingAddFlags.labels, "label", nil, "label of the form prefix:value, repeatable")
	findingAddCmd.Flags().StringSliceVar(&findingAddFlags.references, "reference", nil, "additional code location PATH or PATH:LINE, repeatable")
	findingAddCmd.Flags().BoolVar(&findingAddFlags.verbose, "verbose", false, "print the new finding's id to stdout on success")

	findingCmd.AddCommand(findingAddCmd)
	rootCmd.AddCommand(findingCmd)
}

func runFindingAdd(cmd *cobra.Command, args []string) error {
	runDir := os.Getenv(fettleRunEnv)
	if runDir == "" {
		return internalError(fmt.Errorf("%s is not set; this command must be invoked by the fettle harness during a stage", fettleRunEnv))
	}
	rp, err := run.Open(runDir)
	if err != nil {
		return internalError(fmt.Errorf("open run %s: %w", runDir, err))
	}

	references, refErrs := parseReferences(findingAddFlags.references)

	var problems []string
	if findingAddFlags.file == "" {
		problems = append(problems, "--file is required")
	}
	if findingAddFlags.line < 1 {
		problems = append(problems, "--line must be >= 1")
	}
	if strings.TrimSpace(findingAddFlags.title) == "" {
		problems = append(problems, "--title is required")
	}
	if strings.TrimSpace(findingAddFlags.description) == "" {
		problems = append(problems, "--description is required")
	}
	if strings.TrimSpace(findingAddFlags.suggestion) == "" {
		problems = append(problems, "--suggestion is required")
	}
	for _, e := range refErrs {
		problems = append(problems, e)
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	var severity *string
	if s := strings.TrimSpace(findingAddFlags.severity); s != "" {
		severity = &s
	}
	labels := findingAddFlags.labels
	if labels == nil {
		labels = []string{}
	}
	if references == nil {
		references = []schema.Reference{}
	}

	finding := schema.Finding{
		ID:          schema.NewFindingID(),
		File:        findingAddFlags.file,
		Line:        findingAddFlags.line,
		Title:       strings.TrimSpace(findingAddFlags.title),
		Description: strings.TrimSpace(findingAddFlags.description),
		Suggestion:  strings.TrimSpace(findingAddFlags.suggestion),
		Severity:    severity,
		Labels:      labels,
		References:  references,
		CreatedAt:   time.Now().UTC(),
		// CreatedBy is not set here — the harness records agent identity
		// in run.json's stages.find entry, and the per-finding field is
		// less useful than per-stage identity. Keep empty for now.
	}
	if err := rp.AppendFinding(finding); err != nil {
		return internalError(fmt.Errorf("append finding: %w", err))
	}
	if findingAddFlags.verbose {
		fmt.Println(finding.ID)
	}
	return nil
}

// parseReferences turns each "PATH" or "PATH:LINE" string into a Reference.
// PATH:LINE uses the *last* colon as the separator, and LINE must be a
// positive integer; otherwise the entry is reported as a validation error.
func parseReferences(refs []string) ([]schema.Reference, []string) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]schema.Reference, 0, len(refs))
	var errs []string
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			errs = append(errs, "--reference must not be empty")
			continue
		}
		idx := strings.LastIndex(r, ":")
		if idx < 0 {
			out = append(out, schema.Reference{File: r})
			continue
		}
		path := r[:idx]
		lineStr := r[idx+1:]
		line, err := strconv.Atoi(lineStr)
		if err != nil || line < 1 {
			errs = append(errs, fmt.Sprintf("--reference %q: line must be a positive integer", r))
			continue
		}
		if path == "" {
			errs = append(errs, fmt.Sprintf("--reference %q: path must not be empty", r))
			continue
		}
		out = append(out, schema.Reference{File: path, Line: line})
	}
	return out, errs
}

// validationError signals a usage/validation failure; main.go maps it to
// exit code 1.
func validationError(problems []string) error {
	return &cliError{
		exit: 1,
		msg:  "validation:\n  " + strings.Join(problems, "\n  "),
	}
}

// internalError signals a non-validation failure (I/O, lock, run open);
// main.go maps it to exit code 2.
func internalError(err error) error {
	return &cliError{
		exit: 2,
		msg:  err.Error(),
	}
}

// cliError carries a desired process exit code through cobra's RunE
// path. main.go inspects it before calling os.Exit.
type cliError struct {
	exit int
	msg  string
}

func (e *cliError) Error() string { return e.msg }
func (e *cliError) ExitCode() int { return e.exit }
