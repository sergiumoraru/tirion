package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/sergiumoraru/tirion/internal/diffparse"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type verifyDiffEdit struct {
	file       string
	start, end int
	insertion  bool
	unindexed  bool // binary, mode-only or rename-only: nothing to map to indexed source
	added      bool // newly created text file: no pre-image exists, so nothing is indexed yet
}

// Index coordinates belong to the preimage. Context never constitutes an edit;
// a pure insertion belongs to an existing function only when both sides lie in it.
// Parse failures are *diffparse.Error values: the request body is at fault.
func parseVerifyDiffEdits(diff string) ([]verifyDiffEdit, error) {
	files, err := diffparse.Parse(diff)
	if err != nil {
		return nil, err
	}
	var edits []verifyDiffEdit
	for _, file := range files {
		path := file.Path()
		switch {
		case file.Binary:
			edits = append(edits, verifyDiffEdit{file: path, unindexed: true})
		case file.New:
			edits = append(edits, verifyDiffEdit{file: path, added: true})
		default:
			fileEdits := file.Edits()
			if len(fileEdits) == 0 {
				edits = append(edits, verifyDiffEdit{file: path, unindexed: true})
			}
			for _, edit := range fileEdits {
				edits = append(edits, verifyDiffEdit{file: path, start: edit.Start, end: edit.End, insertion: edit.Insertion})
			}
		}
	}
	return edits, nil
}

// diffResolution is what a diff maps to in the indexed graph.
type diffResolution struct {
	CallerIDs []string
	// Gaps are edits the index cannot account for; they make a verification
	// incomplete.
	Gaps []string
	// Notes are informational (for example added documentation); they never
	// degrade a verdict.
	Notes []string
	// AddedUnchecked lists added files that may hold code or configuration. They
	// have no indexed preimage, so nothing in them was analyzed: Verify treats
	// them as coverage gaps, while Impact reports them as notes (new code has no
	// indexed consumers yet).
	AddedUnchecked []string
	// OnlyAdded is true when every edit created a new text file.
	OnlyAdded bool
}

// addedFileIsInformational reports added files that hold no code or
// configuration Verify would need to inspect: documentation and static assets.
func addedFileIsInformational(file string) bool {
	switch strings.ToLower(path.Base(file)) {
	case "license", "notice", "changelog", "authors", "contributors", ".gitignore", ".gitattributes", ".editorconfig":
		return true
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".md", ".markdown", ".rst", ".adoc", ".txt",
		".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".bmp", ".pdf",
		".woff", ".woff2", ".ttf", ".otf", ".eot", ".mp3", ".mp4", ".mov", ".wav":
		return true
	}
	return false
}

// diffRepoAmbiguousError reports that a diff path exists in several indexed
// repositories and nothing else in the diff disambiguates it.
type diffRepoAmbiguousError struct {
	file       string
	candidates []string
}

func (e *diffRepoAmbiguousError) Error() string {
	return fmt.Sprintf("diff path %q exists in several indexed repositories (%s); pass repo to choose one", e.file, strings.Join(e.candidates, ", "))
}

