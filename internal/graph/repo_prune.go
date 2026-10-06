package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// StalePathFunc reports whether a repository's recorded source path is gone.
// It must return an error, not true, when existence cannot be established.
type StalePathFunc func(path string) (bool, error)

// KeptSnapshot is a snapshot of a stale repository that pruning must leave in
// place because something else still depends on it.
type KeptSnapshot struct {
	ID     int64
	Reason string
}

// StaleRepo describes the pruning outcome for one repository whose source
// directory no longer exists.
type StaleRepo struct {
	Name string
	Path string
	// Selected is true when the workspace selects the repository (its
	// workspace_repos row is removed); false for an orphan repository that no
	// workspace selects and no snapshot references.
	Selected bool
	// RemovedSnapshots are this workspace's snapshots of the repository that are
	// (or, on a dry run, would be) deleted.
	RemovedSnapshots []int64
	// KeptSnapshots are referenced by another workspace's selection or by
	// retained snapshots, and so are not deleted.
	KeptSnapshots []KeptSnapshot
	// RepositoryRemoved is true when no workspace selects the repository and no
	// snapshot of it remains, so the repository row and its facts are removed.
	RepositoryRemoved bool
}

type staleRepoCandidate struct {
	id       int64
	name     string
	path     string
	selected bool
}

// pruneRelations lists the edges that can point from one snapshot's rows into
// another's. It mirrors the relation table in PruneSnapshots; keep them in step.
var pruneRelations = [][5]string{
	{"function_calls", "functions", "caller_function_id", "functions", "callee_function_id"},
	{"file_imports", "files", "file_id", "files", "imports_file_id"},
	{"endpoints", "files", "file_id", "functions", "handler_function_id"},
	{"implementations", "classes", "class_id", "interfaces", "interface_id"},
	{"method_signatures", "functions", "function_id", "functions", "overrides_method_id"},
	{"http_interface_methods", "interfaces", "interface_id", "functions", "function_id"},
	{"bean_definitions", "classes", "config_class_id", "functions", "method_id"},
	{"event_listeners", "classes", "class_id", "functions", "method_id"},
	{"scheduled_methods", "classes", "class_id", "functions", "method_id"},
	{"jpa_relationships", "classes", "source_class_id", "classes", "target_class_id"},
	{"fields", "classes", "class_id", "interfaces", "interface_id"},
	{"fields", "interfaces", "interface_id", "classes", "class_id"},
	{"ibmi_exports", "files", "file_id", "functions", "function_id"},
	{"graphql_usage_operation_links", "graphql_operation_usages", "usage_id", "graphql_operations", "operation_id"},
	{"graphql_backend_controller_links", "graphql_backend_entrypoints", "entrypoint_id", "files", "file_id"},
	{"graphql_backend_controller_links", "files", "file_id", "graphql_backend_entrypoints", "entrypoint_id"},
}

