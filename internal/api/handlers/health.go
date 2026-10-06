package handlers

import (
	"net/http"

	"github.com/sergiumoraru/tirion/internal/buildinfo"
)

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": buildinfo.Version,
	})
}
