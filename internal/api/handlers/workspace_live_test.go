package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
)

func TestWorkspaceLiveSearchIsolatesSameRepoSnapshots(t *testing.T) {
	storage := liveTestStorage(t)

	repoPath := t.TempDir()
	suffix := strings.ToLower(filepath.Base(filepath.Dir(repoPath))) + "-" + filepath.Base(repoPath)
	repoName := "tirion-workspace-live-" + suffix
	masterWS := repoName + "-master"
	releaseWS := repoName + "-release"
	repoID, err := storage.InsertRepositoryWithPathPolicy(repoName, repoPath, nil, nil, true)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := storage.Pool().Exec(cleanupCtx, `DELETE FROM workspaces WHERE slug = ANY($1)`, []string{masterWS, releaseWS}); err != nil {
			t.Errorf("clean up workspaces: %v", err)
		}
		if _, err := storage.Pool().Exec(cleanupCtx, `DELETE FROM repositories WHERE id = $1`, repoID); err != nil {
			t.Errorf("clean up repository: %v", err)
		}
	}()

	master, err := storage.EnsureWorkspace(masterWS, masterWS, "", "", false)
	if err != nil {
		t.Fatalf("ensure master workspace: %v", err)
	}
	release, err := storage.EnsureWorkspace(releaseWS, releaseWS, "", "", false)
	if err != nil {
		t.Fatalf("ensure release workspace: %v", err)
	}
	masterSnapshot, err := storage.EnsureRepoSnapshot(graph.RepoSnapshotInput{
		WorkspaceID: master.ID,
		RepoID:      repoID,
		RepoName:    repoName,
		Branch:      "master",
		SHA:         "master-" + suffix,
		IndexedAt:   time.Now().UTC(),
		Status:      "ok",
	})
	if err != nil {
		t.Fatalf("ensure master snapshot: %v", err)
	}
	releaseSnapshot, err := storage.EnsureRepoSnapshot(graph.RepoSnapshotInput{
		WorkspaceID: release.ID,
		RepoID:      repoID,
		RepoName:    repoName,
		Branch:      "RELEASE_X",
		SHA:         "release-" + suffix,
		IndexedAt:   time.Now().UTC(),
		Status:      "ok",
	})
	if err != nil {
		t.Fatalf("ensure release snapshot: %v", err)
	}
	insertLiveSearchFunction(t, storage, repoID, masterSnapshot.ID, "src/main/ResourceRepository.java", "ResourceRepository.findAll")
	insertLiveSearchFunction(t, storage, repoID, releaseSnapshot.ID, "src/main/ResourceRepository.java", "ResourceRepository.findReleaseResources")

	now := time.Now().UTC()
	if _, err := storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:      master.ID,
		RepoName:         repoName,
		TargetRef:        "master",
		ResolvedBranch:   "master",
		ResolvedSHA:      masterSnapshot.SHA,
		WorktreePath:     filepath.Join(repoPath, "master"),
		ActiveSnapshotID: &masterSnapshot.ID,
		LastIndexedAt:    &now,
		IndexStatus:      "ok",
	}); err != nil {
		t.Fatalf("upsert master workspace repo: %v", err)
	}
	if _, err := storage.UpsertWorkspaceRepo(graph.UpsertWorkspaceRepoInput{
		WorkspaceID:      release.ID,
		RepoName:         repoName,
		TargetRef:        "RELEASE_X",
		ResolvedBranch:   "RELEASE_X",
		ResolvedSHA:      releaseSnapshot.SHA,
		WorktreePath:     filepath.Join(repoPath, "release"),
		ActiveSnapshotID: &releaseSnapshot.ID,
		LastIndexedAt:    &now,
		IndexStatus:      "ok",
	}); err != nil {
		t.Fatalf("upsert release workspace repo: %v", err)
	}

	handler := New(storage, os.Getenv("DATABASE_URL"))
	masterResp := searchLiveWorkspace(t, handler, masterWS, "findReleaseResources")
	if len(masterResp.Results.Functions) != 0 {
		t.Fatalf("master workspace leaked release-only function: %#v", masterResp.Results.Functions)
	}
	if len(masterResp.WorkspaceHints) == 0 || len(masterResp.WorkspaceHints[0].FoundIn) == 0 {
		t.Fatalf("expected exists_elsewhere hint for release-only function, got %#v", masterResp.WorkspaceHints)
	}
	if got := masterResp.WorkspaceHints[0].FoundIn[0].WorkspaceID; got != releaseWS {
		t.Fatalf("expected hint in %s, got %s", releaseWS, got)
	}

	releaseResp := searchLiveWorkspace(t, handler, releaseWS, "findReleaseResources")
	if len(releaseResp.Results.Functions) != 1 {
		t.Fatalf("release workspace should return release-only function, got %#v", releaseResp.Results.Functions)
	}
	masterTrace := traceLiveWorkspace(t, handler, masterWS, "ResourceRepository.findReleaseResources")
	if !masterTrace.NotFound || len(masterTrace.WorkspaceHints) == 0 {
		t.Fatalf("master trace should be not found with release workspace hint, got notFound=%v hints=%#v", masterTrace.NotFound, masterTrace.WorkspaceHints)
	}
	releaseTrace := traceLiveWorkspace(t, handler, releaseWS, "ResourceRepository.findReleaseResources")
	if releaseTrace.NotFound || len(releaseTrace.Matches) != 1 {
		t.Fatalf("release trace should resolve release-only function, got notFound=%v matches=%#v", releaseTrace.NotFound, releaseTrace.Matches)
	}
	masterImpact := impactLiveWorkspace(t, handler, masterWS, "ResourceRepository.findReleaseResources")
	if len(masterImpact.Roots) != 0 || len(masterImpact.WorkspaceHints) == 0 {
		t.Fatalf("master impact should have zero roots with release workspace hint, got roots=%#v hints=%#v", masterImpact.Roots, masterImpact.WorkspaceHints)
	}
	releaseImpact := impactLiveWorkspace(t, handler, releaseWS, "ResourceRepository.findReleaseResources")
	if len(releaseImpact.Roots) != 1 {
		t.Fatalf("release impact should resolve release-only root, got %#v", releaseImpact.Roots)
	}
	masterFlow := flowLiveWorkspace(t, handler, masterWS, "ResourceRepository.findReleaseResources")
	if len(masterFlow.Roots) != 0 || len(masterFlow.WorkspaceHints) == 0 {
		t.Fatalf("master flow should have zero roots with release workspace hint, got roots=%#v hints=%#v", masterFlow.Roots, masterFlow.WorkspaceHints)
	}
	releaseFlow := flowLiveWorkspace(t, handler, releaseWS, "ResourceRepository.findReleaseResources")
	if len(releaseFlow.Roots) != 1 {
		t.Fatalf("release flow should resolve release-only root, got %#v", releaseFlow.Roots)
	}

	if err := storage.DeleteFilesByRepoSnapshot(repoID, masterSnapshot.ID); err != nil {
		t.Fatalf("delete master snapshot files: %v", err)
	}
	insertLiveSearchFunction(t, storage, repoID, masterSnapshot.ID, "src/main/ResourceRepository.java", "ResourceRepository.findAllAfterReindex")
	releaseRespAfterMasterReindex := searchLiveWorkspace(t, handler, releaseWS, "findReleaseResources")
	if len(releaseRespAfterMasterReindex.Results.Functions) != 1 {
		t.Fatalf("release snapshot was lost after master snapshot reindex simulation, got %#v", releaseRespAfterMasterReindex.Results.Functions)
	}
}

