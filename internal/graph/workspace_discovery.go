package graph

import (
	"context"
	"time"
)

// DiscoverOtherWorkspaceHits finds a few functions, classes and endpoints whose
// names match pattern in snapshots that are active in some workspace but are not
// part of the captured selection. It exists only to feed "exists elsewhere"
// hints; ordinary searches never admit these rows, so they cannot consume
// result limits. limit applies to each kind.
func (s *Storage) DiscoverOtherWorkspaceHits(ctx context.Context, pattern string, selected SnapshotFilter, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = s.queryContext()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		WITH other AS (
			SELECT DISTINCT wr.active_snapshot_id AS id
			FROM workspace_repos wr
			WHERE wr.active_snapshot_id IS NOT NULL
			  AND wr.active_snapshot_id <> ALL(COALESCE($2::bigint[], '{}'::bigint[]))
		)
		(SELECT 'function' AS kind, f.id, f.name, f.start_line, f.end_line, fi.snapshot_id, fi.path, r.name
		   FROM functions f JOIN files fi ON fi.id=f.file_id JOIN repositories r ON r.id=fi.repo_id
		  WHERE f.name ILIKE $1 AND fi.snapshot_id IN (SELECT id FROM other)
		  ORDER BY f.name, f.id LIMIT $3)
		UNION ALL
		(SELECT 'class', c.id, c.name, c.start_line, c.end_line, fi.snapshot_id, fi.path, r.name
		   FROM classes c JOIN files fi ON fi.id=c.file_id JOIN repositories r ON r.id=fi.repo_id
		  WHERE c.name ILIKE $1 AND fi.snapshot_id IN (SELECT id FROM other)
		  ORDER BY c.name, c.id LIMIT $3)
		UNION ALL
		(SELECT 'endpoint', e.id, e.path, COALESCE(e.line_number,0), COALESCE(e.line_number,0), fi.snapshot_id, fi.path, r.name
		   FROM endpoints e JOIN files fi ON fi.id=e.file_id JOIN repositories r ON r.id=e.repo_id
		  WHERE e.path ILIKE $1 AND fi.snapshot_id IN (SELECT id FROM other)
		  ORDER BY e.path, e.id LIMIT $3)`,
		"%"+EscapeLike(pattern)+"%", selected.SnapshotIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Type, &r.ID, &r.Name, &r.StartLine, &r.EndLine, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
