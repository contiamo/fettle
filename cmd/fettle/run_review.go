package main

import (
	"bufio"
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
type reviewPromptVars struct {
	SubjectKind      string
	SubjectID        string
	SubjectJSON      string
	RepoRoot         string
	UserInstructions string
}

var runReviewFlags struct {
	run         string
	concurrency int
	limit       int
	agent       string
	model       string
	script      string
	effort      string
	timeout     time.Duration
}

var runReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Run the review agent on every finding (or group) of an existing run",
	Long: `review iterates the findings (find/dedupe runs) or groups (group
runs) of --run, invoking the configured agent on each subject not
yet reviewed by this agent. The agent appends review entries via
` + "`fettle add review`" + `; one entry per subject (or zero — review is
optional per subject).

On first invocation in --run, the active review.md template is
snapshotted into runs/<run>/instructions/review.md. Subsequent
invocations re-use the snapshot.

For custom agent scripts via --agent-script, see the contract
documented on internal/agent.runCustom.`,
	RunE: runRunReview,
}

func init() {
	runReviewCmd.Flags().StringVar(&runReviewFlags.run, "run", "", "path to the target run folder (required)")
	runReviewCmd.Flags().IntVarP(&runReviewFlags.concurrency, "concurrency", "c", 4, "max concurrent agent invocations")
	runReviewCmd.Flags().IntVar(&runReviewFlags.limit, "limit", 0, "review at most N pending subjects this invocation (0 = all)")
	runReviewCmd.Flags().StringVar(&runReviewFlags.agent, "agent", "", "select a built-in agent (claude or codex); mutually exclusive with --agent-script")
	runReviewCmd.Flags().StringVar(&runReviewFlags.model, "model", "", "agent model override")
	runReviewCmd.Flags().StringVar(&runReviewFlags.script, "agent-script", "", "run a custom agent script (path to executable)")
	runReviewCmd.Flags().StringVar(&runReviewFlags.effort, "effort", "", "agent reasoning effort: low|medium|high|xhigh|max")
	runReviewCmd.Flags().DurationVar(&runReviewFlags.timeout, "timeout", defaultReviewTimeout, "per-subject agent timeout")
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

	// Currently support find/dedupe runs (subject = finding). Group
	// runs are deferred to a follow-up — they need group iteration +
	// MEMBERS_JSON in the prompt.
	if in.manifest.Stage == "group" {
		return fmt.Errorf("review of group runs is not yet implemented; v0 supports find/dedupe runs only")
	}
	if in.manifest.Stage != "find" && in.manifest.Stage != "dedupe" {
		return fmt.Errorf("unsupported run stage %q for review", in.manifest.Stage)
	}

	// Snapshot review.md into the run folder if not already there.
	snapPath := filepath.Join(in.rp.Dir(), "instructions", "review.md")
	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if err := snapshotReviewPrompt(dir, cfg, snapPath); err != nil {
		return fmt.Errorf("snapshot review prompt: %w", err)
	}
	promptBody, err := os.ReadFile(snapPath)
	if err != nil {
		return fmt.Errorf("read snapshotted review prompt: %w", err)
	}

	findings, err := loadFindings(in.rp.Dir())
	if err != nil {
		return fmt.Errorf("load findings: %w", err)
	}
	done, err := loadReviewedSubjects(in.rp.Dir(), in.author)
	if err != nil {
		return fmt.Errorf("load existing reviews: %w", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

			err := reviewOne(ctx, in.rp, in.spec, string(promptBody), in.manifest.TargetRepo, f)
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

	targetRepo := manifest.TargetRepo // direct for find runs; dedupe inherits from input runs

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

// snapshotReviewPrompt copies the project's review.md into the run's
// instructions/review.md if not already there. First-write-wins —
// subsequent reviewers re-use the same snapshot.
func snapshotReviewPrompt(projectDir string, cfg project.Config, snapPath string) error {
	if _, err := os.Stat(snapPath); err == nil {
		return nil // already snapshotted
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	srcPath := cfg.Instructions.Review
	if !filepath.IsAbs(srcPath) {
		srcPath = filepath.Join(projectDir, srcPath)
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

// loadFindings reads findings.jsonl into memory. Tolerates malformed
// lines (skipped, like the rest of the harness).
func loadFindings(runDir string) ([]schema.Finding, error) {
	f, err := os.Open(filepath.Join(runDir, "findings.jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var out []schema.Finding
	for sc.Scan() {
		var fnd schema.Finding
		if err := json.Unmarshal(sc.Bytes(), &fnd); err != nil {
			continue
		}
		out = append(out, fnd)
	}
	return out, sc.Err()
}

// loadReviewedSubjects returns the set of finding ids already reviewed
// by author in this run. Empty if the file doesn't exist.
func loadReviewedSubjects(runDir, author string) (map[string]bool, error) {
	f, err := os.Open(filepath.Join(runDir, "reviews_"+author+".jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	defer f.Close()

	done := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		var r schema.Review
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Subject.ID != "" {
			done[r.Subject.ID] = true
		}
	}
	return done, sc.Err()
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
