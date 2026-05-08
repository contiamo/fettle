package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

const defaultDedupeTimeout = 15 * time.Minute

//go:embed prompts/dedupe.md
var dedupePromptFrame string

var dedupePromptTmpl = template.Must(template.New("dedupe").Parse(dedupePromptFrame))

type dedupePromptVars struct {
	RepoRoot         string
	InputRunCount    int
	InputRunsList    string
	FindingsJSON     string
	UserInstructions string
}

var runDedupeFlags struct {
	runs    []string
	name    string
	agent   string
	model   string
	script  string
	effort  string
	prompt  string
	timeout time.Duration
}

var runDedupeCmd = &cobra.Command{
	Use:   "dedupe",
	Short: "Consolidate findings from multiple runs via an LLM agent",
	Long: `dedupe takes two or more input runs, asks the configured agent
to merge equivalent findings into canonical entries, and writes the
result to a new dedupe run folder. Each canonical finding has a
` + "`members[]`" + ` back-pointer (length >= 1) listing the source findings
it subsumes.

Source review state is fed to the agent as input context (so the
agent can skip findings already labeled false-positive / out-of-scope
by reviewers) but is NOT propagated as review entries onto canonical
findings — that would forge authorship under N-to-1 synthesis.
Canonical findings start with no reviews of their own.

Single-shot agent invocation; if it crashes mid-output, delete the
run folder and re-run.`,
	RunE: runRunDedupe,
}

func init() {
	runDedupeCmd.Flags().StringSliceVar(&runDedupeFlags.runs, "run", nil, "input run folder (repeatable; at least one required, two or more typical)")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.name, "name", "", "human label appended to the run folder timestamp")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.agent, "agent", "", "select a built-in agent (claude or codex); mutually exclusive with --agent-script")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.model, "model", "", "agent model override")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.script, "agent-script", "", "run a custom agent script (path to executable)")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.effort, "effort", "", "agent reasoning effort: low|medium|high|xhigh|max")
	runDedupeCmd.Flags().StringVar(&runDedupeFlags.prompt, "prompt", "", "path to the dedupe prompt to use (overrides instructions.dedupe; relative to cwd)")
	runDedupeCmd.Flags().DurationVar(&runDedupeFlags.timeout, "timeout", defaultDedupeTimeout, "agent timeout (single invocation processes all input findings)")
	_ = runDedupeCmd.MarkFlagRequired("run")
	runCmd.AddCommand(runDedupeCmd)
}

func runRunDedupe(cmd *cobra.Command, args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir, err := projectDir()
	if err != nil {
		return err
	}

	// Resolve and validate input runs.
	inputs, relInputs, targetRepo, err := resolveDedupeInputs(dir)
	if err != nil {
		return err
	}

	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	spec, err := buildDedupeSpec(cfg, targetRepo)
	if err != nil {
		return err
	}

	promptAbs, sourcePath, err := resolvePromptSource(dir, runDedupeFlags.prompt, cfg.Instructions.Dedupe)
	if err != nil {
		return fmt.Errorf("resolve dedupe prompt: %w", err)
	}
	promptBytes, err := os.ReadFile(promptAbs)
	if err != nil {
		return fmt.Errorf("read dedupe prompt %s: %w", promptAbs, err)
	}
	promptBody := string(promptBytes)

	out, err := run.CreateForDedupe(run.CreateDedupeOpts{
		ProjectDir:       dir,
		Slug:             runDedupeFlags.name,
		InputRuns:        relInputs,
		DedupePrompt:     promptBody,
		DedupeSourcePath: sourcePath,
		DedupeSpec:       spec,
	})
	if err != nil {
		return fmt.Errorf("create dedupe run: %w", err)
	}

	// Build FINDINGS_JSON: every input finding annotated with from_run +
	// current review state.
	annotated, err := annotateInputs(inputs, dir)
	if err != nil {
		return fmt.Errorf("collect input findings: %w", err)
	}
	findingsJSON, err := json.MarshalIndent(annotated, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal findings: %w", err)
	}

	prompt, err := renderDedupePrompt(dedupePromptVars{
		RepoRoot:         targetRepo,
		InputRunCount:    len(inputs),
		InputRunsList:    strings.Join(relInputs, ", "),
		FindingsJSON:     string(findingsJSON),
		UserInstructions: promptBody,
	})
	if err != nil {
		return fmt.Errorf("render dedupe prompt: %w", err)
	}

	// Snapshot the rendered prompt for archival; the embedded frame
	// template alone isn't enough since FINDINGS_JSON is the variable
	// part, and the agent only sees the rendered version.
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
		"inputs", len(inputs),
		"total_findings", len(annotated),
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
		_ = printRunResult(out.Dir())
		return agentErr
	}

	canonicalCount, err := run.CountLines(filepath.Join(out.Dir(), "findings.jsonl"))
	if err != nil {
		return fmt.Errorf("count canonical findings: %w", err)
	}

	if err := out.MarkCompleted(); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	logger.Info("complete",
		"out", out.Dir(),
		"canonical_findings", canonicalCount,
		"input_findings", len(annotated),
		"elapsed", time.Since(startAll).Round(time.Second),
	)
	_ = printRunResult(out.Dir())
	return nil
}

