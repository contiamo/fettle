// Package identity resolves and persists the slug fettle stamps onto
// records it attributes back (reviews, outcomes). The resolution chain
// is shared between the CLI (cmd/fettle) and the web UI (internal/ui).
//
// Resolution order — first non-empty wins:
//
//  1. $FETTLE_AGENT       — set by the harness during a stage; flagged
//     IsAgent so callers can format `agent:<name>[/<model>]`.
//  2. $FETTLE_AUTHOR      — explicit per-invocation override.
//  3. ~/.config/fettle/identity — slug the UI persisted on first edit.
//
// Falls through to ErrNoIdentity if nothing is configured. The UI
// catches that and prompts; the CLI surfaces it as a hard error.
package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Env var names. Exported so callers (the run-review harness, tests)
// can override them through the same string everywhere.
const (
	EnvAgent  = "FETTLE_AGENT"
	EnvAuthor = "FETTLE_AUTHOR"
	EnvModel  = "FETTLE_MODEL"
)

// ErrNoIdentity is returned by Resolve when none of the sources in the
// chain has a value. Callers test for it with errors.Is to distinguish
// "no identity configured" from genuine I/O failures (which Resolve
// also surfaces, wrapped, when the config-file path is unreadable for
// reasons other than file-not-exist).
var ErrNoIdentity = errors.New("no author identity configured")

// slugRegex mirrors internal/run.slugRegex — both flow into filesystem
// paths (run folders and reviews_<author>.jsonl), so the same character
// class keeps both filename-safe.
var slugRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateSlug reports whether s is a syntactically valid author slug.
// Empty is rejected here (a missing slug is the caller's "no identity"
// path; this function is for "is the value the user typed acceptable?").
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("author slug must not be empty")
	}
	if !slugRegex.MatchString(s) {
		return fmt.Errorf("invalid author slug %q: only [A-Za-z0-9_-] allowed", s)
	}
	return nil
}

// Resolved is the result of Resolve. Slug is the raw author identity;
// IsAgent flags the FETTLE_AGENT branch so callers know to apply the
// `agent:<name>[/<model>]` prefix. Source records which slot the
// value came from — useful for error messages and the UI's "Reviewing
// as: <slug> (from <source>)" hint.
type Resolved struct {
	Slug    string
	IsAgent bool
	Source  Source
}

// Source enumerates where a Resolved slug came from.
type Source int

const (
	SourceNone Source = iota
	SourceAgentEnv
	SourceAuthorEnv
	SourceConfigFile
)

func (s Source) String() string {
	switch s {
	case SourceAgentEnv:
		return "$" + EnvAgent
	case SourceAuthorEnv:
		return "$" + EnvAuthor
	case SourceConfigFile:
		return "~/.config/fettle/identity"
	default:
		return "none"
	}
}

// Resolve walks the chain and returns the first match. ErrNoIdentity
// when nothing has been configured. Other errors (a config file that
// exists but can't be read for reasons other than NotExist) are wrapped
// and returned unchanged — those represent real I/O problems the
// caller should surface.
func Resolve() (Resolved, error) {
	if a := strings.TrimSpace(os.Getenv(EnvAgent)); a != "" {
		return Resolved{Slug: a, IsAgent: true, Source: SourceAgentEnv}, nil
	}
	if a := strings.TrimSpace(os.Getenv(EnvAuthor)); a != "" {
		return Resolved{Slug: a, IsAgent: false, Source: SourceAuthorEnv}, nil
	}
	path, err := configFilePath()
	if err != nil {
		// No home dir → no config file possible. That's not an error,
		// it's the "no identity" state.
		return Resolved{}, ErrNoIdentity
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Resolved{}, ErrNoIdentity
		}
		return Resolved{}, fmt.Errorf("read identity file %s: %w", path, err)
	}
	slug := strings.TrimSpace(string(data))
	if slug == "" {
		return Resolved{}, ErrNoIdentity
	}
	return Resolved{Slug: slug, IsAgent: false, Source: SourceConfigFile}, nil
}

// String formats a Resolved as the canonical stamp written into
// findings.created_by, reviews.author, and outcomes.author:
// `agent:<name>[/<model>]` for agent stamps, `human:<slug>` for
// humans. Implementing Stringer means an identity stringifies
// consistently anywhere fmt.* / log/slog touches it, not just at the
// hand-coded write sites — and the formatting rule lives on the type
// rather than as a free function. EnvModel is read at call time so
// agents that switch models per request still produce a faithful
// stamp without re-resolving the identity.
func (r Resolved) String() string {
	if r.IsAgent {
		model := strings.TrimSpace(os.Getenv(EnvModel))
		if model == "" {
			return "agent:" + r.Slug
		}
		return "agent:" + r.Slug + "/" + model
	}
	return "human:" + r.Slug
}

// Save writes slug to ~/.config/fettle/identity (creating the
// directory if needed). Validates the slug before persisting. The
// path is per-user, per-machine — never under the project — so the
// slug stays out of the repo even when .fettle.json is checked in.
func Save(slug string) error {
	if err := ValidateSlug(slug); err != nil {
		return err
	}
	path, err := configFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, []byte(slug+"\n"), 0o644)
}

// ConfigFilePath returns the on-disk path for the persisted identity.
// Exported for diagnostics and tests; the resolution chain consults it
// internally.
func ConfigFilePath() (string, error) {
	return configFilePath()
}

func configFilePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("FETTLE_IDENTITY_FILE")); override != "" {
		// Test/diagnostic hook. Not documented for users — the chain
		// above is the user-facing surface — but lets tests exercise
		// the file branch without touching $HOME.
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".config", "fettle", "identity"), nil
}

// ErrorMessage formats a user-facing message for ErrNoIdentity.
// Centralised so both the CLI and UI surface the same hint.
func ErrorMessage() string {
	return "no author identity: set $FETTLE_AUTHOR, write a slug to ~/.config/fettle/identity, or run `fettle ui` once to pick one"
}
