// Package run owns the run folder under runs/<name>/: creation,
// manifest, and resume-state loading. Per-finding-doc storage lives in
// findings.go; review aggregation across docs lives in reviews.go.
package run

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/contiamo/fettle/internal/agent"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/schema"
)

// findingsSubdir is the per-run directory holding one JSON file per
// finding. Centralised so callers don't open-code the literal.
const findingsSubdir = "findings"

// jsonlScanInitBuf and jsonlScanMaxLine size the bufio.Scanner buffer
// `LoadDoneFiles` shares with the rest of the package — files.jsonl is
// the last remaining JSONL stream after the per-finding-doc migration.
// 64 KiB initial / 1 MiB max comfortably fits the longest FileStatus
// row observed in practice.
const (
	jsonlScanInitBuf = 1 << 16
	jsonlScanMaxLine = 1 << 20
)

// Path is a handle to a run folder. Methods are safe for concurrent use
// across goroutines; cross-process safety on per-finding writes is
// covered by atomic rename, not flock — see UpdateFinding's godoc for
// the accepted race window. files.jsonl is harness-only and uses an
// in-process mutex.
type Path struct {
	dir        string
	filesMu    sync.Mutex
	manifestMu sync.Mutex
}

// Dir returns the absolute path of the run folder.
func (p *Path) Dir() string { return p.dir }

// CreateFindOpts configures Create when the first stage is `find`.
type CreateFindOpts struct {
	ProjectDir     string
	Slug           string
	TargetRepo     string
	Include        []string
	Exclude        []string
	FindPrompt     string // full text to snapshot
	FindSourcePath string // project-relative path of the source prompt
	FindSpec       agent.Spec
	Args           map[string]any // CLI args worth recording in the manifest
}

// CreateForFind initializes a new run folder under projectDir/runs/ with
// the find prompt snapshotted, run.json populated, and the empty
// findings/ directory ready for per-finding writes.
func CreateForFind(opts CreateFindOpts) (*Path, error) {
	name, err := generateName("find", opts.Slug)
	if err != nil {
		return nil, err
	}
	runsDir := filepath.Join(opts.ProjectDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create runs/: %w", err)
	}
	dir := filepath.Join(runsDir, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	for _, sub := range []string{"instructions", "raw", findingsSubdir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	snap := filepath.Join("instructions", "find.md")
	if err := os.WriteFile(filepath.Join(dir, snap), []byte(opts.FindPrompt), 0o644); err != nil {
		return nil, fmt.Errorf("snapshot find prompt: %w", err)
	}
	if err := touch(filepath.Join(dir, "files.jsonl")); err != nil {
		return nil, err
	}
	manifest := schema.RunManifest{
		Name:          name,
		Stage:         "find",
		FettleVersion: project.Version,
		CreatedAt:     time.Now().UTC(),
		TargetRepo:    opts.TargetRepo,
		TargetRepoGit: ReadGit(opts.TargetRepo),
		Include:       opts.Include,
		Exclude:       opts.Exclude,
		Agent:         agentInfoFromSpec(opts.FindSpec),
		SourcePath:    opts.FindSourcePath,
		SnapshotPath:  snap,
		Args:          opts.Args,
	}
	if err := writeManifest(dir, manifest); err != nil {
		return nil, err
	}
	return &Path{dir: dir}, nil
}

// MarkCompleted stamps run.json's completed_at field. Stages call this
// once the work is fully written, signalling to downstream consumers
// that the run is trustworthy.
func (p *Path) MarkCompleted() error {
	p.manifestMu.Lock()
	defer p.manifestMu.Unlock()
	m, err := readManifest(p.dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	m.CompletedAt = &now
	return writeManifest(p.dir, m)
}

// Open opens an existing run folder.
func Open(dir string) (*Path, error) {
	if _, err := os.Stat(filepath.Join(dir, "run.json")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("not a run folder: %s (no run.json)", dir)
		}
		return nil, err
	}
	return &Path{dir: dir}, nil
}

// Manifest reads run.json.
func (p *Path) Manifest() (schema.RunManifest, error) {
	p.manifestMu.Lock()
	defer p.manifestMu.Unlock()
	return readManifest(p.dir)
}

// AppendFileStatus appends one row to files.jsonl. Concurrent-safe
// within a process via filesMu; cross-process this is harness-only so
// no flock.
func (p *Path) AppendFileStatus(s schema.FileStatus) error {
	p.filesMu.Lock()
	defer p.filesMu.Unlock()
	return appendJSONL(filepath.Join(p.dir, "files.jsonl"), s)
}

// LoadDoneFiles returns the set of repo-relative paths whose latest entry
// in files.jsonl is `ok` or `empty`. `error` rows are excluded so they
// retry on resume.
func (p *Path) LoadDoneFiles() (map[string]bool, error) {
	f, err := os.Open(filepath.Join(p.dir, "files.jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	defer f.Close()

	latest := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, jsonlScanInitBuf), jsonlScanMaxLine)
	for sc.Scan() {
		var row schema.FileStatus
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		latest[row.File] = row.Status
	}
	done := map[string]bool{}
	for file, status := range latest {
		if status == schema.StatusOK || status == schema.StatusEmpty {
			done[file] = true
		}
	}
	return done, sc.Err()
}

// RawDir returns the absolute path of the run's raw/ directory.
func (p *Path) RawDir() string {
	return filepath.Join(p.dir, "raw")
}

// FileHash returns the stable per-file slug used for raw/ output paths.
func FileHash(repoRelPath string) string {
	return sha256ish(repoRelPath)
}

// agentInfoFromSpec maps an agent.Spec onto the manifest's AgentInfo
// shape. Used by every Create* helper that runs an agent.
func agentInfoFromSpec(s agent.Spec) *schema.AgentInfo {
	return &schema.AgentInfo{
		Name:   s.Name,
		Model:  s.Model,
		Effort: s.Effort,
		Script: s.Script,
	}
}

// generateName builds a run folder name like
// `find_20260430T145233Z_<slug>`. The slug must match [A-Za-z0-9_-]+;
// when empty, a 4-byte random hex suffix is used.
func generateName(stage, slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	if slug == "" {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("random slug: %w", err)
		}
		slug = hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%s_%s_%s", stage, ts, slug), nil
}

// slugRegex is the shared validity check for run slugs and finding ids.
// Both flow into filesystem paths (run folders and findings/<id>.json),
// so the same character class keeps both filename-safe.
var slugRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateSlug returns an error if a non-empty run-name slug doesn't
// match slugRegex. Empty is allowed — generateName picks a random
// suffix in that case.
func validateSlug(s string) error {
	if s == "" {
		return nil
	}
	if !slugRegex.MatchString(s) {
		return fmt.Errorf("invalid run slug %q: only [A-Za-z0-9_-] allowed", s)
	}
	return nil
}

func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("touch %s: %w", path, err)
	}
	return f.Close()
}

func appendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(v)
}

func writeManifest(dir string, m schema.RunManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "run.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "run.json"))
}

func readManifest(dir string) (schema.RunManifest, error) {
	var m schema.RunManifest
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(data, &m)
}