// resolveDedupeInputs validates --run inputs and resolves the shared
// target_repo (rejecting input runs that disagree).
func resolveDedupeInputs(projectDir string) ([]inputRun, []string, string, error) {
	if len(runDedupeFlags.runs) == 0 {
		return nil, nil, "", fmt.Errorf("at least one --run is required")
	}

	inputs := make([]inputRun, 0, len(runDedupeFlags.runs))
	relInputs := make([]string, 0, len(runDedupeFlags.runs))
	var targetRepo string
	for _, raw := range runDedupeFlags.runs {
		abs := raw
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, raw)
		}
		rp, err := run.Open(abs)
		if err != nil {
			return nil, nil, "", fmt.Errorf("open %s: %w", raw, err)
		}
		m, err := rp.Manifest()
		if err != nil {
			return nil, nil, "", fmt.Errorf("read %s manifest: %w", raw, err)
		}
		if m.CompletedAt == nil {
			return nil, nil, "", fmt.Errorf("input run %s is not completed (run.json missing completed_at)", raw)
		}
		if m.TargetRepo != "" {
			if targetRepo == "" {
				targetRepo = m.TargetRepo
			} else if targetRepo != m.TargetRepo {
				return nil, nil, "", fmt.Errorf("input runs disagree on target_repo: %q vs %q", targetRepo, m.TargetRepo)
			}
		}
		rel, err := filepath.Rel(projectDir, abs)
		if err != nil {
			rel = abs
		}
		inputs = append(inputs, inputRun{abs: abs, rel: filepath.ToSlash(rel), manifest: m})
		relInputs = append(relInputs, filepath.ToSlash(rel))
	}
	return inputs, relInputs, targetRepo, nil
}

func buildDedupeSpec(cfg project.Config, targetRepo string) (agent.Spec, error) {
	if runDedupeFlags.agent != "" && runDedupeFlags.script != "" {
		return agent.Spec{}, fmt.Errorf("--agent and --agent-script are mutually exclusive")
	}
	if runDedupeFlags.agent != "" {
		switch runDedupeFlags.agent {
		case "claude", "codex":
		default:
			return agent.Spec{}, fmt.Errorf("--agent must be claude or codex; got %q", runDedupeFlags.agent)
		}
	}

	agentName := cfg.Agent.Name
	agentModel := cfg.Agent.Model
	agentScript := cfg.Agent.Script

	if runDedupeFlags.agent != "" {
		agentName = runDedupeFlags.agent
		agentScript = ""
	}
	if runDedupeFlags.script != "" {
		agentScript = runDedupeFlags.script
		if cfg.Agent.Script == "" {
			agentName = "custom"
		}
	}
	if runDedupeFlags.model != "" {
		agentModel = runDedupeFlags.model
	}
	if agentScript != "" && !filepath.IsAbs(agentScript) {
		abs, err := filepath.Abs(agentScript)
		if err != nil {
			return agent.Spec{}, fmt.Errorf("resolve agent.script %q: %w", agentScript, err)
		}
		agentScript = abs
	}
	if agentScript != "" {
		if _, err := exec.LookPath(agentScript); err != nil {
			return agent.Spec{}, fmt.Errorf("agent script %q not executable: %w", agentScript, err)
		}
	}

	spec := agent.Spec{
		Name:    agentName,
		Model:   agentModel,
		Effort:  runDedupeFlags.effort,
		WorkDir: targetRepo,
		Timeout: runDedupeFlags.timeout,
		Script:  agentScript,
	}
	return spec, nil
}

