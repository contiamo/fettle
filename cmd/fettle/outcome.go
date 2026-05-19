package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

var addOutcomeFlags struct {
	run     string
	finding string
	status  string
	prURL   string
	verbose bool
}

var addOutcomeCmd = &cobra.Command{
	Use:   "outcome",
	Short: "Append an outcome event for a finding",
	Long: `add outcome records that a finding has been disposed of (PR
merged, won't fix, etc.). Append-only — re-marking is allowed; the
latest entry wins for current-state display, but the full history
is preserved.

Author identity (the slug stamped into ` + "`marked_by`" + `) chains:
$FETTLE_AGENT (if running under a stage) → $FETTLE_AUTHOR → the
slug in ~/.config/fettle/identity. Agent stamps come out as
` + "`agent:<name>[/<model>]`" + `; humans as ` + "`human:<slug>`" + `.

Exit codes: 0 success, 1 validation error, 2 internal error.`,
	RunE: runAddOutcome,
}

var listOutcomesFlags struct {
	run string
}

var listOutcomesCmd = &cobra.Command{
	Use:   "outcomes",
	Short: "Print all outcome events in --run as a JSON array",
	Long: `list outcomes walks every finding doc in --run and flattens the
embedded outcomes[] arrays into one chronologically-sorted JSON array.
Each entry carries a synthesized {kind: "finding", id: <finding>}
subject. Empty runs (or runs whose findings have no outcome events
yet) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runListOutcomes,
}

var showOutcomeFlags struct {
	run     string
	finding string
	all     bool
}

var showOutcomeCmd = &cobra.Command{
	Use:   "outcome",
	Short: "Print the latest (or full) outcome history for one finding",
	Long: `show outcome prints outcome events for one finding as the
{"data": ...} envelope.

Default: prints the latest event only (current state). With --all,
prints every event in chronological order — including superseded
ones — so you can see who marked what when.

Exits non-zero if no outcome events exist for the finding.

Exit codes: 0 success, 1 validation / not-found, 2 internal error.`,
	RunE: runShowOutcome,
}

func init() {
	addOutcomeCmd.Flags().StringVar(&addOutcomeFlags.run, "run", "", "path to the target run folder (required)")
	addOutcomeCmd.Flags().StringVar(&addOutcomeFlags.finding, "finding", "", "finding id (required)")
	addOutcomeCmd.Flags().StringVar(&addOutcomeFlags.status, "status", "", "outcome status: merged|closed|wontfix|... (required; project-defined)")
	addOutcomeCmd.Flags().StringVar(&addOutcomeFlags.prURL, "pr", "", "URL of the PR or commit (optional)")
	addOutcomeCmd.Flags().BoolVar(&addOutcomeFlags.verbose, "verbose", false, "print the finding id on success")
	_ = addOutcomeCmd.MarkFlagRequired("run")
	_ = addOutcomeCmd.MarkFlagRequired("finding")
	addCmd.AddCommand(addOutcomeCmd)

	listOutcomesCmd.Flags().StringVar(&listOutcomesFlags.run, "run", "", "path to the run folder (required)")
	_ = listOutcomesCmd.MarkFlagRequired("run")
	listCmd.AddCommand(listOutcomesCmd)

	showOutcomeCmd.Flags().StringVar(&showOutcomeFlags.run, "run", "", "path to the target run folder (required)")
	showOutcomeCmd.Flags().StringVar(&showOutcomeFlags.finding, "finding", "", "finding id (required)")
	showOutcomeCmd.Flags().BoolVar(&showOutcomeFlags.all, "all", false, "print every outcome event for the finding (default: latest only)")
	_ = showOutcomeCmd.MarkFlagRequired("run")
	_ = showOutcomeCmd.MarkFlagRequired("finding")
	showCmd.AddCommand(showOutcomeCmd)
}

func runAddOutcome(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunWithManifest(addOutcomeFlags.run)
	if err != nil {
		return err
	}

	var problems []string
	if addOutcomeFlags.finding == "" {
		problems = append(problems, "--finding is required")
	}
	if strings.TrimSpace(addOutcomeFlags.status) == "" {
		problems = append(problems, "--status is required (e.g. merged|closed|wontfix)")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveOutcomeSubject(rp, manifest.Stage, addOutcomeFlags.finding)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	author, err := stamp()
	if err != nil {
		return validationError([]string{err.Error()})
	}

	entry := schema.OutcomeEntry{
		Kind:   schema.SubjectFinding,
		ID:     subject.ID,
		Author: author,
		At:     time.Now().UTC(),
		Status: strings.TrimSpace(addOutcomeFlags.status),
		PRURL:  strings.TrimSpace(addOutcomeFlags.prURL),
	}
	if err := schema.ValidateOutcomeEntry(entry); err != nil {
		return validationError([]string{err.Error()})
	}
	if err := rp.AppendOutcomeEntry(entry); err != nil {
		return internalError(fmt.Errorf("save outcome: %w", err))
	}
	if err := rp.Close(); err != nil {
		return internalError(fmt.Errorf("close run: %w", err))
	}
	return printAddResult(map[string]any{
		"subject": subject,
		"at":      entry.At,
		"author":  entry.Author,
	}, addOutcomeFlags.verbose, subject.ID)
}

// outcomeRecord is the wire shape for `list outcomes` / `show
// outcome` output: the OutcomeEntry's payload paired with a synthesized
// {kind, id} Subject so consumers can route entries back to a finding
// the same way they did before the on-disk layout flipped to JSONL.
type outcomeRecord struct {
	Subject schema.Subject `json:"subject"`
	Author  string         `json:"author"`
	Status  string         `json:"status"`
	PRURL   string         `json:"pr_url,omitempty"`
	At      time.Time      `json:"at"`
}

func runListOutcomes(cmd *cobra.Command, args []string) error {
	rp, _, err := openRunWithManifest(listOutcomesFlags.run)
	if err != nil {
		return err
	}
	all, err := rp.LoadOutcomeEntries()
	if err != nil {
		return internalError(fmt.Errorf("load outcomes: %w", err))
	}
	out := make([]outcomeRecord, len(all))
	for i, e := range all {
		out[i] = outcomeRecord{
			Subject: schema.Subject{Kind: e.Kind, ID: e.ID},
			Author:  e.Author,
			Status:  e.Status,
			PRURL:   e.PRURL,
			At:      e.At,
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if err := printJSON(out); err != nil {
		return internalError(fmt.Errorf("emit outcomes: %w", err))
	}
	return nil
}

func runShowOutcome(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunWithManifest(showOutcomeFlags.run)
	if err != nil {
		return err
	}
	subject, kindErr := resolveOutcomeSubject(rp, manifest.Stage, showOutcomeFlags.finding)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	all, err := rp.LoadOutcomeEntries()
	if err != nil {
		return internalError(fmt.Errorf("load outcomes: %w", err))
	}
	matchingEntries := make([]schema.OutcomeEntry, 0, len(all))
	for _, e := range all {
		if e.Kind == subject.Kind && e.ID == subject.ID {
			matchingEntries = append(matchingEntries, e)
		}
	}
	if len(matchingEntries) == 0 {
		return validationError([]string{fmt.Sprintf("no outcome events for %s %q in %s", subject.Kind, subject.ID, showOutcomeFlags.run)})
	}

	if showOutcomeFlags.all {
		// Full history — same chronological order as the resolver
		// uses, with the (At, AuthorSlug) tie-breaker preserved.
		history := schema.SortOutcomesChronological(matchingEntries)
		out := make([]outcomeRecord, len(history))
		for i, e := range history {
			out[i] = outcomeRecord{Subject: subject, Author: e.Author, Status: e.Status, PRURL: e.PRURL, At: e.At}
		}
		if err := printJSON(out); err != nil {
			return internalError(fmt.Errorf("emit outcomes: %w", err))
		}
		return nil
	}
	latest := schema.ResolveOutcome(matchingEntries)
	if err := printJSON(outcomeRecord{
		Subject: subject, Author: latest.Author, Status: latest.Status, PRURL: latest.PRURL, At: latest.At,
	}); err != nil {
		return internalError(fmt.Errorf("emit outcome: %w", err))
	}
	return nil
}

// openRunWithManifest resolves --run via run.LookupRun (accepting
// absolute / relative paths, full run names, or slug prefixes) and
// returns the opened path plus its manifest.
func openRunWithManifest(rawRun string) (*run.Path, schema.RunManifest, error) {
	if rawRun == "" {
		return nil, schema.RunManifest{}, validationError([]string{"--run is required"})
	}
	abs, err := run.LookupRun(rawRun, projectDir)
	if err != nil {
		return nil, schema.RunManifest{}, validationError([]string{fmt.Sprintf("--run %s: %v", rawRun, err)})
	}
	rp, err := run.Open(abs)
	if err != nil {
		return nil, schema.RunManifest{}, internalError(fmt.Errorf("open run %s: %w", rawRun, err))
	}
	manifest, err := rp.Manifest()
	if err != nil {
		return nil, schema.RunManifest{}, internalError(fmt.Errorf("read run manifest: %w", err))
	}
	return rp, manifest, nil
}

// resolveOutcomeSubject enforces the stage→subject-kind rules and
// confirms the referenced id exists. Mirrors resolveReviewSubject
// in review.go.
func resolveOutcomeSubject(rp *run.Path, stage string, findingID string) (schema.Subject, error) {
	if stage != "find" {
		return schema.Subject{}, fmt.Errorf("unsupported run stage %q for outcome", stage)
	}
	exists, err := rp.FindingEntryExists(findingID)
	if err != nil {
		return schema.Subject{}, fmt.Errorf("check finding %q: %w", findingID, err)
	}
	if !exists {
		return schema.Subject{}, fmt.Errorf("finding %q not found in this run", findingID)
	}
	return schema.Subject{Kind: schema.SubjectFinding, ID: findingID}, nil
}
