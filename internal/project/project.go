// Package project owns the fettle project directory: the .fettle.json
// marker, project-level config, and the instructions/ template tree.
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
// instructions/. They're proper markdown files in stubs/, not Go const
// strings, so they're easy to edit with normal markdown tooling.
//
//go:embed stubs/*.md
var stubFS embed.FS

// Version is the fettle version stamped into .fettle.json on init.
const Version = "0.1.0"

// MarkerFile is the filename that confirms a directory is a fettle project.
const MarkerFile = ".fettle.json"

// Config is the on-disk shape of .fettle.json.
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
// project root. Run folders snapshot these on first stage execution.
type Instructions struct {
	Find   string `json:"find"`
	Review string `json:"review"`
}

// NewConfig returns a Config populated with sensible defaults for `init`.
func NewConfig(targetRepo, agent, model string) Config {
	return Config{
		FettleVersion: Version,
		CreatedAt:     time.Now().UTC(),
		TargetRepo:    targetRepo,
		Agent:         AgentRef{Name: agent, Model: model},
		Include:       []string{"**/*.go"},
		Exclude:       []string{"vendor/**", "node_modules/**", "**/*_generated.go"},
		Instructions: Instructions{
			Find:   "instructions/find.md",
			Review: "instructions/review.md",
		},
	}
}

// Init writes a fresh fettle project at dir. Fails if .fettle.json already
// exists; existing instruction stubs are preserved.
func Init(dir string, cfg Config) error {
	markerPath := filepath.Join(dir, MarkerFile)
	if _, err := os.Stat(markerPath); err == nil {
		return fmt.Errorf("%s already exists in %s", MarkerFile, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", MarkerFile, err)
	}

	insDir := filepath.Join(dir, "instructions")
	if err := os.MkdirAll(insDir, 0o755); err != nil {
		return fmt.Errorf("create instructions/: %w", err)
	}
	if err := writeStubs(insDir); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return fmt.Errorf("create runs/: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(markerPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", MarkerFile, err)
	}
	return nil
}

// Load reads .fettle.json from dir.
func Load(dir string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", MarkerFile, err)
	}
	return cfg, nil
}

// ResolveTargetRepo returns the target repo as an absolute path, treating
// a relative `target_repo` as relative to projectDir. This lets a
// committed .fettle.json portably reference a target via "../.." (or
// similar) without baking in machine-specific absolute paths.
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

