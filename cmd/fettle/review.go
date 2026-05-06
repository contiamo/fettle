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
	finding  string
	group    string
	labels   []string
	severity string
	comment  string
	verbose  bool
}

var addReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Append a review entry to the active run's reviews_<author>.jsonl",
	Long: `add review records one review entry in the run identified by
$FETTLE_RUN.

Author identity (the slug used in the filename) comes from
$FETTLE_AGENT. The harness sets that env var when invoking an agent
during a review stage; humans calling ` + "`fettle add review`" + ` directly
need to set it themselves (or use the UI, which manages identity).

Subject is exactly one of --finding ID or --group ID. The harness
validates that the id exists in the run's findings.jsonl (find/
dedupe runs) or groups.jsonl (group runs) and rejects unknown ids
and mismatched subject kinds.

Exit codes: 0 success, 1 validation error, 2 internal error.`,
	RunE: runAddReview,
}

var listReviewsFlags struct {
	run string
}

var listReviewsCmd = &cobra.Command{
	Use:   "reviews",
	Short: "Print all review entries in --run as a JSON array",
	Long: `list reviews dumps every entry from every reviews_<author>.jsonl
in --run as a flat, chronologically-sorted JSON array. Each entry
includes the author derived from its filename.

Empty runs (or runs with no reviews_*.jsonl files) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runListReviews,
}

var showReviewFlags struct {
	run     string
	finding string
	group   string
	all     bool
}

var showReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Print review state for one finding or group",
	Long: `show review prints review state for a single subject. Default
emits the derived current state — for each author, the latest
entry's full label set, plus a current_labels union across authors.
With --all, emits every entry chronologically (including superseded
entries).

Exits non-zero if no review entries exist for the subject.

Exit codes: 0 success, 1 validation / not-found, 2 internal error.`,
	RunE: runShowReview,
}

func init() {
	addReviewCmd.Flags().StringVar(&addReviewFlags.finding, "finding", "", "review subject is a finding with this id")
	addReviewCmd.Flags().StringVar(&addReviewFlags.group, "group", "", "review subject is a group with this id")
	addReviewCmd.Flags().StringSliceVar(&addReviewFlags.labels, "label", nil, "label of the form prefix:value (or any string), repeatable")
	addReviewCmd.Flags().StringVar(&addReviewFlags.severity, "severity", "", "reviewer's severity judgment (overrides the LLM's initial value); leave unset to defer")
	addReviewCmd.Flags().StringVar(&addReviewFlags.comment, "comment", "", "free-form comment")
	addReviewCmd.Flags().BoolVar(&addReviewFlags.verbose, "verbose", false, "print the appended subject id on success")
	addCmd.AddCommand(addReviewCmd)

	listReviewsCmd.Flags().StringVar(&listReviewsFlags.run, "run", "", "path to the run folder (required)")
	_ = listReviewsCmd.MarkFlagRequired("run")
	listCmd.AddCommand(listReviewsCmd)

	showReviewCmd.Flags().StringVar(&showReviewFlags.run, "run", "", "path to the run folder (required)")
	showReviewCmd.Flags().StringVar(&showReviewFlags.finding, "finding", "", "subject is a finding with this id")
	showReviewCmd.Flags().StringVar(&showReviewFlags.group, "group", "", "subject is a group with this id")
	showReviewCmd.Flags().BoolVar(&showReviewFlags.all, "all", false, "print every review entry for the subject (default: derived current state)")
	_ = showReviewCmd.MarkFlagRequired("run")
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
	hasFinding := addReviewFlags.finding != ""
	hasGroup := addReviewFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		problems = append(problems, "exactly one of --finding or --group, not both")
	case !hasFinding && !hasGroup:
		problems = append(problems, "exactly one of --finding or --group is required")
	}

	if len(addReviewFlags.labels) == 0 && strings.TrimSpace(addReviewFlags.comment) == "" {
		problems = append(problems, "at least one --label or a --comment is required")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, hasFinding, addReviewFlags.finding, hasGroup, addReviewFlags.group)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	slug := os.Getenv(fettleAgentEnv)
	if slug == "" {
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
	review := schema.Review{
		Subject:  subject,
		Author:   authorStamp,
		Labels:   append([]string{}, addReviewFlags.labels...),
		Severity: severity,
		Comment:  strings.TrimSpace(addReviewFlags.comment),
		At:       time.Now().UTC(),
	}
	if err := rp.AppendReview(slug, review); err != nil {
		return internalError(fmt.Errorf("append review: %w", err))
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
	hasFinding := showReviewFlags.finding != ""
	hasGroup := showReviewFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		return validationError([]string{"exactly one of --finding or --group, not both"})
	case !hasFinding && !hasGroup:
		return validationError([]string{"exactly one of --finding or --group is required"})
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, hasFinding, showReviewFlags.finding, hasGroup, showReviewFlags.group)
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

// reviewEntry is one review record annotated with its author. The
// on-disk schema.Review carries author via the filename
// (reviews_<author>.jsonl); this struct flattens that for CLI
// output.
type reviewEntry struct {
	Subject schema.Subject `json:"subject"`
	Author  string         `json:"author"`
	Labels  []string       `json:"labels"`
	Comment string         `json:"comment,omitempty"`
	At      time.Time      `json:"at"`
}

// reviewCurrent is the "current state" view of a subject's reviews:
// the derived label union plus the latest entry per author. Matches
// the per-subject shape of REVIEWS_JSON in dedupe / group prompts.
type reviewCurrent struct {
	CurrentLabels []string             `json:"current_labels"`
	Entries       []reviewCurrentEntry `json:"entries"`
}

type reviewCurrentEntry struct {
	Author  string    `json:"author"`
	Labels  []string  `json:"labels"`
	Comment string    `json:"comment,omitempty"`
	At      time.Time `json:"at"`
}

// loadAllReviewEntries reads every reviews_<author>.jsonl in runDir
// and returns a flat chronological list. Tolerates malformed lines
// (skipped, like other JSONL readers in the harness).
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
			Subject: fr.Subject,
			Author:  fr.Author,
			Labels:  fr.Labels,
			Comment: fr.Comment,
			At:      fr.At,
		}
	}
	return out, nil
}

// deriveReviewState collapses chronological entries for one subject
// into the "current state" view: latest entry per author, plus a
// label union. Authors are emitted in alphabetical order so output
// is stable.
func deriveReviewState(entries []reviewEntry) reviewCurrent {
	latest := map[string]reviewEntry{}
	for _, e := range entries {
		if existing, ok := latest[e.Author]; !ok || e.At.After(existing.At) {
			latest[e.Author] = e
		}
	}
	authors := make([]string, 0, len(latest))
	for a := range latest {
		authors = append(authors, a)
	}
	sort.Strings(authors)

	out := reviewCurrent{Entries: make([]reviewCurrentEntry, 0, len(authors))}
	labelSet := map[string]bool{}
	for _, a := range authors {
		e := latest[a]
		out.Entries = append(out.Entries, reviewCurrentEntry{
			Author:  a,
			Labels:  e.Labels,
			Comment: e.Comment,
			At:      e.At,
		})
		for _, l := range e.Labels {
			labelSet[l] = true
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
func resolveReviewSubject(rp *run.Path, stage string, hasFinding bool, findingID string, hasGroup bool, groupID string) (schema.Subject, error) {
	switch stage {
	case "find", "merge", "dedupe":
		if !hasFinding {
			return schema.Subject{}, fmt.Errorf("%s runs accept --finding, not --group", stage)
		}
		exists, err := rp.FindingExists(findingID)
		if err != nil {
			return schema.Subject{}, fmt.Errorf("check finding %q: %w", findingID, err)
		}
		if !exists {
			return schema.Subject{}, fmt.Errorf("finding %q not found in this run's findings.jsonl", findingID)
		}
		return schema.Subject{Kind: schema.SubjectFinding, ID: findingID}, nil

	case "group":
		if !hasGroup {
			return schema.Subject{}, fmt.Errorf("group runs accept --group, not --finding")
		}
		exists, err := rp.GroupExists(groupID)
		if err != nil {
			return schema.Subject{}, fmt.Errorf("check group %q: %w", groupID, err)
		}
		if !exists {
			return schema.Subject{}, fmt.Errorf("group %q not found in this run's groups.jsonl", groupID)
		}
		return schema.Subject{Kind: schema.SubjectGroup, ID: groupID}, nil

	default:
		return schema.Subject{}, fmt.Errorf("unsupported run stage %q for review", stage)
	}
}
