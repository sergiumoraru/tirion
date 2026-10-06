package handlers

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/parser"
)

type FunctionIntegration struct {
	Type             string `json:"type,omitempty"`
	CallerID         string `json:"callerId"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	ClientType       string `json:"clientType,omitempty"`
	TargetRepo       string `json:"targetRepo"`
	TargetFile       string `json:"targetFile"`
	TargetHandler    string `json:"targetHandler,omitempty"`
	LineNumber       int    `json:"lineNumber"`
	Resolution       string `json:"resolution"`
	TargetSnapshotID *int64 `json:"targetSnapshotId,omitempty"`
}

type FunctionIntegrationsResponse struct {
	Workspace    ResponseWorkspace     `json:"workspace"`
	RepoContext  []ResponseRepoContext `json:"repoContext,omitempty"`
	CallerID     string                `json:"callerId"`
	Integrations []FunctionIntegration `json:"integrations"`
}

func (h *Handlers) GetFunctionIntegrations(w http.ResponseWriter, r *http.Request) {
	callerID := strings.TrimSpace(r.URL.Query().Get("callerId"))
	if callerID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing callerId", nil)
		return
	}

	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}

	if err := h.requireCaller(r.Context(), callerID, workspaceScope); err != nil {
		writeLookupError(w, err)
		return
	}
	integrations, err := h.loadFunctionIntegrationsScoped(r.Context(), callerID, true, workspaceScope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load function integrations", nil)
		return
	}
	if integrations == nil {
		integrations = []FunctionIntegration{}
	}

	writeJSON(w, http.StatusOK, FunctionIntegrationsResponse{
		Workspace:    workspaceScope.Workspace,
		RepoContext:  workspaceScope.RepoContext,
		CallerID:     callerID,
		Integrations: integrations,
	})
}

func (h *Handlers) loadMatchedFunctionIntegrations(ctx context.Context, callerID string) ([]FunctionIntegration, error) {
	batch, err := h.loadMatchedFunctionIntegrationsBatch(ctx, []string{callerID})
	if err != nil {
		return nil, err
	}
	return batch[callerID], nil
}

func (h *Handlers) loadMatchedFunctionIntegrationsBatch(ctx context.Context, callerIDs []string) (map[string][]FunctionIntegration, error) {
	return h.loadMatchedFunctionIntegrationsBatchForScope(ctx, callerIDs, searchWorkspaceScope{})
}

func (h *Handlers) loadMatchedFunctionIntegrationsBatchForScope(ctx context.Context, callerIDs []string, scope searchWorkspaceScope) (map[string][]FunctionIntegration, error) {
	result := make(map[string][]FunctionIntegration, len(callerIDs))
	callerIDs = uniqueIntegrationStrings(callerIDs)
	if len(callerIDs) == 0 {
		return result, nil
	}
	snapshotIDs := activeSnapshotIDs(scope)
	sourceClause := "TRUE"
	targetClause := "TRUE"
	args := []interface{}{callerIDs}
	if scope.EnforceSnapshots {
		args = append(args, snapshotIDs)
		sourceClause = integrationSnapshotClause("h.snapshot_id", len(args), workspaceIncludesLegacy(scope))
		targetClause = integrationSnapshotClause("f.snapshot_id", len(args), workspaceIncludesLegacy(scope))
	}

	query := fmt.Sprintf(`
		SELECT
			h.caller_id,
			COALESCE(NULLIF(h.http_method, ''), COALESCE(NULLIF(e.method_canonical, ''), e.method)) AS method,
			COALESCE(NULLIF(e.path_canonical, ''), e.path) AS path,
			COALESCE(h.client_type, '') AS client_type,
			r.name AS target_repo,
			f.path AS target_file,
			COALESCE(fn.name, '') AS target_handler,
			COALESCE(h.line_number, 0) AS line_number,
			f.snapshot_id AS target_snapshot_id
		FROM http_client_calls h
		JOIN endpoints e
		  ON (
		    ((UPPER(h.http_method) = UPPER(e.method) OR UPPER(h.http_method) IN ('REQUEST', 'ANY')) AND h.url_pattern = e.path)
		    OR (h.url_pattern = e.path_canonical AND (UPPER(h.http_method) = UPPER(e.method_canonical) OR UPPER(h.http_method) IN ('REQUEST', 'ANY')))
		  )
		JOIN files f ON e.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN functions fn ON e.handler_function_id = fn.id
		WHERE h.caller_id = ANY($1)
		  AND %s
		  AND %s
		ORDER BY h.caller_id, path, method, target_repo, target_handler, line_number
	`, sourceClause, targetClause)
	rows, err := h.storage.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item FunctionIntegration
		if err := rows.Scan(
			&item.CallerID,
			&item.Method,
			&item.Path,
			&item.ClientType,
			&item.TargetRepo,
			&item.TargetFile,
			&item.TargetHandler,
			&item.LineNumber,
			&item.TargetSnapshotID,
		); err != nil {
			return nil, err
		}
		item.Resolution = "matched"
		item.Type = "http"
		result[item.CallerID] = append(result[item.CallerID], item)
	}
	return result, rows.Err()
}

func (h *Handlers) loadSqsFunctionIntegrations(ctx context.Context, callerID string) ([]FunctionIntegration, error) {
	return h.loadSqsFunctionIntegrationsForScope(ctx, callerID, searchWorkspaceScope{})
}

func buildSqsFunctionIntegrationsQuery(callerID string, scope searchWorkspaceScope) (string, []interface{}) {
	snapshotIDs := activeSnapshotIDs(scope)
	sourceClause := "TRUE"
	aliasClause := "TRUE"
	sqsTargetClause := "TRUE"
	azureTargetClause := "TRUE"
	dataAccessTargetClause := "TRUE"
	args := []interface{}{callerID}
	if scope.EnforceSnapshots {
		args = append(args, snapshotIDs)
		includeLegacy := workspaceIncludesLegacy(scope)
		sourceClause = integrationSnapshotClause("p.snapshot_id", len(args), includeLegacy)
		aliasClause = integrationSnapshotClause("ra.snapshot_id", len(args), includeLegacy)
		sqsTargetClause = integrationSnapshotClause("snapshot_id", len(args), includeLegacy)
		azureTargetClause = integrationSnapshotClause("f.snapshot_id", len(args), includeLegacy)
		dataAccessTargetClause = integrationSnapshotClause("da.snapshot_id", len(args), includeLegacy)
	}
	query := fmt.Sprintf(`
		WITH queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases ra
			WHERE %s
			  AND COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
		),
		queue_consumers AS (
			SELECT
				consumer_id,
				queue_name,
				COALESCE(handler_method, '') AS handler_method,
				snapshot_id
			FROM sqs_consumers
			WHERE %s
			  AND COALESCE(queue_name, '') <> ''

			UNION ALL

			SELECT
				r.name || ':' || f.path || ':' || t.function_name AS consumer_id,
				COALESCE(t.resource_name, '') AS queue_name,
				'' AS handler_method,
				f.snapshot_id AS snapshot_id
			FROM azure_function_triggers t
			JOIN files f ON f.id = t.file_id
			JOIN repositories r ON r.id = t.repo_id
			WHERE %s
			  AND t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			  AND COALESCE(t.resource_name, '') <> ''

			UNION ALL

			SELECT
				da.caller_id AS consumer_id,
				REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i') AS queue_name,
				'' AS handler_method,
				da.snapshot_id AS snapshot_id
			FROM data_accesses da
			WHERE %s
			  AND LOWER(da.access) = 'read'
			  AND LOWER(da.entity_name) LIKE 'queue:%%'
		)
		SELECT
			p.caller_id,
			p.queue_name,
			COALESCE(p.line_number, 0) AS line_number,
			COALESCE(c.consumer_id, '') AS consumer_id,
			COALESCE(c.handler_method, '') AS handler_method,
			COALESCE(c.snapshot_id, p.snapshot_id) AS target_snapshot_id
		FROM sqs_producers p
		LEFT JOIN queue_consumers c
		  ON (
		    LOWER(TRIM(BOTH '%%' FROM COALESCE(c.queue_name, ''))) =
		      LOWER(TRIM(BOTH '%%' FROM COALESCE(p.queue_name, '')))
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases qa
		      WHERE (
		        qa.alias_key = LOWER(TRIM(BOTH '%%' FROM COALESCE(p.queue_name, '')))
		        AND qa.alias_value = LOWER(TRIM(BOTH '%%' FROM COALESCE(c.queue_name, '')))
		      ) OR (
		        qa.alias_value = LOWER(TRIM(BOTH '%%' FROM COALESCE(p.queue_name, '')))
		        AND qa.alias_key = LOWER(TRIM(BOTH '%%' FROM COALESCE(c.queue_name, '')))
		      )
		    )
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases producer_alias
		      JOIN queue_aliases consumer_alias
		        ON producer_alias.alias_value = consumer_alias.alias_value
		      WHERE (
		        producer_alias.alias_key = LOWER(TRIM(BOTH '%%' FROM COALESCE(p.queue_name, '')))
		        OR producer_alias.alias_value = LOWER(TRIM(BOTH '%%' FROM COALESCE(p.queue_name, '')))
		      )
		        AND (
		          consumer_alias.alias_key = LOWER(TRIM(BOTH '%%' FROM COALESCE(c.queue_name, '')))
		          OR consumer_alias.alias_value = LOWER(TRIM(BOTH '%%' FROM COALESCE(c.queue_name, '')))
		        )
		    )
		  )
		WHERE p.caller_id = $1
		  AND %s
		ORDER BY p.queue_name, c.consumer_id, line_number
	`, aliasClause, sqsTargetClause, azureTargetClause, dataAccessTargetClause, sourceClause)

	return query, args
}

func (h *Handlers) loadSqsFunctionIntegrationsForScope(ctx context.Context, callerID string, scope searchWorkspaceScope) ([]FunctionIntegration, error) {
	query, args := buildSqsFunctionIntegrationsQuery(callerID, scope)
	rows, err := h.storage.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]FunctionIntegration, 0, 4)
	for rows.Next() {
		var item FunctionIntegration
		var consumerID, handlerMethod string
		if err := rows.Scan(&item.CallerID, &item.Path, &item.LineNumber, &consumerID, &handlerMethod, &item.TargetSnapshotID); err != nil {
			return nil, err
		}
		item.Type = "sqs"
		item.Method = "SQS"
		item.ClientType = "sqs"
		item.Resolution = "unresolved"
		if consumerID != "" {
			item.Resolution = "matched"
			repo, file, className := parseFunctionIntegrationConsumerID(consumerID)
			item.TargetRepo = repo
			item.TargetFile = file
			if className != "" {
				item.TargetHandler = className
				if handlerMethod != "" {
					item.TargetHandler += "." + handlerMethod
				}
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func parseFunctionIntegrationConsumerID(consumerID string) (repo, file, className string) {
	parts := strings.SplitN(consumerID, ":", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func (h *Handlers) loadFunctionIntegrations(ctx context.Context, callerID string, includeInferred bool) ([]FunctionIntegration, error) {
	integrations, err := h.loadMatchedFunctionIntegrations(ctx, callerID)
	if err != nil {
		return nil, err
	}
	hasMatchedHTTP := len(integrations) > 0
	sqsIntegrations, err := h.loadSqsFunctionIntegrations(ctx, callerID)
	if err != nil {
		return nil, err
	}
	integrations = append(integrations, sqsIntegrations...)
	if includeInferred && !hasMatchedHTTP {
		inferred, err := h.inferFunctionIntegrations(ctx, callerID, searchWorkspaceScope{})
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, inferred...)
	}

	sort.Slice(integrations, func(i, j int) bool {
		if integrations[i].Type != integrations[j].Type {
			return integrations[i].Type < integrations[j].Type
		}
		if integrations[i].TargetRepo == integrations[j].TargetRepo {
			if integrations[i].Path == integrations[j].Path {
				if integrations[i].Method == integrations[j].Method {
					if integrations[i].TargetHandler == integrations[j].TargetHandler {
						return integrations[i].LineNumber < integrations[j].LineNumber
					}
					return integrations[i].TargetHandler < integrations[j].TargetHandler
				}
				return integrations[i].Method < integrations[j].Method
			}
			return integrations[i].Path < integrations[j].Path
		}
		return integrations[i].TargetRepo < integrations[j].TargetRepo
	})
	if integrations == nil {
		integrations = []FunctionIntegration{}
	}
	return integrations, nil
}

func (h *Handlers) loadFunctionIntegrationsScoped(ctx context.Context, callerID string, includeInferred bool, scope searchWorkspaceScope) ([]FunctionIntegration, error) {
	if scope.EnforceSnapshots {
		var allowed bool
		err := h.storage.Pool().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM functions fn
				JOIN files f ON f.id = fn.file_id
				JOIN repositories r ON r.id = f.repo_id
				WHERE (r.name || ':' || f.path || ':' || fn.name) = $1
				  AND (f.snapshot_id = ANY($2::bigint[]) OR ($3 AND f.snapshot_id IS NULL))
			)`, callerID, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope)).Scan(&allowed)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return []FunctionIntegration{}, nil
		}
	}
	if !scope.EnforceSnapshots {
		return h.loadFunctionIntegrations(ctx, callerID, includeInferred)
	}
	matched, err := h.loadMatchedFunctionIntegrationsBatchForScope(ctx, []string{callerID}, scope)
	if err != nil {
		return nil, err
	}
	integrations := matched[callerID]
	hasMatchedHTTP := len(integrations) > 0
	sqsIntegrations, err := h.loadSqsFunctionIntegrationsForScope(ctx, callerID, scope)
	if err != nil {
		return nil, err
	}
	integrations = append(integrations, sqsIntegrations...)
	if includeInferred && !hasMatchedHTTP {
		inferred, err := h.inferFunctionIntegrations(ctx, callerID, scope)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, filterFunctionIntegrationsByWorkspace(inferred, scope)...)
	}
	sort.Slice(integrations, func(i, j int) bool {
		if integrations[i].Type != integrations[j].Type {
			return integrations[i].Type < integrations[j].Type
		}
		if integrations[i].TargetRepo == integrations[j].TargetRepo {
			if integrations[i].Path == integrations[j].Path {
				if integrations[i].Method == integrations[j].Method {
					if integrations[i].TargetHandler == integrations[j].TargetHandler {
						return integrations[i].LineNumber < integrations[j].LineNumber
					}
					return integrations[i].TargetHandler < integrations[j].TargetHandler
				}
				return integrations[i].Method < integrations[j].Method
			}
			return integrations[i].Path < integrations[j].Path
		}
		return integrations[i].TargetRepo < integrations[j].TargetRepo
	})
	if integrations == nil {
		integrations = []FunctionIntegration{}
	}
	return integrations, nil
}

