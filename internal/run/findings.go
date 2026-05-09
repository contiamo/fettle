package run

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/contiamo/fettle/internal/schema"
)

// findingIDPattern is the validity check for finding ids that flow
// through `internal/run` paths. Mirrors the regex the HTTP layer uses
// for URL params so both surfaces reject the same anti-traversal cases
// before any path-join. Required at the API boundary; the post-open
// `doc.id == filename` check below catches corruption / hand-renames
// after the fact, but pre-open validation is the path-traversal guard.
var findingIDPattern = slugRegex

// FindingNotFoundError is returned by LoadFinding / UpdateFinding when
// the requested id has no file. Match with `errors.As(err,
// &FindingNotFoundError{})` to distinguish "id doesn't exist" from
// "filesystem broken" without matching on string content.
type FindingNotFoundError struct{ ID string }

func (e FindingNotFoundError) Error() string {
	return fmt.Sprintf("finding %q not found in run", e.ID)
}

// findingPath builds the per-finding doc path after validating the id.
// Anything outside the finding-id character class is rejected before
// the join — `id="../../etc/passwd"` becomes a hard error, not a path
// outside the run folder.
func (p *Path) findingPath(id string) (string, error) {
	if !findingIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid finding id %q: only [A-Za-z0-9_-] allowed", id)
	}
	return filepath.Join(p.dir, findingsSubdir, id+".json"), nil
}

