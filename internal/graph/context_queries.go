package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func placeholder(n int) string {
	return "$" + strconv.Itoa(n)
}

// =============================================================================
// API QUERY RESULT TYPES
// =============================================================================

// RepoStats contains statistics for a single repository
type RepoStats struct {
	Functions       int    `json:"functions"`
	Classes         int    `json:"classes"`
	Endpoints       int    `json:"endpoints"`
	Files           int    `json:"files"`
	PrimaryLanguage string `json:"primaryLanguage"`
}

// EndpointInfo contains full endpoint details
type EndpointInfo struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Handler    string `json:"handler"`
	FilePath   string `json:"file_path"`
	Line       int    `json:"line"`
	RepoName   string `json:"repo_name"`
	SnapshotID *int64 `json:"snapshotId,omitempty"`
}

// GraphQLOperationInfo contains indexed GraphQL document operation details.
type GraphQLOperationInfo struct {
	ID            int64  `json:"id"`
	OperationName string `json:"operation_name"`
	OperationType string `json:"operation_type"`
	FilePath      string `json:"file_path"`
	Line          int    `json:"line"`
	RepoName      string `json:"repo_name"`
	UsageCount    int    `json:"usage_count"`
	SnapshotID    *int64 `json:"snapshotId,omitempty"`
}

// GraphQLOperationResolverInfo contains backend resolver exports for GraphQL operations.
type GraphQLOperationResolverInfo struct {
	ID            int64  `json:"id"`
	OperationName string `json:"operation_name"`
	OperationType string `json:"operation_type"`
	ResolverName  string `json:"resolver_name"`
	FilePath      string `json:"file_path"`
	Line          int    `json:"line"`
	RepoName      string `json:"repo_name"`
	SnapshotID    *int64 `json:"snapshotId,omitempty"`
}

// GraphQLOperationUsageInfo contains frontend/backend usage sites for imported GraphQL documents.
type GraphQLOperationUsageInfo struct {
	ID                   int64   `json:"id"`
	ImportPath           string  `json:"import_path"`
	ImportedAs           string  `json:"imported_as"`
	CallerFunction       string  `json:"caller_function"`
	FilePath             string  `json:"file_path"`
	Line                 int     `json:"line"`
	RepoName             string  `json:"repo_name"`
	SnapshotID           *int64  `json:"snapshotId,omitempty"`
	ResolvedOperationID  *int64  `json:"resolved_operation_id,omitempty"`
	ResolvedOperation    *string `json:"resolved_operation_name,omitempty"`
	ResolutionConfidence *string `json:"resolution_confidence,omitempty"`
}

// GraphQLBackendEntrypointInfo contains backend GraphQL registration points.
type GraphQLBackendEntrypointInfo struct {
	ID               int64  `json:"id"`
	HandlerName      string `json:"handler_name"`
	RegistrationKind string `json:"registration_kind"`
	ControllersPath  string `json:"controllers_path"`
	FilePath         string `json:"file_path"`
	Line             int    `json:"line"`
	RepoName         string `json:"repo_name"`
	ControllerCount  int    `json:"controller_count"`
	SnapshotID       *int64 `json:"snapshotId,omitempty"`
}

// GraphQLControllerLinkInfo contains controller files associated with a backend GraphQL entrypoint.
type GraphQLControllerLinkInfo struct {
	EntrypointID          int64  `json:"entrypoint_id"`
	EntrypointHandlerName string `json:"entrypoint_handler_name"`
	EntrypointFilePath    string `json:"entrypoint_file_path"`
	ControllerFileID      int64  `json:"controller_file_id"`
	ControllerFilePath    string `json:"controller_file_path"`
	RepoName              string `json:"repo_name"`
	ResolutionConfidence  string `json:"resolution_confidence"`
	EntrypointSnapshotID  *int64 `json:"entrypointSnapshotId,omitempty"`
	ControllerSnapshotID  *int64 `json:"controllerSnapshotId,omitempty"`
}

