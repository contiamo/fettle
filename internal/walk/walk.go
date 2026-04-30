// Package walk discovers files in the target repo that match the project's
// include/exclude globs.
package walk

import (
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

// alwaysSkip names directories never descended into, regardless of globs.
var alwaysSkip = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"node_modules": true,
}

// Walk returns absolute paths of files under root that match any include
// pattern and no exclude pattern. Patterns are doublestar globs evaluated
// against repo-relative paths with forward slashes.
func Walk(root string, include, exclude []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if alwaysSkip[d.Name()] {
				return fs.SkipDir
			}
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