func filterFunctionIntegrationsByWorkspace(integrations []FunctionIntegration, scope searchWorkspaceScope) []FunctionIntegration {
	if !scope.EnforceSnapshots {
		return integrations
	}
	out := make([]FunctionIntegration, 0, len(integrations))
	for _, item := range integrations {
		if workspaceAllowsSnapshot(item.TargetSnapshotID, scope) {
			out = append(out, item)
		}
	}
	return out
}

func integrationSnapshotClause(column string, argNum int, includeLegacy bool) string {
	if includeLegacy {
		return fmt.Sprintf("($%d::bigint[] IS NULL OR %s = ANY($%d) OR %s IS NULL)", argNum, column, argNum, column)
	}
	return fmt.Sprintf("($%d::bigint[] IS NULL OR %s = ANY($%d))", argNum, column, argNum)
}

type functionContext struct {
	CallerID   string
	SourceCode string
	FilePath   string
	RepoPath   string
	StartLine  int
	EndLine    int
	FileHash   string
	Commit     string
	SnapshotID int64
	Scope      searchWorkspaceScope
}

var (
	endpointConstRefPattern     = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\.([A-Z0-9_]+)\b`)
	javaEndpointConstPattern    = regexp.MustCompile(`public\s+static\s+final\s+String\s+([A-Z0-9_]+)\s*=\s*"([^"]+)"`)
	javaLocalStringConstPattern = regexp.MustCompile(`(?:private|protected|public)?\s*(?:static\s+)?(?:final\s+)?String\s+([A-Za-z0-9_]+)\s*=\s*"([^"]+)"`)
	javaUpperConstRefPattern    = regexp.MustCompile(`\b([A-Z][A-Z0-9_]+)\b`)
	integrationPathParamPattern = regexp.MustCompile(`/\{[^/]+\}`)
	integrationParamSegment     = regexp.MustCompile(`^\{[^/]+\}$`)
	integrationColonSegment     = regexp.MustCompile(`^:[^/]+$`)
)

