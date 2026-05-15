package run

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ArtifactKind names the three JSONL streams fettle writes into a run
// directory. Files are named `<kind>_<datetime>_<human>[_<agent>].jsonl`
// — one file per CLI / UI process invocation that writes the stream,
// opened lazily on first append.
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

// ArtifactTimeFormat is the timestamp layout used in artifact
// filenames: extended date + basic time + microsecond fraction + Z,
// in UTC. Chosen so directory listings sort chronologically by name
// alone, no `:` appears (which several filesystems reject), and two
// CLI invocations launched in the same second still get distinct
// filenames — second-precision would collide under bulk scripted use.
const ArtifactTimeFormat = "2006-01-02T150405.000000Z"

// artifactSlugRe is the character class allowed inside the human /
// agent segments of an artifact filename. Underscore is excluded
// because `_` is the field separator; everything else from the
// fettle slug class is allowed. The agent segment additionally
// holds the agent's `name-model` form (the `/` in `name/model` is
// rendered as `-` in filenames — lossy on parse, but the
// authoritative stamp lives inside each JSONL entry).
var artifactSlugRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// artifactNameRe parses an artifact filename back into its parts.
// The regex enforces the format end-to-end so callers can rely on a
// match to mean "every field is well-formed" without re-validating.
var artifactNameRe = regexp.MustCompile(
	`^(?P<kind>findings|reviews|outcomes)` +
		`_(?P<at>\d{4}-\d{2}-\d{2}T\d{6}\.\d{6}Z)` +
		`_(?P<human>[A-Za-z0-9-]+)` +
		`(?:_(?P<agent>[A-Za-z0-9-]+))?` +
		`\.jsonl$`)

// ArtifactFilename builds a per-session JSONL filename for one of
// the three streams. The human segment is always present (someone
// launched the process); the agent segment is appended only when
// the writer is an agent invocation.
//
//	findings_2026-05-15T103022.123456Z_michael_claude-sonnet.jsonl
//	reviews_2026-05-15T103022.123456Z_michael.jsonl
//	outcomes_2026-05-15T103022.123456Z_michael.jsonl
//
// Returns an error rather than producing a malformed filename when
// any input fails validation, so the caller can surface "won't write
// a file I can't read back" at the boundary instead of leaving an
// untraceable artifact on disk.
func ArtifactFilename(kind ArtifactKind, at time.Time, human, agent string) (string, error) {
	if !kind.IsValid() {
		return "", fmt.Errorf("artifact: unknown kind %q", kind)
	}
	if human == "" {
		return "", fmt.Errorf("artifact: human segment must not be empty")
	}
	if !artifactSlugRe.MatchString(human) {
		return "", fmt.Errorf("artifact: invalid human slug %q: only [A-Za-z0-9-] allowed", human)
	}
	if agent != "" && !artifactSlugRe.MatchString(agent) {
		return "", fmt.Errorf("artifact: invalid agent slug %q: only [A-Za-z0-9-] allowed", agent)
	}
	ts := at.UTC().Format(ArtifactTimeFormat)
	if agent == "" {
		return fmt.Sprintf("%s_%s_%s.jsonl", kind, ts, human), nil
	}
	return fmt.Sprintf("%s_%s_%s_%s.jsonl", kind, ts, human, agent), nil
}

// SanitizeAgentSlug converts the canonical agent identity
// (`<name>` or `<name>/<model>`) into the filename-safe form used
// in the agent segment of an artifact filename. The `/` becomes `-`
// — lossy on parse, but that's fine because the authoritative stamp
// lives inside each JSONL entry's `author` field.
//
// Returns an error if either piece contains characters outside the
// artifact slug class. Empty input returns "" with no error so the
// caller can blindly forward an "unset agent" through.
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
	Kind  ArtifactKind
	At    time.Time
	Human string
	Agent string // "" if absent
}

// ParseArtifactFilename returns the structured form of name. Returns
// (_, false) for any filename that doesn't match the artifact
// format — callers iterating a run directory can skip unrelated
// files without an error path. A genuinely malformed artifact
// filename (matching shape but unparseable datetime) also returns
// false; logging that case is the caller's choice.
func ParseArtifactFilename(name string) (ArtifactMeta, bool) {
	m := artifactNameRe.FindStringSubmatch(name)
	if m == nil {
		return ArtifactMeta{}, false
	}
	at, err := time.Parse(ArtifactTimeFormat, m[artifactNameRe.SubexpIndex("at")])
	if err != nil {
		return ArtifactMeta{}, false
	}
	return ArtifactMeta{
		Kind:  ArtifactKind(m[artifactNameRe.SubexpIndex("kind")]),
		At:    at,
		Human: m[artifactNameRe.SubexpIndex("human")],
		Agent: m[artifactNameRe.SubexpIndex("agent")],
	}, true
}
