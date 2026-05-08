// Package schema holds the on-disk JSONL types fettle reads and writes.
package schema

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Finding is one issue produced by `fettle find`. Lives in
// runs/<run>/findings.jsonl.
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

// Subject identifies what a review or outcome is about.
type Subject struct {
	Kind string `json:"kind"` // always "finding" today; kept as a discriminator for future kinds
	ID   string `json:"id"`
}

// Subject kinds.
const (
	SubjectFinding = "finding"
)

// Review is one row of reviews_<author>.jsonl. Append-only history;
// each entry is the writing author's current full label set on the
// subject. The filename groups entries by author for per-author lock
// granularity, but Author on the record is the canonical source of
// truth — same prefixed form as Outcome.Author and Finding.CreatedBy
// (`human:slug` or `agent:slug[/model]`), so attribution survives
// file moves and concatenation across runs.
type Review struct {
	Subject Subject `json:"subject"`
	Author  string  `json:"author"`
	// Labels uses pointer-to-slice to distinguish three states:
	//   nil           — reviewer didn't touch labels on this entry,
	//                   their prior override (if any) carries forward,
	//                   otherwise the LLM's Finding.Labels stay in effect.
	//   &[]           — explicit clear: the reviewer is asserting "no
	//                   labels on this finding from me" and the LLM's
	//                   set is suppressed (when no other reviewer has
	//                   added back into the union).
	//   &["a", "b"]   — these labels are this reviewer's current set,
	//                   replacing any prior override they made.
	// Effective labels for a finding = union of every reviewer's
	// latest non-nil Labels override, falling back to Finding.Labels
	// when no reviewer has touched labels.
	Labels *[]string `json:"labels,omitempty"`
	// Severity, when non-nil, is the reviewer's judgment that
	// overrides the LLM's initial Finding.Severity for display and
	// sorting. nil means "no judgment" — defer to the find-time
	// value. The effective severity for a finding at any point in
	// time is "the latest review entry whose Severity is non-nil,
	// across all reviewers", falling back to Finding.Severity when
	// no reviewer has set one. This lets reviewers up- or down-grade
	// severity through the same per-author append-only review log
	// the labels and comment use, with a full audit trail (who
	// changed it, when, with what comment) for free.
	Severity *string   `json:"severity,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at"`
}

// RunManifest is the contents of run.json.
type RunManifest struct {
	Name          string         `json:"name"`
	Stage         string         `json:"stage"` // "find" today; kept as a discriminator for future stages
	FettleVersion string         `json:"fettle_version"`
	CreatedAt     time.Time      `json:"created_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	TargetRepo    string         `json:"target_repo,omitempty"`
	TargetRepoGit *GitInfo       `json:"target_repo_git,omitempty"`
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

// Outcome is one event recording what happened to a finding (PR
// merged, won't fix, etc.). Lives in runs/<name>/outcomes.jsonl.
// Append-only; latest entry per subject wins for "current state"
// display, but the full history is preserved (and viewable via
// `fettle show outcome --all`).
type Outcome struct {
	Subject Subject   `json:"subject"`
	Author  string    `json:"author"`
	Status  string    `json:"status"`
	PRURL   string    `json:"pr_url,omitempty"`
	At      time.Time `json:"at"`
}
