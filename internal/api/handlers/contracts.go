package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type ContractSummary struct {
	Repo               string `json:"repo"`
	EndpointCount      int    `json:"endpointCount"`
	HttpCallCount      int    `json:"httpCallCount"`
	GraphQLOperations  int    `json:"graphqlOperations,omitempty"`
	GraphQLUsages      int    `json:"graphqlUsages,omitempty"`
	GraphQLResolvers   int    `json:"graphqlResolvers,omitempty"`
	GraphQLPermissions int    `json:"graphqlPermissions,omitempty"`
	GraphQLEntrypoints int    `json:"graphqlEntrypoints,omitempty"`
	DataAccessCount    int    `json:"dataAccessCount,omitempty"`
	AzureTimerTriggers int    `json:"azureTimerTriggers,omitempty"`
	QueuesProduced     int    `json:"queuesProduced"`
	QueuesConsumed     int    `json:"queuesConsumed"`
}

type ContractEndpoint struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Handler string   `json:"handler"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Owners  []string `json:"owners,omitempty"`
}

type ContractHttpMatch struct {
	Repo    string `json:"repo"`
	Handler string `json:"handler"`
	Path    string `json:"path"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

type ContractHttpCall struct {
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Count      int                 `json:"count"`
	ClientType string              `json:"clientType,omitempty"`
	External   bool                `json:"external,omitempty"`
	Matches    []ContractHttpMatch `json:"matches,omitempty"`
}

type ContractQueue struct {
	Name           string   `json:"name"`
	Count          int      `json:"count"`
	Counterparties []string `json:"counterparties,omitempty"`
}

type ContractRepoCount struct {
	Repo  string `json:"repo"`
	Count int    `json:"count"`
}

