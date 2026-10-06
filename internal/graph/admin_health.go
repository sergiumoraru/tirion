package graph

import (
	"context"
	"time"
)

type RepoHealthInfo struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	UpdatedAt       time.Time `json:"updatedAt"`
	FileCount       int       `json:"fileCount"`
	FunctionCount   int       `json:"functionCount"`
	ClassCount      int       `json:"classCount"`
	EndpointCount   int       `json:"endpointCount"`
	PrimaryLanguage string    `json:"primaryLanguage"`
}

func (s *Storage) ListRepoHealth() ([]RepoHealthInfo, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT
			r.id,
			r.name,
			r.path,
			r.updated_at,
			COALESCE(files.file_count, 0) AS file_count,
			COALESCE(funcs.function_count, 0) AS function_count,
			COALESCE(classes.class_count, 0) AS class_count,
			COALESCE(endpoints.endpoint_count, 0) AS endpoint_count,
			COALESCE(lang.primary_language, 'mixed') AS primary_language
		FROM repositories r
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS file_count
			FROM files f
			WHERE f.repo_id = r.id
		) files ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS function_count
			FROM functions fn
			JOIN files f ON f.id = fn.file_id
			WHERE f.repo_id = r.id
		) funcs ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS class_count
			FROM classes c
			JOIN files f ON f.id = c.file_id
			WHERE f.repo_id = r.id
		) classes ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS endpoint_count
			FROM endpoints e
			WHERE e.repo_id = r.id
		) endpoints ON TRUE
		LEFT JOIN LATERAL (
			SELECT COALESCE(f.language, 'unknown') AS primary_language
			FROM files f
			WHERE f.repo_id = r.id
			GROUP BY f.language
			ORDER BY COUNT(*) DESC, COALESCE(f.language, 'unknown')
			LIMIT 1
		) lang ON TRUE
		ORDER BY r.updated_at DESC NULLS LAST, r.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []RepoHealthInfo
	for rows.Next() {
		var repo RepoHealthInfo
		if err := rows.Scan(
			&repo.ID,
			&repo.Name,
			&repo.Path,
			&repo.UpdatedAt,
			&repo.FileCount,
			&repo.FunctionCount,
			&repo.ClassCount,
			&repo.EndpointCount,
			&repo.PrimaryLanguage,
		); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (s *Storage) ListRepoHealthForWorkspace(workspaceSlug string) ([]RepoHealthInfo, error) {
	includeLegacy := workspaceSlug == DefaultWorkspaceSlug
	rows, err := s.pool.Query(context.Background(), `
		WITH ws AS (
			SELECT id, slug
			FROM workspaces
			WHERE slug = $1
			LIMIT 1
		),
		active AS (
			SELECT
				wr.repo_name,
				wr.active_snapshot_id,
				CASE
					WHEN wr.active_snapshot_id IS NOT NULL THEN wr.active_snapshot_id
					WHEN NOT $2
					 AND wr.mainline_snapshot_id IS NOT NULL
					 AND (
						TRIM(wr.target_ref) IN ('master', 'main')
						OR (TRIM(wr.target_ref) = '' AND TRIM(wr.resolved_branch) IN ('master', 'main'))
					 )
					THEN wr.mainline_snapshot_id
					WHEN NOT $2
					 AND dwr.active_snapshot_id IS NOT NULL
					 AND (
						TRIM(wr.target_ref) IN ('master', 'main')
						OR (TRIM(wr.target_ref) = '' AND TRIM(wr.resolved_branch) IN ('master', 'main'))
					 )
					THEN dwr.active_snapshot_id
					ELSE NULL
				END AS effective_snapshot_id
			FROM workspace_repos wr
			JOIN ws ON ws.id = wr.workspace_id
			LEFT JOIN workspaces dws ON dws.slug = 'default-main'
			LEFT JOIN workspace_repos dwr ON dwr.workspace_id = dws.id AND dwr.repo_name = wr.repo_name
		)
		SELECT
			r.id,
			r.name,
			r.path,
			COALESCE(rs.indexed_at, r.updated_at),
			COALESCE(files.file_count, 0) AS file_count,
			COALESCE(funcs.function_count, 0) AS function_count,
			COALESCE(classes.class_count, 0) AS class_count,
			COALESCE(endpoints.endpoint_count, 0) AS endpoint_count,
			COALESCE(lang.primary_language, 'mixed') AS primary_language
		FROM repositories r
		CROSS JOIN ws
		LEFT JOIN active a ON a.repo_name = r.name
		LEFT JOIN repo_snapshots rs ON rs.id = a.effective_snapshot_id
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS file_count
			FROM files f
			WHERE f.repo_id = r.id
			  AND (
			      (a.effective_snapshot_id IS NOT NULL AND f.snapshot_id = a.effective_snapshot_id)
			      OR ($2 AND a.active_snapshot_id IS NULL AND f.snapshot_id IS NULL)
			  )
		) files ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS function_count
			FROM functions fn
			JOIN files f ON f.id = fn.file_id
			WHERE f.repo_id = r.id
			  AND (
			      (a.effective_snapshot_id IS NOT NULL AND f.snapshot_id = a.effective_snapshot_id)
			      OR ($2 AND a.active_snapshot_id IS NULL AND f.snapshot_id IS NULL)
			  )
		) funcs ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS class_count
			FROM classes c
			JOIN files f ON f.id = c.file_id
			WHERE f.repo_id = r.id
			  AND (
			      (a.effective_snapshot_id IS NOT NULL AND f.snapshot_id = a.effective_snapshot_id)
			      OR ($2 AND a.active_snapshot_id IS NULL AND f.snapshot_id IS NULL)
			  )
		) classes ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS endpoint_count
			FROM endpoints e
			JOIN files f ON f.id = e.file_id
			WHERE e.repo_id = r.id
			  AND (
			      (a.effective_snapshot_id IS NOT NULL AND f.snapshot_id = a.effective_snapshot_id)
			      OR ($2 AND a.active_snapshot_id IS NULL AND f.snapshot_id IS NULL)
			  )
		) endpoints ON TRUE
		LEFT JOIN LATERAL (
			SELECT COALESCE(f.language, 'unknown') AS primary_language
			FROM files f
			WHERE f.repo_id = r.id
			  AND (
			      (a.effective_snapshot_id IS NOT NULL AND f.snapshot_id = a.effective_snapshot_id)
			      OR ($2 AND a.active_snapshot_id IS NULL AND f.snapshot_id IS NULL)
			  )
			GROUP BY f.language
			ORDER BY COUNT(*) DESC, COALESCE(f.language, 'unknown')
			LIMIT 1
		) lang ON TRUE
		ORDER BY COALESCE(rs.indexed_at, r.updated_at) DESC NULLS LAST, r.name
	`, workspaceSlug, includeLegacy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []RepoHealthInfo
	for rows.Next() {
		var repo RepoHealthInfo
		if err := rows.Scan(
			&repo.ID,
			&repo.Name,
			&repo.Path,
			&repo.UpdatedAt,
			&repo.FileCount,
			&repo.FunctionCount,
			&repo.ClassCount,
			&repo.EndpointCount,
			&repo.PrimaryLanguage,
		); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}
