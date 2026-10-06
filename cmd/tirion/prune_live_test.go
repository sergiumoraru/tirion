package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/indexer"
)

type pruneFixture struct {
	t       *testing.T
	storage *graph.Storage
	dbURL   string
	bin     string
	suffix  string
	root    string
}

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	dbURL, bin := liveEnvironment(t, "TIRION_WORKSPACE_LIVE_TESTS", true)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	f := &pruneFixture{t: t, storage: storage, dbURL: dbURL, bin: bin, suffix: fmt.Sprintf("prune%d", time.Now().UnixNano()), root: filepath.Join(t.TempDir(), "repos")}
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := storage.Pool().Exec(ctx, `DELETE FROM workspaces WHERE slug LIKE $1`, f.suffix+"-%"); err != nil {
			t.Errorf("cleanup workspaces: %v", err)
		}
		if _, err := storage.Pool().Exec(ctx, `DELETE FROM repositories WHERE name LIKE $1`, f.suffix+"/%"); err != nil {
			t.Errorf("cleanup repositories: %v", err)
		}
		storage.Close()
	})
	return f
}

func (f *pruneFixture) workspace(name string) *graph.Workspace {
	f.t.Helper()
	slug := f.suffix + "-" + name
	ws, err := f.storage.EnsureWorkspace(slug, slug, "", "", false)
	if err != nil {
		f.t.Fatal(err)
	}
	return ws
}

// repoName is the registered name for a directory relative to the root.
func (f *pruneFixture) repoName(rel string) string { return f.suffix + "/" + rel }

func (f *pruneFixture) dir(rel string) string {
	f.t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
	source := fmt.Sprintf("function %s() { return 1; }\n", strings.NewReplacer("/", "_", "-", "_").Replace(rel))
	if err := os.WriteFile(filepath.Join(path, "app.js"), []byte(source), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *pruneFixture) index(ws *graph.Workspace, rel, path string) {
	f.t.Helper()
	_, err := indexer.RunRepositories(indexer.PipelineOptions{
		DBURL: f.dbURL, Workspace: ws.Slug, ParseBinary: filepath.Join(f.bin, "parse"),
		Repos: []indexer.RepoEntry{{Name: f.repoName(rel), Path: path}}, SkipTests: true, SkipExtractors: true, Resolve: true,
	})
	if err != nil {
		f.t.Fatalf("index %s into %s: %v", rel, ws.Slug, err)
	}
}

func (f *pruneFixture) prune(ws *graph.Workspace, root string, apply bool) []graph.StaleRepo {
	f.t.Helper()
	stale, err := f.storage.PruneStaleRepos(context.Background(), ws.Slug, func(path string) bool { return pathUnderAnyRoot([]string{root}, path) }, registeredPathMissing, apply)
	if err != nil {
		f.t.Fatal(err)
	}
	return stale
}

func (f *pruneFixture) selected(ws *graph.Workspace) []string {
	f.t.Helper()
	rows, err := f.storage.Pool().Query(context.Background(), `SELECT repo_name FROM workspace_repos WHERE workspace_id=$1 ORDER BY repo_name`, ws.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			f.t.Fatal(err)
		}
		names = append(names, strings.TrimPrefix(name, f.suffix+"/"))
	}
	return names
}

func (f *pruneFixture) activeSnapshot(ws *graph.Workspace, rel string) int64 {
	f.t.Helper()
	refs, err := f.storage.ActiveSnapshotsForWorkspace(ws.Slug)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, ref := range refs {
		if ref.RepoName == f.repoName(rel) {
			return ref.SnapshotID
		}
	}
	f.t.Fatalf("%s has no active snapshot for %s", ws.Slug, rel)
	return 0
}

func (f *pruneFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.storage.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *pruneFixture) filesIn(snapshot int64) int {
	return f.count(`SELECT count(*) FROM files WHERE snapshot_id=$1`, snapshot)
}

func (f *pruneFixture) snapshotExists(snapshot int64) bool {
	return f.count(`SELECT count(*) FROM repo_snapshots WHERE id=$1`, snapshot) == 1
}

func (f *pruneFixture) repositoryExists(rel string) bool {
	return f.count(`SELECT count(*) FROM repositories WHERE name=$1`, f.repoName(rel)) == 1
}

