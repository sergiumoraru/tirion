package handlers

import (
	"net/http"
	"strings"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func (h *Handlers) ListGraphQLOperations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	operationType := query.Get("type")
	name := query.Get("name")
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
	operations, err := h.storage.GetGraphQLOperations(repo, operationType, name, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspaceScope.Workspace, "repoContext": workspaceScope.RepoContext, "operations": operations})
}

func (h *Handlers) ListGraphQLOperationUsages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	caller := query.Get("caller")
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

	usages, err := h.storage.GetGraphQLOperationUsages(repo, caller, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspaceScope.Workspace, "repoContext": workspaceScope.RepoContext, "usages": usages})
}

func (h *Handlers) ListGraphQLBackendEntrypoints(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
	registrationKind := query.Get("kind")
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

	entrypoints, err := h.storage.GetGraphQLBackendEntrypoints(repo, registrationKind, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspaceScope.Workspace, "repoContext": workspaceScope.RepoContext, "entrypoints": entrypoints})
}

func (h *Handlers) ListGraphQLControllers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := query.Get("repo")
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

	controllers, err := h.storage.GetGraphQLControllerLinks(repo, pattern, limit, offset, snapshotFilterForScope(workspaceScope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"workspace": workspaceScope.Workspace, "repoContext": workspaceScope.RepoContext, "controllers": controllers})
}

type GraphQLFlowEntrypoint struct {
	graph.GraphQLBackendEntrypointInfo
	MatchConfidence string `json:"match_confidence"`
}

