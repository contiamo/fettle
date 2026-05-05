package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

var addGroupFlags struct {
	title    string
	summary  string
	findings []string
	labels   []string
	verbose  bool
}

var addGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Append one group to the active run's groups.jsonl",
	Long: `add group records one group in the run identified by $FETTLE_RUN.

Intended to be called by the agent fettle has spawned during a group
stage; the harness sets FETTLE_RUN before invoking the agent. Each
invocation appends one row to groups.jsonl under a cross-process
lock.

The id is generated server-side. Member finding ids are validated
against the group run's input_run findings.jsonl; unknown ids are
rejected.

Exit codes: 0 on success, 1 on validation error, 2 on internal error.`,
	RunE: runAddGroup,
}

var showGroupFlags struct {
	run string
}

var showGroupCmd = &cobra.Command{
	Use:   "group ID",
	Short: "Print one group record as JSON",
	Long: `show group prints a single group from --run by id, as the
{"data": {...}} envelope on stdout.

Exit codes: 0 found, 1 not found / validation, 2 internal error.`,
	Args: cobra.ExactArgs(1),
	RunE: runShowGroup,
}

var listGroupsFlags struct {
	run string
}

var listGroupsCmd = &cobra.Command{
	Use:   "groups",
	Short: "Print all groups in --run as a JSON array",
	Long: `list groups dumps every group in --run as a JSON array on
stdout. Empty runs (or missing groups.jsonl) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runListGroups,
}

func init() {
	addGroupCmd.Flags().StringVar(&addGroupFlags.title, "title", "", "short title for the group (required)")
	addGroupCmd.Flags().StringVar(&addGroupFlags.summary, "summary", "", "one-paragraph summary of the cluster (required)")
	addGroupCmd.Flags().StringArrayVar(&addGroupFlags.findings, "finding", nil, "member finding id from the input run (repeatable; at least one required)")
	addGroupCmd.Flags().StringSliceVar(&addGroupFlags.labels, "label", nil, "label of the form prefix:value, repeatable")
	addGroupCmd.Flags().BoolVar(&addGroupFlags.verbose, "verbose", false, "print the new group's id to stdout on success")
	addCmd.AddCommand(addGroupCmd)

	showGroupCmd.Flags().StringVar(&showGroupFlags.run, "run", "", "path to the group run folder (required)")
	_ = showGroupCmd.MarkFlagRequired("run")
	showCmd.AddCommand(showGroupCmd)

	listGroupsCmd.Flags().StringVar(&listGroupsFlags.run, "run", "", "path to the group run folder (required)")
	_ = listGroupsCmd.MarkFlagRequired("run")
	listCmd.AddCommand(listGroupsCmd)
}

func runAddGroup(cmd *cobra.Command, args []string) error {
	runDir := os.Getenv(fettleRunEnv)
	if runDir == "" {
		return internalError(fmt.Errorf("%s is not set; this command must be invoked by the fettle harness during a group stage", fettleRunEnv))
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
	if manifest.Stage != "group" {
		problems = append(problems, fmt.Sprintf("add group is only valid in group runs (this run's stage is %q)", manifest.Stage))
	}
	if strings.TrimSpace(addGroupFlags.title) == "" {
		problems = append(problems, "--title is required")
	}
	if strings.TrimSpace(addGroupFlags.summary) == "" {
		problems = append(problems, "--summary is required")
	}
	if len(addGroupFlags.findings) == 0 {
		problems = append(problems, "at least one --finding is required")
	}

	memberIDs, memberErrs := validateGroupMembers(rp, manifest, addGroupFlags.findings)
	for _, e := range memberErrs {
		problems = append(problems, e)
	}

	if len(problems) > 0 {
		return validationError(problems)
	}

	labels := addGroupFlags.labels
	if labels == nil {
		labels = []string{}
	}

	createdBy := composeCreatedBy(os.Getenv(fettleAgentEnv), os.Getenv(fettleModelEnv))
	g := schema.Group{
		ID:         schema.NewGroupID(),
		Title:      strings.TrimSpace(addGroupFlags.title),
		Summary:    strings.TrimSpace(addGroupFlags.summary),
		FindingIDs: memberIDs,
		Labels:     labels,
		CreatedBy:  createdBy,
		CreatedAt:  time.Now().UTC(),
	}
	if err := rp.AppendGroup(g); err != nil {
		return internalError(fmt.Errorf("append group: %w", err))
	}
	return printAddResult(map[string]any{"id": g.ID}, addGroupFlags.verbose, g.ID)
}

func runShowGroup(cmd *cobra.Command, args []string) error {
	id := args[0]
	rp, err := openRunForRead(showGroupFlags.run)
	if err != nil {
		return err
	}
	groups, err := rp.LoadGroups()
	if err != nil {
		return internalError(fmt.Errorf("load groups: %w", err))
	}
	for _, g := range groups {
		if g.ID == id {
			if err := printJSON(g); err != nil {
				return internalError(fmt.Errorf("emit group: %w", err))
			}
			return nil
		}
	}
	return validationError([]string{fmt.Sprintf("group %q not found in %s", id, showGroupFlags.run)})
}

func runListGroups(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(listGroupsFlags.run)
	if err != nil {
		return err
	}
	groups, err := rp.LoadGroups()
	if err != nil {
		return internalError(fmt.Errorf("load groups: %w", err))
	}
	if groups == nil {
		groups = []schema.Group{}
	}
	if err := printJSON(groups); err != nil {
		return internalError(fmt.Errorf("emit groups: %w", err))
	}
	return nil
}

// validateGroupMembers checks each --finding id against the group
// run's input_run findings.jsonl. Returns the trimmed/de-duplicated
// id list (preserving order of first occurrence) and any errors.
func validateGroupMembers(rp *run.Path, manifest schema.RunManifest, raws []string) ([]string, []string) {
	if manifest.Stage != "group" || len(raws) == 0 {
		return nil, nil // upstream stage check covers the wrong-stage case
	}
	if manifest.InputRun == "" {
		return nil, []string{"group run has no input_run set in run.json (corrupted manifest)"}
	}

	runDir := rp.Dir()
	projectDir := filepath.Dir(filepath.Dir(runDir))
	inputAbs := filepath.Join(projectDir, manifest.InputRun)
	inputRP, err := run.Open(inputAbs)
	if err != nil {
		return nil, []string{fmt.Sprintf("open input run %s: %v", manifest.InputRun, err)}
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(raws))
	var errs []string
	for _, raw := range raws {
		id := strings.TrimSpace(raw)
		if id == "" {
			errs = append(errs, "--finding must not be empty")
			continue
		}
		if seen[id] {
			continue // tolerate duplicate --finding flags silently
		}
		exists, err := inputRP.FindingExists(id)
		if err != nil {
			errs = append(errs, fmt.Sprintf("--finding %q: lookup error: %v", id, err))
			continue
		}
		if !exists {
			errs = append(errs, fmt.Sprintf("--finding %q: not found in input_run %s findings.jsonl", id, manifest.InputRun))
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, errs
}

