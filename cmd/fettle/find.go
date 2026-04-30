package main

import (
	"context"
	_ "embed"
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
	"github.com/contiamo/fettle/internal/walk"
	"github.com/spf13/cobra"
)

const defaultFindTimeout = 10 * time.Minute

// findPromptFrame is the harness-owned wrapper around the user's find
// instructions. It captures the agent contract — variable values, the
// `fettle finding add` recording protocol, and exit-code handling — so
// the user's own find.md can stay scoped to "what to look for".
//
//go:embed prompts/find.md
var findPromptFrame string

// findPromptTmpl is the parsed template, computed once at startup.
var findPromptTmpl = template.Must(template.New("find").Parse(findPromptFrame))

// findPromptVars carries the placeholders the find frame interpolates.
type findPromptVars struct {
	TargetFile       string
	RepoRoot         string
	UserInstructions string
}

var findFlags struct {
	name        string
	resume      string
	concurrency int
	limit       int
	include     []string
	exclude     []string
	agent       string
	model       string
	script      string
	effort      string
	timeout     time.Duration
}

var findCmd = &cobra.Command{
	Use:   "find",
	Short: "Run the find agent on every matching file in the target repo",
	Long: `find creates a new run folder under runs/ (or resumes one with
--resume), snapshots the find prompt into it, walks the target repo, and
runs the configured agent on every file that matches the project's
include/exclude globs. Findings are appended to findings.jsonl; per-file
status is appended to files.jsonl for resume.

On --resume, the run's manifest (run.json) is authoritative — target
repo, include/exclude globs, and agent/model/effort all come from there,
not from .fettle.json. Flags that would change those values are
rejected.

` + agent.CustomScriptDoc,
	RunE: runFind,
}

func init() {
	findCmd.Flags().StringVar(&findFlags.name, "name", "", "human label appended to the run folder timestamp (default: random hex)")
	findCmd.Flags().StringVar(&findFlags.resume, "resume", "", "path to an existing run folder to resume")
	findCmd.Flags().IntVarP(&findFlags.concurrency, "concurrency", "c", 4, "max concurrent agent invocations")
	findCmd.Flags().IntVar(&findFlags.limit, "limit", 0, "scan at most N files this invocation (0 = all)")
	findCmd.Flags().StringSliceVar(&findFlags.include, "include", nil, "include globs (overrides project config; not allowed with --resume)")
	findCmd.Flags().StringSliceVar(&findFlags.exclude, "exclude", nil, "exclude globs (overrides project config; not allowed with --resume)")
	findCmd.Flags().StringVar(&findFlags.agent, "agent", "", "select a built-in agent (claude or codex); mutually exclusive with --agent-script")
	findCmd.Flags().StringVar(&findFlags.model, "model", "", "agent model override (overrides project config; not allowed with --resume)")
	findCmd.Flags().StringVar(&findFlags.script, "agent-script", "", "run a custom agent script (path to executable); mutually exclusive with --agent")
	findCmd.Flags().StringVar(&findFlags.effort, "effort", "", "agent reasoning effort: low|medium|high|xhigh|max (not allowed with --resume)")
	findCmd.Flags().DurationVar(&findFlags.timeout, "timeout", defaultFindTimeout, "per-file agent timeout")
	rootCmd.AddCommand(findCmd)
}

// findInputs is the resolved set of values used to drive a run, regardless
// of whether the run is new or being resumed.
type findInputs struct {
	rp         *run.Path
	targetRepo string
	include    []string
	exclude    []string
	spec       agent.Spec
}

