package main

import (
	"bufio"
	"bytes"
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
	"sort"
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
	timeout     time.Duration
}

var runReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Run the review agent on every finding or group of an existing run",
	Long: `review iterates the subjects of --run, invoking the configured
agent on each subject not yet reviewed by this agent. The agent
appends review entries via ` + "`fettle add review`" + `; one entry per
subject (or zero — review is optional per subject).

Stage → subject mapping:
- find / merge / dedupe runs → iterate findings; rubric is
  ` + "`instructions/review.md`" + `.
- group runs → iterate groups; rubric is
  ` + "`instructions/review_group.md`" + ` and the agent additionally
  receives the cluster's member findings + their existing reviews
  (snapshotted at group-creation time).

On first invocation in --run, the active rubric is snapshotted into
runs/<run>/instructions/<file> (review.md or review_group.md
depending on stage). Subsequent invocations re-use the snapshot.

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

	switch in.manifest.Stage {
	case "find", "merge", "dedupe", "group":
	default:
		return fmt.Errorf("unsupported run stage %q for review", in.manifest.Stage)
	}

	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}

	// Stage selects which user rubric to snapshot + which filename
	// it lands under inside the run folder.
	var srcRel, snapName string
	switch in.manifest.Stage {
	case "find", "merge", "dedupe":
		srcRel = cfg.Instructions.Review
		snapName = "review.md"
	case "group":
		srcRel = cfg.Instructions.ReviewGroup
		snapName = "review_group.md"
	}
	snapPath := filepath.Join(in.rp.Dir(), "instructions", snapName)
	srcAbs := srcRel
	if srcAbs != "" && !filepath.IsAbs(srcAbs) {
		srcAbs = filepath.Join(dir, srcRel)
	}
	if err := snapshotReviewPrompt(srcAbs, snapPath); err != nil {
		if in.manifest.Stage == "group" && srcRel == "" {
			return fmt.Errorf("instructions.review_group is not set in .fettle.json — add `\"review_group\": \"instructions/review_group.md\"` to the instructions block and create the file (a starter is in internal/project/stubs/review_group.md)")
		}
		return fmt.Errorf("snapshot review prompt: %w", err)
	}
	promptBody, err := os.ReadFile(snapPath)
	if err != nil {
		return fmt.Errorf("read snapshotted review prompt: %w", err)
	}

	done, err := loadReviewedSubjects(in.rp.Dir(), in.author)
	if err != nil {
		return fmt.Errorf("load existing reviews: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch in.manifest.Stage {
	case "find", "merge", "dedupe":
		return reviewFindings(ctx, logger, in, string(promptBody), done)
	case "group":
		return reviewGroups(ctx, logger, dir, in, string(promptBody), done)
	}
	return nil
}

