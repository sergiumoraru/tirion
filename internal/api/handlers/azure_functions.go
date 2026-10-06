package handlers

import (
	"net/http"
	"strings"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func (h *Handlers) ListAzureFunctionTriggers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	triggerType := query.Get("type")
	functionName := query.Get("function")
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
	triggers, err := h.storage.GetAzureFunctionTriggers(repo, triggerType, functionName, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspaceScope.Workspace, "repoContext": workspaceScope.RepoContext, "triggers": triggers})
}

type AzureFunctionFlowResponse struct {
	Workspace   ResponseWorkspace                `json:"workspace"`
	RepoContext []ResponseRepoContext            `json:"repoContext,omitempty"`
	Triggers    []graph.AzureFunctionTriggerInfo `json:"triggers,omitempty"`
	Endpoints   []graph.EndpointInfo             `json:"endpoints,omitempty"`
}

func (h *Handlers) AzureFunctionFlow(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	functionName := query.Get("function")
	pattern := query.Get("q")
	limit, ok := parseLimitOnly(w, r, 100, 500)
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
	snapshotFilter := snapshotFilterForScope(workspaceScope)

	triggers, err := h.storage.GetAzureFunctionTriggers(repo, "", functionName, pattern, limit, 0, snapshotFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	endpoints, err := h.loadAzureFlowEndpoints(repo, triggers, functionName, pattern, limit, snapshotFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, AzureFunctionFlowResponse{
		Workspace:   workspaceScope.Workspace,
		RepoContext: workspaceScope.RepoContext,
		Triggers:    triggers,
		Endpoints:   endpoints,
	})
}

func (h *Handlers) loadAzureFlowEndpoints(repo string, triggers []graph.AzureFunctionTriggerInfo, functionName, pattern string, limit int, filters ...graph.SnapshotFilter) ([]graph.EndpointInfo, error) {
	if h == nil || h.storage == nil || len(triggers) == 0 {
		return nil, nil
	}

	terms := azureFlowEndpointSearchTerms(triggers, functionName, pattern)
	seen := make(map[string]bool)
	var candidates []graph.EndpointInfo
	for _, term := range terms {
		matches, err := h.storage.GetEndpoints(repo, "", "", term, limit, 0, filters...)
		if err != nil {
			return nil, err
		}
		for _, endpoint := range matches {
			key := azureFlowEndpointKey(endpoint)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, endpoint)
		}
	}
	return filterAzureFlowEndpoints(triggers, candidates, limit), nil
}

func azureFlowEndpointSearchTerms(triggers []graph.AzureFunctionTriggerInfo, functionName, pattern string) []string {
	var terms []string
	seen := make(map[string]bool)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if seen[key] {
			return
		}
		seen[key] = true
		terms = append(terms, value)
	}

	add(pattern)
	add(functionName)
	for _, trigger := range triggers {
		add(trigger.Route)
		add(trigger.FunctionName)
	}
	return terms
}

func filterAzureFlowEndpoints(triggers []graph.AzureFunctionTriggerInfo, endpoints []graph.EndpointInfo, limit int) []graph.EndpointInfo {
	if len(triggers) == 0 || len(endpoints) == 0 || limit <= 0 {
		return nil
	}

	var filtered []graph.EndpointInfo
	seen := make(map[string]bool)
	for _, endpoint := range endpoints {
		if !matchesAzureFlowEndpoint(triggers, endpoint) {
			continue
		}
		key := azureFlowEndpointKey(endpoint)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, endpoint)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func matchesAzureFlowEndpoint(triggers []graph.AzureFunctionTriggerInfo, endpoint graph.EndpointInfo) bool {
	endpointPath := normalizeAzureFlowIdentity(endpoint.Path)
	endpointHandler := normalizeAzureFlowIdentity(endpoint.Handler)
	for _, trigger := range triggers {
		triggerRoute := normalizeAzureFlowIdentity(trigger.Route)
		triggerName := normalizeAzureFlowIdentity(trigger.FunctionName)
		if triggerRoute != "" && (endpointPath == triggerRoute || strings.Contains(endpointPath, triggerRoute) || strings.Contains(triggerRoute, endpointPath)) {
			return true
		}
		if triggerName != "" && (endpointHandler == triggerName || strings.Contains(endpointHandler, triggerName)) {
			return true
		}
	}
	return false
}

func azureFlowEndpointKey(endpoint graph.EndpointInfo) string {
	return strings.ToLower(strings.TrimSpace(endpoint.RepoName + "|" + endpoint.FilePath + "|" + endpoint.Method + "|" + endpoint.Path + "|" + endpoint.Handler))
}

func normalizeAzureFlowIdentity(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "/"))
}