// FindingExists reports whether `findings/<id>.json` is present in the
// run. Used to validate the --finding flag in `add review` /
// `add outcome` against typos before the mutation hits disk.
func (p *Path) FindingExists(id string) (bool, error) {
	path, err := p.findingPath(id)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// LoadFinding reads one finding doc by id. Hard-errors on a missing
// file (caller asked for a specific id), on a malformed JSON body, or
// on an id mismatch between the filename and the embedded doc.id —
// the last guards against hand-renames and corrupt half-writes.
func (p *Path) LoadFinding(id string) (schema.FindingDoc, error) {
	path, err := p.findingPath(id)
	if err != nil {
		return schema.FindingDoc{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return schema.FindingDoc{}, FindingNotFoundError{ID: id}
		}
		return schema.FindingDoc{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc schema.FindingDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return schema.FindingDoc{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.ID != id {
		return schema.FindingDoc{}, fmt.Errorf("finding %q has mismatched id %q in body — corrupt or hand-renamed file", id, doc.ID)
	}
	return doc, nil
}

// ListFindingIDs returns every finding id present in the run, in
// lexical order so output is deterministic. Empty findings/ directory
// (or missing dir, e.g. on a freshly-created run that hasn't taken its
// first finding yet) returns an empty slice with no error.
//
// `.tmp` files left from a crashed UpdateFinding are skipped; they're
// either in-flight from another process or harmless leftovers.
func (p *Path) ListFindingIDs() ([]string, error) {
	dir := filepath.Join(p.dir, findingsSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !findingIDPattern.MatchString(id) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// LoadAllFindings reads every finding doc in the run. Tolerant: a
// single corrupt or unparseable file is logged to stderr and skipped,
// matching the JSONL-era behaviour where a torn append shouldn't
// fault the whole UI page. Use LoadFinding(id) when the caller asked
// for a specific finding and a parse failure should surface.
func (p *Path) LoadAllFindings() ([]schema.FindingDoc, error) {
	ids, err := p.ListFindingIDs()
	if err != nil {
		return nil, err
	}
	out := make([]schema.FindingDoc, 0, len(ids))
	for _, id := range ids {
		doc, err := p.LoadFinding(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fettle: skipping malformed finding %q: %v\n", id, err)
			continue
		}
		out = append(out, doc)
	}
	return out, nil
}

// WriteFinding creates `findings/<id>.json` for a brand-new finding.
// The publish step uses `os.Link(2)` (atomic create-only on POSIX) so
// two concurrent WriteFinding calls on the same id can't both
// succeed — the loser sees `fs.ErrExist`. Use UpdateFinding to
// mutate an existing doc.
func (p *Path) WriteFinding(doc schema.FindingDoc) error {
	if doc.ID == "" {
		return fmt.Errorf("WriteFinding: doc.ID is empty")
	}
	path, err := p.findingPath(doc.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return exclusiveWriteJSON(path, doc, doc.ID)
}

// UpdateFinding reads the doc, calls mut, and writes the result back
// atomically. The mutator must not move the doc to a different id —
// the path is fixed by the id passed in.
//
// Concurrency: the read-modify-write is NOT cross-process locked. Two
// concurrent UpdateFinding calls on the same id can race as
// `read-A, read-B, write-A, write-B` and silently lose A's mutation.
// Acceptable for fettle's single-user laptop use case; the write
// window is sub-millisecond and atomic-rename guarantees the file is
// never observed half-written. If you ever run multiple mutators in
// parallel against the same finding (concurrent reviewers, an agent
// stage that re-touches existing findings, …), wrap UpdateFinding in
// a flock helper.
func (p *Path) UpdateFinding(id string, mut func(*schema.FindingDoc) error) error {
	path, err := p.findingPath(id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FindingNotFoundError{ID: id}
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc schema.FindingDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.ID != id {
		return fmt.Errorf("finding %q has mismatched id %q in body — refusing to write back over corrupt file", id, doc.ID)
	}
	if err := mut(&doc); err != nil {
		return err
	}
	if doc.ID != id {
		return fmt.Errorf("UpdateFinding: mutator changed doc.ID from %q to %q", id, doc.ID)
	}
	return atomicWriteJSON(path, doc)
}

// CountFindingsForFile returns how many findings in the run anchor to
// the given repo-relative file path. Used by the find harness to
// derive the per-file ledger row from the delta of "before vs after
// the agent ran". Tolerates malformed docs (skipped) so a half-written
// file under SIGKILL doesn't poison the count.
func (p *Path) CountFindingsForFile(file string) (int, error) {
	ids, err := p.ListFindingIDs()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		doc, err := p.LoadFinding(id)
		if err != nil {
			continue
		}
		if doc.File == file {
			count++
		}
	}
	return count, nil
}

// exclusiveWriteJSON is atomicWriteJSON's create-only sibling: it
// writes the doc to a unique temp file then `os.Link(2)`s it to
// `path`. Link fails atomically with `fs.ErrExist` if the target is
// already there, so two concurrent WriteFinding calls on the same id
// can't both think they won. Cleans up the temp regardless of
// outcome.
func exclusiveWriteJSON(path string, v any, id string) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	suffix, err := randomTmpSuffix()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, filepath.Base(path)+"."+suffix+".tmp")

	if err := writeTmpAndSync(tmp, data); err != nil {
		return err
	}
	defer os.Remove(tmp) // tmp is a one-shot; the link is the publish step.

	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("finding %q already exists at %s: %w", id, path, err)
		}
		return fmt.Errorf("link %s -> %s: %w", tmp, path, err)
	}
	syncDir(dir)
	return nil
}

// atomicWriteJSON writes v as indented JSON to path through a unique
// per-write temp file, fsyncs, and atomically renames over the
// target. The temp name carries a random hex suffix so concurrent
// writes on the same target don't truncate each other's in-flight
// bytes — the worst race is now a lost-but-complete prior write
// rather than a half-published interleaved file.
//
// Stale `.tmp` files from a crash are filtered out by ListFindingIDs;
// no explicit cleanup pass is needed.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	suffix, err := randomTmpSuffix()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, filepath.Base(path)+"."+suffix+".tmp")

	if err := writeTmpAndSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	syncDir(dir)
	return nil
}

// writeTmpAndSync writes payload to tmp under O_EXCL, syncs, closes.
// On any failure path the tmp is best-effort removed so a stale
// half-written file doesn't pile up — though ListFindingIDs would
// filter it out anyway.
func writeTmpAndSync(tmp string, data []byte) error {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	return nil
}

// syncDir best-effort fsyncs a directory so an in-flight rename or
// link survives a crash. Failures are swallowed: the change is
// already visible in the page cache, and not every filesystem
// supports directory fsync.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// randomTmpSuffix returns 8 hex chars from 4 random bytes, suitable
// for tagging a one-shot temp filename. Cryptographic strength is
// overkill for this purpose; we use crypto/rand because the std lib
// is the path of least resistance and the call is rare.
func randomTmpSuffix() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random tmp suffix: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
