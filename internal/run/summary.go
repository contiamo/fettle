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
// Missing artifacts (empty run dir, no findings yet) contribute zero
// counts so partial runs render cleanly.
//
// Counts dedupe by id within a stream (rare — same id in two
// findings_*.jsonl files only happens on a re-run / resume) so the
// number reflects "distinct findings" rather than "lines written."
func Summarize(runDir string) (Summary, error) {
	rp, err := Open(runDir)
	if err != nil {
		return Summary{}, err
	}
	m, err := rp.Manifest()
	if err != nil {
		return Summary{}, err
	}

	findings, err := rp.LoadFindingEntries()
	if err != nil {
		return Summary{}, fmt.Errorf("load findings: %w", err)
	}
	seenFindings := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		seenFindings[f.ID] = struct{}{}
	}
	findingCount := len(seenFindings)

	reviewEntries, err := rp.LoadReviewEntries()
	if err != nil {
		return Summary{}, fmt.Errorf("load reviews: %w", err)
	}
	outcomeEntries, err := rp.LoadOutcomeEntries()
	if err != nil {
		return Summary{}, fmt.Errorf("load outcomes: %w", err)
	}
	return Summary{
		Name:        m.Name,
		Stage:       m.Stage,
		CreatedAt:   m.CreatedAt,
		CompletedAt: m.CompletedAt,
		TargetRepo:  m.TargetRepo,
		Counts:      Counts{Findings: &findingCount, Reviews: len(reviewEntries), Outcomes: len(outcomeEntries)},
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

