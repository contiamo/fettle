// Package run owns the run folder under runs/<name>/: creation,
// manifest, append-only writers for findings.jsonl and files.jsonl, and
// resume-state loading.
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
	"sync"
	"time"

	"github.com/contiamo/fettle/internal/agent"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/schema"
)

// Path is a handle to a run folder. Methods are safe for concurrent use
// across goroutines and (for findings.jsonl) across processes — the
// findings append uses flock(2). files.jsonl is harness-only, so it
// uses an in-process mutex.
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
	FindPrompt     string         // full text to snapshot
	FindSourcePath string         // project-relative path of the source prompt
	FindSpec       agent.Spec
	Args           map[string]any // CLI args worth recording in the manifest
}

// CreateForFind initializes a new run folder under projectDir/runs/ with
// the find prompt snapshotted and run.json populated.
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
	for _, sub := range []string{"instructions", "raw"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	snap := filepath.Join("instructions", "find.md")
	if err := os.WriteFile(filepath.Join(dir, snap), []byte(opts.FindPrompt), 0o644); err != nil {
		return nil, fmt.Errorf("snapshot find prompt: %w", err)
	}
	for _, f := range []string{"findings.jsonl", "files.jsonl"} {
		if err := touch(filepath.Join(dir, f)); err != nil {
			return nil, err
		}
	}
	manifest := schema.RunManifest{
		Name:          name,
		FettleVersion: project.Version,
		CreatedAt:     time.Now().UTC(),
		TargetRepo:    opts.TargetRepo,
		TargetRepoGit: gitInfo(opts.TargetRepo),
		Include:       opts.Include,
		Exclude:       opts.Exclude,
		Stages: map[string]schema.StageEntry{
			"find": {
				Agent:        opts.FindSpec.Name,
				Model:        opts.FindSpec.Model,
				Effort:       opts.FindSpec.Effort,
				Command:      opts.FindSpec.Command,
				Args:         opts.FindSpec.Args,
				SourcePath:   opts.FindSourcePath,
				SnapshotPath: snap,
			},
		},
		Args: opts.Args,
	}
	if err := writeManifest(dir, manifest); err != nil {
		return nil, err
	}
	return &Path{dir: dir}, nil
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

// AppendFinding appends one finding to findings.jsonl. Safe across
// goroutines and across processes — the underlying append uses flock(2)
// on the data file, so the agent-spawned `fettle finding add` and the
// harness's own writers serialize through the kernel.
func (p *Path) AppendFinding(f schema.Finding) error {
	line, err := json.Marshal(f)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	return appendWithLock(filepath.Join(p.dir, "findings.jsonl"), line)
}

// AppendFileStatus appends one row to files.jsonl. Concurrent-safe.
func (p *Path) AppendFileStatus(s schema.FileStatus) error {
	p.filesMu.Lock()
	defer p.filesMu.Unlock()
	return appendJSONL(filepath.Join(p.dir, "files.jsonl"), s)
}

// CountFindingsForFile scans findings.jsonl and returns how many rows
// have file == f. Used by the find harness to derive a per-file ledger
// row from the delta of "before vs after the agent ran". Tolerates
// malformed lines (skips them) so a partial-write under SIGKILL doesn't
// poison the count.
func (p *Path) CountFindingsForFile(f string) (int, error) {
	fh, err := os.Open(filepath.Join(p.dir, "findings.jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer fh.Close()

	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var row struct {
		File string `json:"file"`
	}
	count := 0
	for sc.Scan() {
		row.File = ""
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		if row.File == f {
			count++
		}
	}
	return count, sc.Err()
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
	sc.Buffer(make([]byte, 1<<16), 1<<20)
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
	// Cheap, stable, doesn't need to be cryptographic — sha256 first 16
	// hex chars matches the convention from the prior fettle codebase.
	h := sha256ish(repoRelPath)
	return h
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

func validateSlug(s string) error {
	if s == "" {
		return nil
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' {
			continue
		}
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
