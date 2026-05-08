package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/anchor"
	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

const (
	fettleRunEnv   = "FETTLE_RUN"
	fettleAgentEnv = identity.EnvAgent
	fettleModelEnv = identity.EnvModel
)

var addFindingFlags struct {
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

var addFindingCmd = &cobra.Command{
	Use:   "finding",
	Short: "Append one finding to the active run's findings.jsonl",
	Long: `add finding records one finding in the run identified by $FETTLE_RUN.

Intended to be called by the agent fettle has spawned during a find
stage; the harness sets FETTLE_RUN before invoking the agent. Each
invocation appends one row to findings.jsonl under a cross-process
lock, so concurrent agent processes can write safely.

The id is generated server-side. Two findings with identical (file,
line, title) get distinct ids — fettle does not dedupe.

Exit codes: 0 on success, 1 on validation error, 2 on internal error.`,
	RunE: runAddFinding,
}

var showFindingFlags struct {
	run string
}

var showFindingCmd = &cobra.Command{
	Use:   "finding ID",
	Short: "Print one finding record as JSON",
	Long: `show finding prints a single finding from --run by id, as the
{"data": {...}} envelope on stdout. Use --run to pick the find /
merge / dedupe run that owns the finding.

Exit codes: 0 found, 1 not found / validation, 2 internal error.`,
	Args: cobra.ExactArgs(1),
	RunE: runShowFinding,
}

var listFindingsFlags struct {
	run string
}

var listFindingsCmd = &cobra.Command{
	Use:   "findings",
	Short: "Print all findings in --run as a JSON array",
	Long: `list findings dumps every finding in --run as a JSON array on
stdout. For ad-hoc filtering, pipe through jq. Empty runs (or
missing findings.jsonl) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runListFindings,
}

func init() {
	addFindingCmd.Flags().StringVar(&addFindingFlags.file, "file", "", "repo-relative path to the file the finding is anchored to (required)")
	addFindingCmd.Flags().IntVar(&addFindingFlags.line, "line", 0, "1-based line number where the finding starts (required, >= 1)")
	addFindingCmd.Flags().StringVar(&addFindingFlags.title, "title", "", "short imperative title (required)")
	addFindingCmd.Flags().StringVar(&addFindingFlags.description, "description", "", "2-5 sentences describing the issue (required)")
	addFindingCmd.Flags().StringVar(&addFindingFlags.suggestion, "suggestion", "", "1-3 sentences with a concrete fix (required)")
	addFindingCmd.Flags().StringVar(&addFindingFlags.severity, "severity", "", "severity (free-form string; e.g. low|medium|high)")
	addFindingCmd.Flags().StringSliceVar(&addFindingFlags.labels, "label", nil, "label of the form prefix:value, repeatable")
	addFindingCmd.Flags().StringSliceVar(&addFindingFlags.references, "reference", nil, "additional code location PATH or PATH:LINE, repeatable")
	addFindingCmd.Flags().BoolVar(&addFindingFlags.verbose, "verbose", false, "print the new finding's id to stdout on success")
	addCmd.AddCommand(addFindingCmd)

	showFindingCmd.Flags().StringVar(&showFindingFlags.run, "run", "", "path to the run folder containing the finding (required)")
	_ = showFindingCmd.MarkFlagRequired("run")
	showCmd.AddCommand(showFindingCmd)

	listFindingsCmd.Flags().StringVar(&listFindingsFlags.run, "run", "", "path to the run folder to list findings from (required)")
	_ = listFindingsCmd.MarkFlagRequired("run")
	listCmd.AddCommand(listFindingsCmd)
}

func runShowFinding(cmd *cobra.Command, args []string) error {
	id := args[0]
	rp, err := openRunForRead(showFindingFlags.run)
	if err != nil {
		return err
	}
	findings, err := rp.LoadFindings()
	if err != nil {
		return internalError(fmt.Errorf("load findings: %w", err))
	}
	for _, f := range findings {
		if f.ID == id {
			if err := printJSON(f); err != nil {
				return internalError(fmt.Errorf("emit finding: %w", err))
			}
			return nil
		}
	}
	return validationError([]string{fmt.Sprintf("finding %q not found in %s", id, showFindingFlags.run)})
}

func runListFindings(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(listFindingsFlags.run)
	if err != nil {
		return err
	}
	findings, err := rp.LoadFindings()
	if err != nil {
		return internalError(fmt.Errorf("load findings: %w", err))
	}
	if findings == nil {
		findings = []schema.Finding{}
	}
	if err := printJSON(findings); err != nil {
		return internalError(fmt.Errorf("emit findings: %w", err))
	}
	return nil
}

// openRunForRead resolves a --run flag (relative to the project dir
// or absolute) and opens it. Used by every read command (show / list).
func openRunForRead(rawRun string) (*run.Path, error) {
	if rawRun == "" {
		return nil, validationError([]string{"--run is required"})
	}
	abs := rawRun
	if !filepath.IsAbs(abs) {
		dir, err := projectDir()
		if err != nil {
			return nil, internalError(err)
		}
		abs = filepath.Join(dir, rawRun)
	}
	rp, err := run.Open(abs)
	if err != nil {
		return nil, internalError(fmt.Errorf("open run %s: %w", rawRun, err))
	}
	return rp, nil
}

func runAddFinding(cmd *cobra.Command, args []string) error {
	runDir := os.Getenv(fettleRunEnv)
	if runDir == "" {
		return internalError(fmt.Errorf("%s is not set; this command must be invoked by the fettle harness during a stage", fettleRunEnv))
	}
	rp, err := run.Open(runDir)
	if err != nil {
		return internalError(fmt.Errorf("open run %s: %w", runDir, err))
	}
	manifest, err := rp.Manifest()
	if err != nil {
		return internalError(fmt.Errorf("read run manifest: %w", err))
	}

	references, refErrs := parseReferences(addFindingFlags.references)

	var problems []string
	if addFindingFlags.file == "" {
		problems = append(problems, "--file is required")
	}
	if addFindingFlags.line < 1 {
		problems = append(problems, "--line must be >= 1")
	}
	if strings.TrimSpace(addFindingFlags.title) == "" {
		problems = append(problems, "--title is required")
	}
	if strings.TrimSpace(addFindingFlags.description) == "" {
		problems = append(problems, "--description is required")
	}
	if strings.TrimSpace(addFindingFlags.suggestion) == "" {
		problems = append(problems, "--suggestion is required")
	}
	for _, e := range refErrs {
		problems = append(problems, e)
	}

	if manifest.Stage != "find" {
		problems = append(problems, fmt.Sprintf("add finding is not supported in %q runs", manifest.Stage))
	}

	if len(problems) > 0 {
		return validationError(problems)
	}

	var severity *string
	if s := strings.TrimSpace(addFindingFlags.severity); s != "" {
		severity = &s
	}
	labels := addFindingFlags.labels
	if labels == nil {
		labels = []string{}
	}
	if references == nil {
		references = []schema.Reference{}
	}

	createdBy := composeCreatedBy(os.Getenv(fettleAgentEnv), os.Getenv(fettleModelEnv))

	// Capture the line text now so future readers can detect drift if
	// the file changes. A capture failure (file unreadable, line out of
	// range) leaves AnchorLine nil — the finding still records, and the
	// UI shows it with no drift annotation rather than failing the
	// agent's add. Dedupe runs have no direct target_repo on their
	// manifest, so AnchorLine is left nil there too. We log capture
	// failures to stderr so silent degradation doesn't hide a
	// configuration mistake (e.g. agent passing a wrong --line).
	var anchorLine *string
	if manifest.TargetRepo != "" {
		captured, capErr := anchor.Capture(manifest.TargetRepo, addFindingFlags.file, addFindingFlags.line)
		if capErr != nil {
			fmt.Fprintf(os.Stderr, "fettle add finding: anchor capture failed for %s:%d: %v\n", addFindingFlags.file, addFindingFlags.line, capErr)
		} else {
			anchorLine = &captured
		}
	}

	finding := schema.Finding{
		ID:          schema.NewFindingID(),
		File:        addFindingFlags.file,
		Line:        addFindingFlags.line,
		Title:       strings.TrimSpace(addFindingFlags.title),
		Description: strings.TrimSpace(addFindingFlags.description),
		Suggestion:  strings.TrimSpace(addFindingFlags.suggestion),
		Severity:    severity,
		Labels:      labels,
		References:  references,
		AnchorLine:  anchorLine,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().UTC(),
	}
	if err := rp.AppendFinding(finding); err != nil {
		return internalError(fmt.Errorf("append finding: %w", err))
	}
	return printAddResult(map[string]any{"id": finding.ID}, addFindingFlags.verbose, finding.ID)
}

// stamp returns the canonical author/created_by string for the
// resolved identity. Delegates the chain (env → config file → error)
// to internal/identity so the CLI and the UI share one resolver.
// Agents get the `agent:<name>[/<model>]` shape; humans get
// `human:<slug>`. The stringification itself is implemented as
// Resolved.String() (Stringer) on the identity package; this wrapper
// just adds the common error-mapping for missing identity.
func stamp() (string, error) {
	r, err := identity.Resolve()
	if err != nil {
		if errors.Is(err, identity.ErrNoIdentity) {
			return "", fmt.Errorf("%s", identity.ErrorMessage())
		}
		return "", err
	}
	return r.String(), nil
}

// composeCreatedBy reassembles the per-finding `created_by` field from
// the FETTLE_AGENT (name) and FETTLE_MODEL (optional) env vars. Splitting
// these into two env vars keeps the script-facing surface clean — a
// custom agent script can read FETTLE_MODEL directly to pass to its
// underlying CLI without parsing a composite "agent:name/model" string.
func composeCreatedBy(name, model string) string {
	if name == "" {
		return ""
	}
	if model == "" {
		return "agent:" + name
	}
	return "agent:" + name + "/" + model
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
