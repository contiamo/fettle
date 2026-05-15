package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/contiamo/fettle/internal/agent"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

const defaultReviewTimeout = 5 * time.Minute

// reviewPromptFrame wraps the user's review instructions with the
// `fettle add review` recording protocol. The user's review.md fills
// the {{.UserInstructions}} placeholder.
//
//go:embed prompts/review.md
var reviewPromptFrame string

var reviewPromptTmpl = template.Must(template.New("review").Parse(reviewPromptFrame))

// reviewPromptVars carries placeholders the review frame interpolates.
// MembersJSON and MemberReviewsJSON are populated only for group review;
// the frame's `{{if .MembersJSON}}` branch suppresses the Members section
// when they are empty (the finding-review case).
type reviewPromptVars struct {
	SubjectKind       string
	SubjectID         string
	SubjectJSON       string
	MembersJSON       string
	MemberReviewsJSON string
	RepoRoot          string
	UserInstructions  string
}

var runReviewFlags struct {
	run         string
	concurrency int
	limit       int
	agent       string
	model       string
	script      string
	effort      string
	prompt      string
	timeout     time.Duration
}

var runReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Run the review agent on every finding of an existing run",
	Long: `review iterates the findings of --run, invoking the configured
agent on each finding not yet reviewed by this agent. The agent
appends review entries via ` + "`fettle add review`" + `; one entry per
finding (or zero — review is optional per finding).

Resume keys on the agent's slug, so switching the model
(claude/sonnet → claude/opus) doesn't force re-review.

On first invocation in --run, the active prompt is snapshotted
into ` + "`<run>/instructions/review.md`" + `. Subsequent invocations re-
use the snapshot — editing the project's
` + "`instructions/review.md`" + ` after the run started has no
effect.

For custom agent scripts via --agent-script, see the contract
documented on internal/agent.runCustom.`,
	RunE: runRunReview,
}

func init() {
	runReviewCmd.Flags().StringVar(&runReviewFlags.run, "run", "", "path to the target run folder (required)")
	runReviewCmd.Flags().IntVarP(&runReviewFlags.concurrency, "concurrency", "c", 4, "max concurrent agent invocations")
	runReviewCmd.Flags().IntVar(&runReviewFlags.limit, "limit", 0, "review at most N pending findings this invocation (0 = all)")
	runReviewCmd.Flags().StringVar(&runReviewFlags.agent, "agent", "", "select a built-in agent (claude or codex); mutually exclusive with --agent-script")
	runReviewCmd.Flags().StringVar(&runReviewFlags.model, "model", "", "agent model override")
	runReviewCmd.Flags().StringVar(&runReviewFlags.script, "agent-script", "", "run a custom agent script (path to executable)")
	runReviewCmd.Flags().StringVar(&runReviewFlags.effort, "effort", "", "agent reasoning effort: low|medium|high|xhigh|max")
	runReviewCmd.Flags().StringVar(&runReviewFlags.prompt, "prompt", "", "path to the review prompt to use (overrides instructions.review; relative to cwd; first invocation against a run only — subsequent reviews use the snapshot)")
	runReviewCmd.Flags().DurationVar(&runReviewFlags.timeout, "timeout", defaultReviewTimeout, "per-finding agent timeout")
	_ = runReviewCmd.MarkFlagRequired("run")
	runCmd.AddCommand(runReviewCmd)
}

// reviewInputs is the resolved configuration for a review run.
type reviewInputs struct {
	rp       *run.Path
	manifest schema.RunManifest
	spec     agent.Spec
	author   string
}

