package handlers

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestAzureFlowEndpointSearchTermsIncludesQueryAndTriggerRoute(t *testing.T) {
	terms := azureFlowEndpointSearchTerms([]graph.AzureFunctionTriggerInfo{
		{FunctionName: "catalog", Route: "/api/graphql/catalog"},
	}, "catalog", "graphql")

	if len(terms) < 3 {
		t.Fatalf("expected query, function, and route terms, got %#v", terms)
	}
	if terms[0] != "graphql" {
		t.Fatalf("expected query term to be searched first, got %#v", terms)
	}
	foundRoute := false
	for _, term := range terms {
		if term == "/api/graphql/catalog" {
			foundRoute = true
			break
		}
	}
	if !foundRoute {
		t.Fatalf("expected route term in %#v", terms)
	}
}

func TestFilterAzureFlowEndpointsKeepsRouteMatchedEndpoint(t *testing.T) {
	triggers := []graph.AzureFunctionTriggerInfo{
		{FunctionName: "catalog", Route: "/api/graphql/catalog"},
	}
	endpoints := []graph.EndpointInfo{
		{RepoName: "repo", FilePath: "functions/Catalog/function.json", Method: "GET", Path: "/api/graphql/catalog", Handler: "httpTrigger"},
		{RepoName: "repo", FilePath: "functions/Other/function.json", Method: "GET", Path: "/api/other", Handler: "other"},
	}

	filtered := filterAzureFlowEndpoints(triggers, endpoints, 10)
	if len(filtered) != 1 {
		t.Fatalf("expected one matched endpoint, got %#v", filtered)
	}
	if filtered[0].Path != "/api/graphql/catalog" {
		t.Fatalf("expected catalog endpoint, got %#v", filtered[0])
	}
}
