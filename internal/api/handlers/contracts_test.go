package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/trace"
)

func TestAddContractHTTPMatchTargetCountsDedupePerTargetRepo(t *testing.T) {
	counts := map[string]int{}

	addContractHTTPMatchTargetCounts(counts, "frontend", 3, []trace.MatchedEndpoint{
		{Repo: "api", Path: "/orders/{id}", Handler: "GetOrder"},
		{Repo: "api", Path: "/orders/:id", Handler: "GetOrderAlias"},
		{Repo: "billing", Path: "/orders/{id}", Handler: "GetBillingOrder"},
		{Repo: "frontend", Path: "/orders/{id}", Handler: "LocalPreview"},
		{Repo: "", Path: "/orders/{id}", Handler: "Unknown"},
	})

	if counts["api"] != 3 {
		t.Fatalf("api count = %d, want 3", counts["api"])
	}
	if counts["billing"] != 3 {
		t.Fatalf("billing count = %d, want 3", counts["billing"])
	}
	if _, ok := counts["frontend"]; ok {
		t.Fatalf("source repo should not be counted: %#v", counts)
	}
	if _, ok := counts[""]; ok {
		t.Fatalf("empty target repo should not be counted: %#v", counts)
	}
}

func TestContractDetailJSONIncludesGraphQLSurface(t *testing.T) {
	detail := ContractDetail{
		Repo: "resource-ui",
		GraphQLOperations: []ContractGraphQLOperation{
			{Name: "AllResources", Type: "query", File: "src/resources/query/AllResources.gql", Line: 1},
		},
		GraphQLUsages: []ContractGraphQLUsage{
			{ImportedAs: "allResourcesQuery", ImportPath: "./query/AllResources.gql", Caller: "ResourceList", File: "src/resources/ResourceList.vue", Line: 21},
		},
		GraphQLTargets: []ContractRepoCount{
			{Repo: "resource-api", Count: 1},
		},
		GraphQLCallers: []ContractRepoCount{
			{Repo: "resource-ui", Count: 1},
		},
		GraphQLResolvers: []ContractGraphQLResolver{
			{OperationName: "allResources", OperationType: "query", Resolver: "allResources", File: "resolvers/resources.js", Line: 38},
		},
		GraphQLPermissions: []ContractGraphQLPermission{
			{OperationName: "allResources", OperationType: "query", RuleExpression: "or(isAdmin, canReadResources)", File: "permissions.js", Line: 13},
		},
		GraphQLEntrypoints: []ContractGraphQLEntrypoint{
			{RegistrationKind: "ApolloServer", ControllersPath: "controllers", File: "server.js", Line: 93},
		},
		DataAccesses: []ContractDataAccess{
			{Entity: "blob:resource", Access: "write", Caller: "writeResource", File: "functions/WriteResource/index.js", Line: 118},
			{Entity: "document:pdf", Access: "generate", Caller: "generateDocument", File: "src/document.ts", Line: 16},
		},
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal contract detail: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"graphqlOperations"`,
		`"graphqlUsages"`,
		`"graphqlTargets"`,
		`"graphqlCallers"`,
		`"graphqlResolvers"`,
		`"graphqlPermissions"`,
		`"graphqlEntrypoints"`,
		`"dataAccesses"`,
		`"allResourcesQuery"`,
		`"or(isAdmin, canReadResources)"`,
		`"resource-api"`,
		`"blob:resource"`,
		`"document:pdf"`,
		`"ApolloServer"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("contract JSON missing %s: %s", want, body)
		}
	}
}

