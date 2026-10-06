package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type FlowRequest struct {
	Start                  string   `json:"start"`
	WorkspaceID            string   `json:"workspaceId,omitempty"`
	Depth                  int      `json:"depth"`
	MaxHops                int      `json:"maxHops"`
	NoTests                bool     `json:"noTests"`
	Exclude                []string `json:"exclude"`
	IncludeRelatedEntities bool     `json:"includeRelatedEntities"`
	StrictMode             *bool    `json:"strictMode,omitempty"`
}

type FlowEndpoint struct {
	Repo    string `json:"repo"`
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
}

type FlowDiscoveryCandidate struct {
	Entity   string       `json:"entity"`
	From     FlowEndpoint `json:"from"`
	Endpoint FlowEndpoint `json:"endpoint"`
	Reason   string       `json:"reason"`
}

type FlowVia struct {
	Type       string `json:"type"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	Queue      string `json:"queue,omitempty"`
	ClientType string `json:"clientType,omitempty"`
	External   bool   `json:"external,omitempty"`
	Entity     string `json:"entity,omitempty"`
	Table      string `json:"table,omitempty"`
	Access     string `json:"access,omitempty"`
}

type FlowHop struct {
	Depth int          `json:"depth"`
	From  FlowEndpoint `json:"from"`
	Via   FlowVia      `json:"via"`
	To    FlowEndpoint `json:"to"`
}

type FlowStats struct {
	Hops     int    `json:"hops"`
	Roots    int    `json:"roots"`
	Duration string `json:"duration"`
}

type FlowCompleteness struct {
	AppliedMaxHops     int      `json:"appliedMaxHops"`
	ReturnedHops       int      `json:"returnedHops"`
	AvailableHops      int      `json:"availableHops"`
	ExactAvailableHops bool     `json:"exactAvailableHops"`
	Truncated          bool     `json:"truncated"`
	TruncationReasons  []string `json:"truncationReasons,omitempty"`
}

type FlowAssumptions struct {
	StrictMode               bool `json:"strictMode"`
	IncludeRelatedEntities   bool `json:"includeRelatedEntities"`
	IncludeInternalCalls     bool `json:"includeInternalCalls"`
	InternalCallHopSoftLimit int  `json:"internalCallHopSoftLimit,omitempty"`
	StrictModeReducedHops    int  `json:"strictModeReducedHops,omitempty"`
	InternalHopsPruned       int  `json:"internalHopsPruned,omitempty"`
	InternalCallsSoftLimited bool `json:"internalCallsSoftLimited,omitempty"`
}

type FlowResponse struct {
	Workspace      ResponseWorkspace        `json:"workspace"`
	RepoContext    []ResponseRepoContext    `json:"repoContext,omitempty"`
	Roots          []FlowEndpoint           `json:"roots"`
	Hops           []FlowHop                `json:"hops"`
	Narratives     []FlowNarrative          `json:"narratives,omitempty"`
	Callers        []FlowEndpoint           `json:"callers,omitempty"`
	Candidates     []FlowDiscoveryCandidate `json:"candidates,omitempty"`
	Stats          FlowStats                `json:"stats"`
	Completeness   FlowCompleteness         `json:"completeness"`
	Assumptions    FlowAssumptions          `json:"assumptions"`
	WorkspaceHints []WorkspaceHint          `json:"workspaceHints,omitempty"`
	Warnings       []string                 `json:"warnings,omitempty"`
}

type FlowNarrative struct {
	Root  FlowEndpoint        `json:"root"`
	Steps []FlowNarrativeStep `json:"steps"`
}

type FlowNarrativeStep struct {
	Kind       string       `json:"kind"`
	From       FlowEndpoint `json:"from"`
	Via        *FlowVia     `json:"via,omitempty"`
	To         FlowEndpoint `json:"to"`
	Condition  string       `json:"condition,omitempty"`
	Evidence   []string     `json:"evidence,omitempty"`
	Confidence string       `json:"confidence,omitempty"`
}

type endpointRootCandidate struct {
	endpoint      FlowEndpoint
	callerID      string
	candidateRank int
	callerCount   int64
}

type flowBuildStats struct {
	InternalCallHopSoftLimit int
	MaxHopsReached           bool
	InternalCallsSoftLimited bool
	Candidates               []FlowDiscoveryCandidate
	DiscoveryWarnings        []string
}

func (h *Handlers) Flow(w http.ResponseWriter, r *http.Request) {
	var req FlowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body", nil)
		return
	}
	rawStart := strings.TrimSpace(req.Start)
	if rawStart == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing start", nil)
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
	startMode, _ := parseFlowStartMode(rawStart)
	if req.Depth > trace.MaxDepth || req.MaxHops > 10000 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "depth must not exceed 32 and maxHops must not exceed 10000", nil)
		return
	}
	maxDepth := req.Depth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	maxHops := req.MaxHops
	if maxHops <= 0 {
		maxHops = 200
	}
	strictMode := true
	if req.StrictMode != nil {
		strictMode = *req.StrictMode
	} else if req.IncludeRelatedEntities {
		// Related-entity expansion is exploratory by design, so default strict mode off there.
		strictMode = false
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resolution := resolveExecutionRoots(ctx, h, rawStart, workspaceScope)
	if ctx.Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Flow time limit exceeded", nil)
		return
	}
	resolution = h.filterExecutionRootResolutionByWorkspace(ctx, resolution, workspaceScope)
	rootCallerIDs := resolution.callerIDs()
	rootEndpoints := resolution.endpoints()

	if len(rootCallerIDs) == 0 {
		writeJSON(w, http.StatusOK, FlowResponse{
			Workspace:      workspaceScope.Workspace,
			RepoContext:    workspaceScope.RepoContext,
			Roots:          []FlowEndpoint{},
			Hops:           []FlowHop{},
			WorkspaceHints: h.workspaceHintsForQuery(ctx, rawStart, workspaceScope),
			Warnings:       []string{"no matching endpoint or function in active workspace"},
			Completeness: FlowCompleteness{
				AppliedMaxHops: maxHops,
			},
			Assumptions: FlowAssumptions{
				StrictMode:             strictMode,
				IncludeRelatedEntities: req.IncludeRelatedEntities,
			},
		})
		return
	}

	startTime := time.Now()
	entityErr := ensureRepositoryEntities(ctx, h.storage.Pool(), workspaceScope)
	snapshotIDs := activeSnapshotIDs(workspaceScope)
	primaryEntities := inferPrimaryEntities(ctx, h.storage.Pool(), rootCallerIDs, rootEndpoints, snapshotIDs)
	baseEntities := primaryEntities
	var relationshipErr error
	if req.IncludeRelatedEntities {
		primaryEntities, relationshipErr = expandEntitiesWithRelationships(ctx, h.storage.Pool(), primaryEntities, workspaceScope)
	}
	entityTables, tableErr := loadEntityTableMap(ctx, h.storage.Pool(), workspaceScope)
	includeInternal := startMode == "queue" || startMode == "job" || startMode == "scheduled" || strictMode
	hops, buildStats := buildFlowHopsWithStats(ctx, h.storage.Pool(), rootCallerIDs, rootEndpoints, maxDepth, maxHops, req.IncludeRelatedEntities, includeInternal, primaryEntities, baseEntities, entityTables, snapshotIDs, workspaceIncludesLegacy(workspaceScope))
	hops = h.filterFlowHopsByWorkspace(ctx, hops, workspaceScope)
	hops = normalizeFlowHops(hops)
	availableHops := len(hops)
	internalHopsPruned := 0
	if includeInternal && !strictMode && startMode != "queue" && startMode != "job" && startMode != "scheduled" {
		beforePrune := len(hops)
		hops = pruneInternalHops(rootEndpoints, hops)
		internalHopsPruned = beforePrune - len(hops)
	}
	strictModeReducedHops := 0
	if strictMode {
		beforeStrict := len(hops)
		hops = applyStrictFlowMode(rootEndpoints, hops, maxDepth)
		strictModeReducedHops = beforeStrict - len(hops)
	}
	callers := findHttpCallersForEndpoints(ctx, h.storage.Pool(), rootEndpoints, workspaceScope)
	callers = h.filterFlowEndpointsByWorkspace(ctx, callers, workspaceScope)
	narratives := buildFlowNarratives(ctx, h.storage.Pool(), rootEndpoints, hops, primaryEntities, callers, req.IncludeRelatedEntities, entityTables)
	rootEndpoints = dedupeFlowEndpoints(rootEndpoints)
	callers = dedupeFlowEndpoints(callers)
	narratives = dedupeFlowNarratives(narratives)
	completeness := buildFlowCompleteness(maxHops, len(hops), availableHops, buildStats)
	assumptions := buildFlowAssumptions(req.IncludeRelatedEntities, strictMode, includeInternal, buildStats, strictModeReducedHops, internalHopsPruned)

	if ctx.Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Flow time limit exceeded", nil)
		return
	}
	resp := FlowResponse{
		Workspace:   workspaceScope.Workspace,
		RepoContext: workspaceScope.RepoContext,
		Roots:       rootEndpoints,
		Hops:        hops,
		Narratives:  narratives,
		Callers:     callers,
		Candidates:  buildStats.Candidates,
		Stats: FlowStats{
			Hops:     len(hops),
			Roots:    len(rootEndpoints),
			Duration: time.Since(startTime).String(),
		},
		Completeness: completeness,
		Assumptions:  assumptions,
		Warnings:     buildFlowWarnings(completeness, assumptions),
	}

	if entityErr != nil {
		resp.Warnings = append(resp.Warnings, "repository/entity enrichment incomplete; data relationships may be missing")
	}
	resp.Warnings = append(resp.Warnings, uniqueStrings(buildStats.DiscoveryWarnings)...)
	if relationshipErr != nil {
		resp.Warnings = append(resp.Warnings, "related-entity lookup incomplete; using the original entity scope")
	}
	if tableErr != nil {
		resp.Warnings = append(resp.Warnings, "entity table lookup incomplete; table labels are unavailable")
	}
	writeJSON(w, http.StatusOK, resp)
}

func flowStartPathVariants(rawPath string) []string {
	variants := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	add := func(path string) {
		p := strings.TrimSpace(path)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		variants = append(variants, p)
	}

	add(rawPath)
	if forwarded := extractForwardedPathFromQuery(rawPath); forwarded != "" {
		add(forwarded)
	}
	return variants
}

func extractForwardedPathFromQuery(rawPath string) string {
	value := strings.TrimSpace(rawPath)
	if value == "" {
		return ""
	}
	idx := strings.Index(value, "?")
	if idx < 0 || idx >= len(value)-1 {
		return ""
	}
	values, err := url.ParseQuery(value[idx+1:])
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(values.Get("path"))
	if path == "" {
		return ""
	}
	return normalizeFlowStartPath(path)
}

func parseFlowStart(start string) (method string, path string, ok bool) {
	trimmed := strings.TrimSpace(start)
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Fields(trimmed)
	if len(parts) >= 2 {
		methodCandidate := strings.ToUpper(parts[0])
		switch methodCandidate {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "REQUEST":
			return methodCandidate, parts[1], true
		}
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", trimmed, true
	}
	return "", "", false
}

func findEndpointRoots(ctx context.Context, pool *pgxpool.Pool, method, rawPath string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	pathCandidates := buildEndpointPathCandidates(rawPath)
	if len(pathCandidates) == 0 {
		return nil, nil
	}
	candidateRank := make(map[string]int, len(pathCandidates))
	for idx, candidate := range pathCandidates {
		candidateRank[strings.ToLower(candidate)] = idx
	}
	normalizedInputPath := strings.TrimSpace(rawPath)
	if normalizedInputPath != "" && !strings.HasPrefix(normalizedInputPath, "/") {
		normalizedInputPath = "/" + normalizedInputPath
	}
	if len(normalizedInputPath) > 1 {
		normalizedInputPath = strings.TrimSuffix(normalizedInputPath, "/")
	}

	endpointSeen := make(map[string]bool, 64)
	candidates := make([]endpointRootCandidate, 0, 32)

	for _, candidate := range pathCandidates {
		rows, err := pool.Query(ctx, `
			SELECT COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) as method,
			       COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) as path,
			       COALESCE(fn.name, '') as handler,
			       f.path,
			       COALESCE(e.line_number, 0),
			       r.name,
			       (
			         SELECT COUNT(*)
			         FROM http_client_calls h
			         JOIN repositories hr ON hr.id = h.repo_id
			         WHERE hr.name = r.name
			           AND lower(h.url_pattern) = COALESCE(NULLIF(e.path_canonical, ''), lower(e.path))
			           AND ($2 = '' OR upper(h.http_method) = $2)
			           AND `+integrationSnapshotClause("h.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
			       ) AS caller_count
			FROM endpoints e
			JOIN files f ON e.file_id = f.id
			JOIN repositories r ON f.repo_id = r.id
			LEFT JOIN functions fn ON e.handler_function_id = fn.id
			WHERE ($2 = '' OR COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) = $2 OR COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) = 'REQUEST')
			  AND `+integrationSnapshotClause("f.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
			  AND (
			    COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) = lower($1) OR
			    $1 ~* ('^' || regexp_replace(
			               regexp_replace(COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)), '\\\\{[^/]+\\\\}', '[^/]+', 'g'),
			               ':[^/]+', '[^/]+', 'g'
			             ) || '$')
			  )
			ORDER BY r.name, method, path, COALESCE(e.line_number, 0), handler
			LIMIT 50
		`, candidate, method, activeSnapshotIDs(scope))
		if err != nil {
			continue
		}

		for rows.Next() {
			var endpointMethod, path, handler, file, repo string
			var line int
			var callerCount int64
			if err := rows.Scan(&endpointMethod, &path, &handler, &file, &line, &repo, &callerCount); err != nil {
				continue
			}

			ep := FlowEndpoint{
				Repo:    repo,
				Handler: handler,
				File:    file,
				Line:    line,
				Method:  endpointMethod,
				Path:    path,
			}

			epKey := flowEndpointCanonicalIdentityKey(ep)
			if endpointSeen[epKey] {
				continue
			}
			endpointSeen[epKey] = true

			if handler == "" {
				continue
			}
			callerID := trace.BuildCallerID(repo, file, handler)
			if callerID == "" {
				continue
			}
			rank := candidateRank[strings.ToLower(candidate)]
			if rank == 0 && len(pathCandidates) > 0 && strings.ToLower(pathCandidates[0]) != strings.ToLower(candidate) {
				rank = len(pathCandidates) + 1
			}
			if normalizedInputPath != "" && strings.EqualFold(path, normalizedInputPath) {
				rank = -1
			}
			candidates = append(candidates, endpointRootCandidate{
				endpoint:      ep,
				callerID:      callerID,
				candidateRank: rank,
				callerCount:   callerCount,
			})
		}
		rows.Close()
	}

	return selectEndpointRoots(candidates)
}

func selectEndpointRoots(candidates []endpointRootCandidate) ([]FlowEndpoint, []string) {
	if len(candidates) == 0 {
		return nil, nil
	}
	bestByEndpoint := make(map[string]endpointRootCandidate, len(candidates))
	for _, candidate := range candidates {
		// Identical routes in different repositories are distinct execution roots.
		key := candidate.callerID + "|" + canonicalFlowEndpointMethod(candidate.endpoint.Method) + "|" + canonicalFlowEndpointPath(candidate.endpoint.Path)
		if current, ok := bestByEndpoint[key]; !ok || betterEndpointCandidate(candidate, current) {
			bestByEndpoint[key] = candidate
		}
	}
	keys := make([]string, 0, len(bestByEndpoint))
	for key := range bestByEndpoint {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	roots := make([]FlowEndpoint, 0, len(keys))
	callerIDs := make([]string, 0, len(keys))
	seenCaller := make(map[string]bool, len(keys))
	for _, key := range keys {
		candidate := bestByEndpoint[key]
		if candidate.callerID == "" || seenCaller[candidate.callerID] {
			continue
		}
		seenCaller[candidate.callerID] = true
		roots = append(roots, candidate.endpoint)
		callerIDs = append(callerIDs, candidate.callerID)
	}
	return roots, callerIDs
}

func findEndpointRootsWithTraceMatch(pool trace.Queryer, method, rawPath string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil, nil
	}
	methods := make([]string, 0, 6)
	if strings.TrimSpace(method) != "" {
		methods = append(methods, strings.ToUpper(strings.TrimSpace(method)))
	} else {
		methods = append(methods, "POST", "PUT", "PATCH", "DELETE", "GET", "REQUEST")
	}

	seen := make(map[string]bool, 64)
	candidates := make([]endpointRootCandidate, 0, 32)
	for rank, httpMethod := range methods {
		matches := trace.MatchEndpointsForSnapshotFilter(pool, httpMethod, path, "", activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
		for _, match := range matches {
			if strings.TrimSpace(match.Handler) == "" {
				continue
			}
			ep := FlowEndpoint{
				Repo:    match.Repo,
				Handler: match.Handler,
				File:    match.File,
				Line:    match.LineNumber,
				Method:  match.Method,
				Path:    match.Path,
			}
			callerID := trace.BuildCallerID(ep.Repo, ep.File, ep.Handler)
			if callerID == "" {
				continue
			}
			key := flowEndpointCanonicalIdentityKey(ep) + "|" + callerID
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, endpointRootCandidate{
				endpoint:      ep,
				callerID:      callerID,
				candidateRank: rank,
				callerCount:   0,
			})
		}
	}
	return selectEndpointRoots(candidates)
}

func betterEndpointCandidate(a, b endpointRootCandidate) bool {
	if a.callerCount != b.callerCount {
		return a.callerCount > b.callerCount
	}
	if a.candidateRank != b.candidateRank {
		return a.candidateRank < b.candidateRank
	}
	if !strings.EqualFold(a.endpoint.Repo, b.endpoint.Repo) {
		return strings.ToLower(a.endpoint.Repo) < strings.ToLower(b.endpoint.Repo)
	}
	if !strings.EqualFold(a.endpoint.File, b.endpoint.File) {
		return strings.ToLower(a.endpoint.File) < strings.ToLower(b.endpoint.File)
	}
	if !strings.EqualFold(a.endpoint.Handler, b.endpoint.Handler) {
		return strings.ToLower(a.endpoint.Handler) < strings.ToLower(b.endpoint.Handler)
	}
	return a.endpoint.Line < b.endpoint.Line
}

func buildEndpointPathCandidates(rawPath string) []string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return nil
	}

	forwardedPath := ""
	if idx := strings.Index(trimmed, "?"); idx >= 0 {
		query := strings.TrimSpace(trimmed[idx+1:])
		trimmed = strings.TrimSpace(trimmed[:idx])
		if values, err := url.ParseQuery(query); err == nil {
			forwardedPath = strings.TrimSpace(values.Get("path"))
		}
	}

	normalized := normalizeFlowStartPath(trimmed)
	if normalized == "" {
		return nil
	}

	if len(normalized) > 1 {
		normalized = strings.TrimSuffix(normalized, "/")
	}

	seen := make(map[string]bool, 8)
	out := make([]string, 0, 6)
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	add(normalized)

	if forwardedPath != "" {
		for _, candidate := range buildForwardedPathCandidates(forwardedPath) {
			add(candidate)
		}
	}

	return out
}

func normalizeFlowStartPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func buildForwardedPathCandidates(raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	normalized := normalizeFlowStartPath(value)
	if normalized == "" || normalized == "/" {
		return nil
	}

	return []string{normalized}
}

func parseFlowStartMode(start string) (mode string, value string) {
	trimmed := strings.TrimSpace(start)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "queue:"):
		return "queue", strings.TrimSpace(trimmed[len("queue:"):])
	case strings.HasPrefix(lower, "kafka:"):
		return "queue", strings.TrimSpace(trimmed[len("kafka:"):])
	case strings.HasPrefix(lower, "topic:"):
		return "queue", strings.TrimSpace(trimmed[len("topic:"):])
	case strings.HasPrefix(lower, "job:"):
		return "job", strings.TrimSpace(trimmed[len("job:"):])
	case strings.HasPrefix(lower, "scheduled:"):
		return "scheduled", strings.TrimSpace(trimmed[len("scheduled:"):])
	case strings.HasPrefix(lower, "eventbridge:"):
		return "eventbridge", strings.TrimSpace(trimmed[len("eventbridge:"):])
	case strings.HasPrefix(lower, "graphql:"):
		return "graphql", strings.TrimSpace(trimmed[len("graphql:"):])
	case strings.HasPrefix(lower, "azure:"):
		return "azure", strings.TrimSpace(trimmed[len("azure:"):])
	default:
		return "", trimmed
	}
}

type flowFrontier struct {
	callerID string
	endpoint FlowEndpoint
}

func buildFlowHopsWithStats(ctx context.Context, pool *pgxpool.Pool, rootCallerIDs []string, rootEndpoints []FlowEndpoint, maxDepth, maxHops int, includeRelated bool, includeInternal bool, primaryEntities map[string]bool, baseEntities map[string]bool, entityTables map[string]string, snapshotIDs []int64, includeLegacySnapshots bool) ([]FlowHop, flowBuildStats) {
	maxDepth = min(maxDepth, trace.MaxDepth)
	maxHops = min(maxHops, 10000)
	stats := flowBuildStats{}
	if maxHops <= 0 {
		return nil, stats
	}
	callHopSoftLimit := internalCallHopSoftLimit(maxHops)
	stats.InternalCallHopSoftLimit = callHopSoftLimit

	maxNodes := maxHops * 40
	if maxNodes < 800 {
		maxNodes = 800
	}

	hops := make([]FlowHop, 0, maxHops)
	seenData := make(map[string]bool)
	seenCallers := make(map[string]bool)
	seenInternal := make(map[string]bool)
	internalSeeds := make(map[string]FlowEndpoint)
	frontier := make([]flowFrontier, 0, len(rootCallerIDs))
	var defLines map[string]int
	for i, callerID := range rootCallerIDs {
		if callerID == "" {
			continue
		}
		endpoint := FlowEndpoint{}
		if i < len(rootEndpoints) {
			endpoint = rootEndpoints[i]
		}
		frontier = append(frontier, flowFrontier{callerID: callerID, endpoint: endpoint})
	}

	var walk func(node *trace.TreeNode, from FlowEndpoint, hopDepth int, scoped map[string]bool, allowInternal bool)
	walk = func(node *trace.TreeNode, from FlowEndpoint, hopDepth int, scoped map[string]bool, allowInternal bool) {
		if node == nil || len(hops) >= maxHops {
			if len(hops) >= maxHops {
				stats.MaxHopsReached = true
			}
			return
		}

		current := from
		if node.Repo != "" || node.CallerID != "" || node.Name != "" {
			current = flowEndpointFromNode(node, defLines)
		}

		for _, child := range node.Children {
			if len(hops) >= maxHops {
				stats.MaxHopsReached = true
				return
			}

			if !includeRelated && len(primaryEntities) > 0 && !includeInternal {
				if node.CallerID == "" || !scoped[node.CallerID] {
					if isHttpNode(child) || isSqsNode(child) {
						continue
					}
				}
			}

			if isHttpNode(child) {
				nextDepth := hopDepth + 1
				if nextDepth > maxDepth {
					continue
				}
				if len(child.Children) == 0 {
					hops = append(hops, FlowHop{
						Depth: nextDepth - 1,
						From:  current,
						Via: FlowVia{
							Type:       "http",
							Method:     child.HttpMethod,
							Path:       child.HttpTarget,
							ClientType: "",
							External:   true,
						},
						To: FlowEndpoint{
							Repo:    "(external)",
							Handler: "",
						},
					})
					continue
				}
				for _, target := range child.Children {
					if len(hops) >= maxHops {
						stats.MaxHopsReached = true
						return
					}
					toEndpoint := flowEndpointFromNode(target, defLines)
					hops = append(hops, FlowHop{
						Depth: nextDepth - 1,
						From:  current,
						Via: FlowVia{
							Type:   "http",
							Method: child.HttpMethod,
							Path:   child.HttpTarget,
						},
						To: toEndpoint,
					})
					walk(target, toEndpoint, nextDepth, scoped, includeInternal)
				}
				continue
			}

			if isSqsNode(child) {
				nextDepth := hopDepth + 1
				if nextDepth > maxDepth {
					continue
				}
				if len(child.Children) == 0 {
					hops = append(hops, FlowHop{
						Depth: nextDepth - 1,
						From:  current,
						Via: FlowVia{
							Type:  "sqs",
							Queue: child.QueueTarget,
						},
						To: FlowEndpoint{
							Repo:    "(no consumer)",
							Handler: "",
						},
					})
					continue
				}
				for _, target := range child.Children {
					if len(hops) >= maxHops {
						stats.MaxHopsReached = true
						return
					}
					toEndpoint := flowEndpointFromNode(target, defLines)
					hops = append(hops, FlowHop{
						Depth: nextDepth - 1,
						From:  current,
						Via: FlowVia{
							Type:  "sqs",
							Queue: child.QueueTarget,
						},
						To: toEndpoint,
					})
					if includeInternal {
						if toEndpoint.Handler != "" || toEndpoint.Path != "" || toEndpoint.Repo != "" {
							key := flowEndpointKeyNoLine(toEndpoint)
							internalSeeds[key] = toEndpoint
						}
					}
					walk(target, toEndpoint, nextDepth, scoped, includeInternal)
				}
				continue
			}

			if includeInternal && allowInternal {
				toEndpoint := flowEndpointFromNode(child, defLines)
				if shouldSkipLowSignalInternalCall(current, toEndpoint) {
					walk(child, toEndpoint, hopDepth, scoped, true)
					continue
				}
				if toEndpoint.Handler != "" || toEndpoint.Path != "" || toEndpoint.Repo != "" {
					key := current.Repo + "|" + current.Handler + "|" + current.Path + "->" + toEndpoint.Repo + "|" + toEndpoint.Handler + "|" + toEndpoint.Path
					if !seenInternal[key] {
						if len(hops) < callHopSoftLimit {
							seenInternal[key] = true
							hops = append(hops, FlowHop{
								Depth: hopDepth,
								From:  current,
								Via: FlowVia{
									Type: "call",
								},
								To: toEndpoint,
							})
						} else {
							stats.InternalCallsSoftLimited = true
						}
					}
				}
				walk(child, toEndpoint, hopDepth, scoped, true)
				continue
			}

			walk(child, current, hopDepth, scoped, allowInternal)
		}
	}

	for iter := 0; iter < 3 && len(frontier) > 0 && len(hops) < maxHops; iter++ {
		iterationCallerIDs := make([]string, 0, len(frontier))
		rootByCaller := make(map[string]FlowEndpoint, len(frontier))
		for _, item := range frontier {
			if item.callerID == "" || seenCallers[item.callerID] {
				continue
			}
			seenCallers[item.callerID] = true
			iterationCallerIDs = append(iterationCallerIDs, item.callerID)
			if item.endpoint.Repo != "" || item.endpoint.Handler != "" || item.endpoint.Path != "" {
				rootByCaller[item.callerID] = item.endpoint
			}
		}
		if len(iterationCallerIDs) == 0 {
			break
		}

		tuning := trace.DefaultTraceTuning(maxDepth)
		tuning.SnapshotIDs = snapshotIDs
		tuning.IncludeLegacySnapshots = includeLegacySnapshots
		trees := trace.TraceDownstreamWithLimitAndResolveTuning(trace.WithContext(ctx, pool), iterationCallerIDs, maxDepth, maxNodes, true, tuning)
		callerIDs := collectCallerIDs(trees)
		defLines = fetchCallerDefLines(ctx, pool, callerIDs, snapshotIDs, includeLegacySnapshots)
		scopedCallers := map[string]bool{}
		if !includeRelated && len(primaryEntities) > 0 {
			accesses := fetchDataAccesses(ctx, pool, callerIDs, true, nil, snapshotIDs)
			accessByCaller := buildAccessIndex(accesses)
			scopedCallers = scopeDescendantsForEntities(trees, accessByCaller, primaryEntities)
			for _, rootID := range iterationCallerIDs {
				if rootID != "" {
					scopedCallers[rootID] = true
				}
			}
		}

		for _, root := range trees {
			if root == nil {
				continue
			}
			start := flowEndpointFromNode(root, defLines)
			if root.CallerID != "" {
				if override, ok := rootByCaller[root.CallerID]; ok {
					start = override
				}
			}
			walk(root, start, 0, scopedCallers, includeInternal)
			if len(hops) >= maxHops {
				stats.MaxHopsReached = true
				break
			}
		}

		if len(hops) >= maxHops {
			stats.MaxHopsReached = true
			break
		}

		dataHops, dataTargets := buildDataHops(ctx, pool, trees, maxHops-len(hops), seenData, includeRelated, primaryEntities, baseEntities, entityTables, snapshotIDs, includeLegacySnapshots, &stats)
		hops = append(hops, dataHops...)

		if len(hops) >= maxHops {
			stats.MaxHopsReached = true
			break
		}

		if len(dataTargets) == 0 {
			if !includeInternal || len(internalSeeds) == 0 {
				break
			}
			seedIDs := resolveCallerIDsForEndpoints(trace.WithContext(ctx, pool), mapToSlice(internalSeeds), snapshotIDs, includeLegacySnapshots)
			if len(seedIDs) == 0 {
				break
			}
			frontier = seedIDs
			internalSeeds = make(map[string]FlowEndpoint)
			continue
		}

		frontier = resolveCallerIDsForEndpoints(trace.WithContext(ctx, pool), dataTargets, snapshotIDs, includeLegacySnapshots)
	}

	if len(hops) >= maxHops {
		stats.MaxHopsReached = true
	}
	return hops, stats
}

func internalCallHopSoftLimit(maxHops int) int {
	if maxHops <= 0 {
		return 0
	}
	// Keep budget for cross-service/data hops so internal call noise
	// does not exhaust the whole response before handoff edges are added.
	reserve := maxHops / 4
	if reserve < 16 {
		reserve = 16
	}
	if reserve > 120 {
		reserve = 120
	}
	if reserve >= maxHops {
		reserve = maxHops / 2
	}
	limit := maxHops - reserve
	if limit <= 0 {
		limit = maxHops
	}
	return limit
}

func buildFlowCompleteness(maxHops, returnedHops, availableHops int, stats flowBuildStats) FlowCompleteness {
	if availableHops < returnedHops {
		availableHops = returnedHops
	}
	truncationReasons := make([]string, 0, 2)
	truncated := false
	exactAvailable := true
	if stats.MaxHopsReached {
		truncated = true
		exactAvailable = false
		truncationReasons = append(truncationReasons, "flow clipped by maxHops before the exact hop count could be computed")
	}
	if stats.InternalCallsSoftLimited {
		truncated = true
		exactAvailable = false
		truncationReasons = append(truncationReasons, "internal call hop budget was exhausted before all internal call hops could be included")
	}
	return FlowCompleteness{
		AppliedMaxHops:     maxHops,
		ReturnedHops:       returnedHops,
		AvailableHops:      availableHops,
		ExactAvailableHops: exactAvailable,
		Truncated:          truncated,
		TruncationReasons:  truncationReasons,
	}
}

func buildFlowAssumptions(includeRelatedEntities, strictMode, includeInternalCalls bool, stats flowBuildStats, strictModeReducedHops, internalHopsPruned int) FlowAssumptions {
	return FlowAssumptions{
		StrictMode:               strictMode,
		IncludeRelatedEntities:   includeRelatedEntities,
		IncludeInternalCalls:     includeInternalCalls,
		InternalCallHopSoftLimit: stats.InternalCallHopSoftLimit,
		StrictModeReducedHops:    strictModeReducedHops,
		InternalHopsPruned:       internalHopsPruned,
		InternalCallsSoftLimited: stats.InternalCallsSoftLimited,
	}
}

func buildFlowWarnings(completeness FlowCompleteness, assumptions FlowAssumptions) []string {
	_ = assumptions
	warnings := make([]string, 0, 1)
	if completeness.Truncated {
		if completeness.ExactAvailableHops {
			warnings = append(warnings, fmt.Sprintf("Flow is clipped. Showing %d of %d available hops. Increase maxHops to inspect the full graph.", completeness.ReturnedHops, completeness.AvailableHops))
		} else {
			warnings = append(warnings, fmt.Sprintf("Flow is clipped. Showing %d hops; more are available, but the exact total is unknown at this maxHops setting.", completeness.ReturnedHops))
		}
	}
	return warnings
}

func mapToSlice(input map[string]FlowEndpoint) []FlowEndpoint {
	if len(input) == 0 {
		return nil
	}
	out := make([]FlowEndpoint, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func flowNormalizeQueueName(queueName string) string {
	name := strings.TrimSpace(queueName)
	if name == "" {
		return ""
	}
	name = strings.TrimSpace(strings.Trim(name, "%"))
	if strings.HasPrefix(name, "arn:") {
		if idx := strings.LastIndex(name, ":"); idx != -1 && idx+1 < len(name) {
			name = name[idx+1:]
		}
	}
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		if parsed, err := url.Parse(name); err == nil && parsed.Path != "" {
			parts := strings.Split(parsed.Path, "/")
			name = parts[len(parts)-1]
		}
	}
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		name = parts[len(parts)-1]
	}
	return strings.TrimSpace(name)
}

func flowQueueNameVariants(queueName string) []string {
	seen := make(map[string]bool)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		seen[strings.ToLower(value)] = true
	}
	add(queueName)
	base := flowNormalizeQueueName(queueName)
	add(base)
	out := make([]string, 0, len(seen))
	for variant := range seen {
		out = append(out, variant)
	}
	sort.Strings(out)
	return out
}

func fetchSqsConsumers(ctx context.Context, pool *pgxpool.Pool, queue string, scope searchWorkspaceScope) []FlowEndpoint {
	if queue == "" {
		return nil
	}
	variants := flowQueueNameVariants(queue)
	if len(variants) == 0 {
		return nil
	}
	query := `
		WITH input_variants AS (
			SELECT unnest($1::text[]) AS queue_name
		),
		direct_aliases AS (
			SELECT
				LOWER(TRIM(BOTH '%' FROM ra.alias_key)) AS alias_key,
				LOWER(TRIM(BOTH '%' FROM ra.alias_value)) AS alias_value
			FROM resource_aliases ra
			JOIN input_variants iv
			  ON LOWER(TRIM(BOTH '%' FROM ra.alias_value)) = iv.queue_name
			  OR LOWER(TRIM(BOTH '%' FROM ra.alias_key)) = iv.queue_name
			WHERE ` + integrationSnapshotClause("ra.snapshot_id", 2, workspaceIncludesLegacy(scope)) + `
		),
		expanded_variants AS (
			SELECT queue_name FROM input_variants

			UNION

			SELECT alias_key FROM direct_aliases

			UNION

			SELECT alias_value FROM direct_aliases

			UNION

			SELECT LOWER(TRIM(BOTH '%' FROM ra.alias_key))
			FROM resource_aliases ra
			JOIN direct_aliases da
			  ON LOWER(TRIM(BOTH '%' FROM ra.alias_value)) = da.alias_value
			WHERE ` + integrationSnapshotClause("ra.snapshot_id", 2, workspaceIncludesLegacy(scope)) + `
		)
		SELECT consumer_id, handler_method FROM (
			SELECT consumer_id, handler_method
			FROM sqs_consumers sc
			WHERE ` + integrationSnapshotClause("sc.snapshot_id", 2, workspaceIncludesLegacy(scope)) + `
			  AND (LOWER(queue_name) IN (SELECT queue_name FROM expanded_variants)
			   OR LOWER(TRIM(BOTH '%' FROM queue_name)) IN (SELECT queue_name FROM expanded_variants))

			UNION ALL

			SELECT
				r.name || ':' || f.path || ':' || t.function_name AS consumer_id,
				'' AS handler_method
			FROM azure_function_triggers t
			JOIN files f ON f.id = t.file_id
			JOIN repositories r ON r.id = t.repo_id
			WHERE t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			  AND ` + integrationSnapshotClause("t.snapshot_id", 2, workspaceIncludesLegacy(scope)) + `
			  AND (
			    LOWER(COALESCE(t.resource_name, '')) IN (SELECT queue_name FROM expanded_variants)
			    OR LOWER(TRIM(BOTH '%' FROM COALESCE(t.resource_name, ''))) IN (SELECT queue_name FROM expanded_variants)
			  )

			UNION ALL

			SELECT
				da.caller_id AS consumer_id,
				'' AS handler_method
			FROM data_accesses da
			WHERE LOWER(da.access) = 'read'
			  AND da.entity_name LIKE 'queue:%'
			  AND ` + integrationSnapshotClause("da.snapshot_id", 2, workspaceIncludesLegacy(scope)) + `
			  AND LOWER(TRIM(BOTH '%' FROM SUBSTRING(da.entity_name FROM 7))) IN (SELECT queue_name FROM expanded_variants)
		) consumers
		ORDER BY consumer_id, handler_method
		LIMIT 100`
	rows, err := pool.Query(ctx, query, variants, activeSnapshotIDs(scope))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var consumers []FlowEndpoint
	for rows.Next() {
		var consumerID, handlerMethod string
		if err := rows.Scan(&consumerID, &handlerMethod); err != nil {
			continue
		}
		repo, file, className := trace.ParseCallerID(consumerID)
		handler := className
		if handlerMethod != "" {
			handler = className + "." + handlerMethod
		}
		consumers = append(consumers, FlowEndpoint{
			Repo:    repo,
			File:    file,
			Handler: handler,
		})
	}
	return consumers
}

func resolveRootsFromEndpoints(pool trace.Queryer, endpoints []FlowEndpoint, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	frontier := resolveCallerIDsForEndpoints(pool, endpoints, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if len(frontier) == 0 {
		return nil, nil
	}
	roots := make([]FlowEndpoint, 0, len(frontier))
	callerIDs := make([]string, 0, len(frontier))
	seen := make(map[string]bool, len(frontier))
	for _, item := range frontier {
		if item.callerID == "" || seen[item.callerID] {
			continue
		}
		seen[item.callerID] = true
		callerIDs = append(callerIDs, item.callerID)
		roots = append(roots, item.endpoint)
	}
	return roots, callerIDs
}

func findQueueRoots(ctx context.Context, pool *pgxpool.Pool, queueName string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	if queueName == "" {
		return nil, nil
	}
	// Queue starts are consumer roots. Producer-side exploration must be an
	// explicit mode; otherwise a missing consumer parse can look like proof of
	// the wrong execution side.
	consumers := fetchSqsConsumers(ctx, pool, queueName, scope)
	return resolveRootsFromEndpoints(trace.WithContext(ctx, pool), consumers, scope)
}

func findConsumerRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	if query == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT consumer_id, handler_method
		FROM sqs_consumers
		WHERE (consumer_id ILIKE '%' || $1 || '%'
		   OR handler_method ILIKE '%' || $1 || '%')
		  AND `+integrationSnapshotClause("snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		ORDER BY consumer_id
		LIMIT 50
	`, graph.EscapeLike(query), activeSnapshotIDs(scope))
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var endpoints []FlowEndpoint
	for rows.Next() {
		var consumerID, handlerMethod string
		if err := rows.Scan(&consumerID, &handlerMethod); err != nil {
			continue
		}
		repo, file, className := trace.ParseCallerID(consumerID)
		handler := className
		if handlerMethod != "" {
			handler = className + "." + handlerMethod
		}
		endpoints = append(endpoints, FlowEndpoint{
			Repo:    repo,
			File:    file,
			Handler: handler,
		})
	}
	return resolveRootsFromEndpoints(trace.WithContext(ctx, pool), endpoints, scope)
}

func findScheduledRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	if query == "" {
		return nil, nil
	}
	query = strings.TrimSpace(strings.TrimSuffix(query, "()"))
	classPart := ""
	methodPart := ""
	if idx := strings.LastIndex(query, "."); idx > 0 && idx < len(query)-1 {
		classPart = strings.TrimSpace(query[:idx])
		methodPart = strings.TrimSpace(query[idx+1:])
	}
	rows, err := pool.Query(ctx, `
		SELECT r.name, f.path, c.name, sm.method_name, COALESCE(fn.name, ''), COALESCE(sm.cron, ''), sm.fixed_rate, sm.fixed_delay
		FROM scheduled_methods sm
		JOIN classes c ON sm.class_id = c.id
		JOIN files f ON c.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN functions fn ON sm.method_id = fn.id
		WHERE (sm.method_name ILIKE '%' || $1 || '%'
		   OR c.name ILIKE '%' || $1 || '%'
		   OR fn.name ILIKE '%' || $1 || '%'
		   OR (c.name || '.' || sm.method_name) ILIKE '%' || $1 || '%'
		   OR ($2 <> '' AND $3 <> '' AND c.name ILIKE '%' || $2 || '%' AND sm.method_name ILIKE '%' || $3 || '%'))
		  AND `+integrationSnapshotClause("f.snapshot_id", 4, workspaceIncludesLegacy(scope))+`
		ORDER BY r.name, c.name, sm.method_name
		LIMIT 50
	`, graph.EscapeLike(query), graph.EscapeLike(classPart), graph.EscapeLike(methodPart), activeSnapshotIDs(scope))

	var endpoints []FlowEndpoint
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var repo, file, className, methodName, fnName, cron string
			var fixedRate, fixedDelay int64
			if err := rows.Scan(&repo, &file, &className, &methodName, &fnName, &cron, &fixedRate, &fixedDelay); err != nil {
				continue
			}
			handler := fnName
			if handler == "" {
				handler = className + "." + methodName
			}
			path := cron
			if path == "" && fixedRate > 0 {
				path = "fixedRate"
			} else if path == "" && fixedDelay > 0 {
				path = "fixedDelay"
			}
			endpoints = append(endpoints, FlowEndpoint{
				Repo:    repo,
				File:    file,
				Handler: handler,
				Method:  "SCHEDULED",
				Path:    path,
			})
		}
	}
	roots, callerIDs := resolveRootsFromEndpoints(trace.WithContext(ctx, pool), endpoints, scope)
	azureRoots, azureCallerIDs := findAzureTimerRoots(ctx, pool, query, scope)
	return appendUniqueFlowRoots(roots, callerIDs, azureRoots, azureCallerIDs)
}

func appendUniqueFlowRoots(left []FlowEndpoint, leftIDs []string, right []FlowEndpoint, rightIDs []string) ([]FlowEndpoint, []string) {
	seen := make(map[string]bool, len(leftIDs)+len(rightIDs))
	for i, id := range leftIDs {
		key := id
		if i < len(left) {
			key += "|" + flowEndpointCanonicalIdentityKey(left[i])
		}
		seen[key] = true
	}
	for i, id := range rightIDs {
		key := id
		if i < len(right) {
			key += "|" + flowEndpointCanonicalIdentityKey(right[i])
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		leftIDs = append(leftIDs, id)
		if i < len(right) {
			left = append(left, right[i])
		}
	}
	return left, leftIDs
}

func findAzureTimerRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	pattern := "%" + graph.EscapeLike(strings.TrimSpace(query)) + "%"
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name,
		       fi.path,
		       COALESCE(t.script_file, ''),
		       t.function_name,
		       t.trigger_type,
		       COALESCE(fn.name, t.function_name) AS handler,
		       COALESCE(t.line_number, 0) AS line,
		       'SCHEDULED' AS method,
		       COALESCE(t.schedule_expression, '') AS schedule
		FROM azure_function_triggers t
		JOIN repositories r ON r.id = t.repo_id
		JOIN files fi ON fi.id = t.file_id
		LEFT JOIN endpoints e
		  ON e.repo_id = t.repo_id
		 AND e.file_id = t.file_id
		 AND e.line_number = t.line_number
		LEFT JOIN functions fn ON fn.id = e.handler_function_id
		WHERE `+integrationSnapshotClause("fi.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		  AND LOWER(t.trigger_type) = 'timertrigger'
		  AND (
			t.function_name ILIKE $1
			OR COALESCE(t.schedule_expression, '') ILIKE $1
			OR COALESCE(t.resource_name, '') ILIKE $1
		  )
		ORDER BY r.name, fi.path, handler, line
		LIMIT 100
	`, pattern, activeSnapshotIDs(scope))
	if err != nil {
		return nil, nil
	}
	return resolveAzureRootRows(ctx, pool, rows, scope)
}

func findEventBridgeRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	if query == "" {
		return nil, nil
	}
	// Find EventBridge schedules matching the query (by rule name)
	rows, err := pool.Query(ctx, `
		SELECT rule_name, schedule_expression, target_type, target_name, state, source
		FROM eventbridge_schedules
		WHERE rule_name ILIKE '%' || $1 || '%'
		   OR target_name ILIKE '%' || $1 || '%'
		ORDER BY rule_name
		LIMIT 50
	`, graph.EscapeLike(query))
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	// Collect SQS queue targets from matching schedules
	var queueNames []string
	for rows.Next() {
		var ruleName, schedExpr, targetType, targetName, state, source string
		if err := rows.Scan(&ruleName, &schedExpr, &targetType, &targetName, &state, &source); err != nil {
			continue
		}
		if targetType == "sqs" {
			queueNames = append(queueNames, targetName)
		}
	}

	// Find SQS consumers for these queues, then resolve to flow roots
	var endpoints []FlowEndpoint
	for _, queue := range queueNames {
		consumers := fetchSqsConsumers(ctx, pool, queue, scope)
		endpoints = append(endpoints, consumers...)
	}
	return resolveRootsFromEndpoints(trace.WithContext(ctx, pool), endpoints, scope)
}

func findGraphQLOperationRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name, fi.path, u.caller_function, COALESCE(u.line_number, 0)
		FROM graphql_operation_usages u
		JOIN repositories r ON r.id = u.repo_id
		JOIN files fi ON fi.id = u.file_id
		JOIN graphql_usage_operation_links l ON l.usage_id = u.id
		JOIN graphql_operations o ON o.id = l.operation_id
		JOIN files operation_file ON operation_file.id = o.file_id
		WHERE o.operation_name = $1
		  AND `+integrationSnapshotClause("fi.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		  AND `+integrationSnapshotClause("operation_file.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		ORDER BY r.name, fi.path, u.caller_function, COALESCE(u.line_number, 0)
		LIMIT 100
	`, query, activeSnapshotIDs(scope))
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	roots, callerIDs := collectGraphQLOperationRootRows(rows)
	rows.Close()
	if len(roots) > 0 {
		return roots, callerIDs
	}

	rows, err = pool.Query(ctx, `
		SELECT DISTINCT r.name, fi.path, u.caller_function, COALESCE(u.line_number, 0)
		FROM graphql_operation_usages u
		JOIN repositories r ON r.id = u.repo_id
		JOIN files fi ON fi.id = u.file_id
		WHERE u.imported_as = $1
		  AND `+integrationSnapshotClause("fi.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		ORDER BY r.name, fi.path, u.caller_function, COALESCE(u.line_number, 0)
		LIMIT 100
	`, query, activeSnapshotIDs(scope))
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	return collectGraphQLOperationRootRows(rows)
}

func collectGraphQLOperationRootRows(rows pgx.Rows) ([]FlowEndpoint, []string) {
	var roots []FlowEndpoint
	var callerIDs []string
	seen := make(map[string]bool)
	for rows.Next() {
		var repo, file, handler string
		var line int
		if err := rows.Scan(&repo, &file, &handler, &line); err != nil {
			continue
		}
		callerID := trace.BuildCallerID(repo, file, handler)
		if callerID == "" || seen[callerID] {
			continue
		}
		seen[callerID] = true
		roots = append(roots, FlowEndpoint{
			Repo:    repo,
			Handler: handler,
			File:    file,
			Line:    line,
		})
		callerIDs = append(callerIDs, callerID)
	}
	return roots, callerIDs
}

func findAzureFunctionRoots(ctx context.Context, pool *pgxpool.Pool, query string, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	pattern := "%" + graph.EscapeLike(strings.TrimSpace(query)) + "%"
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name,
		       fi.path,
		       COALESCE(t.script_file, ''),
		       t.function_name,
		       t.trigger_type,
		       COALESCE(fn.name, t.function_name) AS handler,
		       COALESCE(e.line_number, t.line_number, 0) AS line,
		       COALESCE(NULLIF(e.method, ''), '') AS method,
		       COALESCE(NULLIF(e.path, ''), COALESCE(t.route, '')) AS path
		FROM azure_function_triggers t
		JOIN repositories r ON r.id = t.repo_id
		JOIN files fi ON fi.id = t.file_id
		LEFT JOIN endpoints e
		  ON e.repo_id = t.repo_id
		 AND e.file_id = t.file_id
		 AND e.line_number = t.line_number
		LEFT JOIN functions fn ON fn.id = e.handler_function_id
		WHERE `+integrationSnapshotClause("fi.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		  AND (
			t.function_name ILIKE $1
			OR COALESCE(t.route, '') ILIKE $1
			OR COALESCE(t.resource_name, '') ILIKE $1
		)
		ORDER BY r.name, fi.path, handler, line
		LIMIT 100
	`, pattern, activeSnapshotIDs(scope))
	if err != nil {
		return nil, nil
	}
	return resolveAzureRootRows(ctx, pool, rows, scope)
}

