package handlers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractHTTPWrapperCallExpressions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".codebase-intel")
	// An explicit TIRION_HOME (set by CI and by developers) overrides HOME; point
	// it at the fixture so the test is hermetic either way.
	t.Setenv("TIRION_HOME", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns.yaml"), []byte(`http_clients:
  - name: resource-client
    language: java
    contains: ["resourceClient."]
    methods:
      fetchResource: GET
      saveResource: POST
`), 0600); err != nil {
		t.Fatal(err)
	}

	source := `
public List<ResourceId> loadResources(User user, Integer resourceId) {
    // resourceClient.fetchResource(user, "/not-a-call");
    String example = "resourceClient.saveResource(user, '/not-a-call')";
    List<Map<String, Object>> resources = resourceClient.fetchResource(user,
            RESOURCE_ENDPOINT + "/" + resourceId,
            new HashMap<String, String>(),
            List.class
    );

    return resourceClient.saveResource(contextUser,
            ClientEndpoints.UPDATE_RESOURCE,
            null,
            Map.class,
            body
    );
}`

	got := extractHTTPWrapperCallExpressions(source)
	want := []httpWrapperCallExpression{
		{
			Method: "GET",
			Args: []string{
				"user",
				`RESOURCE_ENDPOINT + "/" + resourceId`,
				"new HashMap<String, String>()",
				"List.class",
			},
		},
		{
			Method: "POST",
			Args: []string{
				"contextUser",
				"ClientEndpoints.UPDATE_RESOURCE",
				"null",
				"Map.class",
				"body",
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractHTTPWrapperCallExpressions mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestBuildSqsFunctionIntegrationsQueryMatchesAliasesAzureTriggersAndQueueDataAccesses(t *testing.T) {
	t.Parallel()

	query, args := buildSqsFunctionIntegrationsQuery("repo:path:Function.run", searchWorkspaceScope{
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{42: true},
	})

	required := []string{
		"FROM resource_aliases ra",
		"FROM azure_function_triggers t",
		"FROM data_accesses da",
		"t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')",
		"LOWER(da.access) = 'read'",
		"REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i')",
		"LEFT JOIN queue_consumers c",
		"FROM queue_aliases qa",
		"TRIM(BOTH '%' FROM COALESCE(p.queue_name, ''))",
		"f.snapshot_id = ANY($2)",
		"da.snapshot_id = ANY($2)",
		"ra.snapshot_id = ANY($2)",
		"p.snapshot_id = ANY($2)",
	}
	for _, fragment := range required {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected SQS integration query to contain %q\n%s", fragment, query)
		}
	}
	if strings.Contains(query, "LEFT JOIN sqs_consumers c ON LOWER(c.queue_name) = LOWER(p.queue_name)") {
		t.Fatalf("SQS integration query regressed to raw queue-name equality join:\n%s", query)
	}
	if len(args) != 2 {
		t.Fatalf("expected caller and snapshot args, got %#v", args)
	}
}

func TestInferHTTPMethodsFromSourceDoesNotTreatGenericGettersAsHTTPCalls(t *testing.T) {
	t.Parallel()

	source := `
public String example(User user) {
    String email = user.getEmail();
    return delegatedUser.toString() + email;
}`

	got := inferHTTPMethodsFromSource(source)
	if len(got) != 0 {
		t.Fatalf("expected no inferred HTTP methods, got %#v", got)
	}
}

func TestNormalizedIntegrationPathVariantsIncludeOptionalAPIPrefix(t *testing.T) {
	got := normalizedIntegrationPathVariants("/v2/{tenantId}/resources/search")
	want := []string{
		"/v2/resources/search",
		"/api/v2/resources/search",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizedIntegrationPathVariants mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestIntegrationMeaningfulSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "APIM gateway URL",
			in:   "/:apimUrl.value/reports/:productName.value/export-audit/:filename",
			want: []string{"exportaudit"},
		},
		{
			name: "azure function endpoint",
			in:   "/api/ExportAudit/{filename}",
			want: []string{"exportaudit"},
		},
		{
			name: "all params or short",
			in:   "/:id/:name/api/v2",
			want: nil,
		},
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "common words filtered",
			in:   "/api/service/handler/create",
			want: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := integrationMeaningfulSegments(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("integrationMeaningfulSegments(%q): got %v want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("integrationMeaningfulSegments(%q)[%d]: got %q want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIntegrationPathFuzzySegmentMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{
			name:  "APIM rewrite matches azure function endpoint",
			left:  "/api/ExportAudit/{filename}",
			right: "/:apimUrl.value/reports/:productName.value/export-audit/:filename",
			want:  true,
		},
		{
			name:  "no overlap",
			left:  "/api/Users/{id}",
			right: "/reports/export-audit/:filename",
			want:  false,
		},
		{
			name:  "both empty meaningful segments",
			left:  "/api/:id",
			right: "/:token",
			want:  false,
		},
		{
			name:  "similar but different endpoint names",
			left:  "/api/ExportAuditData",
			right: "/:apimUrl/export-audit/:filename",
			want:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := integrationPathFuzzySegmentMatch(tt.left, tt.right)
			if got != tt.want {
				t.Fatalf("integrationPathFuzzySegmentMatch(%q, %q): got %v want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestSelectUnambiguousFuzzyIntegrations(t *testing.T) {
	t.Parallel()

	candidates := []scoredFunctionIntegration{
		{
			item: FunctionIntegration{
				Method:        "POST",
				Path:          "/api/ExportAudit/{filename}",
				TargetRepo:    "audit-api",
				TargetHandler: "ExportAudit",
			},
			score: 1,
		},
	}

	got := selectUnambiguousFuzzyIntegrations(candidates)
	if len(got) != 1 || got[0].TargetHandler != "ExportAudit" {
		t.Fatalf("expected one unambiguous fuzzy integration, got %+v", got)
	}
}

func TestSelectUnambiguousFuzzyIntegrationsRejectsDifferentBestPaths(t *testing.T) {
	t.Parallel()

	candidates := []scoredFunctionIntegration{
		{
			item: FunctionIntegration{
				Method:        "GET",
				Path:          "/api/users/{id}/permissions",
				TargetRepo:    "users-api",
				TargetHandler: "UsersController.permissions",
			},
			score: 1,
		},
		{
			item: FunctionIntegration{
				Method:        "GET",
				Path:          "/api/users/{id}/profile",
				TargetRepo:    "users-api",
				TargetHandler: "UsersController.profile",
			},
			score: 1,
		},
	}

	if got := selectUnambiguousFuzzyIntegrations(candidates); len(got) != 0 {
		t.Fatalf("expected ambiguous fuzzy integrations to be rejected, got %+v", got)
	}
}

func TestIntegrationMeaningfulSegmentsFiltersBroadUserAndReportWords(t *testing.T) {
	t.Parallel()

	got := integrationMeaningfulSegments("/api/users/reports/export-audit/{filename}")
	want := []string{"exportaudit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("integrationMeaningfulSegments filtered broad words: got %#v want %#v", got, want)
	}
}