func runRunReview(cmd *cobra.Command, args []string) error {
	if runReviewFlags.concurrency < 1 {
		runReviewFlags.concurrency = 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir, err := projectDir()
	if err != nil {
		return err
	}

	in, err := resolveReviewInputs(dir)
	if err != nil {
		return err
	}

	if in.manifest.Stage != "find" {
		return fmt.Errorf("unsupported run stage %q for review", in.manifest.Stage)
	}

	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}

	configRel := cfg.Instructions.Review
	snapName := "review.md"
	snapPath := filepath.Join(in.rp.Dir(), "instructions", snapName)

	// --prompt only applies on the first invocation. The snapshot is
	// authoritative thereafter; silently ignoring the flag would be
	// confusing, so reject explicitly.
	if runReviewFlags.prompt != "" {
		if _, err := os.Stat(snapPath); err == nil {
			return fmt.Errorf("--prompt only applies on the first invocation against this run; the snapshot at %s is authoritative now (delete it to re-snapshot, or start a new run)", snapPath)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat snapshot %s: %w", snapPath, err)
		}
	}

	srcAbs, _, err := resolvePromptSource(dir, runReviewFlags.prompt, configRel)
	if err != nil {
		return fmt.Errorf("resolve review prompt: %w", err)
	}
	if err := snapshotReviewPrompt(srcAbs, snapPath); err != nil {
		return fmt.Errorf("snapshot review prompt: %w", err)
	}
	promptBody, err := os.ReadFile(snapPath)
	if err != nil {
		return fmt.Errorf("read snapshotted review prompt: %w", err)
	}

	done, err := loadReviewedSubjects(in.rp, in.author)
	if err != nil {
		return fmt.Errorf("load existing reviews: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = dir

	return reviewFindings(ctx, logger, in, string(promptBody), done)
}

// reviewFindings iterates the run's findings and invokes the agent
// once per pending finding.
func reviewFindings(ctx context.Context, logger *slog.Logger, in *reviewInputs, promptBody string, done map[string]bool) error {
	entries, err := in.rp.LoadFindingEntries()
	if err != nil {
		return fmt.Errorf("load findings: %w", err)
	}
	// Dedupe by id: two findings_*.jsonl files in the same run
	// (rare — happens only when fettle find runs twice with the
	// same target) shouldn't make the review harness double-process
	// a finding. Latest CreatedAt wins.
	deduped := make(map[string]schema.FindingEntry, len(entries))
	for _, e := range entries {
		if existing, ok := deduped[e.ID]; ok && !e.CreatedAt.After(existing.CreatedAt) {
			continue
		}
		deduped[e.ID] = e
	}
	findings := make([]schema.Finding, 0, len(deduped))
	for _, e := range deduped {
		findings = append(findings, e.Finding)
	}

	pending := make([]schema.Finding, 0, len(findings))
	for _, f := range findings {
		if !done[f.ID] {
			pending = append(pending, f)
		}
	}
	if runReviewFlags.limit > 0 && len(pending) > runReviewFlags.limit {
		pending = pending[:runReviewFlags.limit]
	}

	logger.Info("plan",
		"run", in.rp.Dir(),
		"author", in.author,
		"findings", len(findings),
		"already_reviewed", len(done),
		"pending", len(pending),
		"concurrency", runReviewFlags.concurrency,
		"agent", in.spec.Name,
	)
	if len(pending) == 0 {
		_ = printRunResult(in.rp.Dir())
		return nil
	}

	sem := make(chan struct{}, runReviewFlags.concurrency)
	var wg sync.WaitGroup
	var ok, fail atomic.Int64
	startAll := time.Now()

	for i, f := range pending {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, f schema.Finding) {
			defer wg.Done()
			defer func() { <-sem }()

			fileLogger := logger.With("finding", f.ID, "n", i+1, "total", len(pending))
			fileLogger.Info("reviewing")

			// in.spec.WorkDir carries the resolved repo root (walked
			// back through input_runs[] for merge/dedupe inputs);
			// in.manifest.TargetRepo is empty for those stages, so we
			// must use the resolved value to populate REPO_ROOT in
			// the prompt.
			err := reviewOne(ctx, in.rp, in.spec, promptBody, in.spec.WorkDir, f)
			if err != nil {
				fail.Add(1)
				fileLogger.Warn("failed", "error", err)
				return
			}
			ok.Add(1)
			fileLogger.Info("done")
		}(i, f)
	}
	wg.Wait()

	logger.Info("complete",
		"run", in.rp.Dir(),
		"reviewed", ok.Load(),
		"failed", fail.Load(),
		"elapsed", time.Since(startAll).Round(time.Second),
	)
	_ = printRunResult(in.rp.Dir())
	return nil
}

// resolveReviewInputs validates flags + opens the target run, builds
// the agent spec from CLI overrides falling back to project config.
func resolveReviewInputs(projectDir string) (*reviewInputs, error) {
	if runReviewFlags.run == "" {
		return nil, fmt.Errorf("--run is required")
	}
	if runReviewFlags.agent != "" && runReviewFlags.script != "" {
		return nil, fmt.Errorf("--agent and --agent-script are mutually exclusive")
	}
	if runReviewFlags.agent != "" {
		switch runReviewFlags.agent {
		case "claude", "codex":
		default:
			return nil, fmt.Errorf("--agent must be claude or codex (use --agent-script for custom scripts); got %q", runReviewFlags.agent)
		}
	}

	abs := runReviewFlags.run
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, abs)
	}
	rp, err := run.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open run: %w", err)
	}
	manifest, err := rp.Manifest()
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	cfg, err := project.Load(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load project: %w", err)
	}
	agentName := cfg.Agent.Name
	agentModel := cfg.Agent.Model
	agentScript := cfg.Agent.Script

	if runReviewFlags.agent != "" {
		agentName = runReviewFlags.agent
		agentScript = ""
	}
	if runReviewFlags.script != "" {
		agentScript = runReviewFlags.script
		if cfg.Agent.Script == "" {
			agentName = "custom"
		}
	}
	if runReviewFlags.model != "" {
		agentModel = runReviewFlags.model
	}
	if agentScript != "" && !filepath.IsAbs(agentScript) {
		absScript, err := filepath.Abs(agentScript)
		if err != nil {
			return nil, fmt.Errorf("resolve agent.script %q: %w", agentScript, err)
		}
		agentScript = absScript
	}
	if agentScript != "" {
		if _, err := exec.LookPath(agentScript); err != nil {
			return nil, fmt.Errorf("agent script %q not executable: %w", agentScript, err)
		}
	}

	// Find runs carry target_repo directly on the manifest.
	targetRepo := manifest.TargetRepo
	_ = projectDir

	spec := agent.Spec{
		Name:    agentName,
		Model:   agentModel,
		Effort:  runReviewFlags.effort,
		WorkDir: targetRepo,
		Timeout: runReviewFlags.timeout,
		Script:  agentScript,
	}

	return &reviewInputs{
		rp:       rp,
		manifest: manifest,
		spec:     spec,
		author:   agentName,
	}, nil
}

