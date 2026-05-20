// Package skill owns the agent-skill bundles fettle ships and the
// helper that installs them into a coding agent's local skill
// directory. Bundles are //go:embed'd into the binary so an
// `install-skill` run never depends on the network or on the user
// keeping a checkout of fettle around.
//
// Today only the claude-code bundle ships; the package surface treats
// agents as a closed enum so adding more (codex, etc.) later is a
// matter of dropping a bundle into skills/<agent>/ and extending the
// Agents() list.
package skill

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// skillFS holds every shipped skill bundle. The layout is
// skills/<agent>/<skill-name>/...; <skill-name> matches the folder
// the bundle gets written to in the target agent's skills directory
// (and the `name:` in the bundle's SKILL.md frontmatter — Claude
// Code's loader expects those to agree).
//
//go:embed skills
var skillFS embed.FS

// Agent identifies a supported coding agent that fettle ships a skill
// bundle for.
type Agent string

const (
	// AgentClaudeCode is Anthropic's Claude Code CLI. Skills live
	// under ~/.claude/skills/<name>/ (user scope) or
	// <cwd>/.claude/skills/<name>/ (project scope).
	AgentClaudeCode Agent = "claude-code"
)

// Scope determines where a skill bundle is installed.
//
// ScopeUser writes to the user's home skill directory and makes the
// skill available across every Claude Code session on the machine.
// ScopeProject writes to .claude/skills/<name>/ under the cwd so the
// skill can be checked into a repo and shared with teammates, locked
// to the fettle version that produced it.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// SkillName is the name the bundle installs under — always "fettle"
// regardless of agent, so a user with multiple coding agents sees the
// same identifier everywhere.
const SkillName = "fettle"

// Agents returns the agents this binary has bundles for. Used by
// `fettle install-skill --list`.
func Agents() []Agent {
	return []Agent{AgentClaudeCode}
}

// Scopes returns the supported install scopes.
func Scopes() []Scope {
	return []Scope{ScopeUser, ScopeProject}
}

// ErrAgentMissingHome is returned when the target agent's home
// directory (e.g. ~/.claude/ for Claude Code) doesn't exist. The
// caller almost certainly hasn't installed the agent.
var ErrAgentMissingHome = errors.New("agent home directory not found")

// ErrAlreadyInstalled is returned when the destination directory
// exists and `overwrite` was false. Callers map this to the
// `--force` hint at the CLI surface.
var ErrAlreadyInstalled = errors.New("skill already installed")

// ErrUnknownAgent is returned when the caller asks for a bundle the
// binary doesn't ship.
var ErrUnknownAgent = errors.New("unknown agent")

// ErrUnknownScope is returned when the caller passes a scope value
// outside the closed set in Scopes().
var ErrUnknownScope = errors.New("unknown scope")

// DefaultDest returns the conventional install path for an agent's
// skill bundle at the requested scope. For Claude Code:
//   - ScopeUser:    $HOME/.claude/skills/fettle/
//   - ScopeProject: <cwd>/.claude/skills/fettle/
//
// DefaultDest does NOT validate that the path's parents exist — that's
// Install's job, so we can return a precise error from one place.
func DefaultDest(agent Agent, scope Scope) (string, error) {
	if agent != AgentClaudeCode {
		return "", fmt.Errorf("%w: %q", ErrUnknownAgent, agent)
	}
	switch scope {
	case ScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		return filepath.Join(home, ".claude", "skills", SkillName), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
		return filepath.Join(cwd, ".claude", "skills", SkillName), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownScope, scope)
	}
}

// agentHome returns the agent's *home* directory — the parent of the
// `skills/` directory — for the missing-install check. For Claude
// Code that's $HOME/.claude.
func agentHome(agent Agent) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	switch agent {
	case AgentClaudeCode:
		return filepath.Join(home, ".claude"), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownAgent, agent)
	}
}

// Install writes the embedded skill bundle for the named agent into
// dest at the requested scope. Pass dest = "" to use
// DefaultDest(agent, scope).
//
// If overwrite is false and dest exists, Install returns
// ErrAlreadyInstalled — protecting a user who has edited their copy.
// For ScopeUser on the default path, if the agent's home dir doesn't
// exist (e.g. $HOME/.claude/ for Claude Code), returns
// ErrAgentMissingHome so the caller can suggest "is Claude Code
// installed?". ScopeProject skips this check — we're just creating
// directories under cwd, no agent-install assumption.
//
// Returns the absolute destination path on success.
func Install(agent Agent, scope Scope, dest string, overwrite bool) (string, error) {
	bundleRoot := filepath.ToSlash(filepath.Join("skills", string(agent), SkillName))
	if _, err := fs.Stat(skillFS, bundleRoot); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q (no bundle compiled in)", ErrUnknownAgent, agent)
		}
		return "", fmt.Errorf("stat bundle %s: %w", bundleRoot, err)
	}

	usingDefault := dest == ""
	if usingDefault {
		var err error
		dest, err = DefaultDest(agent, scope)
		if err != nil {
			return "", err
		}
	} else {
		abs, err := filepath.Abs(dest)
		if err != nil {
			return "", fmt.Errorf("resolve dest %q: %w", dest, err)
		}
		dest = abs
	}

	// User-scope default path: refuse if the agent home doesn't exist
	// (almost certainly means the agent isn't installed). Project-
	// scope or explicit --output: trust the caller.
	if usingDefault && scope == ScopeUser {
		home, err := agentHome(agent)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(home); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%w: %s", ErrAgentMissingHome, home)
			}
			return "", fmt.Errorf("stat %s: %w", home, err)
		}
	}

	if _, err := os.Stat(dest); err == nil {
		if !overwrite {
			return "", fmt.Errorf("%w: %s", ErrAlreadyInstalled, dest)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("stat %s: %w", dest, err)
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}

	if err := copyBundle(bundleRoot, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// copyBundle walks every file under bundleRoot in skillFS and writes
// it under destDir, preserving relative paths and directory layout.
func copyBundle(bundleRoot, destDir string) error {
	return fs.WalkDir(skillFS, bundleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destDir, rel)
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			return nil
		}
		body, err := fs.ReadFile(skillFS, path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}
