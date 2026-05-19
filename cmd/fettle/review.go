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
	add      []string
	remove   []string
	severity string
	comment  string
	verbose  bool
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
	addReviewCmd.Flags().StringSliceVar(&addReviewFlags.add, "add-label", nil, "label to add (prefix:value or any string), repeatable")
	addReviewCmd.Flags().StringSliceVar(&addReviewFlags.remove, "remove-label", nil, "label to remove, repeatable")
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

	if len(addReviewFlags.add) == 0 && len(addReviewFlags.remove) == 0 &&
		strings.TrimSpace(addReviewFlags.comment) == "" &&
		strings.TrimSpace(addReviewFlags.severity) == "" {
		problems = append(problems, "at least one of --add-label, --remove-label, --severity, --comment is required")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, addReviewFlags.finding)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	authorStamp, err := stamp()
	if err != nil {
		return validationError([]string{err.Error()})
	}

	var severity *string
	if s := strings.TrimSpace(addReviewFlags.severity); s != "" {
		severity = &s
	}
	add := addReviewFlags.add
	if add == nil {
		add = []string{}
	}
	remove := addReviewFlags.remove
	if remove == nil {
		remove = []string{}
	}
	entry := schema.ReviewEntry{
		Kind:     schema.SubjectFinding,
		ID:       subject.ID,
		Author:   authorStamp,
		At:       time.Now().UTC(),
		Add:      add,
		Remove:   remove,
		Severity: severity,
		Comment:  strings.TrimSpace(addReviewFlags.comment),
	}
	// Validate up front so a payload error exits with the validation
	// exit code (1) rather than internal-error (2).
	if err := schema.ValidateReviewEntry(entry); err != nil {
		return validationError([]string{err.Error()})
	}
	if err := rp.AppendReviewEntry(entry); err != nil {
		return internalError(fmt.Errorf("save review: %w", err))
	}
	if err := rp.Close(); err != nil {
		return internalError(fmt.Errorf("close run: %w", err))
	}
	return printAddResult(map[string]any{"subject": subject, "at": entry.At, "author": entry.Author}, addReviewFlags.verbose, subject.ID)
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
	// Load the seed (LLM-emitted) finding so the resolver can compute
	// the effective labels / severity against the right starting point.
	entries, err := rp.LoadFindingEntries()
	if err != nil {
		return internalError(fmt.Errorf("load findings for seed: %w", err))
	}
	var seedLabels []string
	var seedSeverity *string
	for _, f := range entries {
		if f.ID == subject.ID {
			seedLabels = f.Labels
			seedSeverity = f.Severity
			break
		}
	}
	if err := printJSON(deriveReviewState(seedLabels, seedSeverity, matching)); err != nil {
		return internalError(fmt.Errorf("emit review state: %w", err))
	}
	return nil
}

// reviewEntry is the wire shape for `fettle list reviews` /
// `fettle show review` flat output. Mirrors schema.ReviewEntry but
// carries a synthesized Subject so the CLI's existing JSON contract
// (subject + payload) survives the move from per-finding-doc storage
// to JSONL streams.
type reviewEntry struct {
	Subject  schema.Subject `json:"subject"`
	Author   string         `json:"author"`
	Add      []string       `json:"add"`
	Remove   []string       `json:"remove"`
	Severity *string        `json:"severity,omitempty"`
	Comment  string         `json:"comment,omitempty"`
	At       time.Time      `json:"at"`
}

// reviewCurrent is the "current state" view of a subject's reviews:
// the resolver-driven label set plus a chronological history of
// changes. Matches the per-subject shape of REVIEWS_JSON in dedupe
// / group prompts.
type reviewCurrent struct {
	CurrentLabels    []string             `json:"current_labels"`
	CurrentSeverity  *string              `json:"current_severity,omitempty"`
	Entries          []reviewCurrentEntry `json:"entries"`
}

// reviewCurrentEntry is one row in the derived history. Each entry
// reflects exactly what was written — its own Add/Remove arrays plus
// any severity / comment — so a consumer can replay the timeline
// without re-deriving anything.
type reviewCurrentEntry struct {
	Author   string    `json:"author"`
	Add      []string  `json:"add"`
	Remove   []string  `json:"remove"`
	Severity *string   `json:"severity,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at"`
}

// loadAllReviewEntries walks every reviews_*.jsonl in runDir and
// returns the entries chronologically. The CLI's flat output shape
// stays the same as before the redesign — only the on-disk layout
// changed.
func loadAllReviewEntries(runDir string) ([]reviewEntry, error) {
	rp, err := run.Open(runDir)
	if err != nil {
		return nil, err
	}
	all, err := rp.LoadReviewEntries()
	if err != nil {
		return nil, err
	}
	out := make([]reviewEntry, len(all))
	for i, e := range all {
		out[i] = reviewEntry{
			Subject:  schema.Subject{Kind: e.Kind, ID: e.ID},
			Author:   e.Author,
			Add:      e.Add,
			Remove:   e.Remove,
			Severity: e.Severity,
			Comment:  e.Comment,
			At:       e.At,
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// deriveReviewState collapses chronological entries for one subject
// into the resolver's view of "current": the effective label set
// after replaying every add/remove in order, plus the effective
// severity (latest non-nil), plus the per-entry history that
// produced them.
func deriveReviewState(seedLabels []string, seedSeverity *string, entries []reviewEntry) reviewCurrent {
	// Convert to schema.ReviewEntry for the resolver. The CLI's
	// reviewEntry carries an extra Subject field but is otherwise
	// equivalent; the conversion is cheap.
	reviews := make([]schema.ReviewEntry, len(entries))
	for i, e := range entries {
		reviews[i] = schema.ReviewEntry{
			Kind:     e.Subject.Kind,
			ID:       e.Subject.ID,
			Author:   e.Author,
			At:       e.At,
			Add:      e.Add,
			Remove:   e.Remove,
			Severity: e.Severity,
			Comment:  e.Comment,
		}
	}
	currentLabels := schema.ResolveLabels(seedLabels, reviews)
	currentSeverity := schema.ResolveSeverity(seedSeverity, reviews)

	history := make([]reviewCurrentEntry, len(entries))
	for i, e := range entries {
		history[i] = reviewCurrentEntry{
			Author:   e.Author,
			Add:      e.Add,
			Remove:   e.Remove,
			Severity: e.Severity,
			Comment:  e.Comment,
			At:       e.At,
		}
	}
	return reviewCurrent{
		CurrentLabels:   currentLabels,
		CurrentSeverity: currentSeverity,
		Entries:         history,
	}
}

// resolveReviewSubject enforces the stage→subject-kind rules and
// confirms the referenced id exists in any findings_*.jsonl file in
// the run.
func resolveReviewSubject(rp *run.Path, stage string, findingID string) (schema.Subject, error) {
	if stage != "find" {
		return schema.Subject{}, fmt.Errorf("unsupported run stage %q for review", stage)
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
