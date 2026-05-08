package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/ui/templates"
	"github.com/go-chi/chi/v5"
)

// groupHandler renders one group's detail page. The group's findings
// live in the *input* run, not in the group run itself, so the
// handler walks `manifest.InputRun` to resolve them. Member findings
// that no longer resolve (input run was deleted, or the finding was
// edited out by hand) render as "missing" rather than failing the
// page.
func groupHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		id := chi.URLParam(r, "id")
		// Group ids carry a "g_" prefix per schema.NewGroupID, but the
		// regex is the same character class — both names and ids are
		// safe to join into a filesystem path after this check.
		if !runNamePattern.MatchString(name) || !findingIDPattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		runDir := filepath.Join(projectDir, "runs", name)
		if _, err := os.Stat(filepath.Join(runDir, "run.json")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, fmt.Sprintf("stat run: %v", err), http.StatusInternalServerError)
			return
		}

		rp, err := run.Open(runDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("open run: %v", err), http.StatusInternalServerError)
			return
		}
		manifest, err := rp.Manifest()
		if err != nil {
			http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
			return
		}
		if manifest.Stage != "group" {
			http.Error(w, fmt.Sprintf("groups not present on %s runs", manifest.Stage), http.StatusBadRequest)
			return
		}

		groups, err := rp.LoadGroups()
		if err != nil {
			http.Error(w, fmt.Sprintf("load groups: %v", err), http.StatusInternalServerError)
			return
		}
		var found *schema.Group
		for i := range groups {
			if groups[i].ID == id {
				found = &groups[i]
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}

		members, inputRunName, missing := resolveGroupMembers(projectDir, manifest.InputRun, found.FindingIDs)

		subject := schema.Subject{Kind: schema.SubjectGroup, ID: found.ID}
		reviewView, err := buildReviewView(rp, name, subject)
		if err != nil {
			http.Error(w, fmt.Sprintf("load reviews: %v", err), http.StatusInternalServerError)
			return
		}
		// buildReviewView already seeds InitialLabels with the
		// group's effective labels; nothing more to do here.
		outcomeView, err := buildOutcomeView(rp, name, subject)
		if err != nil {
			http.Error(w, fmt.Sprintf("load outcomes: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view := templates.GroupView{
			Manifest:     manifest,
			Group:        *found,
			InputRunName: inputRunName,
			Members:      members,
			MissingIDs:   missing,
			Review:       reviewView,
			Outcome:      outcomeView,
		}
		if err := templates.GroupDetail(view).Render(r.Context(), w); err != nil {
			fmt.Fprintf(os.Stderr, "fettle ui: render group: %v\n", err)
		}
	}
}

// inputRunPattern enforces the project-relative shape FETTLE.md
// guarantees for manifest.InputRun: literally `runs/<name>` where the
// name matches our run-name slug. Validating here means a corrupt or
// hand-edited manifest with `../foo` or an absolute path can never
// drive a filepath.Join outside the project root — even though the
// downstream `run.Open` would have failed too, defense-in-depth keeps
// the file-read attempt off the dangerous path entirely.
var inputRunPattern = regexp.MustCompile(`^runs/[A-Za-z0-9_-]+$`)

// resolveGroupMembers loads the input run's findings and returns the
// subset whose ids appear in want, in the same order as want. Members
// that don't resolve (input run reference invalid, run gone, finding
// deleted) are returned in missing instead. inputRunName is the leaf
// folder name of the input run, used to build per-finding links;
// empty when inputRun didn't pass validation (the template renders
// the missing list without member-detail links in that case).
func resolveGroupMembers(projectDir, inputRun string, want []string) (members []schema.Finding, inputRunName string, missing []string) {
	if !inputRunPattern.MatchString(inputRun) {
		return nil, "", want
	}
	inputRunName = filepath.Base(inputRun)
	inputAbs := filepath.Join(projectDir, inputRun)
	rp, err := run.Open(inputAbs)
	if err != nil {
		return nil, inputRunName, want
	}
	all, err := rp.LoadFindings()
	if err != nil {
		return nil, inputRunName, want
	}
	byID := make(map[string]schema.Finding, len(all))
	for _, f := range all {
		byID[f.ID] = f
	}
	for _, id := range want {
		if f, ok := byID[id]; ok {
			members = append(members, f)
		} else {
			missing = append(missing, id)
		}
	}
	return members, inputRunName, missing
}
