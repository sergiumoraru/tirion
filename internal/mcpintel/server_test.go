package mcpintel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCodebaseToolsExposeWorkspaceID(t *testing.T) {
	t.Parallel()

	server := &mcpServer{}
	tools := server.tools()

	for _, name := range []string{"codebase_search", "codebase_trace", "codebase_flow", "codebase_graphql_flow", "codebase_impact", "codebase_verify", "codebase_contracts"} {
		assertToolProperty(t, findTool(t, tools, name), "workspaceId")
	}
}

func TestCodebaseGraphQLFlowToolUsesAPIResponseShape(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotQuery string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/graphql/flow" {
			t.Fatalf("path = %s, want /api/graphql/flow", r.URL.Path)
		}
		if r.URL.Query().Get("operation") != "AllResources" {
			t.Fatalf("operation = %q, want AllResources", r.URL.Query().Get("operation"))
		}
		if r.URL.Query().Get("workspaceId") != "release-workspace" {
			t.Fatalf("workspaceId = %q, want release-workspace", r.URL.Query().Get("workspaceId"))
		}
		writeTestJSON(t, w, map[string]any{
			"workspace": map[string]any{"id": "release-workspace"},
			"operations": []map[string]any{{
				"operation_name": "AllResources",
				"operation_type": "query",
				"repo_name":      "resource-ui",
				"file_path":      "src/queries/AllResources.gql",
				"line":           1,
			}},
			"usages": []map[string]any{{
				"imported_as":             "allResourcesQuery",
				"resolved_operation_name": "AllResources",
				"repo_name":               "resource-ui",
				"file_path":               "src/views/ResourceList.vue",
				"line":                    21,
				"caller_function":         "ResourceList.vue",
			}},
			"resolvers": []map[string]any{{
				"operation_name": "AllResources",
				"operation_type": "query",
				"resolver_name":  "allResources",
				"repo_name":      "resource-api",
				"file_path":      "src/controllers/Resource.js",
				"line":           42,
			}},
			"entrypoints": []map[string]any{{
				"handler_name":      "resource-api",
				"repo_name":         "resource-api",
				"file_path":         "src/server.js",
				"match_confidence":  "medium",
				"controllers_count": 1,
			}},
			"controllers": []map[string]any{{
				"controller_file_path":  "src/controllers/Resource.js",
				"repo_name":             "resource-api",
				"resolution_confidence": "exact",
			}},
			"inferences": []string{"backend entrypoints are matched heuristically"},
		})
	}))
	defer api.Close()

	server := &mcpServer{apiBaseURL: api.URL, client: api.Client()}
	result := server.callTool("codebase_graphql_flow", map[string]any{
		"operation":   "AllResources",
		"workspaceId": "release-workspace",
	})
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if gotPath == "" || !strings.Contains(gotQuery, "operation=AllResources") {
		t.Fatalf("request not observed correctly path=%q query=%q", gotPath, gotQuery)
	}
	text := result.Content[0].Text
	for _, want := range []string{
		`GraphQL flow "AllResources": 1 operations, 1 frontend usages, 1 resolvers, 1 backend entrypoints, 1 controller files`,
		"Workspace: release-workspace",
		"query AllResources [resource-ui src/queries/AllResources.gql:1]",
		"allResourcesQuery -> AllResources used by ResourceList.vue",
		"query allResources [resource-api src/controllers/Resource.js:42]",
		"resource-api [resource-api src/server.js confidence=medium]",
		"src/controllers/Resource.js [resource-api confidence=exact]",
		"backend entrypoints are matched heuristically",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("GraphQL flow text missing %q in:\n%s", want, text)
		}
	}
}

func TestCodebaseImpactToolPrintsDataSideEffects(t *testing.T) {
	t.Parallel()

	var gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/api/impact" {
			t.Fatalf("path = %s, want /api/impact", r.URL.Path)
		}
		writeTestJSON(t, w, map[string]any{
			"workspace": map[string]any{"id": "release-workspace"},
			"roots":     []string{"resource-worker:src/cleanup.ts:cleanup"},
			"rootDetails": []map[string]any{{
				"callerId": "resource-worker:src/cleanup.ts:cleanup",
				"repo":     "resource-worker",
				"file":     "src/cleanup.ts",
				"name":     "cleanup",
			}},
			"summary": map[string]any{
				"entrypoints":          []map[string]any{},
				"httpCalls":            []map[string]any{},
				"queues":               []map[string]any{},
				"eventBridgeSchedules": []map[string]any{},
				"azureTimerSchedules":  []map[string]any{},
				"repos":                []map[string]any{{"name": "resource-worker", "minDepth": 0, "directlyAffected": true, "fanout": 1, "score": 1.0}},
				"dataAccesses": []map[string]any{{
					"entity":           "blob:resource",
					"access":           "write",
					"minDepth":         1,
					"directlyAffected": false,
					"fanout":           2,
					"score":            0.5,
					"callers":          []string{"resource-worker:src/export.ts:exportResource"},
				}},
			},
			"stats": map[string]any{"downstreamNodes": 2, "upstreamNodes": 0, "totalNodes": 2, "totalEdges": 1, "impactTime": "1ms"},
		})
	}))
	defer api.Close()

	server := &mcpServer{apiBaseURL: api.URL, client: api.Client()}
	result := server.callTool("codebase_impact", map[string]any{
		"functions":   []any{"resource-worker:src/cleanup.ts:cleanup"},
		"workspaceId": "release-workspace",
	})
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if gotPath != "/api/impact" {
		t.Fatalf("request not observed correctly path=%q", gotPath)
	}
	text := result.Content[0].Text
	for _, want := range []string{
		"1 data/side effects",
		"Roots:",
		"cleanup [resource-worker src/cleanup.ts]",
		"resource-worker  [direct depth=0 fanout=1 score=1.00]",
		"Data / side-effect resources:",
		"write blob:resource  [transitive depth=1 fanout=2 score=0.50]",
		"callers: resource-worker:src/export.ts:exportResource",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("impact text missing %q in:\n%s", want, text)
		}
	}
}

