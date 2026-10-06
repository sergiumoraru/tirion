package handlers

import (
	"testing"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestRepoIndexAlignmentStatus(t *testing.T) {
	tests := []struct {
		name        string
		row         adminRepoRow
		wantStatus  string
		wantReasons []string
	}{
		{
			name: "fresh when branch and commit match",
			row: adminRepoRow{
				WorkspaceAvailable: true,
				CurrentBranch:      "main",
				HeadSHA:            "abc123",
				IndexedBranch:      "main",
				IndexedSHA:         "abc123",
			},
			wantStatus: "fresh",
		},
		{
			name: "stale on commit drift",
			row: adminRepoRow{
				WorkspaceAvailable: true,
				CurrentBranch:      "main",
				HeadSHA:            "def456",
				IndexedBranch:      "main",
				IndexedSHA:         "abc123",
				ReparseNeeded:      true,
			},
			wantStatus:  "stale",
			wantReasons: []string{"commit_drift"},
		},
		{
			name: "stale on branch drift",
			row: adminRepoRow{
				WorkspaceAvailable: true,
				CurrentBranch:      "feature/refund",
				HeadSHA:            "abc123",
				IndexedBranch:      "main",
				IndexedSHA:         "abc123",
				BranchDrift:        true,
			},
			wantStatus:  "stale",
			wantReasons: []string{"branch_drift"},
		},
		{
			name: "stale on dirty workspace",
			row: adminRepoRow{
				WorkspaceAvailable: true,
				CurrentBranch:      "main",
				HeadSHA:            "abc123",
				IndexedBranch:      "main",
				IndexedSHA:         "abc123",
				Dirty:              true,
			},
			wantStatus:  "stale",
			wantReasons: []string{"dirty_workspace"},
		},
		{
			name: "unknown when indexed state is missing",
			row: adminRepoRow{
				WorkspaceAvailable: true,
				CurrentBranch:      "main",
				HeadSHA:            "abc123",
			},
			wantStatus:  "unknown",
			wantReasons: []string{"missing_indexed_state", "missing_indexed_branch"},
		},
		{
			name:       "unknown when workspace is unavailable",
			row:        adminRepoRow{},
			wantStatus: "unknown",
			wantReasons: []string{
				"workspace_unavailable",
				"missing_indexed_state",
				"missing_indexed_branch",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotReasons := repoIndexAlignmentStatus(tt.row)
			if gotStatus != tt.wantStatus {
				t.Fatalf("status = %q, want %q", gotStatus, tt.wantStatus)
			}
			if !sameStrings(gotReasons, tt.wantReasons) {
				t.Fatalf("reasons = %#v, want %#v", gotReasons, tt.wantReasons)
			}
		})
	}
}

func TestApplyAdminWorkspaceDriftStateMarksNonMainlineWorkspaceWithoutIndexStale(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch: "feature/mission",
		HeadSHA:       "def456",
	}
	workspaceRepo := testWorkspaceRepo("feature/mission", "feature/mission", "def456")

	applyAdminWorkspaceDriftState(&row, "release-workspace", workspaceRepo, true)

	if !row.ReparseNeeded {
		t.Fatalf("expected workspace repo without indexed SHA to need reparse")
	}
	if row.DriftStatus != "drift" {
		t.Fatalf("drift status = %q, want drift", row.DriftStatus)
	}
	if row.FreshnessStatus != "stale" {
		t.Fatalf("freshness status = %q, want stale", row.FreshnessStatus)
	}
}

func TestApplyAdminWorkspaceDriftStateDoesNotInventBranchDriftForInheritedMainlineSnapshot(t *testing.T) {
	row := adminRepoRow{
		WorkspaceAvailable: true,
		CurrentBranch:      "feature/main-copy",
		HeadSHA:            "abc123",
		IndexedBranch:      "feature/main-copy",
		IndexedSHA:         "abc123",
		IndexedCommit:      "abc123",
		WorkspaceBranch:    "feature/main-copy",
		WorkspaceCommit:    "abc123",
	}
	workspaceRepo := testWorkspaceRepo("feature/main-copy", "feature/main-copy", "abc123")
	workspaceRepo.MainlineResolvedSHA = "abc123"

	applyAdminWorkspaceDriftState(&row, "release-workspace", workspaceRepo, true)
	row.IndexAlignmentStatus, row.IndexAlignmentReasons = repoIndexAlignmentStatus(row)

	if row.BranchDrift {
		t.Fatalf("expected inherited mainline snapshot not to report branch drift")
	}
	if row.ReparseNeeded {
		t.Fatalf("expected matching inherited mainline snapshot not to need reparse")
	}
	if row.DriftStatus != "in_sync" {
		t.Fatalf("drift status = %q, want in_sync", row.DriftStatus)
	}
	if row.IndexAlignmentStatus != "fresh" {
		t.Fatalf("index alignment status = %q, want fresh; reasons=%#v", row.IndexAlignmentStatus, row.IndexAlignmentReasons)
	}
}

func TestAdminRepoRowWorkspaceAliasesCanUseResolvedWorkspaceStateWithoutInspection(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch: "feature/mission",
		HeadSHA:       "def456",
	}

	applyAdminWorkspaceAliases(&row)

	if row.WorkspaceBranch != "feature/mission" {
		t.Fatalf("workspaceBranch = %q, want resolved current branch", row.WorkspaceBranch)
	}
	if row.WorkspaceCommit != "def456" {
		t.Fatalf("workspaceCommit = %q, want resolved head SHA", row.WorkspaceCommit)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func testWorkspaceRepo(targetRef, resolvedBranch, resolvedSHA string) graph.WorkspaceRepo {
	return graph.WorkspaceRepo{
		TargetRef:      targetRef,
		ResolvedBranch: resolvedBranch,
		ResolvedSHA:    resolvedSHA,
	}
}
