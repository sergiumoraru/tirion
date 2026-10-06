package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendWorkspaceProvenanceContract(t *testing.T) {
	t.Parallel()

	checks := []struct {
		path string
		want []string
	}{
		{
			path: "frontend/src/views/SearchView.vue",
			want: []string{"results.workspace.id", "results.repoContext?.length", "snapshotLabel(op.snapshotId", "snapshotLabel(trigger.snapshotId"},
		},
		{
			path: "frontend/src/views/TraceView.vue",
			want: []string{"result.workspace", "result.workspace.id", "result.repoContext?.length"},
		},
		{
			path: "frontend/src/views/FlowView.vue",
			want: []string{"result.workspace", "result.workspace.id", "result.repoContext?.length"},
		},
		{
			path: "frontend/src/views/ImpactView.vue",
			want: []string{"result.workspace", "result.workspace.id", "result.repoContext?.length"},
		},
		{
			path: "frontend/src/views/ContractsView.vue",
			want: []string{"detailWorkspaceId", "detailRepoContextCount"},
		},
		{
			path: "frontend/src/views/AdminView.vue",
			want: []string{"Fetch Refs", "fetchWorkspaceRepo", "api.fetchWorkspaceRepo", "api.bulkIndexWorkspace", "activeWorkspaceBulkIndex"},
		},
	}

	for _, check := range checks {
		check := check
		t.Run(check.path, func(t *testing.T) {
			t.Parallel()
			content := readRepoFile(t, check.path)
			for _, want := range check.want {
				if !strings.Contains(content, want) {
					t.Fatalf("%s does not contain required workspace provenance marker %q", check.path, want)
				}
			}
		})
	}
}

func TestFrontendContractsSurfaceContract(t *testing.T) {
	t.Parallel()

	view := readRepoFile(t, "frontend/src/views/ContractsView.vue")
	for _, want := range []string{
		"GraphQL Operations",
		"GraphQL Usages",
		"GraphQL Resolvers",
		"GraphQL Permissions",
		"GraphQL Entrypoints",
		"Data Access",
		"EventBridge Triggers",
		"Azure Timer Triggers",
		"summaryGraphQLCount",
		"dataAccessCount",
		"azureTimerTriggers",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("ContractsView.vue does not expose expanded contract surface marker %q", want)
		}
	}

	types := readRepoFile(t, "frontend/src/types/index.ts")
	for _, want := range []string{
		"ContractGraphQLOperation",
		"ContractGraphQLUsage",
		"ContractGraphQLResolver",
		"ContractGraphQLPermission",
		"ContractGraphQLEntrypoint",
		"ContractDataAccess",
		"ContractSchedule",
		"eventBridgeTriggers",
		"azureTimerTriggers",
	} {
		if !strings.Contains(types, want) {
			t.Fatalf("frontend contracts types do not model expanded contract surface marker %q", want)
		}
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", rel)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(content)
}
