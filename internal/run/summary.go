package run

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// Summarize reads the manifest and counts the records in a run folder.
// Missing record files contribute zero counts so partial runs report
// cleanly.
func Summarize(runDir string) (Summary, error) {
	rp, err := Open(runDir)
	if err != nil {
		return Summary{}, err
	}
	m, err := rp.Manifest()
	if err != nil {
		return Summary{}, err
	}

	outcomes, err := CountLines(filepath.Join(runDir, "outcomes.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("count outcomes: %w", err)
	}

	reviews, err := countReviews(runDir)
	if err != nil {
		return Summary{}, fmt.Errorf("count reviews: %w", err)
	}

	counts := Counts{Reviews: reviews, Outcomes: outcomes}
	n, err := CountLines(filepath.Join(runDir, "findings.jsonl"))
	if err != nil {
		return Summary{}, fmt.Errorf("count findings: %w", err)
	}
	counts.Findings = &n

	return Summary{
		Name:        m.Name,
		Stage:       m.Stage,
		CreatedAt:   m.CreatedAt,
		CompletedAt: m.CompletedAt,
		TargetRepo:  m.TargetRepo,
		Counts:      counts,
	}, nil
}

// CountLines returns the number of non-empty lines in path. A missing
// file contributes zero, so this is safe to call against a run folder
// where some record files haven't been touched yet.
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

// countReviews sums lines across every reviews_<author>.jsonl in
// runDir. Missing files contribute zero.
func countReviews(runDir string) (int, error) {
	files, err := ReviewFiles(runDir)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, rf := range files {
		n, err := CountLines(rf.Path)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