func resolveAzureRootRows(ctx context.Context, pool *pgxpool.Pool, rows pgx.Rows, scope searchWorkspaceScope) ([]FlowEndpoint, []string) {
	type candidate struct {
		endpoint                              FlowEndpoint
		scriptFile, functionName, triggerType string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.endpoint.Repo, &c.endpoint.File, &c.scriptFile, &c.functionName, &c.triggerType, &c.endpoint.Handler, &c.endpoint.Line, &c.endpoint.Method, &c.endpoint.Path); err != nil {
			rows.Close()
			return nil, nil
		}
		candidates = append(candidates, c)
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil
	}

	var roots []FlowEndpoint
	var callerIDs []string
	seen := make(map[string]bool)
	for _, c := range candidates {
		e := c.endpoint
		resolvedRoots := resolveAzureFunctionRootCandidates(ctx, pool, e.Repo, e.File, c.scriptFile, c.functionName, c.triggerType, e.Handler, e.Line, e.Method, e.Path, scope)
		for _, root := range resolvedRoots {
			callerID := trace.BuildCallerID(root.Repo, root.File, root.Handler)
			if callerID == "" || seen[callerID] {
				continue
			}
			seen[callerID] = true
			roots = append(roots, root)
			callerIDs = append(callerIDs, callerID)
		}
	}
	return roots, callerIDs
}

