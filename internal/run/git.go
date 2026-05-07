package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/contiamo/fettle/internal/schema"
)

// ReadGit returns the target repo's git HEAD + dirty state, or nil if
// the directory isn't a git repo or git isn't on PATH. Used at run
// creation (snapshot into RunManifest.TargetRepoGit) and by the UI
// at render time to detect when the working tree has drifted from
// the scan-time commit.
func ReadGit(dir string) *schema.GitInfo {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil
	}
	dirty := false
	if status, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil {
		dirty = len(bytes.TrimSpace(status)) > 0
	}
	return &schema.GitInfo{
		Head:  strings.TrimSpace(string(head)),
		Dirty: dirty,
	}
}

// CommitsBetween counts commits reachable from `to` that aren't
// reachable from `from`, via `git rev-list --count from..to`. Returns
// (-1, err) when git isn't on PATH, the directory isn't a git repo,
// or either ref is unknown — callers treat that as "uncomputable" and
// suppress the count rather than failing the whole indicator.
//
// from..to is the standard "commits added on top of from to reach
// to" range. To get diverged-branch counts (ahead, behind), call
// this twice with from/to swapped — see Diverged.
func CommitsBetween(dir, from, to string) (int, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return -1, err
	}
	if from == "" || to == "" {
		return -1, errors.New("from/to required")
	}
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", from+".."+to).Output()
	if err != nil {
		return -1, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1, err
	}
	return n, nil
}

// Diverged returns ahead/behind counts of `to` relative to `from`,
// i.e. ahead = "commits to has that from doesn't", behind = "commits
// from has that to doesn't". Either count is -1 when the underlying
// rev-list failed; the caller decides whether to suppress just the
// missing one or hide the indicator entirely.
func Diverged(dir, from, to string) (ahead, behind int) {
	ahead, _ = CommitsBetween(dir, from, to)
	behind, _ = CommitsBetween(dir, to, from)
	return
}

// sha256ish returns the first 16 hex chars of sha256(s) — a short, stable
// slug for derived filenames.
func sha256ish(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