type GraphQLFlowResponse struct {
	Workspace    ResponseWorkspace                    `json:"workspace"`
	RepoContext  []ResponseRepoContext                `json:"repoContext,omitempty"`
	FrontendRepo string                               `json:"frontendRepo,omitempty"`
	BackendRepo  string                               `json:"backendRepo,omitempty"`
	Operations   []graph.GraphQLOperationInfo         `json:"operations,omitempty"`
	Usages       []graph.GraphQLOperationUsageInfo    `json:"usages,omitempty"`
	Resolvers    []graph.GraphQLOperationResolverInfo `json:"resolvers,omitempty"`
	Entrypoints  []GraphQLFlowEntrypoint              `json:"entrypoints,omitempty"`
	Controllers  []graph.GraphQLControllerLinkInfo    `json:"controllers,omitempty"`
	Inferences   []string                             `json:"inferences,omitempty"`
	// Truncated names the lists that were cut short (by the response limit or by
	// the scan ceiling). Absent means every list is complete for the filters used.
	Truncated []string `json:"truncated,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// graphQLFlowScanCeiling bounds how many rows are scanned for lists that can
// only be filtered in memory (backend entrypoints and controller links).
const graphQLFlowScanCeiling = 5000

// fetchAllPages reads pages until the source is exhausted or ceiling rows were
// read. truncated reports that rows remained beyond the ceiling.
func fetchAllPages[T any](pageSize, ceiling int, fetch func(limit, offset int) ([]T, error)) (items []T, truncated bool, err error) {
	for len(items) < ceiling {
		want := min(pageSize, ceiling-len(items))
		page, err := fetch(want+1, len(items))
		if err != nil {
			return nil, false, err
		}
		if len(page) > want {
			items = append(items, page[:want]...)
			if len(items) >= ceiling {
				return items, true, nil
			}
			continue
		}
		return append(items, page...), false, nil
	}
	return items, false, nil
}

// capFlowList trims a list to max and reports whether anything was dropped.
func capFlowList[T any](items []T, max int) ([]T, bool) {
	if max > 0 && len(items) > max {
		return items[:max], true
	}
	return items, false
}

func (h *Handlers) GraphQLFlow(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	frontendRepo := strings.TrimSpace(query.Get("frontendRepo"))
	backendRepo := strings.TrimSpace(query.Get("backendRepo"))
	operation := strings.TrimSpace(query.Get("operation"))
	limit, ok := parseLimitOnly(w, r, 50, 200)
	if !ok {
		return
	}
	if operation == "" && frontendRepo == "" && backendRepo == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "provide at least one of operation, frontendRepo, or backendRepo", nil)
		return
	}

	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	for _, repo := range []string{frontendRepo, backendRepo} {
		if err := h.requireRepo(r.Context(), repo, workspaceScope); err != nil {
			writeLookupError(w, err)
			return
		}
	}
	snapshotFilter := snapshotFilterForScope(workspaceScope)
	loadFrontend, loadBackend := graphQLFlowLookupPlan(frontendRepo, backendRepo, operation)

	var operations []graph.GraphQLOperationInfo
	var usages []graph.GraphQLOperationUsageInfo
	var resolvers []graph.GraphQLOperationResolverInfo
	var entrypoints []graph.GraphQLBackendEntrypointInfo
	var controllers []graph.GraphQLControllerLinkInfo
	var truncated []string
	var controllersScanCut bool
	markTruncated := func(name string, cut bool) {
		if cut {
			truncated = append(truncated, name)
		}
	}

	if loadFrontend {
		var err error
		var cut bool
		operations, err = h.storage.GetGraphQLOperations(frontendRepo, "", operation, operation, limit+1, 0, snapshotFilter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		operations, cut = capFlowList(operations, limit)
		markTruncated("operations", cut)

		// The SQL pattern is a substring match; the exact operation-name filter runs
		// in memory, so every page is scanned (up to the ceiling) before capping.
		var scanCut bool
		usages, scanCut, err = fetchAllPages(500, graphQLFlowScanCeiling, func(n, offset int) ([]graph.GraphQLOperationUsageInfo, error) {
			return h.storage.GetGraphQLOperationUsages(frontendRepo, "", operation, n, offset, snapshotFilter)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		usages = filterUsagesByOperationName(usages, operation)
		usages, cut = capFlowList(usages, limit*4)
		markTruncated("usages", scanCut || cut)
	}

	if loadBackend {
		var err error
		var scanCut, cut bool
		entrypoints, scanCut, err = fetchAllPages(500, graphQLFlowScanCeiling, func(n, offset int) ([]graph.GraphQLBackendEntrypointInfo, error) {
			return h.storage.GetGraphQLBackendEntrypoints(backendRepo, "", "", n, offset, snapshotFilter)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		markTruncated("entrypoints", scanCut)
		var controllersCut bool
		controllers, controllersCut, err = fetchAllPages(500, graphQLFlowScanCeiling, func(n, offset int) ([]graph.GraphQLControllerLinkInfo, error) {
			return h.storage.GetGraphQLControllerLinks(backendRepo, "", n, offset, snapshotFilter)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		resolvers, err = h.storage.GetGraphQLOperationResolvers(backendRepo, "", operation, operation, limit*20+1, 0, snapshotFilter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		resolvers, cut = capFlowList(resolvers, limit*20)
		markTruncated("resolvers", cut)
		controllersScanCut = controllersCut
	}

	flowEntrypoints, inferences := buildGraphQLFlowEntrypoints(operations, usages, entrypoints)
	flowEntrypoints, cutEntrypoints := capFlowList(flowEntrypoints, limit*4)
	markTruncated("entrypoints", cutEntrypoints)
	controllers = filterGraphQLControllersForEntrypoints(controllers, flowEntrypoints)
	controllers, cutControllers := capFlowList(controllers, limit*20)
	markTruncated("controllers", controllersScanCut || cutControllers)
	var warnings []string
	if len(truncated) > 0 {
		warnings = append(warnings, "GraphQL flow results are incomplete: "+strings.Join(uniqueNonEmptyStrings(truncated), ", ")+" were cut short; narrow the operation or repo filters, or raise limit")
	}

	writeJSON(w, http.StatusOK, GraphQLFlowResponse{
		Workspace:    workspaceScope.Workspace,
		RepoContext:  workspaceScope.RepoContext,
		FrontendRepo: frontendRepo,
		BackendRepo:  backendRepo,
		Operations:   operations,
		Usages:       usages,
		Resolvers:    resolvers,
		Entrypoints:  flowEntrypoints,
		Controllers:  controllers,
		Inferences:   inferences,
		Truncated:    uniqueNonEmptyStrings(truncated),
		Warnings:     warnings,
	})
}

func graphQLFlowLookupPlan(frontendRepo, backendRepo, operation string) (loadFrontend, loadBackend bool) {
	loadFrontend = strings.TrimSpace(frontendRepo) != "" || strings.TrimSpace(operation) != ""
	loadBackend = strings.TrimSpace(backendRepo) != "" || strings.TrimSpace(operation) != ""
	return loadFrontend, loadBackend
}

func filterUsagesByOperationName(usages []graph.GraphQLOperationUsageInfo, operation string) []graph.GraphQLOperationUsageInfo {
	operation = strings.TrimSpace(strings.ToLower(operation))
	if operation == "" {
		return usages
	}
	out := make([]graph.GraphQLOperationUsageInfo, 0, len(usages))
	for _, usage := range usages {
		if strings.EqualFold(graphQLUsageOperationName(usage), operation) {
			out = append(out, usage)
		}
	}
	return out
}

func graphQLUsageOperationName(usage graph.GraphQLOperationUsageInfo) string {
	if usage.ResolvedOperation != nil {
		if name := strings.TrimSpace(*usage.ResolvedOperation); name != "" {
			return name
		}
	}
	const directPrefix = "__graphql_operation__:"
	importPath := strings.TrimSpace(usage.ImportPath)
	if strings.HasPrefix(strings.ToLower(importPath), directPrefix) {
		return strings.TrimSpace(importPath[len(directPrefix):])
	}
	return strings.TrimSpace(usage.ImportedAs)
}

func buildGraphQLFlowEntrypoints(operations []graph.GraphQLOperationInfo, usages []graph.GraphQLOperationUsageInfo, entrypoints []graph.GraphQLBackendEntrypointInfo) ([]GraphQLFlowEntrypoint, []string) {
	if len(entrypoints) == 0 {
		return nil, nil
	}

	opTokens := make(map[string]struct{})
	for _, operation := range operations {
		for _, tok := range graphQLFlowTokens(operation.OperationName, operation.FilePath) {
			opTokens[tok] = struct{}{}
		}
	}
	for _, usage := range usages {
		if operationName := graphQLUsageOperationName(usage); operationName != "" {
			for _, tok := range graphQLFlowTokens(operationName, usage.FilePath) {
				opTokens[tok] = struct{}{}
			}
		}
		for _, tok := range graphQLFlowTokens(usage.ImportedAs, usage.ImportPath) {
			opTokens[tok] = struct{}{}
		}
	}

	out := make([]GraphQLFlowEntrypoint, 0, len(entrypoints))
	var inferences []string
	for _, entrypoint := range entrypoints {
		confidence := classifyGraphQLEntrypointMatch(opTokens, entrypoint)
		if confidence == "" && len(opTokens) > 0 {
			continue
		}
		if confidence == "" {
			confidence = "repo"
		}
		out = append(out, GraphQLFlowEntrypoint{
			GraphQLBackendEntrypointInfo: entrypoint,
			MatchConfidence:              confidence,
		})
	}
	if len(opTokens) > 0 {
		inferences = append(inferences, "backend entrypoints are matched heuristically from GraphQL operation names and file-path tokens")
	}
	return out, inferences
}

func graphQLFlowTokens(values ...string) []string {
	blacklist := map[string]struct{}{
		"graphql": {}, "query": {}, "mutation": {}, "subscription": {}, "shared": {}, "src": {}, "views": {}, "composables": {}, "apollo": {},
	}
	seen := make(map[string]struct{})
	var out []string
	for _, value := range values {
		for _, raw := range splitIdentifierTokens(value) {
			raw = strings.ToLower(strings.TrimSpace(raw))
			if len(raw) < 3 {
				continue
			}
			if _, blocked := blacklist[raw]; blocked {
				continue
			}
			if _, ok := seen[raw]; ok {
				continue
			}
			seen[raw] = struct{}{}
			out = append(out, raw)
		}
	}
	return out
}

func classifyGraphQLEntrypointMatch(opTokens map[string]struct{}, entrypoint graph.GraphQLBackendEntrypointInfo) string {
	if len(opTokens) == 0 {
		return ""
	}
	entryTokens := graphQLFlowTokens(entrypoint.HandlerName, entrypoint.FilePath, entrypoint.ControllersPath)
	matches := 0
	for _, token := range entryTokens {
		if _, ok := opTokens[token]; ok {
			matches++
		}
	}
	switch {
	case matches >= 2:
		return "high"
	case matches == 1:
		return "medium"
	default:
		return ""
	}
}

func filterGraphQLControllersForEntrypoints(controllers []graph.GraphQLControllerLinkInfo, entrypoints []GraphQLFlowEntrypoint) []graph.GraphQLControllerLinkInfo {
	if len(controllers) == 0 || len(entrypoints) == 0 {
		return nil
	}
	allowed := make(map[int64]struct{}, len(entrypoints))
	for _, entrypoint := range entrypoints {
		allowed[entrypoint.ID] = struct{}{}
	}
	out := make([]graph.GraphQLControllerLinkInfo, 0, len(controllers))
	for _, controller := range controllers {
		if _, ok := allowed[controller.EntrypointID]; ok {
			out = append(out, controller)
		}
	}
	return out
}
