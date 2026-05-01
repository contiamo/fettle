package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/spf13/cobra"
)

var runMergeFlags struct {
	runs []string
	name string
}

var runMergeCmd = &cobra.Command{
	Use:   "merge",
	Short: "Concatenate multiple non-overlapping runs into one",
	Long: `merge copies findings (and review/outcome attachments) from
two or more input runs into a new merge run. Harness-only — no agent
invocation. Each finding gets a fresh id and a single-element
members[] entry pointing at its source. Source reviews are
propagated verbatim with subject ids remapped.

Use this for non-overlapping inputs (e.g. a find run on **/*.go and
another on **/*.ts). For overlapping inputs (same code scanned by
two different agents), use ` + "`fettle run dedupe`" + ` instead — merge
will warn on exact (file, line, title) duplicates but won't collapse
them.`,
	RunE: runRunMerge,
}

func init() {
	runMergeCmd.Flags().StringSliceVar(&runMergeFlags.runs, "run", nil, "input run folder (repeatable; at least one required)")
	runMergeCmd.Flags().StringVar(&runMergeFlags.name, "name", "", "human label appended to the run folder timestamp")
	_ = runMergeCmd.MarkFlagRequired("run")
	runCmd.AddCommand(runMergeCmd)
}

func runRunMerge(cmd *cobra.Command, args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir, err := projectDir()
	if err != nil {
		return err
	}
	if len(runMergeFlags.runs) == 0 {
		return fmt.Errorf("at least one --run is required")
	}

	// Resolve and validate input runs.
	inputs := make([]inputRun, 0, len(runMergeFlags.runs))
	relInputs := make([]string, 0, len(runMergeFlags.runs))
	for _, raw := range runMergeFlags.runs {
		abs := raw
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, raw)
		}
		rp, err := run.Open(abs)
		if err != nil {
			return fmt.Errorf("open %s: %w", raw, err)
		}
		m, err := rp.Manifest()
		if err != nil {
			return fmt.Errorf("read %s manifest: %w", raw, err)
		}
		if m.CompletedAt == nil {
			return fmt.Errorf("input run %s is not completed (run.json missing completed_at) — wait for it to finish or delete and re-run", raw)
		}
		rel, err := filepath.Rel(dir, abs)
		if err != nil {
			rel = abs
		}
		inputs = append(inputs, inputRun{abs: abs, rel: filepath.ToSlash(rel), manifest: m})
		relInputs = append(relInputs, filepath.ToSlash(rel))
	}

	out, err := run.CreateForMerge(run.CreateMergeOpts{
		ProjectDir: dir,
		Slug:       runMergeFlags.name,
		InputRuns:  relInputs,
	})
	if err != nil {
		return fmt.Errorf("create merge run: %w", err)
	}
	logger.Info("plan", "out", out.Dir(), "inputs", len(inputs))

	// Pass 1: copy findings, build id remap (old id → new id) per input.
	idRemap := make(map[string]map[string]string, len(inputs))
	dupKeys := map[string][]string{} // (file|line|title) → [old ids that already had this key]
	totalCopied := 0
	for _, in := range inputs {
		remap := map[string]string{}
		idRemap[in.abs] = remap

		findings, err := loadFindingsFromRun(in.abs)
		if err != nil {
			return fmt.Errorf("read findings from %s: %w", in.rel, err)
		}
		for _, f := range findings {
			key := f.File + "|" + fmt.Sprint(f.Line) + "|" + strings.TrimSpace(f.Title)
			dupKeys[key] = append(dupKeys[key], in.rel+":"+f.ID)

			oldID := f.ID
			f.ID = schema.NewFindingID()
			f.Members = []schema.Member{{FindingID: oldID, FromRun: in.rel}}
			if err := out.AppendFinding(f); err != nil {
				return fmt.Errorf("append finding from %s: %w", in.rel, err)
			}
			remap[oldID] = f.ID
			totalCopied++
		}
	}

	// Warn on exact (file, line, title) duplicates across inputs.
	dupCount := 0
	for _, sources := range dupKeys {
		if len(sources) > 1 {
			dupCount++
			logger.Warn("duplicate (file, line, title) across inputs — consider `fettle run dedupe` instead",
				"sources", sources)
		}
	}

	// Pass 2: copy reviews, remapping subject ids.
	reviewsCopied := 0
	for _, in := range inputs {
		n, err := copyReviewsRemapped(in.abs, out.Dir(), idRemap[in.abs])
		if err != nil {
			return fmt.Errorf("copy reviews from %s: %w", in.rel, err)
		}
		reviewsCopied += n
	}

	if err := out.MarkCompleted(); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	logger.Info("complete",
		"out", out.Dir(),
		"findings", totalCopied,
		"reviews", reviewsCopied,
		"dup_keys", dupCount,
	)
	_ = printRunResult(out.Dir())
	return nil
}

type inputRun struct {
	abs      string
	rel      string
	manifest schema.RunManifest
}

func loadFindingsFromRun(runDir string) ([]schema.Finding, error) {
	f, err := os.Open(filepath.Join(runDir, "findings.jsonl"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var out []schema.Finding
	for sc.Scan() {
		var fnd schema.Finding
		if err := json.Unmarshal(sc.Bytes(), &fnd); err != nil {
			continue
		}
		out = append(out, fnd)
	}
	return out, sc.Err()
}

// copyReviewsRemapped finds every reviews_<author>.jsonl in srcDir,
// rewrites each entry's subject.id via idMap, and appends the
// remapped entry to the same-named file in outDir. Entries whose
// subject.id isn't in idMap are skipped (they referred to a finding
// not present in the source run, which shouldn't happen but is safe
// to ignore).
func copyReviewsRemapped(srcDir, outDir string, idMap map[string]string) (int, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "reviews_") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		n, err := remapAndAppendReviewFile(filepath.Join(srcDir, e.Name()), filepath.Join(outDir, e.Name()), idMap)
		if err != nil {
			return count, err
		}
		count += n
	}
	return count, nil
}

func remapAndAppendReviewFile(srcPath, dstPath string, idMap map[string]string) (int, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer dst.Close()

	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	enc := json.NewEncoder(dst)
	count := 0
	for sc.Scan() {
		var r schema.Review
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		newID, ok := idMap[r.Subject.ID]
		if !ok {
			continue
		}
		r.Subject.ID = newID
		if err := enc.Encode(r); err != nil {
			return count, err
		}
		count++
	}
	return count, sc.Err()
}