func resolveAzureFunctionRootCandidates(ctx context.Context, pool *pgxpool.Pool, repo, functionJSONPath, scriptFile, functionName, triggerType, fallbackHandler string, line int, method, path string, scope searchWorkspaceScope) []FlowEndpoint {
	candidates := azureSourcePathCandidates(functionJSONPath, scriptFile)
	names := azureFunctionHandlerNameCandidates(triggerType, fallbackHandler, functionName)
	if len(names) == 0 {
		return nil
	}

	if pool != nil && len(candidates) > 0 {
		rows, err := pool.Query(ctx, `
			SELECT f.path, fn.name, fn.start_line
			FROM functions fn
			JOIN files f ON fn.file_id = f.id
			JOIN repositories r ON f.repo_id = r.id
			WHERE r.name = $1
			  AND `+integrationSnapshotClause("f.snapshot_id", 4, workspaceIncludesLegacy(scope))+`
			  AND f.path = ANY($2)
			  AND fn.name = ANY($3)
		`, repo, candidates, names, activeSnapshotIDs(scope))
		if err == nil {
			defer rows.Close()
			var resolved []FlowEndpoint
			seen := make(map[string]bool)
			for rows.Next() {
				var sourceFile, handler string
				var startLine int
				if err := rows.Scan(&sourceFile, &handler, &startLine); err != nil {
					continue
				}
				key := strings.ToLower(sourceFile + "|" + handler)
				if seen[key] {
					continue
				}
				seen[key] = true
				resolved = append(resolved, FlowEndpoint{
					Repo:    repo,
					File:    sourceFile,
					Handler: handler,
					Line:    startLine,
					Method:  method,
					Path:    path,
				})
			}
			if len(resolved) > 0 {
				return selectBestAzureFunctionRoots(resolved, candidates, names)
			}
		}
	}

	if strings.TrimSpace(fallbackHandler) == "" {
		return nil
	}
	return []FlowEndpoint{{
		Repo:    repo,
		File:    functionJSONPath,
		Handler: fallbackHandler,
		Line:    line,
		Method:  method,
		Path:    path,
	}}
}

func selectBestAzureFunctionRoots(resolved []FlowEndpoint, candidatePaths, candidateNames []string) []FlowEndpoint {
	if len(resolved) <= 1 {
		return resolved
	}

	pathRank := make(map[string]int, len(candidatePaths))
	for i, path := range candidatePaths {
		path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
		if path == "" {
			continue
		}
		if _, exists := pathRank[path]; !exists {
			pathRank[path] = i
		}
	}
	nameRank := make(map[string]int, len(candidateNames))
	for i, name := range candidateNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := nameRank[name]; !exists {
			nameRank[name] = i
		}
	}

	sort.SliceStable(resolved, func(i, j int) bool {
		leftPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(resolved[i].File)))
		rightPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(resolved[j].File)))
		leftPathRank, ok := pathRank[leftPath]
		if !ok {
			leftPathRank = len(candidatePaths) + 1000
		}
		rightPathRank, ok := pathRank[rightPath]
		if !ok {
			rightPathRank = len(candidatePaths) + 1000
		}
		if leftPathRank != rightPathRank {
			return leftPathRank < rightPathRank
		}

		leftHandlerRank, ok := nameRank[strings.TrimSpace(resolved[i].Handler)]
		if !ok {
			leftHandlerRank = len(candidateNames) + azureHandlerPriority(resolved[i].Handler)
		}
		rightHandlerRank, ok := nameRank[strings.TrimSpace(resolved[j].Handler)]
		if !ok {
			rightHandlerRank = len(candidateNames) + azureHandlerPriority(resolved[j].Handler)
		}
		if leftHandlerRank != rightHandlerRank {
			return leftHandlerRank < rightHandlerRank
		}

		if resolved[i].Line != resolved[j].Line {
			return resolved[i].Line < resolved[j].Line
		}
		if resolved[i].File != resolved[j].File {
			return resolved[i].File < resolved[j].File
		}
		return resolved[i].Handler < resolved[j].Handler
	})

	best := resolved[0]
	return []FlowEndpoint{best}
}

func azureSourcePathCandidates(functionJSONPath, scriptFile string) []string {
	functionJSONPath = strings.TrimSpace(functionJSONPath)
	if functionJSONPath == "" {
		return nil
	}
	baseDir := filepath.Dir(functionJSONPath)
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	addDefaultIndexCandidates := func() {
		for _, candidateExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(filepath.ToSlash(filepath.Join(baseDir, "index"+candidateExt)))
		}
	}

	scriptFile = strings.TrimSpace(scriptFile)
	if scriptFile != "" {
		resolved := filepath.ToSlash(filepath.Clean(filepath.Join(baseDir, scriptFile)))
		add(resolved)
		ext := filepath.Ext(resolved)
		base := strings.TrimSuffix(resolved, ext)
		for _, candidateExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(base + candidateExt)
		}

		replacements := []struct{ from, to string }{
			{"/dist/", "/src/"},
			{"\\dist\\", "\\src\\"},
		}
		for _, replacement := range replacements {
			if strings.Contains(resolved, replacement.from) {
				alt := strings.Replace(resolved, replacement.from, replacement.to, 1)
				altBase := strings.TrimSuffix(alt, filepath.Ext(alt))
				for _, candidateExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
					add(altBase + candidateExt)
				}
			}
		}
	}

	addDefaultIndexCandidates()

	functionDir := filepath.Base(baseDir)
	parentDir := filepath.Dir(baseDir)
	for _, prefix := range []string{"src", "dist"} {
		for _, candidateExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(filepath.ToSlash(filepath.Join(parentDir, prefix, functionDir, "index"+candidateExt)))
		}
	}

	return out
}

