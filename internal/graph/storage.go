package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Storage struct {
	queryCtx    context.Context
	pool        *pgxpool.Pool
	transaction pgx.Tx
}

// Executor lets publication reuse the resolver inside its database transaction.
type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

func (s *Storage) Executor() Executor {
	if s.transaction != nil {
		return s.transaction
	}
	return s.pool
}
func (s *Storage) WithTransaction(tx pgx.Tx) *Storage { return &Storage{pool: s.pool, transaction: tx} }

func cleanupTraceEnabled() bool {
	return os.Getenv("CODEBASE_TRACE_CLEANUP") == "1"
}

func logCleanupStep(enabled bool, label string, start time.Time) {
	if !enabled {
		return
	}
	fmt.Fprintf(os.Stderr, "cleanup: %s took %s\n", label, time.Since(start))
}

func nullableSnapshotID(snapshotID *int64) any {
	if snapshotID == nil {
		return nil
	}
	return *snapshotID
}

// LiteralPrefixPattern retains literal path characters while allowing PostgreSQL
// to use text_pattern_ops indexes. Pair this with LIKE ... ESCAPE '\'.
func LiteralPrefixPattern(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
}

// LockIndexedFile verifies the exact source version before enrichment. Separate
// legacy NULL snapshots from ordinary equality so the snapshot/path unique index
// can locate one file instead of scanning every retained file in the repository.
func LockIndexedFile(ctx context.Context, tx pgx.Tx, repoID int64, snapshotID *int64, path, hash string) error {
	predicate := "snapshot_id=$2"
	if snapshotID == nil {
		predicate = "snapshot_id IS NULL AND $2::bigint IS NULL"
	}
	var id int64
	return tx.QueryRow(ctx, `SELECT id FROM files WHERE repo_id=$1 AND `+predicate+` AND path=$3 AND hash=$4 FOR UPDATE`, repoID, snapshotID, path, hash).Scan(&id)
}

func NewStorage(connString string) (*Storage, error) {
	return newStorage(connString, true)
}

func NewStorageNoSchemaInit(connString string) (*Storage, error) {
	return newStorage(connString, false)
}

func newStorage(connString string, initializeSchema bool) (*Storage, error) {
	if strings.TrimSpace(connString) == "" {
		return nil, fmt.Errorf("database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)")
	}
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, err
	}

	s := &Storage{pool: pool}
	if initializeSchema && os.Getenv("CODEBASE_SKIP_SCHEMA_INIT") != "1" {
		if err := s.initialize(); err != nil {
			pool.Close()
			return nil, err
		}
	}

	return s, nil
}

func (s *Storage) initialize() error {
	return s.migrate(context.Background())
}

// ResetAll truncates all known tables and resets identities (destructive).
func (s *Storage) ResetAll() error {
	_, err := s.pool.Exec(context.Background(), `
		TRUNCATE TABLE
			repositories,
			repo_dependency_edges,
			files,
			functions,
			classes,
			endpoints,
			file_imports,
			function_calls,
			pending_contains_edges,
			pending_calls_edges,
			pending_imports_edges,
			http_client_calls,
			graphql_operations,
			graphql_operation_usages,
			graphql_backend_entrypoints,
			graphql_backend_controller_links,
			graphql_operation_resolvers,
			graphql_operation_permissions,
			graphql_usage_operation_links,
			azure_host_configs,
			azure_function_triggers,
			gateway_routes,
			sqs_producers,
			sqs_consumers,
			resource_aliases,
			interfaces,
			implementations,
			fields,
			annotations,
			method_signatures,
			constructor_params,
			type_parameters,
			http_interface_methods,
			vue_component_contracts,
			pinia_stores,
			bean_definitions,
			event_listeners,
			scheduled_methods,
			jpa_entities,
			jpa_relationships,
			repository_entities,
			data_accesses,
			synthetic_methods,
			enum_constants,
			lambda_expressions
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		return err
	}
	return nil
}

func (s *Storage) Close() {
	s.pool.Close()
}

// Pool returns the underlying connection pool
func (s *Storage) Pool() *pgxpool.Pool {
	return s.pool
}

// InsertRepository inserts or updates a repository
func (s *Storage) InsertRepository(name, path string, language, framework *string) (int64, error) {
	return s.InsertRepositoryWithPathPolicy(name, path, language, framework, true)
}

func (s *Storage) InsertRepositoryWithPathPolicy(name, path string, language, framework *string, updatePath bool) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO repositories (name, path, language, framework)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(name) DO UPDATE SET
			path = CASE WHEN $5 THEN EXCLUDED.path ELSE repositories.path END,
			language = EXCLUDED.language,
			framework = EXCLUDED.framework,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id
	`, name, path, language, framework, updatePath).Scan(&id)
	return id, err
}

