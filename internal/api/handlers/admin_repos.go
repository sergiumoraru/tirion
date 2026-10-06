package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
	"github.com/sergiumoraru/tirion/internal/workspace"
)

type repoOperationState struct {
	RepoID    int64  `json:"repoId"`
	RepoName  string `json:"repoName"`
	Action    string `json:"action"`
	StartedAt string `json:"startedAt"`
}

type adminRepoRow struct {
	ID                    int64    `json:"id"`
	Name                  string   `json:"name"`
	Path                  string   `json:"path"`
	UpdatedAt             string   `json:"updatedAt"`
	AgeHours              float64  `json:"ageHours"`
	FreshnessStatus       string   `json:"freshnessStatus"`
	FileCount             int      `json:"fileCount"`
	FunctionCount         int      `json:"functionCount"`
	ClassCount            int      `json:"classCount"`
	EndpointCount         int      `json:"endpointCount"`
	PrimaryLanguage       string   `json:"primaryLanguage"`
	SelectedBranch        string   `json:"selectedBranch"`
	CurrentBranch         string   `json:"currentBranch"`
	HeadSHA               string   `json:"headSha"`
	WorkspaceBranch       string   `json:"workspaceBranch"`
	WorkspaceCommit       string   `json:"workspaceCommit"`
	WorkspaceAvailable    bool     `json:"workspaceAvailable"`
	WorkspaceError        string   `json:"workspaceError,omitempty"`
	IndexedBranch         string   `json:"indexedBranch"`
	IndexedSHA            string   `json:"indexedSha"`
	IndexedCommit         string   `json:"indexedCommit"`
	IndexedAt             string   `json:"indexedAt,omitempty"`
	Dirty                 bool     `json:"dirty"`
	AheadCount            int      `json:"aheadCount"`
	BehindCount           int      `json:"behindCount"`
	BranchDrift           bool     `json:"branchDrift"`
	ReparseNeeded         bool     `json:"reparseNeeded"`
	DriftStatus           string   `json:"driftStatus"`
	IndexAlignmentStatus  string   `json:"indexAlignmentStatus"`
	IndexAlignmentReasons []string `json:"indexAlignmentReasons,omitempty"`
	LastOperation         string   `json:"lastOperation,omitempty"`
	LastOperationStatus   string   `json:"lastOperationStatus,omitempty"`
	LastOperationAt       string   `json:"lastOperationAt,omitempty"`
	LastError             string   `json:"lastError,omitempty"`
	Busy                  bool     `json:"busy"`
}

type adminReposResponse struct {
	GeneratedAt     string              `json:"generatedAt"`
	ActiveOperation *repoOperationState `json:"activeOperation,omitempty"`
	Repos           []adminRepoRow      `json:"repos"`
}

type adminRepoCheckoutRequest struct {
	Branch string `json:"branch"`
}

type adminRepoParseRequest struct {
	Resolve bool `json:"resolve"`
}

func (h *Handlers) AdminRepos(w http.ResponseWriter, r *http.Request) {
	rows, err := h.listAdminRepoRows(workspaceIDFromRequest(r))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminReposResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		ActiveOperation: h.getRepoOperationState(),
		Repos:           rows,
	})
}

func (h *Handlers) AdminRepoFetch(w http.ResponseWriter, r *http.Request) {
	h.runRepoMutation(w, r, "fetch", func(repo *graph.RepoInfo) error {
		return workspace.Fetch(repo.Path)
	})
}

func (h *Handlers) AdminRepoCheckout(w http.ResponseWriter, r *http.Request) {
	var req adminRepoCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid checkout request", nil)
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "branch is required", nil)
		return
	}

	if workspaceID := workspaceIDFromRequest(r); normalizeAdminWorkspaceID(workspaceID) != graph.DefaultWorkspaceSlug {
		h.runWorkspaceRepoCheckoutFromAdmin(w, r, branch)
		return
	}

	h.runRepoMutation(w, r, "checkout", func(repo *graph.RepoInfo) error {
		status, err := workspace.InspectRepo(repo.Path)
		if err != nil {
			return err
		}
		if status.Dirty {
			return &repoMutationError{
				status:  http.StatusConflict,
				code:    "DIRTY_REPO",
				message: "repo has local changes; checkout is blocked",
			}
		}
		if err := workspace.Checkout(repo.Path, branch); err != nil {
			return err
		}
		if err := h.storage.UpdateRepoSelectedBranch(repo.ID, branch); err != nil {
			return fmt.Errorf("persist selected branch: %w", err)
		}
		return nil
	})
}