func (h *Handlers) inferFunctionIntegrations(ctx context.Context, callerID string, scope searchWorkspaceScope) ([]FunctionIntegration, error) {
	fnCtx, err := h.loadFunctionContext(ctx, callerID, scope)
	if err != nil || strings.TrimSpace(fnCtx.SourceCode) == "" {
		return nil, err
	}

	integrations := make([]FunctionIntegration, 0, 8)
	seen := make(map[string]struct{})
	for _, call := range h.inferHTTPWrapperCallCandidates(ctx, fnCtx) {
		items, err := h.lookupEndpointIntegrations(ctx, callerID, call.Method, call.Path, scope)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			key := strings.Join([]string{item.Method, item.Path, item.TargetRepo, item.TargetHandler}, "|")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			item.Resolution = "inferred"
			integrations = append(integrations, item)
		}
	}

	methods := inferHTTPMethodsFromSource(fnCtx.SourceCode)
	if len(methods) == 0 {
		methods = []string{"REQUEST"}
	}
	paths := inferInternalPathsFromSource(fnCtx.SourceCode)
	paths = append(paths, h.resolveEndpointConstantPaths(ctx, fnCtx)...)
	paths = uniqueIntegrationStrings(paths)
	for _, path := range paths {
		for _, method := range methods {
			items, err := h.lookupEndpointIntegrations(ctx, callerID, method, path, scope)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				key := strings.Join([]string{item.Method, item.Path, item.TargetRepo, item.TargetHandler}, "|")
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				item.Resolution = "inferred"
				integrations = append(integrations, item)
			}
		}
	}

	return integrations, nil
}

