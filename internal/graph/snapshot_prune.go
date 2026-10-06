package graph

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type PrunedSnapshot struct {
	ID        int64
	RepoID    int64
	Repo      string
	Workspace string
	CreatedAt time.Time
}

// PruneSnapshots removes old, unselected generations atomically. The default CLI
// is a dry run. Selected baselines and facts referenced by retained snapshots
// are protected, including transitive references between old generations.
func (s *Storage) PruneSnapshots(ctx context.Context, olderThan time.Duration, keep int, apply bool) ([]PrunedSnapshot, error) {
	if olderThan < 24*time.Hour || keep < 1 {
		return nil, fmt.Errorf("retention requires at least 24 hours and one successful generation per repository/workspace")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var workspaces []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		workspaces = append(workspaces, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Use the indexer's lock order, and fail immediately rather than waiting on an
	// active indexing run. Transaction locks disappear on errors and dry-run rollback.
	for _, id := range workspaces {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(741926,$1::int)`, id).Scan(&locked); err != nil {
			return nil, err
		}
		if !locked {
			return nil, fmt.Errorf("workspace %d is busy; retry pruning after indexing finishes", id)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(741927,0)`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE prune_snapshot_candidates ON COMMIT DROP AS
 WITH recent AS (SELECT id,row_number() OVER(PARTITION BY workspace_id,repo_id ORDER BY indexed_at DESC,id DESC) AS rank FROM repo_snapshots WHERE status='ok')
 SELECT s.id,s.repo_id FROM repo_snapshots s
 WHERE s.created_at < $1
 AND NOT EXISTS(SELECT 1 FROM recent r WHERE r.id=s.id AND r.rank<=$2)
 AND NOT EXISTS(SELECT 1 FROM workspace_repos w WHERE w.active_snapshot_id=s.id OR w.mainline_snapshot_id=s.id)`, time.Now().Add(-olderThan), keep); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `CREATE UNIQUE INDEX ON prune_snapshot_candidates(id)`); err != nil {
		return nil, err
	}
	// Source and target owners for references that can cross file boundaries.
	// Other file-scoped metadata cascades from its single owner.
	relations := [][5]string{
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
	var dependencies []string
	for _, r := range relations {
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
		dependencies = append(dependencies, "SELECT sf.snapshot_id AS source,tf.snapshot_id AS target FROM "+r[0]+" edge "+sourceJoin+" "+targetJoin+" JOIN prune_snapshot_candidates c ON c.id=tf.snapshot_id WHERE sf.snapshot_id IS DISTINCT FROM tf.snapshot_id"+sourcePresent)
	}

	for _, r := range [][3]string{{"trace_call_edges", "functions", "caller_function_id"}, {"trace_call_edges", "functions", "callee_function_id"}, {"trace_interface_impls", "classes", "class_id"}, {"ibmi_exports", "functions", "function_id"}} {
		dependencies = append(dependencies, "SELECT edge.snapshot_id AS source,tf.snapshot_id AS target FROM "+r[0]+" edge JOIN "+r[1]+" dst ON dst.id=edge."+r[2]+" JOIN files tf ON tf.id=dst.file_id JOIN prune_snapshot_candidates c ON c.id=tf.snapshot_id WHERE edge.snapshot_id IS DISTINCT FROM tf.snapshot_id")
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE prune_snapshot_dependencies ON COMMIT DROP AS `+strings.Join(dependencies, " UNION ")); err != nil {
		return nil, err
	}
	for {
		tag, err := tx.Exec(ctx, `DELETE FROM prune_snapshot_candidates c USING prune_snapshot_dependencies d WHERE d.target=c.id AND NOT EXISTS(SELECT 1 FROM prune_snapshot_candidates source WHERE source.id=d.source)`)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			break
		}
	}
	rows, err = tx.Query(ctx, `SELECT s.id,s.repo_id,s.repo_name,w.slug,s.created_at FROM repo_snapshots s JOIN workspaces w ON w.id=s.workspace_id JOIN prune_snapshot_candidates c ON c.id=s.id ORDER BY s.id FOR UPDATE OF s`)
	if err != nil {
		return nil, err
	}
	var result []PrunedSnapshot
	for rows.Next() {
		var item PrunedSnapshot
		if err := rows.Scan(&item.ID, &item.RepoID, &item.Repo, &item.Workspace, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !apply {
		return result, nil
	}
	for _, item := range result {
		if err := deleteFileMetadata(ctx, tx, item.RepoID, &item.ID, nil); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_snapshots s USING prune_snapshot_candidates c WHERE s.id=c.id`); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
