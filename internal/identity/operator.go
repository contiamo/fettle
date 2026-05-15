package identity

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

// ResolveOperator returns the human slug for the person who launched
// the current fettle process, independent of whether an agent is
// being run on their behalf. This is what gets stamped into the
// `<human>` segment of artifact filenames
// (`<kind>_<datetime>_<human>[_<agent>].jsonl`) so a directory listing
// is attributable to a real person regardless of which agent (if any)
// did the writing.
//
// Resolution chain — first non-empty wins:
//
//  1. $FETTLE_AUTHOR — explicit per-invocation override (set by the
//     operator before launching an agent stage).
//  2. ~/.config/fettle/identity — the slug the UI persisted.
//  3. OS user (`os/user.Current()`) — sanitised through
//     `sanitiseToSlug`. The fallback exists so brand-new installs and
//     stage scripts that haven't run `fettle ui` once still produce a
//     valid filename instead of a hard error.
//
// The returned slug always matches the artifact slug character class
// (`[A-Za-z0-9-]+`); sources that don't are sanitised, and if even
// after sanitisation the slug would be empty, returns ErrNoIdentity.
//
// Resolve() vs ResolveOperator(): Resolve returns whoever fettle
// should *attribute* the write to (`agent:<name>` when running as an
// agent, otherwise the human); ResolveOperator always returns the
// human regardless. The two diverge inside agent stages and converge
// for plain human invocations.
func ResolveOperator() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvAuthor)); v != "" {
		if s := sanitiseToSlug(v); s != "" {
			return s, nil
		}
	}
	path, perr := configFilePath()
	if perr == nil {
		if data, err := os.ReadFile(path); err == nil {
			if s := sanitiseToSlug(strings.TrimSpace(string(data))); s != "" {
				return s, nil
			}
		}
	}
	if u, err := user.Current(); err == nil && u != nil {
		if s := sanitiseToSlug(u.Username); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("%w (and OS user lookup yielded no usable slug)", ErrNoIdentity)
}

// sanitiseToSlug downgrades arbitrary identifiers (env values, OS
// usernames) to the artifact slug character class: drop everything
// outside `[A-Za-z0-9-]`, collapse runs of `-`, trim leading/trailing
// `-`. Returns "" when nothing survived (e.g. an all-punctuation
// input).
func sanitiseToSlug(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			// Treat common separators as "merge to a single dash" so
			// `michael.dietze`, `michael_dietze`, and `michael dietze`
			// all collapse to `michael-dietze`. `_` in particular has
			// to map to `-` because `_` is the artifact field
			// separator.
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	return out
}
