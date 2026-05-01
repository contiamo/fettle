package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/contiamo/fettle/internal/agent"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

const defaultGroupTimeout = 15 * time.Minute

//go:embed prompts/group.md
var groupPromptFrame string

var groupPromptTmpl = template.Must(template.New("group").Parse(groupPromptFrame))

type groupPromptVars struct {
	RepoRoot         string
	InputRun         string
	FindingCount     int
	FindingsJSON     string
	ReviewsJSON      string
	UserInstructions string
}

var runGroupFlags struct {
	run     string
	name    string
	agent   string
	model   string
	script  string
	effort  string
	timeout time.Duration
}

var runGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Cluster a run's findings into PR-sized batches via an LLM agent",
	Long: `group takes a single completed find/merge/dedupe run as input
and asks the configured agent to cluster its findings into PR-sized
groups. The result lands in a new group run folder under runs/.

Reviews from the input run are surfaced to the agent as REVIEWS_JSON
so it can skip findings already labeled false-positive / out-of-scope
/ needs-human. If the input run has no reviews, the agent gets ` + "`{}`" + `
and clusters on findings alone.

Single-shot agent invocation; if it crashes mid-output, delete the
run folder and re-run.`,
	RunE: runRunGroup,
}

func init() {
	runGroupCmd.Flags().StringVar(&runGroupFlags.run, "run", "", "input run folder (required; one of find/merge/dedupe)")
	runGroupCmd.Flags().StringVar(&runGroupFlags.name, "name", "", "human label appended to the run folder timestamp")
	runGroupCmd.Flags().StringVar(&runGroupFlags.agent, "agent", "", "select a built-in agent (claude or codex); mutually exclusive with --agent-script")
	runGroupCmd.Flags().StringVar(&runGroupFlags.model, "model", "", "agent model override")
	runGroupCmd.Flags().StringVar(&runGroupFlags.script, "agent-script", "", "run a custom agent script (path to executable)")
	runGroupCmd.Flags().StringVar(&runGroupFlags.effort, "effort", "", "agent reasoning effort: low|medium|high|xhigh|max")
	runGroupCmd.Flags().DurationVar(&runGroupFlags.timeout, "timeout", defaultGroupTimeout, "agent timeout (single invocation processes all input findings)")
	_ = runGroupCmd.MarkFlagRequired("run")
	runCmd.AddCommand(runGroupCmd)
}

func runRunGroup(cmd *cobra.Command, args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir, err := projectDir()
	if err != nil {
		return err
	}

	input, relInput, targetRepo, err := resolveGroupInput(dir)
	if err != nil {
		return err
	}

	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	spec, sourcePath, err := buildGroupSpec(cfg, targetRepo)
	if err != nil {
		return err
	}

	promptBody, err := readGroupPrompt(dir, cfg)
	if err != nil {
		return err
	}

	out, err := run.CreateForGroup(run.CreateGroupOpts{
		ProjectDir:      dir,
		Slug:            runGroupFlags.name,
		InputRun:        relInput,
		GroupPrompt:     promptBody,
		GroupSourcePath: sourcePath,
		GroupSpec:       spec,
	})
	if err != nil {
		return fmt.Errorf("create group run: %w", err)
	}

	findings, err := loadFindingsFromRun(input.abs)
	if err != nil {
		return fmt.Errorf("read findings from %s: %w", relInput, err)
	}
	reviews, err := loadReviewsByFinding(input.abs)
	if err != nil {
		return fmt.Errorf("read reviews from %s: %w", relInput, err)
	}
	reviewsJSON, err := buildReviewsJSON(reviews)
	if err != nil {
		return fmt.Errorf("marshal reviews: %w", err)
	}
	findingsJSON, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal findings: %w", err)
	}

	prompt, err := renderGroupPrompt(groupPromptVars{
		RepoRoot:         targetRepo,
		InputRun:         relInput,
		FindingCount:     len(findings),
		FindingsJSON:     string(findingsJSON),
		ReviewsJSON:      string(reviewsJSON),
		UserInstructions: promptBody,
	})
	if err != nil {
		return fmt.Errorf("render group prompt: %w", err)
	}

	logPath := filepath.Join(out.RawDir(), "prompt.txt")
	_ = os.WriteFile(logPath, []byte(prompt), 0o644)

	stageSpec := spec
	stageSpec.AddDirs = []string{out.Dir()}
	stageSpec.Env = []string{
		"FETTLE_RUN=" + out.Dir(),
		"FETTLE_AGENT=" + spec.Name,
	}
	if spec.Model != "" {
		stageSpec.Env = append(stageSpec.Env, "FETTLE_MODEL="+spec.Model)
	}
	if spec.Effort != "" {
		stageSpec.Env = append(stageSpec.Env, "FETTLE_EFFORT="+spec.Effort)
	}

	logger.Info("plan",
		"out", out.Dir(),
		"input", relInput,
		"findings", len(findings),
		"reviews", len(reviews),
		"agent", spec.Name,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startAll := time.Now()
	res, agentErr := agent.Run(ctx, stageSpec, prompt)
	if res != nil {
		_ = os.WriteFile(filepath.Join(out.RawDir(), "agent.log"), res.Output, 0o644)
	}
	if agentErr != nil {
		logger.Error("agent failed", "error", agentErr, "elapsed", time.Since(startAll).Round(time.Second))
		fmt.Println(out.Dir())
		return agentErr
	}

	groupCount, err := countLines(filepath.Join(out.Dir(), "groups.jsonl"))
	if err != nil {
		return fmt.Errorf("count groups: %w", err)
	}

	if err := out.MarkCompleted(); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	logger.Info("complete",
		"out", out.Dir(),
		"groups", groupCount,
		"input_findings", len(findings),
		"elapsed", time.Since(startAll).Round(time.Second),
	)
	fmt.Println(out.Dir())
	return nil
}

