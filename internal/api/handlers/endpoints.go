package handlers

import (
	"net/http"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func (h *Handlers) ListEndpoints(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	method := query.Get("method")
	path := query.Get("path")
	pattern := query.Get("q")
	limit, offset, ok := parsePage(w, r, 100, 500)
	if !ok {
		return
	}

	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}

	if err := h.requireRepo(r.Context(), repo, workspaceScope); err != nil {
		writeLookupError(w, err)
		return
	}
	endpoints, err := h.storage.GetEndpoints(repo, method, path, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	if endpoints == nil {
		endpoints = []graph.EndpointInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace":   workspaceScope.Workspace,
		"repoContext": workspaceScope.RepoContext,
		"endpoints":   endpoints,
	})
}
