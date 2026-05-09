package run

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Summary is the per-run shape emitted by `fettle list runs`,
// `fettle show run`, and the web UI's run picker. It pulls a small
// set of identity + provenance fields off the manifest plus a counts
// block summarizing records in the run folder.
type Summary struct {
	Name        string     `json:"name"`
	Stage       string     `json:"stage"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	TargetRepo  string     `json:"target_repo,omitempty"`
	Counts      Counts     `json:"counts"`
}

// Counts breaks record counts out by kind. Findings is stage-specific
// (only find runs carry findings today), so it's a pointer that omits
// when not applicable. Reviews and Outcomes are always emitted — zero
// is meaningful.
type Counts struct {
	Findings *int `json:"findings,omitempty"`
	Reviews  int  `json:"reviews"`
	Outcomes int  `json:"outcomes"`
}

// Summarize reads the manifest, counts the records in the run folder,
// and returns the row shown by `fettle list runs` / `fettle show run`.
// Missing artifacts (no findings/ dir on a fresh run) contribute zero
// counts so partial runs render cleanly.
//
// Findings count = number of `findings/<id>.json` files (including
// any that fail to parse — the file existing means an agent emitted
// a finding, even if a torn write left it unreadable). Reviews and
// outcomes counts walk the parsed docs and skip malformed ones; a
// rebuild after a crash will surface the warning during render.
func Summarize(runDir string) (Summary, error) {
	rp, err := Open(runDir)
	if err != nil {
		return Summary{}, err
	}
	m, err := rp.Manifest()
	if err != nil {
		return Summary{}, err
	}

	ids, err := rp.ListFindingIDs()
	if err != nil {
		return Summary{}, fmt.Errorf("list findings: %w", err)
	}
	findingCount := len(ids)

	docs, err := rp.LoadAllFindings()
	if err != nil {
		return Summary{}, fmt.Errorf("load findings: %w", err)
	}
	reviews, outcomes := 0, 0
	for _, d := range docs {
		reviews += len(d.Reviews)
		outcomes += len(d.Outcomes)
	}
	return Summary{
		Name:        m.Name,
		Stage:       m.Stage,
		CreatedAt:   m.CreatedAt,
		CompletedAt: m.CompletedAt,
		TargetRepo:  m.TargetRepo,
		Counts:      Counts{Findings: &findingCount, Reviews: reviews, Outcomes: outcomes},
	}, nil
}

// CountLines returns the number of non-empty lines in path. A missing
// file contributes zero, so this is safe to call against a run folder
// where some record files haven't been touched yet. files.jsonl is
// the only remaining JSONL stream that uses this; per-finding doc
// counts go through LoadAllFindings.
func CountLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, jsonlScanInitBuf), jsonlScanMaxLine)
	count := 0
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) > 0 {
			count++
		}
	}
	return count, sc.Err()
}

