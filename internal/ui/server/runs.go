package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/ui/templates"
)

// runsHandler renders the run picker — the project's landing page.
// Mirrors the data path that `fettle list runs` takes (run.Summarize
// over each subdirectory of runs/), so what the UI shows is exactly
// what the CLI emits.
func runsHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summaries, err := loadRunSummaries(projectDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("read runs: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := templates.Runs(projectDir, summaries).Render(r.Context(), w); err != nil {
			// Headers are already flushed by templ's first write, so
			// this can only log — there's nothing useful to send back
			// to the client at this point.
			fmt.Fprintf(os.Stderr, "fettle ui: render runs: %v\n", err)
		}
	}
}

func loadRunSummaries(projectDir string) ([]run.Summary, error) {
	runsDir := filepath.Join(projectDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []run.Summary{}, nil
		}
		return nil, err
	}
	summaries := make([]run.Summary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := run.Summarize(filepath.Join(runsDir, e.Name()))
		if err != nil {
			// Same policy as `fettle list runs`: silently skip subdirs
			// that don't look like fettle runs.
			continue
		}
		summaries = append(summaries, s)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return summaries, nil
}
