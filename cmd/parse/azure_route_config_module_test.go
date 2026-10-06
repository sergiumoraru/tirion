package main

import "testing"

func TestEffectiveAzureEndpointPath_DefaultPrefix(t *testing.T) {
	got := effectiveAzureEndpointPath("resources", "graphql/resources", "api")
	if got != "/api/graphql/resources" {
		t.Fatalf("unexpected path: %q", got)
	}
}

func TestEffectiveAzureEndpointPath_EmptyPrefix(t *testing.T) {
	got := effectiveAzureEndpointPath("GetRoles", "", "")
	if got != "/GetRoles" {
		t.Fatalf("unexpected path: %q", got)
	}
}

func TestEffectiveAzureEndpointPath_CustomPrefixDropsStoredDefaultAPIPrefix(t *testing.T) {
	got := effectiveAzureEndpointPath("resources", "/api/graphql/resources", "internal")
	if got != "/internal/graphql/resources" {
		t.Fatalf("unexpected path: %q", got)
	}
}

func TestResolveAzureRoutePrefixPrefersNearestHostConfig(t *testing.T) {
	configs := []azureRouteHostConfig{
		{hostPath: "host.json", routePrefix: "api"},
		{hostPath: "apps/service/host.json", routePrefix: "internal"},
		{hostPath: "apps/service/nested/host.json", routePrefix: "nested"},
	}

	got := resolveAzureRoutePrefix(configs, "apps/service/nested/Export/function.json")
	if got != "nested" {
		t.Fatalf("unexpected route prefix: %q", got)
	}
}

func TestResolveAzureRoutePrefixFallsBackToDefaultAPIPrefix(t *testing.T) {
	configs := []azureRouteHostConfig{
		{hostPath: "apps/service/host.json", routePrefix: "internal"},
	}

	got := resolveAzureRoutePrefix(configs, "apps/catalog/Search/function.json")
	if got != "api" {
		t.Fatalf("unexpected route prefix: %q", got)
	}
}

func TestAzureSourcePathCandidatesIncludesDistToSrcLayout(t *testing.T) {
	candidates := azureSourcePathCandidates(
		"apps/worker/ProcessResource/function.json",
		"../dist/ProcessResource/index.js",
	)

	want := "apps/worker/src/ProcessResource/index.ts"
	for _, candidate := range candidates {
		if candidate == want {
			return
		}
	}
	t.Fatalf("expected %q in candidates %#v", want, candidates)
}
