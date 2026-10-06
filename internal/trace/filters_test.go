package trace

import "testing"

func TestNoTestsFilterExcludesSpecFlowGeneratedFiles(t *testing.T) {
	nodes := []*TreeNode{
		{
			Name: "PublishFeature.Publish",
			Repo: "Data-Publish",
			File: "Middleware-Publish-Tests/Test.Middleware.Content/Features/Publish.feature.cs",
		},
		{
			Name: "CancelFeature.Cancel",
			Repo: "Data-Publish",
			File: "Middleware-Publish-Tests/Test.Middleware.Content/Features/Cancel.feature.vb",
		},
		{
			Name: "PublishService.Publish",
			Repo: "Data-Publish",
			File: "Middleware-Publish/PublishService.cs",
		},
	}

	filtered := FilterTree(nodes, BuildFilters(true, nil, nil, nil))
	if len(filtered) != 1 {
		t.Fatalf("expected only production node to remain, got %#v", filtered)
	}
	if filtered[0].Name != "PublishService.Publish" {
		t.Fatalf("unexpected remaining node: %#v", filtered[0])
	}
}
