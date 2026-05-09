package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

var addReviewFlags struct {
	finding     string
	labels      []string
	clearLabels bool
	severity    string
	comment     string
	verbose     bool
}

var addReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Append a review entry to the target finding's reviews[]",
	Long: `add review records one review entry in the run identified by
$FETTLE_RUN. The entry is appended to the target finding's reviews[]
array via an atomic-rename rewrite of findings/<id>.json.

Author identity (the canonical attribution stamp) comes from
$FETTLE_AGENT. The harness sets that env var when invoking an agent
during a review stage; humans calling ` + "`fettle add review`" + ` directly
need to set it themselves (or use the UI, which manages identity).

Subject is --finding ID. The harness validates that a doc exists at
findings/<id>.json and rejects unknown ids.

Exit codes: 0 success, 1 validation error, 2 internal error.`,
	RunE: runAddReview,
}

var listReviewsFlags struct {
	run string
}

var listReviewsCmd = &cobra.Command{
	Use:   "reviews",
	Short: "Print all review entries in --run as a JSON array",
	Long: `list reviews walks every finding doc in --run and flattens the
embedded reviews[] arrays into one chronologically-sorted JSON array.
Each entry carries a synthesized {kind: "finding", id: <finding>}
subject so consumers can route entries back to their finding.

Empty runs (or runs whose findings have no review entries yet) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runListReviews,
}

var showReviewFlags struct {
	run     string
	finding string
	all     bool
}

var showReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Print review state for one finding",
	Long: `show review prints review state for a single finding. Default
emits the derived current state — for each author, the latest
entry's full label set, plus a current_labels union across authors.
With --all, emits every entry chronologically (including superseded
entries).

Exits non-zero if no review entries exist for the finding.

Exit codes: 0 success, 1 validation / not-found, 2 internal error.`,
	RunE: runShowReview,
}

func init() {
	addReviewCmd.Flags().StringVar(&addReviewFlags.finding, "finding", "", "review subject is a finding with this id (required)")
	addReviewCmd.Flags().StringSliceVar(&addReviewFlags.labels, "label", nil, "label of the form prefix:value (or any string), repeatable; omit to leave labels untouched")
	addReviewCmd.Flags().BoolVar(&addReviewFlags.clearLabels, "clear-labels", false, "explicitly clear all of this reviewer's labels (otherwise omitting --label leaves them as-is)")
	addReviewCmd.Flags().StringVar(&addReviewFlags.severity, "severity", "", "reviewer's severity judgment (overrides the LLM's initial value); leave unset to defer")
	addReviewCmd.Flags().StringVar(&addReviewFlags.comment, "comment", "", "free-form comment")
	addReviewCmd.Flags().BoolVar(&addReviewFlags.verbose, "verbose", false, "print the appended finding id on success")
	addCmd.AddCommand(addReviewCmd)

	listReviewsCmd.Flags().StringVar(&listReviewsFlags.run, "run", "", "path to the run folder (required)")
	_ = listReviewsCmd.MarkFlagRequired("run")
	listCmd.AddCommand(listReviewsCmd)

	showReviewCmd.Flags().StringVar(&showReviewFlags.run, "run", "", "path to the run folder (required)")
	showReviewCmd.Flags().StringVar(&showReviewFlags.finding, "finding", "", "finding id (required)")
	showReviewCmd.Flags().BoolVar(&showReviewFlags.all, "all", false, "print every review entry for the finding (default: derived current state)")
	_ = showReviewCmd.MarkFlagRequired("run")
	_ = showReviewCmd.MarkFlagRequired("finding")
	showCmd.AddCommand(showReviewCmd)
}

func runAddReview(cmd *cobra.Command, args []string) error {
	runDir := os.Getenv(fettleRunEnv)
	if runDir == "" {
		return internalError(fmt.Errorf("%s is not set; this command must be invoked by the fettle harness during a review stage (or with FETTLE_RUN set explicitly)", fettleRunEnv))
	}
	rp, err := run.Open(runDir)
	if err != nil {
		return internalError(fmt.Errorf("open run %s: %w", runDir, err))
	}
	manifest, err := rp.Manifest()
	if err != nil {
		return internalError(fmt.Errorf("read run manifest: %w", err))
	}

	var problems []string
	if addReviewFlags.finding == "" {
		problems = append(problems, "--finding is required")
	}

	if len(addReviewFlags.labels) == 0 && strings.TrimSpace(addReviewFlags.comment) == "" {
		problems = append(problems, "at least one --label or a --comment is required")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, addReviewFlags.finding)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	if os.Getenv(fettleAgentEnv) == "" {
		return internalError(fmt.Errorf("%s is not set; cannot derive reviewer slug", fettleAgentEnv))
	}
	authorStamp, err := stamp()
	if err != nil {
		return validationError([]string{err.Error()})
	}

	var severity *string
	if s := strings.TrimSpace(addReviewFlags.severity); s != "" {
		severity = &s
	}
	// Pointer-to-slice carries the "didn't touch labels" signal: if
	// no --label flag was passed, send nil so the reviewer's prior
	// override (or the LLM's Finding.Labels) stays in effect; if any
	// --label was passed, send the slice as an explicit override.
	// To clear all labels intentionally, pass --clear-labels.
	var labels *[]string
	switch {
	case addReviewFlags.clearLabels:
		empty := []string{}
		labels = &empty
	case len(addReviewFlags.labels) > 0:
		copied := append([]string{}, addReviewFlags.labels...)
		labels = &copied
	}
	review := schema.Review{
		Author:   authorStamp,
		Labels:   labels,
		Severity: severity,
		Comment:  strings.TrimSpace(addReviewFlags.comment),
		At:       time.Now().UTC(),
	}
	if err := rp.UpdateFinding(subject.ID, func(d *schema.FindingDoc) error {
		d.Reviews = append(d.Reviews, review)
		return nil
	}); err != nil {
		return internalError(fmt.Errorf("save review: %w", err))
	}
	return printAddResult(map[string]any{"subject": subject, "at": review.At, "author": review.Author}, addReviewFlags.verbose, subject.ID)
}

func runListReviews(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(listReviewsFlags.run)
	if err != nil {
		return err
	}
	all, err := loadAllReviewEntries(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("load reviews: %w", err))
	}
	if all == nil {
		all = []reviewEntry{}
	}
	if err := printJSON(all); err != nil {
		return internalError(fmt.Errorf("emit reviews: %w", err))
	}
	return nil
}

func runShowReview(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunWithManifest(showReviewFlags.run)
	if err != nil {
		return err
	}
	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, showReviewFlags.finding)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	all, err := loadAllReviewEntries(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("load reviews: %w", err))
	}

	matching := make([]reviewEntry, 0, len(all))
	for _, e := range all {
		if e.Subject == subject {
			matching = append(matching, e)
		}
	}
	if len(matching) == 0 {
		return validationError([]string{fmt.Sprintf("no review entries for %s %q in %s", subject.Kind, subject.ID, showReviewFlags.run)})
	}

	if showReviewFlags.all {
		if err := printJSON(matching); err != nil {
			return internalError(fmt.Errorf("emit reviews: %w", err))
		}
		return nil
	}
	if err := printJSON(deriveReviewState(matching)); err != nil {
		return internalError(fmt.Errorf("emit review state: %w", err))
	}
	return nil
}

// reviewEntry is one review record paired with the synthesized
// subject identifying the finding it belongs to. On disk the review
// lives inside its finding's reviews[] array (no Subject stored);
// this struct is the wire shape for `fettle list reviews` /
// `fettle show review` flat output.
type reviewEntry struct {
	Subject schema.Subject `json:"subject"`
	Author  string         `json:"author"`
	// Labels uses pointer-to-slice to preserve schema.Review's
	// nil-means-untouched semantic when piping review entries
	// through CLI output and on into dedupe/group prompts.
	Labels *[]string `json:"labels,omitempty"`
	// Severity carries the reviewer's severity override (or nil
	// when this entry didn't touch severity). Same nil-vs-set
	// semantic as Labels.
	Severity *string   `json:"severity,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at"`
}

// reviewCurrent is the "current state" view of a subject's reviews:
// the derived label union plus the latest entry per author. Matches
// the per-subject shape of REVIEWS_JSON in dedupe / group prompts.
type reviewCurrent struct {
	CurrentLabels []string             `json:"current_labels"`
	Entries       []reviewCurrentEntry `json:"entries"`
}

type reviewCurrentEntry struct {
	Author string `json:"author"`
	// Labels mirrors schema.Review.Labels' nil-means-don't-touch
	// semantics; omitempty drops the field for entries that
	// didn't override (so JSON consumers see the absence rather
	// than a misleading empty array).
	Labels *[]string `json:"labels,omitempty"`
	// Severity mirrors schema.Review.Severity. nil/omitted means
	// this entry didn't touch severity; non-nil is the reviewer's
	// override.
	Severity *string   `json:"severity,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at"`
}

// loadAllReviewEntries walks every finding doc in runDir and
// flattens the embedded reviews[] arrays into one chronological list,
// each entry tagged with its synthesized subject. Tolerates malformed
// docs (skipped, like the rest of the read path).
func loadAllReviewEntries(runDir string) ([]reviewEntry, error) {
	rp, err := run.Open(runDir)
	if err != nil {
		return nil, err
	}
	flat, err := rp.LoadAllReviews()
	if err != nil {
		return nil, err
	}
	out := make([]reviewEntry, len(flat))
	for i, fr := range flat {
		out[i] = reviewEntry{
			Subject:  fr.Subject,
			Author:   fr.Author,
			Labels:   fr.Labels,
			Severity: fr.Severity,
			Comment:  fr.Comment,
			At:       fr.At,
		}
	}
	return out, nil
}

// deriveReviewState collapses chronological entries for one subject
// into the "current state" view: per-author effective labels +
// severity (each axis tracked independently as "latest entry that
// touched this axis", so a comment-only entry doesn't wipe a prior
// override on the other axis), plus a label union across authors.
// Authors are emitted in alphabetical order so output is stable.
func deriveReviewState(entries []reviewEntry) reviewCurrent {
	type accum struct {
		labels        *[]string
		labelsAt      time.Time
		severity      *string
		severityAt    time.Time
		latestComment string
		latestAt      time.Time
	}
	byAuthor := map[string]*accum{}
	for _, e := range entries {
		a := byAuthor[e.Author]
		if a == nil {
			a = &accum{}
			byAuthor[e.Author] = a
		}
		if e.Labels != nil && (a.labels == nil || e.At.After(a.labelsAt)) {
			a.labels = e.Labels
			a.labelsAt = e.At
		}
		if e.Severity != nil && (a.severity == nil || e.At.After(a.severityAt)) {
			a.severity = e.Severity
			a.severityAt = e.At
		}
		if e.At.After(a.latestAt) {
			a.latestComment = e.Comment
			a.latestAt = e.At
		}
	}
	authors := make([]string, 0, len(byAuthor))
	for a := range byAuthor {
		authors = append(authors, a)
	}
	sort.Strings(authors)

	out := reviewCurrent{Entries: make([]reviewCurrentEntry, 0, len(authors))}
	labelSet := map[string]bool{}
	for _, name := range authors {
		a := byAuthor[name]
		out.Entries = append(out.Entries, reviewCurrentEntry{
			Author:   name,
			Labels:   a.labels,
			Severity: a.severity,
			Comment:  a.latestComment,
			At:       a.latestAt,
		})
		if a.labels != nil {
			for _, l := range *a.labels {
				labelSet[l] = true
			}
		}
	}
	out.CurrentLabels = make([]string, 0, len(labelSet))
	for l := range labelSet {
		out.CurrentLabels = append(out.CurrentLabels, l)
	}
	sort.Strings(out.CurrentLabels)
	return out
}

// resolveReviewSubject enforces the stage→subject-kind rules and
// confirms the referenced id exists.
func resolveReviewSubject(rp *run.Path, stage string, findingID string) (schema.Subject, error) {
	if stage != "find" {
		return schema.Subject{}, fmt.Errorf("unsupported run stage %q for review", stage)
	}
	exists, err := rp.FindingExists(findingID)
	if err != nil {
		return schema.Subject{}, fmt.Errorf("check finding %q: %w", findingID, err)
	}
	if !exists {
		return schema.Subject{}, fmt.Errorf("finding %q not found in this run", findingID)
	}
	return schema.Subject{Kind: schema.SubjectFinding, ID: findingID}, nil
}