func (h *Handlers) AdminRepoParse(w http.ResponseWriter, r *http.Request) {
	var req adminRepoParseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid parse request", nil)
		return
	}

	if workspaceID := workspaceIDFromRequest(r); normalizeAdminWorkspaceID(workspaceID) != graph.DefaultWorkspaceSlug {
		h.runWorkspaceRepoParseFromAdmin(w, r, req.Resolve)
		return
	}

	action := "parse"
	if req.Resolve {
		action = "parse_resolve"
	}
	h.runRepoMutation(w, r, action, func(repo *graph.RepoInfo) error {
		return indexer.RunSingleRepositoryContext(r.Context(), h.dbURL, graph.DefaultWorkspaceSlug, repo.Name, repo.Path, req.Resolve)
	})
}

type repoMutationError struct {
	status  int
	code    string
	message string
}

func (e *repoMutationError) Error() string {
	return e.message
}

func (h *Handlers) runRepoMutation(w http.ResponseWriter, r *http.Request, action string, op func(repo *graph.RepoInfo) error) {
	repo, err := h.loadRepoFromRequest(r)
	if err != nil {
		if mutationErr, ok := err.(*repoMutationError); ok {
			writeError(w, mutationErr.status, mutationErr.code, mutationErr.message, nil)
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	if active := h.beginRepoOperation(repo.ID, repo.Name, action); active != nil {
		writeError(w, http.StatusConflict, "REPO_OPERATION_BUSY", "another repo operation is already running", active)
		return
	}

	startedAt := time.Now().UTC()
	operationRepoID := repo.ID
	selectedBranchFallback := ""
	if strings.HasPrefix(action, "parse") {
		if status, err := workspace.InspectRepo(repo.Path); err == nil {
			selectedBranchFallback = status.CurrentBranch
		}
	}
	if err := h.storage.UpdateRepoWorkspaceOperation(operationRepoID, action, "running", startedAt, ""); err != nil {
		h.endRepoOperation()
		writeError(w, http.StatusInternalServerError, "REPO_STATE_WRITE_FAILED", err.Error(), nil)
		return
	}

	var opErr error
	defer func() {
		if strings.HasPrefix(action, "parse") {
			if reboundRepoID, err := h.ensureRepoWorkspaceAnchor(repo, selectedBranchFallback); err == nil {
				operationRepoID = reboundRepoID
			} else {
				log.Printf("WARN: repo workspace anchor restore failed for %s: %v", repo.Path, err)
			}
		}

		status := "ok"
		lastError := ""
		if opErr != nil {
			status = "failed"
			lastError = opErr.Error()
		}
		if err := h.storage.UpdateRepoWorkspaceOperation(operationRepoID, action, status, time.Now().UTC(), lastError); err != nil {
			log.Printf("WARN: repo workspace operation persist failed for repo_id=%d action=%s: %v", operationRepoID, action, err)
		}
	}()

	if opErr = op(repo); opErr != nil {
		h.endRepoOperation()
		if mutationErr, ok := opErr.(*repoMutationError); ok {
			writeError(w, mutationErr.status, mutationErr.code, mutationErr.message, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "REPO_OPERATION_FAILED", opErr.Error(), nil)
		return
	}
	if strings.HasPrefix(action, "parse") {
		if currentRepo, err := h.storage.GetRepoByPath(repo.Path); err == nil {
			operationRepoID = currentRepo.ID
		}
		h.queueAdminAuditRefresh()
		h.invalidateRepoDependenciesCache()
	}
	h.endRepoOperation()

	rows, err := h.listAdminRepoRows(workspaceIDFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, adminReposResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		ActiveOperation: h.getRepoOperationState(),
		Repos:           rows,
	})
}

func (h *Handlers) ensureRepoWorkspaceAnchor(repo *graph.RepoInfo, selectedBranch string) (int64, error) {
	currentRepo, err := h.storage.GetRepoByPath(repo.Path)
	if err != nil {
		repoID, insertErr := h.storage.InsertRepository(repo.Name, repo.Path, nil, nil)
		if insertErr != nil {
			return 0, insertErr
		}
		if selectedBranch != "" {
			if err := h.storage.UpdateRepoSelectedBranch(repoID, selectedBranch); err != nil {
				return 0, err
			}
		}
		return repoID, nil
	}
	if selectedBranch != "" {
		if err := h.storage.UpdateRepoSelectedBranch(currentRepo.ID, selectedBranch); err != nil {
			return 0, err
		}
	}
	return currentRepo.ID, nil
}

func (h *Handlers) loadRepoFromRequest(r *http.Request) (*graph.RepoInfo, error) {
	idStr := r.PathValue("id")
	if idStr == "" {
		return nil, errInvalidRepoID
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, errInvalidRepoID
	}
	repo, err := h.storage.GetRepoByID(id)
	if err != nil {
		return nil, errRepoNotFound
	}
	return repo, nil
}

var (
	errInvalidRepoID = &repoMutationError{status: http.StatusBadRequest, code: "VALIDATION_ERROR", message: "invalid repo id"}
	errRepoNotFound  = &repoMutationError{status: http.StatusNotFound, code: "NOT_FOUND", message: "repo not found"}
)

func (h *Handlers) listAdminRepoRows(workspaceID string) ([]adminRepoRow, error) {
	workspaceSlug := normalizeAdminWorkspaceID(workspaceID)
	repos, err := h.storage.ListRepoHealthForWorkspace(workspaceSlug)
	if err != nil {
		return nil, err
	}
	workspaceState, err := h.storage.ListRepoWorkspaceState()
	if err != nil {
		return nil, err
	}
	workspaceRepos := map[string]graph.WorkspaceRepo{}
	workspaceSnapshots := map[string]graph.WorkspaceSnapshotRef{}
	if wrs, err := h.storage.ListWorkspaceRepos(workspaceSlug); err == nil {
		for _, wr := range wrs {
			workspaceRepos[wr.RepoName] = wr
		}
	}
	if snapshots, err := h.storage.ActiveSnapshotsForWorkspace(workspaceSlug); err == nil {
		for _, snapshot := range snapshots {
			workspaceSnapshots[snapshot.RepoName] = snapshot
		}
	}
	defaultMainWorkspaceRepos := map[string]graph.WorkspaceRepo{}
	if workspaceSlug != graph.DefaultWorkspaceSlug {
		if wrs, err := h.storage.ListWorkspaceRepos(graph.DefaultWorkspaceSlug); err == nil {
			for _, wr := range wrs {
				defaultMainWorkspaceRepos[wr.RepoName] = wr
			}
		}
	}

	now := time.Now().UTC()
	rows := make([]adminRepoRow, 0, len(repos))
	active := h.getRepoOperationState()

	for _, repo := range repos {
		row := adminRepoRow{
			ID:              repo.ID,
			Name:            repo.Name,
			Path:            repo.Path,
			UpdatedAt:       repo.UpdatedAt.UTC().Format(time.RFC3339),
			AgeHours:        now.Sub(repo.UpdatedAt.UTC()).Hours(),
			FreshnessStatus: repoFreshnessStatus(now.Sub(repo.UpdatedAt.UTC())),
			FileCount:       repo.FileCount,
			FunctionCount:   repo.FunctionCount,
			ClassCount:      repo.ClassCount,
			EndpointCount:   repo.EndpointCount,
			PrimaryLanguage: repo.PrimaryLanguage,
			Busy:            active != nil && active.RepoID == repo.ID,
		}

		livePath := repo.Path
		inspectLivePath := true
		if wr, ok := workspaceRepos[repo.Name]; ok {
			if strings.TrimSpace(wr.WorktreePath) != "" {
				livePath = wr.WorktreePath
			} else if workspaceSlug != graph.DefaultWorkspaceSlug {
				inspectLivePath = false
			}
			row.SelectedBranch = wr.TargetRef
			row.CurrentBranch = wr.ResolvedBranch
			row.HeadSHA = wr.ResolvedSHA
			row.LastOperation = "workspace"
			row.LastOperationStatus = wr.IndexStatus
			row.LastError = wr.LastError
		}
		if inspectLivePath {
			if live, err := workspace.InspectRepo(livePath); err == nil {
				row.CurrentBranch = live.CurrentBranch
				if row.CurrentBranch == "" || row.CurrentBranch == "detached" {
					if wr, ok := workspaceRepos[repo.Name]; ok && strings.TrimSpace(wr.ResolvedBranch) != "" {
						row.CurrentBranch = wr.ResolvedBranch
					}
				}
				row.HeadSHA = live.HeadSHA
				row.WorkspaceAvailable = true
				row.Dirty = live.Dirty
				row.AheadCount = live.AheadCount
				row.BehindCount = live.BehindCount
			} else {
				row.WorkspaceError = err.Error()
			}
		} else if row.CurrentBranch == "" {
			if row.SelectedBranch != "" {
				row.CurrentBranch = row.SelectedBranch
			}
		}

		workspaceRepo, hasWorkspaceRepo := workspaceRepos[repo.Name]
		if snapshot, ok := workspaceSnapshots[repo.Name]; ok {
			row.IndexedBranch = indexedBranchLabelForSnapshot(workspaceSlug, row, snapshot)
			row.IndexedSHA = snapshot.SHA
			row.IndexedAt = snapshot.IndexedAt.UTC().Format(time.RFC3339)
			row.UpdatedAt = snapshot.IndexedAt.UTC().Format(time.RFC3339)
			row.AgeHours = now.Sub(snapshot.IndexedAt.UTC()).Hours()
			row.FreshnessStatus = repoFreshnessStatus(now.Sub(snapshot.IndexedAt.UTC()))
		} else if hasWorkspaceRepo && workspaceSlug != graph.DefaultWorkspaceSlug && workspaceRepoUsesMainline(workspaceRepo) {
			if !applyWorkspaceRepoIndexedState(&row, workspaceRepo, now) {
				if inherited, ok := defaultMainWorkspaceRepos[repo.Name]; ok && applyWorkspaceRepoIndexedState(&row, inherited, now) {
					// Non-default workspaces inherit the mainline indexed snapshot
					// for repos still on mainline. Without this, copied or reset
					// workspaces show SHA "-" even though mainline is indexed.
				} else if state, ok := workspaceState[repo.ID]; ok {
					applyRepoWorkspaceIndexedState(&row, state, now)
				}
			}
		} else if state, ok := workspaceState[repo.ID]; ok && workspaceSlug == graph.DefaultWorkspaceSlug {
			row.SelectedBranch = state.SelectedBranch
			row.IndexedBranch = state.IndexedBranch
			row.IndexedSHA = state.IndexedSHA
			row.IndexedCommit = state.IndexedSHA
			row.LastOperation = state.LastOperation
			row.LastOperationStatus = state.LastOperationStatus
			row.LastError = state.LastError
			if state.IndexedAt != nil {
				row.IndexedAt = state.IndexedAt.UTC().Format(time.RFC3339)
			}
			if state.LastOperationAt != nil {
				row.LastOperationAt = state.LastOperationAt.UTC().Format(time.RFC3339)
			}
		}

		applyAdminWorkspaceDriftState(&row, workspaceSlug, workspaceRepo, hasWorkspaceRepo)
		applyAdminWorkspaceAliases(&row)
		if row.IndexedCommit == "" {
			row.IndexedCommit = row.IndexedSHA
		}
		row.IndexAlignmentStatus, row.IndexAlignmentReasons = repoIndexAlignmentStatus(row)

		// Override freshness: if indexed SHA matches HEAD, the index is current
		// regardless of how old the indexedAt timestamp is (skip-unchanged skips
		// repos with no new commits, leaving timestamps stale)
		if row.DriftStatus == "in_sync" && row.FreshnessStatus != "recent" {
			row.FreshnessStatus = "recent"
		}

		rows = append(rows, row)
	}

	return rows, nil
}

func applyAdminWorkspaceDriftState(row *adminRepoRow, workspaceSlug string, workspaceRepo graph.WorkspaceRepo, hasWorkspaceRepo bool) {
	workspaceParseNeeded := workspaceSlug != graph.DefaultWorkspaceSlug &&
		hasWorkspaceRepo &&
		!workspaceRepoUsesMainline(workspaceRepo) &&
		strings.TrimSpace(row.IndexedSHA) == ""
	row.BranchDrift = row.IndexedBranch != "" && row.CurrentBranch != "" && row.IndexedBranch != row.CurrentBranch
	row.ReparseNeeded = workspaceParseNeeded || (row.IndexedSHA != "" && row.HeadSHA != "" && row.IndexedSHA != row.HeadSHA)
	switch {
	case workspaceParseNeeded:
		row.DriftStatus = "drift"
		row.FreshnessStatus = "stale"
	case row.IndexedSHA == "":
		row.DriftStatus = "unknown"
	case row.BranchDrift || row.ReparseNeeded:
		row.DriftStatus = "drift"
	default:
		row.DriftStatus = "in_sync"
	}
}

func applyAdminWorkspaceAliases(row *adminRepoRow) {
	row.WorkspaceBranch = row.CurrentBranch
	row.WorkspaceCommit = row.HeadSHA
}

func indexedBranchLabelForSnapshot(workspaceSlug string, row adminRepoRow, snapshot graph.WorkspaceSnapshotRef) string {
	branch := strings.TrimSpace(snapshot.Branch)
	if workspaceSlug != graph.DefaultWorkspaceSlug &&
		strings.TrimSpace(row.CurrentBranch) != "" &&
		strings.EqualFold(strings.TrimSpace(row.HeadSHA), strings.TrimSpace(snapshot.SHA)) {
		// A non-default workspace can reuse an inherited mainline snapshot when
		// the selected ref resolves to the same commit. In that case the indexed
		// facts are valid for the workspace ref, so display the workspace branch
		// instead of reporting fake branch drift.
		return row.CurrentBranch
	}
	return branch
}

func applyWorkspaceRepoIndexedState(row *adminRepoRow, wr graph.WorkspaceRepo, now time.Time) bool {
	branch := strings.TrimSpace(wr.TargetRef)
	if branch == "" {
		branch = strings.TrimSpace(wr.ResolvedBranch)
	}
	sha := strings.TrimSpace(wr.MainlineResolvedSHA)
	indexedAt := wr.MainlineLastIndexedAt
	if sha == "" && wr.ActiveSnapshotID != nil {
		sha = strings.TrimSpace(wr.ResolvedSHA)
		indexedAt = wr.LastIndexedAt
	}
	if sha == "" {
		return false
	}
	if isMainlineRef(row.CurrentBranch) && strings.EqualFold(strings.TrimSpace(row.HeadSHA), sha) {
		branch = strings.TrimSpace(row.CurrentBranch)
	}
	row.IndexedBranch = branch
	row.IndexedSHA = sha
	row.IndexedCommit = sha
	applyIndexedTimestamp(row, indexedAt, now)
	return true
}

func applyRepoWorkspaceIndexedState(row *adminRepoRow, state graph.RepoWorkspaceState, now time.Time) bool {
	sha := strings.TrimSpace(state.IndexedSHA)
	if sha == "" {
		return false
	}
	branch := strings.TrimSpace(state.IndexedBranch)
	if isMainlineRef(row.CurrentBranch) && strings.EqualFold(strings.TrimSpace(row.HeadSHA), sha) {
		branch = strings.TrimSpace(row.CurrentBranch)
	}
	row.IndexedBranch = branch
	row.IndexedSHA = sha
	row.IndexedCommit = sha
	applyIndexedTimestamp(row, state.IndexedAt, now)
	return true
}

func applyIndexedTimestamp(row *adminRepoRow, indexedAt *time.Time, now time.Time) {
	if indexedAt == nil {
		return
	}
	t := indexedAt.UTC()
	row.IndexedAt = t.Format(time.RFC3339)
	row.UpdatedAt = t.Format(time.RFC3339)
	row.AgeHours = now.Sub(t).Hours()
	row.FreshnessStatus = repoFreshnessStatus(now.Sub(t))
}

func normalizeAdminWorkspaceID(workspaceID string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return graph.DefaultWorkspaceSlug
	}
	return workspaceID
}

func (h *Handlers) runWorkspaceRepoCheckoutFromAdmin(w http.ResponseWriter, r *http.Request, ref string) {
	repo, err := h.loadRepoFromRequest(r)
	if err != nil {
		if mutationErr, ok := err.(*repoMutationError); ok {
			writeError(w, mutationErr.status, mutationErr.code, mutationErr.message, nil)
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	workspaceSlug := normalizeAdminWorkspaceID(workspaceIDFromRequest(r))
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	targetPath, err := safeWorktreePath(workspaceWorktreeRoot(""), ws.Slug, repo.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if active := h.beginRepoOperation(repo.ID, repo.Name, "checkout"); active != nil {
		writeError(w, http.StatusConflict, "REPO_OPERATION_BUSY", "another repo operation is already running", active)
		return
	}
	defer h.endRepoOperation()

	startedAt := time.Now().UTC()
	existing, _ := h.storage.GetWorkspaceRepo(ws.Slug, repo.Name)
	var opErr error
	defer func() {
		if opErr == nil {
			return
		}
		if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, ref, existing, graph.UpsertWorkspaceRepoInput{
			WorkspaceID: ws.ID,
			RepoName:    repo.Name,
			TargetRef:   ref,
			IndexStatus: "failed",
			LastError:   opErr.Error(),
		})); err != nil {
			log.Printf("WARN: workspace repo operation persist failed for workspace=%s repo=%s: %v", ws.Slug, repo.Name, err)
		}
	}()

	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, ref, existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    ws.ID,
		RepoName:       repo.Name,
		TargetRef:      ref,
		WorktreePath:   targetPath,
		IndexStatus:    "checking_out",
		LastCheckoutAt: &startedAt,
	})); err != nil {
		writeError(w, http.StatusInternalServerError, "REPO_STATE_WRITE_FAILED", err.Error(), nil)
		return
	}

	if opErr = h.checkoutWorkspaceRepoForAdmin(ws, repo, ref, targetPath); opErr != nil {
		writeError(w, http.StatusInternalServerError, "REPO_OPERATION_FAILED", opErr.Error(), nil)
		return
	}
	rows, err := h.listAdminRepoRows(ws.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, adminReposResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		ActiveOperation: h.getRepoOperationState(),
		Repos:           rows,
	})
}