func insertLiveSearchFunction(t *testing.T, storage *graph.Storage, repoID, snapshotID int64, path, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var fileID int64
	if err := storage.Pool().QueryRow(ctx, `
		INSERT INTO files (repo_id, snapshot_id, path, language)
		VALUES ($1, $2, $3, 'java')
		RETURNING id
	`, repoID, snapshotID, path).Scan(&fileID); err != nil {
		t.Fatalf("insert file %s: %v", path, err)
	}
	simpleName := name[strings.LastIndex(name, ".")+1:]
	if _, err := storage.Pool().Exec(ctx, `
		INSERT INTO functions (file_id, name, name_canonical, simple_name, start_line, end_line, source_code)
		VALUES ($1, $2, lower($2), $3, 1, 1, $4)
	`, fileID, name, simpleName, "public void "+simpleName+"() {}"); err != nil {
		t.Fatalf("insert function %s: %v", name, err)
	}
}

func searchLiveWorkspace(t *testing.T, handler *Handlers, workspaceSlug, query string) SearchResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/search?workspaceId="+workspaceSlug+"&q="+query+"&limit=10", nil)
	rr := httptest.NewRecorder()
	handler.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status %d: %s", rr.Code, rr.Body.String())
	}
	var resp SearchResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	return resp
}

func traceLiveWorkspace(t *testing.T, handler *Handlers, workspaceSlug, function string) TraceResponse {
	t.Helper()
	body, err := json.Marshal(TraceRequest{WorkspaceID: workspaceSlug, Function: function, Depth: 1, MaxNodes: 20})
	if err != nil {
		t.Fatalf("encode trace request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/trace", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Trace(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("trace status %d: %s", rr.Code, rr.Body.String())
	}
	var resp TraceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	return resp
}

func impactLiveWorkspace(t *testing.T, handler *Handlers, workspaceSlug, function string) ImpactResponse {
	t.Helper()
	body, err := json.Marshal(ImpactRequest{WorkspaceID: workspaceSlug, Functions: []string{function}, Depth: 1, MaxNodes: 20})
	if err != nil {
		t.Fatalf("encode impact request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/impact", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Impact(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("impact status %d: %s", rr.Code, rr.Body.String())
	}
	var resp ImpactResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode impact response: %v", err)
	}
	return resp
}

func flowLiveWorkspace(t *testing.T, handler *Handlers, workspaceSlug, start string) FlowResponse {
	t.Helper()
	body, err := json.Marshal(FlowRequest{WorkspaceID: workspaceSlug, Start: start, Depth: 1, MaxHops: 20})
	if err != nil {
		t.Fatalf("encode flow request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/flow", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.Flow(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("flow status %d: %s", rr.Code, rr.Body.String())
	}
	var resp FlowResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode flow response: %v", err)
	}
	return resp
}