// DeleteFilesByRepo deletes all files and related data for a repo.
func (s *Storage) DeleteFilesByRepo(repoID int64) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM files WHERE repo_id=$1 ORDER BY id FOR UPDATE`, repoID); err != nil {
		return err
	}
	if err := deleteFileMetadata(ctx, tx, repoID, nil, nil); err != nil {
		return err
	}
	trace := cleanupTraceEnabled()
	type deleteQuery struct {
		label string
		query string
	}
	repoIDQueries := []deleteQuery{
		// Phase 1: Delete new tables first (they reference existing tables)
		{label: "http_interface_methods", query: `DELETE FROM http_interface_methods WHERE interface_id IN (SELECT id FROM interfaces WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "constructor_params", query: `DELETE FROM constructor_params WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "method_signatures", query: `DELETE FROM method_signatures WHERE function_id IN (SELECT id FROM functions WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "fields_interface", query: `DELETE FROM fields WHERE interface_id IN (SELECT id FROM interfaces WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "fields_class", query: `DELETE FROM fields WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "implementations", query: `DELETE FROM implementations WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "vue_component_contracts", query: `DELETE FROM vue_component_contracts v USING files f WHERE v.file_id = f.id AND f.repo_id = $1`},
		{label: "pinia_stores", query: `DELETE FROM pinia_stores p USING files f WHERE p.file_id = f.id AND f.repo_id = $1`},
		{label: "interfaces", query: `DELETE FROM interfaces i USING files f WHERE i.file_id = f.id AND f.repo_id = $1`},
		// Feature-complete Java parser tables
		{label: "jpa_relationships", query: `DELETE FROM jpa_relationships WHERE source_class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "jpa_entities", query: `DELETE FROM jpa_entities WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "repository_entities", query: `DELETE FROM repository_entities WHERE repo_id = $1`},
		{label: "data_accesses", query: `DELETE FROM data_accesses WHERE repo_id = $1`},
		{label: "synthetic_methods", query: `DELETE FROM synthetic_methods WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "enum_constants", query: `DELETE FROM enum_constants WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "lambda_expressions", query: `DELETE FROM lambda_expressions le USING files f WHERE le.file_id = f.id AND f.repo_id = $1`},
		// Spring extraction tables
		{label: "bean_definitions", query: `DELETE FROM bean_definitions WHERE config_class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "event_listeners", query: `DELETE FROM event_listeners WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "scheduled_methods", query: `DELETE FROM scheduled_methods WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "trace_call_edges", query: `DELETE FROM trace_call_edges WHERE repo_id = $1`},
		{label: "trace_interface_impls", query: `DELETE FROM trace_interface_impls WHERE repo_id = $1`},
		// Original queries
		{label: "function_calls", query: `DELETE FROM function_calls WHERE caller_function_id IN (SELECT id FROM functions WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "function_calls_callee", query: `UPDATE function_calls
			SET callee_function_id = NULL, callee_resolution_source = NULL,
			    callee_resolution_confidence = NULL, unresolved_reason = 'callee_removed'
			WHERE callee_function_id IN (SELECT id FROM functions WHERE file_id IN (SELECT id FROM files WHERE repo_id = $1))`},
		{label: "file_imports", query: `DELETE FROM file_imports fi USING files f WHERE fi.file_id = f.id AND f.repo_id = $1`},
		{label: "endpoints", query: `DELETE FROM endpoints WHERE repo_id = $1`},
		{label: "functions", query: `DELETE FROM functions fn USING files f WHERE fn.file_id = f.id AND f.repo_id = $1`},
		{label: "classes", query: `DELETE FROM classes c USING files f WHERE c.file_id = f.id AND f.repo_id = $1`},
		{label: "files", query: `DELETE FROM files WHERE repo_id = $1`},
		{label: "pending_calls_edges", query: `DELETE FROM pending_calls_edges WHERE repo_id = $1`},
		{label: "pending_contains_edges", query: `DELETE FROM pending_contains_edges WHERE repo_id = $1`},
		{label: "pending_imports_edges", query: `DELETE FROM pending_imports_edges WHERE repo_id = $1`},
		{label: "http_client_calls", query: `DELETE FROM http_client_calls WHERE repo_id = $1`},
		{label: "graphql_operations", query: `DELETE FROM graphql_operations WHERE repo_id = $1`},
		{label: "graphql_operation_usages", query: `DELETE FROM graphql_operation_usages WHERE repo_id = $1`},
		{label: "graphql_backend_entrypoints", query: `DELETE FROM graphql_backend_entrypoints WHERE repo_id = $1`},
		{label: "graphql_backend_controller_links", query: `DELETE FROM graphql_backend_controller_links WHERE repo_id = $1`},
		{label: "graphql_operation_resolvers", query: `DELETE FROM graphql_operation_resolvers WHERE repo_id = $1`},
		{label: "graphql_operation_permissions", query: `DELETE FROM graphql_operation_permissions WHERE repo_id = $1`},
		{label: "graphql_usage_operation_links", query: `DELETE FROM graphql_usage_operation_links WHERE repo_id = $1`},
		{label: "azure_host_configs", query: `DELETE FROM azure_host_configs WHERE repo_id = $1`},
		{label: "azure_function_triggers", query: `DELETE FROM azure_function_triggers WHERE repo_id = $1`},
		{label: "gateway_routes", query: `DELETE FROM gateway_routes WHERE repo_id = $1`},
		{label: "sqs_producers", query: `DELETE FROM sqs_producers WHERE repo_id = $1`},
		{label: "sqs_consumers", query: `DELETE FROM sqs_consumers WHERE repo_id = $1`},
		{label: "resource_aliases", query: `DELETE FROM resource_aliases WHERE repo_id = $1`},
	}

	for i, q := range repoIDQueries {
		start := time.Now()
		if _, err := tx.Exec(ctx, q.query, repoID); err != nil {
			return err
		}
		logCleanupStep(trace, fmt.Sprintf("delete_%02d_%s", i+1, q.label), start)
	}
	return tx.Commit(ctx)
}

// DeleteRepositoryRows removes repository-level rows that are not file-scoped.
// DeleteFilesByRepo should run first for indexed facts owned by repository.id.
func (s *Storage) DeleteRepositoryRows(repoID int64, repoName string) error {
	repoName = strings.TrimSpace(repoName)
	ctx := context.Background()
	if repoName != "" {
		queries := []string{
			`DELETE FROM workspace_repos WHERE repo_name = $1`,
			`DELETE FROM trace_call_edges WHERE callee_repo = $1`,
		}
		for _, q := range queries {
			if _, err := s.pool.Exec(ctx, q, repoName); err != nil {
				return err
			}
		}
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, repoID)
	return err
}

// DeleteFilesByRepoSnapshot deletes file-scoped facts for one indexed repo snapshot.
func (s *Storage) DeleteFilesByRepoSnapshot(repoID, snapshotID int64) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM files WHERE repo_id=$1 AND snapshot_id=$2 ORDER BY id FOR UPDATE`, repoID, snapshotID); err != nil {
		return err
	}

	if err := deleteFileMetadata(ctx, tx, repoID, &snapshotID, nil); err != nil {
		return err
	}
	queries := []string{
		`DELETE FROM trace_call_edges WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM trace_interface_impls WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM pending_calls_edges WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM pending_contains_edges WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM pending_imports_edges WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM http_client_calls WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM sqs_producers WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM sqs_consumers WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM resource_aliases WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM repository_entities WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM data_accesses WHERE repo_id = $1 AND snapshot_id = $2`,
		`DELETE FROM files WHERE repo_id = $1 AND snapshot_id = $2`,
	}
	for _, q := range queries {
		if _, err := tx.Exec(ctx, q, repoID, snapshotID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Storage) DeleteFileByRepoSnapshotPath(repoID, snapshotID int64, filePath string) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM files WHERE repo_id=$1 AND snapshot_id=$2 AND path=$3 FOR UPDATE`, repoID, snapshotID, filePath); err != nil {
		return err
	}

	var repoName string
	if err := tx.QueryRow(ctx, `SELECT name FROM repositories WHERE id = $1`, repoID).Scan(&repoName); err != nil {
		return err
	}
	prefix := repoName + ":" + filePath
	// Keep exact identities separate from escaped caller-prefix patterns.
	queries := []string{
		`DELETE FROM trace_call_edges WHERE repo_id = $1 AND snapshot_id = $2 AND caller_id LIKE $3 ESCAPE '\'`,
		`DELETE FROM pending_calls_edges WHERE repo_id = $1 AND snapshot_id = $2 AND caller_id LIKE $3 ESCAPE '\'`,
		`DELETE FROM http_client_calls WHERE repo_id = $1 AND snapshot_id = $2 AND caller_id LIKE $3 ESCAPE '\'`,
		`DELETE FROM data_accesses WHERE repo_id = $1 AND snapshot_id = $2 AND caller_id LIKE $3 ESCAPE '\'`,
		`DELETE FROM sqs_producers WHERE repo_id = $1 AND snapshot_id = $2 AND caller_id LIKE $3 ESCAPE '\'`,
		`DELETE FROM sqs_consumers WHERE repo_id = $1 AND snapshot_id = $2 AND consumer_id LIKE $3 ESCAPE '\'`,
	}
	pattern := LiteralPrefixPattern(prefix + ":")
	for _, q := range queries {
		// An unnamed statement keeps the concrete prefix visible to the planner;
		// a cached generic plan cannot derive its text_pattern_ops range.
		if _, err := tx.Exec(ctx, q, pgx.QueryExecModeExec, repoID, snapshotID, pattern); err != nil {
			return err
		}
	}
	for _, table := range []string{"pending_contains_edges", "pending_imports_edges"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE repo_id=$1 AND snapshot_id=$2 AND file_id=$3`, repoID, snapshotID, prefix); err != nil {
			return err
		}
	}
	if err := deleteFileMetadata(ctx, tx, repoID, &snapshotID, &filePath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE repo_id = $1 AND snapshot_id = $2 AND path = $3`, repoID, snapshotID, filePath); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetFileHash returns the stored hash for a file path in a repo.
func (s *Storage) GetFileHash(repoID int64, filePath string) (string, bool, error) {
	var hash *string
	err := s.pool.QueryRow(context.Background(), `
		SELECT hash FROM files WHERE repo_id = $1 AND path = $2
	`, repoID, filePath).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if hash == nil {
		return "", true, nil
	}
	return *hash, true, nil
}

func (s *Storage) GetFileHashForSnapshot(repoID, snapshotID int64, filePath string) (string, bool, error) {
	var hash *string
	err := s.pool.QueryRow(context.Background(), `
		SELECT hash FROM files
		WHERE repo_id = $1
		  AND snapshot_id = $2
		  AND path = $3
	`, repoID, snapshotID, filePath).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if hash == nil {
		return "", true, nil
	}
	return *hash, true, nil
}

// GetFilesByRepo returns file paths for a repo.
func (s *Storage) GetFilesByRepo(repoID int64) ([]string, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT path FROM files WHERE repo_id = $1
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}

func (s *Storage) GetFilesByRepoSnapshot(repoID, snapshotID int64) ([]string, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT path FROM files
		WHERE repo_id = $1
		  AND snapshot_id = $2
	`, repoID, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}

// InsertFile inserts or updates a file
func (s *Storage) InsertFile(repoID int64, path string, language, hash *string) (int64, error) {
	return s.InsertFileForSnapshot(repoID, nil, path, language, hash)
}

func (s *Storage) InsertFileForSnapshot(repoID int64, snapshotID *int64, path string, language, hash *string) (int64, error) {
	var id int64
	if snapshotID != nil {
		err := s.pool.QueryRow(context.Background(), `
			INSERT INTO files (repo_id, snapshot_id, path, language, hash)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT(snapshot_id, path) WHERE snapshot_id IS NOT NULL DO UPDATE SET
				language = EXCLUDED.language,
				hash = EXCLUDED.hash,
				updated_at = CURRENT_TIMESTAMP
			RETURNING id
		`, repoID, *snapshotID, path, language, hash).Scan(&id)
		return id, err
	}
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO files (repo_id, snapshot_id, path, language, hash)
		VALUES ($1, NULL, $2, $3, $4)
		ON CONFLICT(repo_id, path) WHERE snapshot_id IS NULL DO UPDATE SET
			language = EXCLUDED.language,
			hash = EXCLUDED.hash,
			updated_at = CURRENT_TIMESTAMP
		RETURNING id
	`, repoID, path, language, hash).Scan(&id)
	return id, err
}

