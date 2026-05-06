package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
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

// sha256ish returns the first 16 hex chars of sha256(s) — a short, stable
// slug for derived filenames.
func sha256ish(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
