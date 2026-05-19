package run

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ArtifactKind names the three JSONL streams fettle writes into a
// run directory. Filenames embed the run's slug + start timestamp so
// every artifact is self-identifying when copied out of its run.
//
// Findings carry no author segment — there is one findings file per
// run, written by whichever agent the find stage spawned. Reviews
// and outcomes carry the writer's author slug at the end of the
// filename so multiple reviewers' files coexist in the same run dir
// and can be shared individually via `cp`.
type ArtifactKind string

const (
	ArtifactFindings ArtifactKind = "findings"
	ArtifactReviews  ArtifactKind = "reviews"
	ArtifactOutcomes ArtifactKind = "outcomes"
)

// IsValid reports whether k is one of the three known stream kinds.
func (k ArtifactKind) IsValid() bool {
	switch k {
	case ArtifactFindings, ArtifactReviews, ArtifactOutcomes:
		return true
	}
	return false
}

// artifactSlugRe is the character class allowed inside the author
// segment of an artifact filename. Mirrors the same rule used for
// run slugs and identity slugs — `_` is excluded because it's the
// field separator in the filename format.
var artifactSlugRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// runStartTsRe matches the compact basic-ISO timestamp embedded in
// both run folder names and artifact filenames.
var runStartTsRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

// artifactNameRe parses an artifact filename back into its parts.
// The regex enforces the format end-to-end so a successful match
// guarantees every field is well-formed. Author is optional —
// findings filenames don't have one; reviews / outcomes do.
var artifactNameRe = regexp.MustCompile(
	`^(?P<kind>findings|reviews|outcomes)` +
		`_(?P<slug>[A-Za-z0-9-]+)` +
		`_(?P<ts>\d{8}T\d{6}Z)` +
		`(?:_(?P<author>[A-Za-z0-9-]+))?` +
		`\.jsonl$`)

// ArtifactFilename builds a per-run JSONL filename. slug is the run
// slug (the `<slug>` in `run_<slug>_<ts>`); runStartTs is the
// timestamp embedded in the same run folder name; author is the
// AuthorSlug of the writer's identity stamp (`michael` for a human,
// `claude` for an agent stamp `agent:claude/sonnet`).
//
// Findings always get an empty author — there's one findings file
// per run regardless of how many writers contributed. Reviews and
// outcomes require a non-empty author so different reviewers'
// streams stay distinct (and shareable by `cp`).
//
//	findings_3cdf6f_20260519T110354Z.jsonl
//	reviews_3cdf6f_20260519T110354Z_michael.jsonl
//	outcomes_3cdf6f_20260519T110354Z_claude.jsonl
//
// Returns an error rather than emitting a malformed name when any
// input fails validation; the caller surfaces "won't write a file
// I can't read back" at the boundary instead of leaving an
// untraceable artifact on disk.
func ArtifactFilename(kind ArtifactKind, slug, runStartTs, author string) (string, error) {
	if !kind.IsValid() {
		return "", fmt.Errorf("artifact: unknown kind %q", kind)
	}
	if slug == "" {
		return "", fmt.Errorf("artifact: slug must not be empty")
	}
	if !artifactSlugRe.MatchString(slug) {
		return "", fmt.Errorf("artifact: invalid slug %q: only [A-Za-z0-9-] allowed", slug)
	}
	if !runStartTsRe.MatchString(runStartTs) {
		return "", fmt.Errorf("artifact: invalid run-start ts %q: want YYYYMMDDTHHMMSSZ", runStartTs)
	}
	switch kind {
	case ArtifactFindings:
		if author != "" {
			return "", fmt.Errorf("artifact: findings filenames don't carry an author segment, got %q", author)
		}
		return fmt.Sprintf("%s_%s_%s.jsonl", kind, slug, runStartTs), nil
	case ArtifactReviews, ArtifactOutcomes:
		if author == "" {
			return "", fmt.Errorf("artifact: %s filenames require an author segment", kind)
		}
		if !artifactSlugRe.MatchString(author) {
			return "", fmt.Errorf("artifact: invalid author slug %q: only [A-Za-z0-9-] allowed", author)
		}
		return fmt.Sprintf("%s_%s_%s_%s.jsonl", kind, slug, runStartTs, author), nil
	}
	return "", fmt.Errorf("artifact: unhandled kind %q", kind)
}

// FormatRunStartTs renders a time.Time in the compact form embedded
// in run folder names and artifact filenames. Used by tests and any
// caller building an artifact name from a wall-clock time rather
// than an existing run folder.
func FormatRunStartTs(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// SanitizeAgentSlug converts the canonical agent identity
// (`<name>` or `<name>/<model>`) into the filename-safe form. The
// `/` becomes `-` so `claude/sonnet` → `claude-sonnet`. Used at the
// boundary when an agent's stamp flows into a filename segment.
// Empty input returns "" with no error so callers can blindly
// forward an unset agent through.
func SanitizeAgentSlug(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	out := strings.ReplaceAll(s, "/", "-")
	if !artifactSlugRe.MatchString(out) {
		return "", fmt.Errorf("artifact: agent slug %q contains characters outside [A-Za-z0-9/-]", s)
	}
	return out, nil
}

// ArtifactMeta is the parsed form of an artifact filename.
type ArtifactMeta struct {
	Kind   ArtifactKind
	Slug   string
	StartTime string
	Author string // "" for findings; non-empty for reviews / outcomes
}

// ParseArtifactFilename returns the structured form of name.
// Returns (_, false) for any filename that doesn't match the
// artifact format — callers iterating a run directory can skip
// unrelated files without an error path.
func ParseArtifactFilename(name string) (ArtifactMeta, bool) {
	m := artifactNameRe.FindStringSubmatch(name)
	if m == nil {
		return ArtifactMeta{}, false
	}
	kind := ArtifactKind(m[artifactNameRe.SubexpIndex("kind")])
	author := m[artifactNameRe.SubexpIndex("author")]
	// Findings filenames must not carry an author; reviews and
	// outcomes must. Reject the shape mismatch so a hand-renamed
	// file can't sneak through the parser.
	if kind == ArtifactFindings && author != "" {
		return ArtifactMeta{}, false
	}
	if (kind == ArtifactReviews || kind == ArtifactOutcomes) && author == "" {
		return ArtifactMeta{}, false
	}
	return ArtifactMeta{
		Kind:      kind,
		Slug:      m[artifactNameRe.SubexpIndex("slug")],
		StartTime: m[artifactNameRe.SubexpIndex("ts")],
		Author:    author,
	}, true
}
