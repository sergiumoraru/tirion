package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

type azureRouteConfigModule struct{}

type azureRouteHostConfig struct {
	hostPath    string
	routePrefix string
}

func (m *azureRouteConfigModule) ID() string {
	return "azure_route_config"
}

func (m *azureRouteConfigModule) FinalizeRepo(ctx *repoFinalizeContext) error {
	if ctx == nil || ctx.storage == nil {
		return nil
	}

	routeConfigs, err := loadAzureRouteConfigs(ctx)
	if err != nil {
		return err
	}

	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT e.id, t.function_name, t.trigger_type, t.route, fi.path, COALESCE(t.script_file, '')
		FROM endpoints e
		JOIN azure_function_triggers t
		  ON t.repo_id = e.repo_id
		 AND t.file_id = e.file_id
		 AND t.line_number = e.line_number
		JOIN files fi ON fi.id = t.file_id
		WHERE e.repo_id = $1
		  AND fi.snapshot_id IS NOT DISTINCT FROM $2::bigint
		  AND t.trigger_type = 'httpTrigger'
		  AND lower(COALESCE(t.direction, 'in')) = 'in'
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var endpointID int64
		var functionName string
		var triggerType string
		var rawRoute *string
		var functionFile string
		var scriptFile string
		if err := rows.Scan(&endpointID, &functionName, &triggerType, &rawRoute, &functionFile, &scriptFile); err != nil {
			return err
		}
		routePrefix := resolveAzureRoutePrefix(routeConfigs, functionFile)
		effectivePath := effectiveAzureEndpointPath(functionName, derefString(rawRoute), routePrefix)
		pathCanonical := normalizeEndpointPathIdentity(effectivePath)
		handlerID, err := findAzureEndpointHandlerFunctionID(ctx, functionFile, scriptFile, triggerType, functionName)
		if err != nil {
			return err
		}
		if _, err := ctx.storage.Pool().Exec(context.Background(), `
			UPDATE endpoints
			SET path = $2, path_canonical = $3, handler_function_id = COALESCE($4, handler_function_id)
			WHERE id = $1
		`, endpointID, effectivePath, pathCanonical, handlerID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	triggerRows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT t.id, t.function_name, t.route, fi.path
		FROM azure_function_triggers t
		JOIN files fi ON fi.id = t.file_id
		WHERE t.repo_id = $1
		  AND fi.snapshot_id IS NOT DISTINCT FROM $2::bigint
		  AND t.trigger_type = 'httpTrigger'
		  AND lower(COALESCE(t.direction, 'in')) = 'in'
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return err
	}
	defer triggerRows.Close()

	for triggerRows.Next() {
		var triggerID int64
		var functionName string
		var rawRoute *string
		var functionFile string
		if err := triggerRows.Scan(&triggerID, &functionName, &rawRoute, &functionFile); err != nil {
			return err
		}
		routePrefix := resolveAzureRoutePrefix(routeConfigs, functionFile)
		effectiveRoute := effectiveAzureEndpointPath(functionName, derefString(rawRoute), routePrefix)
		if _, err := ctx.storage.Pool().Exec(context.Background(), `
			UPDATE azure_function_triggers
			SET route = $2
			WHERE id = $1
		`, triggerID, effectiveRoute); err != nil {
			return err
		}
	}
	return triggerRows.Err()
}

