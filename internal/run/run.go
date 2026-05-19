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
	// streamsMu protects streams; see append.go for the lazy-open
	// scheme that backs Append{Finding,Review,Outcome}Entry.
	streamsMu sync.Mutex
	streams   map[streamKey]*stream
}

// Dir returns the absolute path of the run folder.
func (p *Path) Dir() string { return p.dir }

// Slug returns the 6-char hex identifier embedded in the run
// folder name (the `<slug>` in `run_<slug>_<ts>`). Used to key
// artifact filenames and to reference the run from the CLI by a
// short form (`--run <slug>`).
//
// Returns empty string for runs whose folder name doesn't match
// the canonical format — defensive against hand-renames and
// pre-canonical run folders.
func (p *Path) Slug() string {
	slug, _, ok := ParseRunName(filepath.Base(p.dir))
	if !ok {
		return ""
	}
	return slug
}

// StartTime returns the run-start timestamp string embedded in the
// run folder name (the `<ts>` in `run_<slug>_<ts>`). Same caveat as
// Slug for non-canonical folder names.
func (p *Path) StartTime() string {
	_, ts, ok := ParseRunName(filepath.Base(p.dir))
	if !ok {
		return ""
	}
	return ts
}

// CreateFindOpts configures Create when the first stage is `find`.
type CreateFindOpts struct {
	ProjectDir     string
	Slug           string
	TargetRepo     string
	Walker         string // "git" | "fs"; baked into the manifest so resume is honest
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
	name, err := generateName(opts.Slug)
	if err != nil {
		return nil, err
	}
	runsDir := project.RunsDir(opts.ProjectDir)
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
		Walker:        opts.Walker,
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

// RunSlugLen is the byte length of the random hex slug embedded in
// each run dir name. Six characters of hex give 16.7M possibilities,
// far above the collision rate any single project would ever hit,
// and short enough to be readable as a reference (`fettle list
// reviews --run 3cdf6f`).
const RunSlugLen = 6

// generateName builds a run folder name like
// `run_<slug>_20260430T145233Z`. The slug must match slugRegex;
// when empty, a random hex string of RunSlugLen characters is used.
// The slug appears before the timestamp so the eye lands on the
// identity-bearing part first when scanning a directory listing.
//
// Stage isn't encoded in the directory name — it lives only in
// run.json's `stage` field. Runs are uniform on disk regardless of
// what kind of work they hold.
func generateName(slug string) (string, error) {
	if err := validateSlug(slug); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	if slug == "" {
		// RunSlugLen hex chars come from RunSlugLen/2 random bytes.
		// Round up for an odd RunSlugLen so we have at least the
		// requested length.
		bytes := (RunSlugLen + 1) / 2
		b := make([]byte, bytes)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("random slug: %w", err)
		}
		slug = hex.EncodeToString(b)[:RunSlugLen]
	}
	return fmt.Sprintf("run_%s_%s", slug, ts), nil
}

// runNameRe parses `run_<slug>_<ts>` into its slug and timestamp
// pieces. Both are captured into named groups for clear field
// extraction by callers; the timestamp matches the same compact
// basic-ISO form generateName emits.
var runNameRe = regexp.MustCompile(`^run_(?P<slug>[A-Za-z0-9-]+)_(?P<ts>\d{8}T\d{6}Z)$`)

// ParseRunName splits a run folder name into its slug and start
// timestamp. Returns ok=false when the name doesn't match the run
// folder format — callers iterating a project's runs/ directory
// can skip unrelated entries silently.
func ParseRunName(name string) (slug, ts string, ok bool) {
	m := runNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[runNameRe.SubexpIndex("slug")], m[runNameRe.SubexpIndex("ts")], true
}

// slugRegex is the shared validity check for run slugs and finding ids.
// Both flow into filesystem paths (run folders and the JSONL artifact
// streams under each run), so the same character class keeps both
// filename-safe.
//
// Underscore is excluded on purpose: the artifact filename format uses
// `_` as its field separator (`<kind>_<datetime>_<human>[_<agent>].jsonl`),
// so a slug containing `_` would break `ParseArtifactFilename`.
var slugRegex = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// validateSlug returns an error if a non-empty run-name slug doesn't
// match slugRegex. Empty is allowed — generateName picks a random
// suffix in that case.
func validateSlug(s string) error {
	if s == "" {
		return nil
	}
	if !slugRegex.MatchString(s) {
		return fmt.Errorf("invalid run slug %q: only [A-Za-z0-9-] allowed", s)
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