// AzureFunctionTriggerInfo contains parsed Azure Function trigger metadata.
type AzureFunctionTriggerInfo struct {
	ID           int64    `json:"id"`
	FunctionName string   `json:"function_name"`
	TriggerType  string   `json:"trigger_type"`
	BindingName  string   `json:"binding_name"`
	Direction    string   `json:"direction"`
	Methods      []string `json:"methods,omitempty"`
	Route        string   `json:"route,omitempty"`
	AuthLevel    string   `json:"auth_level,omitempty"`
	Schedule     string   `json:"schedule,omitempty"`
	Connection   string   `json:"connection,omitempty"`
	QueueName    string   `json:"queue_name,omitempty"`
	TopicName    string   `json:"topic_name,omitempty"`
	Subscription string   `json:"subscription,omitempty"`
	ScriptFile   string   `json:"script_file,omitempty"`
	FilePath     string   `json:"file_path"`
	Line         int      `json:"line"`
	RepoName     string   `json:"repo_name"`
	SnapshotID   *int64   `json:"snapshotId,omitempty"`
}

type SnapshotFilter struct {
	SnapshotIDs   []int64
	IncludeLegacy bool
}

func optionalSnapshotFilter(filters []SnapshotFilter) (SnapshotFilter, bool) {
	if len(filters) == 0 {
		return SnapshotFilter{}, false
	}
	filter := filters[0]
	filter.SnapshotIDs = uniqueInt64s(filter.SnapshotIDs)
	return filter, true
}

func appendSnapshotFilter(query *string, args *[]interface{}, argNum *int, column string, filter SnapshotFilter) {
	filter.SnapshotIDs = uniqueInt64s(filter.SnapshotIDs)
	switch {
	case len(filter.SnapshotIDs) > 0 && filter.IncludeLegacy:
		*query += " AND (" + column + " = ANY(" + placeholder(*argNum) + ") OR " + column + " IS NULL)"
		*args = append(*args, filter.SnapshotIDs)
		*argNum++
	case len(filter.SnapshotIDs) > 0:
		*query += " AND " + column + " = ANY(" + placeholder(*argNum) + ")"
		*args = append(*args, filter.SnapshotIDs)
		*argNum++
	case filter.IncludeLegacy:
		*query += " AND " + column + " IS NULL"
	default:
		*query += " AND FALSE"
	}
}

func uniqueInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// =============================================================================
// QUERY METHODS
// =============================================================================

// GetRepoStats returns statistics for a specific repository
func (s *Storage) GetRepoStats(repoID int64, filters ...SnapshotFilter) (*RepoStats, error) {
	stats := &RepoStats{}
	selected := "SELECT id, language FROM files WHERE repo_id=$1"
	args := []any{repoID}
	endpointScope := "repo_id=$1"
	if filter, ok := optionalSnapshotFilter(filters); ok {
		endpointScope += " AND (file_id IN (SELECT id FROM selected_files)"
		if filter.IncludeLegacy {
			endpointScope += " OR file_id IS NULL"
		}
		endpointScope += ")"
		argNum := len(args) + 1
		appendSnapshotFilter(&selected, &args, &argNum, "snapshot_id", filter)
	}
	err := s.pool.QueryRow(context.Background(), `WITH selected_files AS (`+selected+`)
 SELECT
 (SELECT COUNT(*) FROM functions WHERE file_id IN (SELECT id FROM selected_files)),
 (SELECT COUNT(*) FROM classes WHERE file_id IN (SELECT id FROM selected_files)),
 (SELECT COUNT(*) FROM endpoints WHERE `+endpointScope+`),
 (SELECT COUNT(*) FROM selected_files)`, args...).Scan(&stats.Functions, &stats.Classes, &stats.Endpoints, &stats.Files)
	if err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(context.Background(), `WITH selected_files AS (`+selected+`)
 SELECT COALESCE(language, 'unknown') FROM selected_files GROUP BY language ORDER BY COUNT(*) DESC, language LIMIT 1`, args...).Scan(&stats.PrimaryLanguage)
	if errors.Is(err, pgx.ErrNoRows) {
		stats.PrimaryLanguage = "mixed"
	} else if err != nil {
		return nil, err
	}
	return stats, nil
}

