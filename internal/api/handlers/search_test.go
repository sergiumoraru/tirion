package handlers

import (
	"encoding/json"
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestFilterNoiseRemovesSyntheticAndAccessorFunctions(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:     "function",
			Name:     "jquery.change__resource_L142",
			FilePath: "src/resource-form.js",
		},
		{
			Type: "function",
			Name: "Resource.getResourceDate",
			SourceCode: `public LocalDate getResourceDate() {
    return resourceDate;
}`,
			FilePath: "src/Resource.java",
		},
		{
			Type:     "function",
			Name:     "ResourceService.sendResourceUpdate",
			FilePath: "src/ResourceService.java",
		},
		{
			Type:     "class",
			Name:     "ResourceService",
			FilePath: "src/ResourceService.java",
		},
	}

	filtered := filterNoise(input)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 results after filtering, got %d", len(filtered))
	}
	if filtered[0].Name != "ResourceService.sendResourceUpdate" {
		t.Fatalf("unexpected first result: %s", filtered[0].Name)
	}
	if filtered[1].Type != "class" {
		t.Fatalf("expected class result to remain, got type=%s", filtered[1].Type)
	}
}

func TestFilterRepoIsExactByDefaultAndPrefixWhenRequested(t *testing.T) {
	input := []graph.SearchResult{
		{Name: "ListResources", RepoName: "resource-ui"},
		{Name: "ProcessResource", RepoName: "resource-ui-worker"},
		{Name: "CreateResource", RepoName: "resource-api"},
	}

	exact := filterRepo(input, "resource-ui")
	if len(exact) != 1 || exact[0].RepoName != "resource-ui" {
		t.Fatalf("exact repo filter = %#v", exact)
	}

	prefix := filterRepo(input, "resource-ui*")
	if len(prefix) != 2 {
		t.Fatalf("prefix repo filter returned %d results: %#v", len(prefix), prefix)
	}
}

func TestSearchResponseIncludesRequestedMode(t *testing.T) {
	payload, err := json.Marshal(SearchResponse{
		Query:         "creatResource",
		RequestedMode: "keyword",
		Mode:          "trigram",
	})
	if err != nil {
		t.Fatalf("marshal search response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal search response: %v", err)
	}
	if decoded["requestedMode"] != "keyword" || decoded["mode"] != "trigram" {
		t.Fatalf("unexpected mode fields: %s", payload)
	}
}

func TestSearchExecutionTotalIncludesAllBuckets(t *testing.T) {
	total := searchExecutionTotal(searchExecution{Stats: SearchBucketBreakdown{
		Functions:         SearchBucketStats{Total: 1},
		Classes:           SearchBucketStats{Total: 2},
		TypeSymbols:       SearchBucketStats{Total: 3},
		Endpoints:         SearchBucketStats{Total: 4},
		DataEntities:      SearchBucketStats{Total: 5},
		ExternalSymbols:   SearchBucketStats{Total: 6},
		Schedules:         SearchBucketStats{Total: 7},
		GraphQLOperations: SearchBucketStats{Total: 8},
		AzureTriggers:     SearchBucketStats{Total: 9},
		QueueHits:         SearchBucketStats{Total: 10},
	}})
	if total != 55 {
		t.Fatalf("total = %d, want 55", total)
	}
}

func TestRerankKeywordFunctionsPrefersBusinessFunction(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:     "function",
			Name:     "jquery.change__resource_L142",
			FilePath: "src/resource-form.js",
			Extra:    map[string]interface{}{"richness": 156},
		},
		{
			Type:     "function",
			Name:     "Resource.getResourceDate",
			FilePath: "src/Resource.java",
			Extra:    map[string]interface{}{"richness": 132},
		},
		{
			Type:     "function",
			Name:     "ResourceService.sendResourceUpdate",
			FilePath: "src/ResourceService.java",
			Extra:    map[string]interface{}{"richness": 30},
		},
	}

	ranked := rerankKeywordFunctions("resource", input, nil, nil, nil)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked results, got %d", len(ranked))
	}
	if ranked[0].Name != "ResourceService.sendResourceUpdate" {
		t.Fatalf("expected business function first, got %s", ranked[0].Name)
	}
	if ranked[2].Name != "jquery.change__resource_L142" {
		t.Fatalf("expected synthetic callback last, got %s", ranked[2].Name)
	}
}

