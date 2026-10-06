package handlers

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/trace"
)

func TestPruneTreeWithStatsReportsClipping(t *testing.T) {
	nodes := []*trace.TreeNode{
		{
			Name: "root",
			Children: []*trace.TreeNode{
				{Name: "child-1"},
				{Name: "child-2"},
				{Name: "child-3"},
			},
		},
	}

	pruned, stats := pruneTreeWithStats(nodes, 2)
	if len(pruned) != 1 {
		t.Fatalf("expected one root node, got %d", len(pruned))
	}
	if stats.AvailableNodes != 4 {
		t.Fatalf("expected availableNodes=4, got %d", stats.AvailableNodes)
	}
	if stats.ReturnedNodes != 2 {
		t.Fatalf("expected returnedNodes=2, got %d", stats.ReturnedNodes)
	}
	if !stats.Truncated {
		t.Fatalf("expected truncated=true")
	}
}

func TestBuildTraceCompletenessCombinesDirections(t *testing.T) {
	completeness := buildTraceCompleteness(
		50,
		trace.LimitStats{},
		tracePruneStats{ReturnedNodes: 50, AvailableNodes: 291, ExactAvailable: true, Truncated: true},
		trace.LimitStats{},
		tracePruneStats{ReturnedNodes: 12, AvailableNodes: 12, ExactAvailable: true, Truncated: false},
	)

	if !completeness.Truncated {
		t.Fatalf("expected completeness.Truncated=true")
	}
	if completeness.AppliedMaxNodes != 50 {
		t.Fatalf("expected appliedMaxNodes=50, got %d", completeness.AppliedMaxNodes)
	}
	if !completeness.Downstream.Truncated {
		t.Fatalf("expected downstream truncated=true")
	}
	if !completeness.Downstream.ExactAvailableNodes {
		t.Fatalf("expected downstream exactAvailableNodes=true")
	}
	if completeness.Upstream.Truncated {
		t.Fatalf("expected upstream truncated=false")
	}
	if len(completeness.TruncationReasons) != 1 {
		t.Fatalf("expected one truncation reason, got %d", len(completeness.TruncationReasons))
	}
}

func TestBuildTraceWarningsIncludesDirectionSpecificMessages(t *testing.T) {
	warnings := buildTraceWarnings(TraceCompleteness{
		Truncated: true,
		Downstream: TraceDirectionCompleteness{
			ReturnedNodes:       50,
			AvailableNodes:      291,
			ExactAvailableNodes: true,
			Truncated:           true,
		},
		Upstream: TraceDirectionCompleteness{
			ReturnedNodes:       24,
			AvailableNodes:      144,
			ExactAvailableNodes: true,
			Truncated:           true,
		},
	})

	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(warnings))
	}
}

func TestBuildTraceCompletenessUsesLowerBoundWhenEngineTruncates(t *testing.T) {
	completeness := buildTraceCompleteness(
		50,
		trace.LimitStats{ReturnedNodes: 50, Truncated: true},
		tracePruneStats{ReturnedNodes: 47, AvailableNodes: 47, ExactAvailable: true, Truncated: false},
		trace.LimitStats{},
		tracePruneStats{ReturnedNodes: 0, AvailableNodes: 0, ExactAvailable: true, Truncated: false},
	)

	if !completeness.Truncated {
		t.Fatalf("expected completeness.Truncated=true")
	}
	if !completeness.Downstream.Truncated {
		t.Fatalf("expected downstream truncated=true")
	}
	if completeness.Downstream.ExactAvailableNodes {
		t.Fatalf("expected downstream exactAvailableNodes=false")
	}
	if completeness.Downstream.AvailableNodes != 47 {
		t.Fatalf("expected downstream lower-bound availableNodes=47, got %d", completeness.Downstream.AvailableNodes)
	}
	if len(completeness.TruncationReasons) != 1 {
		t.Fatalf("expected one truncation reason, got %d", len(completeness.TruncationReasons))
	}
}

func TestBuildTraceWarningsUsesLowerBoundLanguageWhenExactTotalUnknown(t *testing.T) {
	warnings := buildTraceWarnings(TraceCompleteness{
		Truncated: true,
		Downstream: TraceDirectionCompleteness{
			ReturnedNodes:       47,
			AvailableNodes:      47,
			ExactAvailableNodes: false,
			Truncated:           true,
		},
	})

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	expected := "Downstream trace is clipped. Showing 47 nodes; more are available, but the exact total is unknown at this maxNodes setting."
	if warnings[0] != expected {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestAppendIntegrationTraceChildrenAllowsBoundaryPastMaxDepth(t *testing.T) {
	parent := &trace.TreeNode{
		Name:  "ResourceClient.load",
		Repo:  "resource-ui",
		Depth: 4,
	}

	children := appendIntegrationTraceChildren(parent, []FunctionIntegration{
		{
			Method:        "GET",
			Path:          "/api/resources/{id}",
			TargetRepo:    "resource-api",
			TargetFile:    "Controllers/ResourceController.cs",
			TargetHandler: "ResourceController.Get",
			Resolution:    "resolved",
		},
	}, 4)

	if len(children) != 1 {
		t.Fatalf("expected 1 integration child, got %d", len(children))
	}
	if children[0].Name != "[GET /api/resources/{id}]" {
		t.Fatalf("unexpected integration node: %+v", children[0])
	}
	if len(children[0].Children) != 1 || children[0].Children[0].Name != "→ ResourceController.Get [resource-api]" {
		t.Fatalf("expected handler child past max depth, got %+v", children[0].Children)
	}
}