// snapshotReviewPrompt copies srcPath into snapPath if snapPath is
// not already present. First-write-wins — subsequent reviewers re-use
// the same snapshot, and if the snapshot already exists the source
// path is not even read (so legacy runs whose snapshot already landed
// don't require the source-side config to still be valid).
func snapshotReviewPrompt(srcPath, snapPath string) error {
	if _, err := os.Stat(snapPath); err == nil {
		return nil // already snapshotted
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if srcPath == "" {
		return fmt.Errorf("source prompt path is empty")
	}
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source review prompt %q: %w", srcPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(snapPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(snapPath, src, 0o644)
}

// writePromptSidecar persists the rendered review prompt next to the
// agent log so verification + debugging can see exactly what the
// agent received. Merge runs don't pre-create raw/, so MkdirAll
// guards the write.
func writePromptSidecar(rp *run.Path, id, prompt string) error {
	if err := os.MkdirAll(rp.RawDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(rp.RawDir(), "review_"+id+".prompt.txt"), []byte(prompt), 0o644)
}

// loadReviewedSubjects returns the set of finding ids that author
// has already reviewed in this run. The author argument is the
// agent slug (FETTLE_AGENT) — a finding counts as "done by author"
// if any review entry has a matching slug, regardless of model.
// Switching the agent's model doesn't force re-review.
func loadReviewedSubjects(rp *run.Path, author string) (map[string]bool, error) {
	all, err := rp.LoadReviewEntries()
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, e := range all {
		if e.Kind == schema.SubjectFinding && schema.AuthorSlug(e.Author) == author {
			done[e.ID] = true
		}
	}
	return done, nil
}

// reviewOne invokes the agent against a single finding. Errors only on
// agent failure; a "no review emitted" outcome is silent success.
func reviewOne(ctx context.Context, rp *run.Path, spec agent.Spec, promptBody, repoRoot string, f schema.Finding) error {
	logPath := filepath.Join(rp.RawDir(), "review_"+f.ID+".log")

	subjectJSON, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal finding: %w", err)
	}
	prompt, err := renderReviewPrompt(reviewPromptVars{
		SubjectKind:      schema.SubjectFinding,
		SubjectID:        f.ID,
		SubjectJSON:      string(subjectJSON),
		RepoRoot:         repoRoot,
		UserInstructions: promptBody,
	})
	if err != nil {
		return fmt.Errorf("render review prompt: %w", err)
	}
	if err := writePromptSidecar(rp, f.ID, prompt); err != nil {
		return fmt.Errorf("write prompt sidecar: %w", err)
	}

	stageSpec := spec
	stageSpec.AddDirs = []string{rp.Dir()}
	stageSpec.Env = []string{
		"FETTLE_RUN=" + rp.Dir(),
		"FETTLE_AGENT=" + spec.Name,
	}
	if spec.Model != "" {
		stageSpec.Env = append(stageSpec.Env, "FETTLE_MODEL="+spec.Model)
	}
	if spec.Effort != "" {
		stageSpec.Env = append(stageSpec.Env, "FETTLE_EFFORT="+spec.Effort)
	}

	res, err := agent.Run(ctx, stageSpec, prompt)
	if res != nil {
		_ = os.WriteFile(logPath, res.Output, 0o644)
	}
	return err
}

func renderReviewPrompt(vars reviewPromptVars) (string, error) {
	var b strings.Builder
	if err := reviewPromptTmpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}
