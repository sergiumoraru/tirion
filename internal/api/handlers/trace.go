package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type TraceRequest struct {
	Function       string   `json:"function"`
	Match          string   `json:"match"`
	WorkspaceID    string   `json:"workspaceId,omitempty"`
	Profile        string   `json:"profile,omitempty"`
	Depth          int      `json:"depth"`
	NoTests        bool     `json:"noTests"`
	Resolve        *bool    `json:"resolve"`
	Exclude        []string `json:"exclude"`
	IncludeRepos   []string `json:"includeRepos"`
	ExcludeRepos   []string `json:"excludeRepos"`
	MaxNodes       int      `json:"maxNodes"`
	IncludeSource  bool     `json:"includeSource"`
	MaxSourceNodes int      `json:"maxSourceNodes"`
}

type TraceStats struct {
	DownstreamNodes int    `json:"downstreamNodes"`
	UpstreamNodes   int    `json:"upstreamNodes"`
	TraceTime       string `json:"traceTime"`
}

type TraceDirectionCompleteness struct {
	ReturnedNodes       int  `json:"returnedNodes"`
	AvailableNodes      int  `json:"availableNodes"`
	ExactAvailableNodes bool `json:"exactAvailableNodes"`
	Truncated           bool `json:"truncated"`
}

type TraceCompleteness struct {
	AppliedMaxNodes   int                        `json:"appliedMaxNodes"`
	Truncated         bool                       `json:"truncated"`
	TruncationReasons []string                   `json:"truncationReasons,omitempty"`
	Downstream        TraceDirectionCompleteness `json:"downstream"`
	Upstream          TraceDirectionCompleteness `json:"upstream"`
}

type TraceResponse struct {
	Function       string                    `json:"function"`
	Workspace      ResponseWorkspace         `json:"workspace"`
	RepoContext    []ResponseRepoContext     `json:"repoContext,omitempty"`
	Matches        []trace.MatchedFunction   `json:"matches"`
	Downstream     []*trace.TreeNode         `json:"downstream"`
	Upstream       []*trace.TreeNode         `json:"upstream"`
	Selected       string                    `json:"selectedMatch,omitempty"`
	UpstreamSource string                    `json:"upstreamSource,omitempty"`
	Stats          TraceStats                `json:"stats"`
	Completeness   TraceCompleteness         `json:"completeness"`
	Quality        trace.TraceQualitySummary `json:"quality"`
	NotFound       bool                      `json:"notFound,omitempty"`
	Suggestions    []string                  `json:"suggestions,omitempty"`
	WorkspaceHints []WorkspaceHint           `json:"workspaceHints,omitempty"`
	Warnings       []string                  `json:"warnings,omitempty"`
}

