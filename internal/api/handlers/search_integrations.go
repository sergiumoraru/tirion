package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type SearchIntegrationsRequest struct {
	FunctionCallerIDs []string `json:"functionCallerIds"`
	ClassIDs          []int64  `json:"classIds"`
	WorkspaceID       string   `json:"workspaceId,omitempty"`
}

type SearchIntegrationsClassPayload struct {
	Integrations     []ClassIntegration     `json:"integrations"`
	HandledEndpoints []ClassHandledEndpoint `json:"handledEndpoints"`
}

type SearchIntegrationsResponse struct {
	Workspace   ResponseWorkspace                         `json:"workspace"`
	RepoContext []ResponseRepoContext                     `json:"repoContext,omitempty"`
	Functions   map[string][]FunctionIntegration          `json:"functions"`
	Classes     map[string]SearchIntegrationsClassPayload `json:"classes"`
	Errors      []SearchIntegrationError                  `json:"errors,omitempty"`
}

type SearchIntegrationError struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Code string `json:"code"`
}

func (h *Handlers) SearchIntegrations(w http.ResponseWriter, r *http.Request) {
	var req SearchIntegrationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body", nil)
		return
	}
	if req.WorkspaceID == "" {
		req.WorkspaceID = workspaceIDFromRequest(r)
	}
	_, workspaceScope, err := h.resolveWorkspaceScope(req.WorkspaceID, r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}

	resp := SearchIntegrationsResponse{
		Workspace:   workspaceScope.Workspace,
		RepoContext: workspaceScope.RepoContext,
		Functions:   make(map[string][]FunctionIntegration),
		Classes:     make(map[string]SearchIntegrationsClassPayload),
	}

	seenFunctions := make(map[string]struct{}, len(req.FunctionCallerIDs))
	for _, callerID := range req.FunctionCallerIDs {
		if callerID == "" {
			continue
		}
		if _, ok := seenFunctions[callerID]; ok {
			continue
		}
		seenFunctions[callerID] = struct{}{}
		integrations, err := h.loadFunctionIntegrationsScoped(r.Context(), callerID, true, workspaceScope)
		if err != nil {
			resp.Errors = append(resp.Errors, SearchIntegrationError{Kind: "function", ID: callerID, Code: "LOOKUP_FAILED"})
			continue
		}
		if integrations == nil {
			integrations = []FunctionIntegration{}
		}
		resp.Functions[callerID] = integrations
	}

	seenClasses := make(map[int64]struct{}, len(req.ClassIDs))
	classIDs := make([]int64, 0, len(req.ClassIDs))
	for _, classID := range req.ClassIDs {
		if classID <= 0 {
			continue
		}
		if _, ok := seenClasses[classID]; ok {
			continue
		}
		seenClasses[classID] = struct{}{}
		classIDs = append(classIDs, classID)
	}
	if len(classIDs) > 0 {
		for _, classID := range classIDs {
			classResp, err := h.loadClassIntegrationsResponse(r.Context(), classID, workspaceScope)
			if err != nil {
				resp.Errors = append(resp.Errors, SearchIntegrationError{Kind: "class", ID: strconv.FormatInt(classID, 10), Code: "LOOKUP_FAILED"})
				continue
			}
			resp.Classes[strconv.FormatInt(classID, 10)] = SearchIntegrationsClassPayload{
				Integrations:     coalesceClassIntegrations(classResp.Integrations),
				HandledEndpoints: coalesceClassHandledEndpoints(classResp.HandledEndpoints),
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
