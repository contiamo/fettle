// Package project owns the fettle project directory. A project is any
// directory containing a `fettle.json` at its root; that file is both
// the on-disk config and the marker discovery uses to tell "this is a
// fettle project" from any other folder.
//
// The project dir's name is whatever the user picked at `fettle init`
// time — `fettle init foobar` makes `foobar/` the project root,
// `fettle init .` makes the cwd the project root. Subsequent commands
// find it by upward-walk from cwd, or by the `--project-dir` flag /
// `FETTLE_PROJECT_DIR` env override.
package project

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// stubFS holds the per-stage prompt templates `fettle init` writes
// into the project's instructions/ subdirectory.
//
//go:embed stubs/*.md
var stubFS embed.FS

// Version is the fettle version stamped into fettle.json on init.
const Version = "0.1.0"

// ConfigName is the project's manifest filename. Its presence at the
// root of a directory is what marks that directory as a fettle project.
const ConfigName = "fettle.json"

// ErrNotInProject is returned by FindProjectDir when no fettle.json
// can be found by upward-walking from the starting directory.
var ErrNotInProject = errors.New("not inside a fettle project (no fettle.json found upward from cwd)")

// Walker values for Config.Walker. WalkerGit (default) goes through
// `git ls-files` so `.gitignore` rules are honoured automatically.
// WalkerFS walks the filesystem and only filters by the user's globs.
const (
	WalkerGit = "git"
	WalkerFS  = "fs"
)

// ConfigPath returns the absolute path to a project's fettle.json.
func ConfigPath(dir string) string {
	return filepath.Join(dir, ConfigName)
}

// RunsDir returns the absolute path to a project's runs/ directory.
func RunsDir(dir string) string {
	return filepath.Join(dir, "runs")
}

// InstructionsDir returns the absolute path to a project's
// instructions/ directory.
func InstructionsDir(dir string) string {
	return filepath.Join(dir, "instructions")
}

// FindProjectDir upward-walks from startDir looking for a directory
// containing a fettle.json. Returns the absolute path of the directory
// that holds it, or ErrNotInProject when the walk hits the filesystem
// root without finding one.
//
// Used by every command except `fettle init` itself: init takes its
// target as a positional argument so it can create the project
// wherever the user names; everything else upward-walks like
// `git`/`hg`/`go` to let invocation from any subdirectory just work.
func FindProjectDir(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve start dir: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ConfigName)); err == nil {
			return dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", filepath.Join(dir, ConfigName), err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotInProject
		}
		dir = parent
	}
}

// Config is the on-disk shape of <project>/fettle.json.
type Config struct {
	FettleVersion string   `json:"fettle_version"`
	CreatedAt     time.Time `json:"created_at"`
	TargetRepo    string   `json:"target_repo"`
	Agent         AgentRef `json:"agent"`
	// Walker chooses how files are enumerated in the target repo:
	// WalkerGit (default) honours .gitignore; WalkerFS walks the
	// filesystem and filters only by Include/Exclude.
	Walker       string       `json:"walker"`
	Include      []string     `json:"include"`
	Exclude      []string     `json:"exclude"`
	Instructions Instructions `json:"instructions"`
}

// AgentRef points at the agent fettle invokes for a stage.
type AgentRef struct {
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Script string `json:"script,omitempty"`
}

// Instructions points at the editable prompt templates, relative to
// the project directory. Defaults live under instructions/ at the
// project root but a user is free to point at any path they want —
// fettle reads them verbatim.
type Instructions struct {
	Find   string `json:"find"`
	Review string `json:"review"`
}

// NewConfig assembles the Config that `init` writes. include and
// exclude come from the user's flags — there's no
// project-independent default for include that does the right thing,
// so the caller is responsible for collecting them and rejecting an
// empty include list before reaching this function.
func NewConfig(targetRepo, agent, model, walker string, include, exclude []string) Config {
	if include == nil {
		include = []string{}
	}
	if exclude == nil {
		exclude = []string{}
	}
	if walker == "" {
		walker = WalkerGit
	}
	return Config{
		FettleVersion: Version,
		CreatedAt:     time.Now().UTC(),
		TargetRepo:    targetRepo,
		Agent:         AgentRef{Name: agent, Model: model},
		Walker:        walker,
		Include:       include,
		Exclude:       exclude,
		Instructions: Instructions{
			Find:   filepath.Join("instructions", "find.md"),
			Review: filepath.Join("instructions", "review.md"),
		},
	}
}

// Init writes a fresh fettle project at dir. Caller is responsible
// for the directory existing and being empty (or being safe to add to);
// Init only populates it. Fails if fettle.json already exists.
func Init(dir string, cfg Config) error {
	cfgPath := ConfigPath(dir)
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s already exists", cfgPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	if err := os.MkdirAll(InstructionsDir(dir), 0o755); err != nil {
		return fmt.Errorf("create instructions/: %w", err)
	}
	if err := writeStubs(InstructionsDir(dir)); err != nil {
		return err
	}
	if err := os.MkdirAll(RunsDir(dir), 0o755); err != nil {
		return fmt.Errorf("create runs/: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(cfgPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return nil
}

// Load reads fettle.json from a project's directory.
func Load(dir string) (Config, error) {
	var cfg Config
	cfgPath := ConfigPath(dir)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	return cfg, nil
}

// ResolveTargetRepo returns the target repo as an absolute path,
// treating a relative `target_repo` as relative to projectDir. This
// lets a committed config portably reference a target via "../.."
// (or similar) without baking in machine-specific absolute paths.
func (c Config) ResolveTargetRepo(projectDir string) (string, error) {
	if filepath.IsAbs(c.TargetRepo) {
		return c.TargetRepo, nil
	}
	abs, err := filepath.Abs(filepath.Join(projectDir, c.TargetRepo))
	if err != nil {
		return "", fmt.Errorf("resolve target_repo %q: %w", c.TargetRepo, err)
	}
	return abs, nil
}

// writeStubs copies every embedded stub into dir, skipping any file
// the user has already customized (a previously-init'd project).
func writeStubs(dir string) error {
	entries, err := stubFS.ReadDir("stubs")
	if err != nil {
		return fmt.Errorf("read stubs: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		body, err := stubFS.ReadFile("stubs/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