type TraceExpandRequest struct {
	CallerID     string   `json:"callerId"`
	Direction    string   `json:"direction"`
	WorkspaceID  string   `json:"workspaceId,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	Depth        int      `json:"depth"`
	MaxDepth     int      `json:"maxDepth"`
	NoTests      bool     `json:"noTests"`
	Resolve      *bool    `json:"resolve"`
	Exclude      []string `json:"exclude"`
	IncludeRepos []string `json:"includeRepos"`
	ExcludeRepos []string `json:"excludeRepos"`
	MaxNodes     int      `json:"maxNodes"`
	AllowedRepo  string   `json:"allowedRepo"`
}

type TraceExpandResponse struct {
	Workspace       ResponseWorkspace          `json:"workspace"`
	Children        []*trace.TreeNode          `json:"children"`
	AppliedMaxNodes int                        `json:"appliedMaxNodes"`
	Completeness    TraceDirectionCompleteness `json:"completeness"`
	Warnings        []string                   `json:"warnings,omitempty"`
}

type tracePruneStats struct {
	ReturnedNodes  int
	AvailableNodes int
	ExactAvailable bool
	Truncated      bool
}

func (h *Handlers) Trace(w http.ResponseWriter, r *http.Request) {
	var req TraceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body", nil)
		return
	}
	if req.Function == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing function name", nil)
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		req.WorkspaceID = workspaceIDFromRequest(r)
	}
	_, workspaceScope, err := h.resolveWorkspaceScope(req.WorkspaceID, r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	cfg := config.GetEffectiveTraceConfig()
	profile, _ := cfg.ResolveProfile(req.Profile)

	if req.Depth > trace.MaxDepth {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "depth must not exceed 32", nil)
		return
	}
	depth := req.Depth
	if depth <= 0 {
		if profile.Depth != nil && *profile.Depth > 0 {
			depth = *profile.Depth
		} else {
			depth = 4
		}
	}

	depth = min(depth, trace.MaxDepth)
	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		if profile.MaxNodes != nil && *profile.MaxNodes > 0 {
			maxNodes = *profile.MaxNodes
		} else {
			maxNodes = 2000
		}
	}
	if maxNodes > 10000 {
		maxNodes = 10000
	}

	resolve := false
	if profile.Resolve != nil {
		resolve = *profile.Resolve
	}
	if req.Resolve != nil {
		resolve = *req.Resolve
	}
	tuning := trace.NewTraceTuning(profile, depth)
	tuning.SnapshotIDs = activeSnapshotIDs(workspaceScope)
	tuning.IncludeLegacySnapshots = workspaceIncludesLegacy(workspaceScope)

	start := time.Now()
	resolution := resolveExecutionRoots(r.Context(), h, req.Function, workspaceScope)
	resolution = h.filterExecutionRootResolutionByWorkspace(r.Context(), resolution, workspaceScope)
	allCallerIDs := resolution.callerIDs()
	inputIsEndpoint := false
	if _, _, ok := parseFlowStart(req.Function); ok {
		inputIsEndpoint = len(allCallerIDs) > 0
	}
	if len(allCallerIDs) == 0 {
		var suggestions []string
		if fuzzy, err := h.storage.WithQueryContext(r.Context()).TrigramSearchInSnapshots(req.Function, 5, snapshotFilterForScope(workspaceScope)); err == nil {
			seen := make(map[string]bool)
			for _, r := range fuzzy {
				if r.Type == "function" && !seen[r.Name] {
					suggestions = append(suggestions, r.Name)
					seen[r.Name] = true
				}
			}
		}
		writeJSON(w, http.StatusOK, TraceResponse{
			Function:       req.Function,
			Workspace:      workspaceScope.Workspace,
			RepoContext:    workspaceScope.RepoContext,
			Matches:        []trace.MatchedFunction{},
			Downstream:     []*trace.TreeNode{},
			Upstream:       []*trace.TreeNode{},
			NotFound:       true,
			Suggestions:    suggestions,
			WorkspaceHints: h.workspaceHintsForQuery(r.Context(), req.Function, workspaceScope),
			Stats:          TraceStats{TraceTime: time.Since(start).String()},
			Completeness: TraceCompleteness{
				AppliedMaxNodes: maxNodes,
				Downstream: TraceDirectionCompleteness{
					ExactAvailableNodes: true,
				},
				Upstream: TraceDirectionCompleteness{
					ExactAvailableNodes: true,
				},
			},
		})
		return
	}

	filters := trace.BuildFilters(req.NoTests, req.Exclude, req.IncludeRepos, req.ExcludeRepos)

	selectedIDs := allCallerIDs
	upstreamFuncName := req.Function
	if inputIsEndpoint {
		// Endpoint input should resolve by handler IDs; name-based upstream on raw path is misleading.
		upstreamFuncName = ""
	}
	if req.Match != "" {
		found := false
		for _, id := range allCallerIDs {
			if id == req.Match {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "match not found", nil)
			return
		}
		selectedIDs = []string{req.Match}
		upstreamFuncName = ""
	}

	downstream, downstreamLimit := trace.TraceDownstreamWithLimitAndResolveTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), selectedIDs, depth, maxNodes, resolve, tuning)
	downstream = trace.FilterTree(downstream, filters)
	downstream = h.filterTraceTreeByWorkspace(r.Context(), downstream, workspaceScope)
	downstream, downstreamPrune := pruneTreeWithStats(downstream, maxNodes)
	integrationWarnings := h.appendFunctionIntegrationsToTrace(r.Context(), downstream, depth, workspaceScope)
	downstream, _ = pruneTreeWithStats(downstream, maxNodes)
	trace.AnnotateLowSignal(downstream)

	upstream, upstreamSource, upstreamLimit := trace.TraceUpstreamWithModeAndLimitTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), upstreamFuncName, selectedIDs, depth, maxNodes, tuning)
	upstream = trace.FilterTree(upstream, filters)
	upstream = h.filterTraceTreeByWorkspace(r.Context(), upstream, workspaceScope)
	upstream, upstreamPrune := pruneTreeWithStats(upstream, maxNodes)
	trace.AnnotateLowSignal(upstream)

	stats := TraceStats{
		DownstreamNodes: trace.CountNodes(downstream),
		UpstreamNodes:   trace.CountNodes(upstream),
		TraceTime:       time.Since(start).String(),
	}
	completeness := buildTraceCompleteness(maxNodes, downstreamLimit, downstreamPrune, upstreamLimit, upstreamPrune)
	quality := trace.TraceQualitySummary{
		Downstream: trace.CalculateTraceQuality(downstream),
		Upstream:   trace.CalculateTraceQuality(upstream),
	}
	warnings := buildTraceWarnings(completeness)
	warnings = append(warnings, integrationWarnings...)

	// Enrich trace nodes with source code if requested
	if req.IncludeSource {
		maxSrc := req.MaxSourceNodes
		if maxSrc <= 0 {
			maxSrc = 10
		}
		if maxSrc > stats.DownstreamNodes+stats.UpstreamNodes {
			maxSrc = stats.DownstreamNodes + stats.UpstreamNodes
		}
		if err := h.enrichTraceNodesWithSource(r.Context(), downstream, upstream, maxSrc, workspaceScope); err != nil {
			warnings = append(warnings, "Source enrichment could not be loaded for the selected workspace.")
		}
	}

	// Ensure slices are never nil so JSON encodes [] instead of null.
	matches := trace.DescribeCallerIDs(allCallerIDs)
	if matches == nil {
		matches = []trace.MatchedFunction{}
	}
	if downstream == nil {
		downstream = []*trace.TreeNode{}
	}
	if upstream == nil {
		upstream = []*trace.TreeNode{}
	}

	if r.Context().Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Trace time limit exceeded", nil)
		return
	}
	resp := TraceResponse{
		Function:       req.Function,
		Workspace:      workspaceScope.Workspace,
		RepoContext:    workspaceScope.RepoContext,
		Matches:        matches,
		Downstream:     downstream,
		Upstream:       upstream,
		UpstreamSource: upstreamSource,
		Stats:          stats,
		Completeness:   completeness,
		Quality:        quality,
		Warnings:       warnings,
	}
	if req.Match != "" {
		resp.Selected = req.Match
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) TraceExpand(w http.ResponseWriter, r *http.Request) {
	var req TraceExpandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body", nil)
		return
	}
	if req.CallerID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing caller id", nil)
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		req.WorkspaceID = workspaceIDFromRequest(r)
	}
	_, workspaceScope, err := h.resolveWorkspaceScope(req.WorkspaceID, r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if len(h.filterCallerIDsByWorkspace(r.Context(), []string{req.CallerID}, workspaceScope)) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "caller id is not in the selected workspace", nil)
		return
	}

	direction := strings.ToLower(req.Direction)
	if direction == "" {
		direction = "downstream"
	}
	if direction != "downstream" && direction != "upstream" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid direction", nil)
		return
	}
	req.Direction = direction

	cfg := config.GetEffectiveTraceConfig()
	profile, resolvedProfile := cfg.ResolveProfile(req.Profile)

	if req.Depth >= trace.MaxDepth || req.MaxDepth > trace.MaxDepth {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "expansion depth must be below 32 and maxDepth at most 32", nil)
		return
	}
	if req.Depth < 0 {
		req.Depth = 0
	}

	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		if profile.Depth != nil && *profile.Depth > 0 {
			maxDepth = *profile.Depth
		} else {
			maxDepth = req.Depth + 1
		}
	}
	maxDepth = min(maxDepth, trace.MaxDepth)
	if maxDepth < req.Depth+1 {
		maxDepth = req.Depth + 1
	}

	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		if profile.MaxNodes != nil && *profile.MaxNodes > 0 {
			maxNodes = *profile.MaxNodes
		} else {
			maxNodes = 2000
		}
	}
	if maxNodes > 10000 {
		maxNodes = 10000
	}
	req.MaxDepth = maxDepth
	req.MaxNodes = maxNodes

	resolve := false
	if profile.Resolve != nil {
		resolve = *profile.Resolve
	}
	if req.Resolve != nil {
		resolve = *req.Resolve
	}
	tuning := trace.NewTraceTuning(profile, maxDepth)
	tuning.SnapshotIDs = activeSnapshotIDs(workspaceScope)
	tuning.IncludeLegacySnapshots = workspaceIncludesLegacy(workspaceScope)

	filters := trace.BuildFilters(req.NoTests, req.Exclude, req.IncludeRepos, req.ExcludeRepos)
	cacheKey := ""
	if h.traceExpandCache != nil {
		snapshotIDs := activeSnapshotIDs(workspaceScope)
		sort.Slice(snapshotIDs, func(i, j int) bool { return snapshotIDs[i] < snapshotIDs[j] })
		cacheKey = fmt.Sprintf("%s::snapshots=%v::legacy=%t::scoped=%t", traceExpandCacheKey(req, resolve, resolvedProfile), snapshotIDs, workspaceIncludesLegacy(workspaceScope), workspaceScope.EnforceSnapshots)
		if cached, ok := h.traceExpandCache.get(cacheKey); ok {
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	var children []*trace.TreeNode
	var limitStats trace.LimitStats
	if direction == "downstream" {
		children, limitStats = trace.TraceDownstreamChildrenTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), req.CallerID, req.Depth, maxDepth, maxNodes, resolve, tuning)
	} else {
		children, _, limitStats = trace.TraceUpstreamChildrenTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), req.CallerID, req.Depth, maxDepth, maxNodes, req.AllowedRepo, tuning)
	}

	children = trace.FilterTree(children, filters)
	children = h.filterTraceTreeByWorkspace(r.Context(), children, workspaceScope)
	children, pruneStats := pruneTreeWithStats(children, maxNodes)
	var integrationWarnings []string
	if direction == "downstream" {
		integrationWarnings = h.appendFunctionIntegrationsToTrace(r.Context(), children, maxDepth, workspaceScope)
		children, _ = pruneTreeWithStats(children, maxNodes)
	}
	trace.AnnotateLowSignal(children)
	completeness, _ := buildTraceDirectionCompleteness(direction, limitStats, pruneStats)
	warnings := buildTraceExpandWarnings(direction, completeness)
	warnings = append(warnings, integrationWarnings...)
	resp := TraceExpandResponse{
		Workspace:       workspaceScope.Workspace,
		Children:        children,
		AppliedMaxNodes: maxNodes,
		Completeness:    completeness,
		Warnings:        warnings,
	}
	if r.Context().Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Trace time limit exceeded", nil)
		return
	}
	if h.traceExpandCache != nil && cacheKey != "" {
		h.traceExpandCache.set(cacheKey, resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

func pruneTree(nodes []*trace.TreeNode, maxNodes int) []*trace.TreeNode {
	pruned, _ := pruneTreeWithStats(nodes, maxNodes)
	return pruned
}

func pruneTreeWithStats(nodes []*trace.TreeNode, maxNodes int) ([]*trace.TreeNode, tracePruneStats) {
	availableNodes := trace.CountNodes(nodes)
	if maxNodes <= 0 {
		return nodes, tracePruneStats{
			ReturnedNodes:  availableNodes,
			AvailableNodes: availableNodes,
			ExactAvailable: true,
			Truncated:      false,
		}
	}
	if availableNodes <= maxNodes {
		return nodes, tracePruneStats{
			ReturnedNodes:  availableNodes,
			AvailableNodes: availableNodes,
			ExactAvailable: true,
			Truncated:      false,
		}
	}
	count := 0
	var walk func(n *trace.TreeNode) *trace.TreeNode
	walk = func(n *trace.TreeNode) *trace.TreeNode {
		if n == nil || count >= maxNodes {
			return nil
		}
		count++
		if len(n.Children) == 0 {
			return n
		}
		var children []*trace.TreeNode
		for _, child := range n.Children {
			if count >= maxNodes {
				break
			}
			if pruned := walk(child); pruned != nil {
				children = append(children, pruned)
			}
		}
		n.Children = children
		return n
	}

	var out []*trace.TreeNode
	for _, n := range nodes {
		if count >= maxNodes {
			break
		}
		if pruned := walk(n); pruned != nil {
			out = append(out, pruned)
		}
	}
	return out, tracePruneStats{
		ReturnedNodes:  count,
		AvailableNodes: availableNodes,
		ExactAvailable: true,
		Truncated:      availableNodes > count,
	}
}

func (h *Handlers) appendFunctionIntegrationsToTrace(ctx context.Context, nodes []*trace.TreeNode, maxDepth int, scope searchWorkspaceScope) []string {
	callerIDs := collectTraceCallerIDs(nodes, maxDepth)
	var warnings []string
	matchedByCaller, err := h.loadMatchedFunctionIntegrationsBatchForScope(ctx, callerIDs, scope)
	if err != nil {
		warnings = append(warnings, "Matched integration lookup incomplete for the selected workspace.")
	}
	functionContexts, err := h.loadFunctionContextBatch(ctx, javaTraceCallerIDs(callerIDs), scope)
	if err != nil {
		warnings = append(warnings, "Integration source lookup incomplete for the selected workspace.")
	}
	integrationCache := make(map[string][]FunctionIntegration)
	externalCache := make(map[string][]externalTraceCall)
	var walk func(node *trace.TreeNode, isRoot bool)
	walk = func(node *trace.TreeNode, isRoot bool) {
		if node == nil {
			return
		}
		if err := h.hydrateSyntheticTraceContinuation(ctx, node, maxDepth, scope); err != nil {
			warnings = append(warnings, "Integration continuation lookup incomplete.")
		}
		if node.CallerID != "" && node.Depth <= maxDepth {
			integrations, integrationsOK := integrationCache[node.CallerID]
			externalCalls, externalOK := externalCache[node.CallerID]
			if !integrationsOK || !externalOK {
				items, externalItems, err := h.loadTraceNodeEnrichmentFromContext(ctx, node.CallerID, isRoot, matchedByCaller[node.CallerID], functionContexts[node.CallerID], scope)
				if err == nil {
					integrations = items
					externalCalls = externalItems
				} else {
					integrations = matchedByCaller[node.CallerID]
					warnings = append(warnings, "Inferred integration lookup incomplete.")
				}
				integrationCache[node.CallerID] = integrations
				externalCache[node.CallerID] = externalCalls
			}
			if len(integrations) > 0 {
				node.Children = appendIntegrationTraceChildren(node, integrations, maxDepth)
			}
			if len(externalCalls) > 0 {
				node.Children = appendExternalTraceChildren(node, externalCalls, maxDepth)
			}
		}
		for _, child := range node.Children {
			walk(child, false)
		}
	}
	for _, node := range nodes {
		walk(node, true)
	}
	return uniqueStrings(warnings)
}

func javaTraceCallerIDs(callerIDs []string) []string {
	result := make([]string, 0, len(callerIDs))
	for _, callerID := range callerIDs {
		if callerIDSupportsSourceInferredTraceIntegrations(callerID) {
			result = append(result, callerID)
		}
	}
	return result
}

func (h *Handlers) loadTraceNodeEnrichmentFromContext(ctx context.Context, callerID string, includeInferred bool, matched []FunctionIntegration, fnCtx functionContext, scope searchWorkspaceScope) ([]FunctionIntegration, []externalTraceCall, error) {
	integrations := matched
	if !callerIDSupportsSourceInferredTraceIntegrations(callerID) {
		return integrations, nil, nil
	}
	if strings.TrimSpace(fnCtx.SourceCode) == "" {
		return integrations, nil, nil
	}

	wrapperCalls := extractHTTPWrapperCallExpressions(fnCtx.SourceCode)
	genericCalls := extractGenericAPIClientCallExpressions(fnCtx.SourceCode)
	if len(wrapperCalls) == 0 && len(genericCalls) == 0 {
		return integrations, nil, nil
	}

	lookupCache := make(map[string][]FunctionIntegration)
	var lookupErr error
	lookup := func(method, path string) []FunctionIntegration {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		key := method + "|" + path
		if items, ok := lookupCache[key]; ok {
			return items
		}
		items, err := h.lookupEndpointIntegrations(ctx, callerID, method, path, scope)
		if err != nil {
			lookupErr = err
			lookupCache[key] = nil
			return nil
		}
		lookupCache[key] = items
		return items
	}

	if len(integrations) == 0 {
		if includeInferred {
			integrations = h.inferRootFunctionIntegrationsForTraceFromContext(ctx, callerID, fnCtx, wrapperCalls, genericCalls, lookup)
		} else {
			integrations = h.inferNestedFunctionIntegrationsForTraceFromContext(callerID, fnCtx, genericCalls, lookup)
		}
	}

	externalCalls := h.loadExternalTraceCallsForTraceFromContext(callerID, fnCtx, genericCalls, lookup)
	if lookupErr != nil {
		return matched, nil, lookupErr
	}
	return integrations, externalCalls, nil
}

func collectTraceCallerIDs(nodes []*trace.TreeNode, maxDepth int) []string {
	ids := make([]string, 0, 64)
	seen := make(map[string]struct{}, 64)
	var walk func([]*trace.TreeNode)
	walk = func(nodes []*trace.TreeNode) {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			if node.CallerID != "" && node.Depth <= maxDepth {
				if _, ok := seen[node.CallerID]; !ok {
					seen[node.CallerID] = struct{}{}
					ids = append(ids, node.CallerID)
				}
			}
			walk(node.Children)
		}
	}
	walk(nodes)
	return ids
}

func (h *Handlers) hydrateSyntheticTraceContinuation(ctx context.Context, node *trace.TreeNode, maxDepth int, scope searchWorkspaceScope) error {
	if node == nil || node.CallerID == "" || node.Depth > maxDepth || len(node.Children) > 0 {
		return nil
	}
	if node.Source != "integration_handler" && node.Source != "integration_continuation" {
		return nil
	}

	children, err := h.loadDirectResolvedTraceChildren(ctx, node.CallerID, node.Depth, scope)
	if err != nil {
		return err
	}
	markSyntheticTraceContinuation(children)
	node.Children = children
	return nil
}

func (h *Handlers) loadDirectResolvedTraceChildren(ctx context.Context, callerID string, parentDepth int, scope searchWorkspaceScope) ([]*trace.TreeNode, error) {
	repo, file, name := trace.ParseCallerID(callerID)
	if repo == "" || file == "" || name == "" {
		return nil, nil
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT
			COALESCE(fn2.name, fc.callee_name) AS callee_name,
			COALESCE(f2.path, '') AS callee_file,
			COALESCE(r2.name, '') AS callee_repo,
			fc.line_number,
			COALESCE(fn2.start_line, 0),
			COALESCE(fc.callee_resolution_source, '') AS resolution_source,
			COALESCE(fc.callee_resolution_confidence, '') AS resolution_confidence
		FROM repositories r
		JOIN files f ON f.repo_id = r.id AND f.path = $2
		JOIN functions fn ON fn.file_id = f.id AND fn.name = $3
		JOIN function_calls fc ON fc.caller_function_id = fn.id
		LEFT JOIN functions fn2 ON fn2.id = fc.callee_function_id
		LEFT JOIN files f2 ON f2.id = fn2.file_id
		LEFT JOIN repositories r2 ON r2.id = f2.repo_id
		WHERE r.name = $1
		  AND fc.callee_function_id IS NOT NULL
		  AND ($4::bigint[] IS NULL OR f.snapshot_id = ANY($4) OR ($5 AND f.snapshot_id IS NULL))
		  AND ($4::bigint[] IS NULL OR f2.snapshot_id = ANY($4) OR ($5 AND f2.snapshot_id IS NULL))
		ORDER BY fc.line_number
	`, repo, file, name, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	children := make([]*trace.TreeNode, 0, 8)
	seen := make(map[string]bool, 8)
	for rows.Next() {
		var calleeName, calleeFile, calleeRepo string
		var lineNumber, targetLine int
		var resolutionSource, resolutionConfidence string
		if err := rows.Scan(&calleeName, &calleeFile, &calleeRepo, &lineNumber, &targetLine, &resolutionSource, &resolutionConfidence); err != nil {
			return nil, err
		}
		calleeID := trace.BuildCallerID(calleeRepo, calleeFile, calleeName)
		if calleeID == "" || seen[calleeID] {
			continue
		}
		seen[calleeID] = true

		confidence := "high"
		if resolutionConfidence != "" {
			confidence = strings.ToLower(resolutionConfidence)
		}
		detail := resolutionSource
		if detail == "" {
			detail = "resolved"
		}

		children = append(children, &trace.TreeNode{
			Name:       calleeName,
			File:       calleeFile,
			Repo:       calleeRepo,
			Line:       targetLine,
			Depth:      parentDepth + 1,
			EdgeType:   "call",
			Confidence: confidence,
			CallerID:   calleeID,
			Evidence: &trace.EdgeEvidence{
				Source: "function_calls",
				Detail: detail,
				Repo:   repo,
				File:   file,
				Line:   lineNumber,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return children, nil
}

func markSyntheticTraceContinuation(nodes []*trace.TreeNode) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		node.Source = "integration_continuation"
		if len(node.Children) > 0 {
			markSyntheticTraceContinuation(node.Children)
		}
	}
}

type externalTraceCall struct {
	Method     string
	Path       string
	LineNumber int
}

func (h *Handlers) inferRootFunctionIntegrationsForTraceFromContext(ctx context.Context, callerID string, fnCtx functionContext, wrapperCalls []httpWrapperCallExpression, genericCalls []genericAPIClientCallExpression, lookup func(method, path string) []FunctionIntegration) []FunctionIntegration {
	integrations := make([]FunctionIntegration, 0, 8)
	seen := make(map[string]struct{})
	localConstMap := loadLocalStringConstantMap(ctx, fnCtx)
	for _, call := range wrapperCalls {
		for _, path := range h.resolveHTTPWrapperCallPathExpressions(ctx, fnCtx, localConstMap, call.Args) {
			for _, item := range lookup(call.Method, path) {
				key := strings.Join([]string{item.Method, item.Path, item.TargetRepo, item.TargetHandler}, "|")
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				item.Resolution = "inferred"
				integrations = append(integrations, item)
			}
		}
	}

	methods := methodsFromTraceCalls(wrapperCalls, genericCalls)
	for _, path := range h.resolveEndpointConstantPaths(ctx, fnCtx) {
		for _, method := range methods {
			for _, item := range lookup(method, path) {
				key := strings.Join([]string{item.Method, item.Path, item.TargetRepo, item.TargetHandler}, "|")
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				item.Resolution = "inferred"
				integrations = append(integrations, item)
			}
		}
	}
	return integrations
}

func (h *Handlers) inferNestedFunctionIntegrationsForTraceFromContext(callerID string, fnCtx functionContext, calls []genericAPIClientCallExpression, lookup func(method, path string) []FunctionIntegration) []FunctionIntegration {
	if len(calls) == 0 {
		return nil
	}

	localValues := loadLocalStringValueMap(fnCtx)
	integrations := make([]FunctionIntegration, 0, 4)
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if len(call.Args) == 0 {
			continue
		}
		for _, path := range resolveGenericPathCandidatesFromExpression(localValues, call.Args[0]) {
			for _, item := range lookup(call.Method, path) {
				key := strings.Join([]string{item.Method, item.Path, item.TargetRepo, item.TargetHandler}, "|")
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				item.Resolution = "resolved"
				integrations = append(integrations, item)
			}
		}
	}
	return integrations
}

func (h *Handlers) loadExternalTraceCallsForTraceFromContext(callerID string, fnCtx functionContext, calls []genericAPIClientCallExpression, lookup func(method, path string) []FunctionIntegration) []externalTraceCall {
	if len(calls) == 0 {
		return nil
	}

	localValues := loadLocalStringValueMap(fnCtx)
	seen := make(map[string]struct{}, len(calls))
	result := make([]externalTraceCall, 0, len(calls))
	for _, call := range calls {
		if len(call.Args) == 0 {
			continue
		}
		for _, path := range resolveGenericPathCandidatesFromExpression(localValues, call.Args[0]) {
			if len(lookup(call.Method, path)) > 0 {
				continue
			}
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			key := call.Method + "|" + path
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, externalTraceCall{
				Method:     call.Method,
				Path:       path,
				LineNumber: 0,
			})
		}
	}
	return result
}

