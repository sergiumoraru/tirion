package main

import (
	"context"
	stdpath "path"
	"strings"

	"github.com/jackc/pgx/v5"
)

type graphQLControllerResolutionModule struct{}

type graphQLBackendEntrypointRow struct {
	entrypointID     int64
	filePath         string
	controllersPath  string
	registrationKind string
}

type graphQLControllerFileRow struct {
	fileID   int64
	filePath string
}

func (m *graphQLControllerResolutionModule) ID() string {
	return "graphql_controller_resolution"
}

func (m *graphQLControllerResolutionModule) FinalizeRepo(ctx *repoFinalizeContext) error {
	if ctx == nil || ctx.storage == nil {
		return nil
	}

	entrypoints, err := loadGraphQLBackendEntrypoints(ctx)
	if err != nil {
		return err
	}
	if len(entrypoints) == 0 {
		return nil
	}

	files, err := loadRepoFiles(ctx)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	tx, err := ctx.storage.Pool().Begin(context.Background())
	if err != nil {
		return err
	}

	batch := &pgx.Batch{}
	for _, entrypoint := range entrypoints {
		dirs := candidateGraphQLControllerDirs(entrypoint.filePath, entrypoint.controllersPath)
		if len(dirs) == 0 {
			continue
		}
		for _, file := range files {
			confidence := graphqlControllerLinkConfidence(file.filePath, dirs)
			if confidence == "" {
				continue
			}
			batch.Queue(`
				INSERT INTO graphql_backend_controller_links (repo_id, entrypoint_id, file_id, resolution_confidence)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (entrypoint_id, file_id) DO UPDATE SET
					resolution_confidence = EXCLUDED.resolution_confidence
			`, ctx.repoID, entrypoint.entrypointID, file.fileID, confidence)
		}
	}

	if batch.Len() == 0 {
		_ = tx.Rollback(context.Background())
		return nil
	}

	results := tx.SendBatch(context.Background(), batch)
	if err := results.Close(); err != nil {
		_ = tx.Rollback(context.Background())
		return err
	}
	return tx.Commit(context.Background())
}

func loadGraphQLBackendEntrypoints(ctx *repoFinalizeContext) ([]graphQLBackendEntrypointRow, error) {
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT e.id, f.path, e.controllers_path, e.registration_kind
		FROM graphql_backend_entrypoints e
		JOIN files f ON f.id = e.file_id
		WHERE e.repo_id = $1
		  AND f.snapshot_id IS NOT DISTINCT FROM $2::bigint
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graphQLBackendEntrypointRow
	for rows.Next() {
		var row graphQLBackendEntrypointRow
		if err := rows.Scan(&row.entrypointID, &row.filePath, &row.controllersPath, &row.registrationKind); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadRepoFiles(ctx *repoFinalizeContext) ([]graphQLControllerFileRow, error) {
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT id, path
		FROM files
		WHERE repo_id = $1
		  AND snapshot_id IS NOT DISTINCT FROM $2::bigint
		  AND path NOT LIKE '.codebase-snapshots/%'
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graphQLControllerFileRow
	for rows.Next() {
		var row graphQLControllerFileRow
		if err := rows.Scan(&row.fileID, &row.filePath); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func candidateGraphQLControllerDirs(entrypointPath, controllersPath string) []string {
	entrypointPath = normalizeFilePathIdentity(entrypointPath)
	controllersPath = strings.TrimSpace(controllersPath)
	if entrypointPath == "" || controllersPath == "" {
		return nil
	}

	baseDir := stdpath.Dir(entrypointPath)
	seen := make(map[string]bool)
	var out []string
	add := func(value string) {
		value = normalizeFilePathIdentity(strings.TrimSpace(value))
		value = strings.Trim(value, "/")
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)
	}

	if extracted := extractControllerRelativePath(controllersPath); extracted != "" {
		add(stdpath.Join(baseDir, extracted))
		add(extracted)
	}

	if strings.HasPrefix(controllersPath, "./") || strings.HasPrefix(controllersPath, "../") {
		add(stdpath.Join(baseDir, controllersPath))
	}

	return out
}

func extractControllerRelativePath(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	if strings.Contains(expr, "path.join") {
		var parts []string
		for _, token := range strings.Split(expr, ",") {
			token = strings.TrimSpace(token)
			token = strings.TrimSuffix(token, ")")
			token = strings.Trim(token, "\"'` ")
			if token == "" || token == "__dirname" || strings.Contains(token, "path.join(") {
				continue
			}
			parts = append(parts, token)
		}
		if len(parts) > 0 {
			return stdpath.Join(parts...)
		}
	}
	return strings.Trim(expr, "\"'` ")
}

func graphqlControllerLinkConfidence(filePath string, dirs []string) string {
	filePath = normalizeFilePathIdentity(filePath)
	for _, dir := range dirs {
		dir = strings.Trim(normalizeFilePathIdentity(dir), "/")
		if dir == "" {
			continue
		}
		if filePath == dir || strings.HasPrefix(filePath, dir+"/") {
			return "high"
		}
	}
	return ""
}