func TestContractSummaryJSONIncludesAzureTimerTriggerCount(t *testing.T) {
	summary := ContractSummary{
		Repo:               "resource-worker",
		AzureTimerTriggers: 3,
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal contract summary: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"azureTimerTriggers":3`) {
		t.Fatalf("contract summary JSON missing azure timer count: %s", body)
	}
}

func TestFetchHttpCallersMatchesDistinctActiveRepoSnapshots(t *testing.T) {
	ctx := context.Background()
	storage := liveTestStorage(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceSlug := "test-contract-callers-" + suffix
	targetRepo := "test-target-api-" + suffix
	callerRepo := "test-caller-ui-" + suffix
	targetPath := "/tmp/" + targetRepo
	callerPath := "/tmp/" + callerRepo

	pool := storage.Pool()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workspaces WHERE slug = $1`, workspaceSlug)
		_, _ = pool.Exec(ctx, `DELETE FROM repositories WHERE name IN ($1, $2)`, targetRepo, callerRepo)
	}()

	var workspaceID, targetRepoID, callerRepoID, targetSnapshotID, callerSnapshotID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspaces (slug, name, description)
		VALUES ($1, $2, '')
		RETURNING id
	`, workspaceSlug, workspaceSlug).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repositories (name, path, language)
		VALUES ($1, $2, 'go')
		RETURNING id
	`, targetRepo, targetPath).Scan(&targetRepoID); err != nil {
		t.Fatalf("insert target repo: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repositories (name, path, language)
		VALUES ($1, $2, 'typescript')
		RETURNING id
	`, callerRepo, callerPath).Scan(&callerRepoID); err != nil {
		t.Fatalf("insert caller repo: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repo_snapshots (workspace_id, repo_id, repo_name, branch, sha)
		VALUES ($1, $2, $3, 'main', $4)
		RETURNING id
	`, workspaceID, targetRepoID, targetRepo, "target-"+suffix).Scan(&targetSnapshotID); err != nil {
		t.Fatalf("insert target snapshot: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repo_snapshots (workspace_id, repo_id, repo_name, branch, sha)
		VALUES ($1, $2, $3, 'main', $4)
		RETURNING id
	`, workspaceID, callerRepoID, callerRepo, "caller-"+suffix).Scan(&callerSnapshotID); err != nil {
		t.Fatalf("insert caller snapshot: %v", err)
	}

	var targetFileID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (repo_id, snapshot_id, path, language)
		VALUES ($1, $2, 'handlers/orders.go', 'go')
		RETURNING id
	`, targetRepoID, targetSnapshotID).Scan(&targetFileID); err != nil {
		t.Fatalf("insert target file: %v", err)
	}
	var handlerFunctionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO functions (file_id, name, start_line, end_line)
		VALUES ($1, 'GetOrder', 10, 20)
		RETURNING id
	`, targetFileID).Scan(&handlerFunctionID); err != nil {
		t.Fatalf("insert target handler: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO endpoints (repo_id, path, path_canonical, method, method_canonical, handler_function_id, file_id, line_number)
		VALUES ($1, '/api/orders/{id}', '/api/orders/{id}', 'GET', 'GET', $2, $3, 12)
	`, targetRepoID, handlerFunctionID, targetFileID); err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO http_client_calls (repo_id, snapshot_id, caller_id, http_method, url_pattern, line_number, client_type)
		VALUES ($1, $2, $3, 'GET', '/api/orders/:id', 44, 'fetch')
	`, callerRepoID, callerSnapshotID, callerRepo+":src/orders.ts:loadOrder"); err != nil {
		t.Fatalf("insert caller http call: %v", err)
	}

	h := &Handlers{storage: storage}
	scope := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: workspaceSlug, Name: workspaceSlug},
		EnforceSnapshots: true,
		ActiveSnapshots: map[int64]bool{
			targetSnapshotID: true,
			callerSnapshotID: true,
		},
	}
	got, err := h.fetchHttpCallers(ctx, targetRepo, 10, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != callerRepo || got[0].Count != 1 {
		t.Fatalf("fetchHttpCallers() = %+v, want one caller count from distinct active snapshot %s", got, callerRepo)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.fetchHttpCallers(cancelled, targetRepo, 10, scope); err == nil {
		t.Fatal("cancelled caller lookup must return an error, not an empty success")
	}
}

func TestFetchAzureTimerTriggersKeepsLegacyRowsInDefaultWorkspace(t *testing.T) {
	ctx := context.Background()
	storage := liveTestStorage(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	repoName := "test-contract-legacy-azure-" + suffix
	repoPath := "/tmp/" + repoName
	functionName := "ContractLegacyTimer" + suffix
	pool := storage.Pool()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM repositories WHERE name = $1`, repoName)
	}()

	var repoID, fileID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO repositories (name, path, language)
		VALUES ($1, $2, 'typescript')
		RETURNING id
	`, repoName, repoPath).Scan(&repoID); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (repo_id, path, language)
		VALUES ($1, 'functions/contractLegacyTimer/function.json', 'json')
		RETURNING id
	`, repoID).Scan(&fileID); err != nil {
		t.Fatalf("insert legacy file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO azure_function_triggers (repo_id, file_id, function_name, trigger_type, direction, binding_name, schedule_expression, resource_name, line_number)
		VALUES ($1, $2, $3, 'timerTrigger', 'in', 'timer', '0 */10 * * * *', 'contract-legacy-timer', 1)
	`, repoID, fileID, functionName); err != nil {
		t.Fatalf("insert azure timer trigger: %v", err)
	}

	h := &Handlers{storage: storage}
	got, err := h.fetchAzureTimerTriggers(ctx, repoName, 10, searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: graph.DefaultWorkspaceSlug, Name: "Default Main"},
		IsDefault:        true,
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{42: true},
	})
	if err != nil {
		t.Fatalf("fetchAzureTimerTriggers: %v", err)
	}
	if len(got) != 1 || got[0].FunctionName != functionName || got[0].File != "functions/contractLegacyTimer/function.json" {
		t.Fatalf("fetchAzureTimerTriggers() = %+v, want legacy Azure timer retained in default workspace", got)
	}
}
