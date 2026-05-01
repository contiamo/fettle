package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

const (
	fettleRunEnv   = "FETTLE_RUN"
	fettleAgentEnv = "FETTLE_AGENT"
	fettleModelEnv = "FETTLE_MODEL"
)

// findCmd is the parent of `fettle find <verb>` record subcommands
// (add, show, ...). Distinct from `fettle run find` which runs the
// find stage.
var findCmd = &cobra.Command{
	Use:     "find",
	Short:   "Operate on findings (add, list, show)",
	GroupID: groupRecords,
}

var findAddFlags struct {
	file        string
	line        int
	title       string
	description string
	suggestion  string
	severity    string
	labels      []string
	references  []string
	canonicalOf []string
	verbose     bool
}

var findAddCmd = &cobra.Command{
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
	RunE: runFindAdd,
}

func init() {
	findAddCmd.Flags().StringVar(&findAddFlags.file, "file", "", "repo-relative path to the file the finding is anchored to (required)")
	findAddCmd.Flags().IntVar(&findAddFlags.line, "line", 0, "1-based line number where the finding starts (required, >= 1)")
	findAddCmd.Flags().StringVar(&findAddFlags.title, "title", "", "short imperative title (required)")
	findAddCmd.Flags().StringVar(&findAddFlags.description, "description", "", "2-5 sentences describing the issue (required)")
	findAddCmd.Flags().StringVar(&findAddFlags.suggestion, "suggestion", "", "1-3 sentences with a concrete fix (required)")
	findAddCmd.Flags().StringVar(&findAddFlags.severity, "severity", "", "severity (free-form string; e.g. low|medium|high)")
	findAddCmd.Flags().StringSliceVar(&findAddFlags.labels, "label", nil, "label of the form prefix:value, repeatable")
	findAddCmd.Flags().StringSliceVar(&findAddFlags.references, "reference", nil, "additional code location PATH or PATH:LINE, repeatable")
	findAddCmd.Flags().StringArrayVar(&findAddFlags.canonicalOf, "canonical-of", nil, "source RUN:FINDING_ID this canonical finding subsumes (required in dedupe runs, rejected in find runs); repeatable")
	findAddCmd.Flags().BoolVar(&findAddFlags.verbose, "verbose", false, "print the new finding's id to stdout on success")

	findCmd.AddCommand(findAddCmd)

	findShowCmd.Flags().StringVar(&findShowFlags.run, "run", "", "path to the run folder containing the finding (required)")
	_ = findShowCmd.MarkFlagRequired("run")
	findCmd.AddCommand(findShowCmd)

	findListCmd.Flags().StringVar(&findListFlags.run, "run", "", "path to the run folder to list findings from (required)")
	_ = findListCmd.MarkFlagRequired("run")
	findCmd.AddCommand(findListCmd)

	rootCmd.AddCommand(findCmd)
}

var findShowFlags struct {
	run string
}

var findShowCmd = &cobra.Command{
	Use:   "show ID",
	Short: "Print one finding record as JSON",
	Long: `show prints a single finding from --run by id, as pretty JSON
on stdout. Use --run to pick the find/merge/dedupe run that owns
the finding.

Exit codes: 0 found, 1 not found / validation, 2 internal error.`,
	Args: cobra.ExactArgs(1),
	RunE: runFindShow,
}

var findListFlags struct {
	run string
}

var findListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print all findings in --run as a JSON array",
	Long: `list dumps every finding in --run as a JSON array on stdout.
For ad-hoc filtering, pipe through jq. Empty runs (or missing
findings.jsonl) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runFindList,
}

func runFindShow(cmd *cobra.Command, args []string) error {
	id := args[0]
	rp, err := openRunForRead(findShowFlags.run)
	if err != nil {
		return err
	}
	findings, err := loadFindingsFromRun(rp.Dir())
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
	return validationError([]string{fmt.Sprintf("finding %q not found in %s", id, findShowFlags.run)})
}

func runFindList(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(findListFlags.run)
	if err != nil {
		return err
	}
	findings, err := loadFindingsFromRun(rp.Dir())
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
// or absolute) and opens it. Used by the read commands (show / list).
// Distinct from openRunForClose only because the close path also
// needs the manifest; this one doesn't.
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

func runFindAdd(cmd *cobra.Command, args []string) error {
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

	references, refErrs := parseReferences(findAddFlags.references)

	var problems []string
	if findAddFlags.file == "" {
		problems = append(problems, "--file is required")
	}
	if findAddFlags.line < 1 {
		problems = append(problems, "--line must be >= 1")
	}
	if strings.TrimSpace(findAddFlags.title) == "" {
		problems = append(problems, "--title is required")
	}
	if strings.TrimSpace(findAddFlags.description) == "" {
		problems = append(problems, "--description is required")
	}
	if strings.TrimSpace(findAddFlags.suggestion) == "" {
		problems = append(problems, "--suggestion is required")
	}
	for _, e := range refErrs {
		problems = append(problems, e)
	}

	// Stage-aware --canonical-of rules.
	switch manifest.Stage {
	case "find":
		if len(findAddFlags.canonicalOf) > 0 {
			problems = append(problems, "--canonical-of is rejected in find runs (it's required in dedupe runs only)")
		}
	case "dedupe":
		if len(findAddFlags.canonicalOf) == 0 {
			problems = append(problems, "--canonical-of is required in dedupe runs (one or more RUN:FINDING_ID entries)")
		}
	case "group":
		problems = append(problems, "find add is rejected in group runs; use group add instead")
	default:
		problems = append(problems, fmt.Sprintf("find add is not supported in %q runs", manifest.Stage))
	}

	members, memberErrs := parseCanonicalOf(rp, manifest, findAddFlags.canonicalOf)
	for _, e := range memberErrs {
		problems = append(problems, e)
	}

	if len(problems) > 0 {
		return validationError(problems)
	}

	var severity *string
	if s := strings.TrimSpace(findAddFlags.severity); s != "" {
		severity = &s
	}
	labels := findAddFlags.labels
	if labels == nil {
		labels = []string{}
	}
	if references == nil {
		references = []schema.Reference{}
	}

	createdBy := composeCreatedBy(os.Getenv(fettleAgentEnv), os.Getenv(fettleModelEnv))
	finding := schema.Finding{
		ID:          schema.NewFindingID(),
		File:        findAddFlags.file,
		Line:        findAddFlags.line,
		Title:       strings.TrimSpace(findAddFlags.title),
		Description: strings.TrimSpace(findAddFlags.description),
		Suggestion:  strings.TrimSpace(findAddFlags.suggestion),
		Severity:    severity,
		Labels:      labels,
		References:  references,
		Members:     members,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().UTC(),
	}
	if err := rp.AppendFinding(finding); err != nil {
		return internalError(fmt.Errorf("append finding: %w", err))
	}
	return printAddResult(map[string]any{"id": finding.ID}, findAddFlags.verbose, finding.ID)
}

// resolveAuthor returns the identity to stamp on records that
// fettle attributes back (reviews, closures). The chain is:
//
//   - FETTLE_AGENT       — agent name set by the harness during a stage
//   - $FETTLE_AUTHOR     — explicit per-invocation override
//   - ~/.config/fettle/identity — the slug the UI/init persisted
//
// Returns (slug, isAgent, error). The caller decides how to format
// the per-record `marked_by` / `created_by` field — agents get an
// `agent:<name>[/<model>]` prefix, humans get `human:<slug>`.
func resolveAuthor() (string, bool, error) {
	if a := strings.TrimSpace(os.Getenv(fettleAgentEnv)); a != "" {
		return a, true, nil
	}
	if a := strings.TrimSpace(os.Getenv("FETTLE_AUTHOR")); a != "" {
		return a, false, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".config", "fettle", "identity")
		if data, err := os.ReadFile(path); err == nil {
			if slug := strings.TrimSpace(string(data)); slug != "" {
				return slug, false, nil
			}
		}
	}
	return "", false, fmt.Errorf("no author identity: set $FETTLE_AUTHOR, write a slug to ~/.config/fettle/identity, or run `fettle ui` once to pick one")
}

// markedBy returns the `marked_by` / `created_by` string for the
// resolved author. Agents use the existing composeCreatedBy shape so
// closures stamped by an agent match findings stamped by the same
// agent; humans get the simpler `human:<slug>` form.
func markedBy() (string, error) {
	slug, isAgent, err := resolveAuthor()
	if err != nil {
		return "", err
	}
	if isAgent {
		return composeCreatedBy(slug, os.Getenv(fettleModelEnv)), nil
	}
	return "human:" + slug, nil
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

// parseCanonicalOf turns each "RUN:FINDING_ID" string into a Member,
// validating that RUN appears in the dedupe run's input_runs[] and
// FINDING_ID exists in that run's findings.jsonl. Returns nil for
// non-dedupe runs (no parsing needed).
func parseCanonicalOf(rp *run.Path, manifest schema.RunManifest, raws []string) ([]schema.Member, []string) {
	if len(raws) == 0 {
		return nil, nil
	}
	if manifest.Stage != "dedupe" {
		return nil, nil // upstream check already added a problem
	}

	inputSet := map[string]bool{}
	for _, ir := range manifest.InputRuns {
		inputSet[ir] = true
	}

	var out []schema.Member
	var errs []string

	// Resolve the project dir from the run dir (parent's parent).
	runDir := rp.Dir()
	projectDir := filepath.Dir(filepath.Dir(runDir))

	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		idx := strings.LastIndex(raw, ":")
		if idx < 0 {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: expected RUN:FINDING_ID", raw))
			continue
		}
		runRel := raw[:idx]
		findingID := raw[idx+1:]
		if findingID == "" {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: empty finding id", raw))
			continue
		}
		if !inputSet[runRel] {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: %q is not in this run's input_runs[] (%v)", raw, runRel, manifest.InputRuns))
			continue
		}
		// Verify the finding exists in the source run.
		srcPath := filepath.Join(projectDir, runRel)
		srcRP, err := run.Open(srcPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: open source run: %v", raw, err))
			continue
		}
		exists, err := srcRP.FindingExists(findingID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: lookup error: %v", raw, err))
			continue
		}
		if !exists {
			errs = append(errs, fmt.Sprintf("--canonical-of %q: finding %q not found in %s", raw, findingID, runRel))
			continue
		}
		out = append(out, schema.Member{FindingID: findingID, FromRun: runRel})
	}
	return out, errs
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
