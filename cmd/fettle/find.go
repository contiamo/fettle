package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/contiamo/fettle/internal/agent"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/walk"
	"github.com/spf13/cobra"
)

const defaultFindTimeout = 10 * time.Minute

var findFlags struct {
	name        string
	resume      string
	concurrency int
	limit       int
	include     []string
	exclude     []string
	effort      string
	timeout     time.Duration
}

var findCmd = &cobra.Command{
	Use:   "find",
	Short: "Run the find agent on every matching file in the target repo",
	Long: `find creates a new run folder under runs/ (or resumes one with
--resume), snapshots the find prompt into it, walks the target repo,
and runs the configured agent on every file that matches the project's
include/exclude globs. Findings are appended to findings.jsonl; per-file
status is appended to files.jsonl for resume.`,
	RunE: runFind,
}

func init() {
	findCmd.Flags().StringVar(&findFlags.name, "name", "", "human label appended to the run folder timestamp (default: random hex)")
	findCmd.Flags().StringVar(&findFlags.resume, "resume", "", "path to an existing run folder to resume")
	findCmd.Flags().IntVarP(&findFlags.concurrency, "concurrency", "c", 4, "max concurrent agent invocations")
	findCmd.Flags().IntVar(&findFlags.limit, "limit", 0, "scan at most N files (0 = all)")
	findCmd.Flags().StringSliceVar(&findFlags.include, "include", nil, "include globs (overrides project config)")
	findCmd.Flags().StringSliceVar(&findFlags.exclude, "exclude", nil, "exclude globs (overrides project config)")
	findCmd.Flags().StringVar(&findFlags.effort, "effort", "", "codex reasoning effort (low|medium|high|xhigh|max); ignored for other agents")
	findCmd.Flags().DurationVar(&findFlags.timeout, "timeout", defaultFindTimeout, "per-file agent timeout")
	rootCmd.AddCommand(findCmd)
}

func runFind(cmd *cobra.Command, args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	projectDir, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := project.Load(projectDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no .fettle.json in %s — run `fettle init` first", projectDir)
		}
		return fmt.Errorf("load project: %w", err)
	}

	include := cfg.Include
	if len(findFlags.include) > 0 {
		include = findFlags.include
	}
	exclude := cfg.Exclude
	if len(findFlags.exclude) > 0 {
		exclude = findFlags.exclude
	}

	files, err := walk.Walk(cfg.TargetRepo, include, exclude)
	if err != nil {
		return fmt.Errorf("walk target repo: %w", err)
	}

	spec := agent.Spec{
		Name:    cfg.Agent.Name,
		Model:   cfg.Agent.Model,
		Effort:  findFlags.effort,
		WorkDir: cfg.TargetRepo,
		Timeout: findFlags.timeout,
	}

	rp, err := openOrCreateRun(projectDir, cfg, spec, include, exclude)
	if err != nil {
		return err
	}
	manifest, _ := rp.Manifest()
	rp.AddDirsForRun(manifest)

	done, err := rp.LoadDoneFiles()
	if err != nil {
		return fmt.Errorf("load resume state: %w", err)
	}
	pending := pendingFiles(files, cfg.TargetRepo, done)
	if findFlags.limit > 0 && len(pending) > findFlags.limit {
		pending = pending[:findFlags.limit]
	}

	logger.Info("plan",
		"run", rp.Dir(),
		"discovered", len(files),
		"already_done", len(done),
		"pending", len(pending),
		"concurrency", findFlags.concurrency,
		"agent", spec.Name,
		"model", spec.Model,
	)
	if len(pending) == 0 {
		fmt.Println(rp.Dir())
		return nil
	}

	// Re-read the snapshotted find prompt so resume always uses the run's
	// frozen copy, not the editable template.
	promptBody, err := os.ReadFile(filepath.Join(rp.Dir(), "instructions", "find.md"))
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

			rel, _ := filepath.Rel(cfg.TargetRepo, file)
			rel = filepath.ToSlash(rel)
			fileLogger := logger.With("file", rel, "n", i+1, "total", len(pending))
			fileLogger.Info("analyzing")

			res := analyzeOne(ctx, rp, spec, string(promptBody), cfg.TargetRepo, file, rel)
			if appendErr := rp.AppendFileStatus(res.status); appendErr != nil {
				fileLogger.Error("write file status", "error", appendErr)
			}

			switch res.status.Status {
			case schema.StatusOK:
				ok.Add(1)
				total.Add(int64(res.status.FindingCount))
				fileLogger.Info("done", "findings", res.status.FindingCount, "duration", res.status.Ended.Sub(res.status.Started).Round(time.Second))
			case schema.StatusEmpty:
				empty.Add(1)
				fileLogger.Info("done (empty)", "duration", res.status.Ended.Sub(res.status.Started).Round(time.Second))
			case schema.StatusError:
				fail.Add(1)
				fileLogger.Warn("failed", "error", res.status.Error, "duration", res.status.Ended.Sub(res.status.Started).Round(time.Second))
			}
		}(i, file)
	}
	wg.Wait()

	logger.Info("complete",
		"run", rp.Dir(),
		"with_findings", ok.Load(),
		"empty", empty.Load(),
		"failed", fail.Load(),
		"total_findings", total.Load(),
		"elapsed", time.Since(startAll).Round(time.Second),
	)
	fmt.Println(rp.Dir())
	return nil
}

