package main

import (
	"context"
	stdpath "path"
	"strings"

	"github.com/jackc/pgx/v5"
)

type graphQLResolutionModule struct{}

type graphQLOperationUsageRow struct {
	usageID    int64
	callerPath string
	importPath string
	importedAs string
}

type graphQLOperationRow struct {
	operationID   int64
	filePath      string
	operationName string
}

func (m *graphQLResolutionModule) ID() string {
	return "graphql_resolution"
}

func (m *graphQLResolutionModule) FinalizeRepo(ctx *repoFinalizeContext) error {
	if ctx == nil || ctx.storage == nil {
		return nil
	}

	usages, err := loadGraphQLOperationUsages(ctx)
	if err != nil {
		return err
	}
	if len(usages) == 0 {
		return nil
	}

	operations, err := loadGraphQLOperations(ctx)
	if err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}

	opsByPath := make(map[string][]int64)
	opsByName := make(map[string][]int64)
	for _, operation := range operations {
		pathKey := normalizeFilePathCanonical(operation.filePath)
		opsByPath[pathKey] = append(opsByPath[pathKey], operation.operationID)
		nameKey := strings.ToLower(strings.TrimSpace(operation.operationName))
		if nameKey != "" {
			opsByName[nameKey] = append(opsByName[nameKey], operation.operationID)
		}
	}

	tx, err := ctx.storage.Pool().Begin(context.Background())
	if err != nil {
		return err
	}

	batch := &pgx.Batch{}
	for _, usage := range usages {
		seenOperationIDs := make(map[int64]bool)
		if operationName, ok := directGraphQLOperationName(usage.importPath, usage.importedAs); ok {
			operationIDs := opsByName[strings.ToLower(operationName)]
			confidence := "high"
			if len(operationIDs) > 1 {
				confidence = "medium"
			}
			for _, operationID := range operationIDs {
				if seenOperationIDs[operationID] {
					continue
				}
				seenOperationIDs[operationID] = true
				batch.Queue(`
					INSERT INTO graphql_usage_operation_links (repo_id, usage_id, operation_id, resolution_confidence)
					VALUES ($1, $2, $3, $4)
					ON CONFLICT (usage_id, operation_id) DO UPDATE SET
						resolution_confidence = EXCLUDED.resolution_confidence
				`, ctx.repoID, usage.usageID, operationID, confidence)
			}
			continue
		}

		candidates := candidateGraphQLImportPaths(usage.callerPath, usage.importPath)
		for _, candidate := range candidates {
			operationIDs := opsByPath[normalizeFilePathCanonical(candidate)]
			if len(operationIDs) == 0 {
				continue
			}
			confidence := "high"
			if len(operationIDs) > 1 {
				confidence = "medium"
			}
			for _, operationID := range operationIDs {
				if seenOperationIDs[operationID] {
					continue
				}
				seenOperationIDs[operationID] = true
				batch.Queue(`
					INSERT INTO graphql_usage_operation_links (repo_id, usage_id, operation_id, resolution_confidence)
					VALUES ($1, $2, $3, $4)
					ON CONFLICT (usage_id, operation_id) DO UPDATE SET
						resolution_confidence = EXCLUDED.resolution_confidence
				`, ctx.repoID, usage.usageID, operationID, confidence)
			}
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

func loadGraphQLOperationUsages(ctx *repoFinalizeContext) ([]graphQLOperationUsageRow, error) {
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT u.id, f.path, u.import_path, u.imported_as
		FROM graphql_operation_usages u
		JOIN files f ON f.id = u.file_id
		WHERE u.repo_id = $1
		  AND f.snapshot_id IS NOT DISTINCT FROM $2::bigint
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graphQLOperationUsageRow
	for rows.Next() {
		var row graphQLOperationUsageRow
		if err := rows.Scan(&row.usageID, &row.callerPath, &row.importPath, &row.importedAs); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadGraphQLOperations(ctx *repoFinalizeContext) ([]graphQLOperationRow, error) {
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT o.id, f.path, o.operation_name
		FROM graphql_operations o
		JOIN files f ON f.id = o.file_id
		WHERE o.repo_id = $1
		  AND f.snapshot_id IS NOT DISTINCT FROM $2::bigint
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graphQLOperationRow
	for rows.Next() {
		var row graphQLOperationRow
		if err := rows.Scan(&row.operationID, &row.filePath, &row.operationName); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func candidateGraphQLImportPaths(callerPath, importPath string) []string {
	callerPath = normalizeFilePathIdentity(callerPath)
	importPath = strings.TrimSpace(importPath)
	if callerPath == "" || importPath == "" {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	add := func(candidate string) {
		candidate = normalizeFilePathIdentity(candidate)
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		out = append(out, candidate)
	}

	baseDir := stdpath.Dir(callerPath)

	switch {
	case strings.HasPrefix(importPath, "./") || strings.HasPrefix(importPath, "../"):
		add(stdpath.Join(baseDir, importPath))
	case strings.HasPrefix(importPath, "@/"):
		trimmed := strings.TrimPrefix(importPath, "@/")
		add(trimmed)
		add(stdpath.Join("src", trimmed))
	case strings.HasPrefix(importPath, "@") && strings.Contains(importPath, "/"):
		trimmed := strings.TrimPrefix(importPath, "@")
		trimmed = strings.TrimPrefix(trimmed, "/")
		slashIdx := strings.Index(trimmed, "/")
		if slashIdx >= 0 && slashIdx+1 < len(trimmed) {
			withoutAlias := trimmed[slashIdx+1:]
			add(withoutAlias)
			add(stdpath.Join("src", withoutAlias))
			add(trimmed)
			add(stdpath.Join("src", trimmed))
		} else {
			add(trimmed)
			add(stdpath.Join("src", trimmed))
		}
	default:
		add(importPath)
	}

	for _, existing := range append([]string(nil), out...) {
		if ext := stdpath.Ext(existing); ext == "" {
			add(existing + ".gql")
			add(existing + ".graphql")
		}
	}

	return out
}

func directGraphQLOperationName(importPath, importedAs string) (string, bool) {
	const prefix = "__graphql_operation__:"
	if strings.HasPrefix(importPath, prefix) {
		name := strings.TrimSpace(strings.TrimPrefix(importPath, prefix))
		if name != "" {
			return name, true
		}
	}
	return "", false
}