func azureFunctionHandlerNameCandidates(triggerType, fallbackHandler, functionName string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	add(fallbackHandler)
	add(functionName)
	switch strings.TrimSpace(triggerType) {
	case "httpTrigger":
		add("httpTrigger")
	case "timerTrigger":
		add("timerTrigger")
	case "queueTrigger":
		add("queueTrigger")
	case "serviceBusTrigger":
		add("serviceBusTrigger")
	case "blobTrigger":
		add("blobTrigger")
	case "eventHubTrigger":
		add("eventHubTrigger")
	case "cosmosDBTrigger":
		add("cosmosDBTrigger")
	}
	return out
}

func azureHandlerPriority(name string) int {
	switch strings.TrimSpace(name) {
	case "httpTrigger":
		return 0
	case "timerTrigger":
		return 1
	case "queueTrigger":
		return 2
	case "serviceBusTrigger":
		return 3
	default:
		return 10
	}
}

func isHttpNode(node *trace.TreeNode) bool {
	if node == nil || !node.IsCrossService {
		return false
	}
	return node.HttpMethod != "" || node.HttpTarget != ""
}

func isSqsNode(node *trace.TreeNode) bool {
	if node == nil || !node.IsCrossService {
		return false
	}
	return node.IsSqs || node.QueueTarget != ""
}

func flowEndpointFromNode(node *trace.TreeNode, defLines map[string]int) FlowEndpoint {
	if node == nil {
		return FlowEndpoint{}
	}
	repo := node.Repo
	handler := ""
	file := node.File
	if node.CallerID != "" {
		parsedRepo, parsedFile, parsed := trace.ParseCallerID(node.CallerID)
		if parsedRepo != "" {
			repo = parsedRepo
		}
		if parsedFile != "" {
			file = parsedFile
		}
		handler = parsed
	}
	if handler == "" {
		handler = strings.TrimSpace(strings.TrimPrefix(node.Name, "→ "))
		if idx := strings.Index(handler, " ["); idx > 0 {
			handler = handler[:idx]
		}
	}
	line := node.Line
	if node.CallerID != "" && defLines != nil {
		if defLine, ok := defLines[node.CallerID]; ok && defLine > 0 {
			line = defLine
		}
	}
	return FlowEndpoint{
		Repo:    repo,
		Handler: handler,
		File:    file,
		Line:    line,
	}
}

type flowNodeInfo struct {
	Endpoint FlowEndpoint
	Depth    int
}

type dataAccess struct {
	CallerID string
	Entity   string
	Access   string
	Table    string
}

func buildDataHops(ctx context.Context, pool *pgxpool.Pool, roots []*trace.TreeNode, remaining int, seen map[string]bool, includeRelated bool, primaryEntities map[string]bool, baseEntities map[string]bool, entityTables map[string]string, snapshotIDs []int64, includeLegacy bool, stats *flowBuildStats) ([]FlowHop, []FlowEndpoint) {
	if remaining <= 0 {
		return nil, nil
	}

	callerIDs := collectCallerIDs(roots)
	defLines := fetchCallerDefLines(ctx, pool, callerIDs, snapshotIDs, includeLegacy)
	callerInfo := collectCallerInfo(roots, defLines)
	if len(callerInfo) == 0 {
		return nil, nil
	}

	callerIDs = make([]string, 0, len(callerInfo))
	for id := range callerInfo {
		callerIDs = append(callerIDs, id)
	}

	accesses := fetchDataAccesses(ctx, pool, callerIDs, !includeRelated, entityTables, snapshotIDs)
	if len(accesses) == 0 {
		return nil, nil
	}

	writers := make(map[string][]dataAccess)
	for _, acc := range accesses {
		if acc.Access != "write" {
			continue
		}
		if !shouldIncludeFlowEntity(acc.Entity, primaryEntities) {
			continue
		}
		writers[acc.Entity] = append(writers[acc.Entity], acc)
	}
	if len(writers) == 0 {
		return nil, nil
	}

	hops := make([]FlowHop, 0)
	targets := make([]FlowEndpoint, 0)
	endpointCache := make(map[string][]FlowEndpoint)
	candidateCache := make(map[string][]FlowDiscoveryCandidate)
	for entity, writerList := range writers {
		isPrimary := false
		if len(baseEntities) == 0 {
			isPrimary = true
		} else {
			isPrimary = baseEntities[canonicalEntityName(entity)]
		}
		maxPublishPerEntity := 10
		if includeRelated && !isPrimary {
			maxPublishPerEntity = 1
		}
		for _, writer := range writerList {
			writerInfo, ok := callerInfo[writer.CallerID]
			if !ok {
				continue
			}
			writerRepo := writerInfo.Endpoint.Repo
			endpointsKey := entity + "|" + writerRepo
			exposedEndpoints, ok := endpointCache[endpointsKey]
			if !ok {
				var lookupErr error
				exposedEndpoints, lookupErr = findDataEndpoints(ctx, pool, entity, writerRepo, snapshotIDs, includeLegacy)
				if lookupErr != nil {
					stats.DiscoveryWarnings = append(stats.DiscoveryWarnings, "indexed reader lookup incomplete for "+entity)
				}
				endpointCache[endpointsKey] = exposedEndpoints
				candidates, truncated, err := findEndpointDiscoveryCandidates(ctx, pool, entity, writerRepo, snapshotIDs, includeLegacy, exposedEndpoints)
				if err != nil {
					stats.DiscoveryWarnings = append(stats.DiscoveryWarnings, "endpoint candidate lookup incomplete for "+entity)
				}
				candidateCache[endpointsKey] = candidates
				if truncated {
					stats.DiscoveryWarnings = append(stats.DiscoveryWarnings, "endpoint candidate lookup truncated for "+entity)
				}
			}
			for _, candidate := range candidateCache[endpointsKey] {
				endpoint := candidate.Endpoint
				key := "candidate|" + writer.CallerID + "|" + entity + "|" + endpoint.Repo + "|" + endpoint.File + "|" + endpoint.Handler + "|" + endpoint.Method + "|" + endpoint.Path + "|" + candidate.Reason
				if !seen[key] {
					seen[key] = true
					candidate.Entity = entity
					candidate.From = writerInfo.Endpoint
					stats.Candidates = append(stats.Candidates, candidate)
				}
			}
			filtered := filterEndpointsForEntity(exposedEndpoints, entity)
			if len(filtered) > 0 {
				exposedEndpoints = filtered
			} else if includeRelated && !isPrimary {
				continue
			}
			publishCount := 0
			for _, endpoint := range exposedEndpoints {
				if len(hops) >= remaining {
					return hops, targets
				}
				if endpoint.Repo == "" || endpoint.Repo == writerRepo {
					continue
				}
				if publishCount >= maxPublishPerEntity {
					break
				}
				key := writer.CallerID + "|" + endpoint.Repo + "|" + endpoint.Path + "|" + entity + "|endpoint"
				if seen[key] {
					continue
				}
				seen[key] = true
				hops = append(hops, FlowHop{
					Depth: writerInfo.Depth,
					From:  writerInfo.Endpoint,
					Via: FlowVia{
						Type:   "data",
						Entity: entity,
						Table:  writer.Table,
						Access: "publish",
					},
					To: endpoint,
				})
				targets = append(targets, endpoint)
				publishCount++
			}
		}
	}

	return hops, targets
}

func resolveCallerIDsForEndpoints(pool trace.Queryer, endpoints []FlowEndpoint, snapshotIDs []int64, includeLegacy bool) []flowFrontier {
	if len(endpoints) == 0 {
		return nil
	}
	callerIDs := make([]string, 0, len(endpoints))
	endpointByCaller := make(map[string]FlowEndpoint)
	for _, endpoint := range endpoints {
		if endpoint.Repo == "" || endpoint.File == "" || endpoint.Handler == "" {
			continue
		}
		callerID := trace.BuildCallerID(endpoint.Repo, endpoint.File, endpoint.Handler)
		if callerID == "" {
			continue
		}
		if _, exists := endpointByCaller[callerID]; exists {
			continue
		}
		endpointByCaller[callerID] = endpoint
		callerIDs = append(callerIDs, callerID)
	}
	if len(callerIDs) == 0 {
		return nil
	}
	resolved := trace.FindFunctionIDsByCallerIDsForSnapshotFilter(pool, callerIDs, snapshotIDs, includeLegacy)
	out := make([]flowFrontier, 0, len(callerIDs))
	for _, callerID := range callerIDs {
		if resolved[callerID] == 0 {
			continue
		}
		out = append(out, flowFrontier{
			callerID: callerID,
			endpoint: endpointByCaller[callerID],
		})
	}
	return out
}

