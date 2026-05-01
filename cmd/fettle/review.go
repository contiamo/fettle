package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

// reviewCmd is the parent of `fettle review <verb>` record subcommands.
// `fettle run review` (the stage runner) lives separately under `runCmd`.
var reviewCmd = &cobra.Command{
	Use:     "review",
	Short:   "Operate on review entries (add, list, show)",
	GroupID: groupRecords,
}

var reviewAddFlags struct {
	finding string
	group   string
	labels  []string
	comment string
	verbose bool
}

var reviewAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Append a review entry to the active run's reviews_<author>.jsonl",
	Long: `add records one review entry in the run identified by $FETTLE_RUN.

Author identity (the slug used in the filename) comes from
$FETTLE_AGENT. The harness sets that env var when invoking an agent
during a review stage; humans calling `+"`fettle review add`"+` directly need
to set it themselves (or use the UI, which manages identity).

Subject is exactly one of --finding ID or --group ID. The harness
validates that the id exists in the run's findings.jsonl (find/dedupe
runs) or groups.jsonl (group runs) and rejects unknown ids and
mismatched subject kinds.

Exit codes: 0 success, 1 validation error, 2 internal error.`,
	RunE: runReviewAdd,
}

func init() {
	reviewAddCmd.Flags().StringVar(&reviewAddFlags.finding, "finding", "", "review subject is a finding with this id")
	reviewAddCmd.Flags().StringVar(&reviewAddFlags.group, "group", "", "review subject is a group with this id")
	reviewAddCmd.Flags().StringSliceVar(&reviewAddFlags.labels, "label", nil, "label of the form prefix:value (or any string), repeatable")
	reviewAddCmd.Flags().StringVar(&reviewAddFlags.comment, "comment", "", "free-form comment")
	reviewAddCmd.Flags().BoolVar(&reviewAddFlags.verbose, "verbose", false, "print the appended subject id on success")

	reviewCmd.AddCommand(reviewAddCmd)

	reviewListCmd.Flags().StringVar(&reviewListFlags.run, "run", "", "path to the run folder (required)")
	_ = reviewListCmd.MarkFlagRequired("run")
	reviewCmd.AddCommand(reviewListCmd)

	reviewShowCmd.Flags().StringVar(&reviewShowFlags.run, "run", "", "path to the run folder (required)")
	reviewShowCmd.Flags().StringVar(&reviewShowFlags.finding, "finding", "", "subject is a finding with this id")
	reviewShowCmd.Flags().StringVar(&reviewShowFlags.group, "group", "", "subject is a group with this id")
	reviewShowCmd.Flags().BoolVar(&reviewShowFlags.all, "all", false, "print every review entry for the subject (default: derived current state)")
	_ = reviewShowCmd.MarkFlagRequired("run")
	reviewCmd.AddCommand(reviewShowCmd)

	rootCmd.AddCommand(reviewCmd)
}

var reviewListFlags struct {
	run string
}

var reviewListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print all review entries in --run as a JSON array",
	Long: `list dumps every review entry from every reviews_<author>.jsonl
in --run as a flat, chronologically-sorted JSON array. Each entry
includes the author derived from its filename.

Empty runs (or runs with no reviews_*.jsonl files) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runReviewList,
}

var reviewShowFlags struct {
	run     string
	finding string
	group   string
	all     bool
}

var reviewShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print review state for one finding or group",
	Long: `show prints review state for a single subject. Default emits the
derived current state — for each author, the latest entry's full
label set, plus a current_labels union across authors. With --all,
emits every entry chronologically (including superseded entries).

Exits non-zero if no review entries exist for the subject.

Exit codes: 0 success, 1 validation / not-found, 2 internal error.`,
	RunE: runReviewShow,
}

func runReviewAdd(cmd *cobra.Command, args []string) error {
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
	hasFinding := reviewAddFlags.finding != ""
	hasGroup := reviewAddFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		problems = append(problems, "exactly one of --finding or --group, not both")
	case !hasFinding && !hasGroup:
		problems = append(problems, "exactly one of --finding or --group is required")
	}

	if len(reviewAddFlags.labels) == 0 && strings.TrimSpace(reviewAddFlags.comment) == "" {
		problems = append(problems, "at least one --label or a --comment is required")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, hasFinding, reviewAddFlags.finding, hasGroup, reviewAddFlags.group)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	author := os.Getenv(fettleAgentEnv)
	if author == "" {
		return internalError(fmt.Errorf("%s is not set; cannot derive reviewer slug", fettleAgentEnv))
	}

	review := schema.Review{
		Subject: subject,
		Labels:  append([]string{}, reviewAddFlags.labels...),
		Comment: strings.TrimSpace(reviewAddFlags.comment),
		At:      time.Now().UTC(),
	}
	if err := rp.AppendReview(author, review); err != nil {
		return internalError(fmt.Errorf("append review: %w", err))
	}
	return printAddResult(map[string]any{"subject": subject, "at": review.At}, reviewAddFlags.verbose, subject.ID)
}

func runReviewList(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(reviewListFlags.run)
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

func runReviewShow(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunForClose(reviewShowFlags.run) // same flag-+-manifest pattern as close
	if err != nil {
		return err
	}
	hasFinding := reviewShowFlags.finding != ""
	hasGroup := reviewShowFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		return validationError([]string{"exactly one of --finding or --group, not both"})
	case !hasFinding && !hasGroup:
		return validationError([]string{"exactly one of --finding or --group is required"})
	}

	subject, kindErr := resolveReviewSubject(rp, manifest.Stage, hasFinding, reviewShowFlags.finding, hasGroup, reviewShowFlags.group)
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
		return validationError([]string{fmt.Sprintf("no review entries for %s %q in %s", subject.Kind, subject.ID, reviewShowFlags.run)})
	}

	if reviewShowFlags.all {
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
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, err
	}
	var out []reviewEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "reviews_") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		author := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "reviews_"), ".jsonl")
		f, err := os.Open(filepath.Join(runDir, e.Name()))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			var r schema.Review
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				continue
			}
			out = append(out, reviewEntry{
				Subject: r.Subject,
				Author:  author,
				Labels:  r.Labels,
				Comment: r.Comment,
				At:      r.At,
			})
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
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
	case "find", "dedupe":
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
