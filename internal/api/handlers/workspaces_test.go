package handlers

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestWorkspaceRepoNeedsMainlineCheckoutOnlyWhenOffMainline(t *testing.T) {
	cases := []struct {
		name string
		repo graph.WorkspaceRepo
		want bool
	}{
		{
			name: "release ref needs mainline checkout",
			repo: graph.WorkspaceRepo{TargetRef: "RELEASE_2026"},
			want: true,
		},
		{
			name: "master with missing snapshot does not need checkout",
			repo: graph.WorkspaceRepo{TargetRef: "master", ResolvedSHA: "abc123"},
			want: false,
		},
		{
			name: "main with missing snapshot does not need checkout",
			repo: graph.WorkspaceRepo{TargetRef: "main", ResolvedSHA: "abc123"},
			want: false,
		},
		{
			name: "empty target but resolved mainline does not need checkout",
			repo: graph.WorkspaceRepo{ResolvedBranch: "master", ResolvedSHA: "abc123"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceRepoNeedsMainlineCheckout(tc.repo); got != tc.want {
				t.Fatalf("workspaceRepoNeedsMainlineCheckout() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveWorkspaceRefFastPrefersFetchedRemoteBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	origin := filepath.Join(root, "origin.git")
	upstream := filepath.Join(root, "upstream")
	source := filepath.Join(root, "source")
	const branch = "RELEASE_2026"

	runWorkspaceTestGit(t, root, "init", "--bare", origin)
	runWorkspaceTestGit(t, root, "init", upstream)
	runWorkspaceTestGit(t, upstream, "config", "user.email", "test@example.com")
	runWorkspaceTestGit(t, upstream, "config", "user.name", "Test User")
	runWorkspaceTestGit(t, upstream, "checkout", "-b", branch)
	writeWorkspaceTestFile(t, filepath.Join(upstream, "README.md"), "one\n")
	runWorkspaceTestGit(t, upstream, "add", "README.md")
	runWorkspaceTestGit(t, upstream, "commit", "-m", "initial")
	runWorkspaceTestGit(t, upstream, "remote", "add", "origin", origin)
	runWorkspaceTestGit(t, upstream, "push", "-u", "origin", branch)

	runWorkspaceTestGit(t, root, "clone", origin, source)
	runWorkspaceTestGit(t, source, "checkout", branch)
	staleLocal := strings.TrimSpace(runWorkspaceTestGit(t, source, "rev-parse", "HEAD"))

	writeWorkspaceTestFile(t, filepath.Join(upstream, "README.md"), "two\n")
	runWorkspaceTestGit(t, upstream, "commit", "-am", "advance release")
	freshRemote := strings.TrimSpace(runWorkspaceTestGit(t, upstream, "rev-parse", "HEAD"))
	runWorkspaceTestGit(t, upstream, "push", "origin", branch)

	got, _, err := resolveWorkspaceRefFast(source, branch)
	if err != nil {
		t.Fatalf("resolveWorkspaceRefFast() error = %v", err)
	}
	if got != freshRemote {
		t.Fatalf("resolveWorkspaceRefFast() = %s, want freshly fetched remote %s", got, freshRemote)
	}
	localAfter := strings.TrimSpace(runWorkspaceTestGit(t, source, "rev-parse", branch))
	if localAfter != staleLocal {
		t.Fatalf("local branch moved during ref resolution = %s, want still stale %s", localAfter, staleLocal)
	}
}

func TestWorkspaceRepoHasIndexedCommitRequiresActiveSnapshot(t *testing.T) {
	commit := "abc123"
	if workspaceRepoHasIndexedCommit(&graph.WorkspaceRepo{ResolvedSHA: commit}, commit) {
		t.Fatal("stored SHA without active snapshot must not count as already current")
	}
	if !workspaceRepoHasIndexedCommit(&graph.WorkspaceRepo{ResolvedSHA: commit, ActiveSnapshotID: int64Ptr(42)}, commit) {
		t.Fatal("matching active snapshot should count as already current")
	}
	if workspaceRepoHasIndexedCommit(&graph.WorkspaceRepo{ResolvedSHA: "old", ActiveSnapshotID: int64Ptr(42)}, commit) {
		t.Fatal("active snapshot for a different SHA must not count as already current")
	}
}

func TestWorkspaceSourceRepoPathPrefersSourceRootForWorkspaceWorktreePath(t *testing.T) {
	root := t.TempDir()
	worktreeRoot := filepath.Join(root, "workspaces")
	sourceRoot := filepath.Join(root, "repos")
	repoName := "resource-api"
	sourcePath := filepath.Join(sourceRoot, repoName)
	staleWorkspacePath := filepath.Join(worktreeRoot, "default-main", "repos", repoName)
	if err := os.MkdirAll(filepath.Join(sourcePath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIRION_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("TIRION_REPOS_ROOT", "")
	t.Setenv("REPOS_ROOT", sourceRoot)

	got, err := workspaceSourceRepoPath(graph.RepoInfo{Name: repoName, Path: staleWorkspacePath})
	if err != nil {
		t.Fatal(err)
	}
	if got != sourcePath {
		t.Fatalf("workspaceSourceRepoPath() = %q, want source clone %q", got, sourcePath)
	}
}

func TestWorkspaceSourceRepoRootsUseExplicitConfiguration(t *testing.T) {
	t.Setenv("TIRION_REPOS_ROOT", "")
	t.Setenv("REPOS_ROOT", "")
	if roots := workspaceSourceRepoRoots(); len(roots) != 0 {
		t.Fatalf("unexpected inferred roots: %v", roots)
	}
	want := t.TempDir()
	t.Setenv("TIRION_REPOS_ROOT", want)
	t.Setenv("REPOS_ROOT", want)
	if roots := workspaceSourceRepoRoots(); len(roots) != 1 || roots[0] != want {
		t.Fatalf("expected deduplicated configured root %q, got %v", want, roots)
	}
}

func TestNormalizeBulkIndexReposDeduplicatesPreservingOrder(t *testing.T) {
	got := normalizeBulkIndexRepos([]string{" resource-api ", "", "RESOURCE-API", "Inventory-Api"})
	want := []string{"resource-api", "Inventory-Api"}
	if len(got) != len(want) {
		t.Fatalf("normalizeBulkIndexRepos() length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeBulkIndexRepos()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsTransientWorkspaceIndexError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadlock sqlstate", err: errors.New("ERROR: deadlock detected (SQLSTATE 40P01)"), want: true},
		{name: "serialization sqlstate", err: errors.New("ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)"), want: true},
		{name: "normal parse failure", err: errors.New("parse binary failed"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientWorkspaceIndexError(tc.err); got != tc.want {
				t.Fatalf("isTransientWorkspaceIndexError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkspaceBulkIndexJobTracksProgress(t *testing.T) {
	h := &Handlers{
		workspaceBulkIndex: &workspaceBulkIndexJob{
			ID:        "job-1",
			Workspace: "release",
			Status:    "running",
			Total:     2,
			Results: []bulkIndexWorkspaceRepoResult{
				{Repo: "resource-api", Status: "queued"},
				{Repo: "Inventory-Api", Status: "queued"},
			},
		},
	}

	h.updateWorkspaceBulkIndexResult("job-1", 0, "indexing", "", "2026-05-10T10:00:00Z", "")
	snapshot := h.workspaceBulkIndexSnapshot("release")
	if snapshot == nil || snapshot.CurrentRepo != "resource-api" {
		t.Fatalf("CurrentRepo = %#v, want resource-api", snapshot)
	}

	h.updateWorkspaceBulkIndexResult("job-1", 0, "ok", "", "2026-05-10T10:00:00Z", "2026-05-10T10:01:00Z")
	h.updateWorkspaceBulkIndexResult("job-1", 1, "failed", "boom", "2026-05-10T10:01:00Z", "2026-05-10T10:02:00Z")
	h.finishWorkspaceBulkIndexJob("job-1", nil)

	snapshot = h.workspaceBulkIndexSnapshot("release")
	if snapshot == nil {
		t.Fatal("workspaceBulkIndexSnapshot() = nil")
	}
	if snapshot.Status != "failed" || snapshot.Completed != 2 || snapshot.Failed != 1 {
		t.Fatalf("job status/completed/failed = %s/%d/%d, want failed/2/1", snapshot.Status, snapshot.Completed, snapshot.Failed)
	}
	if snapshot.Results[1].Error != "boom" {
		t.Fatalf("failed result error = %q, want boom", snapshot.Results[1].Error)
	}
}

func TestApplyWorkspaceRepoIndexedStateUsesInheritedMainlineSnapshot(t *testing.T) {
	indexedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	row := adminRepoRow{UpdatedAt: indexedAt.Add(-time.Hour).Format(time.RFC3339), FreshnessStatus: "stale"}
	wr := graph.WorkspaceRepo{
		TargetRef:        "master",
		ResolvedBranch:   "master",
		ResolvedSHA:      "workspace-head",
		ActiveSnapshotID: int64Ptr(42),
		LastIndexedAt:    &indexedAt,
	}

	if !applyWorkspaceRepoIndexedState(&row, wr, indexedAt.Add(time.Hour)) {
		t.Fatal("applyWorkspaceRepoIndexedState() = false, want true")
	}
	if row.IndexedBranch != "master" {
		t.Fatalf("IndexedBranch = %q, want master", row.IndexedBranch)
	}
	if row.IndexedSHA != "workspace-head" {
		t.Fatalf("IndexedSHA = %q, want workspace-head", row.IndexedSHA)
	}
	if row.IndexedAt == "" {
		t.Fatal("IndexedAt is empty")
	}
}

func TestApplyWorkspaceRepoIndexedStateUsesMainlineSnapshotFields(t *testing.T) {
	indexedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	row := adminRepoRow{CurrentBranch: "master", HeadSHA: "mainline-indexed"}
	wr := graph.WorkspaceRepo{
		TargetRef:             "master",
		ResolvedBranch:        "master",
		ResolvedSHA:           "release-head",
		MainlineSnapshotID:    int64Ptr(7),
		MainlineResolvedSHA:   "mainline-indexed",
		MainlineLastIndexedAt: &indexedAt,
	}

	if !applyWorkspaceRepoIndexedState(&row, wr, indexedAt.Add(time.Hour)) {
		t.Fatal("applyWorkspaceRepoIndexedState() = false, want true")
	}
	if row.IndexedSHA != "mainline-indexed" {
		t.Fatalf("IndexedSHA = %q, want mainline-indexed", row.IndexedSHA)
	}
	if row.IndexedBranch != "master" {
		t.Fatalf("IndexedBranch = %q, want master", row.IndexedBranch)
	}
}

func TestApplyWorkspaceRepoIndexedStateNormalizesStaleBranchLabelWhenSHAIsCurrent(t *testing.T) {
	indexedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	row := adminRepoRow{CurrentBranch: "master", HeadSHA: "abc123"}
	wr := graph.WorkspaceRepo{
		TargetRef:             "RELEASE_2026",
		ResolvedBranch:        "RELEASE_2026",
		MainlineSnapshotID:    int64Ptr(7),
		MainlineResolvedSHA:   "abc123",
		MainlineLastIndexedAt: &indexedAt,
	}

	if !applyWorkspaceRepoIndexedState(&row, wr, indexedAt.Add(time.Hour)) {
		t.Fatal("applyWorkspaceRepoIndexedState() = false, want true")
	}
	if row.IndexedBranch != "master" {
		t.Fatalf("IndexedBranch = %q, want master", row.IndexedBranch)
	}
}

func TestWorkspaceReleaseRepoWithoutSnapshotShowsParseNeeded(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch:   "RELEASE_2026",
		HeadSHA:         "release-head",
		FreshnessStatus: "recent",
	}
	wr := graph.WorkspaceRepo{
		TargetRef:      "RELEASE_2026",
		ResolvedBranch: "RELEASE_2026",
		ResolvedSHA:    "release-head",
		IndexStatus:    "configured",
	}

	applyAdminWorkspaceDriftState(&row, "release", wr, true)

	if row.DriftStatus != "drift" {
		t.Fatalf("DriftStatus = %q, want drift", row.DriftStatus)
	}
	if !row.ReparseNeeded {
		t.Fatal("ReparseNeeded = false, want true")
	}
	if row.FreshnessStatus != "stale" {
		t.Fatalf("FreshnessStatus = %q, want stale", row.FreshnessStatus)
	}
}

func TestIndexedBranchLabelForInheritedSnapshotUsesWorkspaceBranchWhenSHAIsCurrent(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch: "RELEASE_2026",
		HeadSHA:       "abc123",
	}
	snapshot := graph.WorkspaceSnapshotRef{
		Branch: "master",
		SHA:    "abc123",
	}

	got := indexedBranchLabelForSnapshot("release-workspace", row, snapshot)
	if got != "RELEASE_2026" {
		t.Fatalf("indexedBranchLabelForSnapshot() = %q, want RELEASE_2026", got)
	}
}

func TestIndexedBranchLabelForInheritedSnapshotKeepsSnapshotBranchWhenSHADiffers(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch: "RELEASE_2026",
		HeadSHA:       "release-head",
	}
	snapshot := graph.WorkspaceSnapshotRef{
		Branch: "master",
		SHA:    "mainline-head",
	}

	got := indexedBranchLabelForSnapshot("release-workspace", row, snapshot)
	if got != "master" {
		t.Fatalf("indexedBranchLabelForSnapshot() = %q, want master", got)
	}
}

func TestIndexedBranchLabelForDefaultWorkspaceKeepsSnapshotBranch(t *testing.T) {
	row := adminRepoRow{
		CurrentBranch: "RELEASE_2026",
		HeadSHA:       "abc123",
	}
	snapshot := graph.WorkspaceSnapshotRef{
		Branch: "master",
		SHA:    "abc123",
	}

	got := indexedBranchLabelForSnapshot(graph.DefaultWorkspaceSlug, row, snapshot)
	if got != "master" {
		t.Fatalf("indexedBranchLabelForSnapshot() = %q, want master", got)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func writeWorkspaceTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runWorkspaceTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
