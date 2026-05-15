package run

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/contiamo/fettle/internal/schema"
)

// LoadFindingEntries returns every finding entry across all
// findings_*.jsonl files in the run. Entries are returned in the
// order they appear within each file, with files processed in the
// lexical (chronological) order their filenames sort to — but
// callers that need deduplication by id should explicitly resolve,
// since two `fettle find` invocations on the same run can each
// emit different findings (or, edge case, the same id if two
// processes raced).
//
// Tolerates malformed lines: a corrupt line is logged to stderr
// and skipped, matching the rest of the run-read paths so a torn
// append doesn't fault the whole UI.
func (p *Path) LoadFindingEntries() ([]schema.FindingEntry, error) {
	files, err := p.listArtifacts(ArtifactFindings)
	if err != nil {
		return nil, err
	}
	var out []schema.FindingEntry
	for _, name := range files {
		path := filepath.Join(p.dir, name)
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var e schema.FindingEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				fmt.Fprintf(os.Stderr, "fettle: skipping malformed line in %s: %v\n", name, err)
				continue
			}
			out = append(out, e)
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %s: %w", name, err)
		}
	}
	return out, nil
}

// LoadReviewEntries returns every review entry across all
// reviews_*.jsonl files in the run. Same tolerance for malformed
// lines as LoadFindingEntries; callers ResolveLabels/ResolveSeverity
// the result after filtering by subject.
func (p *Path) LoadReviewEntries() ([]schema.ReviewEntry, error) {
	files, err := p.listArtifacts(ArtifactReviews)
	if err != nil {
		return nil, err
	}
	var out []schema.ReviewEntry
	for _, name := range files {
		path := filepath.Join(p.dir, name)
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var e schema.ReviewEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				fmt.Fprintf(os.Stderr, "fettle: skipping malformed line in %s: %v\n", name, err)
				continue
			}
			out = append(out, e)
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %s: %w", name, err)
		}
	}
	return out, nil
}

// LoadOutcomeEntries mirrors LoadReviewEntries for the outcome stream.
func (p *Path) LoadOutcomeEntries() ([]schema.OutcomeEntry, error) {
	files, err := p.listArtifacts(ArtifactOutcomes)
	if err != nil {
		return nil, err
	}
	var out []schema.OutcomeEntry
	for _, name := range files {
		path := filepath.Join(p.dir, name)
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var e schema.OutcomeEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				fmt.Fprintf(os.Stderr, "fettle: skipping malformed line in %s: %v\n", name, err)
				continue
			}
			out = append(out, e)
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %s: %w", name, err)
		}
	}
	return out, nil
}

// CountFindingEntriesForFile returns how many distinct findings in
// the run anchor to the given repo-relative file path. Used by the
// find harness to derive the per-file ledger row from the delta of
// "before vs after the agent ran." Dedupes by id so the same
// finding appearing in two findings_*.jsonl files counts once.
func (p *Path) CountFindingEntriesForFile(file string) (int, error) {
	entries, err := p.LoadFindingEntries()
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.File != file {
			continue
		}
		seen[e.ID] = struct{}{}
	}
	return len(seen), nil
}

// FindingEntryExists reports whether any findings_*.jsonl file
// contains an entry whose id matches. Used by writers to validate
// `--finding <id>` before appending a review or outcome that would
// otherwise dangle in the audit trail.
//
// O(N) over total findings in the run. Acceptable for the
// validation path; the read happens once per CLI invocation and
// the file set is small. A persistent index could land later if
// this ever shows up in profiles.
func (p *Path) FindingEntryExists(id string) (bool, error) {
	entries, err := p.LoadFindingEntries()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// listArtifacts returns the filenames of artifact files of the
// given kind, in chronological order (lexical sort over the
// timestamped filenames is chronological by construction). Files
// the parser doesn't recognise are skipped silently — the run
// directory holds other things too (manifest, instructions,
// raw outputs).
func (p *Path) listArtifacts(kind ArtifactKind) ([]string, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		meta, ok := ParseArtifactFilename(e.Name())
		if !ok {
			continue
		}
		if meta.Kind != kind {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
