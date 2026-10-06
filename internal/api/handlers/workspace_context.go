package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type ResponseWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ResponseRepoContext = graph.WorkspaceSnapshotRef

func workspaceIDFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("workspaceId")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Tirion-Workspace")); v != "" {
		return v
	}
	return graph.DefaultWorkspaceSlug
}

func responseWorkspace(ws *graph.Workspace) ResponseWorkspace {
	if ws == nil {
		return ResponseWorkspace{ID: graph.DefaultWorkspaceSlug, Name: "Default Main"}
	}
	return ResponseWorkspace{ID: ws.Slug, Name: ws.Name}
}

func (h *Handlers) resolveWorkspaceScope(workspaceID string, contexts ...context.Context) (*graph.Workspace, searchWorkspaceScope, error) {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	ws, err := h.storage.WithQueryContext(ctx).ResolveWorkspace(workspaceID)
	if err != nil {
		return nil, searchWorkspaceScope{}, err
	}
	scope, err := h.searchWorkspaceScope(ws, ctx)
	if err != nil {
		return nil, searchWorkspaceScope{}, err
	}
	return ws, scope, nil
}

func workspaceAllowsSnapshot(snapshotID *int64, scope searchWorkspaceScope) bool {
	if !scope.EnforceSnapshots {
		return true
	}
	if snapshotID == nil {
		return workspaceIncludesLegacy(scope)
	}
	return scope.ActiveSnapshots[*snapshotID]
}

func workspaceIncludesLegacy(scope searchWorkspaceScope) bool {
	return scope.IsDefault && scope.Workspace.ID == graph.DefaultWorkspaceSlug
}

func activeSnapshotIDs(scope searchWorkspaceScope) []int64 {
	if !scope.EnforceSnapshots {
		return nil
	}
	ids := make([]int64, 0, len(scope.ActiveSnapshots))
	for id := range scope.ActiveSnapshots {
		ids = append(ids, id)
	}
	return ids
}

func snapshotFilterForScope(scope searchWorkspaceScope) graph.SnapshotFilter {
	return graph.SnapshotFilter{
		SnapshotIDs:   activeSnapshotIDs(scope),
		IncludeLegacy: workspaceIncludesLegacy(scope),
	}
}

