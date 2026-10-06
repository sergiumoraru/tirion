package trace

import "testing"

func TestEnforceEdgeEvidenceRequirements_AttachesLocationFromParent(t *testing.T) {
	root := &TreeNode{
		Repo: "resource-api",
		File: "src/ResourceController.java",
	}
	child := &TreeNode{
		Name:       "ResourceService.save",
		Line:       216,
		Confidence: confidenceHigh,
		Evidence:   newEvidence(edgeSourceFunctionCalls, "same_class_this"),
	}
	root.Children = []*TreeNode{child}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if child.Evidence == nil {
		t.Fatalf("expected edge evidence to be present")
	}
	if child.Evidence.Repo != root.Repo || child.Evidence.File != root.File || child.Evidence.Line != child.Line {
		t.Fatalf("expected edge evidence location %s/%s:%d, got %s/%s:%d",
			root.Repo, root.File, child.Line,
			child.Evidence.Repo, child.Evidence.File, child.Evidence.Line,
		)
	}
	if child.Confidence != confidenceHigh {
		t.Fatalf("expected confidence to remain high with complete location evidence, got %q", child.Confidence)
	}
}

func TestEnforceEdgeEvidenceRequirements_DowngradesWhenLocationMissing(t *testing.T) {
	root := &TreeNode{
		Repo: "resource-api",
		File: "src/ResourceController.java",
	}
	child := &TreeNode{
		Name:       "Consumer.consume",
		Confidence: confidenceHigh,
		Evidence:   newEvidence(evidenceSourceSqsConsumer, ""),
	}
	root.Children = []*TreeNode{child}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if child.Confidence != confidenceMedium {
		t.Fatalf("expected confidence downgrade from high to medium when location evidence is missing, got %q", child.Confidence)
	}
	if evidenceHasLocation(child.Evidence) {
		t.Fatalf("expected missing location evidence for unresolved child edge, got %#v", child.Evidence)
	}
}

func TestEnforceEdgeEvidenceRequirements_UsesChildDefinitionLocationWhenNeeded(t *testing.T) {
	root := &TreeNode{Name: "[POST /resources]"}
	child := &TreeNode{
		Name:       "→ ResourceController.save [resource-api]",
		Repo:       "resource-api",
		File:       "src/ResourceController.java",
		Line:       216,
		Confidence: confidenceMedium,
		Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
	}
	root.Children = []*TreeNode{child}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if child.Evidence == nil {
		t.Fatalf("expected edge evidence to be present")
	}
	if child.Evidence.Repo != child.Repo || child.Evidence.File != child.File || child.Evidence.Line != child.Line {
		t.Fatalf("expected child definition evidence %s/%s:%d, got %s/%s:%d",
			child.Repo, child.File, child.Line,
			child.Evidence.Repo, child.Evidence.File, child.Evidence.Line,
		)
	}
}

func TestEnforceEdgeEvidenceRequirements_UpstreamCallerEvidenceUsesChildCallerLocation(t *testing.T) {
	root := &TreeNode{
		Repo: "admin-app",
		File: "src/ResourceController.java",
		Line: 42,
	}
	child := &TreeNode{
		Repo:       "web-app",
		File:       "src/ResourceClient.java",
		Line:       128,
		Confidence: confidenceHigh,
		Evidence:   newEvidence(evidenceSourceFunctionCaller, ""),
	}
	root.Children = []*TreeNode{child}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if child.Evidence == nil {
		t.Fatalf("expected edge evidence to be present")
	}
	if child.Evidence.Repo != child.Repo || child.Evidence.File != child.File || child.Evidence.Line != child.Line {
		t.Fatalf("expected upstream edge evidence to use caller location %s/%s:%d, got %s/%s:%d",
			child.Repo, child.File, child.Line,
			child.Evidence.Repo, child.Evidence.File, child.Evidence.Line,
		)
	}
}

func TestEnforceEdgeEvidenceRequirements_RootEdgeGetsOwnLocation(t *testing.T) {
	root := &TreeNode{
		Repo:       "admin-app",
		File:       "src/main.js",
		Line:       108,
		EdgeType:   edgeTypeCall,
		Confidence: confidenceLow,
		Evidence:   newEvidence(evidenceSourcePendingCaller, "name_based"),
	}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if root.Evidence == nil {
		t.Fatalf("expected root edge evidence to be present")
	}
	if root.Evidence.Repo != root.Repo || root.Evidence.File != root.File || root.Evidence.Line != root.Line {
		t.Fatalf("expected root edge evidence %s/%s:%d, got %s/%s:%d",
			root.Repo, root.File, root.Line,
			root.Evidence.Repo, root.Evidence.File, root.Evidence.Line,
		)
	}
}