func find(stale []graph.StaleRepo, name string) *graph.StaleRepo {
	for i := range stale {
		if stale[i].Name == name {
			return &stale[i]
		}
	}
	return nil
}

func TestPruneIsWorkspaceScopedAndKeepsSnapshotsOthersSelect(t *testing.T) {
	f := newPruneFixture(t)
	a, b := f.workspace("a"), f.workspace("b")
	shared := f.dir("shared")
	nested := f.dir("group/svc") // two levels below the root: the old prune missed these
	f.dir("keep")
	f.index(a, "shared", shared)
	f.index(a, "group/svc", nested)
	f.index(a, "keep", filepath.Join(f.root, "keep"))
	// B selects A's snapshots (workspace create --from); only A indexes onlyA.
	if err := f.storage.CloneWorkspaceRepoSelections(a.Slug, b.ID); err != nil {
		t.Fatal(err)
	}
	onlyA := f.dir("onlyA")
	f.index(a, "onlyA", onlyA)

	sharedSnapshot := f.activeSnapshot(b, "shared")
	nestedSnapshot := f.activeSnapshot(b, "group/svc")
	onlyASnapshot := f.activeSnapshot(a, "onlyA")
	if sharedSnapshot != f.activeSnapshot(a, "shared") {
		t.Fatal("fixture: B should select A's snapshot")
	}
	for _, path := range []string{shared, nested, onlyA} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	wantA := []string{"group/svc", "keep", "onlyA", "shared"}
	if got := f.selected(a); !slices.Equal(got, wantA) {
		t.Fatalf("fixture selection of A = %v", got)
	}

	// Dry run: reports the plan and changes nothing.
	plan := f.prune(a, f.root, false)
	if len(plan) != 3 || find(plan, f.repoName("keep")) != nil {
		t.Fatalf("plan should list exactly the three vanished repositories (nested one included), got %+v", plan)
	}
	if entry := find(plan, f.repoName("shared")); entry == nil || len(entry.RemovedSnapshots) != 0 || len(entry.KeptSnapshots) != 1 ||
		entry.KeptSnapshots[0].ID != sharedSnapshot || !strings.Contains(entry.KeptSnapshots[0].Reason, b.Slug) || entry.RepositoryRemoved || !entry.Selected {
		t.Fatalf("shared repository plan wrong: %+v", entry)
	}
	if entry := find(plan, f.repoName("onlyA")); entry == nil || len(entry.RemovedSnapshots) != 1 || entry.RemovedSnapshots[0] != onlyASnapshot || !entry.RepositoryRemoved {
		t.Fatalf("onlyA plan wrong: %+v", entry)
	}
	if got := f.selected(a); !slices.Equal(got, wantA) || !f.snapshotExists(onlyASnapshot) || !f.repositoryExists("onlyA") {
		t.Fatalf("a dry run changed the database: selection=%v", got)
	}

	// Apply for A.
	f.prune(a, f.root, true)
	if got := f.selected(a); !slices.Equal(got, []string{"keep"}) {
		t.Fatalf("A's selection after prune = %v, want only keep", got)
	}
	if f.snapshotExists(onlyASnapshot) || f.repositoryExists("onlyA") {
		t.Fatal("onlyA was selected by no other workspace and should be fully removed")
	}
	wantB := []string{"group/svc", "keep", "shared"}
	if got := f.selected(b); !slices.Equal(got, wantB) {
		t.Fatalf("pruning A altered B's selection: %v", got)
	}
	for _, snapshot := range []int64{sharedSnapshot, nestedSnapshot} {
		if !f.snapshotExists(snapshot) || f.filesIn(snapshot) == 0 {
			t.Fatalf("snapshot %d is still selected by B and must keep its facts", snapshot)
		}
	}
	if !f.repositoryExists("shared") || !f.repositoryExists("group/svc") {
		t.Fatal("repository records still used by B were removed")
	}

	// Pruning B now releases the last references, including A-owned snapshots.
	f.prune(b, f.root, true)
	if got := f.selected(b); !slices.Equal(got, []string{"keep"}) {
		t.Fatalf("B's selection after prune = %v", got)
	}
	for _, snapshot := range []int64{sharedSnapshot, nestedSnapshot} {
		if f.snapshotExists(snapshot) {
			t.Fatalf("snapshot %d is selected by nobody now and should be gone", snapshot)
		}
	}
	if f.repositoryExists("shared") || f.repositoryExists("group/svc") {
		t.Fatal("repositories nobody uses any more should be removed")
	}
	if !f.repositoryExists("keep") || len(f.selected(a)) != 1 {
		t.Fatal("the repository whose directory still exists must be untouched")
	}
	if again := f.prune(a, f.root, true); len(again) != 0 {
		t.Fatalf("a second prune has nothing left to do, got %+v", again)
	}
}