func methodsFromTraceCalls(wrapperCalls []httpWrapperCallExpression, genericCalls []genericAPIClientCallExpression) []string {
	methods := make([]string, 0, len(wrapperCalls)+len(genericCalls))
	for _, call := range wrapperCalls {
		methods = append(methods, call.Method)
	}
	for _, call := range genericCalls {
		methods = append(methods, call.Method)
	}
	return uniqueIntegrationStrings(methods)
}

func callerIDSupportsSourceInferredTraceIntegrations(callerID string) bool {
	_, file, _ := trace.ParseCallerID(callerID)
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(file)), ".java")
}

func appendIntegrationTraceChildren(parent *trace.TreeNode, integrations []FunctionIntegration, maxDepth int) []*trace.TreeNode {
	if parent == nil {
		return nil
	}
	if len(integrations) == 0 || parent.Depth > maxDepth {
		return parent.Children
	}

	childSeen := make(map[string]bool, len(parent.Children))
	for _, child := range parent.Children {
		key := integrationTraceNodeKey(child)
		if key != "" {
			childSeen[key] = true
		}
	}

	children := parent.Children
	for _, integration := range integrations {
		httpNode := &trace.TreeNode{
			Name:           "[" + integration.Method + " " + integration.Path + "]",
			Line:           integration.LineNumber,
			Depth:          parent.Depth + 1,
			EdgeType:       "http",
			Confidence:     integrationTraceConfidence(integration.Resolution),
			IsCrossService: parent.Repo != "" && integration.TargetRepo != "" && parent.Repo != integration.TargetRepo,
			HttpMethod:     integration.Method,
			HttpTarget:     integration.Path,
			Evidence: &trace.EdgeEvidence{
				Source: "function_integrations",
				Detail: integration.Resolution,
				Repo:   integration.TargetRepo,
				File:   integration.TargetFile,
				Line:   integration.LineNumber,
			},
		}

		if parent.Depth+2 <= maxDepth+2 && integration.TargetHandler != "" {
			handlerNode := &trace.TreeNode{
				Name:       "→ " + integration.TargetHandler + " [" + integration.TargetRepo + "]",
				Repo:       integration.TargetRepo,
				File:       integration.TargetFile,
				Line:       integration.LineNumber,
				Depth:      parent.Depth + 2,
				EdgeType:   "http",
				Confidence: integrationTraceConfidence(integration.Resolution),
				CallerID:   trace.BuildCallerID(integration.TargetRepo, integration.TargetFile, integration.TargetHandler),
				Evidence: &trace.EdgeEvidence{
					Source: "function_integrations",
					Detail: integration.Resolution,
					Repo:   integration.TargetRepo,
					File:   integration.TargetFile,
					Line:   integration.LineNumber,
				},
				Source: "integration_handler",
			}
			httpNode.Children = []*trace.TreeNode{handlerNode}
		}

		key := integrationTraceNodeKey(httpNode)
		if key != "" && childSeen[key] {
			continue
		}
		if key != "" {
			childSeen[key] = true
		}
		children = append(children, httpNode)
	}
	return children
}

