package graph

import (
	"context"
	"strings"
)

type RepoInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	FileCount int    `json:"file_count"`
}

// ListRepos returns repositories with file counts.
func (s *Storage) ListRepos(filters ...SnapshotFilter) ([]RepoInfo, error) {
	scope := ""
	args := []any{}
	if filter, ok := optionalSnapshotFilter(filters); ok {
		argNum := len(args) + 1
		appendSnapshotFilter(&scope, &args, &argNum, "f.snapshot_id", filter)
	}
	rows, err := s.pool.Query(context.Background(), strings.ReplaceAll(`
		SELECT r.id, r.name, r.path, COUNT(f.id) AS file_count
		FROM repositories r
		LEFT JOIN files f ON f.repo_id = r.id /* scope */
		GROUP BY r.id
		ORDER BY r.name
	`, "/* scope */", scope), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []RepoInfo
	for rows.Next() {
		var r RepoInfo
		if err := rows.Scan(&r.ID, &r.Name, &r.Path, &r.FileCount); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// GetRepoByID returns a single repository by ID.
func (s *Storage) GetRepoByID(id int64, filters ...SnapshotFilter) (*RepoInfo, error) {
	scope := ""
	args := []any{id}
	if filter, ok := optionalSnapshotFilter(filters); ok {
		argNum := len(args) + 1
		appendSnapshotFilter(&scope, &args, &argNum, "f.snapshot_id", filter)
	}
	var r RepoInfo
	err := s.pool.QueryRow(context.Background(), strings.ReplaceAll(`
		SELECT r.id, r.name, r.path, COUNT(f.id) AS file_count
		FROM repositories r
		LEFT JOIN files f ON f.repo_id = r.id /* scope */
		WHERE r.id = $1
		GROUP BY r.id
	`, "/* scope */", scope), args...).Scan(&r.ID, &r.Name, &r.Path, &r.FileCount)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRepoByName returns a single repository by name.
func (s *Storage) GetRepoByName(name string) (*RepoInfo, error) {
	var r RepoInfo
	err := s.pool.QueryRow(context.Background(), `
		SELECT r.id, r.name, r.path, COUNT(f.id) AS file_count
		FROM repositories r
		LEFT JOIN files f ON f.repo_id = r.id
		WHERE r.name = $1
		GROUP BY r.id
	`, name).Scan(&r.ID, &r.Name, &r.Path, &r.FileCount)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRepoByPath returns a single repository by absolute path.
func (s *Storage) GetRepoByPath(path string) (*RepoInfo, error) {
	var r RepoInfo
	err := s.pool.QueryRow(context.Background(), `
		SELECT r.id, r.name, r.path, COUNT(f.id) AS file_count
		FROM repositories r
		LEFT JOIN files f ON f.repo_id = r.id
		WHERE r.path = $1
		GROUP BY r.id
	`, path).Scan(&r.ID, &r.Name, &r.Path, &r.FileCount)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
