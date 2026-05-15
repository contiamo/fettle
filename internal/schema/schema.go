// Package schema holds the on-disk JSON types fettle reads and writes.
package schema

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Finding is the LLM-emitted payload of one issue. It's embedded in
// FindingEntry on disk (see entries.go), and also surfaced standalone
// wherever the CLI / UI needs the finding without its review history
// (`fettle show finding`, `fettle list findings`, the review-agent
// SUBJECT_JSON prompt variable).
type Finding struct {
	ID          string      `json:"id"`
	File        string      `json:"file"`
	Line        int         `json:"line"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Suggestion  string      `json:"suggestion"`
	Severity    *string     `json:"severity"`
	Labels      []string    `json:"labels"`
	References  []Reference `json:"references"`
	// AnchorLine is the exact text of File[Line] at finding-creation time,
	// truncated to anchor.MaxLen. It lets readers detect drift later: if
	// the file changed, the same content may have shifted to a different
	// line, or disappeared entirely.
	//
	// Pointer (not string) so we can distinguish three cases:
	//   nil          — no anchor was captured (legacy finding, capture
	//                  failed at creation time, or the run had no
	//                  target_repo). Drift is reported as "unknown".
	//   &""          — the anchored line is legitimately blank.
	//   &"…"         — the anchored line's content (truncated).
	// A plain `string` field with `omitempty` would conflate the first
	// two cases and silently disable drift detection on blank-line
	// findings.
	AnchorLine *string   `json:"anchor_line,omitempty"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// Reference is an additional code location an issue points at.
type Reference struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
}

// FileStatus is one row of files.jsonl, the per-file scan ledger.
type FileStatus struct {
	File         string    `json:"file"`
	Status       string    `json:"status"`
	FindingCount int       `json:"finding_count"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`
	Error        string    `json:"error,omitempty"`
}

// Status values for FileStatus.
const (
	StatusOK    = "ok"
	StatusEmpty = "empty"
	StatusError = "error"
)

// Subject identifies what a review or outcome is about. Today this is
// always a finding — the type stays as a discriminator so flat CLI / API
// output ("here are all reviews across the run") can carry the kind +
// finding-id pair without re-encoding the convention everywhere. On disk
// it isn't stored: a review/outcome lives inside its finding's doc, so the
// subject is the file path.
type Subject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Subject kinds.
const (
	SubjectFinding = "finding"
)

// RunManifest is the contents of run.json.
type RunManifest struct {
	Name          string         `json:"name"`
	Stage         string         `json:"stage"` // "find" today; kept as a discriminator for future stages
	FettleVersion string         `json:"fettle_version"`
	CreatedAt     time.Time      `json:"created_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	TargetRepo    string         `json:"target_repo,omitempty"`
	TargetRepoGit *GitInfo       `json:"target_repo_git,omitempty"`
	Walker        string         `json:"walker,omitempty"` // "git" | "fs"; omitted on pre-walker-field runs (treat as "git")
	Include       []string       `json:"include,omitempty"`
	Exclude       []string       `json:"exclude,omitempty"`
	Agent         *AgentInfo     `json:"agent,omitempty"`
	SourcePath    string         `json:"source_path,omitempty"`   // path of the editable prompt at stage start (project-relative)
	SnapshotPath  string         `json:"snapshot_path,omitempty"` // path of the frozen prompt copy inside the run (run-relative)
	Args          map[string]any `json:"args,omitempty"`
}

// GitInfo records the target repo's git state at run start.
type GitInfo struct {
	Head  string `json:"head"`
	Dirty bool   `json:"dirty"`
}

// AgentInfo describes the agent that ran a stage.
type AgentInfo struct {
	Name   string `json:"name"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Script string `json:"script,omitempty"`
}

// NewFindingID returns a fresh random 16-hex-char id. Ids are not derived
// from finding content, so two findings with identical (file, line, title)
// get distinct ids. Crash-mid-write recovery can therefore surface a small
// number of duplicate findings with different ids — humans reviewing the
// UI dismiss them, and a content-hash dedup is unreliable against LLM
// phrasing drift anyway.
func NewFindingID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// AuthorSlug strips the `human:` / `agent:` prefix and any `/<model>`
// suffix from a canonical author stamp, returning the raw slug. Used by
// `fettle run review` to decide whether the same reviewer has already
// touched a finding — keying on slug means switching the agent's model
// (e.g. claude/sonnet → claude/opus) doesn't force re-review.
//
// Empty input or a malformed stamp returns the input unchanged so callers
// don't have to guard against nil; the only callers today are review
// resume and the CLI's `fettle show review` output, both tolerant of the
// pass-through case.
func AuthorSlug(stamp string) string {
	s := stamp
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}