func appendExternalTraceChildren(parent *trace.TreeNode, calls []externalTraceCall, maxDepth int) []*trace.TreeNode {
	if parent == nil {
		return nil
	}
	if len(calls) == 0 || parent.Depth > maxDepth {
		return parent.Children
	}

	childSeen := make(map[string]bool, len(parent.Children))
	for _, child := range parent.Children {
		key := integrationTraceNodeKey(child)
		if key != "" {
			childSeen[key] = true
		}
	}

	children := parent.Children
	for _, call := range calls {
		httpNode := &trace.TreeNode{
			Name:       "[" + call.Method + " " + call.Path + "]",
			Line:       call.LineNumber,
			Depth:      parent.Depth + 1,
			EdgeType:   "http",
			Confidence: "medium",
			HttpMethod: call.Method,
			HttpTarget: call.Path,
			Evidence: &trace.EdgeEvidence{
				Source: "function_integrations",
				Detail: "external",
				Repo:   parent.Repo,
				File:   parent.File,
				Line:   call.LineNumber,
			},
		}
		key := integrationTraceNodeKey(httpNode)
		if key != "" && childSeen[key] {
			continue
		}
		if key != "" {
			childSeen[key] = true
		}
		children = append(children, httpNode)
	}
	return children
}

func integrationTraceNodeKey(node *trace.TreeNode) string {
	if node == nil {
		return ""
	}
	if node.EdgeType == "http" && node.HttpMethod != "" && node.HttpTarget != "" {
		return strings.ToLower(node.EdgeType + "|" + node.HttpMethod + "|" + node.HttpTarget + "|" + node.Name)
	}
	return ""
}