func runFind(cmd *cobra.Command, args []string) error {
	if findFlags.concurrency < 1 {
		findFlags.concurrency = 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir, err := projectDir()
	if err != nil {
		return err
	}

	in, err := resolveFindInputs(dir)
	if err != nil {
		return err
	}

	files, err := walk.Walk(in.targetRepo, in.include, in.exclude)
	if err != nil {
		return fmt.Errorf("walk target repo: %w", err)
	}

	done, err := in.rp.LoadDoneFiles()
	if err != nil {
		return fmt.Errorf("load resume state: %w", err)
	}
	pending := pendingFiles(files, in.targetRepo, done)
	if findFlags.limit > 0 && len(pending) > findFlags.limit {
		pending = pending[:findFlags.limit]
	}

	logger.Info("plan",
		"run", in.rp.Dir(),
		"discovered", len(files),
		"already_done", len(done),
		"pending", len(pending),
		"concurrency", findFlags.concurrency,
		"agent", in.spec.Name,
		"model", in.spec.Model,
	)
	if len(pending) == 0 {
		fmt.Println(in.rp.Dir())
		return nil
	}

	// Always read the snapshotted prompt — never the editable template.
	promptBody, err := os.ReadFile(filepath.Join(in.rp.Dir(), "instructions", "find.md"))
	if err != nil {
		return fmt.Errorf("read snapshotted find prompt: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sem := make(chan struct{}, findFlags.concurrency)
	var wg sync.WaitGroup
	var ok, empty, fail, total atomic.Int64
	startAll := time.Now()

	for i, file := range pending {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, file string) {
			defer wg.Done()
			defer func() { <-sem }()

			rel, relErr := filepath.Rel(in.targetRepo, file)
			if relErr != nil {
				fileLogger := logger.With("file", file, "n", i+1, "total", len(pending))
				fileLogger.Error("compute repo-relative path", "error", relErr)
				_ = in.rp.AppendFileStatus(schema.FileStatus{
					File:    file,
					Status:  schema.StatusError,
					Error:   "filepath.Rel: " + relErr.Error(),
					Started: time.Now().UTC(),
					Ended:   time.Now().UTC(),
				})
				fail.Add(1)
				return
			}
			rel = filepath.ToSlash(rel)
			fileLogger := logger.With("file", rel, "n", i+1, "total", len(pending))
			fileLogger.Info("analyzing")

			status := analyzeOne(ctx, in.rp, in.spec, string(promptBody), in.targetRepo, file, rel)
			if appendErr := in.rp.AppendFileStatus(status); appendErr != nil {
				fileLogger.Error("write file status", "error", appendErr)
			}

			switch status.Status {
			case schema.StatusOK:
				ok.Add(1)
				total.Add(int64(status.FindingCount))
				fileLogger.Info("done", "findings", status.FindingCount, "duration", status.Ended.Sub(status.Started).Round(time.Second))
			case schema.StatusEmpty:
				empty.Add(1)
				fileLogger.Info("done (empty)", "duration", status.Ended.Sub(status.Started).Round(time.Second))
			case schema.StatusError:
				fail.Add(1)
				fileLogger.Warn("failed", "error", status.Error, "duration", status.Ended.Sub(status.Started).Round(time.Second))
			}
		}(i, file)
	}
	wg.Wait()

	logger.Info("complete",
		"run", in.rp.Dir(),
		"with_findings", ok.Load(),
		"empty", empty.Load(),
		"failed", fail.Load(),
		"total_findings", total.Load(),
		"elapsed", time.Since(startAll).Round(time.Second),
	)
	fmt.Println(in.rp.Dir())
	return nil
}

// resolveFindInputs branches on --resume: from-manifest (resume) vs.
// from-project-config (new run). On resume, flags that would change run
// identity are rejected — the manifest is authoritative.
func resolveFindInputs(projectDir string) (*findInputs, error) {
	if findFlags.resume != "" {
		var conflicts []string
		if findFlags.name != "" {
			conflicts = append(conflicts, "--name")
		}
		if len(findFlags.include) > 0 {
			conflicts = append(conflicts, "--include")
		}
		if len(findFlags.exclude) > 0 {
			conflicts = append(conflicts, "--exclude")
		}
		if findFlags.effort != "" {
			conflicts = append(conflicts, "--effort")
		}
		if findFlags.agent != "" {
			conflicts = append(conflicts, "--agent")
		}
		if findFlags.model != "" {
			conflicts = append(conflicts, "--model")
		}
		if findFlags.script != "" {
			conflicts = append(conflicts, "--agent-script")
		}
		if len(conflicts) > 0 {
			return nil, fmt.Errorf("cannot combine --resume with %s; the run's manifest is authoritative", strings.Join(conflicts, ", "))
		}

		abs := findFlags.resume
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, abs)
		}
		rp, err := run.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open run: %w", err)
		}
		m, err := rp.Manifest()
		if err != nil {
			return nil, fmt.Errorf("read run manifest: %w", err)
		}
		findStage, ok := m.Stages["find"]
		if !ok {
			return nil, fmt.Errorf("run %s has no `find` stage in run.json", abs)
		}
		return &findInputs{
			rp:         rp,
			targetRepo: m.TargetRepo,
			include:    m.Include,
			exclude:    m.Exclude,
			spec: agent.Spec{
				Name:    findStage.Agent,
				Model:   findStage.Model,
				Effort:  findStage.Effort,
				Script:  findStage.Script,
				WorkDir: m.TargetRepo,
				Timeout: findFlags.timeout,
			},
		}, nil
	}

	cfg, err := project.Load(projectDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no .fettle.json in %s — run `fettle init` first", projectDir)
		}
		return nil, fmt.Errorf("load project: %w", err)
	}
	targetRepo, err := cfg.ResolveTargetRepo(projectDir)
	if err != nil {
		return nil, err
	}

	include := cfg.Include
	if len(findFlags.include) > 0 {
		include = findFlags.include
	}
	exclude := cfg.Exclude
	if len(findFlags.exclude) > 0 {
		exclude = findFlags.exclude
	}

	// --agent and --agent-script both pick the agent, so allowing both
	// is ambiguous. Each does exactly one thing:
	//   --agent NAME           pick a built-in (claude or codex)
	//   --agent-script PATH    run a custom script
	if findFlags.agent != "" && findFlags.script != "" {
		return nil, fmt.Errorf("--agent and --agent-script are mutually exclusive: --agent picks a built-in (claude|codex), --agent-script runs a custom script")
	}
	if findFlags.agent != "" {
		switch findFlags.agent {
		case "claude", "codex":
			// supported
		default:
			return nil, fmt.Errorf("--agent must be claude or codex (use --agent-script for custom scripts); got %q", findFlags.agent)
		}
	}

	agentName := cfg.Agent.Name
	agentModel := cfg.Agent.Model
	agentScript := cfg.Agent.Script

	if findFlags.agent != "" {
		// Built-in dispatch: clear any custom-script config so we
		// don't accidentally take the runCustom path with a built-in
		// label.
		agentName = findFlags.agent
		agentScript = ""
	}
	if findFlags.script != "" {
		agentScript = findFlags.script
		// Default the label to "custom" unless the user already named
		// it via cfg (e.g. agent.name = "security-pass" alongside
		// agent.script in .fettle.json). Mutex above prevents --agent
		// overriding here.
		if cfg.Agent.Script == "" {
			agentName = "custom"
		}
	}
	if findFlags.model != "" {
		agentModel = findFlags.model
	}
	// Resolve a relative agent script to absolute now so the manifest
	// records a path that's still valid on resume from a different cwd.
	if agentScript != "" && !filepath.IsAbs(agentScript) {
		abs, err := filepath.Abs(agentScript)
		if err != nil {
			return nil, fmt.Errorf("resolve agent.script %q: %w", agentScript, err)
		}
		agentScript = abs
	}
	// Validate the script is executable now, at startup — not per-file
	// at agent run time. Errors here mean no run folder gets created.
	if agentScript != "" {
		if _, err := exec.LookPath(agentScript); err != nil {
			if strings.ContainsAny(agentScript, " \t") {
				return nil, fmt.Errorf(`--agent-script takes a single path, not a command-with-args.
For arguments, write a small wrapper script with the args baked in, or set agent.args in .fettle.json.
Got: %q`, agentScript)
			}
			return nil, fmt.Errorf("agent script %q not executable: %w", agentScript, err)
		}
	}
	spec := agent.Spec{
		Name:    agentName,
		Model:   agentModel,
		Effort:  findFlags.effort,
		WorkDir: targetRepo,
		Timeout: findFlags.timeout,
		Script:  agentScript,
	}

	promptBody, err := os.ReadFile(filepath.Join(projectDir, cfg.Instructions.Find))
	if err != nil {
		return nil, fmt.Errorf("read find prompt %s: %w", cfg.Instructions.Find, err)
	}
	rp, err := run.CreateForFind(run.CreateFindOpts{
		ProjectDir:     projectDir,
		Slug:           findFlags.name,
		TargetRepo:     targetRepo,
		Include:        include,
		Exclude:        exclude,
		FindPrompt:     string(promptBody),
		FindSourcePath: cfg.Instructions.Find,
		FindSpec:       spec,
		Args: map[string]any{
			"concurrency": findFlags.concurrency,
			"limit":       findFlags.limit,
			"timeout":     findFlags.timeout.String(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	return &findInputs{
		rp:         rp,
		targetRepo: targetRepo,
		include:    include,
		exclude:    exclude,
		spec:       spec,
	}, nil
}

// analyzeOne runs the agent against a single file. The agent writes
// findings by shelling out to `fettle finding add`, which appends to
// findings.jsonl under flock(2). The harness derives the per-file
// ledger row from the count of this file's findings before/after the
// agent ran.
func analyzeOne(ctx context.Context, rp *run.Path, spec agent.Spec, promptBody, repoRoot, absFile, relFile string) schema.FileStatus {
	started := time.Now().UTC()
	hash := run.FileHash(relFile)
	logPath := filepath.Join(rp.RawDir(), hash+".log")

	prompt, err := renderFindPrompt(findPromptVars{
		TargetFile:       absFile,
		RepoRoot:         repoRoot,
		UserInstructions: promptBody,
	})
	if err != nil {
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError,
			Error: "render find prompt: " + err.Error(),
			Started: started, Ended: time.Now().UTC(),
		}
	}

	stageSpec := spec
	// AddDirs covers codex's sandbox: the spawned `fettle finding add`
	// subprocess writes findings.jsonl in the run directory.
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

	before, countErr := rp.CountFindingsForFile(relFile)
	if countErr != nil {
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError,
			Error:   "count findings before agent: " + countErr.Error(),
			Started: started, Ended: time.Now().UTC(),
		}
	}
	res, err := agent.Run(ctx, stageSpec, prompt)
	if res != nil {
		// Best-effort log capture — the structured findings live in
		// findings.jsonl; this file is for post-mortem debugging only.
		_ = os.WriteFile(logPath, res.Output, 0o644)
	}
	ended := time.Now().UTC()
	after, countErr := rp.CountFindingsForFile(relFile)
	if countErr != nil {
		// findings may have been committed by the agent; we just can't
		// count. Surface as error so the file retries; random ids mean
		// the retry produces additional rows that humans dismiss.
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError,
			Error:   "count findings after agent: " + countErr.Error(),
			Started: started, Ended: ended,
		}
	}
	delta := after - before

	if err != nil {
		// Even if the agent partially appended findings before failing,
		// status is `error` — resume re-runs the file. Random ids mean
		// retries surface as additional rows that humans can dismiss in
		// the UI; we don't try to roll back partial commits.
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError, Error: err.Error(),
			FindingCount: delta, Started: started, Ended: ended,
		}
	}
	status := schema.StatusOK
	if delta == 0 {
		status = schema.StatusEmpty
	}
	return schema.FileStatus{
		File: relFile, Status: status, FindingCount: delta,
		Started: started, Ended: ended,
	}
}

// renderFindPrompt fills the embedded find frame template with the
// per-file values the agent needs and the user's instructions verbatim.
func renderFindPrompt(vars findPromptVars) (string, error) {
	var b strings.Builder
	if err := findPromptTmpl.Execute(&b, vars); err != nil {
		return "", err
	}
	return b.String(), nil
}

func pendingFiles(absFiles []string, repoRoot string, done map[string]bool) []string {
	pending := make([]string, 0, len(absFiles))
	for _, f := range absFiles {
		rel, err := filepath.Rel(repoRoot, f)
		if err != nil {
			// Path can't be made relative (cross-volume on Windows,
			// for example). Keep it in the pending list so the
			// per-file goroutine surfaces the error explicitly via
			// FileStatus rather than silently dropping coverage.
			pending = append(pending, f)
			continue
		}
		rel = filepath.ToSlash(rel)
		if done[rel] {
			continue
		}
		pending = append(pending, f)
	}
	return pending
}