// InsertFunction inserts a function
func (s *Storage) InsertFunction(fileID int64, name string, startLine, endLine int, params []string, returnType *string, isExported, isAsync bool, sourceCode string) (int64, error) {
	var paramsJSON []byte
	if len(params) > 0 {
		paramsJSON, _ = json.Marshal(params)
	}
	simpleName := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		simpleName = name[idx+1:]
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO functions (file_id, name, simple_name, start_line, end_line, params, return_type, is_exported, is_async, source_code)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, fileID, name, simpleName, startLine, endLine, paramsJSON, returnType, isExported, isAsync, sourceCode).Scan(&id)
	return id, err
}

// InsertClass inserts a class
func (s *Storage) InsertClass(fileID int64, name string, startLine, endLine int, extendsClass *string, implements []string, isExported bool) (int64, error) {
	var implementsJSON []byte
	if len(implements) > 0 {
		implementsJSON, _ = json.Marshal(implements)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO classes (file_id, name, start_line, end_line, extends_class, implements, is_exported)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, fileID, name, startLine, endLine, extendsClass, implementsJSON, isExported).Scan(&id)
	return id, err
}

// InsertImport inserts a file import
func (s *Storage) InsertImport(fileID int64, importPath string, importsFileID *int64, importNames []string, isDefault bool) (int64, error) {
	var namesJSON []byte
	if len(importNames) > 0 {
		namesJSON, _ = json.Marshal(importNames)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO file_imports (file_id, imports_file_id, import_path, import_names, is_default_import)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, fileID, importsFileID, importPath, namesJSON, isDefault).Scan(&id)
	return id, err
}