func loadAzureRouteConfigs(ctx *repoFinalizeContext) ([]azureRouteHostConfig, error) {
	rows, err := ctx.storage.Pool().Query(context.Background(), `
		SELECT fi.path, COALESCE(cfg.route_prefix, 'api')
		FROM azure_host_configs cfg
		JOIN files fi ON fi.id = cfg.file_id
		WHERE cfg.repo_id = $1
		  AND fi.snapshot_id IS NOT DISTINCT FROM $2::bigint
		ORDER BY length(fi.path) DESC, fi.path
	`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []azureRouteHostConfig
	for rows.Next() {
		var hostPath string
		var routePrefix string
		if err := rows.Scan(&hostPath, &routePrefix); err != nil {
			return nil, err
		}
		configs = append(configs, azureRouteHostConfig{
			hostPath:    hostPath,
			routePrefix: routePrefix,
		})
	}
	return configs, rows.Err()
}

func effectiveAzureEndpointPath(functionName, rawRoute, routePrefix string) string {
	prefix := strings.Trim(strings.TrimSpace(routePrefix), "/")
	route := trimAzureDefaultPrefix(rawRoute)
	if route == "" {
		route = strings.Trim(strings.TrimSpace(functionName), "/")
	}
	if prefix == "" {
		return "/" + route
	}
	return fmt.Sprintf("/%s/%s", prefix, route)
}

func resolveAzureRoutePrefix(configs []azureRouteHostConfig, functionFile string) string {
	normalizedFile := normalizeAzureRepoPath(functionFile)
	if normalizedFile == "" {
		return "api"
	}

	bestPrefix := "api"
	bestDepth := -1
	for _, cfg := range configs {
		hostRoot := normalizeAzureRepoPath(filepath.Dir(cfg.hostPath))
		if !azurePathWithinRoot(normalizedFile, hostRoot) {
			continue
		}
		depth := azurePathDepth(hostRoot)
		if depth > bestDepth {
			bestDepth = depth
			bestPrefix = cfg.routePrefix
		}
	}
	return bestPrefix
}

func trimAzureDefaultPrefix(route string) string {
	trimmed := strings.Trim(strings.TrimSpace(route), "/")
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if lower == "api" {
		return ""
	}
	if strings.HasPrefix(lower, "api/") {
		return strings.TrimSpace(trimmed[4:])
	}
	return trimmed
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeAzureRepoPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == "." {
		return ""
	}
	return strings.Trim(cleaned, "/")
}

func azurePathWithinRoot(filePath, rootPath string) bool {
	rootPath = normalizeAzureRepoPath(rootPath)
	filePath = normalizeAzureRepoPath(filePath)
	if filePath == "" {
		return false
	}
	if rootPath == "" {
		return true
	}
	return filePath == rootPath || strings.HasPrefix(filePath, rootPath+"/")
}

func azurePathDepth(value string) int {
	value = normalizeAzureRepoPath(value)
	if value == "" {
		return 0
	}
	return len(strings.Split(value, "/"))
}

func findAzureEndpointHandlerFunctionID(ctx *repoFinalizeContext, functionJSONPath, scriptFile, triggerType, functionName string) (*int64, error) {
	if ctx == nil || ctx.storage == nil {
		return nil, nil
	}
	for _, filePath := range azureSourcePathCandidates(functionJSONPath, scriptFile) {
		for _, handlerName := range azureHandlerNameCandidates(triggerType, functionName) {
			var id int64
			err := ctx.storage.Pool().QueryRow(context.Background(), `
				SELECT fn.id
				FROM functions fn
				JOIN files fi ON fi.id = fn.file_id
				WHERE fi.repo_id = $1
				  AND fi.snapshot_id IS NOT DISTINCT FROM $2::bigint
				  AND fi.path = $3
				  AND fn.name = $4
				ORDER BY fn.start_line, fn.end_line, fn.id
				LIMIT 1
			`, ctx.repoID, nullableInt64Ptr(ctx.snapshotID), filePath, handlerName).Scan(&id)
			if err == nil {
				return &id, nil
			}
			if err == pgx.ErrNoRows {
				continue
			}
			return nil, err
		}
	}
	return nil, nil
}

func azureSourcePathCandidates(functionJSONPath, scriptFile string) []string {
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

	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
		add(filepath.ToSlash(filepath.Join(baseDir, "index"+ext)))
	}

	functionDir := filepath.Base(baseDir)
	parentDir := filepath.Dir(baseDir)
	for _, prefix := range []string{"src", "dist"} {
		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
			add(filepath.ToSlash(filepath.Join(parentDir, prefix, functionDir, "index"+ext)))
		}
	}
	return out
}

func azureHandlerNameCandidates(triggerType, functionName string) []string {
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
	add("_module_")
	return out
}
