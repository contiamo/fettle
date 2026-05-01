package main

import (
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

// closeCmd is the parent of `fettle close <verb>` record subcommands.
// Closures track that a finding or group has been disposed of (PR
// merged, won't fix, etc.) — append-only events, latest wins for
// "current state" display.
var closeCmd = &cobra.Command{
	Use:   "close",
	Short: "Operate on closure events (add, show)",
}

var closeAddFlags struct {
	run     string
	finding string
	group   string
	status  string
	prURL   string
	verbose bool
}

var closeAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Append a closure event to the run's closures.jsonl",
	Long: `add records that a finding or group has been closed (PR merged,
won't fix, deduped, etc.). Append-only — re-marking is allowed; the
latest entry wins for current-state display, but the full history
is preserved.

Author identity (the slug stamped into ` + "`marked_by`" + `) chains:
$FETTLE_AGENT (if running under a stage) → $FETTLE_AUTHOR → the slug
in ~/.config/fettle/identity. Agent stamps come out as
` + "`agent:<name>[/<model>]`" + `; humans as ` + "`human:<slug>`" + `.

Exit codes: 0 success, 1 validation error, 2 internal error.`,
	RunE: runCloseAdd,
}

var closeShowFlags struct {
	run     string
	finding string
	group   string
	all     bool
}

var closeShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the latest (or full) closure history for one subject",
	Long: `show prints closure events for one finding or group as JSONL.

Default: prints the latest event only (current state). With --all,
prints every event in chronological order — including superseded
ones — so you can see who closed what when, and what the state was
before re-marking.

Exits non-zero if no closure events exist for the subject.

Exit codes: 0 success, 1 validation error / not-found, 2 internal error.`,
	RunE: runCloseShow,
}

func init() {
	closeAddCmd.Flags().StringVar(&closeAddFlags.run, "run", "", "path to the target run folder (required)")
	closeAddCmd.Flags().StringVar(&closeAddFlags.finding, "finding", "", "subject is a finding with this id")
	closeAddCmd.Flags().StringVar(&closeAddFlags.group, "group", "", "subject is a group with this id")
	closeAddCmd.Flags().StringVar(&closeAddFlags.status, "status", "", "closure status: merged|closed|wontfix|... (required; project-defined)")
	closeAddCmd.Flags().StringVar(&closeAddFlags.prURL, "pr", "", "URL of the PR or commit that closes the subject (optional)")
	closeAddCmd.Flags().BoolVar(&closeAddFlags.verbose, "verbose", false, "print the closed subject id on success")
	_ = closeAddCmd.MarkFlagRequired("run")

	closeShowCmd.Flags().StringVar(&closeShowFlags.run, "run", "", "path to the target run folder (required)")
	closeShowCmd.Flags().StringVar(&closeShowFlags.finding, "finding", "", "subject is a finding with this id")
	closeShowCmd.Flags().StringVar(&closeShowFlags.group, "group", "", "subject is a group with this id")
	closeShowCmd.Flags().BoolVar(&closeShowFlags.all, "all", false, "print every closure event for the subject (default: latest only)")
	_ = closeShowCmd.MarkFlagRequired("run")

	closeCmd.AddCommand(closeAddCmd)
	closeCmd.AddCommand(closeShowCmd)
	rootCmd.AddCommand(closeCmd)
}

func runCloseAdd(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunForClose(closeAddFlags.run)
	if err != nil {
		return err
	}

	var problems []string
	hasFinding := closeAddFlags.finding != ""
	hasGroup := closeAddFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		problems = append(problems, "exactly one of --finding or --group, not both")
	case !hasFinding && !hasGroup:
		problems = append(problems, "exactly one of --finding or --group is required")
	}
	if strings.TrimSpace(closeAddFlags.status) == "" {
		problems = append(problems, "--status is required (e.g. merged|closed|wontfix)")
	}
	if len(problems) > 0 {
		return validationError(problems)
	}

	subject, kindErr := resolveCloseSubject(rp, manifest.Stage, hasFinding, closeAddFlags.finding, hasGroup, closeAddFlags.group)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	mb, err := markedBy()
	if err != nil {
		return validationError([]string{err.Error()})
	}

	c := schema.Closure{
		Subject:  subject,
		Status:   strings.TrimSpace(closeAddFlags.status),
		PRURL:    strings.TrimSpace(closeAddFlags.prURL),
		At:       time.Now().UTC(),
		MarkedBy: mb,
	}
	if err := rp.AppendClosure(c); err != nil {
		return internalError(fmt.Errorf("append closure: %w", err))
	}
	if closeAddFlags.verbose {
		fmt.Println(subject.ID)
	}
	return nil
}

func runCloseShow(cmd *cobra.Command, args []string) error {
	rp, manifest, err := openRunForClose(closeShowFlags.run)
	if err != nil {
		return err
	}

	hasFinding := closeShowFlags.finding != ""
	hasGroup := closeShowFlags.group != ""
	switch {
	case hasFinding && hasGroup:
		return validationError([]string{"exactly one of --finding or --group, not both"})
	case !hasFinding && !hasGroup:
		return validationError([]string{"exactly one of --finding or --group is required"})
	}

	subject, kindErr := resolveCloseSubject(rp, manifest.Stage, hasFinding, closeShowFlags.finding, hasGroup, closeShowFlags.group)
	if kindErr != nil {
		return validationError([]string{kindErr.Error()})
	}

	closures, err := rp.LoadClosures()
	if err != nil {
		return internalError(fmt.Errorf("load closures: %w", err))
	}
	matching := make([]schema.Closure, 0, len(closures))
	for _, c := range closures {
		if c.Subject == subject {
			matching = append(matching, c)
		}
	}
	if len(matching) == 0 {
		return validationError([]string{fmt.Sprintf("no closure events for %s %q in %s", subject.Kind, subject.ID, closeShowFlags.run)})
	}
	sort.SliceStable(matching, func(i, j int) bool { return matching[i].At.Before(matching[j].At) })

	enc := json.NewEncoder(os.Stdout)
	if closeShowFlags.all {
		for _, c := range matching {
			if err := enc.Encode(c); err != nil {
				return internalError(fmt.Errorf("encode closure: %w", err))
			}
		}
		return nil
	}
	return enc.Encode(matching[len(matching)-1])
}

// openRunForClose resolves the --run flag (relative to the project
// dir or absolute) and reads its manifest. Used by both add and
// show.
func openRunForClose(rawRun string) (*run.Path, schema.RunManifest, error) {
	if rawRun == "" {
		return nil, schema.RunManifest{}, validationError([]string{"--run is required"})
	}
	abs := rawRun
	if !filepath.IsAbs(abs) {
		dir, err := projectDir()
		if err != nil {
			return nil, schema.RunManifest{}, internalError(err)
		}
		abs = filepath.Join(dir, rawRun)
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

// resolveCloseSubject enforces the stage→subject-kind rules and
// confirms the referenced id exists in the run. Mirrors
// resolveReviewSubject in review.go but lives here because closures
// have a slightly broader stage set (any run with subjects).
func resolveCloseSubject(rp *run.Path, stage string, hasFinding bool, findingID string, hasGroup bool, groupID string) (schema.Subject, error) {
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
		return schema.Subject{}, fmt.Errorf("unsupported run stage %q for close", stage)
	}
}
