package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LookupRun resolves a `--run` argument into an absolute run
// directory path. The argument can take several shapes; each
// resolves to one canonical absolute path or an error.
//
//	# Absolute path: used as-is. No project discovery needed.
//	/abs/path/to/project/runs/run_3cdf6f_20260519T110354Z
//
//	# Relative path (contains a separator): joined to cwd. No
//	# project discovery needed.
//	./runs/run_3cdf6f_20260519T110354Z
//	../sibling/runs/run_3cdf6f_20260519T110354Z
//
//	# Bare run dir name. Looked up under <project>/runs/.
//	run_3cdf6f_20260519T110354Z
//
//	# Slug only — the short-reference UX. Looked up under
//	# <project>/runs/, matching the slug exactly first and then by
//	# prefix when unambiguous.
//	3cdf6f
//
// findProject is called lazily — only when the ref shape requires
// resolving against the project's runs/ dir. Callers that don't
// have a way to discover the project (or don't want one to be
// required) pass a function returning an error; LookupRun surfaces
// that error only on the lookup branches that need it. Multiple
// matches produce an "ambiguous" error listing the candidates so
// the caller can disambiguate.
//
// ErrRunNotFound is returned when none of the lookup branches
// matches; callers can test for it via errors.Is.
func LookupRun(ref string, findProject func() (string, error)) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("run reference is empty")
	}
	if filepath.IsAbs(ref) {
		return ref, nil
	}
	if strings.ContainsRune(ref, filepath.Separator) {
		// Relative path: resolve from cwd. Matches what `--run
		// ./runs/foo` would mean to a shell-typing user.
		abs, err := filepath.Abs(ref)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", ref, err)
		}
		return abs, nil
	}
	// Bare name or slug — needs the project dir.
	projectDir, err := findProject()
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", ref, err)
	}
	runsDir := filepath.Join(projectDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: project %s has no runs/ directory", ErrRunNotFound, projectDir)
		}
		return "", fmt.Errorf("read %s: %w", runsDir, err)
	}

	// First pass: exact dir name match (e.g. ref =
	// "run_3cdf6f_20260519T110354Z").
	for _, e := range entries {
		if e.IsDir() && e.Name() == ref {
			return filepath.Join(runsDir, e.Name()), nil
		}
	}

	// Second pass: collect every run whose slug equals or starts
	// with ref. Exact slug matches are preferred when present.
	var exactSlugMatch []string
	var prefixMatches []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug, _, ok := ParseRunName(e.Name())
		if !ok {
			continue
		}
		switch {
		case slug == ref:
			exactSlugMatch = append(exactSlugMatch, filepath.Join(runsDir, e.Name()))
		case strings.HasPrefix(slug, ref):
			prefixMatches = append(prefixMatches, filepath.Join(runsDir, e.Name()))
		}
	}
	if len(exactSlugMatch) == 1 {
		return exactSlugMatch[0], nil
	}
	if len(exactSlugMatch) > 1 {
		return "", ambiguousRunError(ref, exactSlugMatch)
	}
	if len(prefixMatches) == 1 {
		return prefixMatches[0], nil
	}
	if len(prefixMatches) > 1 {
		return "", ambiguousRunError(ref, prefixMatches)
	}
	return "", fmt.Errorf("%w: no run matches %q in %s", ErrRunNotFound, ref, runsDir)
}

// ErrRunNotFound is returned by LookupRun when no run matches the
// reference. Match with errors.Is so the CLI can produce a nicer
// message than the wrapped lookup error.
var ErrRunNotFound = errors.New("run not found")

func ambiguousRunError(ref string, matches []string) error {
	bases := make([]string, len(matches))
	for i, m := range matches {
		bases[i] = filepath.Base(m)
	}
	return fmt.Errorf("ambiguous run reference %q matches %d runs: %s — disambiguate by passing a longer slug or the full run name", ref, len(matches), strings.Join(bases, ", "))
}