type inferredHTTPWrapperCall struct {
	Method string
	Path   string
}

func (h *Handlers) loadFunctionContext(ctx context.Context, callerID string, scope searchWorkspaceScope) (functionContext, error) {
	batch, err := h.loadFunctionContextBatch(ctx, []string{callerID}, scope)
	if err != nil {
		return functionContext{}, err
	}
	return batch[callerID], nil
}

func (h *Handlers) loadFunctionContextBatch(ctx context.Context, callerIDs []string, scopes ...searchWorkspaceScope) (map[string]functionContext, error) {
	results := make(map[string]functionContext, len(callerIDs))
	ambiguous := make(map[string]bool)
	callerIDs = uniqueIntegrationStrings(callerIDs)
	if len(callerIDs) == 0 {
		return results, nil
	}
	query := `
		SELECT
			r.name || ':' || f.path || ':' || fn.name AS caller_id,
			COALESCE(fn.source_code, '') AS source_code,
			f.path,
			COALESCE(NULLIF(rs.source_path,''),r.path),
			fn.start_line,
			fn.end_line,
			COALESCE(f.hash, ''), COALESCE(rs.sha, ''), COALESCE(f.snapshot_id, 0)
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN repo_snapshots rs ON rs.id = f.snapshot_id
		WHERE (r.name || ':' || f.path || ':' || fn.name) = ANY($1)`
	args := []interface{}{callerIDs}
	argNum := 2
	if len(scopes) > 0 && scopes[0].EnforceSnapshots {
		snapshotIDs := activeSnapshotIDs(scopes[0])
		includeLegacy := workspaceIncludesLegacy(scopes[0])
		switch {
		case len(snapshotIDs) > 0 && includeLegacy:
			query += fmt.Sprintf(" AND (f.snapshot_id = ANY($%d) OR f.snapshot_id IS NULL)", argNum)
			args = append(args, snapshotIDs)
			argNum++
		case len(snapshotIDs) > 0:
			query += fmt.Sprintf(" AND f.snapshot_id = ANY($%d)", argNum)
			args = append(args, snapshotIDs)
			argNum++
		case includeLegacy:
			query += " AND f.snapshot_id IS NULL"
		default:
			query += " AND FALSE"
		}
	}
	rows, err := h.storage.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item functionContext
		if err := rows.Scan(&item.CallerID, &item.SourceCode, &item.FilePath, &item.RepoPath, &item.StartLine, &item.EndLine, &item.FileHash, &item.Commit, &item.SnapshotID); err != nil {
			return nil, err
		}
		if len(scopes) > 0 {
			item.Scope = scopes[0]
		}
		if ambiguous[item.CallerID] {
			continue
		}
		if previous, exists := results[item.CallerID]; exists && (previous.SourceCode != item.SourceCode || previous.FileHash != item.FileHash || previous.StartLine != item.StartLine || previous.EndLine != item.EndLine || previous.SnapshotID != item.SnapshotID) {
			delete(results, item.CallerID)
			ambiguous[item.CallerID] = true
			continue
		}
		results[item.CallerID] = item
	}
	return results, rows.Err()
}