// targetPath must come from safeWorktreePath.
func (h *Handlers) checkoutWorkspaceRepoForAdmin(ws *graph.Workspace, repo *graph.RepoInfo, ref, targetPath string) error {
	sourcePath, err := workspaceSourceRepoPath(*repo)
	if err != nil {
		return err
	}
	existing, _ := h.storage.GetWorkspaceRepo(ws.Slug, repo.Name)
	now := time.Now().UTC()
	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, ref, existing, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:    ws.ID,
		RepoName:       repo.Name,
		TargetRef:      ref,
		WorktreePath:   targetPath,
		IndexStatus:    "checking_out",
		LastCheckoutAt: &now,
	})); err != nil {
		return err
	}
	if err := workspace.EnsureWorktree(sourcePath, targetPath, ref); err != nil {
		return err
	}
	status, err := workspace.InspectRepo(targetPath)
	if err != nil {
		return err
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
	_, err = h.storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:      ws.ID,
		RepoName:         repo.Name,
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
	return err
}

func (h *Handlers) runWorkspaceRepoParseFromAdmin(w http.ResponseWriter, r *http.Request, resolve bool) {
	repo, err := h.loadRepoFromRequest(r)
	if err != nil {
		if mutationErr, ok := err.(*repoMutationError); ok {
			writeError(w, mutationErr.status, mutationErr.code, mutationErr.message, nil)
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	workspaceSlug := normalizeAdminWorkspaceID(workspaceIDFromRequest(r))
	ws, err := h.storage.ResolveWorkspace(workspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	wr, err := h.storage.GetWorkspaceRepo(ws.Slug, repo.Name)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("workspace repo %q is not configured", repo.Name), nil)
		return
	}
	action := "parse"
	if resolve {
		action = "parse_resolve"
	}
	if active := h.beginRepoOperation(repo.ID, repo.Name, action); active != nil {
		writeError(w, http.StatusConflict, "REPO_OPERATION_BUSY", "another repo operation is already running", active)
		return
	}
	finishOperation := sync.OnceFunc(h.endRepoOperation)
	defer finishOperation()

	wr, err = h.ensureWorkspaceRepoWorktree(ws, wr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WORKTREE_CHECKOUT_FAILED", err.Error(), nil)
		return
	}
	if _, err := h.storage.UpsertWorkspaceRepo(workspaceRepoInputFromExisting(ws.ID, repo.Name, wr.TargetRef, wr, graph.UpsertWorkspaceRepoInput{
		WorkspaceID:  ws.ID,
		RepoName:     repo.Name,
		IndexStatus:  "indexing",
		WorktreePath: wr.WorktreePath,
	})); err != nil {
		writeError(w, http.StatusInternalServerError, "REPO_STATE_WRITE_FAILED", err.Error(), nil)
		return
	}
	if err := indexer.RunSingleRepositoryContext(r.Context(), h.dbURL, ws.Slug, repo.Name, wr.WorktreePath, resolve); err != nil {
		h.markWorkspaceRepoFailed(ws, repo.Name, wr.TargetRef, wr.WorktreePath, err)
		writeError(w, http.StatusInternalServerError, "WORKSPACE_INDEX_FAILED", err.Error(), nil)
		return
	}
	h.invalidateRepoDependenciesCache()
	finishOperation()
	rows, err := h.listAdminRepoRows(ws.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, adminReposResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		ActiveOperation: h.getRepoOperationState(),
		Repos:           rows,
	})
}

