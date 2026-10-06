package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const DefaultWorkspaceSlug = "default-main"

var ErrInvalidWorkspace = errors.New("invalid workspace")

type Workspace struct {
	ID             int64     `json:"id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	CreatedByLabel string    `json:"createdByLabel"`
	IsDefault      bool      `json:"isDefault"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type WorkspaceRepo struct {
	WorkspaceID           int64      `json:"workspaceId"`
	RepoName              string     `json:"repoName"`
	TargetRef             string     `json:"targetRef"`
	ResolvedBranch        string     `json:"resolvedBranch"`
	ResolvedSHA           string     `json:"resolvedSha"`
	WorktreePath          string     `json:"worktreePath"`
	ActiveSnapshotID      *int64     `json:"activeSnapshotId,omitempty"`
	MainlineSnapshotID    *int64     `json:"mainlineSnapshotId,omitempty"`
	MainlineResolvedSHA   string     `json:"mainlineResolvedSha,omitempty"`
	MainlineLastIndexedAt *time.Time `json:"mainlineLastIndexedAt,omitempty"`
	LastCheckoutAt        *time.Time `json:"lastCheckoutAt,omitempty"`
	LastIndexedAt         *time.Time `json:"lastIndexedAt,omitempty"`
	IndexStatus           string     `json:"indexStatus"`
	LastError             string     `json:"lastError"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

type RepoSnapshot struct {
	ID            int64     `json:"id"`
	WorkspaceID   int64     `json:"workspaceId"`
	RepoID        int64     `json:"repoId"`
	RepoName      string    `json:"repoName"`
	Branch        string    `json:"branch"`
	SHA           string    `json:"sha"`
	IndexedAt     time.Time `json:"indexedAt"`
	ParserVersion string    `json:"parserVersion"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
}

type UpsertWorkspaceRepoInput struct {
	WorkspaceID           int64
	RepoName              string
	TargetRef             string
	ResolvedBranch        string
	ResolvedSHA           string
	WorktreePath          string
	ActiveSnapshotID      *int64
	MainlineSnapshotID    *int64
	MainlineResolvedSHA   string
	MainlineLastIndexedAt *time.Time
	LastCheckoutAt        *time.Time
	LastIndexedAt         *time.Time
	IndexStatus           string
	LastError             string
}

type RepoSnapshotInput struct {
	WorkspaceID   int64
	RepoID        int64
	RepoName      string
	Branch        string
	SHA           string
	IndexedAt     time.Time
	ParserVersion string
	Status        string
}

type WorkspaceSnapshotRef struct {
	WorkspaceID int64     `json:"workspaceId"`
	Workspace   string    `json:"workspace"`
	RepoID      int64     `json:"repoId"`
	RepoName    string    `json:"repoName"`
	SnapshotID  int64     `json:"snapshotId"`
	Branch      string    `json:"branch"`
	SHA         string    `json:"sha"`
	IndexedAt   time.Time `json:"indexedAt"`
}

func normalizeWorkspaceSlug(slug string) string {
	return strings.ToLower(strings.TrimSpace(slug))
}

func validateWorkspaceSlug(slug string) error {
	if slug == "" {
		return errors.New("workspace slug is required")
	}
	if slug == "." || slug == ".." {
		return errors.New("workspace slug cannot be a directory navigation segment")
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("workspace slug %q contains unsupported character %q", slug, r)
	}
	return nil
}

func (s *Storage) EnsureWorkspace(slug, name, description, createdByLabel string, isDefault bool) (*Workspace, error) {
	slug = normalizeWorkspaceSlug(slug)
	if err := validateWorkspaceSlug(slug); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWorkspace, err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if isDefault {
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET is_default = FALSE WHERE slug <> $1 AND is_default`, slug); err != nil {
			return nil, err
		}
	}

	var ws Workspace
	err = tx.QueryRow(ctx, `
		INSERT INTO workspaces (slug, name, description, created_by_label, is_default)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			created_by_label = EXCLUDED.created_by_label,
			is_default = CASE WHEN EXCLUDED.is_default THEN TRUE ELSE workspaces.is_default END,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id, slug, name, description, created_by_label, is_default, created_at, updated_at
	`, slug, name, description, createdByLabel, isDefault).Scan(
		&ws.ID,
		&ws.Slug,
		&ws.Name,
		&ws.Description,
		&ws.CreatedByLabel,
		&ws.IsDefault,
		&ws.CreatedAt,
		&ws.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ws, nil
}

func (s *Storage) GetWorkspaceBySlug(slug string) (*Workspace, error) {
	slug = normalizeWorkspaceSlug(slug)
	if err := validateWorkspaceSlug(slug); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWorkspace, err)
	}
	var ws Workspace
	err := s.pool.QueryRow(s.queryContext(), `
		SELECT id, slug, name, description, created_by_label, is_default, created_at, updated_at
		FROM workspaces
		WHERE slug = $1
	`, slug).Scan(
		&ws.ID,
		&ws.Slug,
		&ws.Name,
		&ws.Description,
		&ws.CreatedByLabel,
		&ws.IsDefault,
		&ws.CreatedAt,
		&ws.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("workspace %q not found: %w", slug, err)
	}
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

func (s *Storage) GetDefaultWorkspace() (*Workspace, error) {
	var ws Workspace
	err := s.pool.QueryRow(s.queryContext(), `
		SELECT id, slug, name, description, created_by_label, is_default, created_at, updated_at
		FROM workspaces
		WHERE is_default
		ORDER BY id
		LIMIT 1
	`).Scan(
		&ws.ID,
		&ws.Slug,
		&ws.Name,
		&ws.Description,
		&ws.CreatedByLabel,
		&ws.IsDefault,
		&ws.CreatedAt,
		&ws.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetWorkspaceBySlug(DefaultWorkspaceSlug)
	}
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

func (s *Storage) SetDefaultWorkspace(slug string) (*Workspace, error) {
	ws, err := s.GetWorkspaceBySlug(slug)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE workspaces SET is_default = FALSE WHERE is_default AND id <> $1`, ws.ID); err != nil {
		return nil, err
	}
	var updated Workspace
	err = tx.QueryRow(ctx, `
		UPDATE workspaces
		SET is_default = TRUE,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING id, slug, name, description, created_by_label, is_default, created_at, updated_at
	`, ws.ID).Scan(
		&updated.ID,
		&updated.Slug,
		&updated.Name,
		&updated.Description,
		&updated.CreatedByLabel,
		&updated.IsDefault,
		&updated.CreatedAt,
		&updated.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Storage) ResolveWorkspace(slug string) (*Workspace, error) {
	slug = normalizeWorkspaceSlug(slug)
	if slug == "" {
		return s.GetDefaultWorkspace()
	}
	return s.GetWorkspaceBySlug(slug)
}

func (s *Storage) ListWorkspaces() ([]Workspace, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, slug, name, description, created_by_label, is_default, created_at, updated_at
		FROM workspaces
		ORDER BY is_default DESC, slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Workspace
	for rows.Next() {
		var ws Workspace
		if err := rows.Scan(
			&ws.ID,
			&ws.Slug,
			&ws.Name,
			&ws.Description,
			&ws.CreatedByLabel,
			&ws.IsDefault,
			&ws.CreatedAt,
			&ws.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *Storage) UpsertWorkspaceRepo(input UpsertWorkspaceRepoInput) (*WorkspaceRepo, error) {
	if input.WorkspaceID <= 0 {
		return nil, errors.New("workspace id is required")
	}
	input.RepoName = strings.TrimSpace(input.RepoName)
	if input.RepoName == "" {
		return nil, errors.New("repo name is required")
	}
	if input.IndexStatus == "" {
		input.IndexStatus = "unknown"
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(741927,0)`); err != nil {
		return nil, err
	}
	var wr WorkspaceRepo
	err = tx.QueryRow(ctx, `
		INSERT INTO workspace_repos (
			workspace_id,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			mainline_snapshot_id,
			mainline_resolved_sha,
			mainline_last_indexed_at,
			last_checkout_at,
			last_indexed_at,
			index_status,
			last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (workspace_id, repo_name) DO UPDATE SET
			target_ref = EXCLUDED.target_ref,
			resolved_branch = EXCLUDED.resolved_branch,
			resolved_sha = EXCLUDED.resolved_sha,
			worktree_path = EXCLUDED.worktree_path,
			active_snapshot_id = COALESCE(EXCLUDED.active_snapshot_id, workspace_repos.active_snapshot_id),
			mainline_snapshot_id = COALESCE(EXCLUDED.mainline_snapshot_id, workspace_repos.mainline_snapshot_id),
			mainline_resolved_sha = COALESCE(NULLIF(EXCLUDED.mainline_resolved_sha, ''), workspace_repos.mainline_resolved_sha),
			mainline_last_indexed_at = COALESCE(EXCLUDED.mainline_last_indexed_at, workspace_repos.mainline_last_indexed_at),
			last_checkout_at = EXCLUDED.last_checkout_at,
			last_indexed_at = COALESCE(EXCLUDED.last_indexed_at, workspace_repos.last_indexed_at),
			index_status = EXCLUDED.index_status,
			last_error = EXCLUDED.last_error,
			updated_at = CURRENT_TIMESTAMP
		RETURNING
			workspace_id,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			mainline_snapshot_id,
			mainline_resolved_sha,
			mainline_last_indexed_at,
			last_checkout_at,
			last_indexed_at,
			index_status,
			last_error,
			created_at,
			updated_at
	`, input.WorkspaceID,
		input.RepoName,
		input.TargetRef,
		input.ResolvedBranch,
		input.ResolvedSHA,
		input.WorktreePath,
		input.ActiveSnapshotID,
		input.MainlineSnapshotID,
		input.MainlineResolvedSHA,
		input.MainlineLastIndexedAt,
		input.LastCheckoutAt,
		input.LastIndexedAt,
		input.IndexStatus,
		input.LastError,
	).Scan(
		&wr.WorkspaceID,
		&wr.RepoName,
		&wr.TargetRef,
		&wr.ResolvedBranch,
		&wr.ResolvedSHA,
		&wr.WorktreePath,
		&wr.ActiveSnapshotID,
		&wr.MainlineSnapshotID,
		&wr.MainlineResolvedSHA,
		&wr.MainlineLastIndexedAt,
		&wr.LastCheckoutAt,
		&wr.LastIndexedAt,
		&wr.IndexStatus,
		&wr.LastError,
		&wr.CreatedAt,
		&wr.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &wr, nil
}

func (s *Storage) GetWorkspaceRepo(workspaceSlug, repoName string) (*WorkspaceRepo, error) {
	ws, err := s.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return nil, err
	}
	var wr WorkspaceRepo
	err = s.pool.QueryRow(context.Background(), `
		SELECT
			workspace_id,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			mainline_snapshot_id,
			mainline_resolved_sha,
			mainline_last_indexed_at,
			last_checkout_at,
			last_indexed_at,
			index_status,
			last_error,
			created_at,
			updated_at
		FROM workspace_repos
		WHERE workspace_id = $1
		  AND repo_name = $2
	`, ws.ID, strings.TrimSpace(repoName)).Scan(
		&wr.WorkspaceID,
		&wr.RepoName,
		&wr.TargetRef,
		&wr.ResolvedBranch,
		&wr.ResolvedSHA,
		&wr.WorktreePath,
		&wr.ActiveSnapshotID,
		&wr.MainlineSnapshotID,
		&wr.MainlineResolvedSHA,
		&wr.MainlineLastIndexedAt,
		&wr.LastCheckoutAt,
		&wr.LastIndexedAt,
		&wr.IndexStatus,
		&wr.LastError,
		&wr.CreatedAt,
		&wr.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &wr, nil
}

func (s *Storage) ListWorkspaceRepos(workspaceSlug string) ([]WorkspaceRepo, error) {
	ws, err := s.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT
			workspace_id,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			mainline_snapshot_id,
			mainline_resolved_sha,
			mainline_last_indexed_at,
			last_checkout_at,
			last_indexed_at,
			index_status,
			last_error,
			created_at,
			updated_at
		FROM workspace_repos
		WHERE workspace_id = $1
		ORDER BY repo_name
	`, ws.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkspaceRepo
	for rows.Next() {
		var wr WorkspaceRepo
		if err := rows.Scan(
			&wr.WorkspaceID,
			&wr.RepoName,
			&wr.TargetRef,
			&wr.ResolvedBranch,
			&wr.ResolvedSHA,
			&wr.WorktreePath,
			&wr.ActiveSnapshotID,
			&wr.MainlineSnapshotID,
			&wr.MainlineResolvedSHA,
			&wr.MainlineLastIndexedAt,
			&wr.LastCheckoutAt,
			&wr.LastIndexedAt,
			&wr.IndexStatus,
			&wr.LastError,
			&wr.CreatedAt,
			&wr.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, wr)
	}
	return out, rows.Err()
}

func (s *Storage) EnsureRepoSnapshot(input RepoSnapshotInput) (*RepoSnapshot, error) {
	if input.WorkspaceID <= 0 {
		return nil, errors.New("workspace id is required")
	}
	if input.RepoID <= 0 {
		return nil, errors.New("repo id is required")
	}
	input.RepoName = strings.TrimSpace(input.RepoName)
	if input.RepoName == "" {
		return nil, errors.New("repo name is required")
	}
	input.SHA = strings.TrimSpace(input.SHA)
	if input.SHA == "" {
		return nil, errors.New("repo snapshot sha is required")
	}
	if input.IndexedAt.IsZero() {
		input.IndexedAt = time.Now()
	}
	if input.Status == "" {
		input.Status = "ok"
	}

	var snap RepoSnapshot
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO repo_snapshots (
			workspace_id,
			repo_id,
			repo_name,
			branch,
			sha,
			indexed_at,
			parser_version,
			status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (workspace_id, repo_id, sha, generation) DO UPDATE SET
			repo_name = EXCLUDED.repo_name,
			branch = EXCLUDED.branch,
			indexed_at = EXCLUDED.indexed_at,
			parser_version = COALESCE(NULLIF(EXCLUDED.parser_version, ''), repo_snapshots.parser_version),
			status = EXCLUDED.status
		RETURNING id, workspace_id, repo_id, repo_name, branch, sha, indexed_at, parser_version, status, created_at
	`, input.WorkspaceID,
		input.RepoID,
		input.RepoName,
		input.Branch,
		input.SHA,
		input.IndexedAt.UTC(),
		input.ParserVersion,
		input.Status,
	).Scan(
		&snap.ID,
		&snap.WorkspaceID,
		&snap.RepoID,
		&snap.RepoName,
		&snap.Branch,
		&snap.SHA,
		&snap.IndexedAt,
		&snap.ParserVersion,
		&snap.Status,
		&snap.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (s *Storage) ActiveSnapshotsForWorkspace(slug string) ([]WorkspaceSnapshotRef, error) {
	ws, err := s.ResolveWorkspace(slug)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(s.queryContext(), `
		SELECT
			w.id,
			w.slug,
			r.id,
			r.name,
			rs.id,
			rs.branch,
			rs.sha,
			rs.indexed_at
		FROM workspace_repos wr
		JOIN workspaces w ON w.id = wr.workspace_id
		JOIN repo_snapshots rs ON rs.id = wr.active_snapshot_id
		JOIN repositories r ON r.id = rs.repo_id
		WHERE wr.workspace_id = $1
		ORDER BY r.name
	`, ws.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkspaceSnapshotRef
	for rows.Next() {
		var ref WorkspaceSnapshotRef
		if err := rows.Scan(
			&ref.WorkspaceID,
			&ref.Workspace,
			&ref.RepoID,
			&ref.RepoName,
			&ref.SnapshotID,
			&ref.Branch,
			&ref.SHA,
			&ref.IndexedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *Storage) WorkspaceRefsForSnapshots(snapshotIDs []int64) (map[int64][]WorkspaceSnapshotRef, error) {
	if len(snapshotIDs) == 0 {
		return map[int64][]WorkspaceSnapshotRef{}, nil
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT
			w.id,
			w.slug,
			r.id,
			r.name,
			rs.id,
			rs.branch,
			rs.sha,
			rs.indexed_at
		FROM repo_snapshots rs
		JOIN repositories r ON r.id = rs.repo_id
		JOIN workspace_repos wr ON wr.active_snapshot_id = rs.id
		JOIN workspaces w ON w.id = wr.workspace_id
		WHERE rs.id = ANY($1)
		ORDER BY w.slug, r.name
	`, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]WorkspaceSnapshotRef)
	for rows.Next() {
		var ref WorkspaceSnapshotRef
		if err := rows.Scan(
			&ref.WorkspaceID,
			&ref.Workspace,
			&ref.RepoID,
			&ref.RepoName,
			&ref.SnapshotID,
			&ref.Branch,
			&ref.SHA,
			&ref.IndexedAt,
		); err != nil {
			return nil, err
		}
		out[ref.SnapshotID] = append(out[ref.SnapshotID], ref)
	}
	return out, rows.Err()
}

func (s *Storage) UpdateWorkspaceRepoIndexedState(workspaceSlug string, repoID int64, branch, sha string, indexedAt time.Time) error {
	repo, err := s.GetRepoByID(repoID)
	if err != nil {
		return err
	}
	return s.UpdateWorkspaceRepoIndexedStateForPath(workspaceSlug, repoID, branch, sha, indexedAt, repo.Path)
}

func (s *Storage) UpdateWorkspaceRepoIndexedStateForPath(workspaceSlug string, repoID int64, branch, sha string, indexedAt time.Time, worktreePath string) error {
	ws, err := s.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return err
	}
	repo, err := s.GetRepoByID(repoID)
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
	if strings.TrimSpace(worktreePath) == "" {
		worktreePath = repo.Path
	}
	input := UpsertWorkspaceRepoInput{
		WorkspaceID:      ws.ID,
		RepoName:         repo.Name,
		TargetRef:        branch,
		ResolvedBranch:   branch,
		ResolvedSHA:      sha,
		WorktreePath:     worktreePath,
		ActiveSnapshotID: &snapshot.ID,
		LastIndexedAt:    &indexedAt,
		IndexStatus:      "ok",
	}
	if isMainlineBranch(branch) {
		input.MainlineSnapshotID = &snapshot.ID
		input.MainlineResolvedSHA = sha
		input.MainlineLastIndexedAt = &indexedAt
	}
	_, err = s.UpsertWorkspaceRepo(input)
	return err
}

func isMainlineBranch(branch string) bool {
	switch strings.TrimSpace(branch) {
	case "master", "main":
		return true
	default:
		return false
	}
}

func (s *Storage) WorkspaceIndexedSHAs(workspaceSlug string) (map[string]string, error) {
	ws, err := s.ResolveWorkspace(workspaceSlug)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT wr.repo_name, COALESCE(rs.sha, '')
		FROM workspace_repos wr
		LEFT JOIN repo_snapshots rs ON rs.id = wr.active_snapshot_id
		WHERE wr.workspace_id = $1
	`, ws.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var repo, sha string
		if err := rows.Scan(&repo, &sha); err != nil {
			return nil, err
		}
		out[repo] = sha
	}
	return out, rows.Err()
}

func (s *Storage) CloneWorkspaceRepoSelections(sourceSlug string, targetWorkspaceID int64) error {
	if targetWorkspaceID <= 0 {
		return errors.New("target workspace id is required")
	}
	source, err := s.ResolveWorkspace(sourceSlug)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Publication checks sharing under the same lock before mutating caller edges.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(741927,0)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, cloneWorkspaceRepoSelectionsSQL, targetWorkspaceID, source.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const cloneWorkspaceRepoSelectionsSQL = `
		INSERT INTO workspace_repos (
			workspace_id,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			mainline_snapshot_id,
			mainline_resolved_sha,
			mainline_last_indexed_at,
			last_indexed_at,
			index_status,
			last_error
		)
		SELECT
			$1,
			repo_name,
			target_ref,
			resolved_branch,
			resolved_sha,
			worktree_path,
			active_snapshot_id,
			COALESCE(mainline_snapshot_id, active_snapshot_id),
			COALESCE(NULLIF(mainline_resolved_sha, ''), resolved_sha),
			COALESCE(mainline_last_indexed_at, last_indexed_at),
			last_indexed_at,
			CASE
				WHEN active_snapshot_id IS NOT NULL THEN 'inherited'
				ELSE 'unindexed'
			END,
			last_error
		FROM workspace_repos
		WHERE workspace_id = $2
		ON CONFLICT (workspace_id, repo_name) DO UPDATE SET
			target_ref = EXCLUDED.target_ref,
			resolved_branch = EXCLUDED.resolved_branch,
			resolved_sha = EXCLUDED.resolved_sha,
			worktree_path = EXCLUDED.worktree_path,
			active_snapshot_id = EXCLUDED.active_snapshot_id,
			mainline_snapshot_id = EXCLUDED.mainline_snapshot_id,
			mainline_resolved_sha = EXCLUDED.mainline_resolved_sha,
			mainline_last_indexed_at = EXCLUDED.mainline_last_indexed_at,
			last_indexed_at = EXCLUDED.last_indexed_at,
			index_status = EXCLUDED.index_status,
			last_error = EXCLUDED.last_error,
			updated_at = CURRENT_TIMESTAMP
`