// annotatedFinding is one input finding with its source-run path and
// current review state attached. This is the per-finding shape inside
// FINDINGS_JSON.
type annotatedFinding struct {
	schema.Finding
	FromRun        string                 `json:"from_run"`
	CurrentReviews map[string]reviewState `json:"current_reviews,omitempty"`
}

type reviewState struct {
	// Labels mirrors schema.Review.Labels' nil-means-untouched
	// semantic — agent prompts can distinguish "didn't override
	// labels" from "explicitly cleared".
	Labels *[]string `json:"labels,omitempty"`
	// Severity mirrors schema.Review.Severity; nil/omitted when
	// this entry didn't override severity. Surfaces the reviewer's
	// downgrades / upgrades so the dedupe and group agents rank by
	// the same effective severity the reviewer sees in the UI.
	Severity *string `json:"severity,omitempty"`
	Comment  string  `json:"comment,omitempty"`
	At       string  `json:"at"`
}

func annotateInputs(inputs []inputRun, projectDir string) ([]annotatedFinding, error) {
	var out []annotatedFinding
	for _, in := range inputs {
		rp, err := run.Open(in.abs)
		if err != nil {
			return nil, fmt.Errorf("open input run %s: %w", in.rel, err)
		}
		findings, err := rp.LoadFindings()
		if err != nil {
			return nil, fmt.Errorf("read findings from %s: %w", in.rel, err)
		}
		reviews, err := loadReviewsByFinding(in.abs)
		if err != nil {
			return nil, fmt.Errorf("read reviews from %s: %w", in.rel, err)
		}
		for _, f := range findings {
			out = append(out, annotatedFinding{
				Finding:        f,
				FromRun:        in.rel,
				CurrentReviews: reviews[f.ID],
			})
		}
	}
	return out, nil
}

// loadReviewsByFinding scans every reviews_<author>.jsonl in runDir
// and returns finding_id → author → effective review state. Each
// axis (labels, severity) tracks its own "latest entry that touched
// this axis": a comment-only entry doesn't wipe a prior label or
// severity override the same author had set, matching the UI's
// nil-don't-touch semantic. Comment + At come from the entry that
// most recently said anything at all (so consumers get the freshest
// associated free-text and timestamp).
func loadReviewsByFinding(runDir string) (map[string]map[string]reviewState, error) {
	files, err := run.ReviewFiles(runDir)
	if err != nil {
		return nil, err
	}
	type accum struct {
		// "Touched" axes — separate per-axis latest tracker so a
		// later comment-only entry doesn't silently revert prior
		// overrides on the other axis.
		labels        *[]string
		labelsAt      time.Time
		severity      *string
		severityAt    time.Time
		latestComment string
		latestAt      time.Time
	}
	acc := map[string]map[string]*accum{}
	for _, rf := range files {
		f, err := os.Open(rf.Path)
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
			if r.Subject.Kind != schema.SubjectFinding || r.Subject.ID == "" {
				continue
			}
			if acc[r.Subject.ID] == nil {
				acc[r.Subject.ID] = map[string]*accum{}
			}
			a := acc[r.Subject.ID][rf.Author]
			if a == nil {
				a = &accum{}
				acc[r.Subject.ID][rf.Author] = a
			}
			if r.Labels != nil && (a.labels == nil || r.At.After(a.labelsAt)) {
				a.labels = r.Labels
				a.labelsAt = r.At
			}
			if r.Severity != nil && (a.severity == nil || r.At.After(a.severityAt)) {
				a.severity = r.Severity
				a.severityAt = r.At
			}
			if r.At.After(a.latestAt) {
				a.latestComment = r.Comment
				a.latestAt = r.At
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	out := make(map[string]map[string]reviewState, len(acc))
	for fid, byAuthor := range acc {
		out[fid] = make(map[string]reviewState, len(byAuthor))
		for author, a := range byAuthor {
			out[fid][author] = reviewState{
				Labels:   a.labels,
				Severity: a.severity,
				Comment:  a.latestComment,
				At:       a.latestAt.Format(time.RFC3339Nano),
			}
		}
	}
	return out, nil
}

func renderDedupePrompt(vars dedupePromptVars) (string, error) {
	var b strings.Builder
	if err := dedupePromptTmpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}