func TestCodebaseVerifyToolPrintsVerdictAndUsesCompactPayload(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotVerify bool
	var gotAnchor map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/api/verify" {
			t.Fatalf("path = %s, want /api/verify", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotVerify, _ = body["verify"].(bool)
		gotAnchor, _ = body["anchor"].(map[string]any)
		writeTestJSON(t, w, map[string]any{
			"workspace":   map[string]any{"id": "release-workspace"},
			"roots":       []string{"api:controller.ts:updateUser"},
			"rootDetails": []map[string]any{{"callerId": "api:controller.ts:updateUser", "repo": "api", "file": "controller.ts", "name": "updateUser"}},
			"stats":       map[string]any{"roots": 1, "totalNodes": 1},
			"repoContext": []map[string]any{{"repoName": "should-not-be-returned"}},
			"report":      map[string]any{"nodes": []map[string]any{{"id": "large-node"}}},
			"verify": map[string]any{
				"verdict": "fail",
				"mode":    "fix_validation",
				"changeValidation": map[string]any{
					"mode":          "fix_validation",
					"fixValidation": "failed",
					"anchor": map[string]any{
						"kind": "endpoint", "ref": "POST /users/{id}", "status": "resolved",
					},
					"connection": map[string]any{
						"changedPathsToBoundary": 1, "untouchedPathsToBoundary": 2,
					},
					"obligations": []map[string]any{
						{"id": "O1", "status": "satisfied"},
						{"id": "O2-1", "status": "missing"},
					},
				},
				"agentWork": map[string]any{
					"status": "verified",
					"claims": []map[string]any{{"id": "REQ-1", "verdict": "grounded"}},
				},
				"breakingChanges": []map[string]any{{
					"severity":      "high",
					"endpoint":      "POST /users/{id}",
					"removedParams": []string{"status"},
					"file":          "controller.ts",
				}},
				"exposedSurface": map[string]any{
					"endpoints": []map[string]any{{"endpoint": "POST /users/{id}", "consumerRepos": 2}},
				},
				"reasons": []string{"POST /users/{id} removed 'status' and has 2 external consumer repo(s)"},
			},
		})
	}))
	defer api.Close()

	server := &mcpServer{apiBaseURL: api.URL, client: api.Client()}
	result := server.callTool("codebase_verify", map[string]any{
		"repo":                 "api",
		"diff":                 "diff --git a/controller.ts b/controller.ts\n",
		"workspaceId":          "release-workspace",
		"anchor":               map[string]any{"kind": "endpoint", "ref": "POST /users/{id}"},
		"task":                 "Update user contract",
		"rootCause":            "The endpoint contract is stale",
		"requirementsComplete": true,
		"rootCauseEvidence":    []map[string]any{{"file": "controller.ts", "symbol": "updateUser"}},
		"claims":               []map[string]any{{"id": "REQ-1", "requirement": "Update user contract", "status": "covered"}},
	})
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if gotPath != "/api/verify" || !gotVerify {
		t.Fatalf("request path=%q verify=%v, want /api/verify verify=true", gotPath, gotVerify)
	}
	if gotAnchor == nil || gotAnchor["kind"] != "endpoint" || gotAnchor["ref"] != "POST /users/{id}" {
		t.Fatalf("anchor not forwarded: %#v", gotAnchor)
	}
	text := result.Content[0].Text
	for _, want := range []string{"Agent change verify: fail", "fix_validation; fix=failed; obligations=2", "anchor: resolved endpoint:POST /users/{id}", "boundary paths: changed=1 untouched=2", "proof: verified", "high POST /users/{id} removed status", "2 external consumer repo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("verify text missing %q in:\n%s", want, text)
		}
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content has type %T", result.StructuredContent)
	}
	if _, ok := structured["report"]; ok {
		t.Fatalf("codebase_verify should return compact payload without report: %#v", structured)
	}
	if _, ok := structured["repoContext"]; ok {
		t.Fatalf("codebase_verify should return compact payload without repoContext: %#v", structured)
	}
}

