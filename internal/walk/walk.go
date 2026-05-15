// Package walk discovers files in the target repo that match the project's
// include/exclude globs. Two walkers are offered: WalkGit honours
// `.gitignore` by going through `git ls-files`, and WalkFS walks the
// filesystem directly with only the user's globs as filtering.
package walk

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

// WalkGit returns absolute paths of files in root that match the
// include / exclude globs *and* are not ignored by git. Concretely,
// it asks git for the union of tracked files and untracked-but-not-
// ignored files, then applies the user's globs on top.
//
// Requires root to be the top of a git repository; returns an error
// if it isn't. Use WalkFS for non-git targets.
func WalkGit(root string, include, exclude []string) ([]string, error) {
	if !isGitRepo(root) {
		return nil, fmt.Errorf("walker=git but %s is not a git repository (no .git entry at top level); set walker=fs in fettle.json or `git init` the target", root)
	}
	rels, err := gitListFiles(root)
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	return filterPaths(root, rels, include, exclude), nil
}

// WalkFS walks the filesystem under root and returns absolute paths
// of files matching the include / exclude globs. The user's globs
// are the only filter — nothing is hard-skipped. Use for non-git
// targets, or when you explicitly want to scan files that are
// gitignored.
func WalkFS(root string, include, exclude []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			if rel != "." && matchesAny(rel, exclude) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if !matchesAny(rel, include) {
			return nil
		}
		if matchesAny(rel, exclude) {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// isGitRepo reports whether root is the top of a git repository.
// Accepts both a `.git` directory (normal repo) and a `.git` file
// (worktree or submodule pointer).
func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// gitListFiles returns repo-relative slash-separated paths of every
// file git considers part of the working tree: tracked files plus
// untracked files that aren't ignored. Deleted-but-still-tracked
// entries are filtered out (file must exist on disk).
func gitListFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files",
		"--others",           // include untracked
		"--cached",           // include tracked
		"--exclude-standard", // honour .gitignore / .git/info/exclude / core.excludesFile
		"-z",                 // NUL-separated for paths with spaces or newlines
	)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	rels := bytes.Split(bytes.TrimRight(out, "\x00"), []byte{0})
	paths := make([]string, 0, len(rels))
	for _, rel := range rels {
		if len(rel) == 0 {
			continue
		}
		rs := string(rel)
		// --cached includes files deleted from disk but still in the
		// index. Skip those — we can't analyze a missing file.
		if _, err := os.Stat(filepath.Join(root, rs)); err != nil {
			continue
		}
		paths = append(paths, filepath.ToSlash(rs))
	}
	return paths, nil
}

// filterPaths applies the user's include/exclude globs to a
// pre-resolved set of repo-relative slash-separated paths,
// returning matching absolute paths in sorted order.
func filterPaths(root string, rels, include, exclude []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		if !matchesAny(rel, include) {
			continue
		}
		if matchesAny(rel, exclude) {
			continue
		}
		out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
	}
	sort.Strings(out)
	return out
}

func matchesAny(relPath string, patterns []string) bool {
	p := filepath.ToSlash(relPath)
	for _, pat := range patterns {
		ok, err := doublestar.Match(pat, p)
		if err == nil && ok {
			return true
		}
	}
	return false
}
