// Package project owns the fettle project directory: the .fettle.json
// marker, project-level config, and the instructions/ template tree.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

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

// AgentRef names a default agent + model for stages that don't override.
type AgentRef struct {
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
}

// Instructions points at the editable prompt templates, relative to the
// project root. Run folders snapshot these on first stage execution.
type Instructions struct {
	Find   string `json:"find"`
	Review string `json:"review"`
	Group  string `json:"group"`
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
			Group:  "instructions/group.md",
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
	for name, body := range stubs {
		path := filepath.Join(insDir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
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

var stubs = map[string]string{
	"find.md":   findStub,
	"review.md": reviewStub,
	"group.md":  groupStub,
}

const findStub = `# Find — instructions for the agent

You are analyzing **one file** for issues. Write findings as JSONL to the
output path, then exit. Replace this stub with what you actually want
fettle to look for.

## Inputs (substituted by fettle)

- ` + "`TARGET_FILE`" + ` — absolute path to the file under analysis
- ` + "`OUTPUT_PATH`" + ` — write findings here, one JSON object per line (may be empty)
- ` + "`REPO_ROOT`" + ` — absolute path to the target repo root

## Method

Replace this section with your domain. Examples:

- Refactor sweep: list legacy patterns to flag, name a conventions doc.
- Security pass: list file-local sinks (SQL concat, shell exec with
  unescaped variables, hardcoded secrets).
- Doc audit: list public-API criteria for missing/outdated comments.

## Output schema (one JSON object per line)

` + "```json" + `
{
  "file": "<repo-relative path>",
  "line": 42,
  "title": "<short imperative title, no trailing period>",
  "description": "<2-5 sentences: what's wrong and where>",
  "suggestion": "<concrete change in 1-3 sentences>",
  "severity": null,
  "labels": [],
  "references": []
}
` + "```" + `

` + "`severity`" + ` is a free-form string or null — you choose the scale.
` + "`labels`" + ` use a ` + "`prefix:value`" + ` convention, e.g.
` + "`[\"category:duplication\", \"confidence:high\"]`" + `. ` + "`references`" + ` lists
extra code locations: ` + "`{\"file\": \"...\", \"line\": 12}`" + `.

If nothing to report, write an empty file (still create it).
`

const reviewStub = `# Review — instructions for the agent

You are reviewing **one finding** produced by ` + "`fettle find`" + `. Decide what
labels and comment to attach.

## Inputs

- ` + "`ISSUE_JSON`" + ` — the finding as a single-line JSON object
- ` + "`OUTPUT_PATH`" + ` — write your review here, one JSON object on one line
- ` + "`REPO_ROOT`" + ` — absolute path to the target repo

## Output schema

` + "```json" + `
{
  "subject": {"kind": "issue", "id": "<echo from ISSUE_JSON.id>"},
  "labels":  ["confirmed", "category:false-positive"],
  "comment": "<1-3 sentences explaining the call>",
  "at":      "<RFC3339 timestamp>"
}
` + "```" + `

Replace this stub with the labels you actually want to apply (e.g.
` + "`confirmed`" + `, ` + "`false-positive`" + `, ` + "`out-of-scope`" + `, ` + "`needs-human`" + `).
`

const groupStub = `# Group — instructions for the agent

Cluster findings into review-sized groups. You see all findings (and
their reviews, if any) at once and write one JSON object per group.

## Inputs

- ` + "`ISSUES_JSON`" + ` — array of all findings in this run
- ` + "`REVIEWS_JSON`" + ` — merged review state, keyed by issue id (` + "`{}`" + ` if no reviews)
- ` + "`OUTPUT_PATH`" + ` — write groups here, one JSON object per line

## Output schema

` + "```json" + `
{
  "id": "g_<8 hex chars>",
  "title": "<short imperative title>",
  "summary": "<1-2 sentences>",
  "issue_ids": ["<finding id>", ...],
  "labels": []
}
` + "```" + `

Every issue id from ` + "`ISSUES_JSON`" + ` should appear in exactly one group's
` + "`issue_ids`" + ` (unless your prompt drops issues based on review labels).
`
