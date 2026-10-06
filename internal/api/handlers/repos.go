package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func (h *Handlers) ListRepos(w http.ResponseWriter, r *http.Request) {
	_, scope, scopeErr := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if scopeErr != nil {
		writeLookupError(w, scopeErr)
		return
	}
	filter := snapshotFilterForScope(scope)

	repos, err := h.storage.ListRepos(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

func (h *Handlers) GetRepo(w http.ResponseWriter, r *http.Request) {
	_, scope, scopeErr := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if scopeErr != nil {
		writeLookupError(w, scopeErr)
		return
	}
	filter := snapshotFilterForScope(scope)

	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing repo id", nil)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid repo id", nil)
		return
	}

	repo, err := h.storage.GetRepoByID(id, filter)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "repo not found", nil)
		} else {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not load repo", nil)
		}
		return
	}

	if err := h.requireRepo(r.Context(), repo.Name, scope); err != nil {
		writeLookupError(w, err)
		return
	}
	stats, err := h.storage.GetRepoStats(id, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":    repo.ID,
		"name":  repo.Name,
		"path":  repo.Path,
		"files": repo.FileCount,
		"stats": stats,
	})
}