func integrationTraceConfidence(resolution string) string {
	if resolution == "matched" {
		return "high"
	}
	return "medium"
}

type genericAPIClientCallExpression struct {
	Method string
	Args   []string
}

func extractGenericAPIClientCallExpressions(source string) []genericAPIClientCallExpression {
	const receiver = "api."
	calls := make([]genericAPIClientCallExpression, 0, 8)

	for offset := 0; offset < len(source); {
		idx := strings.Index(source[offset:], receiver)
		if idx < 0 {
			break
		}
		start := offset + idx
		methodStart := start + len(receiver)
		openIdx := strings.Index(source[methodStart:], "(")
		if openIdx < 0 {
			break
		}
		openPos := methodStart + openIdx
		rawMethod := strings.TrimSpace(source[methodStart:openPos])
		method := normalizeGenericHTTPMethod(rawMethod)
		if method == "" {
			offset = openPos + 1
			continue
		}
		closePos := findMatchingParen(source, openPos)
		if closePos <= openPos {
			offset = openPos + 1
			continue
		}
		args := splitTopLevelJavaArgs(source[openPos+1 : closePos])
		calls = append(calls, genericAPIClientCallExpression{
			Method: method,
			Args:   args,
		})
		offset = closePos + 1
	}
	return calls
}