// inferDiffRepos picks, for every diff path, the indexed repository that owns
// it in the selected workspace. Paths that exist in exactly one repository are
// anchors; a path present in several repositories resolves only when exactly one
// of them is anchored by the rest of the diff.
func (h *Handlers) inferDiffRepos(ctx context.Context, files []string, scope searchWorkspaceScope) (map[string]string, error) {
	candidates := make(map[string][]string, len(files))
	for _, file := range files {
		if _, done := candidates[file]; done || file == "" {
			continue
		}
		rows, err := h.storage.Pool().Query(ctx, `
			SELECT DISTINCT r.name
			FROM files fi JOIN repositories r ON r.id = fi.repo_id
			WHERE fi.path = $1
			  AND ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2) OR ($3::boolean AND fi.snapshot_id IS NULL))
			ORDER BY r.name
			LIMIT 20`, file, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
		if err != nil {
			return nil, fmt.Errorf("diff repository lookup: %w", err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			names = append(names, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		candidates[file] = names
	}
	anchored := map[string]bool{}
	for _, names := range candidates {
		if len(names) == 1 {
			anchored[names[0]] = true
		}
	}
	out := make(map[string]string, len(candidates))
	ordered := make([]string, 0, len(candidates))
	for file := range candidates {
		ordered = append(ordered, file)
	}
	sort.Strings(ordered)
	for _, file := range ordered {
		names := candidates[file]
		switch len(names) {
		case 0:
		case 1:
			out[file] = names[0]
		default:
			var pick []string
			for _, name := range names {
				if anchored[name] {
					pick = append(pick, name)
				}
			}
			if len(pick) != 1 {
				return nil, &diffRepoAmbiguousError{file: file, candidates: names}
			}
			out[file] = pick[0]
		}
	}
	return out, nil
}

func (h *Handlers) resolveVerifyChangedSymbols(ctx context.Context, req ImpactRequest, scope searchWorkspaceScope) ([]string, error) {
	res, err := h.resolveDiffChangedSymbols(ctx, req, scope)
	if err == nil && len(res.Gaps) > 0 {
		err = fmt.Errorf("diff coverage incomplete: %s", strings.Join(res.Gaps, "; "))
	}
	return res.CallerIDs, err
}

// resolveDiffChangedSymbols maps diff edits onto indexed functions. With no
// req.Repo each path is attributed to the repository that indexes it.
func (h *Handlers) resolveDiffChangedSymbols(ctx context.Context, req ImpactRequest, scope searchWorkspaceScope) (diffResolution, error) {
	var res diffResolution
	edits, err := parseVerifyDiffEdits(req.Diff)
	if err != nil {
		return res, err
	}
	if len(edits) == 0 && strings.TrimSpace(req.Diff) != "" {
		res.Gaps = append(res.Gaps, "diff contains no verifiable text edits")
	}
	repoFor := func(string) string { return strings.TrimSpace(req.Repo) }
	if strings.TrimSpace(req.Repo) == "" {
		var paths []string
		for _, edit := range edits {
			if !edit.added {
				paths = append(paths, edit.file)
			}
		}
		inferred, err := h.inferDiffRepos(ctx, paths, scope)
		if err != nil {
			return res, err
		}
		repoFor = func(file string) string { return inferred[file] }
	}
	sawAdded, sawOther := false, false
	var ids []string
	for _, edit := range edits {
		if req.NoTests && isTestLikePath(edit.file) {
			continue
		}
		if edit.added {
			sawAdded = true
			if addedFileIsInformational(edit.file) {
				res.Notes = append(res.Notes, "added file "+edit.file+" not indexed (informational)")
			} else {
				res.AddedUnchecked = append(res.AddedUnchecked, edit.file)
			}
			continue
		}
		sawOther = true
		if edit.unindexed {
			res.Gaps = append(res.Gaps, edit.file+": binary, mode-only or rename-only change has no indexed preimage")
			continue
		}
		repo := repoFor(edit.file)
		if repo == "" {
			res.Gaps = append(res.Gaps, edit.file+": no indexed repository in this workspace contains this path")
			continue
		}
		nextUncovered := edit.start
		coveredInsertion := false
		rows, err := h.storage.Pool().Query(ctx, `
			SELECT r.name, fi.path, f.name, f.start_line, f.end_line
			FROM functions f
			JOIN files fi ON fi.id = f.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE r.name = $1 AND fi.path = $2
			  AND ($5::bigint[] IS NULL OR fi.snapshot_id = ANY($5) OR ($7 AND fi.snapshot_id IS NULL))
			  AND (($6 AND f.start_line <= $3 AND f.end_line >= $4)
			       OR (NOT $6 AND f.start_line <= $4 AND f.end_line >= $3))
			ORDER BY f.start_line, f.name`, repo, edit.file, edit.start, edit.end, activeSnapshotIDs(scope), edit.insertion, workspaceIncludesLegacy(scope))
		if err != nil {
			return res, fmt.Errorf("changed-symbol lookup: %w", err)
		}
		for rows.Next() {
			var repoName, file, name string
			var start, end int
			if err := rows.Scan(&repoName, &file, &name, &start, &end); err != nil {
				rows.Close()
				return res, err
			}
			if edit.insertion {
				coveredInsertion = true
			} else if start <= nextUncovered && end >= nextUncovered {
				nextUncovered = end + 1
			}
			ids = append(ids, trace.BuildCallerID(repoName, file, name))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return res, err
		}
		if (edit.insertion && !coveredInsertion) || (!edit.insertion && nextUncovered <= edit.end) {
			res.Gaps = append(res.Gaps, fmt.Sprintf("%s:%d-%d: edit is not fully covered by indexed functions", edit.file, edit.start, edit.end))
		}
	}
	res.OnlyAdded = sawAdded && !sawOther
	res.CallerIDs = uniqueNonEmptyStrings(ids)
	res.Gaps = uniqueNonEmptyStrings(res.Gaps)
	res.Notes = uniqueNonEmptyStrings(res.Notes)
	res.AddedUnchecked = uniqueNonEmptyStrings(res.AddedUnchecked)
	return res, nil
}

// writeDiffResolutionError maps a diff resolution failure to a response. A diff
// that cannot be read, or whose repository cannot be decided, is the caller's
// problem (400); only server-side failures are reported as incomplete (503).
func writeDiffResolutionError(w http.ResponseWriter, incompleteCode string, err error) {
	var ambiguous *diffRepoAmbiguousError
	switch {
	case diffparse.IsError(err):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "diff cannot be parsed: "+err.Error(), nil)
	case errors.As(err, &ambiguous):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"file": ambiguous.file, "candidates": ambiguous.candidates})
	default:
		writeError(w, http.StatusServiceUnavailable, incompleteCode, err.Error(), nil)
	}
}
