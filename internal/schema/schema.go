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
	Members     []Member    `json:"members,omitempty"`
	CreatedBy   string      `json:"created_by"`
	CreatedAt   time.Time   `json:"created_at"`
}

// Reference is an additional code location an issue points at.
type Reference struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
}

// Member is one source-finding back-pointer on a merge or dedupe
// canonical finding. members.length is always 1 for merge runs and
// 1 or more for dedupe runs.
type Member struct {
	FindingID string `json:"finding_id"`
	FromRun   string `json:"from_run"`
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

// Subject identifies what a review or closure is about.
type Subject struct {
	Kind string `json:"kind"` // "finding" or "group"
	ID   string `json:"id"`
}

// Subject kinds.
const (
	SubjectFinding = "finding"
	SubjectGroup   = "group"
)

// Review is one row of reviews_<author>.jsonl. Append-only history;
// each entry is the writing author's current full label set on the
// subject. Author identity is carried by the filename, not the entry.
type Review struct {
	Subject Subject   `json:"subject"`
	Labels  []string  `json:"labels"`
	Comment string    `json:"comment,omitempty"`
	At      time.Time `json:"at"`
}

// RunManifest is the contents of run.json.
type RunManifest struct {
	Name          string                `json:"name"`
	Stage         string                `json:"stage"` // "find" | "merge" | "dedupe" | "group"
	FettleVersion string                `json:"fettle_version"`
	CreatedAt     time.Time             `json:"created_at"`
	CompletedAt   *time.Time            `json:"completed_at,omitempty"`
	TargetRepo    string                `json:"target_repo,omitempty"`
	TargetRepoGit *GitInfo              `json:"target_repo_git,omitempty"`
	Include       []string              `json:"include,omitempty"`
	Exclude       []string              `json:"exclude,omitempty"`
	InputRun      string                `json:"input_run,omitempty"`  // group runs
	InputRuns     []string              `json:"input_runs,omitempty"` // dedupe runs
	Stages        map[string]StageEntry `json:"stages"`
	Args          map[string]any        `json:"args,omitempty"`
}

// GitInfo records the target repo's git state at run start.
type GitInfo struct {
	Head  string `json:"head"`
	Dirty bool   `json:"dirty"`
}

// StageEntry is one row in RunManifest.Stages — set incrementally as each
// stage runs in this run folder.
type StageEntry struct {
	Agent        string `json:"agent"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Script       string `json:"script,omitempty"`
	SourcePath   string `json:"source_path"`
	SnapshotPath string `json:"snapshot_path"`
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

// Group is one cluster of findings produced by `fettle run group`.
// Lives in runs/<group-run>/groups.jsonl. `finding_ids[]` references
// findings in the group run's `input_run`'s findings.jsonl.
type Group struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	FindingIDs []string  `json:"finding_ids"`
	Labels     []string  `json:"labels"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// NewGroupID returns a fresh random group id of the form `g_xxxxxxxx`
// (8 hex chars). The `g_` prefix keeps groups distinguishable from
// findings at a glance — useful when both kinds appear side by side in
// review/closure logs.
func NewGroupID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return "g_" + hex.EncodeToString(b[:])
}
