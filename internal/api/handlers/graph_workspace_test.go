package handlers

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestGraphResponseForScopeFiltersInactiveSnapshots(t *testing.T) {
	t.Parallel()

	active := int64(11)
	inactive := int64(12)
	scope := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "release-workspace"},
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{active: true},
	}

	nodes := []GraphNode{
		{ID: "active-a", Type: "function", Name: "ActiveA", Properties: map[string]interface{}{"snapshot_id": active}},
		{ID: "active-b", Type: "function", Name: "ActiveB", Properties: map[string]interface{}{"snapshot_id": float64(active)}},
		{ID: "inactive", Type: "function", Name: "Inactive", Properties: map[string]interface{}{"snapshot_id": inactive}},
	}
	edges := []GraphEdge{
		{Source: "active-a", Target: "active-b", Type: "CALLS", Properties: map[string]interface{}{"snapshot_id": active}},
		{Source: "active-a", Target: "inactive", Type: "CALLS", Properties: map[string]interface{}{"snapshot_id": active}},
		{Source: "active-a", Target: "active-b", Type: "CALLS", Properties: map[string]interface{}{"snapshot_id": inactive}},
	}

	resp := graphResponseForScope(scope, nodes, edges)
	if len(resp.Nodes) != 2 {
		t.Fatalf("expected only active nodes, got %#v", resp.Nodes)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("expected only active edge between active nodes, got %#v", resp.Edges)
	}
}

func TestGraphResponseForScopeKeepsLegacyOnlyForDefaultWorkspace(t *testing.T) {
	t.Parallel()

	legacyNode := GraphNode{ID: "legacy", Type: "function", Name: "Legacy", Properties: map[string]interface{}{}}
	nonDefault := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "release-workspace"},
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{},
	}
	if got := graphResponseForScope(nonDefault, []GraphNode{legacyNode}, nil); len(got.Nodes) != 0 {
		t.Fatalf("expected non-default workspace to hide legacy graph nodes, got %#v", got.Nodes)
	}

	defaultScope := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: graph.DefaultWorkspaceSlug},
		EnforceSnapshots: true,
		IsDefault:        true,
		ActiveSnapshots:  map[int64]bool{},
	}
	if got := graphResponseForScope(defaultScope, []GraphNode{legacyNode}, nil); len(got.Nodes) != 1 {
		t.Fatalf("expected default workspace to keep legacy graph nodes, got %#v", got.Nodes)
	}
}
