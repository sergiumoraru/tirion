package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SearchResult represents a search hit with context
type SearchResult struct {
	ID         int64
	Type       string // "function", "class", "endpoint", "file", "data_entity", "external_symbol", "graphql_operation", "graphql_resolver", "azure_trigger"
	Name       string
	FilePath   string
	RepoName   string
	SnapshotID *int64
	StartLine  int
	EndLine    int
	SourceCode string
	Extra      map[string]interface{}
}

// SearchFunctionsAll finds all functions by name pattern.
func (s *Storage) SearchFunctionsAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.searchFunctionsWithLimit(pattern, 0, filters...)
}

func (s *Storage) searchFunctionsWithLimit(pattern string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%", limit}, filters)
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(`
		SELECT f.id, f.name, f.start_line, f.end_line, f.source_code, f.is_exported, f.is_async, fi.snapshot_id,
		       fi.path as file_path, r.name as repo_name
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE f.name ILIKE $1
		/* visibility */
		ORDER BY f.name, f.id
		LIMIT NULLIF($2, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var isExported, isAsync bool
		var sourceCode *string
		err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &sourceCode, &isExported, &isAsync, &r.SnapshotID, &r.FilePath, &r.RepoName)
		if err != nil {
			return nil, err
		}
		r.Type = "function"
		if sourceCode != nil {
			r.SourceCode = *sourceCode
		}
		r.Extra = map[string]interface{}{
			"is_exported": isExported,
			"is_async":    isAsync,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchFunctionsRanked finds functions by name pattern, ordered by trace richness.
// Richness factors in call edges, SQS producers, and HTTP client calls.
// Functions with cross-service edges (SQS/HTTP) get a significant boost.
func (s *Storage) SearchFunctionsRanked(pattern string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%", limit, EscapeLike(pattern) + ".%"}, filters)
	incoming := ""
	if filter, ok := optionalSnapshotFilter(filters); ok {
		argNum := len(args) + 1
		appendSnapshotFilter(&incoming, &args, &argNum, "snapshot_id", filter)
	}
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(strings.ReplaceAll(`
		WITH matched AS (
			SELECT f.id, f.name, f.start_line, f.end_line, f.source_code, f.is_exported, f.is_async,
			       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name,
			       r.name || ':' || fi.path || ':' || f.name AS caller_id
			FROM functions f
			JOIN files fi ON fi.id = f.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE f.name ILIKE $1
		/* visibility */
		),
		out_counts AS (
			SELECT caller_function_id AS fn_id, COUNT(*) AS cnt
			FROM trace_call_edges
			WHERE caller_function_id IN (SELECT id FROM matched)
			GROUP BY caller_function_id
		),
		in_counts AS (
			SELECT callee_function_id AS fn_id, COUNT(*) AS cnt
			FROM trace_call_edges
			WHERE callee_function_id IN (SELECT id FROM matched)
			/* incoming */
			GROUP BY callee_function_id
		),
		sqs_counts AS (
			SELECT m.id AS fn_id, COUNT(*) AS cnt
			FROM matched m
			JOIN sqs_producers sp ON sp.caller_id = m.caller_id
			  AND sp.snapshot_id IS NOT DISTINCT FROM m.snapshot_id
			GROUP BY m.id
		),
		http_counts AS (
			SELECT m.id AS fn_id, COUNT(*) AS cnt
			FROM matched m
			JOIN http_client_calls hc ON hc.caller_id = m.caller_id
			  AND hc.snapshot_id IS NOT DISTINCT FROM m.snapshot_id
			GROUP BY m.id
		)
		SELECT m.id, m.name, m.start_line, m.end_line, m.source_code, m.is_exported, m.is_async,
		       m.snapshot_id, m.file_path, m.repo_name,
		       COALESCE(o.cnt, 0) + COALESCE(i.cnt, 0)
		         + COALESCE(sq.cnt, 0) * 50
		         + COALESCE(ht.cnt, 0) * 50 AS richness
		FROM matched m
		LEFT JOIN out_counts o ON o.fn_id = m.id
		LEFT JOIN in_counts i ON i.fn_id = m.id
		LEFT JOIN sqs_counts sq ON sq.fn_id = m.id
		LEFT JOIN http_counts ht ON ht.fn_id = m.id
		ORDER BY
		         CASE WHEN m.name ILIKE $3 THEN 0 ELSE 1 END,
		         COALESCE(o.cnt, 0) + COALESCE(i.cnt, 0)
		           + COALESCE(sq.cnt, 0) * 50
		           + COALESCE(ht.cnt, 0) * 50 DESC,
		         m.name, m.id
		LIMIT NULLIF($2, 0)
	`, "/* visibility */", visibility), "/* incoming */", incoming), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var isExported, isAsync bool
		var sourceCode *string
		var richness int
		err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &sourceCode, &isExported, &isAsync, &r.SnapshotID, &r.FilePath, &r.RepoName, &richness)
		if err != nil {
			return nil, err
		}
		r.Type = "function"
		if sourceCode != nil {
			r.SourceCode = *sourceCode
		}
		r.Extra = map[string]interface{}{
			"is_exported": isExported,
			"is_async":    isAsync,
			"richness":    richness,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchQueueConsumers finds indexed queue consumers whose queue, caller ID, or
// handler method matches one of the supplied SQL LIKE patterns.
func (s *Storage) SearchQueueConsumers(ctx context.Context, patterns []string, limit int, includedRepos []string, filters ...SnapshotFilter) ([]SearchResult, error) {
	if s == nil || s.pool == nil || len(patterns) == 0 || limit < 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = s.queryContext()
	}
	args := []any{patterns}
	repoFilter := ""
	if len(includedRepos) > 0 {
		args = append(args, repoFilterPatterns(includedRepos))
		repoFilter = fmt.Sprintf(" AND repo_name ILIKE ANY($%d::text[])", len(args))
	}
	aliasFilter := ""
	if filter, ok := optionalSnapshotFilter(filters); ok {
		argNum := len(args) + 1
		appendSnapshotFilter(&repoFilter, &args, &argNum, "qc.snapshot_id", filter)
		appendSnapshotFilter(&aliasFilter, &args, &argNum, "snapshot_id", filter)
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		WITH queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases
			WHERE COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
			%s
		),
			queue_consumers AS (
				SELECT r.name AS repo_name,
				       sc.consumer_id,
				       sc.queue_name,
				       COALESCE(sc.handler_method, '') AS handler_method,
				       ''::text AS trigger_type,
				       ''::text AS script_file,
				       0::bigint AS trigger_id,
				       sc.snapshot_id,
				       0::int AS line_number
				FROM sqs_consumers sc
				JOIN repositories r ON r.id = sc.repo_id

				UNION ALL

				SELECT
					r.name AS repo_name,
					r.name || ':' || f.path || ':' || t.function_name AS consumer_id,
					COALESCE(t.resource_name, '') AS queue_name,
					'' AS handler_method,
					t.trigger_type,
					COALESCE(t.script_file, '') AS script_file,
					t.id AS trigger_id,
					f.snapshot_id,
					COALESCE(t.line_number, 0) AS line_number
				FROM azure_function_triggers t
				JOIN files f ON f.id = t.file_id
				JOIN repositories r ON r.id = t.repo_id
				WHERE t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
				  AND COALESCE(t.resource_name, '') <> ''
			)
			SELECT repo_name, consumer_id, queue_name, handler_method,
			       trigger_type, script_file, trigger_id, snapshot_id, line_number
			FROM queue_consumers qc
			WHERE (
			LOWER(qc.queue_name) LIKE ANY($1)
			OR LOWER(TRIM(BOTH '%%' FROM qc.queue_name)) LIKE ANY($1)
			OR LOWER(qc.consumer_id) LIKE ANY($1)
			OR LOWER(COALESCE(qc.handler_method, '')) LIKE ANY($1)
			OR EXISTS (
				SELECT 1
				FROM queue_aliases qa
				WHERE (
					qa.alias_key = LOWER(TRIM(BOTH '%%' FROM qc.queue_name))
					AND qa.alias_value LIKE ANY($1)
				) OR (
					qa.alias_value = LOWER(TRIM(BOTH '%%' FROM qc.queue_name))
					AND qa.alias_key LIKE ANY($1)
				)
			)
			OR EXISTS (
				SELECT 1
				FROM queue_aliases consumer_alias
				JOIN queue_aliases query_alias
				  ON consumer_alias.alias_value = query_alias.alias_value
				WHERE consumer_alias.alias_key = LOWER(TRIM(BOTH '%%' FROM qc.queue_name))
				  AND (
					query_alias.alias_key LIKE ANY($1)
					OR query_alias.alias_value LIKE ANY($1)
				  )
			)
		)
		%s
		ORDER BY repo_name, consumer_id, queue_name
		LIMIT NULLIF($%d, 0)
	`, aliasFilter, repoFilter, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type consumerRow struct {
		repoName, consumerID, queueName, handlerMethod, triggerType, scriptFile string
		triggerID                                                               int64
		snapshotID                                                              sql.NullInt64
		line                                                                    int
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (consumerRow, error) {
		var c consumerRow
		err := row.Scan(&c.repoName, &c.consumerID, &c.queueName, &c.handlerMethod, &c.triggerType, &c.scriptFile, &c.triggerID, &c.snapshotID, &c.line)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	// Release the listing connection before looking up source declarations.
	results := make([]SearchResult, 0, len(candidates))
	for _, c := range candidates {
		repoName, consumerID, queueName, handlerMethod := c.repoName, c.consumerID, c.queueName, c.handlerMethod
		triggerType, scriptFile, triggerID, triggerSnapshotID, triggerLine := c.triggerType, c.scriptFile, c.triggerID, c.snapshotID, c.line
		repo, filePath, className := parseGraphCallerID(consumerID)
		if repo == "" {
			repo = repoName
		}
		if filePath == "" || className == "" {
			continue
		}
		handler := className
		if handlerMethod != "" {
			handler = className + "." + handlerMethod
		}
		var hit SearchResult
		var ok bool
		var err error
		if triggerType != "" {
			hit, ok, err = s.searchAzureTriggerQueueConsumerHit(ctx, repo, filePath, className, triggerType, scriptFile, triggerID, triggerSnapshotID, triggerLine)
		} else {
			hit, ok, err = s.searchFunctionByIdentity(ctx, repo, filePath, handler, triggerSnapshotID)
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if hit.Extra == nil {
			hit.Extra = map[string]interface{}{}
		}
		hit.Extra["queue"] = queueName
		hit.Extra["consumer_id"] = consumerID
		hit.Extra["consumer"] = true
		results = append(results, hit)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (s *Storage) searchAzureTriggerQueueConsumerHit(ctx context.Context, repo, functionJSONPath, functionName, triggerType, scriptFile string, triggerID int64, snapshotID sql.NullInt64, lineNumber int) (SearchResult, bool, error) {
	for _, sourceFile := range azureTriggerSourcePathCandidates(functionJSONPath, scriptFile) {
		for _, handler := range azureTriggerHandlerNameCandidates(triggerType, functionName) {
			if hit, ok, err := s.searchFunctionByIdentity(ctx, repo, sourceFile, handler, snapshotID); err != nil {
				return SearchResult{}, false, err
			} else if ok {
				if hit.Extra == nil {
					hit.Extra = map[string]interface{}{}
				}
				hit.Extra["azure_trigger"] = true
				hit.Extra["trigger_type"] = triggerType
				hit.Extra["trigger_file"] = functionJSONPath
				hit.Extra["trigger_function"] = functionName
				return hit, true, nil
			}
		}
	}

	if triggerID == 0 || functionJSONPath == "" || functionName == "" {
		return SearchResult{}, false, nil
	}
	hit := SearchResult{
		ID:        triggerID,
		Type:      "azure_trigger",
		Name:      functionName,
		FilePath:  functionJSONPath,
		RepoName:  repo,
		StartLine: lineNumber,
		EndLine:   lineNumber,
		Extra: map[string]interface{}{
			"azure_trigger":     true,
			"trigger_type":      triggerType,
			"trigger_function":  functionName,
			"unresolved_source": true,
		},
	}
	if snapshotID.Valid {
		id := snapshotID.Int64
		hit.SnapshotID = &id
	}
	return hit, true, nil
}

// SearchQueueProducers finds indexed queue producers whose queue or caller ID
// matches one of the supplied SQL LIKE patterns.
func (s *Storage) SearchQueueProducers(ctx context.Context, patterns []string, limit int, includedRepos []string, filters ...SnapshotFilter) ([]SearchResult, error) {
	if s == nil || s.pool == nil || len(patterns) == 0 || limit < 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = s.queryContext()
	}
	args := []any{patterns}
	repoFilter := ""
	if len(includedRepos) > 0 {
		args = append(args, repoFilterPatterns(includedRepos))
		repoFilter = fmt.Sprintf(" AND r.name ILIKE ANY($%d::text[])", len(args))
	}
	aliasFilter := ""
	if filter, ok := optionalSnapshotFilter(filters); ok {
		argNum := len(args) + 1
		appendSnapshotFilter(&repoFilter, &args, &argNum, "sp.snapshot_id", filter)
		appendSnapshotFilter(&aliasFilter, &args, &argNum, "snapshot_id", filter)
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		WITH queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases
			WHERE COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
			%s
		)
		SELECT r.name, sp.caller_id, sp.queue_name, COALESCE(sp.line_number, 0), sp.snapshot_id
		FROM sqs_producers sp
		JOIN repositories r ON r.id = sp.repo_id
		WHERE (
			LOWER(sp.queue_name) LIKE ANY($1)
			OR LOWER(TRIM(BOTH '%%' FROM sp.queue_name)) LIKE ANY($1)
			OR LOWER(sp.caller_id) LIKE ANY($1)
			OR EXISTS (
				SELECT 1
				FROM queue_aliases qa
				WHERE (
					qa.alias_key = LOWER(TRIM(BOTH '%%' FROM sp.queue_name))
					AND qa.alias_value LIKE ANY($1)
				) OR (
					qa.alias_value = LOWER(TRIM(BOTH '%%' FROM sp.queue_name))
					AND qa.alias_key LIKE ANY($1)
				)
			)
			OR EXISTS (
				SELECT 1
				FROM queue_aliases producer_alias
				JOIN queue_aliases query_alias
				  ON producer_alias.alias_value = query_alias.alias_value
				WHERE producer_alias.alias_key = LOWER(TRIM(BOTH '%%' FROM sp.queue_name))
				  AND (
					query_alias.alias_key LIKE ANY($1)
					OR query_alias.alias_value LIKE ANY($1)
				  )
			)
		)
		%s
		ORDER BY r.name, sp.caller_id, sp.queue_name
		LIMIT NULLIF($%d, 0)
	`, aliasFilter, repoFilter, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type producerRow struct {
		repoName, callerID, queueName string
		lineNumber                    int
		snapshotID                    sql.NullInt64
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (producerRow, error) {
		var c producerRow
		err := row.Scan(&c.repoName, &c.callerID, &c.queueName, &c.lineNumber, &c.snapshotID)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(candidates))
	for _, c := range candidates {
		repoName, callerID, queueName, lineNumber, snapshotID := c.repoName, c.callerID, c.queueName, c.lineNumber, c.snapshotID
		repo, filePath, functionName := parseGraphCallerID(callerID)
		if repo == "" {
			repo = repoName
		}
		if filePath == "" || functionName == "" {
			continue
		}
		hit, ok, err := s.searchFunctionByIdentity(ctx, repo, filePath, functionName, snapshotID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if hit.Extra == nil {
			hit.Extra = map[string]interface{}{}
		}
		hit.Extra["queue"] = queueName
		hit.Extra["producer_id"] = callerID
		hit.Extra["producer"] = true
		if lineNumber > 0 {
			hit.StartLine = lineNumber
			hit.EndLine = lineNumber
		}
		results = append(results, hit)
		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (s *Storage) searchFunctionByIdentity(ctx context.Context, repo, filePath, functionName string, snapshotID sql.NullInt64) (SearchResult, bool, error) {
	var hit SearchResult
	var sourceCode *string
	err := s.pool.QueryRow(ctx, `
		SELECT f.id, f.name, f.start_line, f.end_line, f.source_code,
		       fi.path AS file_path, r.name AS repo_name, fi.snapshot_id
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE r.name = $1
		  AND fi.path = $2
		  AND f.name = $3
		  AND fi.snapshot_id IS NOT DISTINCT FROM $4::bigint
		ORDER BY f.start_line, f.id
		LIMIT 1
	`, repo, filePath, functionName, snapshotID).Scan(&hit.ID, &hit.Name, &hit.StartLine, &hit.EndLine, &sourceCode, &hit.FilePath, &hit.RepoName, &hit.SnapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SearchResult{}, false, nil
	}
	if err != nil {
		return SearchResult{}, false, err
	}
	hit.Type = "function"
	if sourceCode != nil {
		hit.SourceCode = *sourceCode
	}
	return hit, true, nil
}

func parseGraphCallerID(callerID string) (repo, filePath, name string) {
	first := strings.Index(callerID, ":")
	last := strings.LastIndex(callerID, ":")
	if first < 0 || last <= first {
		return "", "", ""
	}
	return callerID[:first], callerID[first+1 : last], callerID[last+1:]
}

func azureTriggerSourcePathCandidates(functionJSONPath, scriptFile string) []string {
	functionJSONPath = strings.TrimSpace(functionJSONPath)
	if functionJSONPath == "" {
		return nil
	}

	baseDir := filepath.Dir(functionJSONPath)
	seen := make(map[string]bool)
	var out []string
	add := func(path string) {
		path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	addDefaultIndexCandidates := func() {
		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(filepath.ToSlash(filepath.Join(baseDir, "index"+ext)))
		}
	}

	scriptFile = strings.TrimSpace(scriptFile)
	if scriptFile != "" {
		resolved := filepath.ToSlash(filepath.Clean(filepath.Join(baseDir, scriptFile)))
		add(resolved)
		base := strings.TrimSuffix(resolved, filepath.Ext(resolved))
		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(base + ext)
		}

		if strings.Contains(resolved, "/dist/") {
			alt := strings.Replace(resolved, "/dist/", "/src/", 1)
			altBase := strings.TrimSuffix(alt, filepath.Ext(alt))
			for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
				add(altBase + ext)
			}
		}
	}

	addDefaultIndexCandidates()

	functionDir := filepath.Base(baseDir)
	parentDir := filepath.Dir(baseDir)
	for _, prefix := range []string{"src", "dist"} {
		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(filepath.ToSlash(filepath.Join(parentDir, prefix, functionDir, "index"+ext)))
		}
	}

	return out
}

func azureTriggerHandlerNameCandidates(triggerType, functionName string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	add(functionName)
	switch strings.TrimSpace(triggerType) {
	case "httpTrigger":
		add("httpTrigger")
	case "timerTrigger":
		add("timerTrigger")
	case "queueTrigger":
		add("queueTrigger")
	case "serviceBusTrigger":
		add("serviceBusTrigger")
	case "blobTrigger":
		add("blobTrigger")
	case "eventHubTrigger":
		add("eventHubTrigger")
	case "cosmosDBTrigger":
		add("cosmosDBTrigger")
	}
	return out
}

// FunctionsByFilePath returns all functions defined in a file (optionally scoped to a repo).
func (s *Storage) FunctionsByFilePath(repo, path string, filters ...SnapshotFilter) ([]SearchResult, error) {
	query := `
		SELECT f.id, f.name, f.start_line, f.end_line, fi.snapshot_id,
		       fi.path as file_path, r.name as repo_name
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE fi.path = $1`
	args := []interface{}{path}
	if repo != "" {
		query += " AND r.name = $2"
		args = append(args, repo)
	}
	argNum := len(args) + 1
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}
	query += " ORDER BY f.start_line"

	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.Type = "function"
		results = append(results, r)
	}
	return results, rows.Err()
}

// FunctionsByFileRange returns functions that overlap a line range in a file.
func (s *Storage) FunctionsByFileRange(repo, path string, startLine, endLine int, filters ...SnapshotFilter) ([]SearchResult, error) {
	if startLine > endLine {
		startLine, endLine = endLine, startLine
	}
	query := `
		SELECT f.id, f.name, f.start_line, f.end_line, fi.snapshot_id,
		       fi.path as file_path, r.name as repo_name
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE fi.path = $1
		  AND f.start_line <= $2
		  AND f.end_line >= $3`
	args := []interface{}{path, endLine, startLine}
	argNum := 4
	if repo != "" {
		query += " AND r.name = $4"
		args = append(args, repo)
		argNum = 5
	}
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}
	query += " ORDER BY f.start_line"

	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.Type = "function"
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchClassesAll finds all classes by name pattern.
func (s *Storage) SearchClassesAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.searchClassesWithLimit(pattern, 0, filters...)
}

func (s *Storage) searchClassesWithLimit(pattern string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%", limit}, filters)
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(`
		SELECT c.id, c.name, c.start_line, c.end_line, c.extends_class, c.is_exported,
		       COALESCE(c.is_enum, false), COALESCE(c.is_abstract, false), COALESCE(c.is_record, false),
		       fi.snapshot_id,
		       fi.path as file_path, r.name as repo_name
		FROM classes c
		JOIN files fi ON fi.id = c.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE c.name ILIKE $1
		/* visibility */
		ORDER BY c.name, c.id
		LIMIT NULLIF($2, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var extendsClass *string
		var isExported, isEnum, isAbstract, isRecord bool
		err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &extendsClass, &isExported, &isEnum, &isAbstract, &isRecord, &r.SnapshotID, &r.FilePath, &r.RepoName)
		if err != nil {
			return nil, err
		}
		r.Type = "class"
		if isEnum {
			r.Type = "enum"
		}
		r.Extra = map[string]interface{}{
			"is_exported": isExported,
			"is_enum":     isEnum,
			"is_abstract": isAbstract,
			"is_record":   isRecord,
		}
		if extendsClass != nil {
			r.Extra["extends"] = *extendsClass
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchInterfaces(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT i.id, i.name, i.start_line, i.end_line, i.is_exported, i.is_functional,
		       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM interfaces i
		JOIN files fi ON fi.id = i.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (i.name ILIKE $1 OR fi.path ILIKE $1) %s
		ORDER BY %si.name, i.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var isExported, isFunctional bool
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &isExported, &isFunctional, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.Type = "interface"
		r.Extra = map[string]interface{}{
			"is_exported":   isExported,
			"is_functional": isFunctional,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchTypeAliases(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT ta.id, ta.name, COALESCE(ta.start_line, 0), ta.definition, ta.is_exported,
		       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM type_aliases ta
		JOIN files fi ON fi.id = ta.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (ta.name ILIKE $1 OR ta.definition ILIKE $1 OR fi.path ILIKE $1) %s
		ORDER BY %sta.name, ta.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var definition *string
		var isExported bool
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &definition, &isExported, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = "type_alias"
		r.Extra = map[string]interface{}{
			"is_exported": isExported,
		}
		if definition != nil {
			r.Extra["definition"] = *definition
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchHookCalls(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT hc.id, hc.hook_name, COALESCE(hc.function_name, ''), COALESCE(hc.line_number, 0), hc.is_custom, COALESCE(hc.origin, ''),
		       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM hook_calls hc
		JOIN files fi ON fi.id = hc.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (hc.hook_name ILIKE $1 OR COALESCE(hc.function_name, '') ILIKE $1 OR fi.path ILIKE $1) %s
		ORDER BY %shc.hook_name, fi.path, hc.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var functionName string
		var isCustom bool
		var origin string
		if err := rows.Scan(&r.ID, &r.Name, &functionName, &r.StartLine, &isCustom, &origin, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = "hook_call"
		r.Extra = map[string]interface{}{
			"function":  functionName,
			"is_custom": isCustom,
			"origin":    origin,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchVueComponentContracts(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT v.id, v.kind, v.field_name, COALESCE(v.field_type, ''), COALESCE(v.is_required, false), COALESCE(v.line_number, 0),
		       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM vue_component_contracts v
		JOIN files fi ON fi.id = v.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (v.field_name ILIKE $1 OR COALESCE(v.field_type, '') ILIKE $1 OR fi.path ILIKE $1) %s
		ORDER BY %sv.kind, v.field_name, fi.path, v.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var kind, fieldType string
		var required bool
		if err := rows.Scan(&r.ID, &kind, &r.Name, &fieldType, &required, &r.StartLine, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = "vue_component_contract"
		r.Extra = map[string]interface{}{
			"kind":        kind,
			"field_type":  fieldType,
			"is_required": required,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchPiniaStores(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT p.id, p.store_id, COALESCE(p.line_number, 0), COALESCE(p.state_fields::text, ''),
		       COALESCE(p.getter_names::text, ''), COALESCE(p.action_names::text, ''),
		       fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM pinia_stores p
		JOIN files fi ON fi.id = p.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (p.store_id ILIKE $1
		   OR COALESCE(p.state_fields::text, '') ILIKE $1
		   OR COALESCE(p.getter_names::text, '') ILIKE $1
		   OR COALESCE(p.action_names::text, '') ILIKE $1
		   OR fi.path ILIKE $1) %s
		ORDER BY %sp.store_id, fi.path, p.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var stateFields, getters, actions string
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &stateFields, &getters, &actions, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = "pinia_store"
		r.Extra = map[string]interface{}{
			"state_fields": stateFields,
			"getters":      getters,
			"actions":      actions,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) SearchEnumConstants(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	scopeClause, scopeOrder, args := typeSearchScope(pattern, filters)
	query := fmt.Sprintf(`
		SELECT ec.id, c.name || '.' || ec.name AS name, c.start_line, c.end_line,
		       ec.ordinal, fi.snapshot_id, fi.path AS file_path, r.name AS repo_name
		FROM enum_constants ec
		JOIN classes c ON c.id = ec.class_id
		JOIN files fi ON fi.id = c.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (ec.name ILIKE $1 OR c.name ILIKE $1 OR (c.name || '.' || ec.name) ILIKE $1 OR fi.path ILIKE $1) %s
		ORDER BY %sc.name, ec.ordinal, ec.id
		LIMIT %d
	`, scopeClause, scopeOrder, SearchFetchCap+1)
	rows, err := s.pool.Query(s.queryContext(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var ordinal int
		if err := rows.Scan(&r.ID, &r.Name, &r.StartLine, &r.EndLine, &ordinal, &r.SnapshotID, &r.FilePath, &r.RepoName); err != nil {
			return nil, err
		}
		r.Type = "enum_constant"
		r.Extra = map[string]interface{}{
			"ordinal": ordinal,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchEndpointsAll finds all endpoints by path pattern.
func (s *Storage) SearchEndpointsAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.searchEndpointsWithLimit(pattern, 0, filters...)
}

// SearchGraphQLOperationsAll finds GraphQL document operations by name or path pattern.
func (s *Storage) SearchGraphQLOperationsAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%"}, filters)
	rows, err := s.pool.Query(s.queryContext(), strings.ReplaceAll(`
		SELECT 'graphql_operation' AS result_type, o.id, o.operation_name, o.operation_type,
		       COALESCE(o.line_number, 0), fi.snapshot_id, fi.path, r.name,
		       COUNT(DISTINCT l.usage_id) AS usage_count, '' AS resolver_name
		FROM graphql_operations o
		JOIN files fi ON fi.id = o.file_id
		JOIN repositories r ON r.id = fi.repo_id
		LEFT JOIN graphql_usage_operation_links l ON l.operation_id = o.id
		WHERE (o.operation_name ILIKE $1 OR fi.path ILIKE $1)
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
		/* visibility */
		GROUP BY o.id, o.operation_name, o.operation_type, o.line_number, fi.snapshot_id, fi.path, r.name

		UNION ALL

		SELECT 'graphql_resolver' AS result_type, gr.id, gr.operation_name, gr.operation_type,
		       COALESCE(gr.line_number, 0), fi.snapshot_id, fi.path, r.name,
		       0 AS usage_count, gr.resolver_name
		FROM graphql_operation_resolvers gr
		JOIN files fi ON fi.id = gr.file_id
		JOIN repositories r ON r.id = gr.repo_id
		WHERE (gr.operation_name ILIKE $1 OR gr.resolver_name ILIKE $1 OR fi.path ILIKE $1)
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
		/* visibility */

		ORDER BY 3, 7
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var resultType string
		var operationType string
		var usageCount int
		var resolverName string
		if err := rows.Scan(&resultType, &r.ID, &r.Name, &operationType, &r.StartLine, &r.SnapshotID, &r.FilePath, &r.RepoName, &usageCount, &resolverName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = resultType
		r.Extra = map[string]interface{}{
			"operation_type": operationType,
			"usage_count":    usageCount,
		}
		if resolverName != "" {
			r.Extra["resolver_name"] = resolverName
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchAzureTriggersAll finds Azure Function triggers by function name, route, resource, or file path.
func (s *Storage) SearchAzureTriggersAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%"}, filters)
	rows, err := s.pool.Query(s.queryContext(), strings.ReplaceAll(`
		SELECT t.id, t.function_name, t.trigger_type, COALESCE(t.line_number, 0), fi.snapshot_id,
		       fi.path, r.name, COALESCE(t.route, ''), COALESCE(t.resource_name, '')
		FROM azure_function_triggers t
		JOIN files fi ON fi.id = t.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (
			t.function_name ILIKE $1
			OR COALESCE(t.route, '') ILIKE $1
			OR COALESCE(t.resource_name, '') ILIKE $1
			OR fi.path ILIKE $1
		)
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
		/* visibility */
		ORDER BY t.function_name, fi.path, t.trigger_type
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var triggerType string
		var route string
		var resourceName string
		if err := rows.Scan(&r.ID, &r.Name, &triggerType, &r.StartLine, &r.SnapshotID, &r.FilePath, &r.RepoName, &route, &resourceName); err != nil {
			return nil, err
		}
		r.EndLine = r.StartLine
		r.Type = "azure_trigger"
		r.Extra = map[string]interface{}{
			"trigger_type":  triggerType,
			"route":         route,
			"resource_name": resourceName,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchDataAccessesAll finds data-access entities by name pattern.
func (s *Storage) SearchDataAccessesAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("COALESCE(fi.snapshot_id,da.snapshot_id)", []any{"%" + EscapeLike(pattern) + "%", pattern, EscapeLike(pattern) + "%"}, filters)
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(`
		SELECT da.id, da.entity_name, da.access, da.line_number,
		       split_part(da.caller_id, ':', 3) AS caller_name,
		       COALESCE(fi.snapshot_id, da.snapshot_id) AS snapshot_id,
		       COALESCE(fi.path, split_part(da.caller_id, ':', 2)) AS file_path,
		       r.name AS repo_name,
		       fn.source_code
		FROM data_accesses da
		JOIN repositories r ON r.id = da.repo_id
		LEFT JOIN files fi ON fi.repo_id = da.repo_id
		  AND fi.path = split_part(da.caller_id, ':', 2)
		  AND fi.snapshot_id IS NOT DISTINCT FROM da.snapshot_id
		LEFT JOIN functions fn ON fn.file_id = fi.id
		  AND fn.name = split_part(da.caller_id, ':', 3)
		  AND (da.line_number IS NULL OR da.line_number BETWEEN fn.start_line AND fn.end_line)
		WHERE da.entity_name ILIKE $1
		/* visibility */
		ORDER BY
		     CASE
		       WHEN LOWER(da.entity_name) = LOWER($2) THEN 0
		       WHEN da.entity_name ILIKE $3 THEN 1
		       ELSE 2
		     END,
		     da.entity_name,
		     r.name,
		     COALESCE(fi.path, split_part(da.caller_id, ':', 2)),
		     COALESCE(da.line_number, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var access string
		var lineNumber *int
		var callerName string
		var sourceCode *string
		if err := rows.Scan(&r.ID, &r.Name, &access, &lineNumber, &callerName, &r.SnapshotID, &r.FilePath, &r.RepoName, &sourceCode); err != nil {
			return nil, err
		}
		r.Type = "data_entity"
		if lineNumber != nil {
			r.StartLine = *lineNumber
			r.EndLine = *lineNumber
		}
		if sourceCode != nil {
			r.SourceCode = *sourceCode
		}
		r.Extra = map[string]interface{}{
			"access":    access,
			"caller":    callerName,
			"caller_id": r.RepoName + ":" + r.FilePath + ":" + callerName,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchExternalSymbolsAll finds unresolved/external call targets by name pattern.
func (s *Storage) SearchExternalSymbolsAll(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.SearchExternalSymbols(pattern, 0, filters...)
}

// SearchExternalSymbols finds unresolved/external call targets by name pattern.
func (s *Storage) SearchExternalSymbols(pattern string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%", pattern, EscapeLike(pattern) + "%", limit}, filters)
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(`
		SELECT fc.id, fc.callee_name, fc.line_number,
		       f.name AS caller_name,
		       fi.snapshot_id,
		       fi.path AS file_path,
		       r.name AS repo_name,
		       f.source_code
		FROM function_calls fc
		JOIN functions f ON f.id = fc.caller_function_id
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE fc.callee_function_id IS NULL
		  AND fc.callee_name <> ''
		/* visibility */
		  AND fc.callee_name ILIKE $1
		ORDER BY
		     CASE
		       WHEN LOWER(fc.callee_name) = LOWER($2) THEN 0
		       WHEN fc.callee_name ILIKE $3 THEN 1
		       ELSE 2
		     END,
		     fc.callee_name,
		     r.name,
		     fi.path,
		     COALESCE(fc.line_number, 0)
		LIMIT NULLIF($4, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var lineNumber *int
		var callerName string
		var sourceCode *string
		if err := rows.Scan(&r.ID, &r.Name, &lineNumber, &callerName, &r.SnapshotID, &r.FilePath, &r.RepoName, &sourceCode); err != nil {
			return nil, err
		}
		r.Type = "external_symbol"
		if lineNumber != nil {
			r.StartLine = *lineNumber
			r.EndLine = *lineNumber
		}
		if sourceCode != nil {
			r.SourceCode = *sourceCode
		}
		r.Extra = map[string]interface{}{
			"caller":    callerName,
			"caller_id": r.RepoName + ":" + r.FilePath + ":" + callerName,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) searchEndpointsWithLimit(pattern string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	visibility, args := searchSnapshotVisibility("fi.snapshot_id", []any{"%" + EscapeLike(pattern) + "%", limit}, filters)
	rows, err := s.pool.Query(ctx, strings.ReplaceAll(`
		SELECT e.id, e.path, e.method, e.line_number, fi.snapshot_id, fi.path as file_path, r.name as repo_name,
		       COALESCE(fn.name, '') as handler
		FROM endpoints e
		JOIN files fi ON fi.id = e.file_id
		JOIN repositories r ON r.id = e.repo_id
		LEFT JOIN functions fn ON e.handler_function_id = fn.id
		WHERE e.path ILIKE $1
		/* visibility */
		  AND fi.path NOT LIKE '.codebase-snapshots/%'
		ORDER BY e.method, e.path, e.id
		LIMIT NULLIF($2, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var method string
		var lineNum *int
		var handler string
		err := rows.Scan(&r.ID, &r.Name, &method, &lineNum, &r.SnapshotID, &r.FilePath, &r.RepoName, &handler)
		if err != nil {
			return nil, err
		}
		r.Type = "endpoint"
		if lineNum != nil {
			r.StartLine = *lineNum
		}
		r.Extra = map[string]interface{}{
			"method":  method,
			"handler": handler,
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchFiles finds files by path pattern
func (s *Storage) SearchFiles(pattern string, filters ...SnapshotFilter) ([]SearchResult, error) {
	query := `
			SELECT fi.id, fi.path, fi.language, fi.snapshot_id, r.name as repo_name,
			       (SELECT COUNT(*) FROM functions f WHERE f.file_id = fi.id) as fn_count
			FROM files fi
			JOIN repositories r ON r.id = fi.repo_id
			WHERE fi.path ILIKE $1`
	args := []interface{}{"%" + EscapeLike(pattern) + "%"}
	argNum := 2
	if filter, ok := optionalSnapshotFilter(filters); ok {
		appendSnapshotFilter(&query, &args, &argNum, "fi.snapshot_id", filter)
	}
	query += `
			ORDER BY fi.path
			LIMIT 50`
	ctx, cancel := context.WithTimeout(s.queryContext(), 15*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var language *string
		var fnCount int
		err := rows.Scan(&r.ID, &r.FilePath, &language, &r.SnapshotID, &r.RepoName, &fnCount)
		if err != nil {
			return nil, err
		}
		r.Type = "file"
		r.Name = r.FilePath
		r.Extra = map[string]interface{}{
			"function_count": fnCount,
		}
		if language != nil {
			r.Extra["language"] = *language
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// TrigramSearch performs fuzzy search across searchable entity types.
// Returns combined results sorted by similarity score
func (s *Storage) TrigramSearch(query string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.trigramSearch(query, limit, true, filters...)
}

// TrigramSearchInSnapshots excludes discovery hints from other workspaces before
// applying the limit, for Trace suggestions and other workspace-only lookups.
func (s *Storage) TrigramSearchInSnapshots(query string, limit int, filters ...SnapshotFilter) ([]SearchResult, error) {
	return s.trigramSearch(query, limit, false, filters...)
}
func (s *Storage) trigramSearch(query string, limit int, includeHints bool, filters ...SnapshotFilter) ([]SearchResult, error) {
	visibility, args := searchSnapshotVisibility("combined.snapshot_id", []any{query, limit}, filters)
	if !includeHints {
		visibility = ""
		args = []any{query, limit}
		if filter, ok := optionalSnapshotFilter(filters); ok {
			arg := 3
			appendSnapshotFilter(&visibility, &args, &arg, "combined.snapshot_id", filter)
		}
	}
	rows, err := s.pool.Query(s.queryContext(), strings.ReplaceAll(`
		SELECT type, id, name, start_line, end_line, file_path, repo_name, snapshot_id, score, source_code,
		       access, caller_name, extra_text
		FROM (
			SELECT 'function' as type, f.id, f.name, f.start_line, f.end_line,
			       fi.path as file_path, r.name as repo_name,
			       fi.snapshot_id as snapshot_id,
			       similarity(f.name, $1) as score, f.source_code,
			       NULL::text as access, NULL::text as caller_name, NULL::text as extra_text
			FROM functions f
			JOIN files fi ON fi.id = f.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE f.name % $1
			UNION ALL
			SELECT 'class' as type, c.id, c.name, c.start_line, c.end_line,
			       fi.path as file_path, r.name as repo_name,
			       fi.snapshot_id,
			       similarity(c.name, $1) as score, NULL as source_code,
			       NULL::text as access, NULL::text as caller_name, NULL::text as extra_text
			FROM classes c
			JOIN files fi ON fi.id = c.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE c.name % $1
			UNION ALL
			SELECT 'data_entity' as type, da.id, da.entity_name as name,
			       COALESCE(da.line_number, 0) as start_line,
			       COALESCE(da.line_number, 0) as end_line,
			       COALESCE(fi.path, split_part(da.caller_id, ':', 2)) as file_path,
			       r.name as repo_name,
			       COALESCE(fi.snapshot_id, da.snapshot_id) as snapshot_id,
			       similarity(da.entity_name, $1) as score,
			       fn.source_code,
			       da.access,
			       split_part(da.caller_id, ':', 3) as caller_name,
			       NULL::text as extra_text
			FROM data_accesses da
			JOIN repositories r ON r.id = da.repo_id
			LEFT JOIN files fi ON fi.repo_id = da.repo_id
			  AND fi.path = split_part(da.caller_id, ':', 2)
			  AND fi.snapshot_id IS NOT DISTINCT FROM da.snapshot_id
			LEFT JOIN functions fn ON fn.file_id = fi.id
			  AND fn.name = split_part(da.caller_id, ':', 3)
			  AND (da.line_number IS NULL OR da.line_number BETWEEN fn.start_line AND fn.end_line)
			WHERE da.entity_name % $1
			UNION ALL
			SELECT 'external_symbol' as type, fc.id, fc.callee_name as name,
			       COALESCE(fc.line_number, 0) as start_line,
			       COALESCE(fc.line_number, 0) as end_line,
			       fi.path as file_path,
			       r.name as repo_name,
			       fi.snapshot_id,
			       similarity(fc.callee_name, $1) as score,
			       f.source_code,
			       NULL::text as access,
			       f.name as caller_name,
			       NULL::text as extra_text
			FROM function_calls fc
			JOIN functions f ON f.id = fc.caller_function_id
			JOIN files fi ON fi.id = f.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE fc.callee_function_id IS NULL
			  AND fc.callee_name <> ''
			  AND fc.callee_name % $1
			UNION ALL
			SELECT 'graphql_operation' as type, o.id, o.operation_name as name,
			       COALESCE(o.line_number, 0) as start_line,
			       COALESCE(o.line_number, 0) as end_line,
			       fi.path as file_path,
			       r.name as repo_name,
			       fi.snapshot_id,
			       similarity(o.operation_name, $1) as score,
			       NULL as source_code,
			       NULL::text as access,
			       NULL::text as caller_name,
			       o.operation_type as extra_text
			FROM graphql_operations o
			JOIN files fi ON fi.id = o.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE o.operation_name % $1
			UNION ALL
			SELECT 'graphql_resolver' as type, gr.id, gr.operation_name as name,
			       COALESCE(gr.line_number, 0) as start_line,
			       COALESCE(gr.line_number, 0) as end_line,
			       fi.path as file_path,
			       r.name as repo_name,
			       fi.snapshot_id,
			       GREATEST(
			         similarity(gr.operation_name, $1),
			         similarity(gr.resolver_name, $1)
			       ) as score,
			       NULL as source_code,
			       NULL::text as access,
			       gr.resolver_name as caller_name,
			       gr.operation_type as extra_text
			FROM graphql_operation_resolvers gr
			JOIN files fi ON fi.id = gr.file_id
			JOIN repositories r ON r.id = gr.repo_id
			WHERE gr.operation_name % $1
			   OR gr.resolver_name % $1
			UNION ALL
			SELECT 'azure_trigger' as type, t.id, t.function_name as name,
			       COALESCE(t.line_number, 0) as start_line,
			       COALESCE(t.line_number, 0) as end_line,
			       fi.path as file_path,
			       r.name as repo_name,
			       fi.snapshot_id,
			       GREATEST(
			         similarity(t.function_name, $1),
			         similarity(COALESCE(t.route, ''), $1),
			         similarity(COALESCE(t.resource_name, ''), $1)
			       ) as score,
			       NULL as source_code,
			       NULL::text as access,
			       NULL::text as caller_name,
			       t.trigger_type as extra_text
			FROM azure_function_triggers t
			JOIN files fi ON fi.id = t.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE t.function_name % $1
			   OR COALESCE(t.route, '') % $1
			   OR COALESCE(t.resource_name, '') % $1
		) combined
		WHERE TRUE /* visibility */
		ORDER BY score DESC, type, id
		LIMIT NULLIF($2, 0)
	`, "/* visibility */", visibility), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var score float64
		var sourceCode *string
		var access *string
		var callerName *string
		var extraText *string
		err := rows.Scan(&r.Type, &r.ID, &r.Name, &r.StartLine, &r.EndLine, &r.FilePath, &r.RepoName, &r.SnapshotID, &score, &sourceCode, &access, &callerName, &extraText)
		if err != nil {
			return nil, err
		}
		if sourceCode != nil {
			r.SourceCode = *sourceCode
		}
		r.Extra = map[string]interface{}{
			"score": score,
		}
		if access != nil {
			r.Extra["access"] = *access
		}
		if callerName != nil {
			r.Extra["caller"] = *callerName
			r.Extra["caller_id"] = r.RepoName + ":" + r.FilePath + ":" + *callerName
		}
		if extraText != nil {
			switch r.Type {
			case "graphql_operation", "graphql_resolver":
				r.Extra["operation_type"] = *extraText
			case "azure_trigger":
				r.Extra["trigger_type"] = *extraText
			}
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Type buckets are capped, so visibility is restricted in SQL to the captured
// workspace selection (plus legacy rows when the selection allows them). Rows
// from other workspaces would otherwise consume the cap and be dropped by the
// handler afterwards; cross-workspace discovery uses DiscoverOtherWorkspaceHits.
func typeSearchScope(pattern string, filters []SnapshotFilter) (string, string, []any) {
	args := []any{"%" + EscapeLike(pattern) + "%"}
	filter, ok := optionalSnapshotFilter(filters)
	if !ok {
		return "", "", args
	}
	args = append(args, filter.SnapshotIDs, filter.IncludeLegacy)
	return `AND (fi.snapshot_id=ANY($2::bigint[]) OR ($3::boolean AND fi.snapshot_id IS NULL))`, "", args
}

// searchSnapshotVisibility limits a search to the captured snapshot selection.
// Facts from other workspaces are never admitted here: they are filtered out
// after the fact, which breaks any LIMIT applied in SQL.
func searchSnapshotVisibility(column string, args []any, filters []SnapshotFilter) (string, []any) {
	filter, ok := optionalSnapshotFilter(filters)
	if !ok {
		return "", args
	}
	index := len(args) + 1
	args = append(args, filter.SnapshotIDs, filter.IncludeLegacy)
	return fmt.Sprintf("AND (%s=ANY($%d::bigint[]) OR ($%d::boolean AND %s IS NULL))", column, index, index+1, column), args
}
