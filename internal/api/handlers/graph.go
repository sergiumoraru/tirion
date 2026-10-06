package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type GraphNode struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Name       string                 `json:"name"`
	Properties map[string]interface{} `json:"properties"`
}

type GraphEdge struct {
	Source     string                 `json:"source"`
	Target     string                 `json:"target"`
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

type GraphResponse struct {
	Workspace   ResponseWorkspace     `json:"workspace"`
	RepoContext []ResponseRepoContext `json:"repoContext,omitempty"`
	Center      *GraphNode            `json:"center,omitempty"`
	Nodes       []GraphNode           `json:"nodes"`
	Edges       []GraphEdge           `json:"edges"`
	Warnings    []string              `json:"warnings,omitempty"`
}

type GraphPathResponse struct {
	Workspace   ResponseWorkspace     `json:"workspace"`
	RepoContext []ResponseRepoContext `json:"repoContext,omitempty"`
	Paths       []GraphResponse       `json:"paths"`
	Warnings    []string              `json:"warnings,omitempty"`
}

func (h *Handlers) FindPath(w http.ResponseWriter, r *http.Request) {
	_, scope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	query := r.URL.Query()
	from := strings.TrimSpace(query.Get("from"))
	to := strings.TrimSpace(query.Get("to"))
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing from/to", nil)
		return
	}

	fromType := strings.ToLower(query.Get("fromType"))
	if fromType == "" {
		fromType = "function"
	}
	toType := strings.ToLower(query.Get("toType"))
	if toType == "" {
		toType = "function"
	}
	if fromType != "function" || toType != "function" {
		writeError(w, http.StatusNotImplemented, "GRAPH_PATH_UNSUPPORTED", "graph path currently supports function-to-function SQL paths only", nil)
		return
	}

	if _, err := strconv.ParseInt(from, 10, 64); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", fmt.Sprintf("from must be a numeric function id, got %q", from), map[string]any{"parameter": "from"})
		return
	}
	if _, err := strconv.ParseInt(to, 10, 64); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", fmt.Sprintf("to must be a numeric function id, got %q", to), map[string]any{"parameter": "to"})
		return
	}
	maxDepth, err := queryLimit(r, "maxDepth", 5, 8)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"parameter": "maxDepth"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	nodes, edges, err := h.fetchFunctionPathSQL(ctx, from, to, maxDepth, activeSnapshotIDs(scope))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "graph query timed out", nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	resp := GraphPathResponse{
		Workspace:   scope.Workspace,
		RepoContext: scope.RepoContext,
		Paths:       []GraphResponse{graphResponseForScope(scope, nodes, edges)},
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) fetchFunctionPathSQL(ctx context.Context, fromID, toID string, maxDepth int, snapshotIDs []int64) ([]GraphNode, []GraphEdge, error) {
	startID, err := strconv.ParseInt(fromID, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid from id: %s", fromID)
	}
	endID, err := strconv.ParseInt(toID, 10, 64)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid to id: %s", toID)
	}
	if maxDepth < 1 {
		maxDepth = 1
	}

	if startID == endID {
		nodesMap, err := h.fetchFunctionNodesSQL(ctx, []int64{startID}, snapshotIDs)
		if err != nil {
			return nil, nil, err
		}
		nodes := make([]GraphNode, 0, len(nodesMap))
		for _, node := range nodesMap {
			nodes = append(nodes, node)
		}
		return nodes, []GraphEdge{}, nil
	}

	rows, err := h.storage.Pool().Query(ctx, `
		WITH RECURSIVE walk AS (
			SELECT caller_function_id,
			       callee_function_id,
			       ARRAY[caller_function_id, callee_function_id] AS path,
			       1 AS depth
			FROM trace_call_edges
			WHERE caller_function_id = $1
			  AND callee_function_id IS NOT NULL
			  AND ($4::bigint[] IS NULL OR snapshot_id = ANY($4))
			UNION ALL
			SELECT w.caller_function_id,
			       tce.callee_function_id,
			       w.path || tce.callee_function_id,
			       w.depth + 1
			FROM walk w
			JOIN trace_call_edges tce
			  ON tce.caller_function_id = w.callee_function_id
			WHERE w.depth < $3
			  AND tce.callee_function_id IS NOT NULL
			  AND ($4::bigint[] IS NULL OR tce.snapshot_id = ANY($4))
			  AND NOT tce.callee_function_id = ANY(w.path)
		)
		SELECT path
		FROM walk
		WHERE path[array_length(path, 1)] = $2
		LIMIT 1
	`, startID, endID, maxDepth, snapshotIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return []GraphNode{}, []GraphEdge{}, nil
	}

	var path []int64
	if err := rows.Scan(&path); err != nil {
		return nil, nil, err
	}

	nodesMap, err := h.fetchFunctionNodesSQL(ctx, uniqueInt64(path), snapshotIDs)
	if err != nil {
		return nil, nil, err
	}

	nodes := make([]GraphNode, 0, len(nodesMap))
	for _, node := range nodesMap {
		nodes = append(nodes, node)
	}

	edges := make([]GraphEdge, 0, len(path)-1)
	for i := 0; i < len(path)-1; i++ {
		edges = append(edges, GraphEdge{
			Source: strconv.FormatInt(path[i], 10),
			Target: strconv.FormatInt(path[i+1], 10),
			Type:   "CALLS",
		})
	}

	return nodes, edges, nil
}

