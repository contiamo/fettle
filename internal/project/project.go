// Package project owns the fettle project directory: the .fettle/
// subdir holds every artifact fettle owns (config.json, instructions/,
// runs/), so a fettle project can sit alongside an existing git repo
// without polluting its root.
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

// stubFS holds the per-stage prompt templates `fettle init` writes into
// .fettle/instructions/. They're proper markdown files in stubs/, not
// Go const strings, so they're easy to edit with normal markdown tooling.
//
//go:embed stubs/*.md
var stubFS embed.FS

// Version is the fettle version stamped into config.json on init.
const Version = "0.1.0"

// Subdir is the per-project directory that holds every fettle artifact:
// config.json, instructions/, runs/. Mirrors the .git / .cargo / .cache
// convention so fettle stays out of the host repo's root.
const Subdir = ".fettle"

// ConfigFile is the manifest filename inside Subdir.
const ConfigFile = "config.json"

// ConfigPath returns the absolute path to a project's config.json given
// the project's host directory. Centralised so callers don't open-code
// the join.
func ConfigPath(dir string) string {
	return filepath.Join(dir, Subdir, ConfigFile)
}

// RunsDir returns the absolute path to a project's runs/ directory
// given its host directory. Run folders live under .fettle/runs/.
func RunsDir(dir string) string {
	return filepath.Join(dir, Subdir, "runs")
}

// Config is the on-disk shape of <project>/.fettle/config.json.
type Config struct {
	FettleVersion string       `json:"fettle_version"`
	CreatedAt     time.Time    `json:"created_at"`
	TargetRepo    string       `json:"target_repo"`
	Agent         AgentRef     `json:"agent"`
	Include       []string     `json:"include"`
	Exclude       []string     `json:"exclude"`
	Instructions  Instructions `json:"instructions"`
}

// AgentRef points at the agent fettle invokes for a stage. Set Name to
// "claude" or "codex" for built-in dispatch, or set Script to a path
// for a custom wrapper (see internal/agent.runCustom for the env
// contract scripts can rely on).
type AgentRef struct {
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Script string `json:"script,omitempty"`
}

// Instructions points at the editable prompt templates, relative to the
// project's host directory (the same dir that contains .fettle/). The
// defaults live under .fettle/instructions/ but a user is free to point
// at any path they want — fettle reads them verbatim.
type Instructions struct {
	Find   string `json:"find"`
	Review string `json:"review"`
}

// NewConfig assembles the Config that `init` writes. include and
// exclude come from the user's flags — there's no
// project-independent default for include that does the right
// thing, so the caller (`cmd/fettle`) is responsible for
// collecting them and rejecting an empty include list before
// reaching this function.
//
// The walker hard-skips .git / .hg / .svn / node_modules regardless
// of globs.
func NewConfig(targetRepo, agent, model string, include, exclude []string) Config {
	if include == nil {
		include = []string{}
	}
	if exclude == nil {
		exclude = []string{}
	}
	return Config{
		FettleVersion: Version,
		CreatedAt:     time.Now().UTC(),
		TargetRepo:    targetRepo,
		Agent:         AgentRef{Name: agent, Model: model},
		Include:       include,
		Exclude:       exclude,
		Instructions: Instructions{
			Find:   filepath.Join(Subdir, "instructions", "find.md"),
			Review: filepath.Join(Subdir, "instructions", "review.md"),
		},
	}
}

// Init writes a fresh fettle project at dir. Fails if .fettle/config.json
// already exists; existing instruction stubs are preserved.
func Init(dir string, cfg Config) error {
	cfgPath := ConfigPath(dir)
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s already exists", cfgPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	insDir := filepath.Join(dir, Subdir, "instructions")
	if err := os.MkdirAll(insDir, 0o755); err != nil {
		return fmt.Errorf("create instructions/: %w", err)
	}
	if err := writeStubs(insDir); err != nil {
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

// Load reads .fettle/config.json from a project's host directory.
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
// treating a relative `target_repo` as relative to projectDir (the
// host directory containing .fettle/). This lets a committed config
// portably reference a target via "../.." (or similar) without baking
// in machine-specific absolute paths.
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
