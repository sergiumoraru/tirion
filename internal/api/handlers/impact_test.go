package handlers

import (
	"fmt"
	"testing"

	"github.com/sergiumoraru/tirion/internal/trace"
)

func TestBuildImpactCompletenessCombinesDirections(t *testing.T) {
	completeness := buildImpactCompleteness(
		50,
		trace.LimitStats{},
		tracePruneStats{ReturnedNodes: 50, AvailableNodes: 144, ExactAvailable: true, Truncated: true},
		trace.LimitStats{},
		tracePruneStats{ReturnedNodes: 24, AvailableNodes: 24, ExactAvailable: true, Truncated: false},
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

func TestBuildImpactWarningsIncludesDirectionSpecificMessages(t *testing.T) {
	warnings := buildImpactWarnings(ImpactCompleteness{
		Truncated: true,
		Downstream: ImpactDirectionCompleteness{
			ReturnedNodes:       50,
			AvailableNodes:      144,
			ExactAvailableNodes: true,
			Truncated:           true,
		},
		Upstream: ImpactDirectionCompleteness{
			ReturnedNodes:       24,
			AvailableNodes:      291,
			ExactAvailableNodes: true,
			Truncated:           true,
		},
	})

	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(warnings))
	}
}

func TestBuildImpactWarningsUsesLowerBoundLanguageWhenExactTotalUnknown(t *testing.T) {
	warnings := buildImpactWarnings(ImpactCompleteness{
		Truncated: true,
		Downstream: ImpactDirectionCompleteness{
			ReturnedNodes:       47,
			AvailableNodes:      47,
			ExactAvailableNodes: false,
			Truncated:           true,
		},
	})

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	expected := "Downstream impact graph is clipped. Showing 47 nodes; more are available, but the exact total is unknown at this maxNodes setting."
	if warnings[0] != expected {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestImpactScoreOrdering(t *testing.T) {
	counts := map[string]*impactCount{
		"GET /a": {MinDepth: 0, Count: 1},
		"GET /b": {MinDepth: 2, Count: 10},
	}
	out := buildImpactHttpCalls(counts, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].Path != "/a" {
		t.Fatalf("expected /a to rank first, got %s", out[0].Path)
	}
	if out[0].Score < out[1].Score {
		t.Fatalf("expected scores sorted desc, got %f < %f", out[0].Score, out[1].Score)
	}
	if !out[0].DirectlyAffected {
		t.Fatalf("expected depth-0 impact to be directly affected: %+v", out[0])
	}
	if out[1].DirectlyAffected {
		t.Fatalf("expected depth-2 impact to be transitive: %+v", out[1])
	}
}

func TestNormalizeImpactRangeInheritsParentFileAndStartEndAliases(t *testing.T) {
	got := normalizeImpactRange(ImpactRange{
		Start: 42,
		End:   84,
	}, "resource-ui", "src/views/List.vue")

	if got.Repo != "resource-ui" || got.Path != "src/views/List.vue" {
		t.Fatalf("range did not inherit parent file identity: %#v", got)
	}
	if got.StartLine != 42 || got.EndLine != 84 {
		t.Fatalf("range did not normalize start/end aliases: %#v", got)
	}
}

func TestParseUnifiedDiffRangesDeletionOnlyUsesOldRange(t *testing.T) {
	diff := `diff --git a/Controllers/ResourceController.cs b/Controllers/ResourceController.cs
--- a/Controllers/ResourceController.cs
+++ b/Controllers/ResourceController.cs
@@ -122,3 +122,0 @@
-public IHttpActionResult Create([FromBody] ResourceRequest request)
-{
-}
`
	got := parseUnifiedDiffRanges(diff, "resource-api")
	if len(got) != 1 {
		t.Fatalf("got %d ranges, want 1: %#v", len(got), got)
	}
	if got[0].StartLine != 122 || got[0].EndLine != 124 {
		t.Fatalf("deletion hunk should map to old range, got %#v", got[0])
	}
}

func TestImpactRootsFromCallerIDs(t *testing.T) {
	callerID := trace.BuildCallerID("resource-ui", "src/composables/useNotification.ts", "useNotification")

	got := impactRootsFromCallerIDs([]string{callerID, ""})
	if len(got) != 1 {
		t.Fatalf("expected one structured root, got %+v", got)
	}
	if got[0].CallerID != callerID ||
		got[0].Repo != "resource-ui" ||
		got[0].File != "src/composables/useNotification.ts" ||
		got[0].Name != "useNotification" {
		t.Fatalf("unexpected structured root: %+v", got[0])
	}
}

func TestImpactBuilderDownstreamSummaryAndReport(t *testing.T) {
	rootID := trace.BuildCallerID("repo", "file.go", "Root")
	childID := trace.BuildCallerID("repo", "file.go", "Child")

	root := &trace.TreeNode{
		Name:       "Root",
		Repo:       "repo",
		File:       "file.go",
		Line:       10,
		Depth:      0,
		EdgeType:   "call",
		Confidence: "high",
		CallerID:   rootID,
	}
	child := &trace.TreeNode{
		Name:       "Child",
		Repo:       "repo",
		File:       "file.go",
		Line:       20,
		Depth:      1,
		EdgeType:   "call",
		Confidence: "high",
		CallerID:   childID,
	}
	httpNode := &trace.TreeNode{
		Name:       "[POST /api/x]",
		Depth:      1,
		EdgeType:   "http",
		Confidence: "high",
		HttpMethod: "POST",
		HttpTarget: "/api/x",
		Line:       30,
	}
	sqsNode := &trace.TreeNode{
		Name:        "[SQS -> queue-x]",
		Depth:       1,
		EdgeType:    "sqs",
		Confidence:  "high",
		QueueTarget: "queue-x",
		Line:        40,
	}
	consumerID := trace.BuildCallerID("worker", "queue.go", "QueueWorker.Handle")
	sqsNode.Children = []*trace.TreeNode{{
		Name:       "QueueWorker.Handle",
		Repo:       "worker",
		File:       "queue.go",
		Depth:      2,
		EdgeType:   "sqs",
		Confidence: "high",
		CallerID:   consumerID,
	}}
	root.Children = []*trace.TreeNode{child, httpNode, sqsNode}

	builder := newImpactBuilder()
	builder.ingestTrees([]*trace.TreeNode{root}, "downstream")

	summary := builder.buildSummary(nil, nil, nil, false)
	if len(summary.HttpCalls) != 1 {
		t.Fatalf("expected 1 http call, got %d", len(summary.HttpCalls))
	}
	if summary.HttpCalls[0].Method != "POST" || summary.HttpCalls[0].Path != "/api/x" {
		t.Fatalf("unexpected http call summary: %+v", summary.HttpCalls[0])
	}
	if summary.HttpCalls[0].DirectlyAffected {
		t.Fatalf("expected downstream http call to be transitive: %+v", summary.HttpCalls[0])
	}
	if len(summary.Queues) != 1 || summary.Queues[0].Name != "queue-x" {
		t.Fatalf("unexpected queue summary: %+v", summary.Queues)
	}
	if summary.Queues[0].DirectlyAffected {
		t.Fatalf("expected downstream queue to be transitive: %+v", summary.Queues[0])
	}
	if len(summary.Queues[0].Consumers) != 1 || summary.Queues[0].Consumers[0] != consumerID {
		t.Fatalf("unexpected queue consumers: %+v", summary.Queues[0].Consumers)
	}
	if len(summary.Repos) != 2 || summary.Repos[0].Name != "repo" || summary.Repos[1].Name != "worker" {
		t.Fatalf("unexpected repo summary: %+v", summary.Repos)
	}
	if !summary.Repos[0].DirectlyAffected || summary.Repos[1].DirectlyAffected {
		t.Fatalf("unexpected repo direct/transitive flags: %+v", summary.Repos)
	}

	report := builder.buildReport()
	if len(report.Nodes) < 4 {
		t.Fatalf("expected at least 4 nodes, got %d", len(report.Nodes))
	}
	rootNodeID := "func:" + rootID
	childNodeID := "func:" + childID
	httpNodeID := fmt.Sprintf("http:POST:/api/x:from:%s", rootNodeID)
	sqsNodeID := fmt.Sprintf("sqs:queue-x:from:%s", rootNodeID)

	assertEdge(t, report.Edges, rootNodeID, childNodeID, "call")
	assertEdge(t, report.Edges, rootNodeID, httpNodeID, "http")
	assertEdge(t, report.Edges, rootNodeID, sqsNodeID, "sqs")
}

func TestImpactBuilderUpstreamEdges(t *testing.T) {
	targetID := trace.BuildCallerID("repo", "file.go", "Target")
	callerID := trace.BuildCallerID("repo", "file.go", "Caller")

	target := &trace.TreeNode{
		Name:       "Target",
		Repo:       "repo",
		File:       "file.go",
		Depth:      0,
		EdgeType:   "call",
		Confidence: "low",
		CallerID:   targetID,
	}
	caller := &trace.TreeNode{
		Name:       "Caller",
		Repo:       "repo",
		File:       "file.go",
		Depth:      1,
		EdgeType:   "call",
		Confidence: "low",
		CallerID:   callerID,
	}
	target.Children = []*trace.TreeNode{caller}

	builder := newImpactBuilder()
	builder.ingestTrees([]*trace.TreeNode{target}, "upstream")

	report := builder.buildReport()
	callerNodeID := "func:" + callerID
	targetNodeID := "func:" + targetID
	assertEdge(t, report.Edges, callerNodeID, targetNodeID, "call")
}

func assertEdge(t *testing.T, edges []ImpactEdge, source, target, edgeType string) {
	t.Helper()
	for _, edge := range edges {
		if edge.Source == source && edge.Target == target && edge.Type == edgeType {
			return
		}
	}
	t.Fatalf("expected edge %s -> %s (%s) not found", source, target, edgeType)
}