func TestPaginateSearchResultsBuildsTruthfulStats(t *testing.T) {
	input := []graph.SearchResult{
		{ID: 1, Name: "A.one"},
		{ID: 2, Name: "A.two"},
		{ID: 3, Name: "A.three"},
		{ID: 4, Name: "A.four"},
		{ID: 5, Name: "A.five"},
	}

	page, stats := paginateSearchResults(input, 2, 2)
	if len(page) != 2 {
		t.Fatalf("expected 2 results in page, got %d", len(page))
	}
	if page[0].Name != "A.three" || page[1].Name != "A.four" {
		t.Fatalf("unexpected page contents: %+v", page)
	}
	if stats.Returned != 2 {
		t.Fatalf("expected returned=2, got %d", stats.Returned)
	}
	if stats.Total != 5 {
		t.Fatalf("expected total=5, got %d", stats.Total)
	}
	if stats.Offset != 2 {
		t.Fatalf("expected offset=2, got %d", stats.Offset)
	}
	if stats.Limit != 2 {
		t.Fatalf("expected limit=2, got %d", stats.Limit)
	}
	if !stats.HasMore {
		t.Fatalf("expected hasMore=true")
	}
	if !stats.Truncated {
		t.Fatalf("expected truncated=true")
	}
	if !stats.ExactTotal {
		t.Fatalf("expected exactTotal=true")
	}
}

func TestPaginateSearchResultsHandlesOffsetBeyondTotal(t *testing.T) {
	input := []graph.SearchResult{
		{ID: 1, Name: "A.one"},
		{ID: 2, Name: "A.two"},
	}

	page, stats := paginateSearchResults(input, 5, 25)
	if len(page) != 0 {
		t.Fatalf("expected empty page, got %d results", len(page))
	}
	if stats.Returned != 0 {
		t.Fatalf("expected returned=0, got %d", stats.Returned)
	}
	if stats.Total != 2 {
		t.Fatalf("expected total=2, got %d", stats.Total)
	}
	if stats.HasMore {
		t.Fatalf("expected hasMore=false")
	}
	if stats.Truncated {
		t.Fatalf("expected truncated=false")
	}
}

func TestRerankKeywordFunctionsBoostsMissingTermFromSource(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:       "function",
			Name:       "ResourceController.load",
			FilePath:   "src/ResourceController.java",
			RepoName:   "resource-api",
			SourceCode: "String source = request.getRemoteSource(); return resource;",
			Extra:      map[string]interface{}{"richness": 58},
		},
		{
			Type:     "function",
			Name:     "ResourceService.BuildLoadViewModelAsync",
			FilePath: "Services/ResourceService.cs",
			RepoName: "resource-backend",
			Extra:    map[string]interface{}{"richness": 81},
		},
	}

	ranked := rerankKeywordFunctions("remote load", input, nil, nil, nil)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked results, got %d", len(ranked))
	}
	if ranked[0].Name != "ResourceController.load" {
		t.Fatalf("expected source-covered multi-term match first, got %s", ranked[0].Name)
	}
}

func TestRerankKeywordFunctionsPrefersQualifiedResourceProfileUpdate(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:     "function",
			Name:     "ProfileService.updateProfile",
			FilePath: "src/ProfileService.java",
			RepoName: "profile-api",
			Extra:    map[string]interface{}{"richness": 91},
		},
		{
			Type:     "function",
			Name:     "ResourceProfilesController.updateResourceProfile",
			FilePath: "src/ResourceProfilesController.java",
			RepoName: "resource-profile-api",
			Extra:    map[string]interface{}{"richness": 42},
		},
		{
			Type:     "function",
			Name:     "ResourceProfileService.getResourceProfile",
			FilePath: "src/ResourceProfileService.java",
			RepoName: "core-app",
			Extra:    map[string]interface{}{"richness": 118},
		},
	}

	ranked := rerankKeywordFunctions("resource profile update", input, nil, nil, nil)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked results, got %d", len(ranked))
	}
	if ranked[0].Name != "ResourceProfilesController.updateResourceProfile" {
		t.Fatalf("expected resource profile update controller first, got %s", ranked[0].Name)
	}
}

