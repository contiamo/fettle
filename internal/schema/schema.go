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
	CreatedBy   string      `json:"created_by"`
	CreatedAt   time.Time   `json:"created_at"`
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

// RunManifest is the contents of run.json.
type RunManifest struct {
	Name          string                `json:"name"`
	FettleVersion string                `json:"fettle_version"`
	CreatedAt     time.Time             `json:"created_at"`
	TargetRepo    string                `json:"target_repo"`
	TargetRepoGit *GitInfo              `json:"target_repo_git,omitempty"`
	Include       []string              `json:"include"`
	Exclude       []string              `json:"exclude"`
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