// InsertFunctionCall inserts a function call
func (s *Storage) InsertFunctionCall(callerID int64, calleeName string, calleeID *int64, lineNumber *int, isAsync bool) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO function_calls (caller_function_id, callee_function_id, callee_name, line_number, is_async, callee_resolution_source, callee_resolution_confidence, unresolved_reason)
		VALUES ($1, $2, $3, $4, $5,
			CASE WHEN $2 IS NOT NULL THEN 'parser' ELSE NULL END,
			CASE WHEN $2 IS NOT NULL THEN 'high' ELSE NULL END,
			NULL
		)
		RETURNING id
	`, callerID, calleeID, calleeName, lineNumber, isAsync).Scan(&id)
	return id, err
}

// ResolveFunctionCallCallees fills callee_function_id when a safe, deterministic match is available.
func (s *Storage) ResolveFunctionCallCallees(repoID int64) error {
	return s.ResolveFunctionCallCalleesForSnapshot(repoID, nil)
}

func (s *Storage) ResolveFunctionCallCalleesForSnapshot(repoID int64, snapshotID *int64) error {
	ctx := context.Background()
	tx, err := s.Executor().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	s = s.WithTransaction(tx)
	snapshotArg := nullableSnapshotID(snapshotID)

	// Ensure simple_name is populated for this repo before resolving unqualified calls.
	_, err = s.Executor().Exec(ctx, `
		UPDATE functions fn
		SET simple_name = CASE
			WHEN fn.simple_name IS NOT NULL AND fn.simple_name <> '' THEN fn.simple_name
			WHEN strpos(fn.name, '.') > 0 THEN reverse(split_part(reverse(fn.name), '.', 1))
			ELSE fn.name
		END
		FROM files f
		WHERE f.id = fn.file_id
		  AND f.repo_id = $1
		  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
		  AND (fn.simple_name IS NULL OR fn.simple_name = '')
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	// Fresh snapshot IDs have poor estimates in the historical tables. Materialize
	// the exact unique-name sets once and analyze their real cardinalities, rather
	// than letting an estimated single name repeatedly scan every caller.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS tirion_repo_function_names
		(kind text, file_id bigint, name text, callee_id bigint) ON COMMIT DROP;
		TRUNCATE tirion_repo_function_names`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `WITH scoped AS MATERIALIZED (
		SELECT fn.id,fn.file_id,fn.name,fn.simple_name,f.language
		FROM functions fn JOIN files f ON f.id=fn.file_id
		WHERE f.repo_id=$1 AND ($2::bigint IS NULL OR f.snapshot_id=$2)
	), qualified AS (
		SELECT id,name FROM scoped
		UNION
		SELECT fn.id,array_to_string(parts.name[n.start:],'.') FROM scoped fn
		CROSS JOIN LATERAL (SELECT string_to_array(fn.name,'.') AS name) parts
		CROSS JOIN LATERAL generate_series(2,cardinality(parts.name)-1) n(start)
		WHERE fn.language='java'
	)
	INSERT INTO tirion_repo_function_names(kind,file_id,name,callee_id)
	SELECT 'file',file_id,name,MIN(id) FROM scoped GROUP BY file_id,name HAVING COUNT(*)=1
	UNION ALL SELECT 'exact',NULL,name,MIN(id) FROM scoped GROUP BY name HAVING COUNT(*)=1
	UNION ALL SELECT 'qualified',NULL,name,MIN(id) FROM qualified GROUP BY name HAVING COUNT(*)=1
	UNION ALL SELECT 'simple',NULL,simple_name,MIN(id) FROM scoped GROUP BY simple_name HAVING COUNT(*)=1`, repoID, snapshotArg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE INDEX IF NOT EXISTS tirion_repo_function_names_lookup
		ON tirion_repo_function_names(kind,name,file_id);
		ANALYZE tirion_repo_function_names`); err != nil {
		return err
	}

	// 1) Same-file unique: callee_name matches a single function in the caller's file.
	_, err = s.Executor().Exec(ctx, `
		WITH caller AS (
			SELECT fc.id AS fc_id, fn.file_id, fc.callee_name
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.repo_id = $1
			  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
		),
		unique_file AS (
			SELECT file_id,name,callee_id FROM tirion_repo_function_names WHERE kind='file'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'same_file',
		    callee_resolution_confidence = 'high'
		FROM caller c
		JOIN unique_file u ON u.file_id = c.file_id AND u.name = c.callee_name
		WHERE fc.id = c.fc_id
		  AND fc.callee_function_id IS NULL
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	// 2) Qualified name unique in repo: "Class.method" occurs exactly once in repo.
	_, err = s.Executor().Exec(ctx, `
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE fc.callee_function_id IS NULL
				  AND f.repo_id = $1
				  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
				  AND fc.callee_name LIKE '%.%'
		),
		unique_repo AS (
			SELECT name,callee_id FROM tirion_repo_function_names WHERE kind='qualified'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'qualified_repo',
		    callee_resolution_confidence = 'high'
		FROM caller c
		JOIN unique_repo u ON u.name = c.callee_name
		WHERE fc.id = c.fc_id
		  AND fc.callee_function_id IS NULL
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	// Aggregate declarations before joining calls so repeated/ambiguous names do
	// not multiply each call by every matching declaration.
	// 2b) Same-class receiver ("this.method"): resolve against caller class when unique in repo.
	_, err = s.Executor().Exec(ctx, `
		WITH caller AS (
			SELECT fc.id AS fc_id,
			       f.repo_id,
			       regexp_replace(cfn.name, '\.[^.]+$', '') AS caller_class,
			       regexp_replace(fc.callee_name, '^this\.', '') AS method_name
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files f ON f.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.repo_id = $1
			  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
			  AND fc.callee_name LIKE 'this.%'
			  AND fc.callee_name NOT LIKE 'this.%.%'
		),
		unique_names AS (
			SELECT name,callee_id FROM tirion_repo_function_names WHERE kind='exact'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'same_class_this',
		    callee_resolution_confidence = 'high'
		FROM caller c JOIN unique_names u ON u.name=c.caller_class || '.' || c.method_name
		WHERE fc.id = c.fc_id AND c.caller_class IS NOT NULL AND c.caller_class<>''
		  AND fc.callee_function_id IS NULL
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	// 2c) Unqualified same-class call ("method"): resolve against caller class when unique in repo.
	_, err = s.Executor().Exec(ctx, `
		WITH caller AS (
			SELECT fc.id AS fc_id,
			       f.repo_id,
			       regexp_replace(cfn.name, '\.[^.]+$', '') AS caller_class,
			       fc.callee_name
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files f ON f.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
				  AND f.repo_id = $1
				  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
				  AND fc.callee_name NOT LIKE '%.%'
		),
		unique_names AS (
			SELECT name,callee_id FROM tirion_repo_function_names WHERE kind='exact'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'same_class_simple',
		    callee_resolution_confidence = 'high'
		FROM caller c JOIN unique_names u ON u.name=c.caller_class || '.' || c.callee_name
		WHERE fc.id = c.fc_id AND c.caller_class IS NOT NULL AND c.caller_class<>''
		  AND fc.callee_function_id IS NULL
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	// Do not plan/execute the recursive Java resolver for repositories that have
	// no Java callers. The resolver itself applies this same language restriction.
	var hasJava bool
	if err := s.Executor().QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM files WHERE repo_id=$1
		AND ($2::bigint IS NULL OR snapshot_id=$2) AND language='java'
	)`, repoID, snapshotArg).Scan(&hasJava); err != nil {
		return err
	}
	if hasJava {
		// Java implicit receivers can reach inherited methods through enclosing types.
		// Bind bases in Java scope order. Repository-wide simple-name uniqueness is
		// not evidence of inheritance. Unknown packages/imports remain unresolved.
		_, err = s.Executor().Exec(ctx, `
			WITH RECURSIVE indexed_types AS (
				SELECT cl.*, f.java_package,
				       CASE WHEN f.java_package='' THEN cl.name ELSE f.java_package||'.'||cl.name END AS qualified_name
				FROM classes cl JOIN files f ON f.id=cl.file_id
				WHERE f.repo_id=$1 AND ($2::bigint IS NULL OR f.snapshot_id=$2) AND f.language='java'
			), erased_bases AS (
				SELECT id,extends_class AS base_name FROM indexed_types
				UNION ALL
				-- Remove innermost arguments repeatedly, retaining nested type owners
				-- and supporting arbitrary generic depth without a greedy expression.
				SELECT id,regexp_replace(base_name,'<[^<>]*>','','g') FROM erased_bases
				WHERE base_name ~ '<[^<>]*>'
			), types AS (
				SELECT t.*,regexp_replace(e.base_name,'\s','','g') AS base_name
				FROM indexed_types t JOIN erased_bases e ON e.id=t.id
				WHERE e.base_name IS NULL OR e.base_name !~ '<[^<>]*>'
			), type_scopes AS (
				SELECT id,file_id,base_name,
				       CASE WHEN name LIKE '%.%' THEN regexp_replace(name, '\.[^.]+$', '') ELSE '' END AS scope,
				       0 AS depth FROM types WHERE base_name IS NOT NULL AND base_name<>''
				UNION ALL
				SELECT id,file_id,base_name,
				       CASE WHEN scope LIKE '%.%' THEN regexp_replace(scope, '\.[^.]+$', '') ELSE '' END,depth+1
				FROM type_scopes WHERE scope<>''
			), base_candidates AS (
				SELECT s.id,b.id AS base_id,0 AS precedence,s.depth
				FROM type_scopes s JOIN types b ON b.file_id=s.file_id
				 AND b.name=CASE WHEN s.scope='' THEN s.base_name ELSE s.scope||'.'||s.base_name END
				UNION ALL
				-- A missing explicit import must block a same-named fallback.
				SELECT d.id,b.id,1,0 FROM types d JOIN file_imports i ON i.file_id=d.file_id
				 AND reverse(split_part(reverse(i.import_path),'.',1))=split_part(d.base_name,'.',1)
				LEFT JOIN types b ON b.qualified_name=i.import_path||substr(d.base_name,length(split_part(d.base_name,'.',1))+1)
				UNION ALL
				SELECT d.id,b.id,2,0 FROM types d JOIN types b
				 ON b.java_package=d.java_package AND b.name=d.base_name
				UNION ALL
				SELECT d.id,b.id,3,0 FROM types d JOIN types b ON b.qualified_name=d.base_name
				UNION ALL
				SELECT d.id,b.id,4,0 FROM types d JOIN file_imports i ON i.file_id=d.file_id AND i.import_path LIKE '%.*'
				JOIN types b ON b.qualified_name=left(i.import_path,length(i.import_path)-1)||d.base_name
				UNION ALL
				SELECT d.id,b.id,4,0 FROM types d JOIN types b ON b.qualified_name='java.lang.'||d.base_name
			), ranked_bases AS (
				SELECT *,DENSE_RANK() OVER(PARTITION BY id ORDER BY precedence,depth) AS priority FROM base_candidates
			), bases AS (
				SELECT id,MIN(base_id) AS base_id FROM ranked_bases WHERE priority=1
				GROUP BY id HAVING COUNT(DISTINCT base_id)=1 AND COUNT(*)=COUNT(base_id)
			), lexical AS (
				SELECT fc.id AS fc_id, caller.file_id, caller.start_line AS caller_line,
				       regexp_replace(caller.name, '\.[^.]+$', '') AS class_name,
				       reverse(split_part(reverse(fc.callee_name), '.', 1)) AS method_name, 0 AS depth
				FROM function_calls fc JOIN functions caller ON caller.id=fc.caller_function_id
				JOIN files f ON f.id=caller.file_id
				WHERE fc.callee_function_id IS NULL AND f.repo_id=$1
				  AND ($2::bigint IS NULL OR f.snapshot_id=$2) AND f.language='java'
				  AND regexp_replace(fc.callee_name, '\.[^.]+$', '')=regexp_replace(caller.name, '\.[^.]+$', '')
				  AND fc.callee_name LIKE '%.%'
				  AND NOT EXISTS (SELECT 1 FROM functions enclosing WHERE enclosing.file_id=caller.file_id
				      AND enclosing.start_line<=caller.start_line AND enclosing.end_line>=caller.end_line
				      AND (enclosing.start_line<caller.start_line OR enclosing.end_line>caller.end_line)
				      AND regexp_replace(enclosing.name, '\.[^.]+$', '')=regexp_replace(caller.name, '\.[^.]+$', ''))
				UNION ALL
				SELECT fc_id, file_id, caller_line, regexp_replace(class_name, '\.[^.]+$', ''), method_name, depth+1
				FROM lexical WHERE class_name LIKE '%.%'
			), hierarchy AS (
				SELECT l.fc_id,l.method_name,l.depth AS lexical_depth,0 AS inheritance_depth,
				       t.id,t.file_id,t.name,ARRAY[t.id] AS visited
				FROM lexical l JOIN types t ON t.file_id=l.file_id AND t.name=l.class_name
				  AND l.caller_line BETWEEN t.start_line AND t.end_line
				UNION ALL
				SELECT h.fc_id,h.method_name,h.lexical_depth,h.inheritance_depth+1,
				       t.id,t.file_id,t.name,h.visited||t.id
				FROM hierarchy h JOIN bases b ON b.id=h.id JOIN types t ON t.id=b.base_id
				WHERE NOT(t.id=ANY(h.visited))
			), methods AS (
				SELECT h.fc_id,h.lexical_depth,fn.id AS callee_id,
				       COALESCE(ms.signature,'unknown:'||fn.id::text) AS signature,
				       DENSE_RANK() OVER(PARTITION BY h.fc_id ORDER BY h.lexical_depth,h.inheritance_depth) AS priority
				FROM hierarchy h JOIN functions fn ON fn.file_id=h.file_id AND fn.name=h.name||'.'||h.method_name
				LEFT JOIN method_signatures ms ON ms.function_id=fn.id
			), overloads AS (
				-- Different signatures remain candidates across the whole hierarchy;
				-- proximity does not make an incompatible overload applicable.
				SELECT fc_id,lexical_depth,COUNT(DISTINCT signature) AS signatures
				FROM methods GROUP BY fc_id,lexical_depth
			), unique_method AS (
				SELECT m.fc_id,MIN(m.callee_id) AS callee_id FROM methods m
				JOIN overloads o ON o.fc_id=m.fc_id AND o.lexical_depth=m.lexical_depth AND o.signatures=1
				WHERE m.priority=1 GROUP BY m.fc_id HAVING COUNT(*)=1
			)
			UPDATE function_calls fc SET callee_function_id=m.callee_id,
			    callee_resolution_source='java_inherited_repo',callee_resolution_confidence='medium'
			FROM unique_method m WHERE fc.id=m.fc_id AND fc.callee_function_id IS NULL
		`, repoID, snapshotArg)
		if err != nil {
			return err
		}
	}

	// 3) Unqualified unique in repo: callee_name matches exactly one function in repo (name or Class.name).
	_, err = s.Executor().Exec(ctx, `
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.repo_id = $1
			  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
			  AND fc.callee_name NOT LIKE '%.%'
		),
		unique_names AS (
			SELECT name AS simple_name,callee_id FROM tirion_repo_function_names WHERE kind='simple'
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'unique_repo',
		    callee_resolution_confidence = 'medium'
		FROM caller c JOIN unique_names u ON u.simple_name=c.callee_name
		WHERE fc.id = c.fc_id
		  AND fc.callee_function_id IS NULL
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RefreshTraceCallEdges rebuilds the denormalized trace_call_edges for a repo.
func (s *Storage) RefreshTraceCallEdges(repoID int64) error {
	return s.RefreshTraceCallEdgesForSnapshot(repoID, nil)
}

func (s *Storage) RefreshTraceCallEdgesForSnapshot(repoID int64, snapshotID *int64) error {
	ctx := context.Background()
	tx, err := s.Executor().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	snapshotArg := nullableSnapshotID(snapshotID)
	deleteQuery := `DELETE FROM trace_call_edges WHERE repo_id = $1 AND ($2::bigint IS NULL OR snapshot_id = $2)`
	// Resolution can make previously distinct call records identical. Normalize
	// only this snapshot, atomically with its Trace rebuild, never on server startup.
	if _, err := tx.Exec(ctx, `
		WITH ranked AS (
			SELECT fc.id, ROW_NUMBER() OVER (
				PARTITION BY fc.caller_function_id, COALESCE(fc.callee_function_id, 0),
					fc.callee_name, COALESCE(fc.line_number, -1), fc.is_async, fc.is_callback_argument
				ORDER BY fc.id
			) AS rn
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE f.repo_id = $1 AND ($2::bigint IS NULL OR f.snapshot_id = $2)
		)
		DELETE FROM function_calls fc USING ranked r WHERE fc.id = r.id AND r.rn > 1
	`, repoID, snapshotArg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, deleteQuery, repoID, snapshotArg); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO trace_call_edges (
			repo_id,
			snapshot_id,
			caller_function_id,
			callee_function_id,
			caller_id,
			callee_id,
			callee_name,
			callee_repo,
			callee_file,
			callee_func,
			line_number,
			callee_resolution_source,
			callee_resolution_confidence,
			unresolved_reason
		)
		SELECT
			r.id,
			f.snapshot_id,
			fn.id,
			fn2.id,
			r.name || ':' || f.path || ':' || fn.name AS caller_id,
			CASE
				WHEN fn2.id IS NULL THEN NULL
				ELSE r2.name || ':' || f2.path || ':' || fn2.name
			END AS callee_id,
			fc.callee_name,
			r2.name,
			f2.path,
			fn2.name,
			fc.line_number,
			COALESCE(fc.callee_resolution_source, CASE WHEN fn2.id IS NULL THEN NULL ELSE 'parser' END),
			COALESCE(fc.callee_resolution_confidence, CASE WHEN fn2.id IS NULL THEN NULL ELSE 'high' END),
			fc.unresolved_reason
		FROM function_calls fc
		JOIN functions fn ON fc.caller_function_id = fn.id
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN functions fn2 ON fn2.id = fc.callee_function_id
		LEFT JOIN files f2 ON f2.id = fn2.file_id
		LEFT JOIN repositories r2 ON r2.id = f2.repo_id
		WHERE f.repo_id = $1
		  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Storage) RefreshTraceInterfaceImplsForSnapshot(repoID int64, snapshotID *int64) error {
	ctx := context.Background()
	tx, err := s.Executor().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	snapshotArg := nullableSnapshotID(snapshotID)
	if _, err := tx.Exec(ctx, `DELETE FROM trace_interface_impls WHERE repo_id = $1 AND ($2::bigint IS NULL OR snapshot_id = $2)`, repoID, snapshotArg); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO trace_interface_impls (
			repo_id,
			snapshot_id,
			interface_name,
			class_id,
			class_name,
			class_repo,
			class_file
		)
		SELECT
			r.id,
			f.snapshot_id,
			COALESCE(i.name, impl.interface_name) AS interface_name,
			c.id AS class_id,
			c.name AS class_name,
			r.name AS class_repo,
			f.path AS class_file
		FROM implementations impl
		JOIN classes c ON impl.class_id = c.id
		JOIN files f ON c.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN interfaces i ON impl.interface_id = i.id
		WHERE r.id = $1
		  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
	`, repoID, snapshotArg)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertEndpoint inserts an endpoint
func (s *Storage) InsertEndpoint(repoID int64, path, method string, handlerID, fileID *int64, lineNumber *int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO endpoints (repo_id, path, method, handler_function_id, file_id, line_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, repoID, path, method, handlerID, fileID, lineNumber).Scan(&id)
	return id, err
}

// InsertInterface inserts an interface
func (s *Storage) InsertInterface(fileID int64, name string, startLine, endLine int, extendsInterfaces []string, isFunctional, isExported bool) (int64, error) {
	var extendsJSON []byte
	if len(extendsInterfaces) > 0 {
		extendsJSON, _ = json.Marshal(extendsInterfaces)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO interfaces (file_id, name, start_line, end_line, extends_interfaces, is_functional, is_exported)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(file_id, name) DO UPDATE SET
			start_line = EXCLUDED.start_line,
			end_line = EXCLUDED.end_line,
			extends_interfaces = EXCLUDED.extends_interfaces,
			is_functional = EXCLUDED.is_functional,
			is_exported = EXCLUDED.is_exported
		RETURNING id
	`, fileID, name, startLine, endLine, extendsJSON, isFunctional, isExported).Scan(&id)
	return id, err
}

// InsertImplementation inserts a class-interface implementation relationship
func (s *Storage) InsertImplementation(classID int64, interfaceName string, interfaceID *int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO implementations (class_id, interface_name, interface_id)
		VALUES ($1, $2, $3)
		ON CONFLICT(class_id, interface_name) DO UPDATE SET
			interface_id = EXCLUDED.interface_id
		RETURNING id
	`, classID, interfaceName, interfaceID).Scan(&id)
	return id, err
}

// InsertField inserts a field declaration
func (s *Storage) InsertField(classID, interfaceID *int64, name, fieldType string, typeParams, modifiers []string, startLine int, isInjected bool, injectionType string) (int64, error) {
	var typeParamsJSON, modifiersJSON []byte
	if len(typeParams) > 0 {
		typeParamsJSON, _ = json.Marshal(typeParams)
	}
	if len(modifiers) > 0 {
		modifiersJSON, _ = json.Marshal(modifiers)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO fields (class_id, interface_id, name, field_type, type_parameters, modifiers, start_line, is_injected, injection_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`, classID, interfaceID, name, fieldType, typeParamsJSON, modifiersJSON, startLine, isInjected, injectionType).Scan(&id)
	return id, err
}