func TestPruneLeavesSameNamedRepositoryOfAnotherWorkspaceAlone(t *testing.T) {
	f := newPruneFixture(t)
	a, c := f.workspace("a"), f.workspace("c")
	// Both workspaces index a repository of the same name from their own checkout.
	aCheckout := f.dir("x")
	cRoot := filepath.Join(filepath.Dir(f.root), "c-checkouts")
	cCheckout := filepath.Join(cRoot, "x")
	if err := os.MkdirAll(cCheckout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cCheckout, "app.js"), []byte("function x() { return 2; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.index(a, "x", aCheckout)
	f.index(c, "x", cCheckout)
	aSnapshot, cSnapshot := f.activeSnapshot(a, "x"), f.activeSnapshot(c, "x")
	if err := os.RemoveAll(aCheckout); err != nil {
		t.Fatal(err)
	}

	f.prune(a, f.root, true)
	if len(f.selected(a)) != 0 || f.snapshotExists(aSnapshot) {
		t.Fatal("A's own selection and snapshot of the vanished checkout should be removed")
	}
	if got := f.selected(c); !slices.Equal(got, []string{"x"}) || !f.snapshotExists(cSnapshot) || f.filesIn(cSnapshot) == 0 {
		t.Fatalf("C's checkout still exists; its selection (%v) and snapshot must be intact", got)
	}
	if !f.repositoryExists("x") {
		t.Fatal("the shared repository record is still used by C")
	}
	// C's path is outside A's root, and within C's own root it is not stale.
	if stale := f.prune(c, cRoot, true); len(stale) != 0 {
		t.Fatalf("nothing is stale for C: %+v", stale)
	}
	// A root that does not contain the repository never selects it, stale or not.
	if err := os.RemoveAll(cCheckout); err != nil {
		t.Fatal(err)
	}
	if stale := f.prune(c, f.root, true); len(stale) != 0 {
		t.Fatalf("a repository outside the root must be ignored: %+v", stale)
	}
	if stale := f.prune(c, cRoot, false); len(stale) != 1 {
		t.Fatalf("within its own root C's checkout is stale: %+v", stale)
	}
}

func TestPruneRefusesWhileAnotherRunHoldsTheIndexingLock(t *testing.T) {
	f := newPruneFixture(t)
	a := f.workspace("a")
	path := f.dir("svc")
	f.index(a, "svc", path)
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	lock, err := pgx.ConnectConfig(context.Background(), f.storage.Pool().Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec(context.Background(), `SELECT pg_advisory_lock(741926,$1::int)`, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.storage.PruneStaleRepos(context.Background(), a.Slug, func(string) bool { return true }, registeredPathMissing, true)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("pruning during an index run must refuse, got %v", err)
	}
	if len(f.selected(a)) != 1 {
		t.Fatal("a refused prune must not change anything")
	}
	lock.Close(context.Background()) // releases the session lock
	f.prune(a, f.root, true)
	if len(f.selected(a)) != 0 {
		t.Fatal("prune should succeed once the lock is released")
	}
	// And it must not leave its own lock behind.
	again, err := pgx.ConnectConfig(context.Background(), f.storage.Pool().Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close(context.Background())
	var locked bool
	if err := again.QueryRow(context.Background(), `SELECT pg_try_advisory_lock(741926,$1::int)`, a.ID).Scan(&locked); err != nil || !locked {
		t.Fatalf("prune leaked the indexing lock: locked=%v err=%v", locked, err)
	}
}