// reviewFindings is the find / merge / dedupe path: iterate findings,
// invoke the agent per pending finding.
func reviewFindings(ctx context.Context, logger *slog.Logger, in *reviewInputs, promptBody string, done map[string]bool) error {
	findings, err := loadFindings(in.rp.Dir())
	if err != nil {
		return fmt.Errorf("load findings: %w", err)
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

// reviewGroups is the group path: iterate groups, attach member
// findings + snapshotted member reviews to the prompt, invoke the
// agent per pending group.
func reviewGroups(ctx context.Context, logger *slog.Logger, projectDir string, in *reviewInputs, promptBody string, done map[string]bool) error {
	// Group is single-shot: a partial group run (agent crashed after
	// the snapshot was written but before MarkCompleted) has incomplete
	// groups.jsonl and is not safe to consume — matches FETTLE.md's
	// "delete and re-run" guidance for partial single-shot stages.
	if in.manifest.CompletedAt == nil {
		return fmt.Errorf("group run %s is not completed (run.json missing completed_at) — partial group runs should be deleted and re-run, not reviewed", in.rp.Dir())
	}
	if in.manifest.InputRun == "" {
		return fmt.Errorf("group run %s has empty input_run in run.json — manifest is corrupt", in.rp.Dir())
	}
	inputRunAbs := in.manifest.InputRun
	if !filepath.IsAbs(inputRunAbs) {
		inputRunAbs = filepath.Join(projectDir, in.manifest.InputRun)
	}
	inputRun, err := run.Open(inputRunAbs)
	if err != nil {
		return fmt.Errorf("open input run %s: %w", in.manifest.InputRun, err)
	}
	inputManifest, err := inputRun.Manifest()
	if err != nil {
		return fmt.Errorf("read input run manifest %s: %w", in.manifest.InputRun, err)
	}
	if inputManifest.CompletedAt == nil {
		return fmt.Errorf("input run %s is not completed (run.json missing completed_at)", in.manifest.InputRun)
	}

	inputFindings, err := loadFindingsFromRun(inputRunAbs)
	if err != nil {
		return fmt.Errorf("load input findings from %s: %w", in.manifest.InputRun, err)
	}
	findingsByID := make(map[string]schema.Finding, len(inputFindings))
	for _, f := range inputFindings {
		findingsByID[f.ID] = f
	}

	snapshot, err := loadMemberReviewsSnapshot(in.rp.Dir())
	if err != nil {
		return err
	}

	groups, err := loadGroupsFromRun(in.rp.Dir())
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}

	pending := make([]schema.Group, 0, len(groups))
	for _, g := range groups {
		if !done[g.ID] {
			pending = append(pending, g)
		}
	}
	if runReviewFlags.limit > 0 && len(pending) > runReviewFlags.limit {
		pending = pending[:runReviewFlags.limit]
	}

	logger.Info("plan",
		"run", in.rp.Dir(),
		"author", in.author,
		"groups", len(groups),
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

	for i, g := range pending {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, g schema.Group) {
			defer wg.Done()
			defer func() { <-sem }()

			gLogger := logger.With("group", g.ID, "n", i+1, "total", len(pending))
			gLogger.Info("reviewing")

			err := reviewOneGroup(ctx, in.rp, in.spec, promptBody, in.spec.WorkDir, g, findingsByID, snapshot)
			if err != nil {
				fail.Add(1)
				gLogger.Warn("failed", "error", err)
				return
			}
			ok.Add(1)
			gLogger.Info("done")
		}(i, g)
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

	// Find runs carry target_repo directly. Dedupe / merge runs don't
	// store it on their manifest; resolve by walking the input chain
	// (resolveRepoRoot lives in run_group.go and handles the recursion).
	targetRepo := manifest.TargetRepo
	if targetRepo == "" {
		resolved, rrErr := resolveRepoRoot(projectDir, manifest)
		if rrErr != nil {
			return nil, fmt.Errorf("resolve target_repo for review: %w", rrErr)
		}
		targetRepo = resolved
	}

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
		return fmt.Errorf("source rubric path is empty")
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

// loadMemberReviewsSnapshot reads runs/<group>/member_reviews_snapshot.json
// into a map keyed by finding id with raw-JSON values. Group review
// uses this verbatim (subsetted per group) rather than re-reading the
// live input run's reviews — that's what guarantees byte-for-byte
// stability against the view the grouping agent saw.
//
// A group run created before this feature won't have the snapshot;
// we surface a clear error pointing at the cause.
func loadMemberReviewsSnapshot(runDir string) (map[string]json.RawMessage, error) {
	path := filepath.Join(runDir, "member_reviews_snapshot.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("member_reviews_snapshot.json missing in %s — group review requires this file. Re-create the group run with the current fettle version (older runs predate the snapshot)", runDir)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// The snapshot is authoritative — empty file, JSON null, and any
	// non-object root are corruption, not "no reviews". An honestly
	// empty review state is written as `{}` by run_group.go.
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%s is empty — snapshot is corrupt; re-create the group run", path)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s is JSON null — snapshot is corrupt; re-create the group run", path)
	}
	return m, nil
}

// memberReviewsSubsetJSON returns a JSON object with only the entries
// whose key is in memberIDs. Member ids missing from the snapshot are
// silently skipped (the snapshot is "the input reviews that existed
// at group-creation time" — a member finding with no review then
// legitimately has no entry now). Stable id ordering keeps prompt
// diffs deterministic.
func memberReviewsSubsetJSON(snapshot map[string]json.RawMessage, memberIDs []string) string {
	sub := make(map[string]json.RawMessage, len(memberIDs))
	for _, id := range memberIDs {
		if entry, ok := snapshot[id]; ok {
			sub[id] = entry
		}
	}
	if len(sub) == 0 {
		return "{}"
	}
	ids := make([]string, 0, len(sub))
	for id := range sub {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("{\n")
	for i, id := range ids {
		idJSON, _ := json.Marshal(id)
		b.WriteString("  ")
		b.Write(idJSON)
		b.WriteString(": ")
		b.Write(sub[id])
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.String()
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

// reviewOneGroup invokes the agent against a single group. Looks up
// the group's member findings in findingsByID; missing ids fail the
// group with a clear error (partial member context is a correctness
// risk for a cluster-level verdict). Member-review context comes
// from the group run's snapshotted view, never re-read live.
func reviewOneGroup(ctx context.Context, rp *run.Path, spec agent.Spec, promptBody, repoRoot string, g schema.Group, findingsByID map[string]schema.Finding, snapshot map[string]json.RawMessage) error {
	logPath := filepath.Join(rp.RawDir(), "review_"+g.ID+".log")

	// fettle add group rejects creation of empty groups, so a
	// zero-finding_ids row in groups.jsonl is corrupt and should
	// not be reviewed with empty Members context.
	if len(g.FindingIDs) == 0 {
		return fmt.Errorf("group %s: finding_ids is empty — corrupt group record", g.ID)
	}

	members := make([]schema.Finding, 0, len(g.FindingIDs))
	missing := []string{}
	for _, id := range g.FindingIDs {
		f, ok := findingsByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		members = append(members, f)
	}
	if len(missing) > 0 {
		return fmt.Errorf("group %s: member finding(s) not found in input run: %s", g.ID, strings.Join(missing, ", "))
	}

	subjectJSON, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}
	membersJSON, err := json.MarshalIndent(members, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal members: %w", err)
	}
	memberReviewsJSON := memberReviewsSubsetJSON(snapshot, g.FindingIDs)

	prompt, err := renderReviewPrompt(reviewPromptVars{
		SubjectKind:       schema.SubjectGroup,
		SubjectID:         g.ID,
		SubjectJSON:       string(subjectJSON),
		MembersJSON:       string(membersJSON),
		MemberReviewsJSON: memberReviewsJSON,
		RepoRoot:          repoRoot,
		UserInstructions:  promptBody,
	})
	if err != nil {
		return fmt.Errorf("render review prompt: %w", err)
	}
	if err := writePromptSidecar(rp, g.ID, prompt); err != nil {
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