func TestRerankKeywordFunctionsPrefersQualifiedRemoteLoad(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:     "function",
			Name:     "ResourceController.Load",
			FilePath: "Controllers/ResourceController.cs",
			RepoName: "resource-api",
			Extra:    map[string]interface{}{"richness": 128},
		},
		{
			Type:     "function",
			Name:     "ResourceController.RemoteLoad",
			FilePath: "Controllers/ResourceController.cs",
			RepoName: "resource-api",
			Extra:    map[string]interface{}{"richness": 67},
		},
		{
			Type:     "function",
			Name:     "ResourceAction.load",
			FilePath: "src/ResourceAction.java",
			RepoName: "web-app",
			Extra:    map[string]interface{}{"richness": 94},
		},
	}

	ranked := rerankKeywordFunctions("remote load", input, nil, nil, nil)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked results, got %d", len(ranked))
	}
	if ranked[0].Name != "ResourceController.RemoteLoad" {
		t.Fatalf("expected RemoteLoad first, got %s", ranked[0].Name)
	}
}

func TestKeywordDiscoveryScorePrefersExactLeafMethodOverLoadVariants(t *testing.T) {
	login := graph.SearchResult{
		Type:     "function",
		Name:     "ResourceController.Load",
		FilePath: "Controllers/ResourceController.cs",
		RepoName: "resource-api",
		Extra:    map[string]interface{}{"richness": 90},
	}
	loginUser := graph.SearchResult{
		Type:     "function",
		Name:     "ResourceController.LoadResource",
		FilePath: "Controllers/ResourceController.cs",
		RepoName: "resource-api",
		Extra:    map[string]interface{}{"richness": 120},
	}
	endpointHandlers := map[string]bool{
		"resource-api:Controllers/ResourceController.cs:ResourceController.Load":         true,
		"resource-api:Controllers/ResourceController.cs:ResourceController.LoadResource": true,
	}

	loginScore := keywordDiscoveryScore("remote load", login, nil, nil, endpointHandlers)
	loginUserScore := keywordDiscoveryScore("remote load", loginUser, nil, nil, endpointHandlers)
	if loginScore <= loginUserScore {
		t.Fatalf("expected exact leaf load to outrank LoadResource, got login=%d loginUser=%d", loginScore, loginUserScore)
	}
}

func TestRerankKeywordFunctionsBoostsSourceIdentifiedEndpointHandler(t *testing.T) {
	input := []graph.SearchResult{
		{
			Type:       "function",
			Name:       "ResourceService.updateResourceStatus",
			FilePath:   "src/main/java/example/ResourceService.java",
			RepoName:   "resource-api",
			SourceCode: "return updateResourceStatus(body);",
			Extra:      map[string]interface{}{"richness": 84},
		},
		{
			Type:       "function",
			Name:       "ResourceController.setStatus",
			FilePath:   "src/main/java/example/ResourceController.java",
			RepoName:   "resource-api",
			SourceCode: "requestMapping = \"/resources/updateResourceStatus\";",
			Extra:      map[string]interface{}{"richness": 42},
		},
	}

	ranked := rerankKeywordFunctions(
		"update resource status",
		input,
		nil,
		nil,
		map[string]bool{"resource-api:src/main/java/example/ResourceController.java:ResourceController.setStatus": true},
	)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked results, got %d", len(ranked))
	}
	if ranked[0].Name != "ResourceController.setStatus" {
		t.Fatalf("expected endpoint handler first, got %s", ranked[0].Name)
	}
}
