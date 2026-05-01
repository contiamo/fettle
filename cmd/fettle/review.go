package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

// reviewCmd is the parent of `fettle review <verb>` record subcommands.
// `fettle run review` (the stage runner) lives separately under `runCmd`.
var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Operate on review entries (add)",
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
	rootCmd.AddCommand(reviewCmd)
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