// resolveGroupInput validates the single --run flag, ensures it's a
// completed find/merge/dedupe run, and resolves a target_repo for
// REPO_ROOT in the prompt by walking back through input_run/input_runs[]
// for non-find inputs.
func resolveGroupInput(projectDir string) (inputRun, string, string, error) {
	if runGroupFlags.run == "" {
		return inputRun{}, "", "", fmt.Errorf("--run is required")
	}
	abs := runGroupFlags.run
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, runGroupFlags.run)
	}
	rp, err := run.Open(abs)
	if err != nil {
		return inputRun{}, "", "", fmt.Errorf("open %s: %w", runGroupFlags.run, err)
	}
	m, err := rp.Manifest()
	if err != nil {
		return inputRun{}, "", "", fmt.Errorf("read %s manifest: %w", runGroupFlags.run, err)
	}
	switch m.Stage {
	case "find", "merge", "dedupe":
	case "group":
		return inputRun{}, "", "", fmt.Errorf("input run %s is itself a group run; group takes a find/merge/dedupe run", runGroupFlags.run)
	default:
		return inputRun{}, "", "", fmt.Errorf("input run %s has unsupported stage %q", runGroupFlags.run, m.Stage)
	}
	if m.CompletedAt == nil {
		return inputRun{}, "", "", fmt.Errorf("input run %s is not completed (run.json missing completed_at)", runGroupFlags.run)
	}

	rel, err := filepath.Rel(projectDir, abs)
	if err != nil {
		rel = abs
	}
	rel = filepath.ToSlash(rel)
	in := inputRun{abs: abs, rel: rel, manifest: m}

	targetRepo, err := resolveRepoRoot(projectDir, m)
	if err != nil {
		return inputRun{}, "", "", err
	}
	return in, rel, targetRepo, nil
}

// resolveRepoRoot returns the target_repo for a manifest. find runs
// store it directly; merge/dedupe walk back to the first input. One
// level of recursion covers nested merge/dedupe inputs in practice
// (dedupe rejects mismatched target_repos at its own creation time).
func resolveRepoRoot(projectDir string, m schema.RunManifest) (string, error) {
	if m.TargetRepo != "" {
		return m.TargetRepo, nil
	}
	candidates := m.InputRuns
	if m.InputRun != "" {
		candidates = append([]string{m.InputRun}, candidates...)
	}
	for _, ir := range candidates {
		abs := ir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, ir)
		}
		rp, err := run.Open(abs)
		if err != nil {
			continue
		}
		mm, err := rp.Manifest()
		if err != nil {
			continue
		}
		if mm.TargetRepo != "" {
			return mm.TargetRepo, nil
		}
		if rr, err := resolveRepoRoot(projectDir, mm); err == nil && rr != "" {
			return rr, nil
		}
	}
	return "", fmt.Errorf("could not resolve target_repo from input run chain")
}

