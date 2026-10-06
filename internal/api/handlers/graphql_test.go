package handlers

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestGraphQLFlowTokensExtractsOperationAndPathTokens(t *testing.T) {
	got := graphQLFlowTokens("ResourceStatuses", "src/resources/graphql/query/PaginatedStatuses.gql")
	want := map[string]bool{
		"resources": true,
		"statuses":  true,
	}
	for token := range want {
		found := false
		for _, candidate := range got {
			if candidate == token {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected token %q in %v", token, got)
		}
	}
}

func TestClassifyGraphQLEntrypointMatch(t *testing.T) {
	opTokens := map[string]struct{}{
		"resources": {},
		"status":    {},
	}
	entry := graph.GraphQLBackendEntrypointInfo{
		HandlerName:     "startServer",
		FilePath:        "resources/server.js",
		ControllersPath: "path.join(__dirname, 'controllers')",
	}
	if got := classifyGraphQLEntrypointMatch(opTokens, entry); got != "medium" {
		t.Fatalf("expected medium confidence, got %q", got)
	}
}

func TestFilterGraphQLControllersForEntrypoints(t *testing.T) {
	entrypoints := []GraphQLFlowEntrypoint{
		{GraphQLBackendEntrypointInfo: graph.GraphQLBackendEntrypointInfo{ID: 1}},
	}
	controllers := []graph.GraphQLControllerLinkInfo{
		{EntrypointID: 1, ControllerFilePath: "resources/controllers/A.js"},
		{EntrypointID: 2, ControllerFilePath: "sample/controllers/B.js"},
	}
	filtered := filterGraphQLControllersForEntrypoints(controllers, entrypoints)
	if len(filtered) != 1 || filtered[0].ControllerFilePath != "resources/controllers/A.js" {
		t.Fatalf("unexpected filtered controllers: %#v", filtered)
	}
}

func TestGraphQLFlowLookupPlanBackendOnlySkipsFrontendLookups(t *testing.T) {
	loadFrontend, loadBackend := graphQLFlowLookupPlan("", "backend-repo", "")
	if loadFrontend {
		t.Fatal("expected backend-only flow to skip frontend lookups")
	}
	if !loadBackend {
		t.Fatal("expected backend-only flow to keep backend lookups")
	}
}

func TestGraphQLFlowLookupPlanOperationLoadsBackendLookups(t *testing.T) {
	loadFrontend, loadBackend := graphQLFlowLookupPlan("frontend-repo", "", "ResourceStatuses")
	if !loadFrontend {
		t.Fatal("expected frontend flow to keep frontend lookups")
	}
	if !loadBackend {
		t.Fatal("expected operation flow to load backend resolver lookups")
	}
}

func TestFilterUsagesByOperationNameMatchesExactOperationOnly(t *testing.T) {
	resolvedGetStatus := "GetStatus"
	resolvedGetStatuses := "GetStatuses"
	usages := []graph.GraphQLOperationUsageInfo{
		{ResolvedOperation: &resolvedGetStatus, FilePath: "src/graphql/GetStatus.gql"},
		{ResolvedOperation: &resolvedGetStatuses, FilePath: "src/graphql/GetStatuses.gql"},
	}

	filtered := filterUsagesByOperationName(usages, "GetStatus")
	if len(filtered) != 1 {
		t.Fatalf("expected one exact usage match, got %#v", filtered)
	}
	if filtered[0].ResolvedOperation == nil || *filtered[0].ResolvedOperation != "GetStatus" {
		t.Fatalf("expected GetStatus usage, got %#v", filtered)
	}
}

func TestFilterUsagesByOperationNameMatchesDirectGraphQLClientUsage(t *testing.T) {
	usages := []graph.GraphQLOperationUsageInfo{
		{
			ImportPath:        "__graphql_operation__:GetResource",
			ImportedAs:        "GetResource",
			CallerFunction:    "loadResource",
			FilePath:          "src/services/resources.ts",
			ResolvedOperation: nil,
		},
		{
			ImportPath:     "__graphql_operation__:GetResources",
			ImportedAs:     "GetResources",
			CallerFunction: "loadResources",
			FilePath:       "src/services/resources.ts",
		},
	}

	filtered := filterUsagesByOperationName(usages, "GetResource")
	if len(filtered) != 1 {
		t.Fatalf("expected one direct usage match, got %#v", filtered)
	}
	if filtered[0].CallerFunction != "loadResource" {
		t.Fatalf("expected direct GetResource usage, got %#v", filtered)
	}
}