func normalizeGenericHTTPMethod(raw string) string {
	switch strings.TrimSpace(raw) {
	case "get":
		return "GET"
	case "post":
		return "POST"
	case "put":
		return "PUT"
	case "delete":
		return "DELETE"
	default:
		return ""
	}
}

var javaStringAssignmentPattern = regexp.MustCompile(`(?:private|protected|public)?\s*(?:static\s+)?(?:final\s+)?String\s+([A-Za-z0-9_]+)\s*=\s*([^;]+);`)

func loadLocalStringValueMap(fnCtx functionContext) map[string]string {
	source := strings.TrimSpace(fnCtx.SourceCode)
	if source == "" {
		return nil
	}

	values := make(map[string]string, 8)
	for _, match := range javaStringAssignmentPattern.FindAllStringSubmatch(source, -1) {
		if len(match) < 3 {
			continue
		}
		name := strings.TrimSpace(match[1])
		expr := strings.TrimSpace(match[2])
		if name == "" || expr == "" {
			continue
		}
		if resolved := resolveJavaStringExpression(values, expr); resolved != "" {
			values[name] = resolved
		}
	}
	return values
}

func resolveGenericPathCandidatesFromExpression(localValues map[string]string, expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	if value, ok := localValues[expr]; ok {
		return []string{value}
	}
	if resolved := resolveJavaStringExpression(localValues, expr); resolved != "" {
		return []string{resolved}
	}
	return inferInternalPathsFromSource(expr)
}