func buildGroupSpec(cfg project.Config, targetRepo string) (agent.Spec, string, error) {
	if runGroupFlags.agent != "" && runGroupFlags.script != "" {
		return agent.Spec{}, "", fmt.Errorf("--agent and --agent-script are mutually exclusive")
	}
	if runGroupFlags.agent != "" {
		switch runGroupFlags.agent {
		case "claude", "codex":
		default:
			return agent.Spec{}, "", fmt.Errorf("--agent must be claude or codex; got %q", runGroupFlags.agent)
		}
	}

	agentName := cfg.Agent.Name
	agentModel := cfg.Agent.Model
	agentScript := cfg.Agent.Script

	if runGroupFlags.agent != "" {
		agentName = runGroupFlags.agent
		agentScript = ""
	}
	if runGroupFlags.script != "" {
		agentScript = runGroupFlags.script
		if cfg.Agent.Script == "" {
			agentName = "custom"
		}
	}
	if runGroupFlags.model != "" {
		agentModel = runGroupFlags.model
	}
	if agentScript != "" && !filepath.IsAbs(agentScript) {
		abs, err := filepath.Abs(agentScript)
		if err != nil {
			return agent.Spec{}, "", fmt.Errorf("resolve agent.script %q: %w", agentScript, err)
		}
		agentScript = abs
	}
	if agentScript != "" {
		if _, err := exec.LookPath(agentScript); err != nil {
			return agent.Spec{}, "", fmt.Errorf("agent script %q not executable: %w", agentScript, err)
		}
	}

	spec := agent.Spec{
		Name:    agentName,
		Model:   agentModel,
		Effort:  runGroupFlags.effort,
		WorkDir: targetRepo,
		Timeout: runGroupFlags.timeout,
		Script:  agentScript,
	}
	return spec, cfg.Instructions.Group, nil
}

func readGroupPrompt(projectDir string, cfg project.Config) (string, error) {
	srcPath := cfg.Instructions.Group
	if srcPath == "" {
		return "", fmt.Errorf("instructions.group is empty in .fettle.json")
	}
	if !filepath.IsAbs(srcPath) {
		srcPath = filepath.Join(projectDir, srcPath)
	}
	body, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read group prompt %q: %w", srcPath, err)
	}
	return string(body), nil
}

// reviewsJSONEntry is the per-finding shape inside REVIEWS_JSON: a
// derived current_labels (latest entry per author, unioned across
// authors) plus the chronological per-author entries.
type reviewsJSONEntry struct {
	CurrentLabels []string            `json:"current_labels"`
	Entries       []reviewsJSONAuthor `json:"entries"`
}

type reviewsJSONAuthor struct {
	Author  string   `json:"author"`
	Labels  []string `json:"labels"`
	Comment string   `json:"comment,omitempty"`
	At      string   `json:"at"`
}

// buildReviewsJSON converts the (finding_id → author → reviewState)
// map produced by loadReviewsByFinding (in run_dedupe.go) into the
// shape FETTLE.md's REVIEWS_JSON section describes. Marshalled with
// stable key order so prompt diffs are deterministic.
func buildReviewsJSON(byFinding map[string]map[string]reviewState) ([]byte, error) {
	if len(byFinding) == 0 {
		return []byte("{}"), nil
	}
	out := make(map[string]reviewsJSONEntry, len(byFinding))
	for fid, byAuthor := range byFinding {
		authors := make([]string, 0, len(byAuthor))
		for a := range byAuthor {
			authors = append(authors, a)
		}
		sort.Strings(authors)

		entries := make([]reviewsJSONAuthor, 0, len(authors))
		labelSet := map[string]bool{}
		for _, a := range authors {
			rs := byAuthor[a]
			entries = append(entries, reviewsJSONAuthor{
				Author:  a,
				Labels:  rs.Labels,
				Comment: rs.Comment,
				At:      rs.At,
			})
			for _, l := range rs.Labels {
				labelSet[l] = true
			}
		}
		current := make([]string, 0, len(labelSet))
		for l := range labelSet {
			current = append(current, l)
		}
		sort.Strings(current)
		out[fid] = reviewsJSONEntry{CurrentLabels: current, Entries: entries}
	}
	// Stable id ordering keeps the snapshotted prompt diffable.
	ids := make([]string, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("{\n")
	for i, id := range ids {
		entry := out[id]
		entryJSON, err := json.MarshalIndent(entry, "  ", "  ")
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  %q: %s", id, entryJSON)
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

func renderGroupPrompt(vars groupPromptVars) (string, error) {
	var b strings.Builder
	if err := groupPromptTmpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