// InsertAnnotation inserts an annotation
func (s *Storage) InsertAnnotation(entityType string, entityID int64, name string, values map[string]interface{}, lineNumber int) (int64, error) {
	var valuesJSON []byte
	if len(values) > 0 {
		valuesJSON, _ = json.Marshal(values)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO annotations (entity_type, entity_id, name, values, line_number)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, entityType, entityID, name, valuesJSON, lineNumber).Scan(&id)
	return id, err
}

// InsertMethodSignature inserts a method signature
func (s *Storage) InsertMethodSignature(functionID int64, signature string, parameterTypes []string, returnType string, isOverride bool) (int64, error) {
	var paramTypesJSON []byte
	if len(parameterTypes) > 0 {
		paramTypesJSON, _ = json.Marshal(parameterTypes)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO method_signatures (function_id, signature, parameter_types, return_type, is_override)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(function_id) DO UPDATE SET
			signature = EXCLUDED.signature,
			parameter_types = EXCLUDED.parameter_types,
			return_type = EXCLUDED.return_type,
			is_override = EXCLUDED.is_override
		RETURNING id
	`, functionID, signature, paramTypesJSON, returnType, isOverride).Scan(&id)
	return id, err
}

// InsertConstructorParam inserts a constructor parameter
func (s *Storage) InsertConstructorParam(classID int64, paramName, paramType string, paramIndex int, isInjected bool, annotation, annotationValue string, lineNumber int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO constructor_params (class_id, param_name, param_type, param_index, is_injected, annotation, annotation_value, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, classID, paramName, paramType, paramIndex, isInjected, annotation, annotationValue, lineNumber).Scan(&id)
	return id, err
}

// InsertTypeParameter inserts a type parameter (generic)
func (s *Storage) InsertTypeParameter(entityType string, entityID int64, paramName string, paramIndex int, bounds []string, boundType string) (int64, error) {
	var boundsJSON []byte
	if len(bounds) > 0 {
		boundsJSON, _ = json.Marshal(bounds)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO type_parameters (entity_type, entity_id, param_name, param_index, bounds, bound_type)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, entityType, entityID, paramName, paramIndex, boundsJSON, boundType).Scan(&id)
	return id, err
}

// InsertHttpInterfaceMethod inserts an HTTP interface method (Retrofit/Feign)
func (s *Storage) InsertHttpInterfaceMethod(interfaceID int64, methodName string, functionID *int64, httpMethod, urlPattern string, lineNumber int) (int64, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO http_interface_methods (interface_id, method_name, function_id, http_method, url_pattern, line_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(interface_id, method_name) DO UPDATE SET
			function_id = EXCLUDED.function_id,
			http_method = EXCLUDED.http_method,
			url_pattern = EXCLUDED.url_pattern,
			line_number = EXCLUDED.line_number
		RETURNING id
	`, interfaceID, methodName, functionID, httpMethod, urlPattern, lineNumber).Scan(&id)
	return id, err
}

