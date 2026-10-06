package handlers

import (
	"context"
	"strings"
	"testing"
)

func TestResolveAzureFunctionRootCandidatesFallsBackWithoutScriptFile(t *testing.T) {
	roots := resolveAzureFunctionRootCandidates(
		context.Background(),
		nil,
		"resource-functions",
		"functions/ProcessResource/function.json",
		"",
		"ProcessResource",
		"httpTrigger",
		"ProcessResource",
		5,
		"POST",
		"/api/ProcessResource/{id}",
		searchWorkspaceScope{},
	)

	if len(roots) != 1 {
		t.Fatalf("expected one fallback root, got %#v", roots)
	}
	if roots[0].File != "functions/ProcessResource/function.json" {
		t.Fatalf("expected fallback to function.json path, got %#v", roots[0])
	}
	if roots[0].Handler != "ProcessResource" {
		t.Fatalf("expected fallback handler ProcessResource, got %#v", roots[0])
	}
}

func TestAzureSourcePathCandidatesIncludesDefaultIndexWhenScriptFileBlank(t *testing.T) {
	candidates := azureSourcePathCandidates("functions/ProcessResource/function.json", "")
	if len(candidates) == 0 {
		t.Fatal("expected default source candidates when scriptFile is blank")
	}

	foundLocalIndex := false
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate, "/ProcessResource/index.ts") || strings.HasSuffix(candidate, "/ProcessResource/index.js") {
			foundLocalIndex = true
			break
		}
	}
	if !foundLocalIndex {
		t.Fatalf("expected default index candidate in %#v", candidates)
	}
}

func TestSelectBestAzureFunctionRootsKeepsSingleBestMatch(t *testing.T) {
	resolved := []FlowEndpoint{
		{Repo: "repo", File: "functions/ProcessResource/dist/index.js", Handler: "ProcessResource", Line: 20},
		{Repo: "repo", File: "functions/ProcessResource/src/index.ts", Handler: "ProcessResource", Line: 10},
		{Repo: "repo", File: "functions/ProcessResource/src/index.ts", Handler: "helper", Line: 5},
	}
	candidatePaths := []string{
		"functions/ProcessResource/src/index.ts",
		"functions/ProcessResource/dist/index.js",
	}
	candidateNames := []string{"ProcessResource", "helper"}

	best := selectBestAzureFunctionRoots(resolved, candidatePaths, candidateNames)
	if len(best) != 1 {
		t.Fatalf("expected one best azure root, got %#v", best)
	}
	if best[0].File != "functions/ProcessResource/src/index.ts" || best[0].Handler != "ProcessResource" {
		t.Fatalf("expected src ProcessResource root to win, got %#v", best[0])
	}
}