// GetEndpoints returns endpoints with optional filtering
func (s *Storage) GetEndpoints(repo, method, path, pattern string, limit, offset int, filters ...SnapshotFilter) ([]EndpointInfo, error) {
	query := `
		SELECT e.method, e.path,
		       COALESCE(fn.name, 'handler') as handler,
		       fi.path as file_path,
		       COALESCE(e.line_number, 0) as line,
		       r.name as repo_name,
		       fi.snapshot_id
		FROM endpoints e
		JOIN files fi ON e.file_id = fi.id
		JOIN repositories r ON e.repo_id = r.id
		LEFT JOIN functions fn ON e.handler_function_id = fn.id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if method != "" {
		query += " AND UPPER(e.method) = UPPER(" + placeholder(argNum) + ")"
		args = append(args, method)
		argNum++
	}
	if path != "" {
		query += " AND e.path ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(path)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (e.path ILIKE " + placeholder(argNum) + " OR COALESCE(fn.name, '') ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += " ORDER BY e.method, e.path, e.id LIMIT " + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EndpointInfo
	for rows.Next() {
		var ep EndpointInfo
		if err := rows.Scan(&ep.Method, &ep.Path, &ep.Handler, &ep.FilePath, &ep.Line, &ep.RepoName, &ep.SnapshotID); err != nil {
			return nil, err
		}
		results = append(results, ep)
	}
	return results, rows.Err()
}

// GetGraphQLOperations returns indexed GraphQL document operations with optional filtering.
func (s *Storage) GetGraphQLOperations(repo, operationType, name, pattern string, limit, offset int, filters ...SnapshotFilter) ([]GraphQLOperationInfo, error) {
	query := `
		SELECT o.id, o.operation_name, o.operation_type, fi.path, COALESCE(o.line_number, 0), r.name,
		       fi.snapshot_id,
		       COUNT(DISTINCT l.usage_id) AS usage_count
		FROM graphql_operations o
		JOIN files fi ON o.file_id = fi.id
		JOIN repositories r ON o.repo_id = r.id
		LEFT JOIN graphql_usage_operation_links l ON l.operation_id = o.id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if operationType != "" {
		query += " AND LOWER(o.operation_type) = LOWER(" + placeholder(argNum) + ")"
		args = append(args, operationType)
		argNum++
	}
	if name != "" {
		query += " AND o.operation_name ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(name)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (o.operation_name ILIKE " + placeholder(argNum) + " OR fi.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += `
		GROUP BY o.id, o.operation_name, o.operation_type, fi.path, o.line_number, r.name, fi.snapshot_id
		ORDER BY o.operation_type, o.operation_name, r.name, fi.path, o.id
		LIMIT ` + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphQLOperationInfo
	for rows.Next() {
		var item GraphQLOperationInfo
		if err := rows.Scan(&item.ID, &item.OperationName, &item.OperationType, &item.FilePath, &item.Line, &item.RepoName, &item.SnapshotID, &item.UsageCount); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// GetGraphQLOperationResolvers returns backend resolver exports for GraphQL operations.
func (s *Storage) GetGraphQLOperationResolvers(repo, operationType, name, pattern string, limit, offset int, filters ...SnapshotFilter) ([]GraphQLOperationResolverInfo, error) {
	query := `
		SELECT gr.id, gr.operation_name, gr.operation_type, gr.resolver_name,
		       fi.path, COALESCE(gr.line_number, 0), r.name, fi.snapshot_id
		FROM graphql_operation_resolvers gr
		JOIN files fi ON gr.file_id = fi.id
		JOIN repositories r ON gr.repo_id = r.id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if operationType != "" {
		query += " AND LOWER(gr.operation_type) = LOWER(" + placeholder(argNum) + ")"
		args = append(args, operationType)
		argNum++
	}
	if name != "" {
		query += " AND gr.operation_name ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(name)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (gr.operation_name ILIKE " + placeholder(argNum) +
			" OR gr.resolver_name ILIKE " + placeholder(argNum) +
			" OR fi.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += " ORDER BY gr.operation_type, gr.operation_name, r.name, fi.path, gr.line_number, gr.id LIMIT " + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphQLOperationResolverInfo
	for rows.Next() {
		var item GraphQLOperationResolverInfo
		if err := rows.Scan(&item.ID, &item.OperationName, &item.OperationType, &item.ResolverName, &item.FilePath, &item.Line, &item.RepoName, &item.SnapshotID); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// GetGraphQLOperationUsages returns usage sites for imported GraphQL document operations.
func (s *Storage) GetGraphQLOperationUsages(repo, caller, pattern string, limit, offset int, filters ...SnapshotFilter) ([]GraphQLOperationUsageInfo, error) {
	query := `
		SELECT u.id, u.import_path, u.imported_as, u.caller_function, fi.path, COALESCE(u.line_number, 0), r.name,
		       fi.snapshot_id, o.id, o.operation_name, l.resolution_confidence
		FROM graphql_operation_usages u
		JOIN files fi ON u.file_id = fi.id
		JOIN repositories r ON u.repo_id = r.id
		LEFT JOIN graphql_usage_operation_links l ON l.usage_id = u.id
		LEFT JOIN graphql_operations o ON o.id = l.operation_id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if caller != "" {
		query += " AND COALESCE(u.caller_function, '') ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(caller)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (u.import_path ILIKE " + placeholder(argNum) +
			" OR COALESCE(u.imported_as, '') ILIKE " + placeholder(argNum) +
			" OR COALESCE(o.operation_name, '') ILIKE " + placeholder(argNum) +
			" OR COALESCE(u.caller_function, '') ILIKE " + placeholder(argNum) +
			" OR fi.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += `
		ORDER BY r.name, fi.path, u.line_number, u.import_path, u.id, o.id
		LIMIT ` + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphQLOperationUsageInfo
	for rows.Next() {
		var item GraphQLOperationUsageInfo
		var opID *int64
		var opName *string
		var confidence *string
		if err := rows.Scan(
			&item.ID,
			&item.ImportPath,
			&item.ImportedAs,
			&item.CallerFunction,
			&item.FilePath,
			&item.Line,
			&item.RepoName,
			&item.SnapshotID,
			&opID,
			&opName,
			&confidence,
		); err != nil {
			return nil, err
		}
		item.ResolvedOperationID = opID
		item.ResolvedOperation = opName
		item.ResolutionConfidence = confidence
		results = append(results, item)
	}
	return results, rows.Err()
}

// GetGraphQLBackendEntrypoints returns backend GraphQL registration points.
func (s *Storage) GetGraphQLBackendEntrypoints(repo, registrationKind, pattern string, limit, offset int, filters ...SnapshotFilter) ([]GraphQLBackendEntrypointInfo, error) {
	query := `
		SELECT e.id, e.handler_name, e.registration_kind, COALESCE(e.controllers_path, ''), fi.path, COALESCE(e.line_number, 0), r.name,
		       fi.snapshot_id, COUNT(DISTINCT l.file_id) AS controller_count
		FROM graphql_backend_entrypoints e
		JOIN files fi ON e.file_id = fi.id
		JOIN repositories r ON e.repo_id = r.id
		LEFT JOIN graphql_backend_controller_links l ON l.entrypoint_id = e.id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if registrationKind != "" {
		query += " AND e.registration_kind ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(registrationKind)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (COALESCE(e.handler_name, '') ILIKE " + placeholder(argNum) +
			" OR COALESCE(e.controllers_path, '') ILIKE " + placeholder(argNum) +
			" OR fi.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += `
		GROUP BY e.id, e.handler_name, e.registration_kind, e.controllers_path, fi.path, e.line_number, r.name, fi.snapshot_id
		ORDER BY r.name, fi.path, e.line_number, e.id
		LIMIT ` + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphQLBackendEntrypointInfo
	for rows.Next() {
		var item GraphQLBackendEntrypointInfo
		if err := rows.Scan(&item.ID, &item.HandlerName, &item.RegistrationKind, &item.ControllersPath, &item.FilePath, &item.Line, &item.RepoName, &item.SnapshotID, &item.ControllerCount); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// GetAzureFunctionTriggers returns parsed Azure Function trigger metadata with optional filtering.
func (s *Storage) GetAzureFunctionTriggers(repo, triggerType, functionName, pattern string, limit, offset int, filters ...SnapshotFilter) ([]AzureFunctionTriggerInfo, error) {
	query := `
		SELECT t.id, t.function_name, t.trigger_type, COALESCE(t.binding_name, ''), COALESCE(t.direction, ''),
		       COALESCE(t.http_methods, '[]'::jsonb), COALESCE(t.route, ''), COALESCE(t.auth_level, ''),
		       COALESCE(t.schedule_expression, ''), COALESCE(t.connection_name, ''), COALESCE(t.resource_name, ''),
		       COALESCE(t.script_file, ''),
		       fi.path, COALESCE(t.line_number, 0), r.name, fi.snapshot_id
		FROM azure_function_triggers t
		JOIN files fi ON t.file_id = fi.id
		JOIN repositories r ON t.repo_id = r.id
		WHERE 1=1
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if triggerType != "" {
		query += " AND t.trigger_type ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(triggerType)+"%")
		argNum++
	}
	if functionName != "" {
		query += " AND t.function_name ILIKE " + placeholder(argNum)
		args = append(args, "%"+EscapeLike(functionName)+"%")
		argNum++
	}
	if pattern != "" {
		query += " AND (t.function_name ILIKE " + placeholder(argNum) +
			" OR COALESCE(t.route, '') ILIKE " + placeholder(argNum) +
			" OR COALESCE(t.resource_name, '') ILIKE " + placeholder(argNum) +
			" OR fi.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += " ORDER BY r.name, t.function_name, t.trigger_type, fi.path, t.id LIMIT " + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []AzureFunctionTriggerInfo
	for rows.Next() {
		var item AzureFunctionTriggerInfo
		var methodsJSON []byte
		var resourceName string
		if err := rows.Scan(
			&item.ID,
			&item.FunctionName,
			&item.TriggerType,
			&item.BindingName,
			&item.Direction,
			&methodsJSON,
			&item.Route,
			&item.AuthLevel,
			&item.Schedule,
			&item.Connection,
			&resourceName,
			&item.ScriptFile,
			&item.FilePath,
			&item.Line,
			&item.RepoName,
			&item.SnapshotID,
		); err != nil {
			return nil, err
		}
		if len(methodsJSON) > 0 {
			if err := json.Unmarshal(methodsJSON, &item.Methods); err != nil {
				return nil, err
			}
		}
		switch item.TriggerType {
		case "queueTrigger":
			item.QueueName = resourceName
		case "serviceBusTrigger":
			item.QueueName, item.TopicName, item.Subscription = parseAzureServiceBusResourceName(resourceName)
		default:
			item.QueueName = resourceName
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func parseAzureServiceBusResourceName(resourceName string) (queueName, topicName, subscription string) {
	resourceName = strings.TrimSpace(resourceName)
	if resourceName == "" {
		return "", "", ""
	}
	lower := strings.ToLower(resourceName)
	marker := "/subscriptions/"
	if idx := strings.Index(lower, marker); idx >= 0 {
		topicName = strings.Trim(strings.TrimSpace(resourceName[:idx]), "/")
		subscription = strings.Trim(strings.TrimSpace(resourceName[idx+len(marker):]), "/")
		return "", topicName, subscription
	}
	return resourceName, "", ""
}

// GetGraphQLControllerLinks returns backend controller files linked to GraphQL entrypoints.
func (s *Storage) GetGraphQLControllerLinks(repo, pattern string, limit, offset int, filters ...SnapshotFilter) ([]GraphQLControllerLinkInfo, error) {
	query := `
		SELECT l.entrypoint_id, COALESCE(e.handler_name, ''), epf.path,
		       l.file_id, cf.path, r.name, l.resolution_confidence,
		       epf.snapshot_id, cf.snapshot_id
		FROM graphql_backend_controller_links l
		JOIN graphql_backend_entrypoints e ON e.id = l.entrypoint_id
		JOIN files epf ON epf.id = e.file_id
		JOIN files cf ON cf.id = l.file_id
		JOIN repositories r ON r.id = l.repo_id
		WHERE epf.path NOT LIKE '.codebase-snapshots/%'
		  AND cf.path NOT LIKE '.codebase-snapshots/%'
	`
	args := []interface{}{}
	argNum := 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "epf.snapshot_id", filter)
		appendSnapshotFilter(&query, &args, &argNum, "cf.snapshot_id", filter)
	}

	if strings.TrimSpace(repo) != "" {
		query += " AND r.name ILIKE " + placeholder(argNum)
		args = append(args, RepoFilterPattern(repo))
		argNum++
	}
	if pattern != "" {
		query += " AND (COALESCE(e.handler_name, '') ILIKE " + placeholder(argNum) +
			" OR epf.path ILIKE " + placeholder(argNum) +
			" OR cf.path ILIKE " + placeholder(argNum) + ")"
		args = append(args, "%"+EscapeLike(pattern)+"%")
		argNum++
	}

	query += " ORDER BY r.name, epf.path, cf.path, l.entrypoint_id, l.file_id LIMIT " + placeholder(argNum)
	args = append(args, limit)
	argNum++
	if offset > 0 {
		query += " OFFSET " + placeholder(argNum)
		args = append(args, offset)
	}

	rows, err := s.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []GraphQLControllerLinkInfo
	for rows.Next() {
		var item GraphQLControllerLinkInfo
		if err := rows.Scan(
			&item.EntrypointID,
			&item.EntrypointHandlerName,
			&item.EntrypointFilePath,
			&item.ControllerFileID,
			&item.ControllerFilePath,
			&item.RepoName,
			&item.ResolutionConfidence,
			&item.EntrypointSnapshotID,
			&item.ControllerSnapshotID,
		); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}