func TestCodebaseContractsToolPrintsGraphQLDataAndSchedules(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotWorkspace string
	graphQLResolvers := []map[string]any{{
		"operationName": "allResources",
		"operationType": "query",
		"resolver":      "allResources",
		"file":          "controllers/Resource.js",
		"line":          38,
	}}
	for i := 2; i <= 12; i++ {
		graphQLResolvers = append(graphQLResolvers, map[string]any{
			"operationName": "extraResolver" + strconv.Itoa(i),
			"operationType": "query",
			"resolver":      "extraResolver" + strconv.Itoa(i),
			"file":          "controllers/Extra.js",
			"line":          i,
		})
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotWorkspace = r.URL.Query().Get("workspaceId")
		if r.URL.Path != "/api/contracts/resource-api" {
			t.Fatalf("path = %s, want /api/contracts/resource-api", r.URL.Path)
		}
		writeTestJSON(t, w, map[string]any{
			"workspace": map[string]any{"id": "release-workspace"},
			"service": map[string]any{
				"repo":      "resource-api",
				"endpoints": []map[string]any{},
				"httpCalls": []map[string]any{},
				"graphqlOperations": []map[string]any{{
					"name": "AllResources",
					"type": "query",
					"file": "query/AllResources.gql",
					"line": 1,
				}},
				"graphqlUsages": []map[string]any{{
					"importedAs": "allResourcesQuery",
					"caller":     "ResourceList",
					"file":       "ResourceList.vue",
					"line":       21,
				}},
				"graphqlResolvers": graphQLResolvers,
				"graphqlPermissions": []map[string]any{{
					"operationName":  "allResources",
					"operationType":  "query",
					"ruleExpression": "or(isAdmin, canReadResources)",
					"file":           "permissions.js",
					"line":           13,
				}},
				"graphqlEntrypoints": []map[string]any{{
					"registrationKind": "ApolloServer",
					"controllersPath":  "controllers",
					"file":             "server.js",
					"line":             93,
				}},
				"dataAccesses": []map[string]any{{
					"entity": "blob:resource",
					"access": "write",
					"caller": "createResource",
					"file":   "CreateResource/index.js",
					"line":   118,
				}},
				"queuesProduced": []map[string]any{},
				"queuesConsumed": []map[string]any{},
				"azureTimerTriggers": []map[string]any{{
					"functionName":       "CleanupResources",
					"scheduleExpression": "0 0 0 * * *",
					"file":               "CleanupResources/function.json",
					"line":               5,
				}},
			},
		})
	}))
	defer api.Close()

	server := &mcpServer{apiBaseURL: api.URL, client: api.Client()}
	result := server.callTool("codebase_contracts", map[string]any{
		"repo":        "resource-api",
		"workspaceId": "release-workspace",
	})
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if gotPath != "/api/contracts/resource-api" || gotWorkspace != "release-workspace" {
		t.Fatalf("request path/workspace = %q/%q", gotPath, gotWorkspace)
	}
	text := result.Content[0].Text
	for _, want := range []string{
		"12 GraphQL resolvers",
		"1 GraphQL entrypoints",
		"Workspace: release-workspace",
		"query AllResources",
		"allResourcesQuery in ResourceList",
		"query allResources -> allResources",
		"... and 2 more GraphQL resolvers",
		"query allResources guarded by or(isAdmin, canReadResources)",
		"ApolloServer controllers=controllers",
		"write blob:resource by createResource",
		"CleanupResources  0 0 0 * * *",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("contracts text missing %q in:\n%s", want, text)
		}
	}
}

func TestWorkspaceForArgsUsesExplicitBeforeServerDefault(t *testing.T) {
	t.Parallel()

	server := &mcpServer{workspace: "prod-main"}
	if got := server.workspaceForArgs(map[string]any{"workspaceId": "release-investigation"}); got != "release-investigation" {
		t.Fatalf("explicit workspace = %q, want release-investigation", got)
	}
	if got := server.workspaceForArgs(nil); got != "prod-main" {
		t.Fatalf("default workspace = %q, want prod-main", got)
	}
}

func TestAppendWorkspaceHintsFormatsFoundWorkspaces(t *testing.T) {
	t.Parallel()

	resp := map[string]any{
		"workspaceHints": []any{
			map[string]any{
				"kind": "symbol_exists_elsewhere",
				"foundIn": []any{
					map[string]any{
						"workspaceId": "release-investigation",
						"repo":        "resource-api",
						"branch":      "RELEASE_2026",
						"symbol":      "ResourceRepository.findById",
						"file":        "src/main/java/example/ResourceRepository.java",
					},
				},
			},
		},
	}

	var sb strings.Builder
	appendWorkspaceHints(&sb, resp)
	got := sb.String()
	for _, want := range []string{
		"Workspace hints:",
		"symbol_exists_elsewhere",
		"release-investigation resource-api@RELEASE_2026 ResourceRepository.findById",
		"[src/main/java/example/ResourceRepository.java]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("workspace hints text missing %q in:\n%s", want, got)
		}
	}
}
