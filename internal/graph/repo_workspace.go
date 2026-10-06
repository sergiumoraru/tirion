package graph

import (
	"context"
	"time"
)

type RepoWorkspaceState struct {
	RepoID              int64
	SelectedBranch      string
	IndexedBranch       string
	IndexedSHA          string
	IndexedAt           *time.Time
	LastOperation       string
	LastOperationStatus string
	LastOperationAt     *time.Time
	LastError           string
}

func (s *Storage) ListRepoWorkspaceState() (map[int64]RepoWorkspaceState, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT
			repo_id,
			COALESCE(selected_branch, ''),
			COALESCE(indexed_branch, ''),
			COALESCE(indexed_sha, ''),
			indexed_at,
			COALESCE(last_operation, ''),
			COALESCE(last_operation_status, ''),
			last_operation_at,
			COALESCE(last_error, '')
		FROM repo_workspace_state
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]RepoWorkspaceState)
	for rows.Next() {
		var state RepoWorkspaceState
		if err := rows.Scan(
			&state.RepoID,
			&state.SelectedBranch,
			&state.IndexedBranch,
			&state.IndexedSHA,
			&state.IndexedAt,
			&state.LastOperation,
			&state.LastOperationStatus,
			&state.LastOperationAt,
			&state.LastError,
		); err != nil {
			return nil, err
		}
		out[state.RepoID] = state
	}
	return out, rows.Err()
}

func (s *Storage) UpdateRepoSelectedBranch(repoID int64, branch string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO repo_workspace_state (
			repo_id,
			selected_branch
		)
		VALUES ($1, $2)
		ON CONFLICT (repo_id) DO UPDATE SET
			selected_branch = EXCLUDED.selected_branch
	`, repoID, branch)
	if err != nil {
		return err
	}
	return s.syncDefaultWorkspaceRepoSelection(repoID, branch)
}

func (s *Storage) UpdateRepoIndexedState(repoID int64, branch, sha string, indexedAt time.Time) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO repo_workspace_state (
			repo_id,
			selected_branch,
			indexed_branch,
			indexed_sha,
			indexed_at
		)
		VALUES ($1, (SELECT selected_branch FROM repo_workspace_state WHERE repo_id = $1), $2, $3, $4)
		ON CONFLICT (repo_id) DO UPDATE SET
			selected_branch = COALESCE(repo_workspace_state.selected_branch, EXCLUDED.selected_branch),
			indexed_branch = EXCLUDED.indexed_branch,
			indexed_sha = EXCLUDED.indexed_sha,
			indexed_at = EXCLUDED.indexed_at
	`, repoID, branch, sha, indexedAt.UTC())
	if err != nil {
		return err
	}
	return s.syncDefaultWorkspaceRepoIndexedState(repoID, branch, sha, indexedAt)
}

func (s *Storage) UpdateRepoWorkspaceOperation(repoID int64, operation, status string, at time.Time, lastError string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO repo_workspace_state (
			repo_id,
			selected_branch,
			last_operation,
			last_operation_status,
			last_operation_at,
			last_error
		)
		VALUES ($1, (SELECT selected_branch FROM repo_workspace_state WHERE repo_id = $1), $2, $3, $4, $5)
		ON CONFLICT (repo_id) DO UPDATE SET
			selected_branch = COALESCE(repo_workspace_state.selected_branch, EXCLUDED.selected_branch),
			last_operation = EXCLUDED.last_operation,
			last_operation_status = EXCLUDED.last_operation_status,
			last_operation_at = EXCLUDED.last_operation_at,
			last_error = EXCLUDED.last_error
	`, repoID, operation, status, at.UTC(), lastError)
	if err != nil {
		return err
	}
	return s.syncDefaultWorkspaceRepoOperation(repoID, status, lastError)
}

type SelectedRepoBranch struct {
	RepoID         int64
	Name           string
	Path           string
	SelectedBranch string
}

func (s *Storage) ListSelectedRepoBranches() ([]SelectedRepoBranch, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT r.id, r.name, r.path, s.selected_branch
		FROM repo_workspace_state s
		JOIN repositories r ON r.id = s.repo_id
		WHERE COALESCE(s.selected_branch, '') <> ''
		ORDER BY r.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SelectedRepoBranch
	for rows.Next() {
		var item SelectedRepoBranch
		if err := rows.Scan(&item.RepoID, &item.Name, &item.Path, &item.SelectedBranch); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Storage) syncDefaultWorkspaceRepoSelection(repoID int64, branch string) error {
	repo, err := s.GetRepoByID(repoID)
	if err != nil {
		return err
	}
	ws, err := s.GetWorkspaceBySlug(DefaultWorkspaceSlug)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO workspace_repos (
			workspace_id,
			repo_name,
			target_ref,
			worktree_path,
			index_status
		)
		VALUES ($1, $2, $3, $4, 'unknown')
		ON CONFLICT (workspace_id, repo_name) DO UPDATE SET
			target_ref = EXCLUDED.target_ref,
			worktree_path = EXCLUDED.worktree_path,
			updated_at = CURRENT_TIMESTAMP
	`, ws.ID, repo.Name, branch, repo.Path)
	return err
}

func (s *Storage) syncDefaultWorkspaceRepoIndexedState(repoID int64, branch, sha string, indexedAt time.Time) error {
	repo, err := s.GetRepoByID(repoID)
	if err != nil {
		return err
	}
	ws, err := s.GetWorkspaceBySlug(DefaultWorkspaceSlug)
	if err != nil {
		return err
	}
	snapshot, err := s.EnsureRepoSnapshot(RepoSnapshotInput{
		WorkspaceID: ws.ID,
		RepoID:      repoID,
		RepoName:    repo.Name,
		Branch:      branch,
		SHA:         sha,
		IndexedAt:   indexedAt,
		Status:      "ok",
	})
	if err != nil {
		return err
	}
	_, err = s.UpsertWorkspaceRepo(UpsertWorkspaceRepoInput{
		WorkspaceID:      ws.ID,
		RepoName:         repo.Name,
		TargetRef:        branch,
		ResolvedBranch:   branch,
		ResolvedSHA:      sha,
		WorktreePath:     repo.Path,
		ActiveSnapshotID: &snapshot.ID,
		LastIndexedAt:    &indexedAt,
		IndexStatus:      "ok",
	})
	return err
}

func (s *Storage) syncDefaultWorkspaceRepoOperation(repoID int64, status, lastError string) error {
	repo, err := s.GetRepoByID(repoID)
	if err != nil {
		return err
	}
	ws, err := s.GetWorkspaceBySlug(DefaultWorkspaceSlug)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO workspace_repos (
			workspace_id,
			repo_name,
			worktree_path,
			index_status,
			last_error
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, repo_name) DO UPDATE SET
			worktree_path = EXCLUDED.worktree_path,
			index_status = EXCLUDED.index_status,
			last_error = EXCLUDED.last_error,
			updated_at = CURRENT_TIMESTAMP
	`, ws.ID, repo.Name, repo.Path, status, lastError)
	return err
}