func resolveJavaStringExpression(localValues map[string]string, expr string) string {
	parts := splitTopLevelJavaConcat(expr)
	if len(parts) == 0 {
		return ""
	}

	var b strings.Builder
	hasLiteral := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if unquoted, ok := unquoteJavaString(part); ok {
			b.WriteString(unquoted)
			hasLiteral = true
			continue
		}
		if localValues != nil {
			if value, ok := localValues[part]; ok {
				b.WriteString(value)
				hasLiteral = true
				continue
			}
		}
		token := lastJavaIdentifier(part)
		if token == "" {
			continue
		}
		b.WriteString("{")
		b.WriteString(token)
		b.WriteString("}")
	}
	if !hasLiteral {
		return ""
	}
	return b.String()
}

func splitTopLevelJavaConcat(source string) []string {
	parts := make([]string, 0, 8)
	start := 0
	parenDepth := 0
	inString := false
	var quote byte
	escaped := false
	for i := 0; i < len(source); i++ {
		ch := source[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				inString = false
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inString = true
			quote = ch
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '+':
			if parenDepth == 0 {
				parts = append(parts, strings.TrimSpace(source[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(source[start:]); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

func unquoteJavaString(value string) (string, bool) {
	if len(value) < 2 {
		return "", false
	}
	if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1], true
	}
	return "", false
}

func lastJavaIdentifier(value string) string {
	identifierPattern := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	matches := identifierPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func buildTraceCompleteness(maxNodes int, downstreamLimit trace.LimitStats, downstream tracePruneStats, upstreamLimit trace.LimitStats, upstream tracePruneStats) TraceCompleteness {
	downstreamCompleteness, downstreamReason := buildTraceDirectionCompleteness("downstream", downstreamLimit, downstream)
	upstreamCompleteness, upstreamReason := buildTraceDirectionCompleteness("upstream", upstreamLimit, upstream)
	truncationReasons := make([]string, 0, 2)
	if downstreamReason != "" {
		truncationReasons = append(truncationReasons, downstreamReason)
	}
	if upstreamReason != "" {
		truncationReasons = append(truncationReasons, upstreamReason)
	}
	return TraceCompleteness{
		AppliedMaxNodes:   maxNodes,
		Truncated:         downstreamCompleteness.Truncated || upstreamCompleteness.Truncated,
		TruncationReasons: truncationReasons,
		Downstream:        downstreamCompleteness,
		Upstream:          upstreamCompleteness,
	}
}

func buildTraceDirectionCompleteness(direction string, limit trace.LimitStats, prune tracePruneStats) (TraceDirectionCompleteness, string) {
	completeness := TraceDirectionCompleteness{
		ReturnedNodes:       prune.ReturnedNodes,
		AvailableNodes:      prune.ReturnedNodes,
		ExactAvailableNodes: true,
		Truncated:           false,
	}
	if limit.Truncated {
		completeness.Truncated = true
		completeness.AvailableNodes = prune.ReturnedNodes
		completeness.ExactAvailableNodes = false
		return completeness, direction + " graph clipped by maxNodes before exact available nodes could be computed"
	}
	if prune.Truncated {
		completeness.Truncated = true
		completeness.AvailableNodes = prune.AvailableNodes
		completeness.ExactAvailableNodes = prune.ExactAvailable
		return completeness, direction + " graph clipped by maxNodes"
	}
	completeness.AvailableNodes = prune.AvailableNodes
	completeness.ExactAvailableNodes = prune.ExactAvailable
	return completeness, ""
}

func buildTraceWarnings(completeness TraceCompleteness) []string {
	if !completeness.Truncated {
		return nil
	}
	warnings := make([]string, 0, 2)
	if completeness.Downstream.Truncated {
		warnings = append(warnings, buildTraceDirectionWarning("Downstream", completeness.Downstream))
	}
	if completeness.Upstream.Truncated {
		warnings = append(warnings, buildTraceDirectionWarning("Upstream", completeness.Upstream))
	}
	return warnings
}

func buildTraceDirectionWarning(direction string, completeness TraceDirectionCompleteness) string {
	if completeness.ExactAvailableNodes {
		return fmt.Sprintf("%s trace is clipped. Showing %d of %d available nodes. Increase maxNodes to see the full graph.", direction, completeness.ReturnedNodes, completeness.AvailableNodes)
	}
	if direction == "Upstream" {
		// Upstream expansion also stops at a fixed caller fan-out, which maxNodes
		// does not raise.
		return fmt.Sprintf("%s trace is clipped. Showing %d nodes; more are available, but the exact total is unknown. The limit is maxNodes or the caller fan-out cap (50 direct callers, 20 per caller); trace a narrower start function if raising maxNodes does not help.", direction, completeness.ReturnedNodes)
	}
	return fmt.Sprintf("%s trace is clipped. Showing %d nodes; more are available, but the exact total is unknown at this maxNodes setting.", direction, completeness.ReturnedNodes)
}

func buildTraceExpandWarnings(direction string, completeness TraceDirectionCompleteness) []string {
	if !completeness.Truncated {
		return nil
	}
	label := "Downstream"
	if strings.EqualFold(direction, "upstream") {
		label = "Upstream"
	}
	return []string{buildTraceDirectionWarning(label, completeness)}
}

func traceExpandCacheKey(req TraceExpandRequest, resolve bool, profile string) string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	return strings.Join([]string{
		profile,
		req.CallerID,
		strings.ToLower(req.Direction),
		strconv.Itoa(req.Depth),
		strconv.Itoa(req.MaxDepth),
		strconv.Itoa(req.MaxNodes),
		strconv.FormatBool(req.NoTests),
		strconv.FormatBool(resolve),
		strings.ToLower(strings.TrimSpace(req.WorkspaceID)),
		normalizeCacheList(req.Exclude),
		normalizeCacheList(req.IncludeRepos),
		normalizeCacheList(req.ExcludeRepos),
		strings.ToLower(strings.TrimSpace(req.AllowedRepo)),
	}, "::")
}

func normalizeCacheList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, strings.ToLower(value))
	}
	if len(out) == 0 {
		return ""
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// enrichTraceNodesWithSource attaches source code to root + depth-1 trace nodes.
func (h *Handlers) enrichTraceNodesWithSource(ctx context.Context, downstream, upstream []*trace.TreeNode, maxNodes int, scope searchWorkspaceScope) error {
	// Collect caller IDs from root nodes and their immediate children (depth 1)
	callerIDs := make([]string, 0, maxNodes)
	seen := make(map[string]bool)

	collectShallow := func(nodes []*trace.TreeNode) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if n.CallerID != "" && !seen[n.CallerID] && len(callerIDs) < maxNodes {
				callerIDs = append(callerIDs, n.CallerID)
				seen[n.CallerID] = true
			}
			for _, child := range n.Children {
				if child == nil {
					continue
				}
				if child.CallerID != "" && !seen[child.CallerID] && len(callerIDs) < maxNodes {
					callerIDs = append(callerIDs, child.CallerID)
					seen[child.CallerID] = true
				}
			}
		}
	}
	collectShallow(downstream)
	collectShallow(upstream)

	if len(callerIDs) == 0 {
		return nil
	}

	sourceMap, err := h.loadFunctionContextBatch(ctx, callerIDs, scope)
	if err != nil {
		return err
	}

	// Attach source to nodes
	attachSource := func(nodes []*trace.TreeNode) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if src, ok := sourceMap[n.CallerID]; ok && src.SourceCode != "" {
				n.Source = src.SourceCode
			}
			for _, child := range n.Children {
				if child == nil {
					continue
				}
				if src, ok := sourceMap[child.CallerID]; ok && src.SourceCode != "" {
					child.Source = src.SourceCode
				}
			}
		}
	}
	attachSource(downstream)
	attachSource(upstream)
	return nil
}