type ContractGraphQLOperation struct {
	Name string `json:"name"`
	Type string `json:"type"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type ContractGraphQLUsage struct {
	ImportedAs string `json:"importedAs"`
	ImportPath string `json:"importPath"`
	Caller     string `json:"caller"`
	File       string `json:"file"`
	Line       int    `json:"line"`
}

type ContractGraphQLResolver struct {
	OperationName string `json:"operationName"`
	OperationType string `json:"operationType"`
	Resolver      string `json:"resolver"`
	File          string `json:"file"`
	Line          int    `json:"line"`
}

type ContractGraphQLPermission struct {
	OperationName  string `json:"operationName"`
	OperationType  string `json:"operationType"`
	RuleExpression string `json:"ruleExpression"`
	File           string `json:"file"`
	Line           int    `json:"line"`
}

type ContractGraphQLEntrypoint struct {
	HandlerName      string `json:"handlerName,omitempty"`
	RegistrationKind string `json:"registrationKind"`
	ControllersPath  string `json:"controllersPath,omitempty"`
	File             string `json:"file"`
	Line             int    `json:"line"`
}

type ContractDataAccess struct {
	Entity string `json:"entity"`
	Access string `json:"access"`
	Caller string `json:"caller"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

type ContractSchedule struct {
	RuleName           string `json:"ruleName"`
	ScheduleExpression string `json:"scheduleExpression"`
	TargetQueue        string `json:"targetQueue"`
	State              string `json:"state"`
	Source             string `json:"source,omitempty"`
	FunctionName       string `json:"functionName,omitempty"`
	File               string `json:"file,omitempty"`
	Line               int    `json:"line,omitempty"`
}

type ContractDetail struct {
	Repo                string                      `json:"repo"`
	Endpoints           []ContractEndpoint          `json:"endpoints"`
	HttpCalls           []ContractHttpCall          `json:"httpCalls"`
	HttpTargets         []ContractRepoCount         `json:"httpTargets,omitempty"`
	HttpCallers         []ContractRepoCount         `json:"httpCallers,omitempty"`
	GraphQLOperations   []ContractGraphQLOperation  `json:"graphqlOperations,omitempty"`
	GraphQLUsages       []ContractGraphQLUsage      `json:"graphqlUsages,omitempty"`
	GraphQLTargets      []ContractRepoCount         `json:"graphqlTargets,omitempty"`
	GraphQLCallers      []ContractRepoCount         `json:"graphqlCallers,omitempty"`
	GraphQLResolvers    []ContractGraphQLResolver   `json:"graphqlResolvers,omitempty"`
	GraphQLPermissions  []ContractGraphQLPermission `json:"graphqlPermissions,omitempty"`
	GraphQLEntrypoints  []ContractGraphQLEntrypoint `json:"graphqlEntrypoints,omitempty"`
	DataAccesses        []ContractDataAccess        `json:"dataAccesses,omitempty"`
	QueuesProduced      []ContractQueue             `json:"queuesProduced"`
	QueuesConsumed      []ContractQueue             `json:"queuesConsumed"`
	EventBridgeTriggers []ContractSchedule          `json:"eventBridgeTriggers,omitempty"`
	AzureTimerTriggers  []ContractSchedule          `json:"azureTimerTriggers,omitempty"`
}

type ContractsResponse struct {
	Workspace   ResponseWorkspace     `json:"workspace"`
	RepoContext []ResponseRepoContext `json:"repoContext,omitempty"`
	Services    []ContractSummary     `json:"services,omitempty"`
	Service     *ContractDetail       `json:"service,omitempty"`
}

func (h *Handlers) ListContracts(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, ok := parseLimitOnly(w, r, 200, 1000)
	if !ok {
		return
	}
	includeEmpty := parseBoolParam(r, "includeEmpty")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	snapshotIDs := activeSnapshotIDs(workspaceScope)
	includeLegacy := workspaceIncludesLegacy(workspaceScope)

	rows, err := h.storage.Pool().Query(ctx, `
		WITH service_counts AS (
			SELECT r.name,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM endpoints e
			         JOIN files f ON f.id = e.file_id
			         WHERE e.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as endpoint_count,
			       COALESCE((SELECT COUNT(*) FROM http_client_calls h WHERE h.repo_id = r.id AND ($3::bigint[] IS NULL OR h.snapshot_id = ANY($3))), 0) as http_call_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM graphql_operations go
			         JOIN files f ON f.id = go.file_id
			         WHERE go.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as graphql_operation_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM graphql_operation_usages gu
			         JOIN files f ON f.id = gu.file_id
			         WHERE gu.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as graphql_usage_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM graphql_operation_resolvers gr
			         JOIN files f ON f.id = gr.file_id
			         WHERE gr.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as graphql_resolver_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM graphql_operation_permissions gp
			         JOIN files f ON f.id = gp.file_id
			         WHERE gp.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as graphql_permission_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM graphql_backend_entrypoints ge
			         JOIN files f ON f.id = ge.file_id
			         WHERE ge.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) as graphql_entrypoint_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM data_accesses da
			         WHERE da.repo_id = r.id
			           AND ($3::bigint[] IS NULL OR da.snapshot_id = ANY($3))
			       ), 0) as data_access_count,
			       COALESCE((
			         SELECT COUNT(*)
			         FROM azure_function_triggers t
			         JOIN files f ON f.id = t.file_id
			         WHERE t.repo_id = r.id
			           AND LOWER(t.trigger_type) = 'timertrigger'
			           AND f.path NOT LIKE '.codebase-snapshots/%'
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3) OR ($5::boolean AND f.snapshot_id IS NULL))
			       ), 0) as azure_timer_trigger_count,
			       COALESCE((SELECT COUNT(*) FROM sqs_producers sp WHERE sp.repo_id = r.id AND ($3::bigint[] IS NULL OR sp.snapshot_id = ANY($3))), 0) as queue_produced,
			       COALESCE((SELECT COUNT(*) FROM sqs_consumers sc WHERE sc.repo_id = r.id AND ($3::bigint[] IS NULL OR sc.snapshot_id = ANY($3))), 0) +
			       COALESCE((
			         SELECT COUNT(*)
			         FROM azure_function_triggers t
			         JOIN files f ON f.id = t.file_id
			         WHERE t.repo_id = r.id
			           AND t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			           AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
			       ), 0) +
			       COALESCE((
			         SELECT COUNT(*)
			         FROM data_accesses da
			         WHERE da.repo_id = r.id
			           AND LOWER(da.access) = 'read'
			           AND LOWER(COALESCE(da.entity_name, '')) LIKE 'queue:%'
			           AND ($3::bigint[] IS NULL OR da.snapshot_id = ANY($3))
			       ), 0) as queue_consumed
			FROM repositories r
			WHERE ($1 = '' OR r.name ILIKE '%' || $1 || '%')
		)
		SELECT name, endpoint_count, http_call_count, graphql_operation_count, graphql_usage_count, graphql_resolver_count, graphql_permission_count, graphql_entrypoint_count, data_access_count, azure_timer_trigger_count, queue_produced, queue_consumed
		FROM service_counts
		WHERE $4::boolean
		   OR endpoint_count + http_call_count + graphql_operation_count + graphql_usage_count + graphql_resolver_count + graphql_permission_count + graphql_entrypoint_count + data_access_count + azure_timer_trigger_count + queue_produced + queue_consumed > 0
		ORDER BY name
		LIMIT $2
	`, graph.EscapeLike(query), limit, snapshotIDs, includeEmpty, includeLegacy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}
	defer rows.Close()

	var services []ContractSummary
	for rows.Next() {
		var row ContractSummary
		if err := rows.Scan(
			&row.Repo,
			&row.EndpointCount,
			&row.HttpCallCount,
			&row.GraphQLOperations,
			&row.GraphQLUsages,
			&row.GraphQLResolvers,
			&row.GraphQLPermissions,
			&row.GraphQLEntrypoints,
			&row.DataAccessCount,
			&row.AzureTimerTriggers,
			&row.QueuesProduced,
			&row.QueuesConsumed,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		services = append(services, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	writeJSON(w, http.StatusOK, ContractsResponse{Workspace: workspaceScope.Workspace, RepoContext: workspaceScope.RepoContext, Services: services})
}

func (h *Handlers) GetContract(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.PathValue("repo"))
	if repo == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing repo", nil)
		return
	}
	limit, ok := parseLimitOnly(w, r, 200, 2000)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}

	// The fetchers below key on the stored name, so canonicalize the path value
	// (case-insensitive) before using it.
	canonicalRepo, err := h.resolveRepoName(r.Context(), repo, workspaceScope)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	repo = canonicalRepo
	endpoints, err := h.fetchContractEndpoints(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "endpoints could not be loaded", nil)
		return
	}
	httpCalls, targets, err := h.fetchContractHttpCalls(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "HTTP targets could not be loaded", nil)
		return
	}
	callers, err := h.fetchHttpCallers(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "HTTP callers could not be loaded", nil)
		return
	}
	graphqlOperations, err := h.fetchContractGraphQLOperations(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlUsages, err := h.fetchContractGraphQLUsages(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlTargets, err := h.fetchContractGraphQLTargets(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlCallers, err := h.fetchContractGraphQLCallers(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlResolvers, err := h.fetchContractGraphQLResolvers(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlPermissions, err := h.fetchContractGraphQLPermissions(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	graphqlEntrypoints, err := h.fetchContractGraphQLEntrypoints(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	dataAccesses, err := h.fetchContractDataAccesses(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	queuesProduced, queuesConsumed, err := h.fetchContractQueues(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "queue relationships could not be loaded", nil)
		return
	}
	ebTriggers, err := h.fetchEventBridgeTriggers(ctx, repo, queuesConsumed)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}
	azureTimerTriggers, err := h.fetchAzureTimerTriggers(ctx, repo, limit, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CONTRACTS_INCOMPLETE", "contract evidence could not be loaded", nil)
		return
	}

	detail := &ContractDetail{
		Repo:               repo,
		Endpoints:          endpoints,
		HttpCalls:          httpCalls,
		HttpTargets:        targets,
		HttpCallers:        callers,
		GraphQLOperations:  graphqlOperations,
		GraphQLUsages:      graphqlUsages,
		GraphQLTargets:     graphqlTargets,
		GraphQLCallers:     graphqlCallers,
		GraphQLResolvers:   graphqlResolvers,
		GraphQLPermissions: graphqlPermissions,
		GraphQLEntrypoints: graphqlEntrypoints,
		DataAccesses:       dataAccesses,
		QueuesProduced:     queuesProduced,
		QueuesConsumed:     queuesConsumed,
	}
	if len(ebTriggers) > 0 {
		detail.EventBridgeTriggers = ebTriggers
	}
	if len(azureTimerTriggers) > 0 {
		detail.AzureTimerTriggers = azureTimerTriggers
	}

	resp := ContractsResponse{
		Workspace:   workspaceScope.Workspace,
		RepoContext: workspaceScope.RepoContext,
		Service:     detail,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) fetchContractEndpoints(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractEndpoint, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT e.method, e.path, COALESCE(fn.name, '') as handler, f.path, COALESCE(e.line_number, 0)
		FROM endpoints e
		JOIN files f ON e.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		LEFT JOIN functions fn ON e.handler_function_id = fn.id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY e.path, e.method
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []ContractEndpoint
	for rows.Next() {
		var endpoint ContractEndpoint
		if err := rows.Scan(&endpoint.Method, &endpoint.Path, &endpoint.Handler, &endpoint.File, &endpoint.Line); err != nil {
			return nil, err
		}
		if h.ownerResolver != nil {
			match := h.ownerResolver.Resolve(repo, endpoint.File)
			if len(match.Owners) > 0 {
				endpoint.Owners = match.Owners
			}
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}

func (h *Handlers) fetchContractHttpCalls(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractHttpCall, []ContractRepoCount, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT h.http_method, h.url_pattern, COUNT(*) as call_count,
		       COALESCE(MAX(h.client_type), '') as client_type
		FROM http_client_calls h
		JOIN repositories r ON h.repo_id = r.id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR h.snapshot_id = ANY($3))
		GROUP BY h.http_method, h.url_pattern
		ORDER BY call_count DESC, h.url_pattern
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	targetCounts := make(map[string]int)
	var calls []ContractHttpCall
	for rows.Next() {
		var call ContractHttpCall
		if err := rows.Scan(&call.Method, &call.Path, &call.Count, &call.ClientType); err != nil {
			return nil, nil, err
		}
		calls = append(calls, call)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i := range calls {
		call := &calls[i]
		matches, err := trace.MatchEndpointsChecked(ctx, h.storage.Pool(), call.Method, call.Path, repo, snapshotIDs, workspaceIncludesLegacy(scope))
		if err != nil {
			return nil, nil, err
		}

		if len(matches) == 0 {
			call.External = true
		} else {
			for _, match := range matches {
				call.Matches = append(call.Matches, ContractHttpMatch{
					Repo:    match.Repo,
					Handler: match.Handler,
					Path:    match.Path,
					File:    match.File,
					Line:    match.LineNumber,
				})
			}
			addContractHTTPMatchTargetCounts(targetCounts, repo, call.Count, matches)
		}
	}

	targets := mapToRepoCounts(targetCounts)
	return calls, targets, nil
}

func addContractHTTPMatchTargetCounts(counts map[string]int, sourceRepo string, callCount int, matches []trace.MatchedEndpoint) {
	countedTargetRepos := make(map[string]bool)
	for _, match := range matches {
		if match.Repo == "" || match.Repo == sourceRepo || countedTargetRepos[match.Repo] {
			continue
		}
		counts[match.Repo] += callCount
		countedTargetRepos[match.Repo] = true
	}
}

func (h *Handlers) fetchHttpCallers(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractRepoCount, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH target_endpoints AS MATERIALIZED (
			SELECT
			       upper(COALESCE(NULLIF(e.method_canonical, ''), e.method)) AS method,
			       lower(regexp_replace(regexp_replace(COALESCE(NULLIF(e.path_canonical, ''), e.path), '[?#].*$', ''), '/+', '/', 'g')) AS path
			FROM endpoints e
			JOIN repositories er ON er.id = e.repo_id
			LEFT JOIN files ef ON ef.id = e.file_id
			LEFT JOIN functions hfn ON hfn.id = e.handler_function_id
			LEFT JOIN files hf ON hf.id = hfn.file_id
			WHERE er.name = $1
			  AND trim(both '/' FROM COALESCE(NULLIF(e.path_canonical, ''), e.path)) <> ''
			  AND ($3::bigint[] IS NULL OR COALESCE(ef.snapshot_id, hf.snapshot_id) = ANY($3))
		),
		http_map AS MATERIALIZED (
			SELECT hr.name AS caller_repo,
			       h.id AS call_id,
			       h.http_method,
			       upper(COALESCE(NULLIF(h.http_method, ''), 'REQUEST')) AS method,
			       lower(regexp_replace(
			         regexp_replace(
			           regexp_replace(
			             regexp_replace(h.url_pattern, '^[A-Za-z][A-Za-z0-9+.-]*://[^/]*', ''),
			             '[?#].*$', ''
			           ),
			           '/+', '/', 'g'
			         ),
			         '^/((:[^/]+|[{][^/]+[}])/)+', '/'
			       )) AS path
			FROM http_client_calls h
			JOIN repositories hr ON h.repo_id = hr.id
			WHERE hr.name <> $1
			  AND length(h.url_pattern) > 3
			  AND ($3::bigint[] IS NULL OR h.snapshot_id = ANY($3))
		),
		matches AS (
			SELECT DISTINCT h.caller_repo, h.call_id
			FROM http_map h
			JOIN target_endpoints endpoint ON (endpoint.method = h.method OR endpoint.method = 'REQUEST' OR h.method IN ('REQUEST', 'ANY'))
			                          AND trim(both '/' FROM endpoint.path) <> ''
			                          AND trim(both '/' FROM h.path) <> ''
			                          AND EXISTS (
			                            SELECT 1
			                            FROM unnest(string_to_array(trim(both '/' FROM h.path), '/')) AS hseg(seg)
			                            WHERE hseg.seg <> ''
			                              AND hseg.seg <> '*'
			                              AND hseg.seg <> '**'
			                              AND left(hseg.seg, 1) <> ':'
			                              AND NOT (hseg.seg LIKE '{%' AND hseg.seg LIKE '%}')
			                          )
			                          AND cardinality(string_to_array(trim(both '/' FROM endpoint.path), '/')) = cardinality(string_to_array(trim(both '/' FROM h.path), '/'))
			                          AND NOT EXISTS (
			                            SELECT 1
			                            FROM generate_subscripts(string_to_array(trim(both '/' FROM endpoint.path), '/'), 1) AS idx(i)
			                            WHERE NOT (
			                              (string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] = (string_to_array(trim(both '/' FROM h.path), '/'))[i]
			                              OR left((string_to_array(trim(both '/' FROM endpoint.path), '/'))[i], 1) = ':'
			                              OR ((string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] LIKE '{%' AND (string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] LIKE '%}')
			                              OR left((string_to_array(trim(both '/' FROM h.path), '/'))[i], 1) = ':'
			                              OR ((string_to_array(trim(both '/' FROM h.path), '/'))[i] LIKE '{%' AND (string_to_array(trim(both '/' FROM h.path), '/'))[i] LIKE '%}')
			                            )
			                          )
		)
		SELECT caller_repo, COUNT(DISTINCT call_id) AS call_count
		FROM matches
		GROUP BY caller_repo
		ORDER BY call_count DESC, caller_repo
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractRepoCount
	for rows.Next() {
		var callerRepo string
		var count int
		if err := rows.Scan(&callerRepo, &count); err != nil {
			return nil, err
		}
		out = append(out, ContractRepoCount{Repo: callerRepo, Count: count})
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractGraphQLOperations(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractGraphQLOperation, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT o.operation_name, o.operation_type, f.path, COALESCE(o.line_number, 0)
		FROM graphql_operations o
		JOIN files f ON f.id = o.file_id
		JOIN repositories r ON r.id = o.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY o.operation_type, o.operation_name, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractGraphQLOperation
	for rows.Next() {
		var item ContractGraphQLOperation
		if err := rows.Scan(&item.Name, &item.Type, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractGraphQLUsages(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractGraphQLUsage, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT u.imported_as, u.import_path, u.caller_function, f.path, COALESCE(u.line_number, 0)
		FROM graphql_operation_usages u
		JOIN files f ON f.id = u.file_id
		JOIN repositories r ON r.id = u.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY u.imported_as, u.caller_function, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractGraphQLUsage
	for rows.Next() {
		var item ContractGraphQLUsage
		if err := rows.Scan(&item.ImportedAs, &item.ImportPath, &item.Caller, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractGraphQLTargets(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractRepoCount, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH source_operations AS (
			SELECT DISTINCT
			       u.id AS usage_id,
			       LOWER(COALESCE(o.operation_name, u.imported_as)) AS operation_name,
			       LOWER(COALESCE(o.operation_type, '')) AS operation_type
			FROM graphql_operation_usages u
			JOIN files uf ON uf.id = u.file_id
			JOIN repositories ur ON ur.id = u.repo_id
			LEFT JOIN graphql_usage_operation_links l ON l.usage_id = u.id
			LEFT JOIN graphql_operations o ON o.id = l.operation_id
			WHERE ur.name = $1
			  AND COALESCE(COALESCE(o.operation_name, u.imported_as), '') <> ''
			  AND ($3::bigint[] IS NULL OR uf.snapshot_id = ANY($3))
		),
		target_matches AS (
			SELECT DISTINCT so.usage_id, rr.name AS target_repo
			FROM source_operations so
			JOIN graphql_operation_resolvers gr ON LOWER(gr.operation_name) = so.operation_name
			 AND (so.operation_type = '' OR LOWER(gr.operation_type) = so.operation_type)
			JOIN files gf ON gf.id = gr.file_id
			JOIN repositories rr ON rr.id = gr.repo_id
			WHERE rr.name <> $1
			  AND ($3::bigint[] IS NULL OR gf.snapshot_id = ANY($3))
		)
		SELECT target_repo, COUNT(*) AS usage_count
		FROM target_matches
		GROUP BY target_repo
		ORDER BY usage_count DESC, target_repo
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var target string
		var count int
		if err := rows.Scan(&target, &count); err != nil {
			return nil, err
		}
		if target != "" {
			counts[target] = count
		}
	}
	return mapToRepoCounts(counts), rows.Err()
}

func (h *Handlers) fetchContractGraphQLCallers(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractRepoCount, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH repo_resolvers AS (
			SELECT DISTINCT
			       LOWER(gr.operation_name) AS operation_name,
			       LOWER(gr.operation_type) AS operation_type
			FROM graphql_operation_resolvers gr
			JOIN files gf ON gf.id = gr.file_id
			JOIN repositories rr ON rr.id = gr.repo_id
			WHERE rr.name = $1
			  AND COALESCE(gr.operation_name, '') <> ''
			  AND ($3::bigint[] IS NULL OR gf.snapshot_id = ANY($3))
		),
		caller_matches AS (
			SELECT DISTINCT u.id AS usage_id, ur.name AS caller_repo
			FROM graphql_operation_usages u
			JOIN files uf ON uf.id = u.file_id
			JOIN repositories ur ON ur.id = u.repo_id
			LEFT JOIN graphql_usage_operation_links l ON l.usage_id = u.id
			LEFT JOIN graphql_operations o ON o.id = l.operation_id
			JOIN repo_resolvers ro ON (
				LOWER(COALESCE(o.operation_name, u.imported_as)) = ro.operation_name
				AND (
					COALESCE(o.operation_type, '') = ''
					OR LOWER(o.operation_type) = ro.operation_type
				)
			)
			WHERE ur.name <> $1
			  AND ($3::bigint[] IS NULL OR uf.snapshot_id = ANY($3))
		)
		SELECT caller_repo, COUNT(*) AS usage_count
		FROM caller_matches
		GROUP BY caller_repo
		ORDER BY usage_count DESC, caller_repo
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var caller string
		var count int
		if err := rows.Scan(&caller, &count); err != nil {
			return nil, err
		}
		if caller != "" {
			counts[caller] = count
		}
	}
	return mapToRepoCounts(counts), rows.Err()
}

func (h *Handlers) fetchContractGraphQLResolvers(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractGraphQLResolver, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT gr.operation_name, gr.operation_type, gr.resolver_name, f.path, COALESCE(gr.line_number, 0)
		FROM graphql_operation_resolvers gr
		JOIN files f ON f.id = gr.file_id
		JOIN repositories r ON r.id = gr.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY gr.operation_type, gr.operation_name, gr.resolver_name, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractGraphQLResolver
	for rows.Next() {
		var item ContractGraphQLResolver
		if err := rows.Scan(&item.OperationName, &item.OperationType, &item.Resolver, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractGraphQLPermissions(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractGraphQLPermission, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT gp.operation_name, gp.operation_type, gp.rule_expression, f.path, COALESCE(gp.line_number, 0)
		FROM graphql_operation_permissions gp
		JOIN files f ON f.id = gp.file_id
		JOIN repositories r ON r.id = gp.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY gp.operation_type, gp.operation_name, gp.rule_expression, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractGraphQLPermission
	for rows.Next() {
		var item ContractGraphQLPermission
		if err := rows.Scan(&item.OperationName, &item.OperationType, &item.RuleExpression, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractGraphQLEntrypoints(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractGraphQLEntrypoint, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT ge.handler_name, ge.registration_kind, ge.controllers_path, f.path, COALESCE(ge.line_number, 0)
		FROM graphql_backend_entrypoints ge
		JOIN files f ON f.id = ge.file_id
		JOIN repositories r ON r.id = ge.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3))
		ORDER BY ge.registration_kind, ge.controllers_path, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractGraphQLEntrypoint
	for rows.Next() {
		var item ContractGraphQLEntrypoint
		if err := rows.Scan(&item.HandlerName, &item.RegistrationKind, &item.ControllersPath, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractDataAccesses(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractDataAccess, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT da.entity_name,
		       da.access,
		       split_part(da.caller_id, ':', 3) AS caller_name,
		       split_part(da.caller_id, ':', 2) AS file_path,
		       COALESCE(da.line_number, 0)
		FROM data_accesses da
		JOIN repositories r ON r.id = da.repo_id
		WHERE r.name = $1
		  AND ($3::bigint[] IS NULL OR da.snapshot_id = ANY($3))
		ORDER BY da.entity_name, da.access, file_path, caller_name
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractDataAccess
	for rows.Next() {
		var item ContractDataAccess
		if err := rows.Scan(&item.Entity, &item.Access, &item.Caller, &item.File, &item.Line); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchContractQueues(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractQueue, []ContractQueue, error) {
	produced, err := h.fetchQueuesByDirection(ctx, repo, limit, true, scope)
	if err != nil {
		return produced, nil, err
	}
	consumed, err := h.fetchQueuesByDirection(ctx, repo, limit, false, scope)
	return produced, consumed, err
}

func (h *Handlers) fetchQueuesByDirection(ctx context.Context, repo string, limit int, producer bool, scope searchWorkspaceScope) ([]ContractQueue, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	if !producer {
		rows, err := h.storage.Pool().Query(ctx, `
			SELECT queue_name, COUNT(*) as queue_count FROM (
				SELECT q.queue_name
				FROM sqs_consumers q
				JOIN repositories r ON q.repo_id = r.id
				WHERE r.name = $1
				  AND `+integrationSnapshotClause("q.snapshot_id", 3, workspaceIncludesLegacy(scope))+`

				UNION ALL

				SELECT COALESCE(t.resource_name, '') AS queue_name
				FROM azure_function_triggers t
				JOIN files f ON f.id = t.file_id
				JOIN repositories r ON r.id = t.repo_id
				WHERE r.name = $1
				  AND t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
				  AND COALESCE(t.resource_name, '') <> ''
				  AND `+integrationSnapshotClause("f.snapshot_id", 3, workspaceIncludesLegacy(scope))+`

				UNION ALL

				SELECT REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i') AS queue_name
				FROM data_accesses da
				JOIN repositories r ON da.repo_id = r.id
				WHERE r.name = $1
				  AND LOWER(da.access) = 'read'
				  AND LOWER(COALESCE(da.entity_name, '')) LIKE 'queue:%'
				  AND `+integrationSnapshotClause("da.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
			) q
			WHERE COALESCE(q.queue_name, '') <> ''
			GROUP BY q.queue_name
			ORDER BY q.queue_name
			LIMIT $2
		`, repo, limit, snapshotIDs)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var queues []ContractQueue
		for rows.Next() {
			var queue ContractQueue
			if err := rows.Scan(&queue.Name, &queue.Count); err != nil {
				return queues, err
			}
			queues = append(queues, queue)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return queues, err
		}
		for i := range queues {
			queues[i].Counterparties, err = h.fetchQueueCounterparties(ctx, repo, queues[i].Name, producer, scope)
			if err != nil {
				return queues, err
			}
		}
		return queues, nil
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT q.queue_name, COUNT(*) as queue_count
		FROM sqs_producers q
		JOIN repositories r ON q.repo_id = r.id
		WHERE r.name = $1
		  AND `+integrationSnapshotClause("q.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
		GROUP BY q.queue_name
		ORDER BY q.queue_name
		LIMIT $2
	`, repo, limit, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var queues []ContractQueue
	for rows.Next() {
		var queue ContractQueue
		if err := rows.Scan(&queue.Name, &queue.Count); err != nil {
			return queues, err
		}
		queues = append(queues, queue)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return queues, err
	}
	for i := range queues {
		queues[i].Counterparties, err = h.fetchQueueCounterparties(ctx, repo, queues[i].Name, producer, scope)
		if err != nil {
			return queues, err
		}
	}
	return queues, nil
}

func (h *Handlers) fetchQueueCounterparties(ctx context.Context, repo, queue string, producer bool, scope searchWorkspaceScope) ([]string, error) {
	if !producer {
		return h.fetchQueueProducerCounterparties(ctx, repo, queue, scope)
	}
	return h.fetchQueueConsumerCounterparties(ctx, repo, queue, scope)
}

func (h *Handlers) fetchQueueProducerCounterparties(ctx context.Context, repo, queue string, scope searchWorkspaceScope) ([]string, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases
			WHERE COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
			  AND `+integrationSnapshotClause("snapshot_id", 3, workspaceIncludesLegacy(scope))+`
		)
		SELECT DISTINCT r.name
		FROM sqs_producers q
		JOIN repositories r ON q.repo_id = r.id
		WHERE (
		    LOWER(TRIM(BOTH '%' FROM q.queue_name)) = LOWER(TRIM(BOTH '%' FROM $1))
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases qa
		      WHERE (
		        qa.alias_key = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        AND qa.alias_value = LOWER(TRIM(BOTH '%' FROM $1))
		      ) OR (
		        qa.alias_value = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        AND qa.alias_key = LOWER(TRIM(BOTH '%' FROM $1))
		      )
		    )
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases queue_alias
		      JOIN queue_aliases input_alias
		        ON queue_alias.alias_value = input_alias.alias_value
		      WHERE (
		        queue_alias.alias_key = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        OR queue_alias.alias_value = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		      )
		        AND (
		          input_alias.alias_key = LOWER(TRIM(BOTH '%' FROM $1))
		          OR input_alias.alias_value = LOWER(TRIM(BOTH '%' FROM $1))
		        )
		    )
		  )
		  AND r.name <> $2
		  AND `+integrationSnapshotClause("q.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
		ORDER BY r.name
	`, queue, repo, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return out, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchQueueConsumerCounterparties(ctx context.Context, repo, queue string, scope searchWorkspaceScope) ([]string, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH queue_aliases AS (
			SELECT DISTINCT
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_key, ''))) AS alias_key,
				LOWER(TRIM(BOTH '%' FROM COALESCE(alias_value, ''))) AS alias_value
			FROM resource_aliases
			WHERE COALESCE(alias_key, '') <> ''
			  AND COALESCE(alias_value, '') <> ''
			  AND `+integrationSnapshotClause("snapshot_id", 3, workspaceIncludesLegacy(scope))+`
		),
		queue_consumers AS (
			SELECT q.repo_id, q.queue_name
			FROM sqs_consumers q
			WHERE COALESCE(q.queue_name, '') <> ''
			  AND `+integrationSnapshotClause("q.snapshot_id", 3, workspaceIncludesLegacy(scope))+`

			UNION ALL

			SELECT t.repo_id, COALESCE(t.resource_name, '') AS queue_name
			FROM azure_function_triggers t
			JOIN files f ON f.id = t.file_id
			WHERE t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			  AND COALESCE(t.resource_name, '') <> ''
			  AND `+integrationSnapshotClause("f.snapshot_id", 3, workspaceIncludesLegacy(scope))+`

			UNION ALL

			SELECT da.repo_id, REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i') AS queue_name
			FROM data_accesses da
			WHERE LOWER(da.access) = 'read'
			  AND LOWER(COALESCE(da.entity_name, '')) LIKE 'queue:%'
			  AND `+integrationSnapshotClause("da.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
		)
		SELECT DISTINCT r.name
		FROM queue_consumers q
		JOIN repositories r ON q.repo_id = r.id
		WHERE (
		    LOWER(TRIM(BOTH '%' FROM q.queue_name)) = LOWER(TRIM(BOTH '%' FROM $1))
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases qa
		      WHERE (
		        qa.alias_key = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        AND qa.alias_value = LOWER(TRIM(BOTH '%' FROM $1))
		      ) OR (
		        qa.alias_value = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        AND qa.alias_key = LOWER(TRIM(BOTH '%' FROM $1))
		      )
		    )
		    OR EXISTS (
		      SELECT 1
		      FROM queue_aliases queue_alias
		      JOIN queue_aliases input_alias
		        ON queue_alias.alias_value = input_alias.alias_value
		      WHERE (
		        queue_alias.alias_key = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		        OR queue_alias.alias_value = LOWER(TRIM(BOTH '%' FROM q.queue_name))
		      )
		        AND (
		          input_alias.alias_key = LOWER(TRIM(BOTH '%' FROM $1))
		          OR input_alias.alias_value = LOWER(TRIM(BOTH '%' FROM $1))
		        )
		    )
		  )
		  AND r.name <> $2
		ORDER BY r.name
	`, queue, repo, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return out, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchEventBridgeTriggers(ctx context.Context, repo string, queuesConsumed []ContractQueue) ([]ContractSchedule, error) {
	if len(queuesConsumed) == 0 {
		return nil, nil
	}
	queueNames := make([]string, len(queuesConsumed))
	for i, q := range queuesConsumed {
		queueNames[i] = strings.ToLower(q.Name)
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT rule_name, schedule_expression, target_name, COALESCE(state, 'ENABLED')
		FROM eventbridge_schedules
		WHERE target_type = 'sqs' AND LOWER(target_name) = ANY($1)
		ORDER BY rule_name
	`, queueNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractSchedule
	for rows.Next() {
		var s ContractSchedule
		if err := rows.Scan(&s.RuleName, &s.ScheduleExpression, &s.TargetQueue, &s.State); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *Handlers) fetchAzureTimerTriggers(ctx context.Context, repo string, limit int, scope searchWorkspaceScope) ([]ContractSchedule, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	includeLegacy := workspaceIncludesLegacy(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT t.function_name,
		       COALESCE(t.schedule_expression, ''),
		       f.path,
		       COALESCE(t.line_number, 0)
		FROM azure_function_triggers t
		JOIN files f ON f.id = t.file_id
		JOIN repositories r ON r.id = t.repo_id
		WHERE r.name = $1
		  AND LOWER(t.trigger_type) = 'timertrigger'
		  AND f.path NOT LIKE '.codebase-snapshots/%'
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3) OR ($4::boolean AND f.snapshot_id IS NULL))
		ORDER BY t.function_name, f.path
		LIMIT $2
	`, repo, limit, snapshotIDs, includeLegacy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContractSchedule
	for rows.Next() {
		var s ContractSchedule
		if err := rows.Scan(&s.FunctionName, &s.ScheduleExpression, &s.File, &s.Line); err != nil {
			return nil, err
		}
		s.RuleName = s.FunctionName
		s.State = "ENABLED"
		s.Source = "azure_timer_trigger"
		out = append(out, s)
	}
	return out, rows.Err()
}

func mapToRepoCounts(counts map[string]int) []ContractRepoCount {
	out := make([]ContractRepoCount, 0, len(counts))
	for repo, count := range counts {
		out = append(out, ContractRepoCount{Repo: repo, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func parseBoolParam(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
