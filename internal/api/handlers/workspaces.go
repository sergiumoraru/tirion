package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	workspacefs "github.com/sergiumoraru/tirion/internal/workspace"
)

type workspaceSummary struct {
	graph.Workspace
	ActiveSnapshots []graph.WorkspaceSnapshotRef `json:"activeSnapshots"`
	Repos           []graph.WorkspaceRepo        `json:"repos"`
}

type listWorkspacesResponse struct {
	Workspaces []workspaceSummary `json:"workspaces"`
}

type createWorkspaceRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	From        string `json:"from"`
	Default     bool   `json:"default"`
}

type setWorkspaceRepoRefRequest struct {
	Ref string `json:"ref"`
}

type checkoutWorkspaceRepoRequest struct {
	Ref          string `json:"ref"`
	WorktreeRoot string `json:"worktreeRoot"`
}

type bulkCheckoutWorkspaceRequest struct {
	Ref          string `json:"ref"`
	Mode         string `json:"mode"`
	WorktreeRoot string `json:"worktreeRoot"`
}

type bulkCheckoutWorkspaceRepoResult struct {
	Repo    string `json:"repo"`
	Status  string `json:"status"`
	Branch  string `json:"branch,omitempty"`
	Ref     string `json:"ref,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Path    string `json:"path,omitempty"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
}

type bulkCheckoutWorkspaceResponse struct {
	Workspace      workspaceSummary                  `json:"workspace"`
	Ref            string                            `json:"ref"`
	Mode           string                            `json:"mode"`
	Configured     int                               `json:"configured"`
	CheckedOut     int                               `json:"checkedOut"`
	AlreadyCurrent int                               `json:"alreadyCurrent"`
	Skipped        int                               `json:"skipped"`
	Failed         int                               `json:"failed"`
	Results        []bulkCheckoutWorkspaceRepoResult `json:"results"`
}

type indexWorkspaceRepoRequest struct {
	Resolve bool `json:"resolve"`
}

type indexWorkspaceRequest struct {
	Resolve       bool   `json:"resolve"`
	Root          string `json:"root"`
	ParseBinary   string `json:"parseBinary"`
	SkipTests     *bool  `json:"skipTests"`
	SkipUnchanged *bool  `json:"skipUnchanged"`
	Exclude       string `json:"exclude"`
}

type bulkIndexWorkspaceRequest struct {
	Repos   []string `json:"repos"`
	Resolve bool     `json:"resolve"`
}

