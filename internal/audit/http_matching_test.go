package audit

import "testing"

func TestNormalizeAuditHTTPPathPreservesLiteralPrefix(t *testing.T) {
	t.Parallel()

	got := normalizeAuditHTTPPath("/apiresources/:resourceId/details?expand=true")
	want := "/apiresources/:resourceid/details"
	if got != want {
		t.Fatalf("normalizeAuditHTTPPath() = %q, want %q", got, want)
	}
}

func TestAuditHTTPPathDoesNotInventNamespaceAlias(t *testing.T) {
	t.Parallel()

	callPath := "/api/resources/:id"
	endpointPath := "/api/internal/resources/{id}"
	if auditHTTPPathMatches("GET", callPath, "GET", endpointPath) {
		t.Fatalf("unexpected namespace alias between %q and %q", callPath, endpointPath)
	}
}

func TestAuditHTTPPathMatchesOptionalTrailingWildcardSegments(t *testing.T) {
	t.Parallel()

	callPath := "/action/resources/:resourceId/details"
	endpointPath := "/action/resources/:param1/:param2/:param3"
	if !auditHTTPPathMatches("REQUEST", callPath, "REQUEST", endpointPath) {
		t.Fatalf("expected %q to match %q", callPath, endpointPath)
	}
}

func TestAuditHTTPPathDoesNotAddActionAliasForStandardHTTPCalls(t *testing.T) {
	t.Parallel()

	callPath := "/users/remote/permissions"
	endpointPath := "/action/users/remote/permissions"
	if auditHTTPPathMatches("GET", callPath, "GET", endpointPath) {
		t.Fatalf("expected standard HTTP path %q not to match action route %q", callPath, endpointPath)
	}
}

func TestAuditHTTPPathDoesNotTreatArbitraryAPINamespaceAsEquivalent(t *testing.T) {
	t.Parallel()

	callPath := "/api/users/:id"
	endpointPath := "/api/v1/users/{id}"
	if auditHTTPPathMatches("GET", callPath, "GET", endpointPath) {
		t.Fatalf("expected %q not to match %q", callPath, endpointPath)
	}
}

func TestAuditHTTPRouteLikeExcludesNavigation(t *testing.T) {
	t.Parallel()

	if auditHTTPRouteLike("GET", "navigation", "/action/delegate/:id") {
		t.Fatalf("expected navigation client path to be excluded from route-like audit")
	}
}

func TestClassifyAuditHTTPCallRetainsMethodMismatch(t *testing.T) {
	t.Parallel()

	call := auditHTTPCall{
		Method: "POST",
		Path:   "/catalog/item-types",
	}
	endpoints := []auditHTTPEndpoint{{
		Method: "GET",
		Path:   "/catalog/item-types",
	}}

	classification := classifyAuditHTTPCall(call, endpoints)
	if !classification.routeLike {
		t.Fatalf("expected route-like classification")
	}
	if !classification.methodIsHTTP {
		t.Fatalf("expected HTTP method to be recognized")
	}
	if !classification.hasPathMatch {
		t.Fatalf("expected path match")
	}
	if classification.hasMethodMatch {
		t.Fatalf("expected method mismatch to remain unresolved")
	}
}

func TestClassifyAuditHTTPCallMatchesPassiveParameterizedPath(t *testing.T) {
	t.Parallel()

	call := auditHTTPCall{
		Method: "POST",
		Path:   "/resources/:resourceId/details?expand=true",
	}
	endpoints := []auditHTTPEndpoint{{
		Method: "POST",
		Path:   "/resources/{id}/details",
	}}

	classification := classifyAuditHTTPCall(call, endpoints)
	if !classification.routeLike {
		t.Fatalf("expected route-like classification")
	}
	if !classification.hasPathMatch {
		t.Fatalf("expected path match")
	}
	if !classification.hasMethodMatch {
		t.Fatalf("expected method match")
	}
}

func TestMatchHTTPRepoEndpointsSelectsRepoByMethod(t *testing.T) {
	t.Parallel()

	targets := matchHTTPRepoEndpoints("GET", "/api/v2/{tenantId}/resources/search", []httpRepoEndpoint{
		{RepoID: 7, Method: "GET", Path: "/api/v2/{tenantId}/resources/search"},
		{RepoID: 8, Method: "POST", Path: "/api/v2/{tenantId}/resources/search"},
	})

	if _, ok := targets[7]; !ok {
		t.Fatalf("expected GET resource search to resolve target repo 7")
	}
	if _, ok := targets[8]; ok {
		t.Fatalf("did not expect POST-only endpoint repo to match GET call")
	}
}

func TestAuditHTTPPathAliasesRequireConfiguration(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"/api", "/action", "/gateway/v2"} {
		if auditHTTPPathMatches("REQUEST", "/resources/:id", "REQUEST", prefix+"/resources/{id}") {
			t.Fatalf("invented alias for %s", prefix)
		}
		if !auditHTTPPathMatches("GET", "/resources/:id", "GET", prefix+"/resources/{id}", prefix) {
			t.Fatalf("configured alias not honored for %s", prefix)
		}
	}
	if auditHTTPPathMatches("GET", "/{tenant}/resources", "GET", "/resources") {
		t.Fatal("leading tenant parameter was discarded")
	}
}