func repoIndexAlignmentStatus(row adminRepoRow) (string, []string) {
	var reasons []string
	if !row.WorkspaceAvailable {
		reasons = append(reasons, "workspace_unavailable")
	}
	if row.IndexedSHA == "" {
		reasons = append(reasons, "missing_indexed_state")
	}
	if row.IndexedBranch == "" {
		reasons = append(reasons, "missing_indexed_branch")
	}
	if row.WorkspaceAvailable && row.CurrentBranch == "" {
		reasons = append(reasons, "missing_workspace_branch")
	}
	if row.WorkspaceAvailable && row.HeadSHA == "" {
		reasons = append(reasons, "missing_workspace_commit")
	}

	if !row.WorkspaceAvailable || row.IndexedSHA == "" || row.HeadSHA == "" {
		return "unknown", reasons
	}

	if row.BranchDrift {
		reasons = append(reasons, "branch_drift")
	}
	if row.ReparseNeeded {
		reasons = append(reasons, "commit_drift")
	}
	if row.Dirty {
		reasons = append(reasons, "dirty_workspace")
	}
	if len(reasons) > 0 {
		return "stale", reasons
	}
	return "fresh", nil
}

func (h *Handlers) beginRepoOperation(repoID int64, repoName, action string) *repoOperationState {
	h.repoOpMu.Lock()
	defer h.repoOpMu.Unlock()
	if h.repoOpActive != nil {
		active := *h.repoOpActive
		return &active
	}
	h.repoOpActive = &repoOperationState{
		RepoID:    repoID,
		RepoName:  repoName,
		Action:    action,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return nil
}

func (h *Handlers) endRepoOperation() {
	h.repoOpMu.Lock()
	h.repoOpActive = nil
	h.repoOpMu.Unlock()
}

func (h *Handlers) getRepoOperationState() *repoOperationState {
	h.repoOpMu.Lock()
	defer h.repoOpMu.Unlock()
	if h.repoOpActive == nil {
		return nil
	}
	active := *h.repoOpActive
	return &active
}