func collectCallerInfo(roots []*trace.TreeNode, defLines map[string]int) map[string]flowNodeInfo {
	info := make(map[string]flowNodeInfo)
	var walk func(node *trace.TreeNode)
	walk = func(node *trace.TreeNode) {
		if node == nil {
			return
		}
		if node.CallerID != "" {
			if _, exists := info[node.CallerID]; !exists {
				info[node.CallerID] = flowNodeInfo{
					Endpoint: flowEndpointFromNode(node, defLines),
					Depth:    node.Depth,
				}
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return info
}

func buildAccessIndex(accesses []dataAccess) map[string][]dataAccess {
	index := make(map[string][]dataAccess)
	for _, access := range accesses {
		if access.CallerID == "" || access.Entity == "" {
			continue
		}
		index[access.CallerID] = append(index[access.CallerID], access)
	}
	return index
}

func scopeDescendantsForEntities(roots []*trace.TreeNode, accessByCaller map[string][]dataAccess, primaryEntities map[string]bool) map[string]bool {
	scoped := make(map[string]bool)
	if len(primaryEntities) == 0 {
		return scoped
	}

	callerTouchesPrimary := func(callerID string) bool {
		if callerID == "" {
			return false
		}
		accesses, ok := accessByCaller[callerID]
		if !ok {
			return false
		}
		for _, access := range accesses {
			if primaryEntities[canonicalEntityName(access.Entity)] {
				return true
			}
		}
		return false
	}

	var markSubtree func(node *trace.TreeNode)
	markSubtree = func(node *trace.TreeNode) {
		if node == nil {
			return
		}
		if node.CallerID != "" {
			scoped[node.CallerID] = true
		}
		for _, child := range node.Children {
			markSubtree(child)
		}
	}

	var walk func(node *trace.TreeNode)
	walk = func(node *trace.TreeNode) {
		if node == nil {
			return
		}
		if callerTouchesPrimary(node.CallerID) {
			markSubtree(node)
			return
		}
		for _, child := range node.Children {
			walk(child)
		}
	}

	for _, root := range roots {
		walk(root)
	}
	return scoped
}

func collectCallerIDs(roots []*trace.TreeNode) []string {
	seen := make(map[string]bool)
	var out []string
	var walk func(node *trace.TreeNode)
	walk = func(node *trace.TreeNode) {
		if node == nil {
			return
		}
		if node.CallerID != "" && !seen[node.CallerID] {
			seen[node.CallerID] = true
			out = append(out, node.CallerID)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return out
}

func fetchCallerDefLines(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, snapshotIDs []int64, includeLegacy bool) map[string]int {
	if len(callerIDs) == 0 {
		return nil
	}
	query := `
		SELECT (r.name || ':' || f.path || ':' || fn.name) AS caller_id, fn.start_line
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE (r.name || ':' || f.path || ':' || fn.name) = ANY($1)
		  AND ($2::bigint[] IS NULL OR f.snapshot_id = ANY($2)
		       OR ($3 AND f.snapshot_id IS NULL))`
	rows, err := pool.Query(ctx, query, callerIDs, snapshotIDs, includeLegacy)
	if err != nil {
		return nil
	}
	defer rows.Close()
	defLines := make(map[string]int)
	ambiguous := make(map[string]bool)
	for rows.Next() {
		var callerID string
		var line int
		if err := rows.Scan(&callerID, &line); err != nil {
			return nil
		}
		if ambiguous[callerID] {
			continue
		}
		if previous, exists := defLines[callerID]; exists && previous != line {
			delete(defLines, callerID)
			ambiguous[callerID] = true
			continue
		}
		defLines[callerID] = line
	}
	if rows.Err() != nil {
		return nil
	}
	return defLines
}

func fetchDataAccesses(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, strict bool, entityTables map[string]string, snapshotIDs []int64) []dataAccess {
	if len(callerIDs) == 0 {
		return nil
	}
	query := `
		SELECT caller_id, callee_name
		FROM trace_call_edges
		WHERE caller_id = ANY($1)
		  AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2))`
	rows, err := pool.Query(ctx, query, callerIDs, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var accesses []dataAccess
	seen := make(map[string]bool)
	entityExists := make(map[string]bool)
	receiverEntityCache := make(map[string][]string)
	for rows.Next() {
		var callerID, calleeName string
		if err := rows.Scan(&callerID, &calleeName); err != nil {
			continue
		}
		repo, _, _ := trace.ParseCallerID(callerID)
		receiver, method := splitCall(calleeName)
		if receiver == "" || method == "" {
			continue
		}
		entities := resolveEntitiesForReceiver(ctx, pool, repo, receiver, method, receiverEntityCache)
		if len(entities) == 0 && !strict {
			if fallback := resolveEntityForAccess(ctx, pool, repo, receiver, method, entityExists); fallback != "" {
				entities = []string{fallback}
			}
		}
		if len(entities) == 0 {
			continue
		}
		access := classifyAccess(method)
		if access == "" {
			continue
		}
		for _, entity := range entities {
			if entity == "" {
				continue
			}
			key := callerID + "|" + strings.ToLower(entity) + "|" + access
			if seen[key] {
				continue
			}
			seen[key] = true
			accesses = append(accesses, dataAccess{
				CallerID: callerID,
				Entity:   entity,
				Access:   access,
				Table:    tableForEntity(entityTables, entity),
			})
		}
	}

	accessRows, err := pool.Query(ctx, `
		SELECT da.caller_id, da.entity_name, da.access
		FROM data_accesses da
		WHERE da.caller_id = ANY($1)
		  AND ($2::bigint[] IS NULL OR da.snapshot_id = ANY($2))`, callerIDs, snapshotIDs)
	if err == nil {
		defer accessRows.Close()
		for accessRows.Next() {
			var callerID, entity, access string
			if err := accessRows.Scan(&callerID, &entity, &access); err != nil {
				continue
			}
			if entity == "" || access == "" {
				continue
			}
			key := callerID + "|" + strings.ToLower(entity) + "|" + access
			if seen[key] {
				continue
			}
			seen[key] = true
			accesses = append(accesses, dataAccess{
				CallerID: callerID,
				Entity:   entity,
				Access:   access,
				Table:    tableForEntity(entityTables, entity),
			})
		}
	}

	return accesses
}

func tableForEntity(entityTables map[string]string, entity string) string {
	if len(entityTables) == 0 {
		return ""
	}
	key := canonicalEntityName(entity)
	if key == "" {
		return ""
	}
	return entityTables[key]
}

func shouldIncludeFlowEntity(entity string, allowed map[string]bool) bool {
	if len(allowed) == 0 {
		return true
	}
	return allowed[canonicalEntityName(entity)]
}

func findDataEndpoints(ctx context.Context, pool *pgxpool.Pool, entity string, excludeRepo string, snapshotIDs []int64, includeLegacy bool) ([]FlowEndpoint, error) {
	readerCallerIDs, err := findDataReadersByEntity(ctx, pool, entity, excludeRepo, snapshotIDs, includeLegacy)
	if err != nil {
		return nil, err
	}
	return findEndpointsForReaders(ctx, pool, readerCallerIDs, excludeRepo, snapshotIDs, includeLegacy)
}

func findEndpointsForReaders(ctx context.Context, pool *pgxpool.Pool, readerCallerIDs []string, excludeRepo string, snapshotIDs []int64, includeLegacy bool) ([]FlowEndpoint, error) {
	endpoints := make([]FlowEndpoint, 0)
	if len(readerCallerIDs) > 0 {
		const maxDepth = 3
		seen := make(map[string]bool)

		// Include endpoints whose handler is the reader itself.
		direct, err := queryEndpointsForCallers(ctx, pool, readerCallerIDs, excludeRepo, seen, snapshotIDs, includeLegacy)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, direct...)

		// Walk upstream from the reader to find endpoints that reach it through services.
		query := `
			WITH RECURSIVE upstream AS (
				SELECT caller_id, callee_id, 1 AS depth
				FROM trace_call_edges
				WHERE callee_id = ANY($1)
				  AND ($3::bigint[] IS NULL OR snapshot_id = ANY($3) OR ($4 AND snapshot_id IS NULL))
				UNION ALL
				SELECT t.caller_id, t.callee_id, u.depth + 1
				FROM trace_call_edges t
				JOIN upstream u ON t.callee_id = u.caller_id
				WHERE u.depth < $2
				  AND ($3::bigint[] IS NULL OR t.snapshot_id = ANY($3) OR ($4 AND t.snapshot_id IS NULL))
			)
			SELECT DISTINCT u.caller_id
			FROM upstream u
			ORDER BY u.caller_id
			LIMIT 500`
		rows, err := pool.Query(ctx, query, readerCallerIDs, maxDepth, snapshotIDs, includeLegacy)
		if err != nil {
			return nil, err
		}
		{
			var upstreamCallers []string
			for rows.Next() {
				var callerID string
				if err := rows.Scan(&callerID); err != nil {
					rows.Close()
					return nil, err
				}
				upstreamCallers = append(upstreamCallers, callerID)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if len(upstreamCallers) > 0 {
				upstream, err := queryEndpointsForCallers(ctx, pool, upstreamCallers, excludeRepo, seen, snapshotIDs, includeLegacy)
				if err != nil {
					return nil, err
				}
				endpoints = append(endpoints, upstream...)
			}
		}
	}

	return endpoints, nil
}

func findDataReadersByEntity(ctx context.Context, pool *pgxpool.Pool, entity string, excludeRepo string, snapshotIDs []int64, includeLegacy bool) ([]string, error) {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return nil, nil
	}
	query := `
		SELECT DISTINCT da.caller_id
		FROM data_accesses da
		JOIN repositories r ON da.repo_id = r.id
		WHERE lower(da.entity_name) = lower($1)
		  AND da.access = 'read'
		  AND r.name != $2
		  AND ($3::bigint[] IS NULL OR da.snapshot_id = ANY($3) OR ($4 AND da.snapshot_id IS NULL))
		ORDER BY da.caller_id
		LIMIT 500`
	rows, err := pool.Query(ctx, query, entity, excludeRepo, snapshotIDs, includeLegacy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var callerID string
		if err := rows.Scan(&callerID); err != nil {
			return nil, err
		}
		out = append(out, callerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func inferPrimaryEntities(ctx context.Context, pool *pgxpool.Pool, rootCallerIDs []string, rootEndpoints []FlowEndpoint, snapshotIDs []int64) map[string]bool {
	scores := make(map[string]int)
	forced := make(map[string]bool)
	add := func(entity string, weight int) {
		if entity == "" {
			return
		}
		canonical := canonicalEntityName(entity)
		if canonical == "" {
			return
		}
		scores[canonical] += weight
	}
	addForced := func(entity string) {
		if entity == "" {
			return
		}
		canonical := canonicalEntityName(entity)
		if canonical == "" {
			return
		}
		forced[canonical] = true
	}

	for _, endpoint := range rootEndpoints {
		if endpoint.Path != "" {
			entity := entityFromPath(endpoint.Path)
			add(entity, 6)
			addForced(entity)
		}
		if endpoint.Handler != "" {
			entity := entityFromHandler(endpoint.Handler)
			add(entity, 3)
			addForced(entity)
		}
	}

	if len(forced) > 0 {
		out := make(map[string]bool)
		for entity := range forced {
			out[entity] = true
		}
		return out
	}

	if len(rootCallerIDs) > 0 {
		accesses := fetchDataAccesses(ctx, pool, rootCallerIDs, true, nil, snapshotIDs)
		for _, access := range accesses {
			if access.Access == "write" {
				add(access.Entity, 10)
			} else {
				add(access.Entity, 3)
			}
		}
	}

	if len(scores) == 0 {
		return nil
	}

	maxScore := 0
	for _, score := range scores {
		if score > maxScore {
			maxScore = score
		}
	}
	if maxScore == 0 {
		return nil
	}

	threshold := maxScore
	if maxScore >= 8 {
		threshold = maxScore - 2
	}

	out := make(map[string]bool)
	for entity, score := range scores {
		if score >= threshold {
			out[entity] = true
		}
	}
	return out
}

func expandEntitiesWithRelationships(ctx context.Context, pool *pgxpool.Pool, entities map[string]bool, scope searchWorkspaceScope) (map[string]bool, error) {
	if len(entities) == 0 {
		return entities, nil
	}
	out := make(map[string]bool)
	for key := range entities {
		out[key] = true
	}
	rows, err := pool.Query(ctx, `
		SELECT c.name, r.target_entity_name
		FROM jpa_relationships r
		JOIN classes c ON c.id = r.source_class_id
		JOIN files f ON f.id = c.file_id
		WHERE ($1::bigint[] IS NULL OR f.snapshot_id = ANY($1)
		       OR ($2 AND f.snapshot_id IS NULL))`, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		return entities, err
	}
	defer rows.Close()

	for rows.Next() {
		var source, target string
		if err := rows.Scan(&source, &target); err != nil {
			return entities, err
		}
		srcKey := canonicalEntityName(source)
		tgtKey := canonicalEntityName(target)
		if srcKey == "" || tgtKey == "" {
			continue
		}
		// Expand one relationship from the requested scope, independent of row order.
		if entities[srcKey] {
			out[tgtKey] = true
		}
		if entities[tgtKey] {
			out[srcKey] = true
		}
	}
	if err := rows.Err(); err != nil {
		return entities, err
	}
	return out, nil
}

func loadEntityTableMap(ctx context.Context, pool *pgxpool.Pool, scope searchWorkspaceScope) (map[string]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.name, e.table_name
		FROM jpa_entities e
		JOIN classes c ON c.id = e.class_id
		JOIN files f ON f.id = c.file_id
		WHERE ($1::bigint[] IS NULL OR f.snapshot_id = ANY($1)
		       OR ($2 AND f.snapshot_id IS NULL))`, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	ambiguous := make(map[string]bool)
	for rows.Next() {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			return nil, err
		}
		key := canonicalEntityName(name)
		if key == "" || table == "" {
			continue
		}
		if ambiguous[key] {
			continue
		}
		if previous, exists := out[key]; exists && previous != table {
			delete(out, key)
			ambiguous[key] = true
			continue
		}
		out[key] = table
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func entityFromPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "{") {
			continue
		}
		lower := strings.ToLower(segment)
		switch lower {
		case "api", "action", "v1", "v2", "v3", "v4", "v5":
			continue
		default:
			return normalizeEntityName(segment)
		}
	}
	return ""
}

func entityFromHandler(handler string) string {
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return ""
	}
	if idx := strings.Index(handler, "."); idx >= 0 {
		handler = handler[:idx]
	}
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return ""
	}
	suffixes := []string{
		"Controller",
		"Resource",
		"ActionBean",
		"Action",
		"Service",
		"Manager",
		"Handler",
		"Endpoint",
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(handler, suffix) {
			handler = strings.TrimSuffix(handler, suffix)
			break
		}
	}
	return normalizeEntityName(handler)
}

func canonicalEntityName(entity string) string {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return ""
	}
	return strings.ToLower(normalizeEntityName(entity))
}

func filterEndpointsForEntity(endpoints []FlowEndpoint, entity string) []FlowEndpoint {
	if len(endpoints) == 0 || entity == "" {
		return nil
	}
	variants := entityNameVariants(strings.ToLower(entity))
	if len(variants) == 0 {
		return nil
	}
	matches := make([]FlowEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpointMatchesEntity(endpoint, variants) {
			matches = append(matches, endpoint)
		}
	}
	return matches
}

func endpointMatchesEntity(endpoint FlowEndpoint, variants []string) bool {
	if len(variants) == 0 {
		return false
	}
	path := strings.ToLower(endpoint.Path)
	handler := strings.ToLower(endpoint.Handler)
	for _, variant := range variants {
		if variant == "" {
			continue
		}
		if path != "" {
			if strings.Contains(path, "/"+variant) || strings.Contains(path, variant+"/") || strings.Contains(path, variant) {
				return true
			}
		}
		if handler != "" && strings.Contains(handler, variant) {
			return true
		}
	}
	return false
}

func buildFlowNarratives(ctx context.Context, pool *pgxpool.Pool, roots []FlowEndpoint, hops []FlowHop, primaryEntities map[string]bool, callers []FlowEndpoint, includeRelated bool, entityTables map[string]string) []FlowNarrative {
	if len(roots) == 0 || len(hops) == 0 {
		return nil
	}
	narratives := make([]FlowNarrative, 0, len(roots))
	for _, root := range roots {
		steps := buildNarrativeForRoot(ctx, pool, root, hops, primaryEntities, callers, includeRelated, entityTables)
		if len(steps) == 0 {
			continue
		}
		narratives = append(narratives, FlowNarrative{
			Root:  root,
			Steps: steps,
		})
	}
	return narratives
}

func buildNarrativeForRoot(ctx context.Context, pool *pgxpool.Pool, root FlowEndpoint, hops []FlowHop, primaryEntities map[string]bool, callers []FlowEndpoint, includeRelated bool, entityTables map[string]string) []FlowNarrativeStep {
	steps := make([]FlowNarrativeStep, 0)
	resolver := newFlowNarrativeEvidenceResolver(ctx, pool)
	chain := buildNarrativeChainIndices(root, hops, 8)
	if caller := bestCallerForRoot(root, callers); caller != nil {
		via := FlowVia{Type: "http", Method: root.Method, Path: root.Path}
		step := FlowNarrativeStep{
			Kind: "caller",
			From: *caller,
			Via:  &via,
			To:   root,
		}
		enrichNarrativeStepEvidence(&step, resolver)
		steps = append(steps, step)
	}

	if detailed, focus := buildOperationalNarrativeForRoot(resolver, root); len(detailed) > 0 {
		steps = append(steps, detailed...)
		submitHop := findBestRelatedDataHop(chain, hops, root, focus, detailed)
		if includeRelated && focus != nil {
			if dataStep := buildSubmitWriteStep(ctx, pool, *focus, primaryEntities, entityTables); dataStep != nil {
				enrichNarrativeStepEvidence(dataStep, resolver)
				steps = append(steps, *dataStep)
			}
		}
		if submitHop != nil && !sameEndpoint(submitHop.To, root) {
			via := submitHop.Via
			bridge := FlowNarrativeStep{
				Kind: "hop",
				From: submitHop.From,
				Via:  &via,
				To:   submitHop.To,
			}
			enrichNarrativeStepEvidence(&bridge, resolver)
			steps = append(steps, bridge)

			if relatedDetailed, relatedFocus := buildOperationalNarrativeForRoot(resolver, submitHop.To); len(relatedDetailed) > 0 {
				steps = append(steps, relatedDetailed...)
				if includeRelated && relatedFocus != nil {
					if dataStep := buildSubmitWriteStep(ctx, pool, *relatedFocus, primaryEntities, entityTables); dataStep != nil {
						enrichNarrativeStepEvidence(dataStep, resolver)
						steps = append(steps, *dataStep)
					}
				}
			} else if includeRelated {
				if dataStep := buildSubmitWriteStep(ctx, pool, submitHop.To, primaryEntities, entityTables); dataStep != nil {
					enrichNarrativeStepEvidence(dataStep, resolver)
					steps = append(steps, *dataStep)
				}
			}
		}
		return dedupeNarrativeSteps(steps)
	}

	if len(chain) == 0 {
		return dedupeNarrativeSteps(steps)
	}

	callOutDegree := callOutDegreeBySource(hops)
	var submitEndpoint *FlowEndpoint
	for _, idx := range chain {
		hop := hops[idx]
		if hop.Via.Type == "call" {
			if branchStep := buildBranchNarrativeStep(resolver, hop, callOutDegree); branchStep != nil {
				steps = append(steps, *branchStep)
			}
		}
		via := hop.Via
		step := FlowNarrativeStep{
			Kind: "hop",
			From: hop.From,
			Via:  &via,
			To:   hop.To,
		}
		enrichNarrativeStepEvidence(&step, resolver)
		steps = append(steps, step)
		if submitEndpoint == nil && hop.Via.Type == "data" {
			to := hop.To
			submitEndpoint = &to
		}
	}

	if includeRelated && submitEndpoint != nil {
		if dataStep := buildSubmitWriteStep(ctx, pool, *submitEndpoint, primaryEntities, entityTables); dataStep != nil {
			enrichNarrativeStepEvidence(dataStep, resolver)
			steps = append(steps, *dataStep)
		}
	}
	return dedupeNarrativeSteps(steps)
}

func firstDataHopOnChain(chain []int, hops []FlowHop) *FlowHop {
	if len(chain) == 0 || len(hops) == 0 {
		return nil
	}
	for _, idx := range chain {
		if idx < 0 || idx >= len(hops) {
			continue
		}
		hop := hops[idx]
		if hop.Via.Type != "data" {
			continue
		}
		copy := hop
		return &copy
	}
	return nil
}

func findBestRelatedDataHop(chain []int, hops []FlowHop, root FlowEndpoint, focus *FlowEndpoint, detailed []FlowNarrativeStep) *FlowHop {
	if hop := firstDataHopOnChain(chain, hops); hop != nil {
		return hop
	}
	if len(hops) == 0 {
		return nil
	}
	seedKeys := make(map[string]bool)
	addSeed := func(endpoint FlowEndpoint) {
		if key := flowEndpointTraversalKey(endpoint); key != "" {
			seedKeys[key] = true
		}
	}
	addSeed(root)
	if focus != nil {
		addSeed(*focus)
	}
	for _, step := range detailed {
		addSeed(step.From)
		addSeed(step.To)
	}
	if len(seedKeys) == 0 {
		return nil
	}

	var best *FlowHop
	bestScore := -1
	for _, hop := range hops {
		if hop.Via.Type != "data" || strings.TrimSpace(hop.To.Path) == "" {
			continue
		}
		fromKey := flowEndpointTraversalKey(hop.From)
		if fromKey == "" || !seedKeys[fromKey] {
			continue
		}
		score := endpointSpecificityScore(hop.To)
		if score > bestScore {
			copy := hop
			best = &copy
			bestScore = score
		}
	}
	return best
}

type flowNarrativeEvidenceResolver struct {
	ctx            context.Context
	pool           *pgxpool.Pool
	callLineCache  map[string]int
	sourceCache    map[string]flowFunctionSource
	callSitesCache map[string][]flowCallSite
}

type flowFunctionSource struct {
	StartLine int
	Source    string
}

type flowCallSite struct {
	CalleeID   string
	CalleeName string
	Line       int
}

func newFlowNarrativeEvidenceResolver(ctx context.Context, pool *pgxpool.Pool) *flowNarrativeEvidenceResolver {
	return &flowNarrativeEvidenceResolver{
		ctx:            ctx,
		pool:           pool,
		callLineCache:  make(map[string]int),
		sourceCache:    make(map[string]flowFunctionSource),
		callSitesCache: make(map[string][]flowCallSite),
	}
}

func narrativeStepHasRequiredEvidence(step *FlowNarrativeStep) bool {
	if step == nil {
		return false
	}
	if endpointEvidenceRef(step.From) == "" || endpointEvidenceRef(step.To) == "" {
		return false
	}
	return true
}

func enforceNarrativeStepEvidenceConfidence(step *FlowNarrativeStep) {
	if step == nil {
		return
	}
	hasRequiredEvidence := narrativeStepHasRequiredEvidence(step)
	if step.Confidence == "" {
		if hasRequiredEvidence {
			step.Confidence = "exact"
		} else {
			step.Confidence = "unknown"
		}
		return
	}
	if step.Confidence == "exact" && !hasRequiredEvidence {
		step.Confidence = "unknown"
	}
}

func enrichNarrativeStepEvidence(step *FlowNarrativeStep, resolver *flowNarrativeEvidenceResolver) {
	if step == nil {
		return
	}
	evidence := make([]string, 0, 3)
	if fromRef := endpointEvidenceRef(step.From); fromRef != "" {
		evidence = append(evidence, fromRef)
	}
	if toRef := endpointEvidenceRef(step.To); toRef != "" {
		evidence = append(evidence, toRef)
	}
	if resolver != nil && step.Via != nil && step.Via.Type == "call" {
		if callRef := resolver.callSiteEvidence(step.From, step.To); callRef != "" {
			evidence = append(evidence, callRef)
		}
	}
	step.Evidence = uniqueStrings(evidence)
	enforceNarrativeStepEvidenceConfidence(step)
}

func endpointEvidenceRef(endpoint FlowEndpoint) string {
	if endpoint.Repo == "" || endpoint.File == "" || endpoint.Line <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/%s:%d", endpoint.Repo, endpoint.File, endpoint.Line)
}

func buildBranchNarrativeStep(resolver *flowNarrativeEvidenceResolver, hop FlowHop, callOutDegree map[string]int) *FlowNarrativeStep {
	if hop.Via.Type != "call" {
		return nil
	}
	fromKey := flowEndpointTraversalKey(hop.From)
	if fromKey == "" || callOutDegree[fromKey] < 2 {
		return nil
	}
	condition := ""
	conditionEvidence := ""
	if resolver != nil {
		condition, conditionEvidence = resolver.resolveBranchCondition(hop.From, hop.To)
	}
	confidence := "exact"
	if condition == "" {
		condition = "unknown"
		confidence = "unknown"
	}
	step := FlowNarrativeStep{
		Kind:       "branch",
		From:       hop.From,
		To:         hop.To,
		Condition:  condition,
		Confidence: confidence,
	}
	evidence := make([]string, 0, 3)
	if conditionEvidence != "" {
		evidence = append(evidence, conditionEvidence)
	}
	if fromRef := endpointEvidenceRef(hop.From); fromRef != "" {
		evidence = append(evidence, fromRef)
	}
	if toRef := endpointEvidenceRef(hop.To); toRef != "" {
		evidence = append(evidence, toRef)
	}
	step.Evidence = uniqueStrings(evidence)
	enforceNarrativeStepEvidenceConfidence(&step)
	return &step
}

func callOutDegreeBySource(hops []FlowHop) map[string]int {
	out := make(map[string]int)
	for _, hop := range hops {
		if hop.Via.Type != "call" {
			continue
		}
		key := flowEndpointTraversalKey(hop.From)
		if key == "" {
			continue
		}
		out[key]++
	}
	return out
}

func buildOperationalNarrativeForRoot(resolver *flowNarrativeEvidenceResolver, root FlowEndpoint) ([]FlowNarrativeStep, *FlowEndpoint) {
	if resolver == nil {
		return nil, nil
	}
	focus := chooseOperationalFocusFunction(resolver, root)
	callSites := filterOperationalCallSites(resolver.orderedCallSites(focus))
	if len(callSites) == 0 {
		return nil, nil
	}

	steps := make([]FlowNarrativeStep, 0, len(callSites)+2)
	if !sameEndpoint(root, focus) {
		via := FlowVia{Type: "call"}
		bridge := FlowNarrativeStep{
			Kind: "hop",
			From: root,
			Via:  &via,
			To:   focus,
		}
		enrichNarrativeStepEvidence(&bridge, resolver)
		steps = append(steps, bridge)
	}

	var source flowFunctionSource
	hasSource := false
	if src, ok := resolver.functionSource(focus); ok {
		source = src
		hasSource = true
	}

	prevCondition := ""
	maxOps := 24
	for _, site := range callSites {
		if len(steps) >= maxOps {
			break
		}
		if hasSource {
			condition := extractBranchConditionFromSource(source.Source, source.StartLine, site.Line)
			if condition != "" && condition != prevCondition {
				branch := FlowNarrativeStep{
					Kind:       "branch",
					From:       focus,
					To:         focus,
					Condition:  condition,
					Confidence: "exact",
					Evidence:   uniqueStrings([]string{callSiteEvidenceRef(focus, site.Line)}),
				}
				enforceNarrativeStepEvidenceConfidence(&branch)
				steps = append(steps, branch)
				prevCondition = condition
			}
		}

		to := resolver.resolveCalleeEndpoint(site)
		if to.Repo == "" && to.Handler == "" {
			to = FlowEndpoint{
				Repo:    focus.Repo,
				Handler: strings.TrimSpace(site.CalleeName),
			}
		}
		via := FlowVia{Type: "call"}
		step := FlowNarrativeStep{
			Kind: "hop",
			From: focus,
			Via:  &via,
			To:   to,
		}
		enrichNarrativeStepEvidence(&step, resolver)
		if ref := callSiteEvidenceRef(focus, site.Line); ref != "" {
			step.Evidence = uniqueStrings(append(step.Evidence, ref))
		}
		enforceNarrativeStepEvidenceConfidence(&step)
		steps = append(steps, step)
	}

	if len(steps) == 0 {
		return nil, nil
	}
	return dedupeNarrativeSteps(steps), &focus
}

func chooseOperationalFocusFunction(resolver *flowNarrativeEvidenceResolver, root FlowEndpoint) FlowEndpoint {
	best := root
	rootCalls := filterOperationalCallSites(resolver.orderedCallSites(root))
	bestCount := len(rootCalls)
	for _, site := range rootCalls {
		callee := resolver.resolveCalleeEndpoint(site)
		if callee.Repo == "" || callee.Handler == "" {
			continue
		}
		if !strings.EqualFold(callee.Repo, root.Repo) {
			continue
		}
		count := len(filterOperationalCallSites(resolver.orderedCallSites(callee)))
		if count > bestCount+2 {
			best = callee
			bestCount = count
		}
	}
	return best
}

func filterOperationalCallSites(callSites []flowCallSite) []flowCallSite {
	if len(callSites) == 0 {
		return nil
	}
	out := make([]flowCallSite, 0, len(callSites))
	seen := make(map[string]bool, len(callSites))
	for _, site := range callSites {
		if !isOperationalCallSite(site) {
			continue
		}
		key := strconv.Itoa(site.Line) + "|" + strings.TrimSpace(site.CalleeName)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, site)
	}
	return out
}

func isOperationalCallSite(site flowCallSite) bool {
	if site.Line <= 0 {
		return false
	}
	receiver, method := splitCall(site.CalleeName)
	if method == "" {
		return false
	}
	m := strings.ToLower(strings.TrimSpace(method))
	if m == "" {
		return false
	}
	trivialExact := map[string]bool{
		"equals": true, "hashcode": true, "tostring": true, "length": true, "size": true,
		"trim": true, "isempty": true, "iterator": true, "next": true, "hasnext": true,
		"valueof": true, "ordinal": true, "name": true, "getmessage": true, "printstacktrace": true,
	}
	if trivialExact[m] {
		return false
	}
	if site.CalleeID != "" {
		return true
	}
	if isOperationalReceiver(receiver) {
		return true
	}
	if classifyAccess(method) != "" {
		// Avoid noisy POJO-style accessors (Page.getId, User.setX, etc.).
		if strings.HasPrefix(m, "get") || strings.HasPrefix(m, "set") || strings.HasPrefix(m, "is") {
			return false
		}
		return true
	}
	if strings.HasPrefix(m, "send") || strings.HasPrefix(m, "execute") || strings.HasPrefix(m, "run") || strings.HasPrefix(m, "validate") || strings.HasPrefix(m, "log") {
		return true
	}
	return false
}

func isOperationalReceiver(receiver string) bool {
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return false
	}
	if idx := strings.LastIndex(receiver, "."); idx >= 0 {
		receiver = receiver[idx+1:]
	}
	suffixes := []string{
		"Service", "Manager", "Dao", "Repository", "Repo", "Mapper", "Controller",
		"ActionBean", "Action", "Executor", "Client", "Producer", "Consumer", "Util",
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(receiver, suffix) && len(receiver) > len(suffix) {
			return true
		}
	}
	return false
}

func callSiteEvidenceRef(from FlowEndpoint, line int) string {
	if from.Repo == "" || from.File == "" || line <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/%s:%d", from.Repo, from.File, line)
}

func (r *flowNarrativeEvidenceResolver) orderedCallSites(endpoint FlowEndpoint) []flowCallSite {
	if r == nil || r.pool == nil {
		return nil
	}
	callerID := trace.BuildCallerID(endpoint.Repo, endpoint.File, endpoint.Handler)
	if callerID == "" {
		return nil
	}
	if cached, ok := r.callSitesCache[callerID]; ok {
		return cached
	}
	rows, err := r.pool.Query(r.ctx, `
		SELECT COALESCE(callee_id, ''), callee_name, line_number
		FROM trace_call_edges
		WHERE caller_id = $1
		  AND line_number IS NOT NULL
		ORDER BY line_number, callee_name
		LIMIT 500
	`, callerID)
	if err != nil {
		r.callSitesCache[callerID] = nil
		return nil
	}
	defer rows.Close()
	callSites := make([]flowCallSite, 0, 32)
	for rows.Next() {
		var calleeID, calleeName string
		var line int
		if err := rows.Scan(&calleeID, &calleeName, &line); err != nil {
			continue
		}
		callSites = append(callSites, flowCallSite{
			CalleeID:   strings.TrimSpace(calleeID),
			CalleeName: strings.TrimSpace(calleeName),
			Line:       line,
		})
	}
	r.callSitesCache[callerID] = callSites
	return callSites
}

func (r *flowNarrativeEvidenceResolver) resolveCalleeEndpoint(site flowCallSite) FlowEndpoint {
	if site.CalleeID != "" {
		repo, file, handler := trace.ParseCallerID(site.CalleeID)
		ep := FlowEndpoint{
			Repo:    repo,
			File:    file,
			Handler: handler,
		}
		if src, ok := r.functionSource(ep); ok && src.StartLine > 0 {
			ep.Line = src.StartLine
		}
		return ep
	}

	// A display name does not identify a callee across files or repositories.
	return FlowEndpoint{Handler: strings.TrimSpace(site.CalleeName)}
}

func (r *flowNarrativeEvidenceResolver) resolveBranchCondition(from, to FlowEndpoint) (string, string) {
	if r == nil || r.pool == nil {
		return "", ""
	}
	callLine := r.callSiteLine(from, to)
	if callLine <= 0 {
		return "", ""
	}
	source, ok := r.functionSource(from)
	if !ok {
		return "", r.callSiteEvidence(from, to)
	}
	condition := extractBranchConditionFromSource(source.Source, source.StartLine, callLine)
	return condition, r.callSiteEvidence(from, to)
}

func (r *flowNarrativeEvidenceResolver) callSiteEvidence(from, to FlowEndpoint) string {
	if from.Repo == "" || from.File == "" {
		return ""
	}
	line := r.callSiteLine(from, to)
	if line <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/%s:%d", from.Repo, from.File, line)
}

func (r *flowNarrativeEvidenceResolver) callSiteLine(from, to FlowEndpoint) int {
	if r == nil || r.pool == nil {
		return 0
	}
	callerID := trace.BuildCallerID(from.Repo, from.File, from.Handler)
	calleeID := trace.BuildCallerID(to.Repo, to.File, to.Handler)
	if callerID == "" || calleeID == "" {
		return 0
	}
	cacheKey := callerID + "->" + calleeID
	if line, ok := r.callLineCache[cacheKey]; ok {
		return line
	}
	var line int
	err := r.pool.QueryRow(r.ctx, `
		SELECT line_number
		FROM trace_call_edges
		WHERE caller_id = $1
		  AND callee_id = $2
		  AND line_number IS NOT NULL
		ORDER BY line_number
		LIMIT 1
	`, callerID, calleeID).Scan(&line)
	if err != nil {
		line = 0
	}
	r.callLineCache[cacheKey] = line
	return line
}

func (r *flowNarrativeEvidenceResolver) functionSource(endpoint FlowEndpoint) (flowFunctionSource, bool) {
	if r == nil || r.pool == nil {
		return flowFunctionSource{}, false
	}
	if endpoint.Repo == "" || endpoint.File == "" || endpoint.Handler == "" {
		return flowFunctionSource{}, false
	}
	cacheKey := endpoint.Repo + "|" + endpoint.File + "|" + endpoint.Handler
	if src, ok := r.sourceCache[cacheKey]; ok {
		return src, src.Source != ""
	}
	var startLine int
	var source string
	err := r.pool.QueryRow(r.ctx, `
		SELECT fn.start_line, COALESCE(fn.source_code, '')
		FROM functions fn
		JOIN files f ON f.id = fn.file_id
		JOIN repositories r ON r.id = f.repo_id
		WHERE r.name = $1 AND f.path = $2 AND fn.name = $3
		LIMIT 1
	`, endpoint.Repo, endpoint.File, endpoint.Handler).Scan(&startLine, &source)
	if err != nil || strings.TrimSpace(source) == "" {
		r.sourceCache[cacheKey] = flowFunctionSource{}
		return flowFunctionSource{}, false
	}
	src := flowFunctionSource{StartLine: startLine, Source: source}
	r.sourceCache[cacheKey] = src
	return src, true
}

func extractBranchConditionFromSource(source string, startLine, callLine int) string {
	if strings.TrimSpace(source) == "" || startLine <= 0 || callLine <= 0 {
		return ""
	}
	lines := strings.Split(source, "\n")
	if len(lines) == 0 {
		return ""
	}
	idx := callLine - startLine
	if idx < 0 {
		idx = 0
	}
	if idx >= len(lines) {
		idx = len(lines) - 1
	}
	minIdx := idx - 25
	if minIdx < 0 {
		minIdx = 0
	}
	for i := idx; i >= minIdx; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "if(") ||
			strings.HasPrefix(lower, "if (") ||
			strings.HasPrefix(lower, "else if(") ||
			strings.HasPrefix(lower, "else if (") ||
			strings.Contains(lower, "} else if(") ||
			strings.Contains(lower, "} else if (") ||
			strings.HasPrefix(lower, "switch(") ||
			strings.HasPrefix(lower, "switch (") ||
			strings.HasPrefix(lower, "case ") {
			return normalizeConditionSnippet(line)
		}
	}
	return ""
}

func normalizeConditionSnippet(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(line, "{")
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > 180 {
		line = line[:177] + "..."
	}
	return line
}

func normalizeFlowHops(hops []FlowHop) []FlowHop {
	if len(hops) <= 1 {
		return hops
	}
	seen := make(map[string]bool, len(hops))
	out := make([]FlowHop, 0, len(hops))
	for _, hop := range hops {
		key := flowHopKey(hop)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, hop)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a := out[i]
		b := out[j]
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		ap := flowViaPriority(a.Via)
		bp := flowViaPriority(b.Via)
		if ap != bp {
			return ap < bp
		}
		af := flowEndpointSortKey(a.From)
		bf := flowEndpointSortKey(b.From)
		if af != bf {
			return af < bf
		}
		at := flowEndpointSortKey(a.To)
		bt := flowEndpointSortKey(b.To)
		if at != bt {
			return at < bt
		}
		return flowViaSortKey(a.Via) < flowViaSortKey(b.Via)
	})
	return out
}

func applyStrictFlowMode(roots []FlowEndpoint, hops []FlowHop, maxDepth int) []FlowHop {
	if len(roots) == 0 || len(hops) == 0 {
		return hops
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}

	selected := make([]FlowHop, 0, len(roots)*maxDepth)
	seen := make(map[string]bool)
	for _, root := range roots {
		path := buildNarrativeChainIndices(root, hops, maxDepth)
		for _, idx := range path {
			next := hops[idx]
			hopKey := flowHopKey(next)
			if !seen[hopKey] {
				selected = append(selected, next)
				seen[hopKey] = true
			}
		}
	}
	if len(selected) == 0 {
		return hops
	}
	return normalizeFlowHops(selected)
}

func flowEndpointTraversalKey(ep FlowEndpoint) string {
	repo := strings.ToLower(strings.TrimSpace(ep.Repo))
	handler := strings.TrimSpace(ep.Handler)
	file := strings.TrimSpace(ep.File)
	method := strings.ToUpper(strings.TrimSpace(ep.Method))
	path := strings.ToLower(strings.TrimSpace(ep.Path))

	// Most internal/call hops don't carry endpoint method/path.
	// For strict traversal we need a stable key shared by roots and hops.
	if repo != "" && handler != "" {
		return repo + "|" + file + "|" + handler
	}
	if repo != "" && method != "" && path != "" {
		return repo + "|" + method + "|" + path
	}
	if handler != "" {
		return handler
	}
	if method != "" || path != "" || file != "" {
		return method + "|" + path + "|" + file
	}
	return ""
}

func narrativePathStepBudget(maxDepth int) int {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	budget := maxDepth * 12
	if budget < 24 {
		budget = 24
	}
	if budget > 240 {
		budget = 240
	}
	return budget
}

func buildNarrativeChainIndices(root FlowEndpoint, hops []FlowHop, maxDepth int) []int {
	if len(hops) == 0 {
		return nil
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	maxSteps := narrativePathStepBudget(maxDepth)
	adj := make(map[string][]int)
	for i, hop := range hops {
		fromKey := flowEndpointTraversalKey(hop.From)
		if fromKey == "" {
			continue
		}
		adj[fromKey] = append(adj[fromKey], i)
	}
	for key := range adj {
		indices := adj[key]
		sort.SliceStable(indices, func(i, j int) bool {
			si := narrativeHopScore(hops[indices[i]])
			sj := narrativeHopScore(hops[indices[j]])
			if si == sj {
				return narrativeHopLess(hops[indices[i]], hops[indices[j]])
			}
			return si > sj
		})
		adj[key] = indices
	}

	type state struct {
		key           string
		path          []int
		crossServices int
	}
	rootKey := flowEndpointTraversalKey(root)
	if rootKey == "" {
		return nil
	}
	queue := []state{{key: rootKey, path: nil, crossServices: 0}}
	visited := map[string]int{rootKey + "|0": 0}
	bestPath := make([]int, 0)
	bestScore := -1 << 30
	bestLastEndpointScore := -1

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, idx := range adj[cur.key] {
			hop := hops[idx]
			nextPath := append(append([]int{}, cur.path...), idx)
			if len(nextPath) > maxSteps {
				continue
			}
			nextCross := cur.crossServices
			if isCrossServiceVia(hop.Via) {
				nextCross++
			}
			if nextCross > maxDepth {
				continue
			}
			pathScore := narrativePathScore(nextPath, hops)
			lastEndpointScore := endpointSpecificityScore(hop.To)
			if pathScore > bestScore || (pathScore == bestScore && (len(nextPath) > len(bestPath) || (len(nextPath) == len(bestPath) && lastEndpointScore > bestLastEndpointScore))) {
				bestPath = append([]int{}, nextPath...)
				bestScore = pathScore
				bestLastEndpointScore = lastEndpointScore
			}
			nextKey := flowEndpointTraversalKey(hop.To)
			if nextKey == "" {
				continue
			}
			visitKey := nextKey + "|" + strconv.Itoa(nextCross)
			if prevScore, seen := visited[visitKey]; seen && prevScore >= pathScore {
				continue
			}
			visited[visitKey] = pathScore
			queue = append(queue, state{key: nextKey, path: nextPath, crossServices: nextCross})
		}
	}
	if len(bestPath) > 0 {
		return bestPath
	}
	return nil
}

func narrativePathScore(pathIdx []int, hops []FlowHop) int {
	score := 0
	for _, idx := range pathIdx {
		if idx < 0 || idx >= len(hops) {
			continue
		}
		score += narrativeHopScore(hops[idx])
	}
	score += len(pathIdx) * 10
	return score
}

func narrativeHopScore(hop FlowHop) int {
	score := endpointSpecificityScore(hop.To)
	if isCrossServiceVia(hop.Via) {
		score += 90
	}
	switch strings.ToLower(strings.TrimSpace(hop.Via.Type)) {
	case "call":
		score += 10
	case "http":
		score += 40
	case "sqs":
		score += 34
	case "data":
		score += 22
	}
	return score
}

func narrativeHopLess(a, b FlowHop) bool {
	aCross := isCrossServiceVia(a.Via)
	bCross := isCrossServiceVia(b.Via)
	if aCross != bCross {
		return aCross
	}
	if aCross && a.Via.Type != b.Via.Type {
		return crossServiceTypePriority(a.Via.Type) < crossServiceTypePriority(b.Via.Type)
	}
	aSpec := endpointSpecificityScore(a.To)
	bSpec := endpointSpecificityScore(b.To)
	if aSpec != bSpec {
		return aSpec > bSpec
	}
	af := flowEndpointSortKey(a.From)
	bf := flowEndpointSortKey(b.From)
	if af != bf {
		return af < bf
	}
	at := flowEndpointSortKey(a.To)
	bt := flowEndpointSortKey(b.To)
	if at != bt {
		return at < bt
	}
	return flowViaSortKey(a.Via) < flowViaSortKey(b.Via)
}

func crossServiceTypePriority(viaType string) int {
	switch strings.ToLower(strings.TrimSpace(viaType)) {
	case "http":
		return 0
	case "sqs":
		return 1
	case "data":
		return 2
	default:
		return 3
	}
}

func endpointSpecificityScore(endpoint FlowEndpoint) int {
	score := 0
	path := strings.Trim(endpoint.Path, "/")
	if path != "" {
		segments := strings.Split(path, "/")
		staticSegments := 0
		for _, seg := range segments {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			if strings.HasPrefix(seg, ":") || (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) {
				continue
			}
			staticSegments++
		}
		score += staticSegments * 10
		score += len(segments)
	}
	if endpoint.Handler != "" {
		score += 2
	}
	if endpoint.Line > 0 {
		score++
	}
	if endpoint.Method != "" && endpoint.Method != "REQUEST" {
		score++
	}
	return score
}

func isCrossServiceVia(via FlowVia) bool {
	switch strings.ToLower(strings.TrimSpace(via.Type)) {
	case "http", "sqs", "data":
		return true
	default:
		return false
	}
}

func flowHopKey(hop FlowHop) string {
	return strings.Join([]string{
		strconv.Itoa(hop.Depth),
		flowEndpointSortKey(hop.From),
		flowEndpointSortKey(hop.To),
		flowViaSortKey(hop.Via),
	}, "|")
}

func flowEndpointSortKey(ep FlowEndpoint) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(ep.Repo)),
		strings.ToLower(strings.TrimSpace(normalizeSnapshotFile(ep.File))),
		strings.ToLower(strings.TrimSpace(ep.Handler)),
		canonicalFlowEndpointMethod(ep.Method),
		canonicalFlowEndpointPath(ep.Path),
		strconv.Itoa(ep.Line),
	}, "|")
}

func flowViaSortKey(v FlowVia) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(v.Type)),
		strings.ToUpper(strings.TrimSpace(v.Method)),
		strings.ToLower(strings.TrimSpace(v.Path)),
		strings.ToLower(strings.TrimSpace(v.Queue)),
		strings.ToLower(strings.TrimSpace(v.ClientType)),
		strings.ToLower(strings.TrimSpace(v.Entity)),
		strings.ToLower(strings.TrimSpace(v.Table)),
		strings.ToLower(strings.TrimSpace(v.Access)),
	}, "|")
}

func flowViaPriority(v FlowVia) int {
	switch strings.ToLower(strings.TrimSpace(v.Type)) {
	case "call":
		return 0
	case "http":
		return 1
	case "sqs":
		return 2
	case "data":
		return 3
	default:
		return 4
	}
}

func buildNarrativeChain(root FlowEndpoint, hops []FlowHop, used map[int]bool) []FlowNarrativeStep {
	path := buildNarrativeChainIndices(root, hops, 8)
	if len(path) == 0 {
		return nil
	}
	steps := make([]FlowNarrativeStep, 0, len(path))
	for _, idx := range path {
		if idx < 0 || idx >= len(hops) {
			continue
		}
		used[idx] = true
		h := hops[idx]
		via := h.Via
		steps = append(steps, FlowNarrativeStep{
			Kind: "hop",
			From: h.From,
			Via:  &via,
			To:   h.To,
		})
	}
	return steps
}

func bestCallerForRoot(root FlowEndpoint, callers []FlowEndpoint) *FlowEndpoint {
	if len(callers) == 0 {
		return nil
	}
	var best *FlowEndpoint
	for _, caller := range callers {
		if root.Path != "" && caller.Path != "" && !pathPatternMatches(caller.Path, root.Path) && !pathPatternMatches(root.Path, caller.Path) {
			continue
		}
		if root.Method != "" && root.Method != "REQUEST" && caller.Method != "" && !strings.EqualFold(root.Method, caller.Method) {
			continue
		}
		if best == nil {
			c := caller
			best = &c
			continue
		}
		// Prefer non-snapshot file paths.
		if strings.Contains(best.File, ".codebase-snapshots") && !strings.Contains(caller.File, ".codebase-snapshots") {
			c := caller
			best = &c
		}
	}
	return best
}

func buildSubmitWriteStep(ctx context.Context, pool *pgxpool.Pool, submitEndpoint FlowEndpoint, primaryEntities map[string]bool, entityTables map[string]string) *FlowNarrativeStep {
	if submitEndpoint.Repo == "" || submitEndpoint.File == "" || submitEndpoint.Handler == "" {
		return nil
	}
	callerID := trace.BuildCallerID(submitEndpoint.Repo, submitEndpoint.File, submitEndpoint.Handler)
	if callerID == "" {
		return nil
	}
	// Trace a short depth inside the submit handler to capture real writes.
	accesses := fetchSubmitAccesses(ctx, pool, callerID, entityTables)
	if len(accesses) == 0 {
		return nil
	}
	counts := make(map[string]int)
	labels := make(map[string]string)
	for _, access := range accesses {
		if access.Access != "write" {
			continue
		}
		entity := canonicalEntityName(access.Entity)
		if entity == "" {
			continue
		}
		if primaryEntities != nil && primaryEntities[entity] {
			continue
		}
		counts[entity]++
		if labels[entity] == "" {
			labels[entity] = strings.TrimSpace(access.Entity)
		}
	}
	if len(counts) == 0 {
		return nil
	}
	bestEntity := ""
	bestScore := 0
	for entity, score := range counts {
		if score > bestScore {
			bestScore = score
			bestEntity = entity
		}
	}
	if bestEntity == "" {
		return nil
	}
	label := labels[bestEntity]
	if label == "" {
		label = entityDisplayName(ctx, pool, bestEntity)
	}
	if label == "" {
		label = strings.ToUpper(bestEntity[:1]) + bestEntity[1:]
	}
	via := FlowVia{
		Type:   "data",
		Entity: label,
		Table:  tableForEntity(entityTables, bestEntity),
		Access: "write",
	}
	return &FlowNarrativeStep{
		Kind: "data-store",
		From: submitEndpoint,
		Via:  &via,
		To: FlowEndpoint{
			Repo:    submitEndpoint.Repo,
			Handler: label + " saved",
		},
	}
}

func fetchSubmitAccesses(ctx context.Context, pool *pgxpool.Pool, callerID string, entityTables map[string]string) []dataAccess {
	// Start with direct access on the submit handler.
	accesses := fetchDataAccesses(ctx, pool, []string{callerID}, false, entityTables, nil)
	if len(accesses) > 0 {
		return accesses
	}

	// Fallback: trace a short depth to collect downstream callers and access those.
	tuning := trace.DefaultTraceTuning(2)
	trees := trace.TraceDownstreamWithLimitAndResolveTuning(trace.WithContext(ctx, pool), []string{callerID}, 2, 200, true, tuning)
	callerIDs := collectCallerIDs(trees)
	if len(callerIDs) == 0 {
		return nil
	}
	accesses = fetchDataAccesses(ctx, pool, callerIDs, false, entityTables, nil)
	return accesses
}

func dedupeNarrativeSteps(steps []FlowNarrativeStep) []FlowNarrativeStep {
	if len(steps) < 2 {
		return steps
	}
	seen := make(map[string]bool)
	out := make([]FlowNarrativeStep, 0, len(steps))
	for _, step := range steps {
		key := step.Kind + "|" + flowEndpointKey(step.From) + "|" + flowEndpointKey(step.To) + "|" + step.Condition + "|" + step.Confidence
		if step.Via != nil {
			key += "|" + step.Via.Type + "|" + step.Via.Entity + "|" + step.Via.Path + "|" + step.Via.Queue + "|" + step.Via.Method
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, step)
	}
	return out
}

func pruneInternalHops(roots []FlowEndpoint, hops []FlowHop) []FlowHop {
	if len(hops) == 0 {
		return hops
	}

	callEdges := make(map[string][]string)
	callHopByEdge := make(map[string]FlowHop)
	boundary := make(map[string]bool)
	seedRoots := make(map[string]bool)

	for _, hop := range hops {
		if hop.Via.Type == "call" {
			fromKey := flowEndpointKeyNoLine(hop.From)
			toKey := flowEndpointKeyNoLine(hop.To)
			if fromKey == "" || toKey == "" {
				continue
			}
			callEdges[fromKey] = append(callEdges[fromKey], toKey)
			edgeKey := fromKey + "->" + toKey
			if _, ok := callHopByEdge[edgeKey]; !ok {
				callHopByEdge[edgeKey] = hop
			}
			continue
		}
		if hop.From.Repo != "" || hop.From.Handler != "" || hop.From.Path != "" {
			boundary[flowEndpointKeyNoLine(hop.From)] = true
		}
		if hop.To.Repo != "" || hop.To.Handler != "" || hop.To.Path != "" {
			seedRoots[flowEndpointKeyNoLine(hop.To)] = true
		}
	}

	rootSet := make(map[string]bool)
	for _, root := range roots {
		if key := flowEndpointKeyNoLine(root); key != "" {
			rootSet[key] = true
		}
	}
	for key := range seedRoots {
		rootSet[key] = true
	}

	keepCallEdges := make(map[string]bool)
	if len(boundary) == 0 {
		for edgeKey, hop := range callHopByEdge {
			if rootSet[flowEndpointKeyNoLine(hop.From)] {
				keepCallEdges[edgeKey] = true
			}
		}
	} else {
		type queueItem struct {
			key string
		}
		queue := make([]queueItem, 0, len(rootSet))
		parent := make(map[string]string)
		visited := make(map[string]bool)
		for key := range rootSet {
			visited[key] = true
			queue = append(queue, queueItem{key: key})
		}
		for len(queue) > 0 {
			item := queue[0]
			queue = queue[1:]
			for _, next := range callEdges[item.key] {
				if visited[next] {
					continue
				}
				visited[next] = true
				parent[next] = item.key
				queue = append(queue, queueItem{key: next})
			}
		}
		for node := range boundary {
			if !visited[node] {
				continue
			}
			cur := node
			for {
				prev, ok := parent[cur]
				if !ok {
					break
				}
				edgeKey := prev + "->" + cur
				if _, ok := callHopByEdge[edgeKey]; ok {
					keepCallEdges[edgeKey] = true
				}
				cur = prev
			}
		}
		if len(keepCallEdges) == 0 {
			for edgeKey, hop := range callHopByEdge {
				if rootSet[flowEndpointKeyNoLine(hop.From)] {
					keepCallEdges[edgeKey] = true
				}
			}
		}

		// Ensure each root keeps at least its direct call edges if it has no boundary path.
		for rootKey := range rootSet {
			hasEdge := false
			prefix := rootKey + "->"
			for edgeKey := range keepCallEdges {
				if strings.HasPrefix(edgeKey, prefix) {
					hasEdge = true
					break
				}
			}
			if hasEdge {
				continue
			}
			for _, toKey := range callEdges[rootKey] {
				edgeKey := rootKey + "->" + toKey
				if _, ok := callHopByEdge[edgeKey]; ok {
					keepCallEdges[edgeKey] = true
				}
			}
		}
	}

	out := make([]FlowHop, 0, len(hops))
	for _, hop := range hops {
		if hop.Via.Type != "call" {
			out = append(out, hop)
			continue
		}
		edgeKey := flowEndpointKeyNoLine(hop.From) + "->" + flowEndpointKeyNoLine(hop.To)
		if keepCallEdges[edgeKey] {
			out = append(out, hop)
		}
	}
	return out
}

func flowEndpointKey(endpoint FlowEndpoint) string {
	normalizedFile := strings.ToLower(strings.TrimSpace(normalizeSnapshotFile(endpoint.File)))
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(endpoint.Repo)),
		normalizedFile,
		strings.ToLower(strings.TrimSpace(endpoint.Handler)),
		canonicalFlowEndpointMethod(endpoint.Method),
		canonicalFlowEndpointPath(endpoint.Path),
		strconv.Itoa(endpoint.Line),
	}, "|")
}

func flowEndpointKeyNoLine(endpoint FlowEndpoint) string {
	if endpoint.Repo == "" && endpoint.File == "" && endpoint.Handler == "" && endpoint.Method == "" && endpoint.Path == "" {
		return ""
	}
	normalizedFile := strings.ToLower(strings.TrimSpace(normalizeSnapshotFile(endpoint.File)))
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(endpoint.Repo)),
		normalizedFile,
		strings.ToLower(strings.TrimSpace(endpoint.Handler)),
		canonicalFlowEndpointMethod(endpoint.Method),
		canonicalFlowEndpointPath(endpoint.Path),
	}, "|")
}

func canonicalFlowEndpointMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func canonicalFlowEndpointPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return strings.ToLower(path)
}

func flowEndpointCanonicalIdentityKey(endpoint FlowEndpoint) string {
	return flowEndpointKey(endpoint)
}

func flowEndpointDedupKey(endpoint FlowEndpoint) string {
	normalizedFile := normalizeSnapshotFile(endpoint.File)
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(endpoint.Repo)),
		canonicalFlowEndpointMethod(endpoint.Method),
		canonicalFlowEndpointPath(endpoint.Path),
		strings.ToLower(strings.TrimSpace(endpoint.Handler)),
		strings.ToLower(strings.TrimSpace(normalizedFile)),
	}, "|")
}

func normalizeSnapshotFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	const snapshotPrefix = ".codebase-snapshots/"
	if !strings.HasPrefix(path, snapshotPrefix) {
		return path
	}
	rest := strings.TrimPrefix(path, snapshotPrefix)
	if idx := strings.Index(rest, "/"); idx >= 0 && idx+1 < len(rest) {
		return rest[idx+1:]
	}
	return path
}

func isSnapshotFile(path string) bool {
	return strings.Contains(path, ".codebase-snapshots/")
}

func choosePreferredEndpoint(existing, candidate FlowEndpoint) FlowEndpoint {
	existingSnapshot := isSnapshotFile(existing.File)
	candidateSnapshot := isSnapshotFile(candidate.File)
	if existingSnapshot && !candidateSnapshot {
		return candidate
	}
	if !existingSnapshot && candidateSnapshot {
		return existing
	}
	if existing.Line <= 0 && candidate.Line > 0 {
		return candidate
	}
	if candidate.Line > 0 && existing.Line > 0 && candidate.Line < existing.Line {
		return candidate
	}
	if existing.File == "" && candidate.File != "" {
		return candidate
	}
	return existing
}

func dedupeFlowEndpoints(items []FlowEndpoint) []FlowEndpoint {
	if len(items) <= 1 {
		return items
	}
	indexByKey := make(map[string]int, len(items))
	out := make([]FlowEndpoint, 0, len(items))
	for _, item := range items {
		key := flowEndpointDedupKey(item)
		if idx, ok := indexByKey[key]; ok {
			out[idx] = choosePreferredEndpoint(out[idx], item)
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, item)
	}
	return out
}

func dedupeFlowNarratives(items []FlowNarrative) []FlowNarrative {
	if len(items) <= 1 {
		return items
	}
	indexByKey := make(map[string]int, len(items))
	out := make([]FlowNarrative, 0, len(items))
	for _, item := range items {
		key := flowEndpointDedupKey(item.Root)
		if idx, ok := indexByKey[key]; ok {
			prev := out[idx]
			prevSnapshot := isSnapshotFile(prev.Root.File)
			itemSnapshot := isSnapshotFile(item.Root.File)
			if prevSnapshot && !itemSnapshot {
				out[idx] = item
				continue
			}
			if len(item.Steps) > len(prev.Steps) {
				out[idx] = item
			}
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, item)
	}
	return out
}

func sameEndpoint(a, b FlowEndpoint) bool {
	compared := false
	if a.Repo != "" && b.Repo != "" {
		compared = true
		if !strings.EqualFold(a.Repo, b.Repo) {
			return false
		}
	}
	if a.Handler != "" && b.Handler != "" {
		compared = true
		if a.Handler != b.Handler {
			return false
		}
	}
	if a.File != "" && b.File != "" {
		compared = true
		if a.File != b.File {
			return false
		}
	}
	if a.Method != "" && b.Method != "" {
		compared = true
		if !strings.EqualFold(a.Method, b.Method) {
			return false
		}
	}
	if a.Path != "" && b.Path != "" {
		compared = true
		if !pathPatternMatches(a.Path, b.Path) && !pathPatternMatches(b.Path, a.Path) {
			return false
		}
	}
	return compared
}

func classFromHandler(handler string) string {
	if handler == "" {
		return ""
	}
	if idx := strings.Index(handler, "."); idx >= 0 {
		return handler[:idx]
	}
	return handler
}

func shouldSkipLowSignalInternalCall(from, to FlowEndpoint) bool {
	if strings.TrimSpace(from.Handler) != "_module_" {
		parent := &trace.TreeNode{
			Name: from.Handler,
			Repo: from.Repo,
			File: from.File,
		}
		child := &trace.TreeNode{
			Name: to.Handler,
			Repo: to.Repo,
			File: to.File,
		}
		return trace.IsLowSignalNode(parent, child)
	}

	toHandler := strings.TrimSpace(to.Handler)
	if toHandler == "" {
		return true
	}
	// Decorator/type-level calls emitted from module scope (e.g. NgModule, Component)
	// create noisy hops but don't represent executable business flow.
	if strings.Contains(toHandler, ".") {
		return false
	}
	return looksLikeTypeNameIdentifier(toHandler)
}

func looksLikeTypeNameIdentifier(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	runes := []rune(name)
	if len(runes) == 0 {
		return false
	}
	first := runes[0]
	if first < 'A' || first > 'Z' {
		return false
	}
	for _, r := range runes {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '$' {
			continue
		}
		return false
	}
	return true
}

func findHttpCallersForEndpoints(ctx context.Context, pool *pgxpool.Pool, endpoints []FlowEndpoint, scope searchWorkspaceScope) []FlowEndpoint {
	if len(endpoints) == 0 {
		return nil
	}
	paths := make([]string, 0, len(endpoints))
	seen := make(map[string]bool)
	for _, endpoint := range endpoints {
		if endpoint.Path == "" {
			continue
		}
		key := endpoint.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		paths = append(paths, endpoint.Path)
	}
	if len(paths) == 0 {
		return nil
	}

	query := `
		SELECT h.caller_id, h.http_method, h.url_pattern
		FROM http_client_calls h
		WHERE ($2::bigint[] IS NULL OR h.snapshot_id = ANY($2)
		       OR ($3 AND h.snapshot_id IS NULL))
		  AND (
		       h.url_pattern = ANY($1)
		   OR EXISTS (
			 SELECT 1
			 FROM unnest($1::text[]) AS p(path)
			 WHERE p.path LIKE regexp_replace(
			             regexp_replace(replace(replace(replace(h.url_pattern, '\', '\\'), '%', '\%'), '_', '\_'), '\{[^/]+\}', '%', 'g'),
			             ':[^/]+', '%', 'g'
			           )
		   )
		  )
		LIMIT 2000`
	rows, err := pool.Query(ctx, query, paths, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		return nil
	}
	defer rows.Close()
	callers := make([]FlowEndpoint, 0)
	seenCaller := make(map[string]bool)
	var callerIDs []string
	type callerRow struct {
		callerID string
		method   string
		path     string
	}
	rowsData := make([]callerRow, 0)
	for rows.Next() {
		var callerID, method, path string
		if err := rows.Scan(&callerID, &method, &path); err != nil {
			continue
		}
		if callerID == "" || !httpCallMatchesEndpoints(method, path, endpoints) {
			continue
		}
		key := callerID + ":" + path
		if seenCaller[key] {
			continue
		}
		seenCaller[key] = true
		rowsData = append(rowsData, callerRow{callerID: callerID, method: method, path: path})
		callerIDs = append(callerIDs, callerID)
	}

	if rows.Err() != nil {
		return nil
	}
	rows.Close()
	defLines := fetchCallerDefLines(ctx, pool, callerIDs, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	for _, row := range rowsData {
		repo, file, handler := trace.ParseCallerID(row.callerID)
		if repo == "" || file == "" {
			continue
		}
		line := defLines[row.callerID]
		callers = append(callers, FlowEndpoint{
			Repo:    repo,
			File:    file,
			Handler: handler,
			Line:    line,
			Method:  row.method,
			Path:    row.path,
		})
	}
	return callers
}

func httpCallMatchesEndpoints(method, pattern string, endpoints []FlowEndpoint) bool {
	pattern = normalizeHTTPRoutePattern(pattern)
	if pattern == "" {
		return false
	}
	if !pathHasLiteralSegment(pattern) {
		return false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	for _, endpoint := range endpoints {
		endpointPath := normalizeHTTPRoutePattern(endpoint.Path)
		if endpointPath == "" {
			continue
		}
		if endpointPath != pattern && !pathPatternMatches(pattern, endpointPath) {
			continue
		}
		if endpoint.Method == "" || endpoint.Method == "REQUEST" || method == "" || method == "ANY" {
			return true
		}
		if strings.EqualFold(endpoint.Method, method) {
			return true
		}
	}
	return false
}

func pathPatternMatches(pattern, path string) bool {
	pattern = strings.Trim(normalizeHTTPRoutePattern(pattern), "/")
	path = strings.Trim(normalizeHTTPRoutePattern(path), "/")
	if pattern == "" || path == "" {
		return false
	}
	pSegs := strings.Split(pattern, "/")
	pathSegs := strings.Split(path, "/")
	if len(pSegs) != len(pathSegs) {
		return false
	}
	for i, seg := range pSegs {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, ":") {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if seg != pathSegs[i] {
			return false
		}
	}
	return true
}

func pathHasLiteralSegment(path string) bool {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return false
	}
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if seg == "*" || seg == "**" {
			continue
		}
		if strings.HasPrefix(seg, ":") {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		return true
	}
	return false
}

func normalizeHTTPRoutePattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	if strings.HasPrefix(pattern, "http://") || strings.HasPrefix(pattern, "https://") {
		if parsed, err := url.Parse(pattern); err == nil && parsed.Path != "" {
			pattern = parsed.Path
		}
	}
	if idx := strings.IndexAny(pattern, "?#"); idx >= 0 {
		pattern = pattern[:idx]
	}
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	for strings.Contains(pattern, "//") {
		pattern = strings.ReplaceAll(pattern, "//", "/")
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	pattern = stripHTTPRouteBasePrefix(pattern)
	if len(pattern) > 1 {
		pattern = strings.TrimRight(pattern, "/")
	}
	return pattern
}

func stripHTTPRouteBasePrefix(pattern string) string {
	for {
		if !strings.HasPrefix(pattern, "/:") && !strings.HasPrefix(pattern, "/{") {
			return pattern
		}
		slash := strings.Index(pattern[1:], "/")
		if slash < 0 {
			return pattern
		}
		pattern = pattern[slash+1:]
		if !strings.HasPrefix(pattern, "/") {
			pattern = "/" + pattern
		}
	}
}

func entityDisplayName(ctx context.Context, pool *pgxpool.Pool, entity string) string {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return ""
	}
	row := pool.QueryRow(ctx, `
		SELECT entity_name
		FROM repository_entities
		WHERE lower(entity_name) = lower($1)
		LIMIT 1`, entity)
	var name string
	if err := row.Scan(&name); err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

func entityNameVariants(entity string) []string {
	if entity == "" {
		return nil
	}
	primary := strings.ToLower(entity)
	alt := primary
	if strings.HasSuffix(primary, "s") {
		alt = strings.TrimSuffix(primary, "s")
	} else if len(primary) > 2 {
		alt = primary + "s"
	}
	if alt == primary {
		return []string{primary}
	}
	return []string{primary, alt}
}

func findDataReaderCallers(ctx context.Context, pool *pgxpool.Pool, patterns []string, snapshotIDs []int64, includeLegacy bool) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool)
	var readers []string
	for _, pattern := range patterns {
		query := `
			SELECT caller_id, callee_name
			FROM trace_call_edges
			WHERE callee_name ILIKE $1
			  AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2) OR ($3 AND snapshot_id IS NULL))
			ORDER BY caller_id, callee_name
			LIMIT 1000`
		rows, err := pool.Query(ctx, query, pattern, snapshotIDs, includeLegacy)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var callerID, calleeName string
			if err := rows.Scan(&callerID, &calleeName); err != nil {
				rows.Close()
				return nil, err
			}
			_, method := splitCall(calleeName)
			if method == "" || classifyAccess(method) != "read" {
				continue
			}
			if !seen[callerID] {
				seen[callerID] = true
				readers = append(readers, callerID)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return readers, nil
}

func queryEndpointsForCallers(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, excludeRepo string, seen map[string]bool, snapshotIDs []int64, includeLegacy bool) ([]FlowEndpoint, error) {
	if len(callerIDs) == 0 {
		return nil, nil
	}
	query := `
		SELECT r.name, f.path, e.path, e.method, e.line_number, COALESCE(fn.name, '')
		FROM functions fn
		JOIN files f ON f.id = fn.file_id
		JOIN repositories r ON r.id = f.repo_id
		JOIN endpoints e ON e.handler_function_id = fn.id
		WHERE (r.name || ':' || f.path || ':' || fn.name) = ANY($1)
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3) OR ($4 AND f.snapshot_id IS NULL))
		  AND ($2 = '' OR r.name <> $2)
		ORDER BY r.name, f.path, e.line_number, e.method, e.path
		LIMIT 500`
	rows, err := pool.Query(ctx, query, callerIDs, excludeRepo, snapshotIDs, includeLegacy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []FlowEndpoint
	for rows.Next() {
		var repo, file, path, method, handler string
		var line int
		if err := rows.Scan(&repo, &file, &path, &method, &line, &handler); err != nil {
			return nil, err
		}
		key := repo + ":" + file + ":" + method + ":" + path + ":" + handler
		if seen[key] {
			continue
		}
		seen[key] = true
		endpoints = append(endpoints, FlowEndpoint{
			Repo:    repo,
			File:    file,
			Path:    path,
			Method:  method,
			Line:    line,
			Handler: handler,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return endpoints, nil
}

func splitCall(calleeName string) (string, string) {
	trimmed := strings.TrimSpace(calleeName)
	if trimmed == "" {
		return "", ""
	}
	lastDot := strings.LastIndex(trimmed, ".")
	if lastDot <= 0 || lastDot >= len(trimmed)-1 {
		return "", ""
	}
	receiver := strings.TrimSpace(trimmed[:lastDot])
	method := strings.TrimSpace(trimmed[lastDot+1:])
	receiver = strings.TrimPrefix(receiver, "this.")
	receiver = strings.TrimPrefix(receiver, "this->")
	return receiver, method
}

func resolveEntitiesForReceiver(ctx context.Context, pool *pgxpool.Pool, repo, receiver, method string, cache map[string][]string) []string {
	if repo == "" || receiver == "" {
		return nil
	}
	cacheKey := repo + "|" + receiver
	if cached, ok := cache[cacheKey]; ok {
		return cached
	}
	candidates := repositoryNameCandidates(receiver)
	if len(candidates) == 0 {
		cache[cacheKey] = nil
		return nil
	}
	query := `
		SELECT DISTINCT re.entity_name
		FROM repository_entities re
		JOIN repositories r ON re.repo_id = r.id
		WHERE r.name = $1 AND re.repository_name = ANY($2)
		LIMIT 20`
	rows, err := pool.Query(ctx, query, repo, candidates)
	if err != nil {
		cache[cacheKey] = nil
		return nil
	}
	defer rows.Close()

	var entities []string
	for rows.Next() {
		var entity string
		if err := rows.Scan(&entity); err != nil {
			continue
		}
		entity = strings.ToLower(strings.TrimSpace(entity))
		if entity == "" {
			continue
		}
		entities = append(entities, entity)
	}

	entities = uniqueStrings(entities)
	if len(entities) == 0 {
		cache[cacheKey] = nil
		return nil
	}

	// If method name strongly suggests one entity and that entity exists for this repository,
	// prefer it to avoid broad multi-entity fan-out in non-related flows.
	derived := normalizeEntityName(entityFromMethodName(method))
	if derived != "" {
		derived = strings.ToLower(derived)
		for _, entity := range entities {
			if canonicalEntityName(entity) == canonicalEntityName(derived) {
				cache[cacheKey] = []string{entity}
				return cache[cacheKey]
			}
		}
	}

	cache[cacheKey] = entities
	return cache[cacheKey]
}

func resolveEntityForAccess(ctx context.Context, pool *pgxpool.Pool, repo, receiver, method string, existsCache map[string]bool) string {
	if resolved := resolveEntitiesForReceiver(ctx, pool, repo, receiver, method, map[string][]string{}); len(resolved) > 0 {
		return resolved[0]
	}

	candidates := []string{}
	if base := stripServiceSuffix(receiver); base != "" {
		candidates = append(candidates, normalizeEntityName(base))
	}
	if derived := entityFromMethodName(method); derived != "" {
		candidates = append(candidates, normalizeEntityName(derived))
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if entityExists(ctx, pool, candidate, existsCache) {
			return strings.ToLower(candidate)
		}
	}
	return ""
}

func entityExists(ctx context.Context, pool *pgxpool.Pool, entity string, cache map[string]bool) bool {
	key := strings.ToLower(strings.TrimSpace(entity))
	if key == "" {
		return false
	}
	if val, ok := cache[key]; ok {
		return val
	}
	val := len(repositoryNamesForEntity(ctx, pool, entity)) > 0
	cache[key] = val
	return val
}

func stripServiceSuffix(receiver string) string {
	base := strings.TrimSpace(receiver)
	if base == "" {
		return ""
	}
	suffixes := []string{"Service", "Manager", "Dao", "Repository", "Repo", "Mapper"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(base, suffix) && len(base) > len(suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}

func entityFromMethodName(method string) string {
	if method == "" {
		return ""
	}
	lower := strings.ToLower(method)
	prefixes := []string{"save", "insert", "update", "delete", "create", "remove", "add", "set", "get", "find", "load", "list", "query", "select", "search", "fetch", "count"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) && len(method) > len(prefix) {
			return method[len(prefix):]
		}
	}
	return ""
}

func repositoryNameCandidates(receiver string) []string {
	receiver = strings.TrimSpace(receiver)
	if receiver == "" {
		return nil
	}
	base := receiver
	base = strings.TrimSuffix(base, "Repository")
	base = strings.TrimSuffix(base, "repository")
	base = strings.TrimSuffix(base, "Repo")
	base = strings.TrimSuffix(base, "repo")
	if base == "" {
		base = receiver
	}
	names := []string{
		upperCamel(receiver),
		upperCamel(base) + "Repository",
		upperCamel(base),
	}
	seen := make(map[string]bool)
	var unique []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	return unique
}

func repositoryNamesForEntity(ctx context.Context, pool *pgxpool.Pool, entity string) []string {
	entity = strings.TrimSpace(entity)
	if entity == "" {
		return nil
	}
	query := `
		SELECT DISTINCT repository_name
		FROM repository_entities
		WHERE lower(entity_name) = lower($1)
		LIMIT 200`
	rows, err := pool.Query(ctx, query, entity)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	return names
}

func repositoryReceiverPatterns(repoNames []string) []string {
	if len(repoNames) == 0 {
		return nil
	}
	var patterns []string
	for _, name := range repoNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		patterns = append(patterns, graph.EscapeLike(name)+".%")
		patterns = append(patterns, graph.EscapeLike(lowerCamel(name))+".%")
	}
	return uniqueStrings(patterns)
}

func repositoryReceiverPatternsForEntity(entity string) []string {
	base := strings.TrimSpace(entity)
	if base == "" {
		return nil
	}
	base = upperCamel(base)
	if base == "" {
		return nil
	}

	alt := base
	if strings.HasSuffix(base, "s") {
		alt = strings.TrimSuffix(base, "s")
	} else {
		alt = base + "s"
	}

	suffixes := []string{"Repository", "Repo", "Dao", "Mapper", "Store"}
	var names []string
	for _, suffix := range suffixes {
		names = append(names, base+suffix)
		if alt != base {
			names = append(names, alt+suffix)
		}
	}
	return repositoryReceiverPatterns(uniqueStrings(names))
}

func lowerCamel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = []rune(strings.ToLower(string(runes[0])))[0]
	return string(runes)
}

func upperCamel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func ensureRepositoryEntities(ctx context.Context, pool *pgxpool.Pool, scope searchWorkspaceScope) error {
	missing, err := findReposMissingEntities(ctx, pool, scope)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	return backfillRepositoryEntities(ctx, pool, missing, scope)
}

func findReposMissingEntities(ctx context.Context, pool *pgxpool.Pool, scope searchWorkspaceScope) ([]string, error) {
	query := `
		SELECT DISTINCT r.name
		FROM repositories r
		JOIN files f ON f.repo_id = r.id
		WHERE ` + integrationSnapshotClause("f.snapshot_id", 1, workspaceIncludesLegacy(scope)) + `
		AND NOT EXISTS (
			SELECT 1 FROM repository_entities re
			WHERE re.repo_id = r.id AND re.snapshot_id IS NOT DISTINCT FROM f.snapshot_id
		)`
	rows, err := pool.Query(ctx, query, activeSnapshotIDs(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	missing := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		missing = append(missing, name)
	}
	return missing, rows.Err()
}

func backfillRepositoryEntities(ctx context.Context, pool *pgxpool.Pool, repoNames []string, scope searchWorkspaceScope) error {
	if len(repoNames) == 0 {
		return nil
	}

	type entityMapping struct {
		repoID, fileID     int64
		snapshotID         *int64
		repository, entity string
		line               int
	}
	var mappings []entityMapping
	collect := func(repoID int64, snapshotID *int64, fileID int64, repoName, entity string, line int) {
		if repoName == "" || entity == "" {
			return
		}
		mappings = append(mappings, entityMapping{repoID, fileID, snapshotID, repoName, entity, line})
	}

	// Interfaces
	ifaceRows, err := pool.Query(ctx, `
		SELECT r.id, f.snapshot_id, i.file_id, i.name, i.start_line, i.extends_interfaces
		FROM interfaces i
		JOIN files f ON i.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE r.name = ANY($1) AND `+integrationSnapshotClause("f.snapshot_id", 2, workspaceIncludesLegacy(scope)), repoNames, activeSnapshotIDs(scope))
	if err != nil {
		return err
	}
	{
		for ifaceRows.Next() {
			var repoID, fileID int64
			var snapshotID *int64
			var name string
			var line int
			var raw []byte
			if err := ifaceRows.Scan(&repoID, &snapshotID, &fileID, &name, &line, &raw); err != nil {
				ifaceRows.Close()
				return err
			}
			var extends []string
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &extends); err != nil {
					ifaceRows.Close()
					return err
				}
			}
			for _, spec := range extends {
				if entity, ok := parseRepositoryEntity(spec); ok {
					collect(repoID, snapshotID, fileID, name, entity, line)
				}
			}
		}
		ifaceRows.Close()
		if err := ifaceRows.Err(); err != nil {
			return err
		}
	}

	// Classes (implements repository base)
	classRows, err := pool.Query(ctx, `
		SELECT r.id, f.snapshot_id, c.file_id, c.name, c.start_line, c.implements
		FROM classes c
		JOIN files f ON c.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE r.name = ANY($1) AND `+integrationSnapshotClause("f.snapshot_id", 2, workspaceIncludesLegacy(scope)), repoNames, activeSnapshotIDs(scope))
	if err != nil {
		return err
	}
	{
		for classRows.Next() {
			var repoID, fileID int64
			var snapshotID *int64
			var name string
			var line int
			var raw []byte
			if err := classRows.Scan(&repoID, &snapshotID, &fileID, &name, &line, &raw); err != nil {
				classRows.Close()
				return err
			}
			var impls []string
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &impls); err != nil {
					classRows.Close()
					return err
				}
			}
			for _, spec := range impls {
				if entity, ok := parseRepositoryEntity(spec); ok {
					collect(repoID, snapshotID, fileID, name, entity, line)
				}
			}
		}
		classRows.Close()
		if err := classRows.Err(); err != nil {
			return err
		}
	}
	// Release both query connections before acquiring a connection for writes.
	if len(mappings) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, mapping := range mappings {
		if _, err := tx.Exec(ctx, `
			INSERT INTO repository_entities (repo_id, snapshot_id, file_id, repository_name, entity_name, line_number)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT DO NOTHING
		`, mapping.repoID, mapping.snapshotID, mapping.fileID, mapping.repository, mapping.entity, mapping.line); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func parseRepositoryEntity(typeSpec string) (string, bool) {
	typeSpec = strings.TrimSpace(typeSpec)
	if typeSpec == "" {
		return "", false
	}
	base, args := splitGenericType(typeSpec)
	if base == "" || len(args) == 0 {
		return "", false
	}
	baseName := strings.ToLower(baseNameFromType(base))
	if !isRepositoryBase(baseName) {
		return "", false
	}
	entity := strings.TrimSpace(args[0])
	entity = strings.TrimPrefix(entity, "?")
	entity = strings.TrimSpace(strings.TrimPrefix(entity, "extends"))
	entity = strings.TrimSpace(strings.TrimPrefix(entity, "super"))
	entity = normalizeEntityName(entity)
	if entity == "" {
		return "", false
	}
	return entity, true
}

func isRepositoryBase(base string) bool {
	switch base {
	case "jparepository", "crudrepository", "pagingandsortingrepository", "repository":
		return true
	default:
		return false
	}
}

func normalizeEntityName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, "<"); idx >= 0 {
		raw = raw[:idx]
	}
	if idx := strings.LastIndex(raw, "."); idx >= 0 {
		raw = raw[idx+1:]
	}
	raw = strings.TrimSpace(raw)
	return singularizeEntityName(raw)
}

func singularizeEntityName(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "ies") && len(name) > 3 {
		return name[:len(name)-3] + "y"
	}
	if strings.HasSuffix(lower, "s") && len(name) > 1 && !strings.HasSuffix(lower, "ss") {
		return name[:len(name)-1]
	}
	return name
}

func splitGenericType(typeSpec string) (string, []string) {
	open := strings.Index(typeSpec, "<")
	if open < 0 {
		return strings.TrimSpace(typeSpec), nil
	}
	base := strings.TrimSpace(typeSpec[:open])
	inner := strings.TrimSuffix(typeSpec[open+1:], ">")
	return base, splitGenericArgs(inner)
}

func splitGenericArgs(inner string) []string {
	var args []string
	var current strings.Builder
	depth := 0
	for _, r := range inner {
		switch r {
		case '<':
			depth++
			current.WriteRune(r)
		case '>':
			depth--
			current.WriteRune(r)
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, strings.TrimSpace(current.String()))
	}
	return args
}

func baseNameFromType(typeName string) string {
	typeName = strings.TrimSpace(typeName)
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		return typeName[idx+1:]
	}
	return typeName
}

func classifyAccess(method string) string {
	m := strings.ToLower(method)
	writeExact := map[string]bool{
		"save": true, "saveall": true, "insert": true, "update": true, "delete": true, "deletebyid": true,
		"persist": true, "merge": true, "create": true, "upsert": true, "remove": true, "flush": true,
	}
	readExact := map[string]bool{
		"find": true, "findall": true, "get": true, "load": true, "list": true, "query": true,
		"select": true, "search": true, "fetch": true, "exists": true, "count": true,
	}
	if writeExact[m] {
		return "write"
	}
	if readExact[m] {
		return "read"
	}
	readPrefixes := []string{"find", "get", "load", "list", "query", "select", "search", "fetch", "exists"}
	for _, prefix := range readPrefixes {
		if strings.HasPrefix(m, prefix) {
			return "read"
		}
	}
	writePrefixes := []string{"save", "insert", "update", "delete", "create", "upsert", "remove"}
	for _, prefix := range writePrefixes {
		if strings.HasPrefix(m, prefix) {
			return "write"
		}
	}
	return ""
}