func (h *Handlers) workspaceCallerIDAllowMap(ctx context.Context, callerIDs []string, scope searchWorkspaceScope) map[string]bool {
	out := make(map[string]bool, len(callerIDs))
	seen := make(map[string]bool, len(callerIDs))
	var unique []string
	for _, id := range callerIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
		if !scope.EnforceSnapshots {
			out[id] = true
		}
	}
	if len(unique) == 0 || !scope.EnforceSnapshots {
		return out
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT
			r.name || ':' || f.path || ':' || fn.name AS caller_id,
			f.snapshot_id
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE (r.name || ':' || f.path || ':' || fn.name) = ANY($1)
	`, unique)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var callerID string
		var snapshotID *int64
		if err := rows.Scan(&callerID, &snapshotID); err != nil {
			continue
		}
		if workspaceAllowsSnapshot(snapshotID, scope) {
			out[callerID] = true
		}
	}
	rows, err = h.storage.Pool().Query(ctx, `
		SELECT
			r.name || ':' || f.path || ':' || u.caller_function AS caller_id,
			f.snapshot_id
		FROM graphql_operation_usages u
		JOIN files f ON u.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE (r.name || ':' || f.path || ':' || u.caller_function) = ANY($1)
	`, unique)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var callerID string
		var snapshotID *int64
		if err := rows.Scan(&callerID, &snapshotID); err != nil {
			continue
		}
		if workspaceAllowsSnapshot(snapshotID, scope) {
			out[callerID] = true
		}
	}
	return out
}

func (h *Handlers) filterCallerIDsByWorkspace(ctx context.Context, callerIDs []string, scope searchWorkspaceScope) []string {
	if len(callerIDs) == 0 || !scope.EnforceSnapshots {
		return callerIDs
	}
	allowed := h.workspaceCallerIDAllowMap(ctx, callerIDs, scope)
	out := make([]string, 0, len(callerIDs))
	for _, id := range callerIDs {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out
}

func (h *Handlers) filterExecutionRootResolutionByWorkspace(ctx context.Context, resolution executionRootResolution, scope searchWorkspaceScope) executionRootResolution {
	if len(resolution.Roots) == 0 || !scope.EnforceSnapshots {
		return resolution
	}
	callerIDs := make([]string, 0, len(resolution.Roots))
	for _, root := range resolution.Roots {
		callerIDs = append(callerIDs, root.CallerID)
	}
	allowed := h.workspaceCallerIDAllowMap(ctx, callerIDs, scope)
	filtered := resolution
	filtered.Roots = filtered.Roots[:0]
	for _, root := range resolution.Roots {
		if allowed[root.CallerID] {
			filtered.Roots = append(filtered.Roots, root)
		}
	}
	return filtered
}

func (h *Handlers) filterTraceTreeByWorkspace(ctx context.Context, nodes []*trace.TreeNode, scope searchWorkspaceScope) []*trace.TreeNode {
	if len(nodes) == 0 || !scope.EnforceSnapshots {
		return nodes
	}
	callerIDs := collectTraceCallerIDs(nodes, 10000)
	allowed := h.workspaceCallerIDAllowMap(ctx, callerIDs, scope)
	return filterTraceNodesByAllowedCallerID(nodes, allowed)
}

func filterTraceNodesByAllowedCallerID(nodes []*trace.TreeNode, allowed map[string]bool) []*trace.TreeNode {
	out := make([]*trace.TreeNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		children := filterTraceNodesByAllowedCallerID(node.Children, allowed)
		node.Children = children
		if node.CallerID == "" || allowed[node.CallerID] || len(children) > 0 {
			out = append(out, node)
		}
	}
	return out
}

func (h *Handlers) filterFlowEndpointsByWorkspace(ctx context.Context, endpoints []FlowEndpoint, scope searchWorkspaceScope) []FlowEndpoint {
	if len(endpoints) == 0 || !scope.EnforceSnapshots {
		return endpoints
	}
	callerIDs := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if callerID := flowEndpointCallerID(endpoint); callerID != "" {
			callerIDs = append(callerIDs, callerID)
		}
	}
	allowed := h.workspaceCallerIDAllowMap(ctx, callerIDs, scope)
	out := make([]FlowEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		callerID := flowEndpointCallerID(endpoint)
		if callerID == "" || allowed[callerID] {
			out = append(out, endpoint)
		}
	}
	return out
}

func (h *Handlers) filterFlowHopsByWorkspace(ctx context.Context, hops []FlowHop, scope searchWorkspaceScope) []FlowHop {
	if len(hops) == 0 || !scope.EnforceSnapshots {
		return hops
	}
	callerIDs := make([]string, 0, len(hops)*2)
	for _, hop := range hops {
		if callerID := flowEndpointCallerID(hop.From); callerID != "" {
			callerIDs = append(callerIDs, callerID)
		}
		if callerID := flowEndpointCallerID(hop.To); callerID != "" {
			callerIDs = append(callerIDs, callerID)
		}
	}
	allowed := h.workspaceCallerIDAllowMap(ctx, callerIDs, scope)
	out := make([]FlowHop, 0, len(hops))
	for _, hop := range hops {
		fromID := flowEndpointCallerID(hop.From)
		toID := flowEndpointCallerID(hop.To)
		fromOK := fromID == "" || allowed[fromID]
		toOK := toID == "" || allowed[toID]
		if fromOK && toOK {
			out = append(out, hop)
		}
	}
	return out
}

func flowEndpointCallerID(endpoint FlowEndpoint) string {
	if strings.TrimSpace(endpoint.Repo) == "" || strings.TrimSpace(endpoint.File) == "" || strings.TrimSpace(endpoint.Handler) == "" {
		return ""
	}
	return trace.BuildCallerID(endpoint.Repo, endpoint.File, endpoint.Handler)
}