// InsertBeanDefinition inserts a @Bean method definition
func (s *Storage) InsertBeanDefinition(configClassID int64, methodID *int64, beanName, beanType string, qualifiers []string, isPrimary bool, lineNumber int) (int64, error) {
	var qualifiersJSON []byte
	if len(qualifiers) > 0 {
		qualifiersJSON, _ = json.Marshal(qualifiers)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO bean_definitions (config_class_id, method_id, bean_name, bean_type, qualifiers, is_primary, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, configClassID, methodID, beanName, beanType, qualifiersJSON, isPrimary, lineNumber).Scan(&id)
	return id, err
}

// InsertEventListener inserts an @EventListener method
func (s *Storage) InsertEventListener(classID int64, methodID *int64, methodName string, eventTypes []string, condition string, isAsync bool, lineNumber int) (int64, error) {
	var eventTypesJSON []byte
	if len(eventTypes) > 0 {
		eventTypesJSON, _ = json.Marshal(eventTypes)
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO event_listeners (class_id, method_id, method_name, event_types, condition, is_async, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, classID, methodID, methodName, eventTypesJSON, condition, isAsync, lineNumber).Scan(&id)
	return id, err
}

// InsertScheduledMethod inserts a @Scheduled method
func (s *Storage) InsertScheduledMethod(classID int64, methodID *int64, methodName, cron string, fixedRate, fixedDelay, initialDelay int64, lineNumber int) (int64, error) {
	var cronPtr, fixedRatePtr, fixedDelayPtr, initialDelayPtr interface{}
	if cron != "" {
		cronPtr = cron
	}
	if fixedRate > 0 {
		fixedRatePtr = fixedRate
	}
	if fixedDelay > 0 {
		fixedDelayPtr = fixedDelay
	}
	if initialDelay > 0 {
		initialDelayPtr = initialDelay
	}

	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO scheduled_methods (class_id, method_id, method_name, cron, fixed_rate, fixed_delay, initial_delay, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, classID, methodID, methodName, cronPtr, fixedRatePtr, fixedDelayPtr, initialDelayPtr, lineNumber).Scan(&id)
	return id, err
}

// GetStats returns database statistics
func (s *Storage) GetStats(filters ...SnapshotFilter) (Stats, error) {
	var stats Stats

	if filter, ok := optionalSnapshotFilter(filters); ok {
		selected := "SELECT id, repo_id FROM files WHERE TRUE"
		args := []any{}
		endpointScope := "file_id IN (SELECT id FROM selected_files)"
		if filter.IncludeLegacy {
			endpointScope += " OR file_id IS NULL"
		}
		argNum := 1
		appendSnapshotFilter(&selected, &args, &argNum, "snapshot_id", filter)
		err := s.pool.QueryRow(context.Background(), `WITH selected_files AS (`+selected+`)
 SELECT (SELECT COUNT(DISTINCT repo_id) FROM selected_files),
 (SELECT COUNT(*) FROM selected_files),
 (SELECT COUNT(*) FROM functions WHERE file_id IN (SELECT id FROM selected_files)),
 (SELECT COUNT(*) FROM classes WHERE file_id IN (SELECT id FROM selected_files)),
 (SELECT COUNT(*) FROM endpoints WHERE `+endpointScope+`)`, args...).Scan(&stats.Repos, &stats.Files, &stats.Functions, &stats.Classes, &stats.Endpoints)
		return stats, err
	}

	queries := map[string]*int{
		"SELECT COUNT(*) FROM repositories": &stats.Repos,
		"SELECT COUNT(*) FROM files":        &stats.Files,
		"SELECT COUNT(*) FROM functions":    &stats.Functions,
		"SELECT COUNT(*) FROM classes":      &stats.Classes,
		"SELECT COUNT(*) FROM endpoints":    &stats.Endpoints,
	}

	for q, dest := range queries {
		if err := s.pool.QueryRow(context.Background(), q).Scan(dest); err != nil {
			return stats, err
		}
	}

	return stats, nil
}

// Polymorphic metadata has no foreign keys; remove it while owners still exist.
func deleteFileMetadata(ctx context.Context, tx pgx.Tx, repoID int64, snapshotID *int64, path *string) error {
	const owners = `WITH selected_files AS (
 SELECT id FROM files WHERE repo_id=$1 AND ($2::bigint IS NULL OR snapshot_id=$2) AND ($3::text IS NULL OR path=$3)
 ), owners AS (
 SELECT 'class' AS kind, id FROM classes WHERE file_id IN (SELECT id FROM selected_files)
 UNION ALL SELECT 'interface', id FROM interfaces WHERE file_id IN (SELECT id FROM selected_files)
 UNION ALL SELECT 'method', id FROM functions WHERE file_id IN (SELECT id FROM selected_files)
 UNION ALL SELECT 'field', id FROM fields WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM selected_files))
 OR interface_id IN (SELECT id FROM interfaces WHERE file_id IN (SELECT id FROM selected_files))
 UNION ALL SELECT 'parameter', id FROM constructor_params WHERE class_id IN (SELECT id FROM classes WHERE file_id IN (SELECT id FROM selected_files))
 ) `
	for _, table := range []string{"annotations", "type_parameters"} {
		if _, err := tx.Exec(ctx, owners+`DELETE FROM `+table+` m USING owners o WHERE m.entity_type=o.kind AND m.entity_id=o.id`, repoID, snapshotID, path); err != nil {
			return err
		}
	}
	return nil
}

// WithQueryContext shares the pool while binding read helpers to one request.
func (s *Storage) WithQueryContext(ctx context.Context) *Storage {
	clone := *s
	clone.queryCtx = ctx
	return &clone
}
func (s *Storage) queryContext() context.Context {
	if s.queryCtx != nil {
		return s.queryCtx
	}
	return context.Background()
}

// EscapeLike treats search input as literal text; only the caller supplies wildcards.
func EscapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