type bulkIndexWorkspaceRepoResult struct {
	Repo       string `json:"repo"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type workspaceBulkIndexJob struct {
	ID          string                         `json:"id"`
	Workspace   string                         `json:"workspace"`
	Resolve     bool                           `json:"resolve"`
	Status      string                         `json:"status"`
	Total       int                            `json:"total"`
	Completed   int                            `json:"completed"`
	Failed      int                            `json:"failed"`
	CurrentRepo string                         `json:"currentRepo,omitempty"`
	StartedAt   string                         `json:"startedAt"`
	FinishedAt  string                         `json:"finishedAt,omitempty"`
	Results     []bulkIndexWorkspaceRepoResult `json:"results"`
}

type bulkIndexWorkspaceResponse struct {
	Job *workspaceBulkIndexJob `json:"job,omitempty"`
}

func (h *Handlers) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := h.storage.ListWorkspaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	out := make([]workspaceSummary, 0, len(workspaces))
	for _, ws := range workspaces {
		out = append(out, h.workspaceSummary(ws))
	}
	writeJSON(w, http.StatusOK, listWorkspacesResponse{Workspaces: out})
}

func (h *Handlers) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace request", nil)
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if req.Slug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Slug
	}
	ws, err := h.storage.EnsureWorkspace(req.Slug, name, req.Description, "", req.Default)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	cloneFrom := strings.TrimSpace(req.From)
	if cloneFrom == "" && ws.Slug != graph.DefaultWorkspaceSlug {
		cloneFrom = graph.DefaultWorkspaceSlug
	}
	if cloneFrom != "" && cloneFrom != ws.Slug {
		existingRepos, err := h.storage.ListWorkspaceRepos(ws.Slug)
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
			return
		}
		if len(existingRepos) == 0 {
			if err := h.storage.CloneWorkspaceRepoSelections(cloneFrom, ws.ID); err != nil {
				writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, h.workspaceSummary(*ws))
}

func (h *Handlers) SetDefaultWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	if workspaceSlug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	ws, err := h.storage.SetDefaultWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.workspaceSummary(*ws))
}

func (h *Handlers) SetWorkspaceRepoRef(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := r.PathValue("slug")
	repoName := strings.TrimSpace(r.PathValue("repo"))
	if strings.TrimSpace(workspaceSlug) == "" || repoName == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug and repo are required", nil)
		return
	}
	var req setWorkspaceRepoRefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace repo ref request", nil)
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "ref is required", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ws.Slug == graph.DefaultWorkspaceSlug {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "default-main uses the shared repo checkout; use Repo Workspace checkout controls instead", nil)
		return
	}
	wr, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID: ws.ID,
		RepoName:    repoName,
		TargetRef:   ref,
		IndexStatus: "unknown",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, wr)
}

func (h *Handlers) FetchWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	repoName := strings.TrimSpace(r.PathValue("repo"))
	if workspaceSlug == "" || repoName == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug and repo are required", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ws.Slug == graph.DefaultWorkspaceSlug {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "default-main uses the shared repo checkout; use Repo Workspace checkout controls instead", nil)
		return
	}
	repo, err := h.storage.GetRepoByName(repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("repo %q not found", repoName), nil)
		return
	}
	var existing *graph.WorkspaceRepo
	if current, err := h.storage.GetWorkspaceRepo(ws.Slug, repoName); err == nil {
		existing = current
	}
	sourcePath, err := workspaceSourceRepoPath(*repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKSPACE_FETCH_FAILED", err.Error(), nil)
		return
	}
	if err := workspacefs.Fetch(sourcePath); err != nil {
		_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, "", existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID: ws.ID,
			RepoName:    repoName,
			LastError:   err.Error(),
		}))
		writeError(w, http.StatusInternalServerError, "WORKSPACE_FETCH_FAILED", err.Error(), nil)
		return
	}
	wr, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, "", existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID: ws.ID,
		RepoName:    repoName,
		LastError:   "",
	}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, wr)
}

func (h *Handlers) CheckoutWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	repoName := strings.TrimSpace(r.PathValue("repo"))
	if workspaceSlug == "" || repoName == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug and repo are required", nil)
		return
	}
	var req checkoutWorkspaceRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace checkout request", nil)
		return
	}
	if req.WorktreeRoot != "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "worktreeRoot is server-controlled; configure TIRION_WORKTREE_ROOT", nil)
		return
	}

	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	repo, err := h.storage.GetRepoByName(repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("repo %q not found", repoName), nil)
		return
	}

	ref := strings.TrimSpace(req.Ref)
	var existing *graph.WorkspaceRepo
	if current, err := h.storage.GetWorkspaceRepo(ws.Slug, repoName); err == nil {
		existing = current
		if ref == "" {
			ref = strings.TrimSpace(current.TargetRef)
		}
	}
	if ref == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace repo ref is required", nil)
		return
	}

	targetPath, err := safeWorktreePath(workspaceWorktreeRoot(""), ws.Slug, repoName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	sourcePath, err := workspaceSourceRepoPath(*repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKTREE_CHECKOUT_FAILED", err.Error(), nil)
		return
	}
	now := time.Now().UTC()
	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, ref, existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    ws.ID,
		RepoName:       repoName,
		TargetRef:      ref,
		WorktreePath:   targetPath,
		IndexStatus:    "checking_out",
		LastCheckoutAt: &now,
	})); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	if err := workspacefs.EnsureWorktree(sourcePath, targetPath, ref); err != nil {
		_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, ref, existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID:  ws.ID,
			RepoName:     repoName,
			TargetRef:    ref,
			WorktreePath: targetPath,
			IndexStatus:  "failed",
			LastError:    err.Error(),
		}))
		writeError(w, http.StatusInternalServerError, "WORKTREE_CHECKOUT_FAILED", err.Error(), nil)
		return
	}
	status, err := workspacefs.InspectRepo(targetPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKTREE_INSPECT_FAILED", err.Error(), nil)
		return
	}
	resolvedBranch := status.CurrentBranch
	if resolvedBranch == "" || resolvedBranch == "detached" {
		resolvedBranch = ref
	}
	var activeSnapshotID *int64
	var lastIndexedAt *time.Time
	if existing != nil && strings.TrimSpace(existing.ResolvedSHA) == strings.TrimSpace(status.HeadSHA) {
		activeSnapshotID = existing.ActiveSnapshotID
		lastIndexedAt = existing.LastIndexedAt
	}
	checkoutAt := time.Now().UTC()
	wr, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:      ws.ID,
		RepoName:         repoName,
		TargetRef:        ref,
		ResolvedBranch:   resolvedBranch,
		ResolvedSHA:      status.HeadSHA,
		WorktreePath:     targetPath,
		ActiveSnapshotID: activeSnapshotID,
		LastCheckoutAt:   &checkoutAt,
		LastIndexedAt:    lastIndexedAt,
		IndexStatus:      "checked_out",
		LastError:        "",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, wr)
}

func (h *Handlers) BulkCheckoutWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	if workspaceSlug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	var req bulkCheckoutWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace bulk checkout request", nil)
		return
	}
	if req.WorktreeRoot != "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "worktreeRoot is server-controlled; configure TIRION_WORKTREE_ROOT", nil)
		return
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "ref"
	}
	ref := strings.TrimSpace(req.Ref)
	if mode != "ref" && mode != "mainline" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "mode must be ref or mainline", nil)
		return
	}
	if mode == "ref" && ref == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "ref is required", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if mode == "ref" && ws.Slug == graph.DefaultWorkspaceSlug {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "bulk branch checkout requires a non-default workspace", nil)
		return
	}
	repos, err := h.storage.ListRepos()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	root := workspaceWorktreeRoot("")
	defaultBranches := map[int64]string{}
	if mode == "mainline" {
		workspaceState, err := h.storage.ListRepoWorkspaceState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		for repoID, state := range workspaceState {
			defaultBranches[repoID] = strings.TrimSpace(state.SelectedBranch)
		}
		if ws.Slug != graph.DefaultWorkspaceSlug {
			workspaceRepos, err := h.storage.ListWorkspaceRepos(ws.Slug)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
				return
			}
			reposByName := make(map[string]graph.RepoInfo, len(repos))
			for _, repo := range repos {
				reposByName[repo.Name] = repo
			}
			selected := make([]graph.RepoInfo, 0, len(workspaceRepos))
			for _, workspaceRepo := range workspaceRepos {
				if !workspaceRepoNeedsMainlineCheckout(workspaceRepo) {
					continue
				}
				repo, ok := reposByName[workspaceRepo.RepoName]
				if !ok {
					continue
				}
				selected = append(selected, repo)
			}
			repos = selected
		}
	}
	results := h.bulkCheckoutWorkspaceRepos(ws, repos, mode, ref, root, defaultBranches)
	var configured, checkedOut, alreadyCurrent, skipped, failed int
	for _, result := range results {
		switch result.Status {
		case "configured":
			configured++
		case "checked_out":
			checkedOut++
		case "already_current":
			alreadyCurrent++
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}

	writeJSON(w, http.StatusOK, bulkCheckoutWorkspaceResponse{
		Workspace:      h.workspaceSummary(*ws),
		Ref:            ref,
		Mode:           mode,
		Configured:     configured,
		CheckedOut:     checkedOut,
		AlreadyCurrent: alreadyCurrent,
		Skipped:        skipped,
		Failed:         failed,
		Results:        results,
	})
}

func (h *Handlers) bulkCheckoutWorkspaceRepos(ws *graph.Workspace, repos []graph.RepoInfo, mode, ref, root string, defaultBranches map[int64]string) []bulkCheckoutWorkspaceRepoResult {
	const workers = 8
	results := make([]bulkCheckoutWorkspaceRepoResult, len(repos))
	type job struct {
		index int
		repo  graph.RepoInfo
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	workerCount := workers
	if len(repos) < workerCount {
		workerCount = len(repos)
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results[item.index] = h.bulkCheckoutWorkspaceRepo(ws, item.repo, mode, ref, root, defaultBranches)
			}
		}()
	}
	for index, repo := range repos {
		jobs <- job{index: index, repo: repo}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (h *Handlers) bulkCheckoutWorkspaceRepo(ws *graph.Workspace, repo graph.RepoInfo, mode, ref, root string, defaultBranches map[int64]string) bulkCheckoutWorkspaceRepoResult {
	result := bulkCheckoutWorkspaceRepoResult{Repo: repo.Name}
	sourcePath, err := workspaceSourceRepoPath(repo)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	repoRef := ref
	if mode == "mainline" {
		repoRef = defaultBranches[repo.ID]
		if !isMainlineRef(repoRef) {
			repoRef = ""
		}
		if repoRef == "" {
			if status, err := workspacefs.InspectRepo(sourcePath); err == nil {
				repoRef = strings.TrimSpace(status.CurrentBranch)
				if !isMainlineRef(repoRef) {
					repoRef = ""
				}
			}
		}
		if repoRef == "" || repoRef == "detached" {
			for _, candidate := range []string{"master", "main"} {
				if _, _, err := resolveWorkspaceRefFast(sourcePath, candidate); err == nil {
					repoRef = candidate
					break
				}
			}
		}
		if repoRef == "" || repoRef == "detached" {
			result.Status = "skipped"
			result.Error = "mainline ref could not be determined"
			return result
		}
	}
	result.Ref = repoRef
	var existing *graph.WorkspaceRepo
	if current, err := h.storage.GetWorkspaceRepo(ws.Slug, repo.Name); err == nil {
		existing = current
	}
	if mode == "mainline" && ws.Slug == graph.DefaultWorkspaceSlug {
		return h.bulkCheckoutDefaultMainRepo(ws, repo, repoRef)
	}
	commit, warning, err := resolveWorkspaceRefFast(sourcePath, repoRef)
	if err != nil {
		result.Status = "skipped"
		result.Error = fmt.Sprintf("ref %q not found", repoRef)
		return result
	}
	result.Warning = warning

	targetPath, err := safeWorktreePath(root, ws.Slug, repo.Name)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if existing != nil &&
		strings.TrimSpace(existing.TargetRef) == repoRef &&
		strings.TrimSpace(existing.ResolvedSHA) == strings.TrimSpace(commit) &&
		workspaceRepoHasIndexedCommit(existing, commit) &&
		!(mode == "mainline" && ws.Slug != graph.DefaultWorkspaceSlug && existing.ActiveSnapshotID == nil) {
		if strings.TrimSpace(existing.WorktreePath) == "" {
			result.Status = "already_current"
			result.Branch = existing.ResolvedBranch
			if result.Branch == "" {
				result.Branch = repoRef
			}
			result.SHA = commit
			result.Path = existing.WorktreePath
			return result
		}
		if status, err := workspacefs.InspectRepo(existing.WorktreePath); err == nil && !status.Dirty && strings.TrimSpace(status.HeadSHA) == strings.TrimSpace(commit) {
			result.Status = "already_current"
			result.Branch = existing.ResolvedBranch
			if result.Branch == "" {
				result.Branch = repoRef
			}
			result.SHA = status.HeadSHA
			result.Path = existing.WorktreePath
			return result
		}
	}

	now := time.Now().UTC()
	if ws.Slug != graph.DefaultWorkspaceSlug {
		return h.configureWorkspaceRepoRef(ws, repo, mode, repoRef, commit, warning, existing, now)
	}
	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, repoRef, existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    ws.ID,
		RepoName:       repo.Name,
		TargetRef:      repoRef,
		WorktreePath:   targetPath,
		IndexStatus:    "checking_out",
		LastCheckoutAt: &now,
		LastError:      "",
	})); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	if err := workspacefs.EnsureWorktreeAtCommit(sourcePath, targetPath, commit); err != nil {
		_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, repoRef, existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID:  ws.ID,
			RepoName:     repo.Name,
			TargetRef:    repoRef,
			WorktreePath: targetPath,
			IndexStatus:  "failed",
			LastError:    err.Error(),
		}))
		result.Status = "failed"
		result.Path = targetPath
		result.Error = err.Error()
		return result
	}
	status, err := workspacefs.InspectRepo(targetPath)
	if err != nil {
		_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, repoRef, existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID:  ws.ID,
			RepoName:     repo.Name,
			TargetRef:    repoRef,
			WorktreePath: targetPath,
			IndexStatus:  "failed",
			LastError:    err.Error(),
		}))
		result.Status = "failed"
		result.Path = targetPath
		result.Error = err.Error()
		return result
	}
	resolvedBranch := status.CurrentBranch
	if resolvedBranch == "" || resolvedBranch == "detached" {
		resolvedBranch = repoRef
	}
	var activeSnapshotID *int64
	var lastIndexedAt *time.Time
	if existing != nil && strings.TrimSpace(existing.ResolvedSHA) == strings.TrimSpace(status.HeadSHA) {
		activeSnapshotID = existing.ActiveSnapshotID
		lastIndexedAt = existing.LastIndexedAt
	}
	mainlineSnapshotID, mainlineResolvedSHA, mainlineLastIndexedAt := workspaceRepoMainlineState(existing)
	if mode == "mainline" && ws.Slug != graph.DefaultWorkspaceSlug && activeSnapshotID == nil {
		if existing != nil &&
			existing.MainlineSnapshotID != nil &&
			strings.TrimSpace(existing.MainlineResolvedSHA) == strings.TrimSpace(status.HeadSHA) {
			activeSnapshotID = existing.MainlineSnapshotID
			lastIndexedAt = existing.MainlineLastIndexedAt
		} else if inherited, err := h.storage.GetWorkspaceRepo(graph.DefaultWorkspaceSlug, repo.Name); err == nil &&
			strings.TrimSpace(inherited.ResolvedSHA) == strings.TrimSpace(status.HeadSHA) &&
			inherited.ActiveSnapshotID != nil {
			activeSnapshotID = inherited.ActiveSnapshotID
			lastIndexedAt = inherited.LastIndexedAt
		}
	}
	if mode == "mainline" && activeSnapshotID != nil {
		mainlineSnapshotID = activeSnapshotID
		mainlineResolvedSHA = status.HeadSHA
		mainlineLastIndexedAt = lastIndexedAt
	}
	checkoutAt := time.Now().UTC()
	if _, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:           ws.ID,
		RepoName:              repo.Name,
		TargetRef:             repoRef,
		ResolvedBranch:        resolvedBranch,
		ResolvedSHA:           status.HeadSHA,
		WorktreePath:          targetPath,
		ActiveSnapshotID:      activeSnapshotID,
		MainlineSnapshotID:    mainlineSnapshotID,
		MainlineResolvedSHA:   mainlineResolvedSHA,
		MainlineLastIndexedAt: mainlineLastIndexedAt,
		LastCheckoutAt:        &checkoutAt,
		LastIndexedAt:         lastIndexedAt,
		IndexStatus:           "checked_out",
		LastError:             "",
	}); err != nil {
		result.Status = "failed"
		result.Path = targetPath
		result.Error = err.Error()
		return result
	}
	result.Status = "checked_out"
	result.Branch = resolvedBranch
	result.SHA = status.HeadSHA
	result.Path = targetPath
	return result
}

func workspaceRepoHasIndexedCommit(repo *graph.WorkspaceRepo, commit string) bool {
	if repo == nil || repo.ActiveSnapshotID == nil {
		return false
	}
	return strings.TrimSpace(repo.ResolvedSHA) == strings.TrimSpace(commit)
}

func (h *Handlers) configureWorkspaceRepoRef(ws *graph.Workspace, repo graph.RepoInfo, mode, repoRef, commit, warning string, existing *graph.WorkspaceRepo, now time.Time) bulkCheckoutWorkspaceRepoResult {
	result := bulkCheckoutWorkspaceRepoResult{
		Repo:    repo.Name,
		Status:  "configured",
		Branch:  repoRef,
		Ref:     repoRef,
		SHA:     commit,
		Warning: warning,
	}
	var activeSnapshotID *int64
	var lastIndexedAt *time.Time
	if existing != nil && strings.TrimSpace(existing.ResolvedSHA) == strings.TrimSpace(commit) {
		activeSnapshotID = existing.ActiveSnapshotID
		lastIndexedAt = existing.LastIndexedAt
	}
	mainlineSnapshotID, mainlineResolvedSHA, mainlineLastIndexedAt := workspaceRepoMainlineState(existing)
	if mode == "mainline" {
		if existing != nil &&
			existing.MainlineSnapshotID != nil &&
			strings.TrimSpace(existing.MainlineResolvedSHA) == strings.TrimSpace(commit) {
			activeSnapshotID = existing.MainlineSnapshotID
			lastIndexedAt = existing.MainlineLastIndexedAt
		} else if inherited, err := h.storage.GetWorkspaceRepo(graph.DefaultWorkspaceSlug, repo.Name); err == nil &&
			strings.TrimSpace(inherited.ResolvedSHA) == strings.TrimSpace(commit) &&
			inherited.ActiveSnapshotID != nil {
			activeSnapshotID = inherited.ActiveSnapshotID
			lastIndexedAt = inherited.LastIndexedAt
		}
		if activeSnapshotID != nil {
			mainlineSnapshotID = activeSnapshotID
			mainlineResolvedSHA = commit
			mainlineLastIndexedAt = lastIndexedAt
		}
	}
	if _, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:           ws.ID,
		RepoName:              repo.Name,
		TargetRef:             repoRef,
		ResolvedBranch:        repoRef,
		ResolvedSHA:           commit,
		WorktreePath:          "",
		ActiveSnapshotID:      activeSnapshotID,
		MainlineSnapshotID:    mainlineSnapshotID,
		MainlineResolvedSHA:   mainlineResolvedSHA,
		MainlineLastIndexedAt: mainlineLastIndexedAt,
		LastCheckoutAt:        &now,
		LastIndexedAt:         lastIndexedAt,
		IndexStatus:           "configured",
		LastError:             "",
	}); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
	}
	return result
}

func (h *Handlers) bulkCheckoutDefaultMainRepo(ws *graph.Workspace, repo graph.RepoInfo, repoRef string) bulkCheckoutWorkspaceRepoResult {
	result := bulkCheckoutWorkspaceRepoResult{Repo: repo.Name, Ref: repoRef, Path: repo.Path}
	sourcePath, err := workspaceSourceRepoPath(repo)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	repo.Path = sourcePath
	result.Path = sourcePath
	commit, warning, err := resolveWorkspaceRefFast(sourcePath, repoRef)
	if err != nil {
		result.Status = "skipped"
		result.Error = fmt.Sprintf("ref %q not found", repoRef)
		return result
	}
	result.Warning = warning
	status, err := workspacefs.InspectRepo(sourcePath)
	if err == nil && status.Dirty {
		result.Status = "failed"
		result.Error = "repo has local changes"
		return result
	}
	if err == nil && strings.TrimSpace(status.HeadSHA) == strings.TrimSpace(commit) {
		branch := status.CurrentBranch
		if branch == "" || branch == "detached" {
			branch = repoRef
		}
		if err := h.upsertDefaultMainWorkspaceRepo(ws.ID, repo, repoRef, branch, status.HeadSHA, "ok"); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			return result
		}
		result.Status = "already_current"
		result.Branch = branch
		result.SHA = status.HeadSHA
		return result
	}
	if err := workspacefs.CheckoutRefDetached(sourcePath, commit); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	status, err = workspacefs.InspectRepo(sourcePath)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	branch := status.CurrentBranch
	if branch == "" || branch == "detached" {
		branch = repoRef
	}
	if err := h.upsertDefaultMainWorkspaceRepo(ws.ID, repo, repoRef, branch, status.HeadSHA, "checked_out"); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result
	}
	result.Status = "checked_out"
	result.Branch = branch
	result.SHA = status.HeadSHA
	return result
}

func (h *Handlers) upsertDefaultMainWorkspaceRepo(workspaceID int64, repo graph.RepoInfo, targetRef, branch, sha, status string) error {
	now := time.Now().UTC()
	_, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    workspaceID,
		RepoName:       repo.Name,
		TargetRef:      targetRef,
		ResolvedBranch: branch,
		ResolvedSHA:    sha,
		WorktreePath:   repo.Path,
		LastCheckoutAt: &now,
		IndexStatus:    status,
		LastError:      "",
	})
	return err
}

func isMainlineRef(ref string) bool {
	switch strings.TrimSpace(ref) {
	case "master", "main":
		return true
	default:
		return false
	}
}

func workspaceRepoUsesMainline(repo graph.WorkspaceRepo) bool {
	if isMainlineRef(repo.TargetRef) {
		return true
	}
	if strings.TrimSpace(repo.TargetRef) == "" && isMainlineRef(repo.ResolvedBranch) {
		return true
	}
	return false
}

func workspaceRepoNeedsMainlineCheckout(repo graph.WorkspaceRepo) bool {
	return !workspaceRepoUsesMainline(repo)
}

func workspaceRepoMainlineState(repo *graph.WorkspaceRepo) (*int64, string, *time.Time) {
	if repo == nil {
		return nil, "", nil
	}
	if repo.MainlineSnapshotID != nil {
		return repo.MainlineSnapshotID, repo.MainlineResolvedSHA, repo.MainlineLastIndexedAt
	}
	if workspaceRepoUsesMainline(*repo) && repo.ActiveSnapshotID != nil {
		return repo.ActiveSnapshotID, repo.ResolvedSHA, repo.LastIndexedAt
	}
	return nil, "", nil
}

func resolveWorkspaceRefFast(repoPath, ref string) (string, string, error) {
	if err := workspacefs.FetchRef(repoPath, ref); err != nil {
		return "", "", err
	}
	commit, err := workspacefs.ResolveRefCommit(repoPath, "origin/"+strings.TrimSpace(ref))
	return commit, "", err
}

func (h *Handlers) BulkIndexWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	if workspaceSlug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	var req bulkIndexWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace bulk index request", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ws.Slug == graph.DefaultWorkspaceSlug {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "bulk workspace indexing is disabled for default-main; use nightly indexing", nil)
		return
	}

	repos := normalizeBulkIndexRepos(req.Repos)
	if len(repos) == 0 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "at least one repo is required", nil)
		return
	}
	if len(repos) > 200 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "too many repos requested", nil)
		return
	}
	for _, repoName := range repos {
		if _, err := h.storage.GetWorkspaceRepo(ws.Slug, repoName); err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", fmt.Sprintf("workspace repo %q is not configured", repoName), nil)
			return
		}
	}

	job, err := h.startWorkspaceBulkIndexJob(ws, repos, req.Resolve)
	if err != nil {
		writeError(w, http.StatusConflict, "WORKSPACE_INDEX_RUNNING", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusAccepted, bulkIndexWorkspaceResponse{Job: job})
}

func (h *Handlers) ActiveWorkspaceBulkIndex(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	if workspaceSlug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	writeJSON(w, http.StatusOK, bulkIndexWorkspaceResponse{Job: h.workspaceBulkIndexSnapshot(workspaceSlug)})
}

func (h *Handlers) IndexWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	repoName := strings.TrimSpace(r.PathValue("repo"))
	if workspaceSlug == "" || repoName == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug and repo are required", nil)
		return
	}
	var req indexWorkspaceRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace index request", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ws.Slug == graph.DefaultWorkspaceSlug {
		h.indexDefaultMainRepo(w, r, ws, repoName, req)
		return
	}
	if err := h.indexWorkspaceRepoInternalWithRetry(r.Context(), ws, repoName, req.Resolve); err != nil {
		writeError(w, http.StatusInternalServerError, "WORKSPACE_INDEX_FAILED", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, h.workspaceSummary(*ws))
}

func (h *Handlers) indexWorkspaceRepoInternalWithRetry(ctx context.Context, ws *graph.Workspace, repoName string, resolve bool) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := h.indexWorkspaceRepoInternal(ctx, ws, repoName, resolve)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientWorkspaceIndexError(err) || attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
	return lastErr
}

func isTransientWorkspaceIndexError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlstate 40p01") ||
		strings.Contains(message, "deadlock detected") ||
		strings.Contains(message, "sqlstate 40001") ||
		strings.Contains(message, "could not serialize access")
}

func (h *Handlers) indexWorkspaceRepoInternal(ctx context.Context, ws *graph.Workspace, repoName string, resolve bool) error {
	wr, err := h.storage.GetWorkspaceRepo(ws.Slug, repoName)
	if err != nil {
		return fmt.Errorf("workspace repo %q is not configured", repoName)
	}
	prepared, err := h.ensureWorkspaceRepoWorktree(ws, wr)
	if err != nil {
		h.markWorkspaceRepoFailed(ws, repoName, wr.TargetRef, wr.WorktreePath, err)
		return err
	}
	wr = prepared

	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, wr.TargetRef, wr, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:  ws.ID,
		RepoName:     repoName,
		IndexStatus:  "indexing",
		WorktreePath: wr.WorktreePath,
	})); err != nil {
		return err
	}

	if err := indexer.RunSingleRepositoryContext(ctx, h.dbURL, ws.Slug, repoName, wr.WorktreePath, resolve); err != nil {
		h.markWorkspaceRepoFailed(ws, repoName, wr.TargetRef, wr.WorktreePath, err)
		return err
	}
	h.invalidateRepoDependenciesCache()
	return nil
}

func normalizeBulkIndexRepos(input []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(input))
	for _, repo := range input {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		key := strings.ToLower(repo)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, repo)
	}
	return out
}

func (h *Handlers) startWorkspaceBulkIndexJob(ws *graph.Workspace, repos []string, resolve bool) (*workspaceBulkIndexJob, error) {
	if ws == nil {
		return nil, fmt.Errorf("workspace is required")
	}
	now := time.Now().UTC()
	job := &workspaceBulkIndexJob{
		ID:        fmt.Sprintf("wbi_%s_%d", ws.Slug, now.UnixNano()),
		Workspace: ws.Slug,
		Resolve:   resolve,
		Status:    "running",
		Total:     len(repos),
		StartedAt: now.Format(time.RFC3339),
		Results:   make([]bulkIndexWorkspaceRepoResult, 0, len(repos)),
	}
	for _, repo := range repos {
		job.Results = append(job.Results, bulkIndexWorkspaceRepoResult{Repo: repo, Status: "queued"})
	}

	h.workspaceBulkIndexMu.Lock()
	if h.workspaceBulkIndex != nil && h.workspaceBulkIndex.Status == "running" {
		active := h.workspaceBulkIndex.ID
		h.workspaceBulkIndexMu.Unlock()
		return nil, fmt.Errorf("workspace bulk index job %s is already running", active)
	}
	h.workspaceBulkIndex = job
	snapshot := cloneWorkspaceBulkIndexJob(job)
	h.workspaceBulkIndexMu.Unlock()

	for _, repo := range repos {
		if existing, err := h.storage.GetWorkspaceRepo(ws.Slug, repo); err == nil {
			_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo, existing.TargetRef, existing, graph.UpsertWorkspaceRepoInput{
				WorkspaceID: ws.ID,
				RepoName:    repo,
				IndexStatus: "queued",
				LastError:   "",
			}))
		}
	}

	go h.runWorkspaceBulkIndexJob(job.ID, ws.Slug, repos, resolve)
	return snapshot, nil
}

func (h *Handlers) runWorkspaceBulkIndexJob(jobID, workspaceSlug string, repos []string, resolve bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		h.finishWorkspaceBulkIndexJob(jobID, err)
		return
	}
	for i, repo := range repos {
		startedAt := time.Now().UTC().Format(time.RFC3339)
		h.updateWorkspaceBulkIndexResult(jobID, i, "indexing", "", startedAt, "")
		err := h.indexWorkspaceRepoInternalWithRetry(ctx, ws, repo, resolve)
		finishedAt := time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			h.updateWorkspaceBulkIndexResult(jobID, i, "failed", err.Error(), startedAt, finishedAt)
			continue
		}
		h.updateWorkspaceBulkIndexResult(jobID, i, "ok", "", startedAt, finishedAt)
	}
	h.finishWorkspaceBulkIndexJob(jobID, nil)
}

func (h *Handlers) updateWorkspaceBulkIndexResult(jobID string, index int, status, message, startedAt, finishedAt string) {
	h.workspaceBulkIndexMu.Lock()
	defer h.workspaceBulkIndexMu.Unlock()
	job := h.workspaceBulkIndex
	if job == nil || job.ID != jobID || index < 0 || index >= len(job.Results) {
		return
	}
	result := &job.Results[index]
	previous := result.Status
	result.Status = status
	if message != "" {
		result.Error = message
	}
	if startedAt != "" && result.StartedAt == "" {
		result.StartedAt = startedAt
	}
	if finishedAt != "" {
		result.FinishedAt = finishedAt
	}
	if status == "indexing" {
		job.CurrentRepo = result.Repo
	}
	if (status == "ok" || status == "failed") && previous != "ok" && previous != "failed" {
		job.Completed++
		if status == "failed" {
			job.Failed++
		}
	}
}

func (h *Handlers) finishWorkspaceBulkIndexJob(jobID string, err error) {
	h.workspaceBulkIndexMu.Lock()
	defer h.workspaceBulkIndexMu.Unlock()
	job := h.workspaceBulkIndex
	if job == nil || job.ID != jobID {
		return
	}
	if err != nil {
		job.Status = "failed"
		if job.Completed < job.Total {
			job.Failed += job.Total - job.Completed
			job.Completed = job.Total
		}
	} else if job.Failed > 0 {
		job.Status = "failed"
	} else {
		job.Status = "ok"
	}
	job.CurrentRepo = ""
	job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
}

func (h *Handlers) workspaceBulkIndexSnapshot(workspaceSlug string) *workspaceBulkIndexJob {
	h.workspaceBulkIndexMu.Lock()
	defer h.workspaceBulkIndexMu.Unlock()
	if h.workspaceBulkIndex == nil || h.workspaceBulkIndex.Workspace != workspaceSlug {
		return nil
	}
	return cloneWorkspaceBulkIndexJob(h.workspaceBulkIndex)
}

func cloneWorkspaceBulkIndexJob(job *workspaceBulkIndexJob) *workspaceBulkIndexJob {
	if job == nil {
		return nil
	}
	out := *job
	out.Results = append([]bulkIndexWorkspaceRepoResult(nil), job.Results...)
	return &out
}

func (h *Handlers) ensureWorkspaceRepoWorktree(ws *graph.Workspace, wr *graph.WorkspaceRepo) (*graph.WorkspaceRepo, error) {
	if ws == nil || wr == nil {
		return nil, fmt.Errorf("workspace repo is required")
	}
	repo, err := h.storage.GetRepoByName(wr.RepoName)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(wr.TargetRef)
	if ref == "" {
		ref = strings.TrimSpace(wr.ResolvedBranch)
	}
	if ref == "" {
		return nil, fmt.Errorf("workspace repo %q has no target ref", wr.RepoName)
	}
	commit := strings.TrimSpace(wr.ResolvedSHA)
	sourcePath := ""
	if commit == "" {
		sourcePath, err = workspaceSourceRepoPath(*repo)
		if err != nil {
			return nil, err
		}
		var warning string
		commit, warning, err = resolveWorkspaceRefFast(sourcePath, ref)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, wr.RepoName, ref, wr, graph.UpsertWorkspaceRepoInput{
				WorkspaceID: ws.ID,
				RepoName:    wr.RepoName,
				LastError:   warning,
			}))
		}
	}
	targetPath := strings.TrimSpace(wr.WorktreePath)
	if targetPath == "" {
		targetPath, err = safeWorktreePath(workspaceWorktreeRoot(""), ws.Slug, wr.RepoName)
		if err != nil {
			return nil, err
		}
	}
	if status, err := workspacefs.InspectRepo(targetPath); err == nil && !status.Dirty && strings.TrimSpace(status.HeadSHA) == commit {
		resolvedBranch := status.CurrentBranch
		if resolvedBranch == "" || resolvedBranch == "detached" {
			resolvedBranch = ref
		}
		mainlineSnapshotID, mainlineResolvedSHA, mainlineLastIndexedAt := workspaceRepoMainlineState(wr)
		updated, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
			WorkspaceID:           ws.ID,
			RepoName:              wr.RepoName,
			TargetRef:             ref,
			ResolvedBranch:        resolvedBranch,
			ResolvedSHA:           status.HeadSHA,
			WorktreePath:          targetPath,
			ActiveSnapshotID:      wr.ActiveSnapshotID,
			MainlineSnapshotID:    mainlineSnapshotID,
			MainlineResolvedSHA:   mainlineResolvedSHA,
			MainlineLastIndexedAt: mainlineLastIndexedAt,
			LastCheckoutAt:        wr.LastCheckoutAt,
			LastIndexedAt:         wr.LastIndexedAt,
			IndexStatus:           "checked_out",
			LastError:             "",
		})
		if err != nil {
			return nil, err
		}
		return updated, nil
	}
	if sourcePath == "" {
		sourcePath, err = workspaceSourceRepoPath(*repo)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	mainlineSnapshotID, mainlineResolvedSHA, mainlineLastIndexedAt := workspaceRepoMainlineState(wr)
	if _, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:           ws.ID,
		RepoName:              wr.RepoName,
		TargetRef:             ref,
		ResolvedBranch:        ref,
		ResolvedSHA:           commit,
		WorktreePath:          targetPath,
		MainlineSnapshotID:    mainlineSnapshotID,
		MainlineResolvedSHA:   mainlineResolvedSHA,
		MainlineLastIndexedAt: mainlineLastIndexedAt,
		IndexStatus:           "checking_out",
		LastCheckoutAt:        &now,
		LastError:             "",
	}); err != nil {
		return nil, err
	}
	if err := workspacefs.EnsureWorktreeAtCommit(sourcePath, targetPath, commit); err != nil {
		h.markWorkspaceRepoFailed(ws, wr.RepoName, ref, targetPath, err)
		return nil, err
	}
	status, err := workspacefs.InspectRepo(targetPath)
	if err != nil {
		h.markWorkspaceRepoFailed(ws, wr.RepoName, ref, targetPath, err)
		return nil, err
	}
	resolvedBranch := status.CurrentBranch
	if resolvedBranch == "" || resolvedBranch == "detached" {
		resolvedBranch = ref
	}
	var activeSnapshotID *int64
	var lastIndexedAt *time.Time
	if strings.TrimSpace(wr.ResolvedSHA) == strings.TrimSpace(status.HeadSHA) {
		activeSnapshotID = wr.ActiveSnapshotID
		lastIndexedAt = wr.LastIndexedAt
	}
	updated, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:           ws.ID,
		RepoName:              wr.RepoName,
		TargetRef:             ref,
		ResolvedBranch:        resolvedBranch,
		ResolvedSHA:           status.HeadSHA,
		WorktreePath:          targetPath,
		ActiveSnapshotID:      activeSnapshotID,
		MainlineSnapshotID:    mainlineSnapshotID,
		MainlineResolvedSHA:   mainlineResolvedSHA,
		MainlineLastIndexedAt: mainlineLastIndexedAt,
		LastCheckoutAt:        &now,
		LastIndexedAt:         lastIndexedAt,
		IndexStatus:           "checked_out",
		LastError:             "",
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *Handlers) indexDefaultMainRepo(w http.ResponseWriter, r *http.Request, ws *graph.Workspace, repoName string, req indexWorkspaceRepoRequest) {
	repo, err := h.storage.GetRepoByName(repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("repo %q not found", repoName), nil)
		return
	}
	status, err := workspacefs.InspectRepo(repo.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKTREE_INSPECT_FAILED", err.Error(), nil)
		return
	}
	ref := strings.TrimSpace(status.CurrentBranch)
	if ref == "" || ref == "detached" {
		ref = "HEAD"
	}
	if _, err := h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    ws.ID,
		RepoName:       repo.Name,
		TargetRef:      ref,
		ResolvedBranch: ref,
		ResolvedSHA:    status.HeadSHA,
		WorktreePath:   repo.Path,
		IndexStatus:    "indexing",
		LastError:      "",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	if err := indexer.RunSingleRepositoryContext(r.Context(), h.dbURL, ws.Slug, repoName, repo.Path, req.Resolve); err != nil {
		h.markWorkspaceRepoFailed(ws, repoName, ref, repo.Path, err)
		writeError(w, http.StatusInternalServerError, "WORKSPACE_INDEX_FAILED", err.Error(), nil)
		return
	}
	h.invalidateRepoDependenciesCache()
	writeJSON(w, http.StatusOK, h.workspaceSummary(*ws))
}

func (h *Handlers) IndexWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceSlug := strings.TrimSpace(r.PathValue("slug"))
	if workspaceSlug == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace slug is required", nil)
		return
	}
	var req indexWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid workspace index request", nil)
		return
	}
	if req.Root != "" || req.ParseBinary != "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "root and parseBinary are server-controlled; configure TIRION_REPOS_ROOT and install helpers beside tirion", nil)
		return
	}
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	root := ""
	if ws.Slug == graph.DefaultWorkspaceSlug {
		root = strings.TrimSpace(os.Getenv("TIRION_REPOS_ROOT"))
	}
	if root == "" {
		root = filepath.Join(workspaceWorktreeRoot(""), ws.Slug, "repos")
	}
	if !filepath.IsAbs(root) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "configure an absolute TIRION_REPOS_ROOT or TIRION_WORKTREE_ROOT", nil)
		return
	}
	skipTests := true
	if req.SkipTests != nil {
		skipTests = *req.SkipTests
	}
	skipUnchanged := true
	if req.SkipUnchanged != nil {
		skipUnchanged = *req.SkipUnchanged
	}
	_, err = indexer.RunBatchDetailed(indexer.BatchOptions{
		DBURL:         h.dbURL,
		Root:          root,
		ParseBinary:   indexer.DefaultParseBinary(),
		Context:       r.Context(),
		Workspace:     ws.Slug,
		SkipTests:     skipTests,
		GlobalResolve: req.Resolve,
		Exclude:       indexer.ParseExcluded(req.Exclude),
		SkipUnchanged: skipUnchanged,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKSPACE_INDEX_FAILED", err.Error(), nil)
		return
	}
	h.invalidateRepoDependenciesCache()
	writeJSON(w, http.StatusOK, h.workspaceSummary(*ws))
}

func (h *Handlers) workspaceSummary(ws graph.Workspace) workspaceSummary {
	snapshots, err := h.storage.ActiveSnapshotsForWorkspace(ws.Slug)
	if err != nil {
		snapshots = []graph.WorkspaceSnapshotRef{}
	}
	repos, err := h.storage.ListWorkspaceRepos(ws.Slug)
	if err != nil {
		repos = []graph.WorkspaceRepo{}
	}
	if snapshots == nil {
		snapshots = []graph.WorkspaceSnapshotRef{}
	}
	if repos == nil {
		repos = []graph.WorkspaceRepo{}
	}
	return workspaceSummary{
		Workspace:       ws,
		ActiveSnapshots: snapshots,
		Repos:           repos,
	}
}

func workspaceWorktreeRoot(override string) string {
	// Request bodies cannot override this directory. The parameter is retained for
	// compatibility with internal callers; only process configuration is trusted.
	if root := strings.TrimSpace(os.Getenv("TIRION_WORKTREE_ROOT")); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("TIRION_WORKSPACES_ROOT")); root != "" {
		return root
	}
	root, err := runtimeconfig.UserPath("workspaces")
	if err != nil {
		return ""
	}
	return root
}

func workspaceSourceRepoPath(repo graph.RepoInfo) (string, error) {
	repoPath := strings.TrimSpace(repo.Path)
	if gitRepoPathExists(repoPath) {
		return repoPath, nil
	}
	if !filepath.IsLocal(repo.Name) || filepath.Base(repo.Name) != repo.Name || repo.Name == "." {
		return "", fmt.Errorf("repo %q has no usable registered source path", repo.Name)
	}
	for _, root := range workspaceSourceRepoRoots() {
		candidate := filepath.Join(root, repo.Name)
		if gitRepoPathExists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("repo %q has no usable source clone; restore its registered path or configure TIRION_REPOS_ROOT", repo.Name)
}

func workspaceSourceRepoRoots() []string {
	var out []string
	seen := map[string]bool{}
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		abs, err := filepath.Abs(root)
		if err == nil {
			root = abs
		}
		if seen[root] {
			return
		}
		seen[root] = true
		out = append(out, root)
	}
	add(os.Getenv("TIRION_REPOS_ROOT"))
	add(os.Getenv("REPOS_ROOT"))
	return out
}

func gitRepoPathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

func workspaceRepoInputFromExisting(workspaceID int64, repoName, targetRef string, existing *graph.WorkspaceRepo, next graph.UpsertWorkspaceRepoInput) graph.UpsertWorkspaceRepoInput {
	if next.WorkspaceID == 0 {
		next.WorkspaceID = workspaceID
	}
	if next.RepoName == "" {
		next.RepoName = repoName
	}
	if next.TargetRef == "" {
		next.TargetRef = targetRef
	}
	if existing == nil {
		if next.IndexStatus == "" {
			next.IndexStatus = "unknown"
		}
		return next
	}
	if next.TargetRef == "" {
		next.TargetRef = existing.TargetRef
	}
	if next.ResolvedBranch == "" {
		next.ResolvedBranch = existing.ResolvedBranch
	}
	if next.ResolvedSHA == "" {
		next.ResolvedSHA = existing.ResolvedSHA
	}
	if next.WorktreePath == "" {
		next.WorktreePath = existing.WorktreePath
	}
	if next.ActiveSnapshotID == nil {
		next.ActiveSnapshotID = existing.ActiveSnapshotID
	}
	if next.MainlineSnapshotID == nil {
		next.MainlineSnapshotID = existing.MainlineSnapshotID
	}
	if next.MainlineResolvedSHA == "" {
		next.MainlineResolvedSHA = existing.MainlineResolvedSHA
	}
	if next.MainlineLastIndexedAt == nil {
		next.MainlineLastIndexedAt = existing.MainlineLastIndexedAt
	}
	if next.LastCheckoutAt == nil {
		next.LastCheckoutAt = existing.LastCheckoutAt
	}
	if next.LastIndexedAt == nil {
		next.LastIndexedAt = existing.LastIndexedAt
	}
	if next.IndexStatus == "" {
		next.IndexStatus = existing.IndexStatus
	}
	return next
}

func (h *Handlers) markWorkspaceRepoFailed(ws *graph.Workspace, repoName, targetRef, worktreePath string, opErr error) {
	if ws == nil || opErr == nil {
		return
	}
	existing, _ := h.storage.GetWorkspaceRepo(ws.Slug, repoName)
	_, _ = h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repoName, targetRef, existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:  ws.ID,
		RepoName:     repoName,
		IndexStatus:  "failed",
		WorktreePath: worktreePath,
		LastError:    opErr.Error(),
	}))
}

// Reject path segments from graph identities and symlinked worktree parents.
// Git is allowed to write only below an operator-selected directory.
func safeWorktreePath(root, slug, repo string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("TIRION_WORKTREE_ROOT must be an absolute path")
	}
	// Discovered repository names may be nested ("group/svc"); every component
	// must be a plain local name.
	repoSegments := strings.Split(filepath.ToSlash(repo), "/")
	for _, segment := range append([]string{slug}, repoSegments...) {
		if !filepath.IsLocal(segment) || filepath.Base(segment) != segment || segment == "." {
			return "", fmt.Errorf("invalid workspace/repository path segment")
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	current := root
	segments := append([]string{slug, "repos"}, repoSegments...)
	for i, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if i == len(segments)-1 {
				break
			}
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("worktree path contains a symlink or non-directory: %s", current)
		}
	}
	return current, nil
}
