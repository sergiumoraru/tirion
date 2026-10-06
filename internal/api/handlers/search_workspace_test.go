package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestPartitionWorkspaceResultsKeepsOnlyActiveSnapshots(t *testing.T) {
	t.Parallel()

	active := int64(1)
	other := int64(2)
	legacy := graph.SearchResult{Name: "Legacy"}
	inScope := graph.SearchResult{Name: "InScope", SnapshotID: &active}
	outOfScope := graph.SearchResult{Name: "OutOfScope", SnapshotID: &other}

	scope := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "release-workspace"},
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{active: true},
	}

	got, excluded := partitionWorkspaceResults([]graph.SearchResult{legacy, inScope, outOfScope}, scope, nil)
	if len(got) != 1 || got[0].Name != "InScope" {
		t.Fatalf("expected only active snapshot result, got %#v", got)
	}
	if len(excluded) != 1 || excluded[0].Name != "OutOfScope" {
		t.Fatalf("expected inactive snapshot excluded for cross-workspace hints, got %#v", excluded)
	}
}

func TestPartitionWorkspaceResultsAllowsLegacyOnlyForDefaultWorkspace(t *testing.T) {
	t.Parallel()

	legacy := graph.SearchResult{Name: "Legacy"}
	scope := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: graph.DefaultWorkspaceSlug},
		EnforceSnapshots: true,
		IsDefault:        true,
		ActiveSnapshots:  map[int64]bool{},
	}

	got, excluded := partitionWorkspaceResults([]graph.SearchResult{legacy}, scope, nil)
	if len(got) != 1 || got[0].Name != "Legacy" {
		t.Fatalf("expected default workspace to retain legacy unscoped rows, got %#v", got)
	}
	if len(excluded) != 0 {
		t.Fatalf("expected no excluded rows, got %#v", excluded)
	}

	mutableDefault := searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "release-workspace"},
		EnforceSnapshots: true,
		IsDefault:        true,
		ActiveSnapshots:  map[int64]bool{},
	}
	got, _ = partitionWorkspaceResults([]graph.SearchResult{legacy}, mutableDefault, nil)
	if len(got) != 0 {
		t.Fatalf("expected legacy rows to remain tied to %s, got %#v", graph.DefaultWorkspaceSlug, got)
	}
}

func TestSearchResultsHaveLegacyUnscopedRows(t *testing.T) {
	t.Parallel()

	snapshotID := int64(42)
	if searchResultsHaveLegacyUnscopedRows(SearchResults{
		Functions: []FunctionResult{{Name: "Scoped", SnapshotID: &snapshotID}},
	}) {
		t.Fatal("expected scoped results not to report legacy unscoped rows")
	}

	if !searchResultsHaveLegacyUnscopedRows(SearchResults{
		Functions: []FunctionResult{{Name: "Legacy"}},
	}) {
		t.Fatal("expected nil snapshot result to report legacy unscoped rows")
	}
}

func TestWorkspaceCallerIDAllowMapAllowsGraphQLUsageSyntheticCaller(t *testing.T) {
	ctx := context.Background()
	storage := liveTestStorage(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	repoName := "test-gql-usage-" + suffix
	repoPath := t.TempDir()
	filePath := "src/views/ResourceList.vue"
	pool := storage.Pool()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workspaces WHERE slug = $1`, "test-gql-usage-ws-"+suffix)
		_, _ = pool.Exec(ctx, `DELETE FROM repositories WHERE name = $1`, repoName)
	}()

	var workspaceID, repoID, snapshotID, fileID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspaces (slug, name, description)
		VALUES ($1, $1, '')
		RETURNING id
	`, "test-gql-usage-ws-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repositories (name, path, language)
		VALUES ($1, $2, 'typescript')
		RETURNING id
	`, repoName, repoPath).Scan(&repoID); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO repo_snapshots (workspace_id, repo_id, repo_name, branch, sha)
		VALUES ($1, $2, $3, 'main', $4)
		RETURNING id
	`, workspaceID, repoID, repoName, "sha-"+suffix).Scan(&snapshotID); err != nil {
		t.Fatalf("insert repo snapshot: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (repo_id, snapshot_id, path, language)
		VALUES ($1, $2, $3, 'vue')
		RETURNING id
	`, repoID, snapshotID, filePath).Scan(&fileID); err != nil {
		t.Fatalf("insert file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO graphql_operation_usages (repo_id, file_id, import_path, imported_as, caller_function, line_number)
		VALUES ($1, $2, './query/AllResources.gql', 'allResourcesQuery', '_template_', 21)
		ON CONFLICT DO NOTHING
	`, repoID, fileID); err != nil {
		t.Fatalf("insert graphql usage: %v", err)
	}

	h := &Handlers{storage: storage}
	callerID := repoName + ":" + filePath + ":_template_"
	allowed := h.workspaceCallerIDAllowMap(ctx, []string{callerID}, searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "test-workspace"},
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{snapshotID: true},
	})
	if !allowed[callerID] {
		t.Fatalf("expected GraphQL usage synthetic caller to be allowed, got %#v", allowed)
	}
}

