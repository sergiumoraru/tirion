package graph

import (
	"strings"
	"testing"
)

func TestCloneWorkspaceRepoSelectionsCopiesLogicalBaselineSnapshot(t *testing.T) {
	t.Parallel()

	insertHead := cloneWorkspaceRepoSelectionsSQL
	if idx := strings.Index(insertHead, ")"); idx >= 0 {
		insertHead = insertHead[:idx]
	}
	if !strings.Contains(insertHead, "active_snapshot_id") {
		t.Fatal("workspace clone must carry active snapshots as logical baseline entries")
	}
	if !strings.Contains(cloneWorkspaceRepoSelectionsSQL, "EXCLUDED.active_snapshot_id") {
		t.Fatal("workspace clone upsert must preserve the inherited active snapshot")
	}
	if strings.Contains(cloneWorkspaceRepoSelectionsSQL, "active_snapshot_id = NULL") {
		t.Fatal("workspace clone must not clear inherited baseline snapshots")
	}
	if !strings.Contains(cloneWorkspaceRepoSelectionsSQL, "last_indexed_at = EXCLUDED.last_indexed_at") {
		t.Fatal("workspace clone must preserve baseline indexed-at provenance")
	}
}