func inferHTTPMethodsFromSource(source string) []string {
	methods := make([]string, 0, 4)
	for _, call := range extractHTTPWrapperCallExpressions(source) {
		methods = append(methods, call.Method)
	}
	for _, call := range extractGenericAPIClientCallExpressions(source) {
		methods = append(methods, call.Method)
	}
	return uniqueIntegrationStrings(methods)
}

func inferInternalPathsFromSource(source string) []string {
	pathPattern := regexp.MustCompile("[\"'`]([A-Za-z0-9_\\-/]+/[A-Za-z0-9_\\-/{}/]+)[\"'`]")
	matches := pathPattern.FindAllStringSubmatch(source, -1)
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path := strings.TrimSpace(match[1])
		if path == "" || strings.Contains(path, "http://") || strings.Contains(path, "https://") {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		paths = append(paths, path)
	}
	return uniqueIntegrationStrings(paths)
}

func resolveLocalConstantPaths(ctx context.Context, fnCtx functionContext) []string {
	localConstMap := loadLocalStringConstantMap(ctx, fnCtx)
	if len(localConstMap) == 0 {
		return nil
	}

	paths := make([]string, 0, 8)
	for _, match := range javaUpperConstRefPattern.FindAllStringSubmatch(fnCtx.SourceCode, -1) {
		if len(match) < 2 {
			continue
		}
		path, ok := localConstMap[match[1]]
		if !ok || path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		paths = append(paths, path)
	}
	return uniqueIntegrationStrings(paths)
}

func loadLocalStringConstantMap(ctx context.Context, fnCtx functionContext) map[string]string {
	if fnCtx.RepoPath == "" || fnCtx.FilePath == "" || strings.TrimSpace(fnCtx.SourceCode) == "" {
		return nil
	}

	data, err := readIntegrationSource(ctx, fnCtx.RepoPath, fnCtx.FilePath, fnCtx.FileHash, fnCtx.Commit)
	if err != nil {
		return nil
	}

	fileSource := string(data)
	constants := make(map[string]string, 8)
	for _, match := range javaLocalStringConstPattern.FindAllStringSubmatch(fileSource, -1) {
		if len(match) < 3 {
			continue
		}
		constName := match[1]
		path := strings.TrimSpace(match[2])
		if constName == "" || path == "" {
			continue
		}
		constants[constName] = path
	}
	return constants
}

func (h *Handlers) resolveEndpointConstantPaths(ctx context.Context, fnCtx functionContext) []string {
	localPaths := resolveLocalConstantPaths(ctx, fnCtx)
	matches := endpointConstRefPattern.FindAllStringSubmatch(fnCtx.SourceCode, -1)
	if len(matches) == 0 {
		return localPaths
	}

	paths := make([]string, 0, len(localPaths)+len(matches))
	paths = append(paths, localPaths...)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		className := match[1]
		constName := match[2]
		if path := h.resolveJavaConstantPath(ctx, fnCtx, className, constName); path != "" {
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			paths = append(paths, path)
		}
	}
	return uniqueIntegrationStrings(paths)
}