func TestSearchSchedulesKeepsEventBridgeGlobalInEnforcedWorkspace(t *testing.T) {
	ctx := context.Background()
	storage := liveTestStorage(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	ruleName := "test-global-eventbridge-" + suffix
	pool := storage.Pool()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM eventbridge_schedules WHERE rule_name = $1`, ruleName)
	}()

	if _, err := pool.Exec(ctx, `
		INSERT INTO eventbridge_schedules (rule_name, schedule_expression, target_type, target_name, description, state, source)
		VALUES ($1, 'rate(5 minutes)', 'lambda', 'global-audit-worker', 'test fixture', 'ENABLED', 'eventbridge')
	`, ruleName); err != nil {
		t.Fatalf("insert eventbridge schedule: %v", err)
	}

	h := &Handlers{storage: storage}
	got := h.searchSchedules(ruleName, 10, searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: "test-enforced-workspace"},
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{42: true},
	})
	if len(got) != 1 || got[0].RuleName != ruleName || got[0].Source != "eventbridge" {
		t.Fatalf("searchSchedules() = %+v, want global EventBridge schedule retained in enforced workspace", got)
	}
	if got[0].SnapshotID != nil {
		t.Fatalf("EventBridge schedule should remain global/unscoped, got snapshot %d", *got[0].SnapshotID)
	}
}

func TestSearchAzureTimerSchedulesKeepsLegacyRowsInDefaultWorkspace(t *testing.T) {
	ctx := context.Background()
	storage := liveTestStorage(t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	repoName := "test-legacy-azure-" + suffix
	repoPath := t.TempDir()
	functionName := "LegacyTimer" + suffix
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
		VALUES ($1, 'functions/legacyTimer/function.json', 'json')
		RETURNING id
	`, repoID).Scan(&fileID); err != nil {
		t.Fatalf("insert legacy file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO azure_function_triggers (repo_id, file_id, function_name, trigger_type, direction, binding_name, schedule_expression, resource_name, line_number)
		VALUES ($1, $2, $3, 'timerTrigger', 'in', 'timer', '0 */5 * * * *', 'legacy-timer-resource', 1)
	`, repoID, fileID, functionName); err != nil {
		t.Fatalf("insert azure timer trigger: %v", err)
	}

	h := &Handlers{storage: storage}
	got := h.searchAzureTimerSchedules(functionName, 10, searchWorkspaceScope{
		Workspace:        ResponseWorkspace{ID: graph.DefaultWorkspaceSlug, Name: "Default Main"},
		IsDefault:        true,
		EnforceSnapshots: true,
		ActiveSnapshots:  map[int64]bool{42: true},
	})
	if len(got) != 1 || got[0].RuleName != functionName || got[0].Source != "functions/legacyTimer/function.json" {
		t.Fatalf("searchAzureTimerSchedules() = %+v, want legacy Azure timer retained in default workspace", got)
	}
	if got[0].SnapshotID != nil {
		t.Fatalf("legacy Azure timer should have nil snapshot, got %d", *got[0].SnapshotID)
	}
}
