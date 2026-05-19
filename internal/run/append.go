package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/contiamo/fettle/internal/schema"
)

// streamKey identifies an open JSONL artifact file within one
// process. For findings there's one stream per run regardless of
// writer; for reviews / outcomes the stream is keyed on author so
// multiple reviewers in one process don't share a file.
type streamKey struct {
	kind   ArtifactKind
	author string // "" for findings
}

// stream holds the open file handle plus the JSON encoder bound to
// it. Encoder is cached so each Append is one allocation rather
// than re-creating an encoder per line.
type stream struct {
	f   *os.File
	enc *json.Encoder
}

// AppendFindingEntry appends one FindingEntry to the run's single
// findings stream (`findings_<slug>_<ts>.jsonl`). Every writer in
// the same run — every agent the find harness spawned, every
// human shelling `fettle add finding` against the run — appends
// to the same file via `O_APPEND`. The kernel atomically advances
// the file offset before each write so concurrent writers never
// overwrite each other; rare cross-process buffer interleaving
// would surface as a malformed line, which the read path skips
// with a stderr warning. See `streamForLocked` for the full
// concurrency story.
func (p *Path) AppendFindingEntry(e schema.FindingEntry) error {
	if e.Kind == "" {
		e.Kind = schema.SubjectFinding
	}
	return p.appendEntry(ArtifactFindings, "", e)
}

// AppendReviewEntry appends one ReviewEntry to a per-(run, author)
// reviews stream (`reviews_<slug>_<ts>_<author>.jsonl`). The author
// slug is derived from e.Author so each reviewer's entries land in
// their own file — shareable individually via `cp`. The entry is
// validated through schema.ValidateReviewEntry before any I/O so
// malformed entries can't reach disk.
func (p *Path) AppendReviewEntry(e schema.ReviewEntry) error {
	if err := schema.ValidateReviewEntry(e); err != nil {
		return err
	}
	author := schema.AuthorSlug(e.Author)
	if author == "" {
		return fmt.Errorf("review entry: author %q has no extractable slug", e.Author)
	}
	return p.appendEntry(ArtifactReviews, author, e)
}

// AppendOutcomeEntry appends one OutcomeEntry to a per-(run, author)
// outcomes stream. Same author derivation as AppendReviewEntry.
func (p *Path) AppendOutcomeEntry(e schema.OutcomeEntry) error {
	if err := schema.ValidateOutcomeEntry(e); err != nil {
		return err
	}
	author := schema.AuthorSlug(e.Author)
	if author == "" {
		return fmt.Errorf("outcome entry: author %q has no extractable slug", e.Author)
	}
	return p.appendEntry(ArtifactOutcomes, author, e)
}

// Close flushes and closes every artifact stream opened by Append*.
// Safe to call multiple times. CLI single-shot commands `defer
// rp.Close()` so the buffered writes hit disk before the process
// exits.
func (p *Path) Close() error {
	p.streamsMu.Lock()
	defer p.streamsMu.Unlock()
	var firstErr error
	for k, s := range p.streams {
		if err := s.f.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %s/%s: %w", k.kind, k.author, err)
		}
		delete(p.streams, k)
	}
	return firstErr
}

// appendEntry is the shared implementation: look up (or open) the
// file for this (kind, author) tuple, encode v with a trailing
// newline. JSON-Encoder writes a newline after each value, so the
// on-disk shape is one entry per line.
//
// The streamsMu lock is held through the encode (not just the
// open/lookup) because json.Encoder is not safe for concurrent
// use: two goroutines encoding into the same encoder would
// interleave bytes on the wire. The lock also blocks Close, so a
// goroutine can't close the file from under an in-flight encode.
func (p *Path) appendEntry(kind ArtifactKind, author string, v any) error {
	p.streamsMu.Lock()
	defer p.streamsMu.Unlock()
	s, err := p.streamForLocked(kind, author)
	if err != nil {
		return err
	}
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("append %s entry: %w", kind, err)
	}
	return nil
}

// streamForLocked returns the cached stream for (kind, author),
// opening the file on first use. The filename is deterministic per
// run (derived from the run dir's slug + start ts), so multiple
// fettle processes writing to the same run all open the same file.
// Multi-writer safety: `O_APPEND` makes the file-offset advance
// atomic per write so concurrent writers don't overwrite each
// other; cross-process buffer-level atomicity is not POSIX-
// promised, so the read path treats malformed lines as
// skip-with-warning. Caller must hold streamsMu.
func (p *Path) streamForLocked(kind ArtifactKind, author string) (*stream, error) {
	if p.streams == nil {
		p.streams = make(map[streamKey]*stream)
	}
	key := streamKey{kind: kind, author: author}
	if s, ok := p.streams[key]; ok {
		return s, nil
	}
	slug, ts := p.Slug(), p.StartTime()
	if slug == "" || ts == "" {
		return nil, fmt.Errorf("run dir %q doesn't match the canonical run_<slug>_<ts> format", filepath.Base(p.dir))
	}
	name, err := ArtifactFilename(kind, slug, ts, author)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(p.dir, name)
	// O_APPEND across processes: each write atomically positions to
	// end-of-file before writing the buffer, so concurrent writers
	// can't overwrite each other. POSIX doesn't strictly guarantee
	// the *buffer* itself is atomic against another process's
	// concurrent write, so in pathological cases two writes could in
	// theory interleave mid-line. The read path treats malformed
	// lines as skip-with-warning, so a corrupted line is recoverable
	// — and on Linux / macOS for entry-sized writes the
	// interleaving doesn't occur in practice. No O_EXCL — multiple
	// processes legitimately share this file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &stream{f: f, enc: json.NewEncoder(f)}
	p.streams[key] = s
	return s, nil
}