func TestEnforceEdgeEvidenceRequirements_RootEdgeDowngradesWhenLocationMissing(t *testing.T) {
	root := &TreeNode{
		Repo:       "admin-app",
		File:       "src/main.js",
		EdgeType:   edgeTypeCall,
		Confidence: confidenceHigh,
		Evidence:   newEvidence(evidenceSourcePendingCaller, "name_based"),
	}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if root.Confidence != confidenceMedium {
		t.Fatalf("expected root edge confidence to downgrade when location is missing, got %q", root.Confidence)
	}
}

func TestEnforceEdgeEvidenceRequirements_HttpClientEdgeUsesCallerFileAndCallLine(t *testing.T) {
	root := &TreeNode{
		Repo: "web-app",
		File: "src/api/resources.js",
	}
	httpEdge := &TreeNode{
		Name:       "[POST /resources]",
		Line:       83,
		EdgeType:   edgeTypeHTTP,
		Confidence: confidenceHigh,
		Evidence:   newEvidence(evidenceSourceHttpClient, ""),
	}
	root.Children = []*TreeNode{httpEdge}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if httpEdge.Evidence == nil {
		t.Fatalf("expected evidence for HTTP client edge")
	}
	if httpEdge.Evidence.Repo != root.Repo || httpEdge.Evidence.File != root.File || httpEdge.Evidence.Line != 83 {
		t.Fatalf("expected HTTP client evidence %s/%s:%d, got %s/%s:%d",
			root.Repo, root.File, 83,
			httpEdge.Evidence.Repo, httpEdge.Evidence.File, httpEdge.Evidence.Line,
		)
	}
}

func TestEnforceEdgeEvidenceRequirements_HttpEndpointEdgeUsesEndpointDefinitionLine(t *testing.T) {
	root := &TreeNode{
		Name:       "[POST /resources]",
		EdgeType:   edgeTypeHTTP,
		Confidence: confidenceMedium,
		Evidence:   newEvidence(evidenceSourceHttpClient, ""),
	}
	endpoint := &TreeNode{
		Name:       "→ ResourceController.save [resource-api]",
		Repo:       "resource-api",
		File:       "src/ResourceController.java",
		Line:       216,
		EdgeType:   edgeTypeHTTP,
		Confidence: confidenceMedium,
		Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
	}
	root.Children = []*TreeNode{endpoint}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if endpoint.Evidence == nil {
		t.Fatalf("expected evidence for HTTP endpoint edge")
	}
	if endpoint.Evidence.Repo != endpoint.Repo || endpoint.Evidence.File != endpoint.File || endpoint.Evidence.Line != 216 {
		t.Fatalf("expected HTTP endpoint evidence %s/%s:%d, got %s/%s:%d",
			endpoint.Repo, endpoint.File, 216,
			endpoint.Evidence.Repo, endpoint.Evidence.File, endpoint.Evidence.Line,
		)
	}
}

func TestEnforceEdgeEvidenceRequirements_SqsConsumerEdgeUsesConsumerDefinitionLine(t *testing.T) {
	root := &TreeNode{
		Name:       "[SQS -> resource-events]",
		EdgeType:   edgeTypeSQS,
		Confidence: confidenceHigh,
		Evidence:   newEvidence(evidenceSourceSqsProducer, ""),
	}
	consumer := &TreeNode{
		Name:       "→ ResourceConsumer.process [resource-worker]",
		Repo:       "resource-worker",
		File:       "src/ResourceConsumer.java",
		Line:       47,
		EdgeType:   edgeTypeSQS,
		Confidence: confidenceMedium,
		Evidence:   newEvidence(evidenceSourceSqsConsumer, ""),
	}
	root.Children = []*TreeNode{consumer}

	enforceEdgeEvidenceRequirements([]*TreeNode{root})

	if consumer.Evidence == nil {
		t.Fatalf("expected evidence for SQS consumer edge")
	}
	if consumer.Evidence.Repo != consumer.Repo || consumer.Evidence.File != consumer.File || consumer.Evidence.Line != 47 {
		t.Fatalf("expected SQS consumer evidence %s/%s:%d, got %s/%s:%d",
			consumer.Repo, consumer.File, 47,
			consumer.Evidence.Repo, consumer.Evidence.File, consumer.Evidence.Line,
		)
	}
}
