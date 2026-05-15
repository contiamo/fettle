package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// streamKey identifies an open JSONL artifact file within one
// process. The same (kind, human, agent) triple shares a single
// file for the lifetime of the Path — keeping the design's
// "one file per process invocation" semantics even when a writer
// appends many entries (a bulk-review POST or a sequence of CLI
// add calls within one harness invocation).
type streamKey struct {
	kind  ArtifactKind
	human string
	agent string
}

// stream holds the open file handle plus the JSON encoder bound to
// it. Encoder is cached so each Append is one allocation rather
// than re-creating an encoder per line.
type stream struct {
	f   *os.File
	enc *json.Encoder
}

// AppendFindingEntry appends one FindingEntry to the appropriate
// findings_*.jsonl stream. The file is opened lazily on first call
// per (kind, human, agent) triple and held open until Close. Concurrent
// calls are serialised through the path's stream mutex; writes
// against the underlying file are O_APPEND so the kernel guarantees
// each JSONL line lands atomically up to PIPE_BUF (4 KiB on Linux,
// well above a finding entry's typical size).
//
// human and agent are filename-segment slugs (already sanitised);
// caller is responsible for resolving them via identity.ResolveOperator
// and identity.Resolve.
func (p *Path) AppendFindingEntry(e schema.FindingEntry, human, agent string) error {
	if e.Kind == "" {
		e.Kind = schema.SubjectFinding
	}
	return p.appendEntry(ArtifactFindings, human, agent, e)
}

// AppendReviewEntry appends one ReviewEntry to the appropriate
// reviews_*.jsonl stream. The entry is validated through
// schema.ValidateReviewEntry before any I/O — invalid entries are
// rejected at the boundary so a malformed line can't make it to
// disk and confuse the resolver later.
func (p *Path) AppendReviewEntry(e schema.ReviewEntry, human, agent string) error {
	if err := schema.ValidateReviewEntry(e); err != nil {
		return err
	}
	return p.appendEntry(ArtifactReviews, human, agent, e)
}

// AppendOutcomeEntry appends one OutcomeEntry to the appropriate
// outcomes_*.jsonl stream. Like AppendReviewEntry, the entry is
// validated first.
func (p *Path) AppendOutcomeEntry(e schema.OutcomeEntry, human, agent string) error {
	if err := schema.ValidateOutcomeEntry(e); err != nil {
		return err
	}
	return p.appendEntry(ArtifactOutcomes, human, agent, e)
}

// Close flushes and closes every artifact stream opened by Append*.
// Safe to call multiple times. CLI single-shot commands `defer
// rp.Close()` so the buffered writes hit disk before the process
// exits; the UI server calls it at the end of each request that
// opened a Path (until session-scoped Path caching lands).
func (p *Path) Close() error {
	p.streamsMu.Lock()
	defer p.streamsMu.Unlock()
	var firstErr error
	for k, s := range p.streams {
		if err := s.f.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %s_%s_%s: %w", k.kind, k.human, k.agent, err)
		}
		delete(p.streams, k)
	}
	return firstErr
}

// appendEntry is the shared implementation: look up (or open) the
// file for this triple, encode v with a trailing newline. JSON-Encoder
// already writes a newline after each value, so the on-disk shape is
// one entry per line.
//
// The streamsMu lock is held through the encode (not just the
// open/lookup) because json.Encoder is not safe for concurrent use:
// two goroutines encoding into the same encoder would interleave
// bytes on the wire. The lock also blocks Close, so a goroutine
// can't close the file from under an in-flight encode.
func (p *Path) appendEntry(kind ArtifactKind, human, agent string, v any) error {
	p.streamsMu.Lock()
	defer p.streamsMu.Unlock()
	s, err := p.streamForLocked(kind, human, agent)
	if err != nil {
		return err
	}
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("append %s entry: %w", kind, err)
	}
	return nil
}

// streamForLocked returns the cached stream for (kind, human, agent),
// opening a fresh file on first use. Caller must hold streamsMu.
//
// The filename is fixed at first open — subsequent appends keep
// adding to the same file even if minutes have passed, so "one file
// per process invocation" holds.
func (p *Path) streamForLocked(kind ArtifactKind, human, agent string) (*stream, error) {
	if p.streams == nil {
		p.streams = make(map[streamKey]*stream)
	}
	key := streamKey{kind: kind, human: human, agent: agent}
	if s, ok := p.streams[key]; ok {
		return s, nil
	}
	name, err := ArtifactFilename(kind, time.Now().UTC(), human, agent)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(p.dir, name)
	// O_EXCL guards against the (vanishingly rare) microsecond-level
	// collision: if a file with this name already exists, we bail
	// rather than risk merging two sessions' streams.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &stream{f: f, enc: json.NewEncoder(f)}
	p.streams[key] = s
	return s, nil
}

