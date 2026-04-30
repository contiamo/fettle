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
	"slices"
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
--resume), snapshots the find prompt into it, walks the target repo, and
runs the configured agent on every file that matches the project's
include/exclude globs. Findings are appended to findings.jsonl; per-file
status is appended to files.jsonl for resume.

On --resume, the run's manifest (run.json) is authoritative — target
repo, include/exclude globs, and agent/model/effort all come from there,
not from .fettle.json. Flags that would change those values are
rejected.`,
	RunE: runFind,
}

func init() {
	findCmd.Flags().StringVar(&findFlags.name, "name", "", "human label appended to the run folder timestamp (default: random hex)")
	findCmd.Flags().StringVar(&findFlags.resume, "resume", "", "path to an existing run folder to resume")
	findCmd.Flags().IntVarP(&findFlags.concurrency, "concurrency", "c", 4, "max concurrent agent invocations")
	findCmd.Flags().IntVar(&findFlags.limit, "limit", 0, "scan at most N files this invocation (0 = all)")
	findCmd.Flags().StringSliceVar(&findFlags.include, "include", nil, "include globs (overrides project config; not allowed with --resume)")
	findCmd.Flags().StringSliceVar(&findFlags.exclude, "exclude", nil, "exclude globs (overrides project config; not allowed with --resume)")
	findCmd.Flags().StringVar(&findFlags.effort, "effort", "", "codex reasoning effort; not allowed with --resume")
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

	spec := agent.Spec{
		Name:    cfg.Agent.Name,
		Model:   cfg.Agent.Model,
		Effort:  findFlags.effort,
		WorkDir: targetRepo,
		Timeout: findFlags.timeout,
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

// analyzeOne runs the agent against a single file, parses the resulting
// JSONL, and appends each finding. Returns a FileStatus for files.jsonl.
func analyzeOne(ctx context.Context, rp *run.Path, spec agent.Spec, promptBody, repoRoot, absFile, relFile string) schema.FileStatus {
	started := time.Now().UTC()
	hash := run.FileHash(relFile)
	outputPath := filepath.Join(rp.RawDir(), hash+".jsonl")
	logPath := filepath.Join(rp.RawDir(), hash+".log")
	// Best-effort: clear any stale output from a prior run of this file
	// before invoking the agent. ENOENT is the expected case on a fresh
	// run; any other error here will surface when we try to read back.
	_ = os.Remove(outputPath)

	prompt := buildFindPrompt(promptBody, map[string]string{
		"TARGET_FILE": absFile,
		"OUTPUT_PATH": outputPath,
		"REPO_ROOT":   repoRoot,
	})

	stageSpec := spec
	stageSpec.AddDirs = []string{rp.RawDir()}
	stageSpec.Env = []string{"FETTLE_RUN=" + rp.Dir()}

	res, err := agent.Run(ctx, stageSpec, prompt)
	if res != nil {
		// Best-effort log capture — the structured findings live in
		// findings.jsonl; this file is for post-mortem debugging only.
		_ = os.WriteFile(logPath, res.Output, 0o644)
	}
	ended := time.Now().UTC()
	if err != nil {
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError, Error: err.Error(),
			Started: started, Ended: ended,
		}
	}

	findings, parseErr := readFindings(outputPath, relFile, started, formatCreatedBy(spec))
	if parseErr != nil {
		return schema.FileStatus{
			File: relFile, Status: schema.StatusError, Error: parseErr.Error(),
			Started: started, Ended: ended,
		}
	}
	for _, f := range findings {
		if appendErr := rp.AppendFinding(f); appendErr != nil {
			return schema.FileStatus{
				File: relFile, Status: schema.StatusError, Error: "append finding: " + appendErr.Error(),
				Started: started, Ended: ended,
			}
		}
	}

	status := schema.StatusOK
	if len(findings) == 0 {
		status = schema.StatusEmpty
	}
	return schema.FileStatus{
		File: relFile, Status: status, FindingCount: len(findings),
		Started: started, Ended: ended,
	}
}

// formatCreatedBy returns "agent:<name>" or "agent:<name>/<model>".
func formatCreatedBy(spec agent.Spec) string {
	if spec.Model == "" {
		return "agent:" + spec.Name
	}
	return "agent:" + spec.Name + "/" + spec.Model
}

// buildFindPrompt prepends a key=value header so the agent reads the
// substituted variables at the top of its prompt without us mangling the
// template body.
func buildFindPrompt(body string, vars map[string]string) string {
	var b strings.Builder
	for _, k := range sortedKeys(vars) {
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
	slices.Sort(out)
	return out
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

// readFindings parses one agent-written JSONL file and stamps each finding
// with stable id, source file, and timestamp. The file MUST exist (even
// if empty) — a missing OUTPUT_PATH means the agent failed to follow the
// contract and we surface it as an error so coverage isn't silently lost.
//
// Bad lines are tolerated: any line that fails to JSON-decode is skipped
// with a slog warning and the rest of the file is salvaged. Only when
// every line fails do we return an error, so a single mis-formatted
// finding doesn't sink the rest of a file's coverage.
func readFindings(outputPath, sourceFile string, analyzedAt time.Time, createdBy string) ([]schema.Finding, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("agent did not write OUTPUT_PATH: %s", outputPath)
		}
		return nil, fmt.Errorf("read findings: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []schema.Finding
	var parseErrs []string
	for ln, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f schema.Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			parseErrs = append(parseErrs, fmt.Sprintf("line %d: %v", ln+1, err))
			continue
		}
		if f.File == "" {
			f.File = sourceFile
		}
		f.ID = schema.NewFindingID()
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
	if len(out) == 0 && len(parseErrs) > 0 {
		return nil, fmt.Errorf("no findings parsed: %s", strings.Join(parseErrs, "; "))
	}
	if len(parseErrs) > 0 {
		slog.Warn("partial parse failures",
			"file", sourceFile,
			"salvaged", len(out),
			"errors", strings.Join(parseErrs, "; "),
		)
	}
	return out, nil
}
