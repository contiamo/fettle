package run

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// ReviewFile is one reviews_<author>.jsonl file in a run directory:
// the absolute path to the file plus the author slug parsed off the
// filename. Returned by ReviewFiles so every caller does the slug
// extraction the same way.
type ReviewFile struct {
	Path   string
	Author string
}

// ReviewFiles enumerates every reviews_<author>.jsonl file in runDir.
// Returns entries in directory-iteration order — callers that need
// determinism should sort the result.
//
// Single point of truth for "what counts as a review file": the prior
// CLI (run_dedupe, review, run_merge) and the summary loader each
// open-coded the same prefix/suffix slice off the filename, with the
// same off-by-one risk if the convention ever changed. Centralising
// here keeps that decision in one place.
func ReviewFiles(runDir string) ([]ReviewFile, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, err
	}
	var out []ReviewFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "reviews_") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		author := strings.TrimSuffix(strings.TrimPrefix(name, "reviews_"), ".jsonl")
		out = append(out, ReviewFile{Path: filepath.Join(runDir, name), Author: author})
	}
	return out, nil
}

// FlatReview is one schema.Review flattened with the author slug
// extracted from the filename. AuthorSlug is the bare reviews_<slug>
// filename component used for routing and append-locking; Author is
// the full prefixed stamp from the record (`human:slug` or
// `agent:slug[/model]`), the canonical "who reviewed this" carried
// on the JSONL line itself.
type FlatReview struct {
	Subject    schema.Subject `json:"subject"`
	Author     string         `json:"author"`
	AuthorSlug string         `json:"-"`
	Labels     *[]string      `json:"labels,omitempty"`
	Severity   *string        `json:"severity,omitempty"`
	Comment    string         `json:"comment,omitempty"`
	At         time.Time      `json:"at"`
}

// LoadAllReviews reads every reviews_<author>.jsonl in the run folder
// and returns a chronological flat list (oldest first) annotated with
// the author slug. Malformed lines are skipped (matching the rest of
// the JSONL readers in this package). Missing files contribute zero —
// a run with no reviewers yet returns an empty slice.
func (p *Path) LoadAllReviews() ([]FlatReview, error) {
	files, err := ReviewFiles(p.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []FlatReview
	for _, rf := range files {
		entries, err := readReviewFile(rf.Path, rf.Author)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func readReviewFile(path, slug string) ([]FlatReview, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, jsonlScanInitBuf), jsonlScanMaxLine)
	var out []FlatReview
	for sc.Scan() {
		var r schema.Review
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		out = append(out, FlatReview{
			Subject:    r.Subject,
			Author:     r.Author,
			AuthorSlug: slug,
			Labels:     r.Labels,
			Severity:   r.Severity,
			Comment:    r.Comment,
			At:         r.At,
		})
	}
	return out, sc.Err()
}
