package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

// groupCmd is the parent of `fettle group <verb>` record subcommands.
// Distinct from `fettle run group`, which runs the group stage.
var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Operate on groups (add)",
}

var groupAddFlags struct {
	title    string
	summary  string
	findings []string
	labels   []string
	verbose  bool
}

var groupAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Append one group to the active run's groups.jsonl",
	Long: `add records one group in the run identified by $FETTLE_RUN.

Intended to be called by the agent fettle has spawned during a group
stage; the harness sets FETTLE_RUN before invoking the agent. Each
invocation appends one row to groups.jsonl under a cross-process
lock.

The id is generated server-side. Member finding ids are validated
against the group run's input_run findings.jsonl; unknown ids are
rejected.

Exit codes: 0 on success, 1 on validation error, 2 on internal error.`,
	RunE: runGroupAdd,
}

func init() {
	groupAddCmd.Flags().StringVar(&groupAddFlags.title, "title", "", "short title for the group (required)")
	groupAddCmd.Flags().StringVar(&groupAddFlags.summary, "summary", "", "one-paragraph summary of the cluster (required)")
	groupAddCmd.Flags().StringArrayVar(&groupAddFlags.findings, "finding", nil, "member finding id from the input run (repeatable; at least one required)")
	groupAddCmd.Flags().StringSliceVar(&groupAddFlags.labels, "label", nil, "label of the form prefix:value, repeatable")
	groupAddCmd.Flags().BoolVar(&groupAddFlags.verbose, "verbose", false, "print the new group's id to stdout on success")

	groupCmd.AddCommand(groupAddCmd)

	groupShowCmd.Flags().StringVar(&groupShowFlags.run, "run", "", "path to the group run folder (required)")
	_ = groupShowCmd.MarkFlagRequired("run")
	groupCmd.AddCommand(groupShowCmd)

	groupListCmd.Flags().StringVar(&groupListFlags.run, "run", "", "path to the group run folder (required)")
	_ = groupListCmd.MarkFlagRequired("run")
	groupCmd.AddCommand(groupListCmd)

	rootCmd.AddCommand(groupCmd)
}

var groupShowFlags struct {
	run string
}

var groupShowCmd = &cobra.Command{
	Use:   "show ID",
	Short: "Print one group record as JSON",
	Long: `show prints a single group from --run by id, as pretty JSON
on stdout.

Exit codes: 0 found, 1 not found / validation, 2 internal error.`,
	Args: cobra.ExactArgs(1),
	RunE: runGroupShow,
}

var groupListFlags struct {
	run string
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print all groups in --run as a JSON array",
	Long: `list dumps every group in --run as a JSON array on stdout.
Empty runs (or missing groups.jsonl) print [].

Exit codes: 0 success, 2 internal error.`,
	RunE: runGroupList,
}

func runGroupShow(cmd *cobra.Command, args []string) error {
	id := args[0]
	rp, err := openRunForRead(groupShowFlags.run)
	if err != nil {
		return err
	}
	groups, err := loadGroupsFromRun(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("load groups: %w", err))
	}
	for _, g := range groups {
		if g.ID == id {
			data, err := json.MarshalIndent(g, "", "  ")
			if err != nil {
				return internalError(fmt.Errorf("marshal group: %w", err))
			}
			fmt.Println(string(data))
			return nil
		}
	}
	return validationError([]string{fmt.Sprintf("group %q not found in %s", id, groupShowFlags.run)})
}

func runGroupList(cmd *cobra.Command, args []string) error {
	rp, err := openRunForRead(groupListFlags.run)
	if err != nil {
		return err
	}
	groups, err := loadGroupsFromRun(rp.Dir())
	if err != nil {
		return internalError(fmt.Errorf("load groups: %w", err))
	}
	if groups == nil {
		groups = []schema.Group{}
	}
	data, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return internalError(fmt.Errorf("marshal groups: %w", err))
	}
	fmt.Println(string(data))
	return nil
}

// loadGroupsFromRun reads groups.jsonl. Tolerates malformed lines
// (skipped, like other JSONL readers in the harness). Missing file
// returns nil/empty without error.
func loadGroupsFromRun(runDir string) ([]schema.Group, error) {
	f, err := os.Open(filepath.Join(runDir, "groups.jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var out []schema.Group
	for sc.Scan() {
		var g schema.Group
		if err := json.Unmarshal(sc.Bytes(), &g); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, sc.Err()
}

func runGroupAdd(cmd *cobra.Command, args []string) error {
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
		problems = append(problems, fmt.Sprintf("group add is only valid in group runs (this run's stage is %q)", manifest.Stage))
	}
	if strings.TrimSpace(groupAddFlags.title) == "" {
		problems = append(problems, "--title is required")
	}
	if strings.TrimSpace(groupAddFlags.summary) == "" {
		problems = append(problems, "--summary is required")
	}
	if len(groupAddFlags.findings) == 0 {
		problems = append(problems, "at least one --finding is required")
	}

	memberIDs, memberErrs := validateGroupMembers(rp, manifest, groupAddFlags.findings)
	for _, e := range memberErrs {
		problems = append(problems, e)
	}

	if len(problems) > 0 {
		return validationError(problems)
	}

	labels := groupAddFlags.labels
	if labels == nil {
		labels = []string{}
	}

	createdBy := composeCreatedBy(os.Getenv(fettleAgentEnv), os.Getenv(fettleModelEnv))
	g := schema.Group{
		ID:         schema.NewGroupID(),
		Title:      strings.TrimSpace(groupAddFlags.title),
		Summary:    strings.TrimSpace(groupAddFlags.summary),
		FindingIDs: memberIDs,
		Labels:     labels,
		CreatedBy:  createdBy,
		CreatedAt:  time.Now().UTC(),
	}
	if err := rp.AppendGroup(g); err != nil {
		return internalError(fmt.Errorf("append group: %w", err))
	}
	if groupAddFlags.verbose {
		fmt.Println(g.ID)
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
