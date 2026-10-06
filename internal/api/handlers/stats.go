package handlers

import (
	"net/http"
)

func (h *Handlers) Stats(w http.ResponseWriter, r *http.Request) {
	_, scope, scopeErr := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if scopeErr != nil {
		writeLookupError(w, scopeErr)
		return
	}
	filter := snapshotFilterForScope(scope)

	stats, err := h.storage.GetStats(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	// graph.Stats carries camelCase JSON tags: repos, files, functions, classes, endpoints.
	writeJSON(w, http.StatusOK, stats)
}