// PruneStaleRepos removes repositories of one workspace whose source directory
// no longer exists, without disturbing anything another workspace uses.
//
// Scope: only the named workspace's selection (workspace_repos row) and that
// workspace's own snapshots of the repository are removed. A snapshot is kept
// when another workspace selects it (workspace create --from shares snapshots) or
// when a retained snapshot still has rows pointing into it, using the same
// closure as PruneSnapshots. The shared repositories row and repository-level
// facts are removed only once no workspace selects the repository and no
// snapshot of it remains. Repositories that nothing references (no selection, no
// snapshot) and whose recorded path is stale are removed as well, which also lets
// an interrupted prune be completed by running it again.
//
// stale decides existence from each repository's path, taken from its active
// snapshot's source_path, then the workspace worktree path, then the registered
// path. inRoot restricts which repositories are considered at all.
//
// Locking matches indexing and publication: the per-workspace indexing lock
// (741926, workspace id) is taken without waiting for every workspace, because
// removal consults other workspaces' selections, then the publication lock
// (741927, 0). Locks are held for the whole run, including a dry run, and
// released when the dedicated connection closes.
func (s *Storage) PruneStaleRepos(ctx context.Context, workspaceSlug string, inRoot func(path string) bool, stale StalePathFunc, apply bool) ([]StaleRepo, error) {
	ws, err := s.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", workspaceSlug, err)
	}
	lock, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	defer lock.Close(context.Background())
	workspaceIDs, err := queryInt64s(ctx, lock, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for _, id := range workspaceIDs {
		var locked bool
		if err := lock.QueryRow(ctx, `SELECT pg_try_advisory_lock(741926,$1::int)`, id).Scan(&locked); err != nil {
			return nil, err
		}
		if !locked {
			return nil, fmt.Errorf("workspace %d is busy (indexing or pruning); retry after it finishes", id)
		}
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(741927,0)`); err != nil {
		return nil, err
	}

	candidates, err := s.staleRepoCandidates(ctx, ws.ID, inRoot, stale)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	selectedIDs := []int64{}
	selectedNames := []string{}
	for _, c := range candidates {
		if c.selected {
			selectedIDs = append(selectedIDs, c.id)
			selectedNames = append(selectedNames, c.name)
		}
	}
	// Snapshots in scope: this workspace's own snapshots of the stale repositories,
	// plus whatever its stale selections point at (a workspace created with
	// --from selects snapshots owned by another workspace). Of those, only the ones
	// no other selection keeps: this workspace's stale rows are being removed and
	// do not count.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE prune_repo_scope ON COMMIT DROP AS
 SELECT s.id,s.repo_id FROM repo_snapshots s WHERE s.workspace_id=$1 AND s.repo_id=ANY($2)
 UNION
 SELECT s.id,s.repo_id FROM workspace_repos w JOIN repo_snapshots s ON s.id IN (w.active_snapshot_id,w.mainline_snapshot_id)
 WHERE w.workspace_id=$1 AND w.repo_name=ANY($3)`, ws.ID, selectedIDs, selectedNames); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE prune_repo_candidates ON COMMIT DROP AS
 SELECT s.id,s.repo_id FROM prune_repo_scope s
 WHERE NOT EXISTS(SELECT 1 FROM workspace_repos w WHERE (w.active_snapshot_id=s.id OR w.mainline_snapshot_id=s.id)
   AND NOT (w.workspace_id=$1 AND w.repo_name=ANY($2)))`, ws.ID, selectedNames); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `CREATE UNIQUE INDEX ON prune_repo_candidates(id)`); err != nil {
		return nil, err
	}
	var dependencies []string
	for _, r := range pruneRelations {
		sourceJoin := "LEFT JOIN " + r[1] + " src ON src.id=edge." + r[2]
		if r[1] == "files" {
			sourceJoin = "LEFT JOIN files sf ON sf.id=edge." + r[2]
		} else {
			sourceJoin += " LEFT JOIN files sf ON sf.id=src.file_id"
		}
		targetJoin := "JOIN " + r[3] + " dst ON dst.id=edge." + r[4]
		if r[3] == "files" {
			targetJoin = "JOIN files tf ON tf.id=edge." + r[4]
		} else {
			targetJoin += " JOIN files tf ON tf.id=dst.file_id"
		}
		sourcePresent := ""
		if r[0] == "fields" {
			sourcePresent = " AND edge." + r[2] + " IS NOT NULL"
		}
		dependencies = append(dependencies, "SELECT sf.snapshot_id AS source,tf.snapshot_id AS target FROM "+r[0]+" edge "+sourceJoin+" "+targetJoin+" JOIN prune_repo_candidates c ON c.id=tf.snapshot_id WHERE sf.snapshot_id IS DISTINCT FROM tf.snapshot_id"+sourcePresent)
	}
	for _, r := range [][3]string{{"trace_call_edges", "functions", "caller_function_id"}, {"trace_call_edges", "functions", "callee_function_id"}, {"trace_interface_impls", "classes", "class_id"}, {"ibmi_exports", "functions", "function_id"}} {
		dependencies = append(dependencies, "SELECT edge.snapshot_id AS source,tf.snapshot_id AS target FROM "+r[0]+" edge JOIN "+r[1]+" dst ON dst.id=edge."+r[2]+" JOIN files tf ON tf.id=dst.file_id JOIN prune_repo_candidates c ON c.id=tf.snapshot_id WHERE edge.snapshot_id IS DISTINCT FROM tf.snapshot_id")
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE prune_repo_dependencies ON COMMIT DROP AS `+strings.Join(dependencies, " UNION ")); err != nil {
		return nil, err
	}
	for {
		tag, err := tx.Exec(ctx, `DELETE FROM prune_repo_candidates c USING prune_repo_dependencies d WHERE d.target=c.id AND NOT EXISTS(SELECT 1 FROM prune_repo_candidates source WHERE source.id=d.source)`)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			break
		}
	}

	results := make([]StaleRepo, len(candidates))
	byID := map[int64]int{}
	for i, c := range candidates {
		results[i] = StaleRepo{Name: c.name, Path: c.path, Selected: c.selected}
		byID[c.id] = i
	}
	type snapshotRow struct {
		id, repoID int64
		removed    bool
		selectedBy *string
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.repo_id,EXISTS(SELECT 1 FROM prune_repo_candidates c WHERE c.id=s.id),
 (SELECT string_agg(DISTINCT w2.slug,', ' ORDER BY w2.slug) FROM workspace_repos w JOIN workspaces w2 ON w2.id=w.workspace_id
   WHERE (w.active_snapshot_id=s.id OR w.mainline_snapshot_id=s.id) AND NOT (w.workspace_id=$1 AND w.repo_name=ANY($2)))
 FROM prune_repo_scope s ORDER BY s.id`, ws.ID, selectedNames)
	if err != nil {
		return nil, err
	}
	var snapshotRows []snapshotRow
	for rows.Next() {
		var row snapshotRow
		if err := rows.Scan(&row.id, &row.repoID, &row.removed, &row.selectedBy); err != nil {
			rows.Close()
			return nil, err
		}
		snapshotRows = append(snapshotRows, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, row := range snapshotRows {
		i := byID[row.repoID]
		switch {
		case row.removed:
			results[i].RemovedSnapshots = append(results[i].RemovedSnapshots, row.id)
		case row.selectedBy != nil:
			results[i].KeptSnapshots = append(results[i].KeptSnapshots, KeptSnapshot{ID: row.id, Reason: "selected by workspace " + *row.selectedBy})
		default:
			results[i].KeptSnapshots = append(results[i].KeptSnapshots, KeptSnapshot{ID: row.id, Reason: "referenced by retained snapshots"})
		}
	}
	// A repository is removed outright only when nothing would still use it after
	// this run: no other workspace selects it and no snapshot of it survives.
	for i, c := range candidates {
		var inUse bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_repos w WHERE w.repo_name=$2 AND NOT (w.workspace_id=$1 AND $3::boolean))
 OR EXISTS(SELECT 1 FROM repo_snapshots s WHERE s.repo_id=$4 AND NOT EXISTS(SELECT 1 FROM prune_repo_candidates c WHERE c.id=s.id))`,
			ws.ID, c.name, c.selected, c.id).Scan(&inUse); err != nil {
			return nil, err
		}
		results[i].RepositoryRemoved = !inUse
	}
	if !apply {
		return results, nil
	}

	for i, result := range results {
		for _, id := range result.RemovedSnapshots {
			if err := deleteFileMetadata(ctx, tx, candidates[i].id, &id, nil); err != nil {
				return nil, err
			}
		}
	}
	if len(selectedNames) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM workspace_repos WHERE workspace_id=$1 AND repo_name=ANY($2)`, ws.ID, selectedNames); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_snapshots s USING prune_repo_candidates c WHERE s.id=c.id`); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Repository-level facts are not snapshot-scoped. Nothing selects or
	// references these repositories any more, and the locks are still held.
	for i, c := range candidates {
		if !results[i].RepositoryRemoved {
			continue
		}
		if err := s.DeleteFilesByRepo(c.id); err != nil {
			return results, fmt.Errorf("delete indexed facts for %s: %w", c.name, err)
		}
		if err := s.DeleteRepositoryRows(c.id, c.name); err != nil {
			return results, fmt.Errorf("delete repository rows for %s: %w", c.name, err)
		}
	}
	return results, nil
}