func (h *Handlers) fetchFunctionNodesSQL(ctx context.Context, ids []int64, snapshotIDs []int64) (map[int64]GraphNode, error) {
	nodes := map[int64]GraphNode{}
	if len(ids) == 0 {
		return nodes, nil
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT f.id, f.name, f.start_line, f.end_line, f.is_exported, f.is_async,
		       fi.path, r.name, fi.snapshot_id
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE f.id = ANY($1)
		  AND ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2))
	`, ids, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var name, filePath, repoName string
		var startLine, endLine int
		var isExported, isAsync bool
		var snapshotID *int64
		if err := rows.Scan(&id, &name, &startLine, &endLine, &isExported, &isAsync, &filePath, &repoName, &snapshotID); err != nil {
			continue
		}
		props := map[string]interface{}{
			"file":        filePath,
			"repo":        repoName,
			"start_line":  startLine,
			"end_line":    endLine,
			"is_exported": isExported,
			"is_async":    isAsync,
		}
		if snapshotID != nil {
			props["snapshot_id"] = *snapshotID
		}
		nodes[id] = GraphNode{
			ID:         strconv.FormatInt(id, 10),
			Type:       "function",
			Name:       name,
			Properties: props,
		}
	}

	return nodes, nil
}

func graphResponseForScope(scope searchWorkspaceScope, nodes []GraphNode, edges []GraphEdge) GraphResponse {
	nodes, edges = filterGraphByWorkspace(scope, nodes, edges)
	return GraphResponse{
		Workspace:   scope.Workspace,
		RepoContext: scope.RepoContext,
		Nodes:       nodes,
		Edges:       edges,
	}
}

func filterGraphByWorkspace(scope searchWorkspaceScope, nodes []GraphNode, edges []GraphEdge) ([]GraphNode, []GraphEdge) {
	if !scope.EnforceSnapshots {
		return nodes, edges
	}
	allowedNodes := make(map[string]bool, len(nodes))
	filteredNodes := make([]GraphNode, 0, len(nodes))
	for _, node := range nodes {
		if !graphNodeAllowedInWorkspace(node, scope) {
			continue
		}
		allowedNodes[node.ID] = true
		filteredNodes = append(filteredNodes, node)
	}
	filteredEdges := make([]GraphEdge, 0, len(edges))
	for _, edge := range edges {
		if !allowedNodes[edge.Source] || !allowedNodes[edge.Target] {
			continue
		}
		if snapshotID := snapshotIDFromProperties(edge.Properties); !workspaceAllowsSnapshot(snapshotID, scope) {
			continue
		}
		filteredEdges = append(filteredEdges, edge)
	}
	return filteredNodes, filteredEdges
}

func graphNodeAllowedInWorkspace(node GraphNode, scope searchWorkspaceScope) bool {
	return workspaceAllowsSnapshot(snapshotIDFromProperties(node.Properties), scope)
}

func snapshotIDFromProperties(props map[string]interface{}) *int64 {
	if props == nil {
		return nil
	}
	value, ok := props["snapshot_id"]
	if !ok {
		value = props["snapshotId"]
	}
	switch v := value.(type) {
	case int:
		id := int64(v)
		return &id
	case int32:
		id := int64(v)
		return &id
	case int64:
		id := v
		return &id
	case float64:
		id := int64(v)
		if float64(id) == v {
			return &id
		}
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return &parsed
		}
	}
	return nil
}

func uniqueInt64(items []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

// GetRepoDependencies returns a graph of repos with HTTP and SQS edges between them
func (h *Handlers) GetRepoDependencies(w http.ResponseWriter, r *http.Request) {
	_, scope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	snapshotIDs := activeSnapshotIDs(scope)
	if !scope.EnforceSnapshots {
		if cached, ok := h.repoDependenciesFromCache(); ok {
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	// Get all repos with stats
	repoCtx, repoCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer repoCancel()

	repoRows, err := h.storage.Pool().Query(repoCtx, `
		SELECT r.id, r.name,
		       COALESCE((
		         SELECT COUNT(*)
		         FROM files f
		         WHERE f.repo_id = r.id
		           AND ($1::bigint[] IS NULL OR f.snapshot_id = ANY($1))
		       ), 0) as file_count,
		       COALESCE((
		         SELECT COUNT(*)
		         FROM functions fn
		         JOIN files ff ON ff.id = fn.file_id
		         WHERE ff.repo_id = r.id
		           AND ($1::bigint[] IS NULL OR ff.snapshot_id = ANY($1))
		       ), 0) as function_count,
		       COALESCE((
		         SELECT COUNT(*)
		         FROM endpoints e
		         JOIN files ef ON ef.id = e.file_id
		         WHERE ef.repo_id = r.id
		           AND ($1::bigint[] IS NULL OR ef.snapshot_id = ANY($1))
		       ), 0) as endpoint_count
		FROM repositories r
		WHERE $1::bigint[] IS NULL
		   OR EXISTS (
		       SELECT 1
		       FROM repo_snapshots rs
		       WHERE rs.repo_id = r.id
		         AND rs.id = ANY($1)
		   )
		ORDER BY function_count DESC
	`, snapshotIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	defer repoRows.Close()

	nodes := []GraphNode{}
	repoNames := map[int]string{}

	for repoRows.Next() {
		var id int
		var name string
		var fileCount, functionCount, endpointCount int
		if err := repoRows.Scan(&id, &name, &fileCount, &functionCount, &endpointCount); err != nil {
			continue
		}
		repoNames[id] = name
		nodes = append(nodes, GraphNode{
			ID:   strconv.Itoa(id),
			Type: "repo",
			Name: name,
			Properties: map[string]interface{}{
				"files":     fileCount,
				"functions": functionCount,
				"endpoints": endpointCount,
			},
		})
	}

	edges := []GraphEdge{}
	edgeRows, err := h.storage.Pool().Query(r.Context(), repoDependencyEdgesQuery(scope.EnforceSnapshots), snapshotIDs)
	if err == nil {
		defer edgeRows.Close()
		for edgeRows.Next() {
			var sourceID, targetID, count int
			var edgeType, queueName string
			if err := edgeRows.Scan(&sourceID, &targetID, &edgeType, &queueName, &count); err != nil {
				continue
			}
			props := map[string]interface{}{"count": count}
			if queueName != "" {
				props["queue"] = queueName
			}
			edges = append(edges, GraphEdge{
				Source:     strconv.Itoa(sourceID),
				Target:     strconv.Itoa(targetID),
				Type:       edgeType,
				Properties: props,
			})
		}
	}

	// Filter to only repos that have connections
	connectedRepos := map[string]bool{}
	for _, edge := range edges {
		connectedRepos[edge.Source] = true
		connectedRepos[edge.Target] = true
	}

	filteredNodes := []GraphNode{}
	for _, node := range nodes {
		if connectedRepos[node.ID] {
			filteredNodes = append(filteredNodes, node)
		}
	}

	resp := GraphResponse{
		Workspace:   scope.Workspace,
		RepoContext: scope.RepoContext,
		Nodes:       filteredNodes,
		Edges:       edges,
	}
	if scope.EnforceSnapshots && len(edges) == 0 {
		resp.Warnings = append(resp.Warnings, "No snapshot-scoped repo dependency edges are available for this workspace; refresh HTTP/SQS extraction for this workspace before relying on the repo graph.")
	}
	if !scope.EnforceSnapshots {
		h.storeRepoDependenciesCache(resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

const repoDependenciesCacheTTL = 2 * time.Minute

func repoDependencyEdgesQuery(workspaceScoped bool) string {
	if !workspaceScoped {
		return `
			SELECT source_repo_id, target_repo_id, edge_type, queue_name, count
			FROM repo_dependency_edges
			ORDER BY count DESC, source_repo_id, target_repo_id, edge_type, queue_name
		`
	}
	return `
		WITH http_edges AS (
			SELECT
				h.repo_id AS source_repo_id,
				e.repo_id AS target_repo_id,
				'HTTP_CALLS'::text AS edge_type,
				''::text AS queue_name,
				COUNT(*)::int AS count
			FROM http_client_calls h
			JOIN endpoints e
			  ON h.repo_id <> e.repo_id
			 AND (
			   UPPER(COALESCE(NULLIF(h.http_method, ''), 'REQUEST')) = UPPER(COALESCE(NULLIF(e.method_canonical, ''), NULLIF(e.method, ''), 'REQUEST'))
			   OR UPPER(COALESCE(NULLIF(h.http_method, ''), 'REQUEST')) = 'REQUEST'
			   OR UPPER(COALESCE(NULLIF(e.method_canonical, ''), NULLIF(e.method, ''), 'REQUEST')) = 'REQUEST'
			 )
			 AND LOWER(TRIM(BOTH '/' FROM COALESCE(NULLIF(h.url_pattern, ''), ''))) =
			     LOWER(TRIM(BOTH '/' FROM COALESCE(NULLIF(e.path_canonical, ''), NULLIF(e.path, ''), '')))
			JOIN files ef ON ef.id = e.file_id
			WHERE h.snapshot_id = ANY($1)
			  AND ef.snapshot_id = ANY($1)
			  AND COALESCE(h.url_pattern, '') LIKE '/%'
			GROUP BY h.repo_id, e.repo_id
		),
		queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases
			WHERE snapshot_id = ANY($1)
			  AND COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
		),
		queue_consumers AS (
			SELECT repo_id, queue_name
			FROM sqs_consumers
			WHERE snapshot_id = ANY($1)
			  AND COALESCE(queue_name, '') <> ''

			UNION ALL

			SELECT t.repo_id, COALESCE(t.resource_name, '') AS queue_name
			FROM azure_function_triggers t
			JOIN files tf ON tf.id = t.file_id
			WHERE tf.snapshot_id = ANY($1)
			  AND t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			  AND COALESCE(t.resource_name, '') <> ''
		),
		sqs_edges AS (
			SELECT
				p.repo_id AS source_repo_id,
				c.repo_id AS target_repo_id,
				'SQS'::text AS edge_type,
				p.queue_name AS queue_name,
				COUNT(*)::int AS count
			FROM sqs_producers p
			JOIN queue_consumers c
			  ON p.repo_id <> c.repo_id
			 AND (
			   LOWER(TRIM(BOTH '%' FROM p.queue_name)) = LOWER(TRIM(BOTH '%' FROM c.queue_name))
			   OR EXISTS (
			     SELECT 1
			     FROM queue_aliases qa
			     WHERE (
			       qa.alias_key = LOWER(TRIM(BOTH '%' FROM p.queue_name))
			       AND qa.alias_value = LOWER(TRIM(BOTH '%' FROM c.queue_name))
			     ) OR (
			       qa.alias_value = LOWER(TRIM(BOTH '%' FROM p.queue_name))
			       AND qa.alias_key = LOWER(TRIM(BOTH '%' FROM c.queue_name))
			     )
			   )
			   OR EXISTS (
			     SELECT 1
			     FROM queue_aliases producer_alias
			     JOIN queue_aliases consumer_alias
			       ON producer_alias.alias_value = consumer_alias.alias_value
			     WHERE (
			       producer_alias.alias_key = LOWER(TRIM(BOTH '%' FROM p.queue_name))
			       OR producer_alias.alias_value = LOWER(TRIM(BOTH '%' FROM p.queue_name))
			     )
			       AND (
			         consumer_alias.alias_key = LOWER(TRIM(BOTH '%' FROM c.queue_name))
			         OR consumer_alias.alias_value = LOWER(TRIM(BOTH '%' FROM c.queue_name))
			       )
			   )
			 )
			WHERE p.snapshot_id = ANY($1)
			GROUP BY p.repo_id, c.repo_id, p.queue_name
		)
		SELECT source_repo_id, target_repo_id, edge_type, queue_name, count
		FROM http_edges
		UNION ALL
		SELECT source_repo_id, target_repo_id, edge_type, queue_name, count
		FROM sqs_edges
		ORDER BY count DESC, source_repo_id, target_repo_id, edge_type, queue_name
	`
}

func (h *Handlers) repoDependenciesFromCache() (GraphResponse, bool) {
	h.repoDepsMu.RLock()
	defer h.repoDepsMu.RUnlock()
	if h.repoDepsCacheAt.IsZero() || time.Since(h.repoDepsCacheAt) > repoDependenciesCacheTTL {
		return GraphResponse{}, false
	}
	return h.repoDepsCache, true
}

func (h *Handlers) storeRepoDependenciesCache(resp GraphResponse) {
	h.repoDepsMu.Lock()
	h.repoDepsCache = resp
	h.repoDepsCacheAt = time.Now()
	h.repoDepsMu.Unlock()
}

func (h *Handlers) invalidateRepoDependenciesCache() {
	h.repoDepsMu.Lock()
	h.repoDepsCache = GraphResponse{}
	h.repoDepsCacheAt = time.Time{}
	h.repoDepsMu.Unlock()
}