// openOrCreateRun returns the run folder, creating it if --resume wasn't passed.
func openOrCreateRun(projectDir string, cfg project.Config, spec agent.Spec, include, exclude []string) (*runPath, error) {
	if findFlags.resume != "" {
		abs := findFlags.resume
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, abs)
		}
		rp, err := run.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open run: %w", err)
		}
		return &runPath{Path: rp, projectDir: projectDir}, nil
	}

	promptPath := filepath.Join(projectDir, cfg.Instructions.Find)
	body, err := os.ReadFile(promptPath)
	if err != nil {
		return nil, fmt.Errorf("read find prompt %s: %w", cfg.Instructions.Find, err)
	}
	rp, err := run.CreateForFind(run.CreateFindOpts{
		ProjectDir:     projectDir,
		Slug:           findFlags.name,
		TargetRepo:     cfg.TargetRepo,
		Include:        include,
		Exclude:        exclude,
		FindPrompt:     string(body),
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
	return &runPath{Path: rp, projectDir: projectDir}, nil
}

// runPath wraps run.Path with project-aware sandbox config.
type runPath struct {
	*run.Path
	projectDir string
}

// AddDirsForRun returns the dirs the agent must be allowed to write to.
// Currently just the run's raw/ — that's where per-file findings land.
func (rp *runPath) AddDirsForRun(_ schema.RunManifest) []string {
	return []string{rp.RawDir()}
}

// analysisResult holds what one file analysis produced.
type analysisResult struct {
	status schema.FileStatus
}

// analyzeOne runs the agent against a single file, parses the resulting
// JSONL, and appends each finding. Returns a FileStatus for files.jsonl.
func analyzeOne(ctx context.Context, rp *runPath, spec agent.Spec, promptBody, repoRoot, absFile, relFile string) analysisResult {
	started := time.Now().UTC()
	hash := run.FileHash(relFile)
	outputPath := filepath.Join(rp.RawDir(), hash+".jsonl")
	logPath := filepath.Join(rp.RawDir(), hash+".log")
	_ = os.Remove(outputPath)

	prompt := buildFindPrompt(promptBody, map[string]string{
		"TARGET_FILE": absFile,
		"OUTPUT_PATH": outputPath,
		"REPO_ROOT":   repoRoot,
	})

	stageSpec := spec
	stageSpec.AddDirs = []string{rp.RawDir()}

	res, err := agent.Run(ctx, stageSpec, prompt)
	if res != nil {
		_ = os.WriteFile(logPath, res.Output, 0o644)
	}
	ended := time.Now().UTC()
	if err != nil {
		return analysisResult{status: schema.FileStatus{
			File: relFile, Status: schema.StatusError, Error: err.Error(),
			Started: started, Ended: ended,
		}}
	}

	findings, parseErr := readFindings(outputPath, relFile, started, fmt.Sprintf("agent:%s/%s", spec.Name, spec.Model))
	if parseErr != nil {
		return analysisResult{status: schema.FileStatus{
			File: relFile, Status: schema.StatusError, Error: "parse findings: " + parseErr.Error(),
			Started: started, Ended: ended,
		}}
	}
	for _, f := range findings {
		if appendErr := rp.AppendFinding(f); appendErr != nil {
			return analysisResult{status: schema.FileStatus{
				File: relFile, Status: schema.StatusError, Error: "append finding: " + appendErr.Error(),
				Started: started, Ended: ended,
			}}
		}
	}

	status := schema.StatusOK
	if len(findings) == 0 {
		status = schema.StatusEmpty
	}
	return analysisResult{status: schema.FileStatus{
		File: relFile, Status: status, FindingCount: len(findings),
		Started: started, Ended: ended,
	}}
}

// buildFindPrompt prepends a key=value header so the agent reads the
// substituted variables at the top of its prompt without us mangling the
// template body.
func buildFindPrompt(body string, vars map[string]string) string {
	var b strings.Builder
	keys := sortedKeys(vars)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(vars[k])
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(body)
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// small map, simple sort
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// pendingFiles returns absolute paths whose repo-relative form isn't
// already in done.
func pendingFiles(absFiles []string, repoRoot string, done map[string]bool) []string {
	pending := make([]string, 0, len(absFiles))
	for _, f := range absFiles {
		rel, err := filepath.Rel(repoRoot, f)
		if err != nil {
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

// readFindings parses one agent-written JSONL file and stamps each finding
// with stable id, source file, and timestamp. An empty/missing output
// file is a valid "no findings" result.
func readFindings(outputPath, sourceFile string, analyzedAt time.Time, createdBy string) ([]schema.Finding, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []schema.Finding
	for ln, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f schema.Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, fmt.Errorf("line %d: %w", ln+1, err)
		}
		// Always re-derive these — agents shouldn't be trusted with id/timestamps.
		if f.File == "" {
			f.File = sourceFile
		}
		f.ID = schema.FindingID(f.File, f.Line, f.Title)
		f.CreatedBy = createdBy
		f.CreatedAt = analyzedAt
		if f.Labels == nil {
			f.Labels = []string{}
		}
		if f.References == nil {
			f.References = []schema.Reference{}
		}
		out = append(out, f)
	}
	return out, nil
}