func (h *Handlers) resolveJavaConstantPath(ctx context.Context, fnCtx functionContext, className, constName string) string {
	if h == nil || h.storage == nil {
		return ""
	}
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT COALESCE(NULLIF(rs.source_path,''),r.path), f.path, COALESCE(f.hash, ''), COALESCE(rs.sha, '')
		FROM files f
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN repo_snapshots rs ON rs.id = f.snapshot_id
		WHERE (f.path = $1 OR right(f.path, length($2)) = $2)
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3) OR ($4 AND f.snapshot_id IS NULL))
		ORDER BY r.name, f.path
	`, className+".java", "/"+className+".java", activeSnapshotIDs(fnCtx.Scope), workspaceIncludesLegacy(fnCtx.Scope))
	if err != nil {
		return ""
	}
	type candidateSource struct{ repo, file, hash, commit string }
	var candidates []candidateSource
	for rows.Next() {
		var candidate candidateSource
		if err := rows.Scan(&candidate.repo, &candidate.file, &candidate.hash, &candidate.commit); err != nil {
			rows.Close()
			return ""
		}
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ""
	}
	var value string
	found := false
	for _, candidate := range candidates {
		data, err := readIntegrationSource(ctx, candidate.repo, candidate.file, candidate.hash, candidate.commit)
		if err != nil {
			return ""
		}
		for _, match := range javaEndpointConstPattern.FindAllStringSubmatch(string(data), -1) {
			if len(match) < 3 || match[1] != constName {
				continue
			}
			if found && value != match[2] {
				return ""
			}
			value, found = match[2], true
		}
	}
	return value
}

func (h *Handlers) lookupEndpointIntegrations(ctx context.Context, callerID, method, path string, scope searchWorkspaceScope) ([]FunctionIntegration, error) {
	pathVariants := normalizedIntegrationPathVariants(path)
	if len(pathVariants) == 0 {
		return nil, nil
	}
	searchTerms := integrationLookupSearchTerms(path)

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT
			$1 AS caller_id,
			$2 AS method,
			COALESCE(NULLIF(e.path_canonical, ''), e.path) AS path,
			'inferred' AS client_type,
			r.name AS target_repo,
			f.path AS target_file,
			COALESCE(fn.name, '') AS target_handler,
			0 AS line_number,
			f.snapshot_id AS target_snapshot_id
		FROM endpoints e
		JOIN files f ON e.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN functions fn ON e.handler_function_id = fn.id
		WHERE
			(UPPER($2) = UPPER(COALESCE(NULLIF(e.method_canonical, ''), e.method)) OR UPPER($2) IN ('REQUEST', 'ANY'))
			AND ($4::bigint[] IS NULL OR f.snapshot_id = ANY($4) OR ($5 AND f.snapshot_id IS NULL))
			AND (
				cardinality($3::text[]) = 0
				OR EXISTS (
					SELECT 1
				FROM unnest($3::text[]) AS term(value)
				WHERE replace(replace(LOWER(COALESCE(NULLIF(e.path_canonical, ''), e.path)), '-', ''), '_', '') LIKE '%' || replace(replace(replace(term.value, '\', '\\'), '%', '\%'), '_', '\_') || '%'
			)
		)
	ORDER BY r.name, target_handler
	`, callerID, method, searchTerms, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]FunctionIntegration, 0, 4)
	fuzzyCandidates := make([]scoredFunctionIntegration, 0, 4)
	for rows.Next() {
		var item FunctionIntegration
		if err := rows.Scan(
			&item.CallerID,
			&item.Method,
			&item.Path,
			&item.ClientType,
			&item.TargetRepo,
			&item.TargetFile,
			&item.TargetHandler,
			&item.LineNumber,
			&item.TargetSnapshotID,
		); err != nil {
			return nil, err
		}
		if integrationPathMatchesVariants(item.Path, pathVariants) || integrationPathEquivalent(item.Path, path) {
			items = append(items, item)
			continue
		}
		if score := integrationPathFuzzySegmentScore(item.Path, path); score > 0 {
			fuzzyCandidates = append(fuzzyCandidates, scoredFunctionIntegration{item: item, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 0 {
		return items, nil
	}
	items = selectUnambiguousFuzzyIntegrations(fuzzyCandidates)
	return items, nil
}

type scoredFunctionIntegration struct {
	item  FunctionIntegration
	score int
}

func normalizeIntegrationPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = integrationPathParamPattern.ReplaceAllString(path, "")
	if path != "/" {
		path = strings.TrimRight(path, "/")
	}
	return strings.ToLower(path)
}

func normalizedIntegrationPathVariants(path string) []string {
	normalized := normalizeIntegrationPath(path)
	if normalized == "" {
		return nil
	}

	variants := []string{normalized}
	if strings.HasPrefix(normalized, "/api/") {
		variants = append(variants, strings.TrimPrefix(normalized, "/api"))
	} else if normalized != "/api" {
		variants = append(variants, "/api"+normalized)
	}
	return uniqueIntegrationStrings(variants)
}

func integrationPathMatchesVariants(path string, variants []string) bool {
	normalized := normalizeIntegrationPath(path)
	for _, variant := range variants {
		if strings.EqualFold(normalized, variant) {
			return true
		}
	}
	return false
}

func integrationLookupSearchTerms(path string) []string {
	segments := normalizedIntegrationSegments(path)
	if len(segments) == 0 {
		return nil
	}

	terms := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "api" || segment == "v1" || segment == "v2" || segment == "v3" {
			continue
		}
		if isIntegrationWildcardSegment(segment) {
			continue
		}
		if allDigits(segment) {
			continue
		}
		terms = append(terms, segment)
		// Also include a hyphen/underscore-stripped form so gateway-rewritten
		// URLs (e.g. /export-audit vs /ExportAudit) can match via LIKE.
		stripped := strings.ReplaceAll(segment, "-", "")
		stripped = strings.ReplaceAll(stripped, "_", "")
		if stripped != segment && len(stripped) > 4 {
			terms = append(terms, stripped)
		}
	}
	if len(terms) > 6 {
		terms = terms[len(terms)-6:]
	}
	return uniqueIntegrationStrings(terms)
}

func normalizedIntegrationSegments(path string) []string {
	path = strings.TrimSpace(strings.ToLower(path))
	if path == "" {
		return nil
	}
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	if len(segments) > 0 && segments[0] == "api" {
		segments = segments[1:]
	}
	return segments
}

func isIntegrationWildcardSegment(segment string) bool {
	return integrationParamSegment.MatchString(segment) || integrationColonSegment.MatchString(segment)
}

func integrationPathEquivalent(left, right string) bool {
	leftSegs := normalizedIntegrationSegments(left)
	rightSegs := normalizedIntegrationSegments(right)
	if len(leftSegs) != len(rightSegs) {
		return false
	}
	for i := range leftSegs {
		if leftSegs[i] == rightSegs[i] {
			continue
		}
		if isIntegrationWildcardSegment(leftSegs[i]) || isIntegrationWildcardSegment(rightSegs[i]) {
			continue
		}
		return false
	}
	return len(leftSegs) > 0
}

// integrationPathFuzzySegmentMatch checks whether left and right share a
// meaningful path segment after normalizing hyphens, underscores, and case.
// This handles API gateway rewrites where the path structure changes completely
// (e.g., /gateway/:tenant/task-status/:id vs /api/TaskStatus/{id}).
func integrationPathFuzzySegmentMatch(left, right string) bool {
	return integrationPathFuzzySegmentScore(left, right) > 0
}

func integrationPathFuzzySegmentScore(left, right string) int {
	leftSegs := integrationMeaningfulSegments(left)
	rightSegs := integrationMeaningfulSegments(right)
	if len(leftSegs) == 0 || len(rightSegs) == 0 {
		return 0
	}
	rightSet := make(map[string]bool, len(rightSegs))
	for _, s := range rightSegs {
		rightSet[s] = true
	}
	score := 0
	for _, s := range leftSegs {
		if rightSet[s] {
			score++
		}
	}
	return score
}

func selectUnambiguousFuzzyIntegrations(candidates []scoredFunctionIntegration) []FunctionIntegration {
	if len(candidates) == 0 {
		return nil
	}
	maxScore := 0
	for _, candidate := range candidates {
		if candidate.score > maxScore {
			maxScore = candidate.score
		}
	}
	if maxScore == 0 {
		return nil
	}

	bestByKey := make(map[string]FunctionIntegration)
	for _, candidate := range candidates {
		if candidate.score != maxScore {
			continue
		}
		item := candidate.item
		key := strings.Join([]string{
			strings.ToUpper(item.Method),
			normalizeIntegrationPath(item.Path),
			item.TargetRepo,
			item.TargetHandler,
		}, "|")
		if _, ok := bestByKey[key]; !ok {
			bestByKey[key] = item
		}
	}
	if len(bestByKey) == 0 {
		return nil
	}

	best := make([]FunctionIntegration, 0, len(bestByKey))
	var firstPath string
	allSamePath := true
	for _, item := range bestByKey {
		path := normalizeIntegrationPath(item.Path)
		if firstPath == "" {
			firstPath = path
		} else if path != firstPath {
			allSamePath = false
		}
		best = append(best, item)
	}
	if len(best) == 1 || allSamePath {
		sort.Slice(best, func(i, j int) bool {
			if best[i].TargetRepo == best[j].TargetRepo {
				if best[i].TargetHandler == best[j].TargetHandler {
					return best[i].Method < best[j].Method
				}
				return best[i].TargetHandler < best[j].TargetHandler
			}
			return best[i].TargetRepo < best[j].TargetRepo
		})
		return best
	}
	return nil
}

// integrationMeaningfulSegments extracts normalized static segments suitable for
// fuzzy matching. Strips hyphens/underscores, skips params, short (<=4) and common segments.
func integrationMeaningfulSegments(path string) []string {
	segs := normalizedIntegrationSegments(path) // already lowercased, stripped /api prefix
	result := make([]string, 0, len(segs))
	seen := make(map[string]bool)
	for _, seg := range segs {
		if isIntegrationWildcardSegment(seg) {
			continue
		}
		norm := strings.ReplaceAll(seg, "-", "")
		norm = strings.ReplaceAll(norm, "_", "")
		if len(norm) <= 4 {
			continue
		}
		if integrationCommonSegments[norm] {
			continue
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		result = append(result, norm)
	}
	return result
}

// integrationCommonSegments mirrors commonEndpointSegments in internal/trace/trace.go.
// Keep both in sync when adding entries.
var integrationCommonSegments = map[string]bool{
	"action": true, "admin": true, "api": true, "controller": true,
	"create": true, "data": true, "delete": true, "endpoint": true,
	"external": true, "health": true, "index": true, "internal": true,
	"items": true, "list": true, "private": true, "proxy": true,
	"public": true, "query": true, "report": true, "reports": true,
	"resource": true, "resources": true,
	"service": true, "services": true, "status": true, "swagger": true,
	"update": true, "user": true, "users": true, "value": true, "handler": true,
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func uniqueIntegrationStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func (h *Handlers) inferHTTPWrapperCallCandidates(ctx context.Context, fnCtx functionContext) []inferredHTTPWrapperCall {
	methodCalls := extractHTTPWrapperCallExpressions(fnCtx.SourceCode)
	if len(methodCalls) == 0 {
		return nil
	}

	localConstMap := loadLocalStringConstantMap(ctx, fnCtx)
	seen := make(map[string]struct{}, len(methodCalls))
	candidates := make([]inferredHTTPWrapperCall, 0, len(methodCalls))
	for _, call := range methodCalls {
		for _, path := range h.resolveHTTPWrapperCallPathExpressions(ctx, fnCtx, localConstMap, call.Args) {
			if path == "" {
				continue
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			key := call.Method + "|" + path
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, inferredHTTPWrapperCall{
				Method: call.Method,
				Path:   path,
			})
		}
	}
	return candidates
}

type httpWrapperCallExpression struct {
	Method string
	Args   []string
}

func extractHTTPWrapperCallExpressions(source string) []httpWrapperCallExpression {
	var calls []httpWrapperCallExpression
	patterns := config.GetEffectivePatterns().GetJavaPatterns()
	for _, invocation := range parser.JavaInvocations(source) {
		if method := configuredInvocationHTTPMethod(invocation, patterns); method != "" {
			calls = append(calls, httpWrapperCallExpression{Method: method, Args: invocation.Args})
		}
	}
	return calls
}

func configuredInvocationHTTPMethod(call parser.JavaInvocation, patterns []config.HttpClientPattern) string {
	invocation := call.Receiver + "." + call.Method + "("
	resolved := ""
	for _, pattern := range patterns {
		matched := false
		for _, receiver := range pattern.Contains {
			if receiver != "" && strings.Contains(invocation, receiver) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for expression, verb := range pattern.Methods {
			name := strings.TrimPrefix(strings.TrimSpace(expression), ".")
			if open := strings.IndexByte(name, '('); open >= 0 {
				if strings.TrimSpace(name[open:]) == "()" && len(call.Args) != 0 {
					continue
				}
				name = strings.TrimSpace(name[:open])
			}
			if name != call.Method || strings.TrimSpace(verb) == "" {
				continue
			}
			verb = strings.ToUpper(strings.TrimSpace(verb))
			if resolved != "" && resolved != verb {
				return ""
			}
			resolved = verb
		}
	}
	return resolved
}

func findMatchingParen(source string, openPos int) int {
	depth := 0
	inString := false
	var quote byte
	escaped := false
	for i := openPos; i < len(source); i++ {
		ch := source[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				inString = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = true
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTopLevelJavaArgs(source string) []string {
	args := make([]string, 0, 8)
	start := 0
	parenDepth := 0
	angleDepth := 0
	bracketDepth := 0
	braceDepth := 0
	inString := false
	var quote byte
	escaped := false

	for i := 0; i < len(source); i++ {
		ch := source[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				inString = false
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inString = true
			quote = ch
		case '(':
			parenDepth++
		case ')':
			parenDepth--
		case '<':
			if looksLikeJavaGenericBoundary(source, i) {
				angleDepth++
			}
		case '>':
			if angleDepth > 0 {
				angleDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			bracketDepth--
		case '{':
			braceDepth++
		case '}':
			braceDepth--
		case ',':
			if parenDepth == 0 && angleDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				args = append(args, strings.TrimSpace(source[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(source[start:]); tail != "" {
		args = append(args, tail)
	}
	return args
}

func looksLikeJavaGenericBoundary(source string, idx int) bool {
	prev := idx - 1
	for prev >= 0 && unicode.IsSpace(rune(source[prev])) {
		prev--
	}
	next := idx + 1
	for next < len(source) && unicode.IsSpace(rune(source[next])) {
		next++
	}
	if prev < 0 || next >= len(source) {
		return false
	}
	return (unicode.IsLetter(rune(source[prev])) || unicode.IsDigit(rune(source[prev])) || source[prev] == '?' || source[prev] == '>') &&
		(unicode.IsLetter(rune(source[next])) || source[next] == '?' || source[next] == '@')
}

func (h *Handlers) resolveHTTPWrapperCallPathExpressions(ctx context.Context, fnCtx functionContext, localConstMap map[string]string, args []string) []string {
	paths := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, arg := range args {
		for _, path := range resolvePathCandidatesFromExpression(ctx, h, fnCtx, localConstMap, arg) {
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	return paths
}

func resolvePathCandidatesFromExpression(ctx context.Context, h *Handlers, fnCtx functionContext, localConstMap map[string]string, expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}

	paths := make([]string, 0, 4)
	if strings.Contains(expr, "/") {
		for _, path := range inferInternalPathsFromSource(expr) {
			paths = append(paths, path)
		}
	}
	for _, match := range endpointConstRefPattern.FindAllStringSubmatch(expr, -1) {
		if len(match) < 3 {
			continue
		}
		if path := h.resolveJavaConstantPath(ctx, fnCtx, match[1], match[2]); path != "" {
			paths = append(paths, path)
		}
	}
	for _, match := range javaUpperConstRefPattern.FindAllStringSubmatch(expr, -1) {
		if len(match) < 2 {
			continue
		}
		if path, ok := localConstMap[match[1]]; ok {
			paths = append(paths, path)
		}
	}
	return uniqueIntegrationStrings(paths)
}