// staleRepoCandidates returns the workspace's selected repositories, plus
// repositories nothing references, whose path is inside the requested root and
// no longer exists.
func (s *Storage) staleRepoCandidates(ctx context.Context, workspaceID int64, inRoot func(string) bool, stale StalePathFunc) ([]staleRepoCandidate, error) {
	var candidates []staleRepoCandidate
	collect := func(query string, selected bool, args ...any) error {
		rows, err := s.pool.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		type row struct {
			id         int64
			name, path string
		}
		var found []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.name, &r.path); err != nil {
				rows.Close()
				return err
			}
			found = append(found, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range found {
			if !inRoot(r.path) {
				continue
			}
			gone, err := stale(r.path)
			if err != nil {
				return fmt.Errorf("inspect registered path %s of %s: %w", r.path, r.name, err)
			}
			if gone {
				candidates = append(candidates, staleRepoCandidate{id: r.id, name: r.name, path: r.path, selected: selected})
			}
		}
		return nil
	}
	if err := collect(`SELECT r.id,r.name,COALESCE(NULLIF(rs.source_path,''),NULLIF(wr.worktree_path,''),r.path)
 FROM workspace_repos wr JOIN repositories r ON r.name=wr.repo_name
 LEFT JOIN repo_snapshots rs ON rs.id=wr.active_snapshot_id
 WHERE wr.workspace_id=$1 ORDER BY r.name`, true, workspaceID); err != nil {
		return nil, err
	}
	if err := collect(`SELECT r.id,r.name,r.path FROM repositories r
 WHERE NOT EXISTS(SELECT 1 FROM workspace_repos wr WHERE wr.repo_name=r.name)
 AND NOT EXISTS(SELECT 1 FROM repo_snapshots s WHERE s.repo_id=r.id) ORDER BY r.name`, false); err != nil {
		return nil, err
	}
	return candidates, nil
}

func queryInt64s(ctx context.Context, conn *pgx.Conn, query string, args ...any) ([]int64, error) {
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
