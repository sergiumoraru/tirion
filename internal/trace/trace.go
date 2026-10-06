package trace

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

type CallEdge struct {
	CallerID   string
	CalleeName string
	LineNumber int
}

type resolvedCallEdge struct {
	CalleeName           string
	LineNumber           int
	CalleeCallerID       string
	Source               string
	ResolutionSource     string
	ResolutionConfidence string
	UnresolvedReason     string
}

type pendingEdge struct {
	callerID   string
	calleeName string
	lineNum    int
}

const (
	edgeTypeCall        = "call"
	edgeTypeHTTP        = "http"
	edgeTypeSQS         = "sqs"
	edgeTypeEventBridge = "eventbridge"
	edgeTypeAzureTimer  = "azure_timer"
	edgeTypeResolve     = "resolve"
)

const (
	confidenceHigh   = "high"
	confidenceMedium = "medium"
	confidenceLow    = "low"
)

const traceLookupMatchLimit = 100

var (
	globalCallEdgeCache           = newLRU(10000, 5*time.Minute)
	globalCallCountCache          = newLRU(20000, 5*time.Minute)
	globalInterfaceImplsLRU       = newLRU(5000, 10*time.Minute)
	traceCallEdgesEnabled   int32 = 1
	traceInterfaceImplsFlag int32 = 1
)

type diLimits struct {
	maxDepth int
	maxImpls int
}

type TraceTuning struct {
	DiMaxDepth             int
	DiMaxImpls             int
	ResolveExpandMode      string
	ResolveExpandMaxCalls  int
	UpstreamMaxNameCount   int
	SnapshotIDs            []int64
	IncludeLegacySnapshots bool
}

func snapshotIDsFromCache(cache *traceCache) []int64 {
	if cache == nil {
		return nil
	}
	return cache.tuning.SnapshotIDs
}

func includeLegacySnapshotsFromCache(cache *traceCache) bool {
	return cache != nil && cache.tuning.IncludeLegacySnapshots
}

func hasSnapshotScope(cache *traceCache) bool {
	return snapshotIDsFromCache(cache) != nil
}

func traceSnapshotClause(column string, argNum int, includeLegacy bool) string {
	if includeLegacy {
		return fmt.Sprintf("($%d::bigint[] IS NULL OR %s = ANY($%d) OR %s IS NULL)", argNum, column, argNum, column)
	}
	return fmt.Sprintf("($%d::bigint[] IS NULL OR %s = ANY($%d))", argNum, column, argNum)
}

func NewTraceTuning(profile config.TraceProfile, maxDepth int) TraceTuning {
	mode := strings.ToLower(strings.TrimSpace(profile.ResolveExpandMode))
	if mode == "" {
		if profile.ResolveExpand != nil && *profile.ResolveExpand {
			mode = "full"
		} else {
			mode = "leaf"
		}
	}
	if mode != "leaf" && mode != "smart" && mode != "full" {
		mode = "leaf"
	}

	diMaxDepth := maxDepth
	if profile.DiMaxDepth != nil {
		if *profile.DiMaxDepth <= 0 {
			diMaxDepth = maxDepth
		} else if *profile.DiMaxDepth < maxDepth {
			diMaxDepth = *profile.DiMaxDepth
		}
	}

	diMaxImpls := 0
	if profile.DiMaxImpls != nil {
		diMaxImpls = *profile.DiMaxImpls
	}

	upstreamMaxNameCount := 0
	if profile.UpstreamMaxNameCount != nil {
		upstreamMaxNameCount = *profile.UpstreamMaxNameCount
	}

	resolveExpandMaxCalls := 0
	if profile.ResolveExpandMaxCalls != nil {
		resolveExpandMaxCalls = *profile.ResolveExpandMaxCalls
	}

	return TraceTuning{
		DiMaxDepth:            diMaxDepth,
		DiMaxImpls:            diMaxImpls,
		ResolveExpandMode:     mode,
		ResolveExpandMaxCalls: resolveExpandMaxCalls,
		UpstreamMaxNameCount:  upstreamMaxNameCount,
	}
}

func DefaultTraceTuning(maxDepth int) TraceTuning {
	cfg := config.GetEffectiveTraceConfig()
	profile, _ := cfg.ResolveProfile("")
	return NewTraceTuning(profile, maxDepth)
}

func getDILimits(tuning TraceTuning, maxDepth int) diLimits {
	depthLimit := tuning.DiMaxDepth
	if depthLimit <= 0 {
		depthLimit = maxDepth
	}
	if depthLimit > maxDepth {
		depthLimit = maxDepth
	}
	return diLimits{
		maxDepth: depthLimit,
		maxImpls: tuning.DiMaxImpls,
	}
}

func limitImplIDs(implIDs []string, repo string, maxImpls int) []string {
	if maxImpls <= 0 || len(implIDs) <= maxImpls {
		return implIDs
	}
	if repo == "" {
		return implIDs[:maxImpls]
	}
	sameRepo := make([]string, 0, len(implIDs))
	other := make([]string, 0, len(implIDs))
	for _, implID := range implIDs {
		if extractRepo(implID) == repo {
			sameRepo = append(sameRepo, implID)
		} else {
			other = append(other, implID)
		}
	}
	ordered := append(sameRepo, other...)
	if len(ordered) > maxImpls {
		return ordered[:maxImpls]
	}
	return ordered
}

func looksLikeTypeName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

const (
	edgeSourceFunctionCalls = "function_calls"
	edgeSourcePendingCalls  = "pending_calls"
)

const (
	evidenceSourceHttpClient     = "http_client"
	evidenceSourceHttpEndpoint   = "http_endpoint"
	evidenceSourceHttpInterface  = "http_interface"
	evidenceSourceSqsProducer    = "sqs_producer"
	evidenceSourceSqsConsumer    = "sqs_consumer"
	evidenceSourceInterfaceImpl  = "interface_impl"
	evidenceSourceInjectedField  = "injected_field"
	evidenceSourceImplementation = "implementation_lookup"
	evidenceSourceFunctionCaller = "function_callers"
	evidenceSourcePendingCaller  = "pending_callers"
	evidenceSourceEventBridge    = "eventbridge_schedule"
	evidenceSourceAzureTimer     = "azure_timer_trigger"
	evidenceSourceGatewayRoute   = "gateway_route"
)

type HttpCall struct {
	HttpMethod string
	UrlPattern string
	LineNumber int
	ClientType string
}

type SqsProducerCall struct {
	QueueName  string
	LineNumber int
}

type SqsConsumer struct {
	ConsumerID    string
	QueueName     string
	HandlerMethod string
	Repo          string
	File          string
	Class         string
	TriggerType   string
}

type MatchedEndpoint struct {
	Repo       string
	File       string
	Handler    string
	Path       string
	Method     string
	LineNumber int
}

type MatchedGatewayRoute struct {
	Repo          string
	File          string
	GatewayType   string
	APIName       string
	OperationName string
	PublicMethod  string
	PublicPath    string
	BackendMethod string
	BackendURL    string
	BackendPath   string
	BackendID     string
	LineNumber    int
}

type MatchedFunction struct {
	Repo        string `json:"repo"`
	File        string `json:"file"`
	Name        string `json:"name"`
	QualifiedID string `json:"qualified_id"`
}

type EdgeEvidence struct {
	Source string `json:"source,omitempty"`
	Detail string `json:"detail,omitempty"`
	Repo   string `json:"repo,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// InterfaceImpl represents a class that implements an interface
type InterfaceImpl struct {
	ClassName string
	ClassID   int64
	Repo      string
	File      string
}

// HttpInterfaceMethod represents a Retrofit/Feign-style HTTP method
type HttpInterfaceMethod struct {
	Repo          string
	File          string
	InterfaceName string
	MethodName    string
	HttpMethod    string
	UrlPattern    string
	LineNumber    int
}

type resolveStats struct {
	interfaceRequests      int
	interfaceBatchRequests int
	interfaceCacheHits     int
	interfaceResolvedEdges int
	interfaceBatchResolved int
	interfaceDuration      time.Duration
	interfaceBatchDuration time.Duration
	injectedRequests       int
	injectedBatchRequests  int
	injectedCacheHits      int
	injectedResolvedEdges  int
	injectedBatchResolved  int
	injectedDuration       time.Duration
	injectedBatchDuration  time.Duration
}

type traceCache struct {
	httpCalls              map[string][]HttpCall
	endpointMatches        map[string][]MatchedEndpoint
	gatewayMatches         map[string][]MatchedGatewayRoute
	sqsProducers           map[string][]SqsProducerCall
	sqsConsumers           map[string][]SqsConsumer
	httpInterfaceByFull    map[string][]HttpInterfaceMethod
	httpInterfaceByName    map[string][]HttpInterfaceMethod
	interfaceImpls         map[string][]InterfaceImpl
	fieldInfo              map[string]InjectedField
	injectedFieldNames     map[string]map[string]bool
	ctorParams             map[string][]InjectedField
	callerIDByRepoFileFunc map[string]string
	uniqueCallerIDByName   map[string]string
	callerIDByFunctionID   map[int64]string
	functionIDByCallerID   map[string]int64
	implementationByName   map[string]string
	injectedFieldCalls     map[string][]string
	interfaceMethodCalls   map[string][]string
	qualifierCache         map[string]bool
	nameBasedRulesByRepo   map[string]nameBasedRules
	functionNameCounts     map[string]int
	functionFileCounts     map[string]int
	callEdgeCounts         map[string]int
	resolvedCallEdges      map[string][]resolvedCallEdge
	classByName            map[string][]InterfaceImpl
	primaryClass           map[int64]bool
	primaryBeanByClass     map[string]bool
	httpPrefixesByRepo     map[string][]string
	sqsPrefixesByRepo      map[string][]string
	resolveStats           *resolveStats
	tuning                 TraceTuning
}

func newTraceCache(pool Queryer, tuning TraceTuning) *traceCache {
	fullIndex, nameIndex, err := loadHttpInterfaceIndex(pool, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)
	if err != nil {
		log.Printf("trace HTTP interface index incomplete: %v", err)
	}
	return &traceCache{
		httpCalls:              make(map[string][]HttpCall),
		endpointMatches:        make(map[string][]MatchedEndpoint),
		gatewayMatches:         make(map[string][]MatchedGatewayRoute),
		sqsProducers:           make(map[string][]SqsProducerCall),
		sqsConsumers:           make(map[string][]SqsConsumer),
		httpInterfaceByFull:    fullIndex,
		httpInterfaceByName:    nameIndex,
		interfaceImpls:         make(map[string][]InterfaceImpl),
		fieldInfo:              make(map[string]InjectedField),
		injectedFieldNames:     make(map[string]map[string]bool),
		ctorParams:             make(map[string][]InjectedField),
		callerIDByRepoFileFunc: make(map[string]string),
		uniqueCallerIDByName:   make(map[string]string),
		callerIDByFunctionID:   make(map[int64]string),
		functionIDByCallerID:   make(map[string]int64),
		implementationByName:   make(map[string]string),
		injectedFieldCalls:     make(map[string][]string),
		interfaceMethodCalls:   make(map[string][]string),
		qualifierCache:         make(map[string]bool),
		nameBasedRulesByRepo:   make(map[string]nameBasedRules),
		functionNameCounts:     make(map[string]int),
		functionFileCounts:     make(map[string]int),
		callEdgeCounts:         make(map[string]int),
		resolvedCallEdges:      make(map[string][]resolvedCallEdge),
		classByName:            make(map[string][]InterfaceImpl),
		primaryClass:           make(map[int64]bool),
		primaryBeanByClass:     make(map[string]bool),
		httpPrefixesByRepo:     make(map[string][]string),
		sqsPrefixesByRepo:      make(map[string][]string),
		resolveStats:           &resolveStats{},
		tuning:                 tuning,
	}
}

func loadHttpInterfaceIndex(pool Queryer, snapshotIDs []int64, includeLegacy bool) (map[string][]HttpInterfaceMethod, map[string][]HttpInterfaceMethod, error) {
	query := fmt.Sprintf(`
		SELECT r.name, f.path, i.name, h.method_name, h.http_method, h.url_pattern, COALESCE(h.line_number, 0)
		FROM http_interface_methods h
		JOIN interfaces i ON h.interface_id = i.id
		JOIN files f ON i.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE %s
		ORDER BY f.repo_id, f.path, i.name, h.method_name, h.line_number, h.id`,
		traceSnapshotClause("f.snapshot_id", 1, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, snapshotIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	fullIndex := make(map[string][]HttpInterfaceMethod)
	nameIndex := make(map[string][]HttpInterfaceMethod)
	for rows.Next() {
		var method HttpInterfaceMethod
		if err := rows.Scan(&method.Repo, &method.File, &method.InterfaceName, &method.MethodName, &method.HttpMethod, &method.UrlPattern, &method.LineNumber); err != nil {
			return nil, nil, err
		}
		fullKey := strings.ToLower(method.InterfaceName + "." + method.MethodName)
		nameKey := strings.ToLower(method.MethodName)
		fullIndex[fullKey] = append(fullIndex[fullKey], method)
		nameIndex[nameKey] = append(nameIndex[nameKey], method)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return fullIndex, nameIndex, nil
}

func cacheKey(parts ...string) string {
	return strings.ToLower(strings.Join(parts, "|"))
}

func traceEdgesEnabled() bool {
	return atomic.LoadInt32(&traceCallEdgesEnabled) == 1
}

func disableTraceEdges() {
	atomic.StoreInt32(&traceCallEdgesEnabled, 0)
}

func traceInterfaceImplsEnabled() bool {
	return atomic.LoadInt32(&traceInterfaceImplsFlag) == 1
}

func disableTraceInterfaceImpls() {
	atomic.StoreInt32(&traceInterfaceImplsFlag, 0)
}

func isMissingRelation(err error, table string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "relation") && strings.Contains(msg, table) && strings.Contains(msg, "does not exist")
}

func SpinWithMessage(stop <-chan struct{}, message string) {
	frames := []string{"|", "/", "-", "\\"}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-stop:
			fmt.Fprint(os.Stderr, "\r")
			return
		case <-ticker.C:
			fmt.Fprintf(os.Stderr, "\r%s... %s", message, frames[i%len(frames)])
			i++
		}
	}
}

type traceFilters struct {
	noTests         bool
	excludePatterns []string
	includeRepos    map[string]bool
	excludeRepos    map[string]bool
}

func buildFilters(noTests bool, excludePatterns, includeRepos, excludeRepos string) *traceFilters {
	patterns := parseList(excludePatterns)

	return &traceFilters{
		noTests:         noTests,
		excludePatterns: normalizePatterns(patterns),
		includeRepos:    parseSet(includeRepos),
		excludeRepos:    parseSet(excludeRepos),
	}
}

func parseList(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func parseSet(input string) map[string]bool {
	items := parseList(input)
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[strings.ToLower(item)] = true
	}
	return set
}

func normalizePatterns(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, strings.ToLower(item))
	}
	return out
}

func filterTree(nodes []*TreeNode, filters *traceFilters) []*TreeNode {
	if filters == nil {
		return nodes
	}
	var out []*TreeNode
	for _, node := range nodes {
		if shouldExcludeNode(node, filters) {
			continue
		}
		node.Children = filterTree(node.Children, filters)
		out = append(out, node)
	}
	return out
}

func shouldExcludeNode(node *TreeNode, filters *traceFilters) bool {
	if node == nil {
		return true
	}
	if len(filters.includeRepos) > 0 && node.Repo != "" && !filters.includeRepos[strings.ToLower(node.Repo)] {
		return true
	}
	if len(filters.excludeRepos) > 0 && node.Repo != "" && filters.excludeRepos[strings.ToLower(node.Repo)] {
		return true
	}
	if filters.noTests && sourcepath.IsTest(node.File) {
		return true
	}
	if len(filters.excludePatterns) == 0 {
		return false
	}

	name := strings.ToLower(node.Name)
	file := strings.ToLower(node.File)
	for _, pattern := range filters.excludePatterns {
		if pattern == "" {
			continue
		}
		if (name != "" && strings.Contains(name, pattern)) || (file != "" && strings.Contains(file, pattern)) {
			return true
		}
	}
	return false
}

func findCallerIDs(pool Queryer, funcName string) []string {
	if funcName == "" {
		return nil
	}
	if strings.Count(funcName, ":") >= 2 {
		return []string{funcName}
	}
	if ids := findCallerIDsByEndpointStartForSnapshotFilter(pool, funcName, nil, false); len(ids) > 0 {
		return ids
	}

	if ids := findCallerIDsByGraphQLOperationNameForSnapshotFilter(pool, funcName, nil, false); len(ids) > 0 {
		return ids
	}
	if ids := findCallerIDsByAzureTriggerNameForSnapshotFilter(pool, funcName, nil, false); len(ids) > 0 {
		return ids
	}
	if ids := findCallerIDsByFunctionName(pool, funcName); len(ids) > 0 {
		return ids
	}

	clause, args := callerIDFunctionClause(funcName, 1)
	query := fmt.Sprintf(`
		SELECT DISTINCT caller_id FROM (
			SELECT caller_id FROM pending_calls_edges WHERE %s
			UNION
			SELECT caller_id FROM http_client_calls WHERE %s
			UNION
			SELECT caller_id FROM sqs_producers WHERE %s
		) combined
		LIMIT %d`, clause, clause, clause, traceLookupMatchLimit)
	rows, err := pool.Query(queryContext(pool), query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func findCallerIDsByGraphQLOperationName(pool Queryer, operationName string) []string {
	return findCallerIDsByGraphQLOperationNameForSnapshotFilter(pool, operationName, nil, false)
}

func findCallerIDsByGraphQLOperationNameForSnapshotFilter(pool Queryer, operationName string, snapshotIDs []int64, includeLegacy bool) []string {
	operationName = strings.TrimSpace(operationName)
	if operationName == "" {
		return nil
	}
	query := fmt.Sprintf(`
		SELECT DISTINCT repo, file_path, function_name, line_number, rank FROM (
			SELECT r.name AS repo,
			       f.path AS file_path,
			       gr.resolver_name AS function_name,
			       COALESCE(gr.line_number, 0) AS line_number,
			       0 AS rank
			FROM graphql_operation_resolvers gr
			JOIN files f ON f.id = gr.file_id
			JOIN repositories r ON r.id = gr.repo_id
			WHERE LOWER(gr.operation_name) = LOWER($1)
			  AND f.path NOT LIKE '.codebase-snapshots/%%'
			  AND %s

			UNION ALL

			SELECT r.name AS repo,
			       uf.path AS file_path,
			       u.caller_function AS function_name,
			       COALESCE(u.line_number, 0) AS line_number,
			       1 AS rank
			FROM graphql_operation_usages u
			JOIN files uf ON uf.id = u.file_id
			JOIN repositories r ON r.id = u.repo_id
			LEFT JOIN graphql_usage_operation_links l ON l.usage_id = u.id
			LEFT JOIN graphql_operations o ON o.id = l.operation_id
			WHERE (
				LOWER(o.operation_name) = LOWER($1)
				OR LOWER(u.imported_as) = LOWER($1)
				OR LOWER(u.import_path) = LOWER('__graphql_operation__:' || $1)
				OR LOWER(regexp_replace(split_part(reverse(split_part(reverse(u.import_path), '/', 1)), '.', 1), '(Query|Mutation|Subscription)$', '', 'i')) = LOWER($1)
			)
			  AND uf.path NOT LIKE '.codebase-snapshots/%%'
			  AND %s
		) matches
		ORDER BY rank, repo, file_path, function_name, line_number
		LIMIT %d`,
		traceSnapshotClause("f.snapshot_id", 2, includeLegacy),
		traceSnapshotClause("uf.snapshot_id", 2, includeLegacy),
		traceLookupMatchLimit)
	rows, err := pool.Query(queryContext(pool), query, operationName, snapshotIDs)
	if err != nil {
		if isMissingRelation(err, "graphql_operation_resolvers") {
			return nil
		}
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var repo, file, resolver string
		var line int
		var rank int
		if err := rows.Scan(&repo, &file, &resolver, &line, &rank); err != nil {
			continue
		}
		if repo == "" || file == "" || resolver == "" {
			continue
		}
		ids = append(ids, BuildCallerID(repo, file, resolver))
	}
	return ids
}

func findCallerIDsByAzureTriggerNameForSnapshotFilter(pool Queryer, functionName string, snapshotIDs []int64, includeLegacy bool) []string {
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return nil
	}
	query := fmt.Sprintf(`
		SELECT r.name, f.path, t.function_name, t.trigger_type, COALESCE(t.script_file, '')
		FROM azure_function_triggers t
		JOIN files f ON f.id = t.file_id
		JOIN repositories r ON r.id = t.repo_id
		WHERE (
			LOWER(t.function_name) = LOWER($1)
			OR LOWER(COALESCE(t.route, '')) = LOWER($1)
			OR LOWER(TRIM(BOTH '%%' FROM COALESCE(t.resource_name, ''))) = LOWER($1)
		)
		  AND f.path NOT LIKE '.codebase-snapshots/%%'
		  AND %s
		ORDER BY r.name, f.path, t.function_name
		LIMIT %d`, traceSnapshotClause("f.snapshot_id", 2, includeLegacy), traceLookupMatchLimit)
	rows, err := pool.Query(queryContext(pool), query, functionName, snapshotIDs)
	if err != nil {
		if isMissingRelation(err, "azure_function_triggers") {
			return nil
		}
		return nil
	}
	defer rows.Close()

	type triggerSource struct {
		repo, file, name, kind, scriptFile string
	}
	var triggers []triggerSource
	for rows.Next() {
		var trigger triggerSource
		if err := rows.Scan(&trigger.repo, &trigger.file, &trigger.name, &trigger.kind, &trigger.scriptFile); err != nil {
			return nil
		}
		triggers = append(triggers, trigger)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, trigger := range triggers {
		for _, sourceFile := range azureTriggerSourcePathCandidates(trigger.file, trigger.scriptFile) {
			for _, handler := range azureTriggerHandlerNameCandidates(trigger.kind, trigger.name) {
				callerID := findCallerIDByRepoFileFuncForSnapshotFilter(pool, trigger.repo, sourceFile, handler, snapshotIDs, includeLegacy)
				if callerID == "" || seen[callerID] {
					continue
				}
				seen[callerID] = true
				ids = append(ids, callerID)
			}
		}
	}
	return ids
}

func findCallerIDsByEndpointStartForSnapshotFilter(pool Queryer, start string, snapshotIDs []int64, includeLegacy bool) []string {
	method, path, ok := parseEndpointStartForTraceLookup(start)
	if !ok {
		return nil
	}
	endpoints := matchEndpoint(pool, method, path, nil, snapshotIDs, includeLegacy)
	if len(endpoints) == 0 {
		return findCallerIDsByGatewayStartForSnapshotFilter(pool, method, path, snapshotIDs, includeLegacy)
	}
	seen := make(map[string]bool, len(endpoints))
	var ids []string
	for _, ep := range endpoints {
		id := buildCallerID(ep.Repo, ep.File, ep.Handler)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func findCallerIDsByGatewayStartForSnapshotFilter(pool Queryer, method, path string, snapshotIDs []int64, includeLegacy bool) []string {
	routes := matchGatewayRoutes(pool, method, path, nil, snapshotIDs, includeLegacy)
	if len(routes) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, route := range routes {
		for _, candidate := range gatewayBackendRouteCandidates(route) {
			endpoints := matchEndpoint(pool, firstNonEmpty(route.BackendMethod, route.PublicMethod), candidate, nil, snapshotIDs, includeLegacy)
			for _, ep := range endpoints {
				id := buildCallerID(ep.Repo, ep.File, ep.Handler)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func parseEndpointStartForTraceLookup(start string) (method string, path string, ok bool) {
	trimmed := strings.TrimSpace(start)
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Fields(trimmed)
	if len(parts) >= 2 {
		methodCandidate := strings.ToUpper(parts[0])
		switch methodCandidate {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD", "REQUEST", "ANY":
			pathCandidate := strings.TrimSpace(parts[1])
			if traceLookupPathLike(pathCandidate) {
				return methodCandidate, pathCandidate, true
			}
		}
	}
	if traceLookupPathLike(trimmed) {
		return "REQUEST", trimmed, true
	}
	return "", "", false
}

func traceLookupPathLike(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return true
	}
	if strings.Contains(value, "://") {
		return true
	}
	return strings.Contains(value, "/") && !strings.Contains(value, " ")
}

type TreeNode struct {
	Name             string        `json:"name"`
	File             string        `json:"file,omitempty"`
	Repo             string        `json:"repo,omitempty"`
	Line             int           `json:"line,omitempty"`
	Children         []*TreeNode   `json:"children,omitempty"`
	Depth            int           `json:"depth,omitempty"`
	EdgeType         string        `json:"edge_type,omitempty"`
	Confidence       string        `json:"confidence,omitempty"`
	CallerID         string        `json:"caller_id,omitempty"`
	IsCrossService   bool          `json:"is_cross_service,omitempty"`
	HttpMethod       string        `json:"http_method,omitempty"`
	HttpTarget       string        `json:"http_target,omitempty"`
	IsSqs            bool          `json:"is_sqs,omitempty"`
	QueueTarget      string        `json:"queue_target,omitempty"`
	Injected         bool          `json:"injected,omitempty"`
	LowSignal        bool          `json:"low_signal,omitempty"`
	UnresolvedReason string        `json:"unresolved_reason,omitempty"`
	Evidence         *EdgeEvidence `json:"evidence,omitempty"`
	Source           string        `json:"source,omitempty"`
}

type nodeLimiter struct {
	max   int
	count int
	hit   bool
}

type LimitStats struct {
	ReturnedNodes int  `json:"returnedNodes"`
	Truncated     bool `json:"truncated"`
}

func newNodeLimiter(max int) *nodeLimiter {
	if max <= 0 || max > MaxNodes {
		max = MaxNodes
	}
	return &nodeLimiter{max: max}
}

func (l *nodeLimiter) allow() bool {
	if l == nil || l.max <= 0 {
		return true
	}
	if l.count >= l.max {
		l.hit = true
		return false
	}
	l.count++
	return true
}

// markIncomplete records that results were cut short by something other than the
// node budget (a per-query fan-out cap or a failed query), so completeness
// reporting does not claim the listing is exhaustive.
func (l *nodeLimiter) markIncomplete() {
	if l != nil {
		l.hit = true
	}
}

func (l *nodeLimiter) stats() LimitStats {
	if l == nil {
		return LimitStats{}
	}
	return LimitStats{
		ReturnedNodes: l.count,
		Truncated:     l.hit,
	}
}

// FindCallerIDs returns canonical caller IDs for a function name or qualified ID.
func FindCallerIDs(pool Queryer, funcName string) []string {
	return findCallerIDs(pool, funcName)
}

// FindCallerIDsForSnapshots returns canonical caller IDs scoped to active
// snapshots. includeLegacy allows default-main callers without snapshot
// provenance during legacy migrations.
func FindCallerIDsForSnapshots(pool Queryer, funcName string, snapshotIDs []int64, includeLegacy bool) []string {
	if funcName == "" {
		return nil
	}
	if strings.Count(funcName, ":") >= 2 {
		return []string{funcName}
	}
	if ids := findCallerIDsByEndpointStartForSnapshotFilter(pool, funcName, snapshotIDs, includeLegacy); len(ids) > 0 {
		return ids
	}
	if ids := findCallerIDsByGraphQLOperationNameForSnapshotFilter(pool, funcName, snapshotIDs, includeLegacy); len(ids) > 0 {
		return ids
	}
	if ids := findCallerIDsByAzureTriggerNameForSnapshotFilter(pool, funcName, snapshotIDs, includeLegacy); len(ids) > 0 {
		return ids
	}
	if ids := findCallerIDsByFunctionNameForSnapshotFilter(pool, funcName, snapshotIDs, includeLegacy); len(ids) > 0 {
		return ids
	}
	return nil
}

// DescribeCallerIDs converts caller IDs into human-friendly matches.
func DescribeCallerIDs(ids []string) []MatchedFunction {
	var matches []MatchedFunction
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			matches = append(matches, MatchedFunction{
				Repo:        parts[0],
				File:        parts[1],
				Name:        parts[2],
				QualifiedID: id,
			})
		}
	}
	return matches
}

// TraceDownstream builds downstream call trees.
func TraceDownstream(pool Queryer, callerIDs []string, maxDepth int) []*TreeNode {
	tuning := DefaultTraceTuning(maxDepth)
	roots, _ := traceDownstream(pool, callerIDs, maxDepth, 0, true, tuning)
	return roots
}

// TraceDownstreamWithLimit builds downstream call trees with a hard node limit.
func TraceDownstreamWithLimit(pool Queryer, callerIDs []string, maxDepth, maxNodes int) []*TreeNode {
	tuning := DefaultTraceTuning(maxDepth)
	roots, _ := traceDownstream(pool, callerIDs, maxDepth, maxNodes, true, tuning)
	return roots
}

// TraceDownstreamWithLimitAndResolve builds downstream call trees with a hard node limit and optional resolution.
func TraceDownstreamWithLimitAndResolve(pool Queryer, callerIDs []string, maxDepth, maxNodes int, resolve bool) []*TreeNode {
	tuning := DefaultTraceTuning(maxDepth)
	roots, _ := traceDownstream(pool, callerIDs, maxDepth, maxNodes, resolve, tuning)
	return roots
}

// TraceDownstreamWithLimitAndResolveTuning builds downstream call trees with explicit tuning.
func TraceDownstreamWithLimitAndResolveTuning(pool Queryer, callerIDs []string, maxDepth, maxNodes int, resolve bool, tuning TraceTuning) []*TreeNode {
	roots, _ := traceDownstream(pool, callerIDs, maxDepth, maxNodes, resolve, tuning)
	return roots
}

func TraceDownstreamWithLimitAndResolveTuningStats(pool Queryer, callerIDs []string, maxDepth, maxNodes int, resolve bool, tuning TraceTuning) ([]*TreeNode, LimitStats) {
	return traceDownstream(pool, callerIDs, maxDepth, maxNodes, resolve, tuning)
}

// TraceDownstreamChildren returns the immediate children for a caller at a given depth.
func TraceDownstreamChildren(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, resolve bool) []*TreeNode {
	tuning := DefaultTraceTuning(maxDepth)
	children, _ := TraceDownstreamChildrenTuningStats(pool, callerID, parentDepth, maxDepth, maxNodes, resolve, tuning)
	return children
}

// TraceDownstreamChildrenTuning returns the immediate children for a caller at a given depth with explicit tuning.
func TraceDownstreamChildrenTuning(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, resolve bool, tuning TraceTuning) []*TreeNode {
	children, _ := TraceDownstreamChildrenTuningStats(pool, callerID, parentDepth, maxDepth, maxNodes, resolve, tuning)
	return children
}

func TraceDownstreamChildrenTuningStats(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, resolve bool, tuning TraceTuning) (children []*TreeNode, stats LimitStats) {
	maxDepth = min(maxDepth, MaxDepth)
	if queryContext(pool).Err() != nil {
		return
	}
	if parentDepth < 0 || parentDepth >= MaxDepth {
		return
	}
	if callerID == "" || parentDepth >= maxDepth {
		return nil, stats
	}
	if maxNodes <= 0 {
		maxNodes = 2000
	}
	defer func() {
		enforceEdgeEvidenceRequirements(children)
	}()
	limiter := newNodeLimiter(maxNodes)
	defer func() {
		stats = limiter.stats()
	}()
	cache := newTraceCache(pool, tuning)
	di := getDILimits(tuning, maxDepth)
	visited := make(map[string]bool)
	depth := parentDepth

	if depth+1 > maxDepth {
		return nil, stats
	}

	childSeen := make(map[string]bool)

	// HTTP client calls (cross-service)
	httpCalls := findHttpCallsCached(cache, pool, callerID)
	for _, httpCall := range httpCalls {
		if !limiter.allow() {
			return
		}
		httpNode := &TreeNode{
			Name:           fmt.Sprintf("[%s %s]", httpCall.HttpMethod, httpCall.UrlPattern),
			Line:           httpCall.LineNumber,
			Depth:          depth + 1,
			EdgeType:       edgeTypeHTTP,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			HttpMethod:     httpCall.HttpMethod,
			HttpTarget:     httpCall.UrlPattern,
			Evidence:       newEvidence(evidenceSourceHttpClient, ""),
		}

		if hitLimit := attachHTTPResolutionTargets(pool, cache, callerID, httpNode, depth, maxDepth, limiter); hitLimit {
			return
		}

		if len(httpNode.Children) == 0 {
			httpNode.Name = fmt.Sprintf("[%s %s] (no matching endpoint or gateway)", httpCall.HttpMethod, httpCall.UrlPattern)
		}
		children = appendUniqueNode(children, httpNode, childSeen)
	}

	// SQS producer calls
	sqsCalls := findSqsProducerCallsCached(cache, pool, callerID)
	for _, sqsCall := range sqsCalls {
		if !limiter.allow() {
			return
		}
		sqsNode := &TreeNode{
			Name:           fmt.Sprintf("[SQS → %s]", sqsCall.QueueName),
			Line:           sqsCall.LineNumber,
			Depth:          depth + 1,
			EdgeType:       edgeTypeSQS,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			IsSqs:          true,
			QueueTarget:    sqsCall.QueueName,
			Evidence:       newEvidence(evidenceSourceSqsProducer, ""),
		}

		if depth+2 <= maxDepth {
			consumerSeen := make(map[string]bool)
			consumers := findSqsConsumersCached(cache, pool, sqsCall.QueueName, extractRepo(callerID))
			for _, consumer := range consumers {
				if !limiter.allow() {
					return
				}
				consumerNode := &TreeNode{
					Name:       formatQueueConsumerNodeName(consumer),
					File:       consumer.File,
					Repo:       consumer.Repo,
					Depth:      depth + 2,
					EdgeType:   edgeTypeSQS,
					Confidence: confidenceMedium,
					Evidence:   newEvidence(evidenceSourceSqsConsumer, ""),
				}

				handlerID := findConsumerHandler(pool, cache, consumer)
				if handlerID != "" && !visited[handlerID] {
					consumerNode.CallerID = handlerID
					visited[handlerID] = true
				}

				sqsNode.Children = appendUniqueNode(sqsNode.Children, consumerNode, consumerSeen)
			}
		}

		if len(sqsNode.Children) == 0 {
			sqsNode.Name = fmt.Sprintf("[SQS → %s] (no consumer found)", sqsCall.QueueName)
		}
		children = appendUniqueNode(children, sqsNode, childSeen)
	}

	edges := getResolvedCallEdges(pool, cache, callerID)
	for _, edge := range edges {
		calleeName := edge.CalleeName
		lineNum := edge.LineNumber
		calleeCallerID := edge.CalleeCallerID

		if calleeCallerID == "" && isGenericMethod(calleeName) {
			continue
		}
		if calleeCallerID == "" && visited[calleeName] {
			continue
		}

		if !limiter.allow() {
			return
		}

		if calleeCallerID != "" {
			if visited[calleeCallerID] {
				continue
			}
			visited[calleeCallerID] = true
			child := &TreeNode{
				Name:             calleeName,
				File:             extractFile(calleeCallerID),
				Repo:             extractRepo(calleeCallerID),
				Line:             lineNum,
				Depth:            depth + 1,
				EdgeType:         edgeTypeCall,
				Confidence:       callConfidence(edge),
				CallerID:         calleeCallerID,
				UnresolvedReason: edge.UnresolvedReason,
				Evidence:         evidenceForCallEdge(edge),
			}
			children = appendUniqueNode(children, child, childSeen)
			continue
		}

		visited[calleeName] = true
		if !resolve {
			child := &TreeNode{
				Name:             calleeName,
				Line:             lineNum,
				Depth:            depth + 1,
				EdgeType:         edgeTypeCall,
				Confidence:       callConfidence(edge),
				UnresolvedReason: edge.UnresolvedReason,
				Evidence:         evidenceForCallEdge(edge),
			}
			children = appendUniqueNode(children, child, childSeen)
			continue
		}

		// HTTP interface method (Retrofit/Feign style)
		httpInterfaceMethods, nameOnly := checkForHttpInterfaceMethodCached(cache, pool, calleeName)
		if len(httpInterfaceMethods) > 0 {
			for _, httpMethod := range httpInterfaceMethods {
				if !limiter.allow() {
					return
				}
				httpNode := &TreeNode{
					Name:           fmt.Sprintf("[%s %s] via %s.%s", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName),
					Line:           lineNum,
					Depth:          depth + 1,
					EdgeType:       edgeTypeHTTP,
					Confidence:     httpInterfaceConfidence(nameOnly),
					IsCrossService: true,
					HttpMethod:     httpMethod.HttpMethod,
					HttpTarget:     httpMethod.UrlPattern,
					Evidence:       evidenceForHTTPInterface(httpMethod),
				}

				if hitLimit := attachHTTPResolutionTargets(pool, cache, callerID, httpNode, depth, maxDepth, limiter); hitLimit {
					return
				}

				if len(httpNode.Children) == 0 {
					httpNode.Name = fmt.Sprintf("[%s %s] via %s.%s (no matching endpoint or gateway)", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName)
				}
				children = appendUniqueNode(children, httpNode, childSeen)
			}
			continue
		}

		allowResolve := resolve && edge.Source == edgeSourceFunctionCalls

		// Interface method resolution
		typeName := calleeName
		if idx := strings.Index(typeName, "."); idx > 0 {
			typeName = typeName[:idx]
		}
		if allowResolve && depth < di.maxDepth && looksLikeTypeName(typeName) {
			implCallerIDs := resolveInterfaceMethodCallCached(pool, cache, calleeName)
			implCallerIDs = limitImplIDs(implCallerIDs, extractRepo(callerID), di.maxImpls)
			if len(implCallerIDs) > 0 {
				for _, implID := range implCallerIDs {
					if visited[implID] {
						continue
					}
					visited[implID] = true
					if !limiter.allow() {
						return
					}
					implNode := &TreeNode{
						Name:       extractMethod(implID) + " (impl)",
						File:       extractFile(implID),
						Repo:       extractRepo(implID),
						Depth:      depth + 1,
						EdgeType:   edgeTypeResolve,
						Confidence: confidenceMedium,
						CallerID:   implID,
						Evidence:   newEvidence(evidenceSourceInterfaceImpl, ""),
					}
					children = appendUniqueNode(children, implNode, childSeen)
				}
				continue
			}
		}

		// Injected field resolution (DI)
		currentClass := extractClassName(callerID)
		if allowResolve && currentClass != "" && strings.Contains(calleeName, ".") && depth < di.maxDepth {
			diCallerIDs := resolveInjectedFieldCallCached(pool, cache, currentClass, calleeName)
			diCallerIDs = limitImplIDs(diCallerIDs, extractRepo(callerID), di.maxImpls)
			if len(diCallerIDs) > 0 {
				for _, diID := range diCallerIDs {
					if visited[diID] {
						continue
					}
					visited[diID] = true
					if !limiter.allow() {
						return
					}
					diNode := &TreeNode{
						Name:       extractMethod(diID) + " (injected)",
						File:       extractFile(diID),
						Repo:       extractRepo(diID),
						Depth:      depth + 1,
						Injected:   true,
						EdgeType:   edgeTypeResolve,
						Confidence: confidenceMedium,
						CallerID:   diID,
						Evidence:   newEvidence(evidenceSourceInjectedField, ""),
					}
					children = appendUniqueNode(children, diNode, childSeen)
				}
				continue
			}
		}

		// Direct class method resolution
		child := &TreeNode{
			Name:             calleeName,
			Line:             lineNum,
			Depth:            depth + 1,
			EdgeType:         edgeTypeCall,
			Confidence:       callConfidence(edge),
			UnresolvedReason: edge.UnresolvedReason,
			Evidence:         evidenceForCallEdge(edge),
		}
		implID := findImplementationCached(pool, cache, calleeName)
		if implID != "" && !visited[implID] {
			visited[implID] = true
			child.File = extractFile(implID)
			child.Repo = extractRepo(implID)
			child.CallerID = implID
			if edge.Source != edgeSourceFunctionCalls {
				child.EdgeType = edgeTypeResolve
				child.Confidence = confidenceMedium
				child.Evidence = newEvidence(evidenceSourceImplementation, edge.Source)
			}
		}
		children = appendUniqueNode(children, child, childSeen)
	}

	return
}

// TraceUpstream builds upstream call trees.
func TraceUpstream(pool Queryer, funcName string, callerIDs []string, maxDepth int) []*TreeNode {
	tuning := DefaultTraceTuning(maxDepth)
	nodes, _, _ := traceUpstreamWithMode(pool, funcName, callerIDs, maxDepth, 0, tuning)
	return nodes
}

// TraceUpstreamWithMode builds upstream call trees and reports the lookup mode.
func TraceUpstreamWithMode(pool Queryer, funcName string, callerIDs []string, maxDepth int) ([]*TreeNode, string) {
	tuning := DefaultTraceTuning(maxDepth)
	nodes, mode, _ := traceUpstreamWithMode(pool, funcName, callerIDs, maxDepth, 0, tuning)
	return nodes, mode
}

// TraceUpstreamWithModeAndLimit builds upstream call trees with a hard node limit.
func TraceUpstreamWithModeAndLimit(pool Queryer, funcName string, callerIDs []string, maxDepth, maxNodes int) ([]*TreeNode, string) {
	tuning := DefaultTraceTuning(maxDepth)
	nodes, mode, _ := traceUpstreamWithMode(pool, funcName, callerIDs, maxDepth, maxNodes, tuning)
	return nodes, mode
}

// TraceUpstreamWithModeAndLimitTuning builds upstream call trees with explicit tuning.
func TraceUpstreamWithModeAndLimitTuning(pool Queryer, funcName string, callerIDs []string, maxDepth, maxNodes int, tuning TraceTuning) ([]*TreeNode, string) {
	nodes, mode, _ := traceUpstreamWithMode(pool, funcName, callerIDs, maxDepth, maxNodes, tuning)
	return nodes, mode
}

func TraceUpstreamWithModeAndLimitTuningStats(pool Queryer, funcName string, callerIDs []string, maxDepth, maxNodes int, tuning TraceTuning) ([]*TreeNode, string, LimitStats) {
	return traceUpstreamWithMode(pool, funcName, callerIDs, maxDepth, maxNodes, tuning)
}

// TraceUpstreamChildren returns the immediate callers for a node at a given depth.
func TraceUpstreamChildren(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, allowedRepo string) ([]*TreeNode, string) {
	tuning := DefaultTraceTuning(maxDepth)
	children, mode, _ := TraceUpstreamChildrenTuningStats(pool, callerID, parentDepth, maxDepth, maxNodes, allowedRepo, tuning)
	return children, mode
}

// TraceUpstreamChildrenTuning returns the immediate callers with explicit tuning.
func TraceUpstreamChildrenTuning(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, allowedRepo string, tuning TraceTuning) ([]*TreeNode, string) {
	children, mode, _ := TraceUpstreamChildrenTuningStats(pool, callerID, parentDepth, maxDepth, maxNodes, allowedRepo, tuning)
	return children, mode
}

func TraceUpstreamChildrenTuningStats(pool Queryer, callerID string, parentDepth, maxDepth, maxNodes int, allowedRepo string, tuning TraceTuning) (children []*TreeNode, mode string, stats LimitStats) {
	maxDepth = min(maxDepth, MaxDepth)
	if queryContext(pool).Err() != nil {
		return
	}
	if parentDepth < 0 || parentDepth >= MaxDepth {
		return
	}
	if callerID == "" || parentDepth >= maxDepth {
		return nil, "", stats
	}
	if maxNodes <= 0 {
		maxNodes = 2000
	}
	defer func() {
		enforceEdgeEvidenceRequirements(children)
	}()
	limiter := newNodeLimiter(maxNodes)
	defer func() {
		stats = limiter.stats()
	}()
	depth := parentDepth
	cache := newTraceCache(pool, tuning)

	if traceEdgesEnabled() {
		if rows, more, err := queryCallersByCalleeCallerID(pool, callerID, maxNodes, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots); err == nil && len(rows) > 0 {
			if more {
				limiter.markIncomplete()
			}
			childSeen := make(map[string]bool)
			for _, row := range rows {
				if !limiter.allow() {
					break
				}
				child := &TreeNode{
					Name:       row.Name,
					File:       row.File,
					Repo:       row.Repo,
					Line:       row.Line,
					Depth:      depth + 1,
					EdgeType:   edgeTypeCall,
					Confidence: confidenceHigh,
					CallerID:   buildCallerID(row.Repo, row.File, row.Name),
					Evidence:   newEvidence(evidenceSourceFunctionCaller, ""),
				}
				children = appendUniqueNode(children, child, childSeen)
			}
			children = append(children, findScheduleTriggersForCaller(pool, callerID, parentDepth, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)...)
			return children, "id", stats
		} else if err != nil && isMissingRelation(err, "trace_call_edges") {
			disableTraceEdges()
		}
	}

	if calleeID, ok := findFunctionIDByCallerID(pool, callerID, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots); ok {
		rows, more, err := queryFunctionCallersByCallee(pool, calleeID, maxNodes, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)
		if err != nil {
			log.Printf("WARN: trace upstream caller lookup failed for %s: %v", callerID, err)
			limiter.markIncomplete()
		} else {
			if more {
				limiter.markIncomplete()
			}
			childSeen := make(map[string]bool)
			for _, row := range rows {
				if !limiter.allow() {
					break
				}
				child := &TreeNode{
					Name:       row.Name,
					File:       row.File,
					Repo:       row.Repo,
					Line:       row.Line,
					Depth:      depth + 1,
					EdgeType:   edgeTypeCall,
					Confidence: confidenceHigh,
					CallerID:   buildCallerID(row.Repo, row.File, row.Name),
					Evidence:   newEvidence(evidenceSourceFunctionCaller, ""),
				}
				children = appendUniqueNode(children, child, childSeen)
			}
			children = append(children, findScheduleTriggersForCaller(pool, callerID, parentDepth, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)...)
			return children, "id", stats
		}
	}

	methodName := extractMethod(callerID)
	targetFile := extractFile(callerID)
	if methodName == "" {
		return nil, "name", stats
	}

	repo := allowedRepo
	if repo == "" {
		repo = extractRepo(callerID)
	}
	if !shouldSearchUpstreamName(pool, cache, repo, methodName) {
		return nil, "name", stats
	}
	fileCount := countFunctionFilesByNameCached(pool, cache, repo, methodName)
	hasAmbiguity := fileCount > 1

	clause, args := calleeNameClause(methodName, 1)
	query := fmt.Sprintf(`SELECT DISTINCT caller_id, callee_name, line_number FROM pending_calls_edges
	          WHERE (%s)`, clause)
	if allowedRepo != "" {
		query += fmt.Sprintf(" AND split_part(caller_id, ':', 1) = $%d", len(args)+1)
		args = append(args, allowedRepo)
	}
	query += " AND " + traceSnapshotClause("snapshot_id", len(args)+1, tuning.IncludeLegacySnapshots)
	args = append(args, tuning.SnapshotIDs)
	query += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	args = append(args, maxNodes)

	rows, err := pool.Query(queryContext(pool), query, args...)
	if err != nil {
		return nil, "name", stats
	}
	defer rows.Close()

	visited := make(map[string]bool)
	childSeen := make(map[string]bool)
	targetTypes := buildTargetTypes([]string{callerID})
	var pendingRows []pendingEdge
	for rows.Next() {
		var row pendingEdge
		if err := rows.Scan(&row.callerID, &row.calleeName, &row.lineNum); err != nil {
			return nil, "name", stats
		}
		pendingRows = append(pendingRows, row)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, "name", stats
	}
	for _, row := range pendingRows {
		upstreamID, calleeName, lineNum := row.callerID, row.calleeName, row.lineNum

		if upstreamID == callerID || visited[upstreamID] {
			continue
		}
		if hasAmbiguity {
			upstreamFile := extractFile(upstreamID)
			if upstreamFile != targetFile {
				continue
			}
		}
		if !shouldIncludeNameBasedEdge(pool, cache, upstreamID, methodName) &&
			!matchesInjectedReceiver(pool, cache, upstreamID, calleeName, targetTypes) {
			continue
		}
		visited[upstreamID] = true

		if !limiter.allow() {
			break
		}
		child := &TreeNode{
			Name:       extractMethod(upstreamID),
			File:       extractFile(upstreamID),
			Repo:       extractRepo(upstreamID),
			Line:       lineNum,
			Depth:      depth + 1,
			EdgeType:   edgeTypeCall,
			Confidence: confidenceLow,
			CallerID:   upstreamID,
			Evidence:   newEvidence(evidenceSourcePendingCaller, "name_based"),
		}
		children = appendUniqueNode(children, child, childSeen)
	}

	children = append(children, findScheduleTriggersForCaller(pool, callerID, parentDepth, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)...)

	return children, "name", stats
}

// BuildFilters builds trace filters from slices.
func BuildFilters(noTests bool, excludePatterns, includeRepos, excludeRepos []string) *traceFilters {
	return buildFilters(
		noTests,
		strings.Join(excludePatterns, ","),
		strings.Join(includeRepos, ","),
		strings.Join(excludeRepos, ","),
	)
}

// FilterTree applies filters to a tree.
func FilterTree(nodes []*TreeNode, filters *traceFilters) []*TreeNode {
	return filterTree(nodes, filters)
}

// PrintTree writes a formatted tree to stdout.
func PrintTree(nodes []*TreeNode, prefix string) {
	printTree(nodes, prefix)
}

// CountNodes returns a count of all nodes in a tree.
func CountNodes(nodes []*TreeNode) int {
	total := 0
	var walk func(n *TreeNode)
	walk = func(n *TreeNode) {
		total++
		for _, child := range n.Children {
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return total
}

func callConfidence(edge resolvedCallEdge) string {
	if edge.Source == edgeSourceFunctionCalls {
		if edge.ResolutionConfidence != "" {
			return edge.ResolutionConfidence
		}
		return confidenceHigh
	}
	return confidenceLow
}

func newEvidence(source, detail string) *EdgeEvidence {
	if source == "" && detail == "" {
		return nil
	}
	return &EdgeEvidence{
		Source: source,
		Detail: detail,
	}
}

func evidenceForHTTPInterface(method HttpInterfaceMethod) *EdgeEvidence {
	return &EdgeEvidence{
		Source: evidenceSourceHttpInterface,
		Detail: method.InterfaceName + "." + method.MethodName,
		Repo:   method.Repo,
		File:   method.File,
		Line:   method.LineNumber,
	}
}

func evidenceForCallEdge(edge resolvedCallEdge) *EdgeEvidence {
	if edge.Source == "" {
		return nil
	}
	detail := ""
	if edge.Source == edgeSourcePendingCalls {
		detail = "name_based"
	} else if edge.Source == edgeSourceFunctionCalls && edge.ResolutionSource != "" {
		detail = edge.ResolutionSource
	} else if edge.Source == edgeSourceFunctionCalls && edge.UnresolvedReason != "" {
		detail = "unresolved:" + edge.UnresolvedReason
	}
	return newEvidence(edge.Source, detail)
}

func evidenceHasLocation(evidence *EdgeEvidence) bool {
	return evidence != nil && evidence.Repo != "" && evidence.File != "" && evidence.Line > 0
}

func downgradeEdgeConfidence(confidence string) string {
	switch confidence {
	case confidenceHigh:
		return confidenceMedium
	case confidenceMedium:
		return confidenceLow
	case "":
		return confidenceLow
	default:
		return confidence
	}
}

func nodeRepoFile(node *TreeNode) (string, string) {
	if node == nil {
		return "", ""
	}
	repo := strings.TrimSpace(node.Repo)
	file := strings.TrimSpace(node.File)
	if repo != "" && file != "" {
		return repo, file
	}
	if node.CallerID != "" {
		parsedRepo, parsedFile, _ := ParseCallerID(node.CallerID)
		if repo == "" {
			repo = strings.TrimSpace(parsedRepo)
		}
		if file == "" {
			file = strings.TrimSpace(parsedFile)
		}
	}
	return repo, file
}

func inferEdgeEvidenceLocation(parent, child *TreeNode) (string, string, int) {
	if parent == nil || child == nil {
		return "", "", 0
	}
	parentRepo, parentFile := nodeRepoFile(parent)
	childRepo, childFile := nodeRepoFile(child)
	source := ""
	if child.Evidence != nil {
		source = child.Evidence.Source
	}

	// Upstream edges are represented as parent->caller, so call-site location lives on child.
	if source == evidenceSourceFunctionCaller || source == evidenceSourcePendingCaller {
		if childRepo != "" && childFile != "" && child.Line > 0 {
			return childRepo, childFile, child.Line
		}
	}

	// Downstream call-like edges carry call-site line on child, but caller repo/file is parent.
	if source == edgeSourceFunctionCalls || source == edgeSourcePendingCalls ||
		source == evidenceSourceHttpClient || source == evidenceSourceSqsProducer ||
		source == evidenceSourceHttpInterface {
		if parentRepo != "" && parentFile != "" && child.Line > 0 {
			return parentRepo, parentFile, child.Line
		}
	}

	// Endpoint/consumer/implementation edges are tied to the resolved target definition.
	if source == evidenceSourceHttpEndpoint || source == evidenceSourceSqsConsumer ||
		source == evidenceSourceInterfaceImpl || source == evidenceSourceInjectedField ||
		source == evidenceSourceImplementation {
		if childRepo != "" && childFile != "" && child.Line > 0 {
			return childRepo, childFile, child.Line
		}
	}

	if parentRepo != "" && parentFile != "" && child.Line > 0 {
		return parentRepo, parentFile, child.Line
	}
	if childRepo != "" && childFile != "" && child.Line > 0 {
		return childRepo, childFile, child.Line
	}
	return "", "", 0
}

func ensureEdgeEvidenceLocation(parent, child *TreeNode) {
	if parent == nil || child == nil {
		return
	}
	// A partial source location must not be completed with an unrelated call site.
	if child.Evidence != nil && (child.Evidence.Repo != "" || child.Evidence.File != "" || child.Evidence.Line > 0) {
		return
	}
	repo, file, line := inferEdgeEvidenceLocation(parent, child)
	if repo == "" || file == "" || line <= 0 {
		return
	}
	if child.Evidence == nil {
		child.Evidence = &EdgeEvidence{}
	}
	if child.Evidence.Repo == "" {
		child.Evidence.Repo = repo
	}
	if child.Evidence.File == "" {
		child.Evidence.File = file
	}
	if child.Evidence.Line <= 0 {
		child.Evidence.Line = line
	}
}

func ensureRootEdgeEvidenceLocation(node *TreeNode) {
	if node == nil || node.EdgeType == "" {
		return
	}
	if node.Evidence != nil && (node.Evidence.Repo != "" || node.Evidence.File != "" || node.Evidence.Line > 0) {
		return
	}
	repo, file := nodeRepoFile(node)
	if repo == "" || file == "" || node.Line <= 0 {
		return
	}
	if node.Evidence == nil {
		node.Evidence = &EdgeEvidence{}
	}
	if node.Evidence.Repo == "" {
		node.Evidence.Repo = repo
	}
	if node.Evidence.File == "" {
		node.Evidence.File = file
	}
	if node.Evidence.Line <= 0 {
		node.Evidence.Line = node.Line
	}
}

func enforceEdgeEvidenceRequirements(nodes []*TreeNode) {
	var walk func(parent, node *TreeNode)
	walk = func(parent, node *TreeNode) {
		if node == nil {
			return
		}
		if parent == nil {
			ensureRootEdgeEvidenceLocation(node)
			if node.EdgeType != "" && !evidenceHasLocation(node.Evidence) {
				node.Confidence = downgradeEdgeConfidence(node.Confidence)
			}
		} else {
			ensureEdgeEvidenceLocation(parent, node)
			if !evidenceHasLocation(node.Evidence) {
				node.Confidence = downgradeEdgeConfidence(node.Confidence)
			}
		}
		for _, child := range node.Children {
			walk(node, child)
		}
	}
	for _, node := range nodes {
		walk(nil, node)
	}
}

func traceDownstream(pool Queryer, callerIDs []string, maxDepth, maxNodes int, resolve bool, tuning TraceTuning) (roots []*TreeNode, stats LimitStats) {
	maxDepth = min(maxDepth, MaxDepth)
	if queryContext(pool).Err() != nil {
		return
	}
	startTrace := time.Now()
	visited := make(map[string]bool)
	defer func() {
		enforceEdgeEvidenceRequirements(roots)
	}()
	limiter := newNodeLimiter(maxNodes)
	defer func() {
		stats = limiter.stats()
	}()
	cache := newTraceCache(pool, tuning)
	di := getDILimits(tuning, maxDepth)
	var edgeBatchTime time.Duration
	var httpBatchTime time.Duration
	var sqsBatchTime time.Duration
	var totalEdges int
	var totalHttpCalls int
	var totalSqsCalls int
	var totalCallers int

	type nodeRef struct {
		node     *TreeNode
		callerID string
	}
	levels := make(map[int][]nodeRef)

	for _, callerID := range callerIDs {
		if visited[callerID] {
			continue
		}
		visited[callerID] = true
		if !limiter.allow() {
			break
		}
		node := &TreeNode{
			Name:     extractMethod(callerID),
			File:     extractFile(callerID),
			Repo:     extractRepo(callerID),
			Depth:    0,
			CallerID: callerID,
		}
		roots = append(roots, node)
		levels[0] = append(levels[0], nodeRef{node: node, callerID: callerID})
	}

	for depth := 0; depth < maxDepth && queryContext(pool).Err() == nil; depth++ {
		items := levels[depth]
		if len(items) == 0 {
			break
		}
		callerIDsAtDepth := make([]string, 0, len(items))
		for _, item := range items {
			if item.callerID != "" {
				callerIDsAtDepth = append(callerIDsAtDepth, item.callerID)
			}
		}
		totalCallers += len(callerIDsAtDepth)
		edgeStart := time.Now()
		edgesByCaller := getResolvedCallEdgesBatch(pool, cache, callerIDsAtDepth)
		edgeBatchTime += time.Since(edgeStart)
		for _, edges := range edgesByCaller {
			totalEdges += len(edges)
		}
		httpStart := time.Now()
		httpCallsByCaller := findHttpCallsBatchCached(cache, pool, callerIDsAtDepth)
		httpBatchTime += time.Since(httpStart)
		for _, calls := range httpCallsByCaller {
			totalHttpCalls += len(calls)
		}
		sqsStart := time.Now()
		sqsCallsByCaller := findSqsProducerCallsBatchCached(cache, pool, callerIDsAtDepth)
		sqsBatchTime += time.Since(sqsStart)
		for _, calls := range sqsCallsByCaller {
			totalSqsCalls += len(calls)
		}
		var interfaceResolutions map[string][]string
		var injectedResolutions map[string][]string
		if resolve && depth < di.maxDepth {
			calleeNamesSet := make(map[string]bool)
			var injectedRequests []injectedCallRequest
			for _, item := range items {
				currentClass := extractClassName(item.callerID)
				if currentClass == "" {
					continue
				}
				for _, edge := range edgesByCaller[item.callerID] {
					if edge.Source != edgeSourceFunctionCalls {
						continue
					}
					if edge.CalleeCallerID != "" {
						continue
					}
					calleeName := edge.CalleeName
					if calleeName == "" || !strings.Contains(calleeName, ".") {
						continue
					}
					typeName := calleeName
					if idx := strings.LastIndex(typeName, "."); idx > 0 {
						typeName = typeName[:idx]
					}
					if looksLikeTypeName(typeName) {
						calleeNamesSet[calleeName] = true
					}
					injectedRequests = append(injectedRequests, injectedCallRequest{
						className:  currentClass,
						calleeName: calleeName,
					})
				}
			}
			if len(calleeNamesSet) > 0 {
				var calleeNames []string
				for name := range calleeNamesSet {
					calleeNames = append(calleeNames, name)
				}
				interfaceResolutions = resolveInterfaceMethodCallBatchCached(pool, cache, calleeNames)
			}
			if len(injectedRequests) > 0 {
				injectedResolutions = resolveInjectedFieldCallBatchCached(pool, cache, injectedRequests)
			}
		}

		for _, item := range items {
			parent := item.node
			callerID := item.callerID
			if parent == nil || callerID == "" {
				continue
			}
			childSeen := make(map[string]bool)

			// HTTP client calls (cross-service)
			httpCalls := httpCallsByCaller[callerID]
			for _, httpCall := range httpCalls {
				if !limiter.allow() {
					return
				}
				httpNode := &TreeNode{
					Name:           fmt.Sprintf("[%s %s]", httpCall.HttpMethod, httpCall.UrlPattern),
					Line:           httpCall.LineNumber,
					Depth:          parent.Depth + 1,
					EdgeType:       edgeTypeHTTP,
					Confidence:     confidenceHigh,
					IsCrossService: true,
					HttpMethod:     httpCall.HttpMethod,
					HttpTarget:     httpCall.UrlPattern,
					Evidence:       newEvidence(evidenceSourceHttpClient, ""),
				}

				if hitLimit := attachHTTPResolutionTargets(pool, cache, callerID, httpNode, parent.Depth, maxDepth, limiter); hitLimit {
					return
				}
				if len(httpNode.Children) == 0 {
					httpNode.Name = fmt.Sprintf("[%s %s] (no matching endpoint or gateway)", httpCall.HttpMethod, httpCall.UrlPattern)
				}
				parent.Children = appendUniqueNode(parent.Children, httpNode, childSeen)
			}

			// SQS producer calls
			sqsCalls := sqsCallsByCaller[callerID]
			for _, sqsCall := range sqsCalls {
				if !limiter.allow() {
					return
				}
				sqsNode := &TreeNode{
					Name:           fmt.Sprintf("[SQS → %s]", sqsCall.QueueName),
					Line:           sqsCall.LineNumber,
					Depth:          parent.Depth + 1,
					EdgeType:       edgeTypeSQS,
					Confidence:     confidenceHigh,
					IsCrossService: true,
					IsSqs:          true,
					QueueTarget:    sqsCall.QueueName,
					Evidence:       newEvidence(evidenceSourceSqsProducer, ""),
				}

				consumers := findSqsConsumersCached(cache, pool, sqsCall.QueueName, extractRepo(callerID))
				consumerSeen := make(map[string]bool)
				for _, consumer := range consumers {
					if !limiter.allow() {
						return
					}
					consumerNode := &TreeNode{
						Name:       formatQueueConsumerNodeName(consumer),
						File:       consumer.File,
						Repo:       consumer.Repo,
						Depth:      parent.Depth + 2,
						EdgeType:   edgeTypeSQS,
						Confidence: confidenceMedium,
						Evidence:   newEvidence(evidenceSourceSqsConsumer, ""),
					}

					handlerID := findConsumerHandler(pool, cache, consumer)
					if handlerID != "" && !visited[handlerID] {
						consumerNode.CallerID = handlerID
						visited[handlerID] = true
						if parent.Depth+2 <= maxDepth {
							levels[parent.Depth+2] = append(levels[parent.Depth+2], nodeRef{node: consumerNode, callerID: handlerID})
						}
					}

					sqsNode.Children = appendUniqueNode(sqsNode.Children, consumerNode, consumerSeen)
				}

				if len(sqsNode.Children) == 0 {
					sqsNode.Name = fmt.Sprintf("[SQS → %s] (no consumer found)", sqsCall.QueueName)
				}
				parent.Children = appendUniqueNode(parent.Children, sqsNode, childSeen)
			}

			edges := edgesByCaller[callerID]
			for _, edge := range edges {
				calleeName := edge.CalleeName
				lineNum := edge.LineNumber
				calleeCallerID := edge.CalleeCallerID

				if calleeCallerID == "" && isGenericMethod(calleeName) {
					continue
				}
				if calleeCallerID == "" && visited[calleeName] {
					continue
				}

				if !limiter.allow() {
					return
				}
				child := &TreeNode{
					Name:             calleeName,
					Line:             lineNum,
					Depth:            parent.Depth + 1,
					EdgeType:         edgeTypeCall,
					Confidence:       callConfidence(edge),
					UnresolvedReason: edge.UnresolvedReason,
					Evidence:         evidenceForCallEdge(edge),
				}

				if calleeCallerID != "" {
					if visited[calleeCallerID] {
						continue
					}
					visited[calleeCallerID] = true
					child.File = extractFile(calleeCallerID)
					child.Repo = extractRepo(calleeCallerID)
					child.CallerID = calleeCallerID
					parent.Children = appendUniqueNode(parent.Children, child, childSeen)
					if parent.Depth+1 <= maxDepth {
						levels[parent.Depth+1] = append(levels[parent.Depth+1], nodeRef{node: child, callerID: calleeCallerID})
					}
					continue
				}

				visited[calleeName] = true
				allowResolve := resolve && edge.Source == edgeSourceFunctionCalls
				if !allowResolve {
					parent.Children = appendUniqueNode(parent.Children, child, childSeen)
					continue
				}

				// HTTP interface method (Retrofit/Feign style)
				httpInterfaceMethods, nameOnly := checkForHttpInterfaceMethodCached(cache, pool, calleeName)
				if len(httpInterfaceMethods) > 0 {
					for _, httpMethod := range httpInterfaceMethods {
						if !limiter.allow() {
							return
						}
						httpNode := &TreeNode{
							Name:           fmt.Sprintf("[%s %s] via %s.%s", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName),
							Line:           lineNum,
							Depth:          parent.Depth + 1,
							EdgeType:       edgeTypeHTTP,
							Confidence:     httpInterfaceConfidence(nameOnly),
							IsCrossService: true,
							HttpMethod:     httpMethod.HttpMethod,
							HttpTarget:     httpMethod.UrlPattern,
							Evidence:       evidenceForHTTPInterface(httpMethod),
						}

						if hitLimit := attachHTTPResolutionTargets(pool, cache, callerID, httpNode, parent.Depth, maxDepth, limiter); hitLimit {
							return
						}
						if len(httpNode.Children) == 0 {
							httpNode.Name = fmt.Sprintf("[%s %s] via %s.%s (no matching endpoint or gateway)", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName)
						}
						parent.Children = appendUniqueNode(parent.Children, httpNode, childSeen)
					}
					continue
				}

				// Interface method resolution
				typeName := calleeName
				if idx := strings.Index(typeName, "."); idx > 0 {
					typeName = typeName[:idx]
				}
				if parent.Depth < di.maxDepth && looksLikeTypeName(typeName) {
					implCallerIDs := interfaceResolutions[calleeName]
					if interfaceResolutions == nil {
						implCallerIDs = resolveInterfaceMethodCallCached(pool, cache, calleeName)
					}
					implCallerIDs = limitImplIDs(implCallerIDs, extractRepo(callerID), di.maxImpls)
					if len(implCallerIDs) > 0 {
						for _, implID := range implCallerIDs {
							if visited[implID] {
								continue
							}
							visited[implID] = true
							if !limiter.allow() {
								return
							}
							implNode := &TreeNode{
								Name:       extractMethod(implID) + " (impl)",
								File:       extractFile(implID),
								Repo:       extractRepo(implID),
								Depth:      parent.Depth + 1,
								EdgeType:   edgeTypeResolve,
								Confidence: confidenceMedium,
								CallerID:   implID,
								Evidence:   newEvidence(evidenceSourceInterfaceImpl, ""),
							}
							parent.Children = appendUniqueNode(parent.Children, implNode, childSeen)
							if parent.Depth+1 <= maxDepth && shouldExpandResolved(pool, cache, implID, parent.Repo) {
								levels[parent.Depth+1] = append(levels[parent.Depth+1], nodeRef{node: implNode, callerID: implID})
							}
						}
						continue
					}
				}

				// Injected field resolution (DI)
				currentClass := extractClassName(callerID)
				if currentClass != "" && strings.Contains(calleeName, ".") && parent.Depth < di.maxDepth {
					var diCallerIDs []string
					if injectedResolutions != nil {
						key := cacheKey(currentClass, calleeName)
						if cachedIDs, ok := injectedResolutions[key]; ok {
							diCallerIDs = cachedIDs
						} else {
							req := injectedCallRequest{className: currentClass, calleeName: calleeName}
							batch := resolveInjectedFieldCallBatchCached(pool, cache, []injectedCallRequest{req})
							diCallerIDs = batch[cacheKey(currentClass, calleeName)]
							injectedResolutions[key] = diCallerIDs
						}
					} else {
						diCallerIDs = resolveInjectedFieldCallCached(pool, cache, currentClass, calleeName)
					}
					diCallerIDs = limitImplIDs(diCallerIDs, extractRepo(callerID), di.maxImpls)
					if len(diCallerIDs) > 0 {
						for _, diID := range diCallerIDs {
							if visited[diID] {
								continue
							}
							visited[diID] = true
							if !limiter.allow() {
								return
							}
							diNode := &TreeNode{
								Name:       extractMethod(diID) + " (injected)",
								File:       extractFile(diID),
								Repo:       extractRepo(diID),
								Depth:      parent.Depth + 1,
								Injected:   true,
								EdgeType:   edgeTypeResolve,
								Confidence: confidenceMedium,
								CallerID:   diID,
								Evidence:   newEvidence(evidenceSourceInjectedField, ""),
							}
							parent.Children = appendUniqueNode(parent.Children, diNode, childSeen)
							if parent.Depth+1 <= maxDepth && shouldExpandResolved(pool, cache, diID, parent.Repo) {
								levels[parent.Depth+1] = append(levels[parent.Depth+1], nodeRef{node: diNode, callerID: diID})
							}
						}
						continue
					}
				}

				// Direct class method resolution
				implID := findImplementationCached(pool, cache, calleeName)
				if implID != "" && !visited[implID] {
					visited[implID] = true
					child.File = extractFile(implID)
					child.Repo = extractRepo(implID)
					child.CallerID = implID
					if edge.Source != edgeSourceFunctionCalls {
						child.EdgeType = edgeTypeResolve
						child.Confidence = confidenceMedium
						child.Evidence = newEvidence(evidenceSourceImplementation, edge.Source)
					}
					parent.Children = appendUniqueNode(parent.Children, child, childSeen)
					if parent.Depth+1 <= maxDepth {
						levels[parent.Depth+1] = append(levels[parent.Depth+1], nodeRef{node: child, callerID: implID})
					}
					continue
				}

				parent.Children = appendUniqueNode(parent.Children, child, childSeen)
			}
		}
	}

	log.Printf("trace batch stats: callers=%d edges=%d edgeBatch=%s httpCalls=%d httpBatch=%s sqsCalls=%d sqsBatch=%s",
		totalCallers,
		totalEdges,
		edgeBatchTime,
		totalHttpCalls,
		httpBatchTime,
		totalSqsCalls,
		sqsBatchTime,
	)
	log.Printf("trace downstream time=%s", time.Since(startTrace))

	if resolve && cache != nil && cache.resolveStats != nil {
		stats := cache.resolveStats
		interfaceReq := stats.interfaceRequests + stats.interfaceBatchRequests
		interfaceResolved := stats.interfaceResolvedEdges + stats.interfaceBatchResolved
		injectedReq := stats.injectedRequests + stats.injectedBatchRequests
		injectedResolved := stats.injectedResolvedEdges + stats.injectedBatchResolved
		interfaceTime := stats.interfaceDuration + stats.interfaceBatchDuration
		injectedTime := stats.injectedDuration + stats.injectedBatchDuration
		log.Printf("trace resolve stats: interface req=%d (batch=%d cache=%d) resolved=%d time=%s | injected req=%d (batch=%d cache=%d) resolved=%d time=%s",
			interfaceReq,
			stats.interfaceBatchRequests,
			stats.interfaceCacheHits,
			interfaceResolved,
			interfaceTime,
			injectedReq,
			stats.injectedBatchRequests,
			stats.injectedCacheHits,
			injectedResolved,
			injectedTime,
		)
	}

	return
}

func getResolvedCallEdges(pool Queryer, cache *traceCache, callerID string) []resolvedCallEdge {
	if callerID == "" {
		return nil
	}
	result := getResolvedCallEdgesBatch(pool, cache, []string{callerID})
	return result[callerID]
}

func getResolvedCallEdgesBatch(pool Queryer, cache *traceCache, callerIDs []string) map[string][]resolvedCallEdge {
	result := make(map[string][]resolvedCallEdge, len(callerIDs))
	if len(callerIDs) == 0 {
		return result
	}
	snapshotIDs := snapshotIDsFromCache(cache)
	scoped := hasSnapshotScope(cache)

	seen := make(map[string]bool, len(callerIDs))
	var ids []string
	repoByCaller := make(map[string]string, len(callerIDs))
	fileByCaller := make(map[string]string, len(callerIDs))
	nameByCaller := make(map[string]string, len(callerIDs))
	for _, callerID := range callerIDs {
		if callerID == "" || seen[callerID] {
			continue
		}
		repo, file, name := ParseCallerID(callerID)
		if repo == "" || file == "" || name == "" {
			continue
		}
		seen[callerID] = true
		ids = append(ids, callerID)
		repoByCaller[callerID] = repo
		fileByCaller[callerID] = file
		nameByCaller[callerID] = name
	}

	for _, callerID := range ids {
		if cache != nil {
			if edges, ok := cache.resolvedCallEdges[callerID]; ok {
				result[callerID] = edges
				continue
			}
		}
		if !scoped {
			if cached, ok := globalCallEdgeCache.get(callerID); ok {
				if edges, ok := cached.([]resolvedCallEdge); ok {
					result[callerID] = edges
					if cache != nil {
						cache.resolvedCallEdges[callerID] = edges
					}
					continue
				}
			}
		}
	}

	var missingDirect []string
	for _, callerID := range ids {
		if _, ok := result[callerID]; !ok {
			missingDirect = append(missingDirect, callerID)
		}
	}

	if len(missingDirect) > 0 && traceEdgesEnabled() {
		query := fmt.Sprintf(`
			SELECT caller_id,
			       callee_id,
			       callee_name,
			       line_number,
			       callee_repo,
			       callee_file,
			       callee_func,
			       callee_resolution_source,
			       callee_resolution_confidence,
			       unresolved_reason
			FROM trace_call_edges
			WHERE caller_id = ANY($1)
			  AND %s
			ORDER BY caller_id, line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, missingDirect, snapshotIDs)
		if err != nil {
			if isMissingRelation(err, "trace_call_edges") {
				disableTraceEdges()
			}
		} else {
			defer rows.Close()
			for rows.Next() {
				var callerID string
				var calleeID sql.NullString
				var calleeName string
				var lineNum sql.NullInt64
				var calleeRepo, calleeFile, calleeFunc sql.NullString
				var resolutionSource, resolutionConfidence, unresolvedReason sql.NullString
				if err := rows.Scan(&callerID, &calleeID, &calleeName, &lineNum, &calleeRepo, &calleeFile, &calleeFunc, &resolutionSource, &resolutionConfidence, &unresolvedReason); err != nil {
					continue
				}
				edge := resolvedCallEdge{
					CalleeName: calleeName,
					Source:     edgeSourceFunctionCalls,
				}
				if lineNum.Valid {
					edge.LineNumber = int(lineNum.Int64)
				}
				if resolutionSource.Valid {
					edge.ResolutionSource = resolutionSource.String
				}
				if resolutionConfidence.Valid {
					edge.ResolutionConfidence = resolutionConfidence.String
				}
				if unresolvedReason.Valid {
					edge.UnresolvedReason = unresolvedReason.String
				}
				if calleeID.Valid && calleeID.String != "" {
					edge.CalleeCallerID = calleeID.String
				} else if calleeRepo.Valid && calleeFile.Valid && calleeFunc.Valid {
					edge.CalleeCallerID = buildCallerID(calleeRepo.String, calleeFile.String, calleeFunc.String)
				}
				result[callerID] = append(result[callerID], edge)
			}
		}
	}

	var missing []string
	for _, callerID := range ids {
		if len(result[callerID]) == 0 {
			missing = append(missing, callerID)
		}
	}

	if len(missing) > 0 {
		var repos []string
		var files []string
		var names []string
		for _, callerID := range missing {
			repos = append(repos, repoByCaller[callerID])
			files = append(files, fileByCaller[callerID])
			names = append(names, nameByCaller[callerID])
		}
		query := fmt.Sprintf(`
			WITH input AS (
				SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[])
					AS t(caller_id, repo, file, name)
			)
			SELECT t.caller_id,
			       fc.callee_function_id,
			       fc.callee_name,
			       fc.line_number,
			       r2.name,
			       f2.path,
			       fn2.name,
			       fc.callee_resolution_source,
			       fc.callee_resolution_confidence,
			       fc.unresolved_reason
			FROM input t
			JOIN repositories r ON r.name = t.repo
			JOIN files f ON f.repo_id = r.id AND f.path = t.file
			JOIN functions fn ON fn.file_id = f.id AND fn.name = t.name
			JOIN function_calls fc ON fc.caller_function_id = fn.id
			LEFT JOIN functions fn2 ON fn2.id = fc.callee_function_id
			LEFT JOIN files f2 ON f2.id = fn2.file_id
			LEFT JOIN repositories r2 ON r2.id = f2.repo_id
			WHERE %s
			ORDER BY t.caller_id, fc.line_number`, traceSnapshotClause("f.snapshot_id", 5, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, missing, repos, files, names, snapshotIDs)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var callerID string
				var calleeID sql.NullInt64
				var calleeName string
				var lineNum int
				var calleeRepo, calleeFile, calleeFunc sql.NullString
				var resolutionSource, resolutionConfidence, unresolvedReason sql.NullString
				if err := rows.Scan(&callerID, &calleeID, &calleeName, &lineNum, &calleeRepo, &calleeFile, &calleeFunc, &resolutionSource, &resolutionConfidence, &unresolvedReason); err != nil {
					continue
				}
				edge := resolvedCallEdge{
					CalleeName: calleeName,
					LineNumber: lineNum,
					Source:     edgeSourceFunctionCalls,
				}
				if resolutionSource.Valid {
					edge.ResolutionSource = resolutionSource.String
				}
				if resolutionConfidence.Valid {
					edge.ResolutionConfidence = resolutionConfidence.String
				}
				if unresolvedReason.Valid {
					edge.UnresolvedReason = unresolvedReason.String
				}
				if calleeRepo.Valid && calleeFile.Valid && calleeFunc.Valid {
					edge.CalleeCallerID = buildCallerID(calleeRepo.String, calleeFile.String, calleeFunc.String)
				}
				result[callerID] = append(result[callerID], edge)
			}
		}
	}

	var pendingMissing []string
	for _, callerID := range ids {
		if len(result[callerID]) == 0 {
			pendingMissing = append(pendingMissing, callerID)
		}
	}

	if len(pendingMissing) > 0 {
		query := fmt.Sprintf(`SELECT caller_id, callee_name, line_number
	          FROM pending_calls_edges
	          WHERE caller_id = ANY($1)
	          AND %s
	          ORDER BY caller_id, line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, pendingMissing, snapshotIDs)
		if err == nil {
			var pendingEdges []pendingEdge
			defer rows.Close()
			for rows.Next() {
				var callerID, calleeName string
				var lineNum int
				if err := rows.Scan(&callerID, &calleeName, &lineNum); err != nil {
					continue
				}
				pendingEdges = append(pendingEdges, pendingEdge{
					callerID:   callerID,
					calleeName: calleeName,
					lineNum:    lineNum,
				})
			}
			preloadNameCountsForPendingEdges(pool, cache, pendingEdges)
			for _, edge := range pendingEdges {
				if !shouldIncludeNameBasedEdge(pool, cache, edge.callerID, edge.calleeName) {
					continue
				}
				result[edge.callerID] = append(result[edge.callerID], resolvedCallEdge{
					CalleeName: edge.calleeName,
					LineNumber: edge.lineNum,
					Source:     edgeSourcePendingCalls,
				})
			}
		}
	}

	for _, callerID := range ids {
		edges := result[callerID]
		if cache != nil {
			cache.resolvedCallEdges[callerID] = edges
		}
		if !scoped {
			globalCallEdgeCache.set(callerID, edges)
		}
	}

	return result
}

func preloadNameCountsForPendingEdges(pool Queryer, cache *traceCache, edges []pendingEdge) {
	if cache == nil || len(edges) == 0 {
		return
	}
	namesByRepo := make(map[string]map[string]bool)
	for _, edge := range edges {
		if edge.calleeName == "" || strings.Contains(edge.calleeName, ".") {
			continue
		}
		repo := extractRepo(edge.callerID)
		if repo == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(edge.calleeName))
		if name == "" {
			continue
		}
		cacheKeyValue := strings.ToLower(repo) + ":" + name
		if _, ok := cache.functionNameCounts[cacheKeyValue]; ok {
			continue
		}
		set := namesByRepo[repo]
		if set == nil {
			set = make(map[string]bool)
			namesByRepo[repo] = set
		}
		set[name] = true
	}

	for repo, namesSet := range namesByRepo {
		if len(namesSet) == 0 {
			continue
		}
		names := make([]string, 0, len(namesSet))
		for name := range namesSet {
			names = append(names, name)
		}
		query := fmt.Sprintf(`
			SELECT LOWER(fn.name), COUNT(*)
			FROM functions fn
			JOIN files fi ON fn.file_id = fi.id
			JOIN repositories r ON fi.repo_id = r.id
		WHERE r.name = $1 AND LOWER(fn.name) = ANY($2)
			  AND %s
			GROUP BY LOWER(fn.name)`, traceSnapshotClause("fi.snapshot_id", 3, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, repo, names, snapshotIDsFromCache(cache))
		if err != nil {
			continue
		}
		counts := make(map[string]int, len(names))
		for rows.Next() {
			var name string
			var count int
			if rows.Scan(&name, &count) == nil && name != "" {
				counts[name] = count
			}
		}
		rows.Close()
		for _, name := range names {
			cache.functionNameCounts[strings.ToLower(repo)+":"+name] = counts[name]
		}
	}
}

type nameBasedRules struct {
	minNameLength int
	exclude       map[string]bool
	include       map[string]bool
}

func getNameBasedRules(cache *traceCache, repo string) nameBasedRules {
	if cache != nil {
		if rules, ok := cache.nameBasedRulesByRepo[strings.ToLower(repo)]; ok {
			return rules
		}
	}

	cfg := config.GetEffectiveTraceConfig()
	merged := cfg.NameBasedForRepo(repo)
	rules := nameBasedRules{
		minNameLength: merged.MinNameLength,
		exclude:       make(map[string]bool),
		include:       make(map[string]bool),
	}
	if rules.minNameLength <= 0 {
		rules.minNameLength = 3
	}
	for _, name := range merged.Exclude {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		rules.exclude[name] = true
	}
	for _, name := range merged.Include {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		rules.include[name] = true
	}

	if cache != nil {
		cache.nameBasedRulesByRepo[strings.ToLower(repo)] = rules
	}
	return rules
}

func normalizeCalleeNames(calleeName string) (string, string) {
	trimmed := strings.TrimSpace(calleeName)
	if trimmed == "" {
		return "", ""
	}
	full := strings.ToLower(trimmed)
	simple := full
	if idx := strings.LastIndex(simple, "."); idx != -1 {
		simple = simple[idx+1:]
	}
	return simple, full
}

func shouldIncludeNameBasedEdge(pool Queryer, cache *traceCache, callerID, calleeName string) bool {
	repo := extractRepo(callerID)
	rules := getNameBasedRules(cache, repo)
	simple, full := normalizeCalleeNames(calleeName)
	if simple == "" {
		return false
	}

	if rules.include[simple] || rules.include[full] {
		return true
	}
	if rules.exclude[simple] || rules.exclude[full] {
		return false
	}
	if len(simple) < rules.minNameLength {
		return false
	}
	if isGenericMethod(simple) {
		return false
	}

	if !strings.Contains(calleeName, ".") {
		file := extractFile(callerID)
		if repo == "" || file == "" {
			return false
		}
		if match := findCallerIDByRepoFileFuncCached(pool, cache, repo, file, simple); match != "" {
			return true
		}
		if countFunctionsByNameCached(pool, cache, repo, simple) == 1 {
			return true
		}
		return false
	}

	return true
}

func shouldSearchUpstreamName(pool Queryer, cache *traceCache, repo, calleeName string) bool {
	rules := getNameBasedRules(cache, repo)
	simple, full := normalizeCalleeNames(calleeName)
	if simple == "" {
		return false
	}
	if rules.include[simple] || rules.include[full] {
		return true
	}
	if rules.exclude[simple] || rules.exclude[full] {
		return false
	}
	if len(simple) < rules.minNameLength {
		return false
	}
	if isGenericMethod(simple) {
		return false
	}
	if strings.Contains(calleeName, ".") {
		return true
	}
	maxCount := 0
	if cache != nil {
		maxCount = cache.tuning.UpstreamMaxNameCount
	}
	if maxCount > 0 && repo != "" {
		if countFunctionsByNameCached(pool, cache, repo, simple) > maxCount {
			return false
		}
	}
	return true
}

func shouldExpandResolved(pool Queryer, cache *traceCache, callerID string, parentRepo string) bool {
	if cache == nil {
		return false
	}
	mode := cache.tuning.ResolveExpandMode
	switch mode {
	case "full":
		return true
	case "smart":
		if parentRepo != "" && extractRepo(callerID) != parentRepo {
			return false
		}
		maxCalls := cache.tuning.ResolveExpandMaxCalls
		if maxCalls <= 0 {
			return false
		}
		if count := countDirectCallEdgesCached(pool, cache, callerID); count > 0 && count <= maxCalls {
			return true
		}
		return false
	default:
		return false
	}
}

func countDirectCallEdgesCached(pool Queryer, cache *traceCache, callerID string) int {
	if cache != nil {
		if count, ok := cache.callEdgeCounts[callerID]; ok {
			return count
		}
	}
	scoped := hasSnapshotScope(cache)
	if !scoped {
		if cached, ok := globalCallCountCache.get(callerID); ok {
			if count, ok := cached.(int); ok {
				if cache != nil {
					cache.callEdgeCounts[callerID] = count
				}
				return count
			}
		}
	}

	var count int
	usedTrace := false
	if traceEdgesEnabled() {
		query := fmt.Sprintf(`SELECT COUNT(*) FROM trace_call_edges WHERE caller_id = $1 AND %s`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
		if err := pool.QueryRow(queryContext(pool), query, callerID, snapshotIDsFromCache(cache)).Scan(&count); err != nil {
			if isMissingRelation(err, "trace_call_edges") {
				disableTraceEdges()
			} else {
				count = 0
			}
		} else {
			usedTrace = true
		}
	}

	if !usedTrace {
		idMap := findFunctionIDsByCallerIDsCached(pool, cache, []string{callerID})
		fnID := idMap[callerID]
		if fnID == 0 {
			if cache != nil {
				cache.callEdgeCounts[callerID] = 0
			}
			if !scoped {
				globalCallCountCache.set(callerID, 0)
			}
			return 0
		}
		query := `SELECT COUNT(*) FROM function_calls WHERE caller_function_id = $1`
		if err := pool.QueryRow(queryContext(pool), query, fnID).Scan(&count); err != nil {
			count = 0
		}
	}
	if cache != nil {
		cache.callEdgeCounts[callerID] = count
	}
	if !scoped {
		globalCallCountCache.set(callerID, count)
	}
	return count
}

func buildTargetTypes(callerIDs []string) map[string]bool {
	targets := make(map[string]bool)
	for _, id := range callerIDs {
		if className := extractClassName(id); className != "" {
			targets[strings.ToLower(className)] = true
		}
	}
	return targets
}

func matchesInjectedReceiver(pool Queryer, cache *traceCache, callerID, calleeName string, targetTypes map[string]bool) bool {
	if len(targetTypes) == 0 {
		return false
	}
	receiver, _ := splitReceiverAndMethod(calleeName)
	if receiver == "" {
		return false
	}
	if !startsWithLower(receiver) {
		return false
	}
	className := extractClassName(callerID)
	if className == "" {
		return false
	}

	fieldInfo := InjectedField{}
	if cache != nil {
		fieldInfo = findFieldInfoCached(pool, cache, className, receiver)
	} else {
		fieldInfo = findFieldInfo(pool, className, receiver)
	}
	if matchesInjectedType(fieldInfo, receiver, targetTypes) {
		return true
	}

	var params []InjectedField
	if cache != nil {
		params = findConstructorInjectedParamsCached(pool, cache, className)
	} else {
		params = findConstructorInjectedParams(pool, className)
	}
	for _, param := range params {
		if !param.IsInjected {
			continue
		}
		if !strings.EqualFold(param.FieldName, receiver) {
			continue
		}
		if matchesTypeName(param.FieldType, targetTypes) {
			return true
		}
	}

	return false
}

func splitReceiverAndMethod(calleeName string) (string, string) {
	if calleeName == "" || !strings.Contains(calleeName, ".") {
		return "", ""
	}
	parts := strings.Split(calleeName, ".")
	if len(parts) < 2 {
		return "", ""
	}
	receiver := parts[len(parts)-2]
	method := parts[len(parts)-1]
	return strings.TrimSpace(receiver), strings.TrimSpace(method)
}

func startsWithLower(value string) bool {
	if value == "" {
		return false
	}
	r := value[0]
	return r >= 'a' && r <= 'z'
}

func matchesInjectedType(info InjectedField, receiver string, targetTypes map[string]bool) bool {
	if info.FieldType == "" {
		return false
	}
	if info.FieldName != "" && !strings.EqualFold(info.FieldName, receiver) {
		return false
	}
	return matchesTypeName(info.FieldType, targetTypes)
}

func matchesTypeName(typeName string, targetTypes map[string]bool) bool {
	normalized := strings.ToLower(normalizeTypeName(typeName))
	if normalized == "" {
		return false
	}
	return targetTypes[normalized]
}

func countFunctionsByNameCached(pool Queryer, cache *traceCache, repo, name string) int {
	if repo == "" || name == "" {
		return 0
	}
	key := strings.ToLower(repo) + ":" + strings.ToLower(name)
	if cache != nil {
		if count, ok := cache.functionNameCounts[key]; ok {
			return count
		}
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM functions fn
		JOIN files fi ON fn.file_id = fi.id
		JOIN repositories r ON fi.repo_id = r.id
		WHERE r.name = $1 AND LOWER(fn.name) = LOWER($2)
		  AND %s`, traceSnapshotClause("fi.snapshot_id", 3, includeLegacySnapshotsFromCache(cache)))
	var count int
	if err := pool.QueryRow(queryContext(pool), query, repo, name, snapshotIDsFromCache(cache)).Scan(&count); err != nil {
		return 0
	}
	if cache != nil {
		cache.functionNameCounts[key] = count
	}
	return count
}

func countFunctionFilesByNameCached(pool Queryer, cache *traceCache, repo, name string) int {
	if repo == "" || name == "" {
		return 0
	}
	key := strings.ToLower(repo) + ":" + strings.ToLower(name)
	if cache != nil {
		if count, ok := cache.functionFileCounts[key]; ok {
			return count
		}
	}
	clause, args := functionNameClause("fn.name", name, 1)
	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT f.path)
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE r.name = $%d AND (%s)
		  AND %s`, len(args)+1, clause, traceSnapshotClause("f.snapshot_id", len(args)+2, includeLegacySnapshotsFromCache(cache)))
	args = append(args, repo)
	args = append(args, snapshotIDsFromCache(cache))
	var count int
	if err := pool.QueryRow(queryContext(pool), query, args...).Scan(&count); err != nil {
		return 0
	}
	if cache != nil {
		cache.functionFileCounts[key] = count
	}
	return count
}

func expandDownstream(pool Queryer, parent *TreeNode, callerID string, visited map[string]bool, maxDepth int, limiter *nodeLimiter, cache *traceCache, resolve bool) {
	if queryContext(pool).Err() != nil {
		return
	}
	if parent.Depth >= maxDepth {
		return
	}
	childSeen := make(map[string]bool)
	tuning := DefaultTraceTuning(maxDepth)
	if cache != nil {
		tuning = cache.tuning
	}
	di := getDILimits(tuning, maxDepth)

	// Check for HTTP client calls (cross-service)
	httpCalls := findHttpCallsCached(cache, pool, callerID)
	for _, httpCall := range httpCalls {
		// Create a node for the HTTP call
		if !limiter.allow() {
			return
		}
		httpNode := &TreeNode{
			Name:           fmt.Sprintf("[%s %s]", httpCall.HttpMethod, httpCall.UrlPattern),
			Line:           httpCall.LineNumber,
			Depth:          parent.Depth + 1,
			EdgeType:       edgeTypeHTTP,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			HttpMethod:     httpCall.HttpMethod,
			HttpTarget:     httpCall.UrlPattern,
			Evidence:       newEvidence(evidenceSourceHttpClient, ""),
		}

		// Try to match this HTTP call to an endpoint
		endpoints := capEndpointMatches(matchEndpointCached(cache, pool, httpCall.HttpMethod, httpCall.UrlPattern, extractRepo(callerID)), limiter)
		endpointSeen := make(map[string]bool)
		for _, ep := range endpoints {
			if !limiter.allow() {
				return
			}
			endpointNode := &TreeNode{
				Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
				Repo:       ep.Repo,
				File:       ep.File,
				Line:       ep.LineNumber,
				Depth:      parent.Depth + 2,
				EdgeType:   edgeTypeHTTP,
				Confidence: confidenceMedium,
				Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
			}

			// Try to continue tracing from the endpoint handler
			handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
			if handlerID != "" && !visited[handlerID] {
				endpointNode.CallerID = handlerID
				visited[handlerID] = true
				expandDownstream(pool, endpointNode, handlerID, visited, maxDepth, limiter, cache, resolve)
			}

			httpNode.Children = appendUniqueNode(httpNode.Children, endpointNode, endpointSeen)
		}

		if len(httpNode.Children) == 0 {
			gatewaySeen := make(map[string]bool)
			routes := matchGatewayRoutesCached(cache, pool, httpCall.HttpMethod, httpCall.UrlPattern, extractRepo(callerID))
			for _, route := range routes {
				if !limiter.allow() {
					return
				}
				gatewayNode := &TreeNode{
					Name:       formatGatewayNodeName(route),
					Repo:       route.Repo,
					File:       route.File,
					Line:       route.LineNumber,
					Depth:      parent.Depth + 2,
					EdgeType:   edgeTypeHTTP,
					Confidence: confidenceMedium,
					Evidence:   newEvidence(evidenceSourceGatewayRoute, route.GatewayType),
				}
				for _, candidate := range gatewayBackendRouteCandidates(route) {
					for _, ep := range matchEndpoint(pool, firstNonEmpty(route.BackendMethod, route.PublicMethod), candidate, nil, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache)) {
						if !limiter.allow() {
							return
						}
						endpointNode := &TreeNode{
							Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
							Repo:       ep.Repo,
							File:       ep.File,
							Line:       ep.LineNumber,
							Depth:      parent.Depth + 3,
							EdgeType:   edgeTypeHTTP,
							Confidence: confidenceMedium,
							Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
						}
						handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
						if handlerID != "" && !visited[handlerID] {
							endpointNode.CallerID = handlerID
							visited[handlerID] = true
							expandDownstream(pool, endpointNode, handlerID, visited, maxDepth, limiter, cache, resolve)
						}
						gatewayNode.Children = appendUniqueNode(gatewayNode.Children, endpointNode, endpointSeen)
					}
				}
				if len(gatewayNode.Children) == 0 {
					if target := firstNonEmpty(combineGatewayBackendPath(route), route.BackendURL, route.BackendID); target != "" {
						gatewayNode.Children = append(gatewayNode.Children, &TreeNode{
							Name:       "→ backend " + target,
							Depth:      parent.Depth + 3,
							EdgeType:   edgeTypeHTTP,
							Confidence: confidenceLow,
							Evidence:   newEvidence(evidenceSourceGatewayRoute, target),
							HttpTarget: target,
						})
					}
				}
				httpNode.Children = appendUniqueNode(httpNode.Children, gatewayNode, gatewaySeen)
			}
		}

		if len(httpNode.Children) == 0 {
			// No endpoint matched, but still show the HTTP call
			httpNode.Name = fmt.Sprintf("[%s %s] (no matching endpoint or gateway)", httpCall.HttpMethod, httpCall.UrlPattern)
		}

		parent.Children = appendUniqueNode(parent.Children, httpNode, childSeen)
	}

	// Check for SQS producer calls (cross-service via message queue)
	sqsCalls := findSqsProducerCallsCached(cache, pool, callerID)
	for _, sqsCall := range sqsCalls {
		// Create a node for the SQS send
		if !limiter.allow() {
			return
		}
		sqsNode := &TreeNode{
			Name:           fmt.Sprintf("[SQS → %s]", sqsCall.QueueName),
			Line:           sqsCall.LineNumber,
			Depth:          parent.Depth + 1,
			EdgeType:       edgeTypeSQS,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			IsSqs:          true,
			QueueTarget:    sqsCall.QueueName,
			Evidence:       newEvidence(evidenceSourceSqsProducer, ""),
		}

		// Find consumers of this queue
		consumers := findSqsConsumersCached(cache, pool, sqsCall.QueueName, extractRepo(callerID))
		consumerSeen := make(map[string]bool)
		for _, consumer := range consumers {
			if !limiter.allow() {
				return
			}
			consumerNode := &TreeNode{
				Name:       formatQueueConsumerNodeName(consumer),
				File:       consumer.File,
				Repo:       consumer.Repo,
				Depth:      parent.Depth + 2,
				EdgeType:   edgeTypeSQS,
				Confidence: confidenceMedium,
				Evidence:   newEvidence(evidenceSourceSqsConsumer, ""),
			}

			// Try to continue tracing from the consumer's handler method
			handlerID := findConsumerHandler(pool, cache, consumer)
			if handlerID != "" && !visited[handlerID] {
				consumerNode.CallerID = handlerID
				visited[handlerID] = true
				expandDownstream(pool, consumerNode, handlerID, visited, maxDepth, limiter, cache, resolve)
			}

			sqsNode.Children = appendUniqueNode(sqsNode.Children, consumerNode, consumerSeen)
		}

		if len(sqsNode.Children) == 0 {
			sqsNode.Name = fmt.Sprintf("[SQS → %s] (no consumer found)", sqsCall.QueueName)
		}

		parent.Children = appendUniqueNode(parent.Children, sqsNode, childSeen)
	}

	// Get direct callees (prefer function_calls, fallback to pending_calls_edges)
	edges := getResolvedCallEdges(pool, cache, callerID)
	for _, edge := range edges {
		calleeName := edge.CalleeName
		lineNum := edge.LineNumber
		calleeCallerID := edge.CalleeCallerID

		// Skip already visited or generic methods
		if calleeCallerID != "" {
			if visited[calleeCallerID] {
				continue
			}
			visited[calleeCallerID] = true
		} else {
			if visited[calleeName] || isGenericMethod(calleeName) {
				continue
			}
			visited[calleeName] = true
		}

		// Check if this is an HTTP interface method (Retrofit/Feign style)
		httpInterfaceMethods, nameOnly := checkForHttpInterfaceMethodCached(cache, pool, calleeName)
		if len(httpInterfaceMethods) > 0 {
			for _, httpMethod := range httpInterfaceMethods {
				// Create a node for the HTTP interface call
				httpNode := &TreeNode{
					Name:           fmt.Sprintf("[%s %s] via %s.%s", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName),
					Line:           lineNum,
					Depth:          parent.Depth + 1,
					EdgeType:       edgeTypeHTTP,
					Confidence:     httpInterfaceConfidence(nameOnly),
					IsCrossService: true,
					HttpMethod:     httpMethod.HttpMethod,
					Evidence:       evidenceForHTTPInterface(httpMethod),
				}

				// Try to match to backend endpoint
				endpoints := capEndpointMatches(matchEndpointCached(cache, pool, httpMethod.HttpMethod, httpMethod.UrlPattern, extractRepo(callerID)), limiter)
				endpointSeen := make(map[string]bool)
				for _, ep := range endpoints {
					if !limiter.allow() {
						return
					}
					endpointNode := &TreeNode{
						Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
						Repo:       ep.Repo,
						File:       ep.File,
						Line:       ep.LineNumber,
						Depth:      parent.Depth + 2,
						EdgeType:   edgeTypeHTTP,
						Confidence: confidenceMedium,
						Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
					}

					// Try to continue tracing from the endpoint handler
					handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
					if handlerID != "" && !visited[handlerID] {
						visited[handlerID] = true
						expandDownstream(pool, endpointNode, handlerID, visited, maxDepth, limiter, cache, resolve)
					}

					httpNode.Children = appendUniqueNode(httpNode.Children, endpointNode, endpointSeen)
				}

				if len(httpNode.Children) == 0 {
					gatewaySeen := make(map[string]bool)
					routes := matchGatewayRoutesCached(cache, pool, httpMethod.HttpMethod, httpMethod.UrlPattern, extractRepo(callerID))
					for _, route := range routes {
						if !limiter.allow() {
							return
						}
						gatewayNode := &TreeNode{
							Name:       formatGatewayNodeName(route),
							Repo:       route.Repo,
							File:       route.File,
							Line:       route.LineNumber,
							Depth:      parent.Depth + 2,
							EdgeType:   edgeTypeHTTP,
							Confidence: confidenceMedium,
							Evidence:   newEvidence(evidenceSourceGatewayRoute, route.GatewayType),
						}
						for _, candidate := range gatewayBackendRouteCandidates(route) {
							for _, ep := range matchEndpoint(pool, firstNonEmpty(route.BackendMethod, route.PublicMethod), candidate, nil, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache)) {
								if !limiter.allow() {
									return
								}
								endpointNode := &TreeNode{
									Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
									Repo:       ep.Repo,
									File:       ep.File,
									Line:       ep.LineNumber,
									Depth:      parent.Depth + 3,
									EdgeType:   edgeTypeHTTP,
									Confidence: confidenceMedium,
									Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
								}
								handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
								if handlerID != "" && !visited[handlerID] {
									visited[handlerID] = true
									expandDownstream(pool, endpointNode, handlerID, visited, maxDepth, limiter, cache, resolve)
								}
								gatewayNode.Children = appendUniqueNode(gatewayNode.Children, endpointNode, endpointSeen)
							}
						}
						if len(gatewayNode.Children) == 0 {
							if target := firstNonEmpty(combineGatewayBackendPath(route), route.BackendURL, route.BackendID); target != "" {
								gatewayNode.Children = append(gatewayNode.Children, &TreeNode{
									Name:       "→ backend " + target,
									Depth:      parent.Depth + 3,
									EdgeType:   edgeTypeHTTP,
									Confidence: confidenceLow,
									Evidence:   newEvidence(evidenceSourceGatewayRoute, target),
									HttpTarget: target,
								})
							}
						}
						httpNode.Children = appendUniqueNode(httpNode.Children, gatewayNode, gatewaySeen)
					}
				}

				if len(httpNode.Children) == 0 {
					httpNode.Name = fmt.Sprintf("[%s %s] via %s.%s (no matching endpoint or gateway)", httpMethod.HttpMethod, httpMethod.UrlPattern, httpMethod.InterfaceName, httpMethod.MethodName)
				}

				parent.Children = appendUniqueNode(parent.Children, httpNode, childSeen)
			}
			continue
		}

		if !limiter.allow() {
			return
		}
		child := &TreeNode{
			Name:             calleeName,
			Line:             lineNum,
			Depth:            parent.Depth + 1,
			EdgeType:         edgeTypeCall,
			Confidence:       callConfidence(edge),
			UnresolvedReason: edge.UnresolvedReason,
			Evidence:         evidenceForCallEdge(edge),
		}

		if calleeCallerID != "" {
			child.File = extractFile(calleeCallerID)
			child.Repo = extractRepo(calleeCallerID)
			child.CallerID = calleeCallerID
			expandDownstream(pool, child, calleeCallerID, visited, maxDepth, limiter, cache, resolve)
			parent.Children = appendUniqueNode(parent.Children, child, childSeen)
			continue
		}

		if !resolve {
			parent.Children = appendUniqueNode(parent.Children, child, childSeen)
			continue
		}

		// Check if this is a call through an interface - resolve to implementations
		typeName := calleeName
		if idx := strings.Index(typeName, "."); idx > 0 {
			typeName = typeName[:idx]
		}
		if resolve && edge.Source == edgeSourceFunctionCalls && parent.Depth < di.maxDepth && looksLikeTypeName(typeName) {
			implCallerIDs := resolveInterfaceMethodCallCached(pool, cache, calleeName)
			implCallerIDs = limitImplIDs(implCallerIDs, extractRepo(callerID), di.maxImpls)
			if len(implCallerIDs) > 0 {
				// We have interface implementations - trace through each
				for _, implID := range implCallerIDs {
					if visited[implID] {
						continue
					}
					visited[implID] = true

					if !limiter.allow() {
						return
					}
					implNode := &TreeNode{
						Name:       extractMethod(implID) + " (impl)",
						File:       extractFile(implID),
						Repo:       extractRepo(implID),
						Depth:      parent.Depth + 1,
						EdgeType:   edgeTypeResolve,
						Confidence: confidenceMedium,
						CallerID:   implID,
						Evidence:   newEvidence(evidenceSourceInterfaceImpl, ""),
					}
					if shouldExpandResolved(pool, cache, implID, parent.Repo) {
						expandDownstream(pool, implNode, implID, visited, maxDepth, limiter, cache, resolve)
					}
					parent.Children = appendUniqueNode(parent.Children, implNode, childSeen)
				}
				continue
			}
		}

		// Check if this is a call on an injected field (DI resolution)
		// Extract current class from caller_id for DI context
		currentClass := extractClassName(callerID)
		if resolve && edge.Source == edgeSourceFunctionCalls && currentClass != "" && strings.Contains(calleeName, ".") && parent.Depth < di.maxDepth {
			diCallerIDs := resolveInjectedFieldCallCached(pool, cache, currentClass, calleeName)
			diCallerIDs = limitImplIDs(diCallerIDs, extractRepo(callerID), di.maxImpls)
			if len(diCallerIDs) > 0 {
				for _, diID := range diCallerIDs {
					if visited[diID] {
						continue
					}
					visited[diID] = true

					if !limiter.allow() {
						return
					}
					diNode := &TreeNode{
						Name:       extractMethod(diID) + " (injected)",
						File:       extractFile(diID),
						Repo:       extractRepo(diID),
						Depth:      parent.Depth + 1,
						Injected:   true,
						EdgeType:   edgeTypeResolve,
						Confidence: confidenceMedium,
						CallerID:   diID,
						Evidence:   newEvidence(evidenceSourceInjectedField, ""),
					}
					if shouldExpandResolved(pool, cache, diID, parent.Repo) {
						expandDownstream(pool, diNode, diID, visited, maxDepth, limiter, cache, resolve)
					}
					parent.Children = appendUniqueNode(parent.Children, diNode, childSeen)
				}
				continue
			}
		}

		// Try to find the implementation to continue traversal (direct class method)
		implID := findImplementationCached(pool, cache, calleeName)
		if implID != "" && !visited[implID] {
			visited[implID] = true
			child.File = extractFile(implID)
			child.Repo = extractRepo(implID)
			child.CallerID = implID
			if edge.Source != edgeSourceFunctionCalls {
				child.EdgeType = edgeTypeResolve
				child.Confidence = confidenceMedium
				child.Evidence = newEvidence(evidenceSourceImplementation, edge.Source)
			}
			expandDownstream(pool, child, implID, visited, maxDepth, limiter, cache, resolve)
		}

		parent.Children = appendUniqueNode(parent.Children, child, childSeen)
	}
}

// checkForHttpInterfaceMethodCached maps an unresolved call to declared HTTP
// client interface methods (Feign, Retrofit, Spring HTTP interfaces). An
// Interface.method match is preferred. Otherwise the receiver is usually a field
// or variable name, so only the method name is known: that fallback is accepted
// only when exactly one client interface declares the name, and nameOnly tells
// callers to label the link low confidence.
func checkForHttpInterfaceMethodCached(cache *traceCache, pool Queryer, calleeName string) (methods []HttpInterfaceMethod, nameOnly bool) {
	if cache == nil {
		// HTTP interface resolution needs the workspace-scoped index; there is no
		// unscoped fallback.
		return nil, false
	}

	parts := strings.Split(calleeName, ".")
	if len(parts) >= 2 {
		typeName := parts[len(parts)-2]
		methodName := parts[len(parts)-1]
		fullKey := strings.ToLower(typeName + "." + methodName)
		if methods, ok := cache.httpInterfaceByFull[fullKey]; ok {
			return methods, false
		}
	}

	if len(parts) >= 1 {
		methodName := parts[len(parts)-1]
		if methods, ok := cache.httpInterfaceByName[strings.ToLower(methodName)]; ok && declaredByOneInterface(methods) {
			return methods, true
		}
	}

	return nil, false
}

// httpInterfaceConfidence labels an HTTP-interface hop: a link attributed by
// method name alone is a weaker inference than an Interface.method match.
func httpInterfaceConfidence(nameOnly bool) string {
	if nameOnly {
		return confidenceLow
	}
	return confidenceMedium
}

// declaredByOneInterface reports whether all methods belong to a single client
// interface; a name shared by several interfaces cannot be attributed.
func declaredByOneInterface(methods []HttpInterfaceMethod) bool {
	if len(methods) == 0 {
		return false
	}
	first := methods[0].Repo + "\x00" + methods[0].File + "\x00" + methods[0].InterfaceName
	for _, m := range methods[1:] {
		if m.Repo+"\x00"+m.File+"\x00"+m.InterfaceName != first {
			return false
		}
	}
	return true
}

func traceUpstreamWithMode(pool Queryer, funcName string, callerIDs []string, maxDepth, maxNodes int, tuning TraceTuning) (roots []*TreeNode, mode string, stats LimitStats) {
	maxDepth = min(maxDepth, MaxDepth)
	if queryContext(pool).Err() != nil {
		return
	}
	startTrace := time.Now()
	visited := make(map[string]bool)
	defer func() {
		enforceEdgeEvidenceRequirements(roots)
	}()
	allowedRepo := ""
	limiter := newNodeLimiter(maxNodes)
	defer func() {
		stats = limiter.stats()
	}()
	cache := newTraceCache(pool, tuning)

	if len(callerIDs) == 1 {
		if calleeID, ok := findFunctionIDByCallerID(pool, callerIDs[0], tuning.SnapshotIDs, tuning.IncludeLegacySnapshots); ok {
			nodes := traceUpstreamByID(pool, calleeID, maxDepth, limiter, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)
			if len(nodes) > 0 {
				scheduleNodes := findScheduleTriggersForCaller(pool, callerIDs[0], 0, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)
				for _, schedule := range scheduleNodes {
					schedule.Depth = 0
					nodes = append(nodes, schedule)
				}
				log.Printf("trace upstream time=%s mode=id roots=%d", time.Since(startTrace), len(nodes))
				return nodes, "id", stats
			}
		}
	}
	mode = "name"

	// Build a map of target functions: method name -> list of (caller_id, file) pairs
	// This helps us distinguish between different functions with the same name
	targetFunctions := make(map[string][]struct {
		callerID string
		file     string
	})

	// Add the initial search term if present.
	if funcName != "" {
		targetFunctions[funcName] = []struct {
			callerID string
			file     string
		}{}
	}

	// Add all found caller_ids
	for _, id := range callerIDs {
		method := extractMethod(id)
		file := extractFile(id)
		if method != "" {
			targetFunctions[method] = append(targetFunctions[method], struct {
				callerID string
				file     string
			}{id, file})

			// Also add just the method name without class prefix
			if strings.Contains(method, ".") {
				parts := strings.Split(method, ".")
				simpleName := parts[len(parts)-1]
				targetFunctions[simpleName] = append(targetFunctions[simpleName], struct {
					callerID string
					file     string
				}{id, file})
			}
		}
	}

	targetTypes := buildTargetTypes(callerIDs)

	// Find who calls these specific methods
	for methodName, targets := range targetFunctions {
		isQualified := strings.Contains(methodName, ".")
		var files []string
		singleTargetFile := ""
		if len(targets) > 0 && !isQualified {
			fileSet := make(map[string]bool)
			var orderedFiles []string
			for _, target := range targets {
				if target.file == "" || fileSet[target.file] {
					continue
				}
				fileSet[target.file] = true
				orderedFiles = append(orderedFiles, target.file)
			}
			if len(fileSet) == 1 && len(orderedFiles) == 1 {
				singleTargetFile = orderedFiles[0]
			}
			if len(fileSet) > 1 {
				files = orderedFiles
			}
		}

		if !shouldSearchUpstreamName(pool, cache, allowedRepo, methodName) {
			continue
		}

		clause, args := calleeNameClause(methodName, 1)
		if len(targets) > 0 && isQualified {
			clause, args = calleeNameExactClause(methodName, 1)
		}
		query := fmt.Sprintf(`SELECT DISTINCT caller_id, callee_name, line_number FROM pending_calls_edges
		          WHERE (%s)`, clause)
		if len(files) > 0 {
			query += fmt.Sprintf(" AND split_part(caller_id, ':', 2) = ANY($%d)", len(args)+1)
			args = append(args, files)
		}
		if allowedRepo != "" {
			query += fmt.Sprintf(" AND split_part(caller_id, ':', 1) = $%d", len(args)+1)
			args = append(args, allowedRepo)
		}
		query += " AND " + traceSnapshotClause("snapshot_id", len(args)+1, tuning.IncludeLegacySnapshots)
		args = append(args, tuning.SnapshotIDs)
		query += " LIMIT 50"
		rows, err := pool.Query(queryContext(pool), query, args...)
		if err != nil {
			continue
		}
		type pendingRootRow struct {
			callerID   string
			calleeName string
			lineNum    int
		}
		var pendingRows []pendingRootRow
		for rows.Next() {
			var row pendingRootRow
			if err := rows.Scan(&row.callerID, &row.calleeName, &row.lineNum); err != nil {
				continue
			}
			pendingRows = append(pendingRows, row)
		}
		rows.Close()

		for _, row := range pendingRows {
			callerID := row.callerID
			calleeName := row.calleeName
			lineNum := row.lineNum

			// Skip if it's one of our starting points or already visited
			isStartingPoint := false
			for _, id := range callerIDs {
				if callerID == id {
					isStartingPoint = true
					break
				}
			}
			if isStartingPoint || visited[callerID] {
				continue
			}

			// IMPORTANT FIX: Verify this call actually leads to one of our target functions
			// For same-file calls, we can be certain which function is being called
			callerFile := extractFile(callerID)
			isValidCall := false

			if len(targets) == 0 {
				// No specific targets, include all (for initial search term)
				isValidCall = true
			} else if len(targets) == 1 || singleTargetFile != "" {
				// Only one target with this name - no ambiguity, include all calls
				isValidCall = true
			} else {
				// Multiple functions with the same name - apply file filtering to disambiguate
				for _, target := range targets {
					if target.file == "" {
						// No file context, include it (initial search term)
						isValidCall = true
						break
					} else if callerFile == target.file {
						// Same file - definitely calling the target function
						isValidCall = true
						break
					}
					// Note: For cross-file calls with ambiguity, we're being conservative
					// (would need import resolution to handle these correctly)
				}
			}

			if !isValidCall {
				continue
			}
			allowSingleTargetNameMatch := !isQualified && (len(targets) == 1 || singleTargetFile != "")
			if !allowSingleTargetNameMatch &&
				!shouldIncludeNameBasedEdge(pool, cache, callerID, methodName) &&
				!matchesInjectedReceiver(pool, cache, callerID, calleeName, targetTypes) {
				continue
			}

			visited[callerID] = true

			if !limiter.allow() {
				break
			}
			node := &TreeNode{
				Name:       extractMethod(callerID),
				File:       extractFile(callerID),
				Repo:       extractRepo(callerID),
				Line:       lineNum,
				Depth:      0,
				EdgeType:   edgeTypeCall,
				Confidence: confidenceLow,
				CallerID:   callerID,
				Evidence:   newEvidence(evidenceSourcePendingCaller, "name_based"),
			}
			expandUpstream(pool, cache, node, callerID, visited, maxDepth, allowedRepo, limiter)
			roots = append(roots, node)
		}
	}
	for _, id := range callerIDs {
		scheduleNodes := findScheduleTriggersForCaller(pool, id, 0, tuning.SnapshotIDs, tuning.IncludeLegacySnapshots)
		for _, schedule := range scheduleNodes {
			schedule.Depth = 0
			roots = append(roots, schedule)
		}
	}

	log.Printf("trace upstream time=%s mode=%s roots=%d", time.Since(startTrace), mode, len(roots))
	return roots, mode, stats
}

type callEdgeRow struct {
	CallerID int64
	Line     int
	Name     string
	File     string
	Repo     string
}

// queryCallersByCalleeCallerID lists distinct callers of a callee id in a stable
// order. It asks for limit+1 rows so more reports whether the cap cut the list.
func queryCallersByCalleeCallerID(pool Queryer, calleeCallerID string, limit int, snapshotIDs []int64, includeLegacy bool) (out []callEdgeRow, more bool, err error) {
	query := fmt.Sprintf(`
		SELECT caller_id, line_number FROM (
			SELECT DISTINCT ON (caller_id) caller_id, line_number
			FROM trace_call_edges
			WHERE callee_id = $1
			  AND %s
			ORDER BY caller_id, line_number
		) callers
		ORDER BY caller_id
		LIMIT $3`, traceSnapshotClause("snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, calleeCallerID, snapshotIDs, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var callerID string
		var lineNum sql.NullInt64
		if err := rows.Scan(&callerID, &lineNum); err != nil {
			return nil, false, err
		}
		repo, file, name := ParseCallerID(callerID)
		if repo == "" || file == "" || name == "" {
			continue
		}
		row := callEdgeRow{
			Name: name,
			File: file,
			Repo: repo,
		}
		if lineNum.Valid {
			row.Line = int(lineNum.Int64)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		out, more = out[:limit], true
	}
	return out, more, nil
}

// queryFunctionCallersByCallee lists distinct caller functions of calleeID in a
// stable order (repo, file, name, id). It asks for limit+1 rows so more reports
// whether the cap cut the list; scan or iteration errors are returned, never
// skipped, because a silently shortened caller list reads as "no more callers".
func queryFunctionCallersByCallee(pool Queryer, calleeID int64, limit int, snapshotIDs []int64, includeLegacy bool) (out []callEdgeRow, more bool, err error) {
	query := fmt.Sprintf(`
		SELECT caller_function_id, line_number, name, path, repo FROM (
			SELECT DISTINCT ON (fc.caller_function_id)
			       fc.caller_function_id, COALESCE(fc.line_number, 0) AS line_number,
			       fn.name AS name, fi.path AS path, r.name AS repo
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files fi ON fi.id = fn.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE fc.callee_function_id = $1
			  AND %s
			ORDER BY fc.caller_function_id, fc.line_number
		) callers
		ORDER BY repo, path, name, caller_function_id
		LIMIT $3`, traceSnapshotClause("fi.snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, calleeID, snapshotIDs, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var row callEdgeRow
		if err := rows.Scan(&row.CallerID, &row.Line, &row.Name, &row.File, &row.Repo); err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		out, more = out[:limit], true
	}
	return out, more, nil
}

// traceUpstreamByID lists callers breadth-first, so a function reachable by
// several paths is placed at its shortest depth. A depth-first walk marks a node
// visited the first time any path reaches it, which can be at maxDepth through a
// long chain; its callers are then never listed even though a short path exists.
// Fan-out caps and failed queries mark the limiter incomplete.
func traceUpstreamByID(pool Queryer, calleeID int64, maxDepth int, limiter *nodeLimiter, snapshotIDs []int64, includeLegacy bool) []*TreeNode {
	const rootFanout, childFanout = 50, 20
	visited := map[int64]bool{calleeID: true}
	fetch := func(id int64, fanout int) []callEdgeRow {
		rows, more, err := queryFunctionCallersByCallee(pool, id, fanout, snapshotIDs, includeLegacy)
		if err != nil {
			log.Printf("WARN: trace upstream caller lookup failed for function %d: %v", id, err)
			limiter.markIncomplete()
			return nil
		}
		if more {
			limiter.markIncomplete()
		}
		return rows
	}
	newNode := func(row callEdgeRow, depth int) *TreeNode {
		return &TreeNode{
			Name:       row.Name,
			File:       row.File,
			Repo:       row.Repo,
			Line:       row.Line,
			Depth:      depth,
			EdgeType:   edgeTypeCall,
			Confidence: confidenceHigh,
			CallerID:   buildCallerID(row.Repo, row.File, row.Name),
			Evidence:   newEvidence(evidenceSourceFunctionCaller, ""),
		}
	}
	type pending struct {
		node *TreeNode
		id   int64
	}
	var roots []*TreeNode
	var frontier []pending
	for _, row := range fetch(calleeID, rootFanout) {
		if visited[row.CallerID] {
			continue
		}
		visited[row.CallerID] = true
		if !limiter.allow() {
			return roots
		}
		node := newNode(row, 0)
		roots = append(roots, node)
		frontier = append(frontier, pending{node: node, id: row.CallerID})
	}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []pending
		for _, parent := range frontier {
			if queryContext(pool).Err() != nil {
				limiter.markIncomplete()
				return roots
			}
			childSeen := make(map[string]bool)
			for _, row := range fetch(parent.id, childFanout) {
				if visited[row.CallerID] {
					continue
				}
				visited[row.CallerID] = true
				if !limiter.allow() {
					return roots
				}
				child := newNode(row, depth+1)
				parent.node.Children = appendUniqueNode(parent.node.Children, child, childSeen)
				next = append(next, pending{node: child, id: row.CallerID})
			}
		}
		frontier = next
	}
	return roots
}

func expandUpstream(pool Queryer, cache *traceCache, parent *TreeNode, callerID string, visited map[string]bool, maxDepth int, allowedRepo string, limiter *nodeLimiter) {
	if queryContext(pool).Err() != nil {
		return
	}
	if parent.Depth >= maxDepth {
		return
	}
	childSeen := make(map[string]bool)

	methodName := extractMethod(callerID)
	targetFile := extractFile(callerID)
	if methodName == "" {
		return
	}

	// Check if there are multiple definition files for this method name (ambiguity)
	repo := allowedRepo
	if repo == "" {
		repo = extractRepo(callerID)
	}
	if !shouldSearchUpstreamName(pool, cache, repo, methodName) {
		return
	}
	fileCount := countFunctionFilesByNameCached(pool, cache, repo, methodName)
	hasAmbiguity := fileCount > 1

	// Find who calls this method
	clause, args := calleeNameClause(methodName, 1)
	query := fmt.Sprintf(`SELECT DISTINCT caller_id, line_number FROM pending_calls_edges
	          WHERE (%s)`, clause)
	if allowedRepo != "" {
		query += fmt.Sprintf(" AND split_part(caller_id, ':', 1) = $%d", len(args)+1)
		args = append(args, allowedRepo)
	}
	query += " AND " + traceSnapshotClause("snapshot_id", len(args)+1, includeLegacySnapshotsFromCache(cache))
	args = append(args, snapshotIDsFromCache(cache))
	query += " LIMIT 20"
	rows, err := pool.Query(queryContext(pool), query, args...)
	if err != nil {
		return
	}
	type pendingCallerRow struct {
		upstreamID string
		lineNum    int
	}
	var pendingRows []pendingCallerRow
	for rows.Next() {
		var row pendingCallerRow
		if err := rows.Scan(&row.upstreamID, &row.lineNum); err != nil {
			continue
		}
		pendingRows = append(pendingRows, row)
	}
	rows.Close()

	for _, row := range pendingRows {
		upstreamID := row.upstreamID
		lineNum := row.lineNum

		if visited[upstreamID] || upstreamID == callerID {
			continue
		}

		// Apply same-file filtering only when there's ambiguity
		if hasAmbiguity {
			upstreamFile := extractFile(upstreamID)
			if upstreamFile != targetFile {
				// Skip cross-file calls when ambiguous to prevent false positives
				continue
			}
		}
		if !shouldIncludeNameBasedEdge(pool, cache, upstreamID, methodName) {
			continue
		}

		visited[upstreamID] = true

		if !limiter.allow() {
			break
		}
		child := &TreeNode{
			Name:       extractMethod(upstreamID),
			File:       extractFile(upstreamID),
			Repo:       extractRepo(upstreamID),
			Line:       lineNum,
			Depth:      parent.Depth + 1,
			EdgeType:   edgeTypeCall,
			Confidence: confidenceLow,
			CallerID:   upstreamID,
			Evidence:   newEvidence(evidenceSourcePendingCaller, "name_based"),
		}
		expandUpstream(pool, cache, child, upstreamID, visited, maxDepth, allowedRepo, limiter)
		parent.Children = appendUniqueNode(parent.Children, child, childSeen)
	}
}

func findImplementation(pool Queryer, funcName string) string {
	if funcName == "" {
		return ""
	}
	if callerID := findUniqueCallerIDByFunctionName(pool, funcName); callerID != "" {
		return callerID
	}
	return ""
}

func findImplementationCached(pool Queryer, cache *traceCache, funcName string) string {
	if cache == nil {
		return findImplementation(pool, funcName)
	}
	key := strings.ToLower(funcName)
	if callerID, ok := cache.implementationByName[key]; ok {
		return callerID
	}
	if callerID := findUniqueCallerIDByFunctionNameCached(pool, cache, funcName); callerID != "" {
		cache.implementationByName[key] = callerID
		return callerID
	}
	cache.implementationByName[key] = ""
	return ""
}

func extractRepo(callerID string) string {
	parts := strings.Split(callerID, ":")
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

func extractFile(callerID string) string {
	parts := strings.Split(callerID, ":")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func extractMethod(callerID string) string {
	parts := strings.Split(callerID, ":")
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func buildCallerID(repo, file, name string) string {
	if repo == "" || file == "" || name == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s", repo, file, name)
}

// BuildCallerID creates a stable caller ID from repo, file, and function name.
func BuildCallerID(repo, file, name string) string {
	return buildCallerID(repo, file, name)
}

// ParseCallerID splits a caller ID into repo, file, and function name.
func ParseCallerID(callerID string) (string, string, string) {
	return extractRepo(callerID), extractFile(callerID), extractMethod(callerID)
}

// FindCallerIDsByFunctionIDs resolves function IDs to caller IDs.
func FindCallerIDsByFunctionIDs(pool Queryer, ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	query := `
		SELECT fn.id, r.name, f.path, fn.name
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE fn.id = ANY($1)`
	rows, err := pool.Query(queryContext(pool), query, ids)
	if err != nil {
		return nil
	}
	defer rows.Close()

	resolved := make(map[int64]string)
	for rows.Next() {
		var id int64
		var repo, file, name string
		if err := rows.Scan(&id, &repo, &file, &name); err != nil {
			continue
		}
		resolved[id] = buildCallerID(repo, file, name)
	}
	var out []string
	seen := make(map[string]bool)
	for _, id := range ids {
		callerID := resolved[id]
		if callerID == "" || seen[callerID] {
			continue
		}
		seen[callerID] = true
		out = append(out, callerID)
	}
	return out
}

// FindFunctionIDsByCallerIDs resolves caller IDs to function IDs.
func FindFunctionIDsByCallerIDs(pool Queryer, callerIDs []string) map[string]int64 {
	return FindFunctionIDsByCallerIDsForSnapshots(pool, callerIDs, nil)
}

func FindFunctionIDsByCallerIDsForSnapshots(pool Queryer, callerIDs []string, snapshotIDs []int64) map[string]int64 {
	return FindFunctionIDsByCallerIDsForSnapshotFilter(pool, callerIDs, snapshotIDs, false)
}

func FindFunctionIDsByCallerIDsForSnapshotFilter(pool Queryer, callerIDs []string, snapshotIDs []int64, includeLegacy bool) map[string]int64 {
	if len(callerIDs) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	var repos []string
	var files []string
	var names []string
	for _, callerID := range callerIDs {
		if callerID == "" || seen[callerID] {
			continue
		}
		repo, file, name := ParseCallerID(callerID)
		if repo == "" || file == "" || name == "" {
			continue
		}
		seen[callerID] = true
		ids = append(ids, callerID)
		repos = append(repos, repo)
		files = append(files, file)
		names = append(names, name)
	}
	if len(ids) == 0 {
		return nil
	}

	query := fmt.Sprintf(`
		WITH input AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[])
				AS t(caller_id, repo, file, name)
		)
		SELECT t.caller_id, fn.id, fn.start_line, fn.end_line
		FROM input t
		JOIN repositories r ON r.name = t.repo
		JOIN files f ON f.repo_id = r.id AND f.path = t.file
		JOIN functions fn ON fn.file_id = f.id AND fn.name = t.name
		WHERE %s`, traceSnapshotClause("f.snapshot_id", 5, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, ids, repos, files, names, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	candidatesByCaller := make(map[string][]functionIDCandidate)
	for rows.Next() {
		var callerID string
		var id int64
		var startLine, endLine int
		if err := rows.Scan(&callerID, &id, &startLine, &endLine); err != nil {
			continue
		}
		candidatesByCaller[callerID] = append(candidatesByCaller[callerID], functionIDCandidate{
			id:        id,
			startLine: startLine,
			endLine:   endLine,
		})
	}
	out := make(map[string]int64)
	for _, callerID := range ids {
		candidates := candidatesByCaller[callerID]
		id, ok := pickDeterministicFunctionID(candidates)
		if !ok {
			continue
		}
		out[callerID] = id
	}
	return out
}

func findFunctionIDsByCallerIDsCached(pool Queryer, cache *traceCache, callerIDs []string) map[string]int64 {
	if cache == nil {
		return FindFunctionIDsByCallerIDs(pool, callerIDs)
	}
	resolved := make(map[string]int64, len(callerIDs))
	var missing []string
	for _, callerID := range callerIDs {
		if callerID == "" {
			continue
		}
		if id, ok := cache.functionIDByCallerID[callerID]; ok {
			if id != 0 {
				resolved[callerID] = id
			}
			continue
		}
		missing = append(missing, callerID)
	}
	if len(missing) > 0 {
		found := FindFunctionIDsByCallerIDsForSnapshotFilter(pool, missing, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
		for callerID, id := range found {
			cache.functionIDByCallerID[callerID] = id
			if id != 0 {
				resolved[callerID] = id
			}
		}
		for _, callerID := range missing {
			if _, ok := cache.functionIDByCallerID[callerID]; !ok {
				cache.functionIDByCallerID[callerID] = 0
			}
		}
	}
	return resolved
}

func findFunctionIDByCallerID(pool Queryer, callerID string, snapshotIDs []int64, includeLegacy bool) (int64, bool) {
	repo := extractRepo(callerID)
	file := extractFile(callerID)
	name := extractMethod(callerID)
	if repo == "" || file == "" || name == "" {
		return 0, false
	}
	query := fmt.Sprintf(`
		SELECT fn.id, fn.start_line, fn.end_line
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE r.name = $1 AND f.path = $2 AND fn.name = $3
		  AND %s
	`, traceSnapshotClause("f.snapshot_id", 4, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, repo, file, name, snapshotIDs)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var candidates []functionIDCandidate
	for rows.Next() {
		var id int64
		var startLine, endLine int
		if err := rows.Scan(&id, &startLine, &endLine); err != nil {
			continue
		}
		candidates = append(candidates, functionIDCandidate{
			id:        id,
			startLine: startLine,
			endLine:   endLine,
		})
	}
	return pickDeterministicFunctionID(candidates)
}

func callerIDFunctionClause(funcName string, startArg int) (string, []interface{}) {
	return functionNameClause("split_part(caller_id, ':', 3)", funcName, startArg)
}

func calleeNameClause(funcName string, startArg int) (string, []interface{}) {
	return functionNameClause("callee_name", funcName, startArg)
}

func calleeNameExactClause(funcName string, startArg int) (string, []interface{}) {
	return fmt.Sprintf("callee_name = $%d", startArg), []interface{}{funcName}
}

func functionNameClause(column, funcName string, startArg int) (string, []interface{}) {
	literal := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(funcName)
	if strings.Contains(funcName, ".") {
		return fmt.Sprintf("%s ILIKE $%d", column, startArg), []interface{}{literal}
	}
	return fmt.Sprintf("%s ILIKE $%d OR %s ILIKE $%d", column, startArg, column, startArg+1),
		[]interface{}{literal, "%." + literal}
}

func findCallerIDsByFunctionName(pool Queryer, funcName string) []string {
	return findCallerIDsByFunctionNameForSnapshots(pool, funcName, nil)
}

func findCallerIDsByFunctionNameForSnapshots(pool Queryer, funcName string, snapshotIDs []int64) []string {
	return findCallerIDsByFunctionNameForSnapshotFilter(pool, funcName, snapshotIDs, false)
}

func findCallerIDsByFunctionNameForSnapshotFilter(pool Queryer, funcName string, snapshotIDs []int64, includeLegacy bool) []string {
	clause, args := functionNameClause("f.name", funcName, 1)
	query := fmt.Sprintf(`
		SELECT r.name, fi.path, f.name
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE (%s)
		  AND %s
		ORDER BY r.name, fi.path, f.start_line, f.end_line, f.id
		LIMIT %d`, clause, traceSnapshotClause("fi.snapshot_id", len(args)+1, includeLegacy), traceLookupMatchLimit)
	args = append(args, snapshotIDs)
	rows, err := pool.Query(queryContext(pool), query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ids []string
	seen := make(map[string]bool)
	for rows.Next() {
		var repo, file, name string
		if err := rows.Scan(&repo, &file, &name); err != nil {
			continue
		}
		if id := buildCallerID(repo, file, name); id != "" {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func findUniqueCallerIDByFunctionName(pool Queryer, funcName string) string {
	return pickUniqueCallerID(findCallerIDsByFunctionName(pool, funcName))
}

func findUniqueCallerIDByFunctionNameCached(pool Queryer, cache *traceCache, funcName string) string {
	if cache == nil {
		return findUniqueCallerIDByFunctionName(pool, funcName)
	}
	key := strings.ToLower(funcName)
	if callerID, ok := cache.uniqueCallerIDByName[key]; ok {
		return callerID
	}
	callerID := pickUniqueCallerID(findCallerIDsByFunctionNameForSnapshotFilter(pool, funcName, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache)))
	cache.uniqueCallerIDByName[key] = callerID
	return callerID
}

func findCallerIDByRepoFileFunc(pool Queryer, repo, file, funcName string, snapshotIDs []int64) string {
	return findCallerIDByRepoFileFuncForSnapshotFilter(pool, repo, file, funcName, snapshotIDs, false)
}

func findCallerIDByRepoFileFuncForSnapshotFilter(pool Queryer, repo, file, funcName string, snapshotIDs []int64, includeLegacy bool) string {
	if repo == "" || file == "" || funcName == "" {
		return ""
	}
	query := fmt.Sprintf(`
		SELECT r.name, f.path, fn.name
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE r.name = $1 AND f.path = $2 AND fn.name = $3
		  AND %s
		ORDER BY fn.start_line, fn.end_line, fn.id
		LIMIT 1`, traceSnapshotClause("f.snapshot_id", 4, includeLegacy))
	var repoName, filePath, name string
	if err := pool.QueryRow(queryContext(pool), query, repo, file, funcName, snapshotIDs).Scan(&repoName, &filePath, &name); err != nil {
		return ""
	}
	return buildCallerID(repoName, filePath, name)
}

func findCallerIDByRepoFileFuncCached(pool Queryer, cache *traceCache, repo, file, funcName string) string {
	if cache == nil {
		return findCallerIDByRepoFileFunc(pool, repo, file, funcName, nil)
	}
	key := cacheKey(repo, file, funcName)
	if callerID, ok := cache.callerIDByRepoFileFunc[key]; ok {
		return callerID
	}
	callerID := findCallerIDByRepoFileFuncForSnapshotFilter(pool, repo, file, funcName, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.callerIDByRepoFileFunc[key] = callerID
	return callerID
}

type repoFileFuncKey struct {
	Repo string
	File string
	Name string
}

func (k repoFileFuncKey) cacheKey() string {
	return cacheKey(k.Repo, k.File, k.Name)
}

func findCallerIDsByRepoFileFuncBatchCached(pool Queryer, cache *traceCache, keys []repoFileFuncKey) map[string]string {
	result := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return result
	}

	seen := make(map[string]bool, len(keys))
	var pending []repoFileFuncKey
	for _, key := range keys {
		if key.Repo == "" || key.File == "" || key.Name == "" {
			continue
		}
		cacheKeyValue := key.cacheKey()
		if seen[cacheKeyValue] {
			continue
		}
		seen[cacheKeyValue] = true
		if cache != nil {
			if callerID, ok := cache.callerIDByRepoFileFunc[cacheKeyValue]; ok {
				result[cacheKeyValue] = callerID
				continue
			}
		}
		pending = append(pending, key)
	}

	if len(pending) == 0 {
		return result
	}

	repos := make([]string, 0, len(pending))
	files := make([]string, 0, len(pending))
	names := make([]string, 0, len(pending))
	for _, key := range pending {
		repos = append(repos, key.Repo)
		files = append(files, key.File)
		names = append(names, key.Name)
	}

	query := fmt.Sprintf(`
		WITH input AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[])
				AS t(repo, file, name)
		)
		SELECT t.repo, t.file, t.name, r.name, f.path, fn.name
		FROM input t
		JOIN repositories r ON r.name = t.repo
		JOIN files f ON f.repo_id = r.id AND f.path = t.file
		JOIN functions fn ON fn.file_id = f.id AND fn.name = t.name
		WHERE %s`, traceSnapshotClause("f.snapshot_id", 4, includeLegacySnapshotsFromCache(cache)))
	rows, err := pool.Query(queryContext(pool), query, repos, files, names, snapshotIDsFromCache(cache))
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var repo, file, name string
			var repoName, filePath, fnName string
			if err := rows.Scan(&repo, &file, &name, &repoName, &filePath, &fnName); err != nil {
				continue
			}
			cacheKeyValue := cacheKey(repo, file, name)
			callerID := buildCallerID(repoName, filePath, fnName)
			result[cacheKeyValue] = callerID
			if cache != nil {
				cache.callerIDByRepoFileFunc[cacheKeyValue] = callerID
			}
		}
	}

	if cache != nil {
		for _, key := range pending {
			cacheKeyValue := key.cacheKey()
			if _, ok := result[cacheKeyValue]; !ok {
				cache.callerIDByRepoFileFunc[cacheKeyValue] = ""
			}
		}
	}

	return result
}

func resolveCallerIDsByFunctionIDs(pool Queryer, cache *traceCache, ids []int64) {
	if cache == nil || len(ids) == 0 {
		return
	}
	var missing []int64
	for _, id := range ids {
		if _, ok := cache.callerIDByFunctionID[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return
	}

	query := fmt.Sprintf(`
		SELECT fn.id, r.name, f.path, fn.name
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE fn.id = ANY($1) AND %s`, traceSnapshotClause("f.snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
	rows, err := pool.Query(queryContext(pool), query, missing, snapshotIDsFromCache(cache))
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var repo, file, name string
		if err := rows.Scan(&id, &repo, &file, &name); err != nil {
			continue
		}
		cache.callerIDByFunctionID[id] = buildCallerID(repo, file, name)
	}
}

func findCallerIDByFunctionIDCached(cache *traceCache, id int64) string {
	if cache == nil {
		return ""
	}
	return cache.callerIDByFunctionID[id]
}

// extractClassName extracts the class name from caller_id (repo:file:ClassName.methodName)
func extractClassName(callerID string) string {
	method := extractMethod(callerID)
	if method == "" {
		return ""
	}
	// ClassName.methodName -> ClassName
	parts := strings.Split(method, ".")
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

var genericMethodPrefixes = []string{"get", "set", "is", "has", "add", "remove", "put", "clear"}

// isGenericMethod reports short accessor-shaped names (get/set/is/has/add/put/
// remove/clear, alone or followed by a camelCase, digit, or underscore boundary)
// that are noise when a call stays unresolved. For a bare name the prefix must
// end at a word boundary: issueRefund, hashPassword, and settleInvoice are
// ordinary calls. Qualified names (HashMap.put, svc.getUser) keep the coarse
// whole-name prefix rule, which also hides common collection calls.
func isGenericMethod(name string) bool {
	if len(name) >= 15 {
		return false
	}
	if strings.Contains(name, ".") {
		lower := strings.ToLower(name)
		for _, prefix := range genericMethodPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		return false
	}
	for _, prefix := range genericMethodPrefixes {
		if len(name) < len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) {
			continue
		}
		if len(name) == len(prefix) {
			return true
		}
		next := name[len(prefix)]
		if next == '_' || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9') {
			return true
		}
	}
	return false
}

func nodeDedupeKey(node *TreeNode) string {
	if node == nil {
		return ""
	}
	if node.CallerID != "" {
		return node.CallerID + "|" + node.EdgeType
	}
	key := node.Name
	if node.File != "" {
		key += "|" + node.File
	}
	if node.EdgeType != "" {
		key += "|" + node.EdgeType
	}
	return key
}

func appendUniqueNode(nodes []*TreeNode, node *TreeNode, seen map[string]bool) []*TreeNode {
	if node == nil {
		return nodes
	}
	if seen != nil {
		key := nodeDedupeKey(node)
		if key != "" {
			if seen[key] {
				return nodes
			}
			seen[key] = true
		}
	}
	return append(nodes, node)
}

func printTree(nodes []*TreeNode, prefix string) {
	for i, node := range nodes {
		isLast := i == len(nodes)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		location := ""
		if node.File != "" {
			// Shorten file path
			parts := strings.Split(node.File, "/")
			shortFile := node.File
			if len(parts) > 2 {
				shortFile = parts[0] + "/.../" + parts[len(parts)-1]
			}
			if node.Repo != "" {
				location = fmt.Sprintf(" [%s] (%s", node.Repo, shortFile)
			} else {
				location = fmt.Sprintf(" (%s", shortFile)
			}
			if node.Line > 0 {
				location += fmt.Sprintf(":%d)", node.Line)
			} else {
				location += ")"
			}
		} else if node.Line > 0 {
			location = fmt.Sprintf(" (line %d)", node.Line)
		}

		fmt.Printf("%s%s%s%s\n", prefix, connector, node.Name, location)

		childPrefix := prefix
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
		printTree(node.Children, childPrefix)
	}
}

func normalizeHttpMethod(method string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		return "REQUEST"
	}
	return m
}

func httpMethodMatchesAny(method string) bool {
	m := normalizeHttpMethod(method)
	return m == "REQUEST" || m == "ANY"
}

func normalizeContextPrefixes(prefixes []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, prefix := range prefixes {
		p := strings.TrimSpace(prefix)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if len(p) > 1 && strings.HasSuffix(p, "/") {
			p = strings.TrimSuffix(p, "/")
		}
		p = strings.ToLower(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func getHttpContextPrefixes(cache *traceCache, repo string) []string {
	key := strings.ToLower(repo)
	if cache != nil {
		if prefixes, ok := cache.httpPrefixesByRepo[key]; ok {
			return prefixes
		}
	}
	cfg := config.GetEffectiveTraceConfig()
	httpCfg := cfg.HTTPForRepo(repo)
	prefixes := normalizeContextPrefixes(httpCfg.ContextPathPrefixes)
	if cache != nil {
		cache.httpPrefixesByRepo[key] = prefixes
	}
	return prefixes
}

func normalizeQueuePrefixes(prefixes []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, prefix := range prefixes {
		p := strings.TrimSpace(prefix)
		if p == "" {
			continue
		}
		p = strings.ToLower(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func getSqsQueuePrefixes(cache *traceCache, repo string) []string {
	key := strings.ToLower(repo)
	if cache != nil {
		if prefixes, ok := cache.sqsPrefixesByRepo[key]; ok {
			return prefixes
		}
	}
	cfg := config.GetEffectiveTraceConfig()
	sqsCfg := cfg.SQSForRepo(repo)
	prefixes := normalizeQueuePrefixes(sqsCfg.QueuePrefixes)
	if cache != nil {
		cache.sqsPrefixesByRepo[key] = prefixes
	}
	return prefixes
}

func normalizeQueueName(queueName string) string {
	name := strings.TrimSpace(queueName)
	if name == "" {
		return ""
	}
	name = strings.TrimSpace(strings.Trim(name, "%"))
	if strings.HasPrefix(name, "arn:") {
		if idx := strings.LastIndex(name, ":"); idx != -1 && idx+1 < len(name) {
			name = name[idx+1:]
		}
	}
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		if parsed, err := url.Parse(name); err == nil {
			if parsed.Path != "" {
				parts := strings.Split(parsed.Path, "/")
				name = parts[len(parts)-1]
			}
		}
	}
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		name = parts[len(parts)-1]
	}
	return strings.TrimSpace(name)
}

func queueNameVariants(queueName string, prefixes []string) []string {
	seen := make(map[string]bool)
	add := func(value string) {
		v := strings.TrimSpace(value)
		if v == "" {
			return
		}
		seen[strings.ToLower(v)] = true
	}

	add(queueName)
	base := normalizeQueueName(queueName)
	add(base)
	if looksLikeQueueSettingName(base) {
		add("%" + base + "%")
	}
	baseNoFIFO := base
	if len(base) > len(".fifo") && strings.EqualFold(base[len(base)-len(".fifo"):], ".fifo") {
		baseNoFIFO = base[:len(base)-len(".fifo")]
		add(baseNoFIFO)
	}

	baseLower := strings.ToLower(base)
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(baseLower, prefix) && len(base) > len(prefix) {
			add(base[len(prefix):])
		}
		baseNoFIFOLower := strings.ToLower(baseNoFIFO)
		if strings.HasPrefix(baseNoFIFOLower, prefix) && len(baseNoFIFO) > len(prefix) {
			add(baseNoFIFO[len(prefix):])
		}
	}

	var variants []string
	for v := range seen {
		variants = append(variants, v)
	}
	sort.Strings(variants)
	return variants
}

func looksLikeQueueSettingName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return strings.Contains(name, "_")
}

func findHttpCallsCached(cache *traceCache, pool Queryer, callerID string) []HttpCall {
	if cache != nil {
		if calls, ok := cache.httpCalls[callerID]; ok {
			return calls
		}
	}
	calls := findHttpCalls(pool, callerID, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	if cache != nil {
		cache.httpCalls[callerID] = calls
	}
	return calls
}

func findHttpCallsBatchCached(cache *traceCache, pool Queryer, callerIDs []string) map[string][]HttpCall {
	result := make(map[string][]HttpCall, len(callerIDs))
	if len(callerIDs) == 0 {
		return result
	}
	var missing []string
	if cache != nil {
		for _, callerID := range callerIDs {
			if calls, ok := cache.httpCalls[callerID]; ok {
				result[callerID] = calls
			} else {
				missing = append(missing, callerID)
			}
		}
	} else {
		missing = callerIDs
	}

	if len(missing) == 0 {
		return result
	}

	snapshotIDs := snapshotIDsFromCache(cache)
	query := fmt.Sprintf(`SELECT caller_id, http_method, url_pattern, line_number, client_type
	          FROM http_client_calls
	          WHERE caller_id = ANY($1::text[])
	            AND %s
	          ORDER BY caller_id, line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
	rows, err := pool.Query(queryContext(pool), query, missing, snapshotIDs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var callerID string
			var call HttpCall
			if err := rows.Scan(&callerID, &call.HttpMethod, &call.UrlPattern, &call.LineNumber, &call.ClientType); err != nil {
				continue
			}
			call.HttpMethod = normalizeHttpMethod(call.HttpMethod)
			result[callerID] = append(result[callerID], call)
		}
	}

	for _, callerID := range missing {
		calls, ok := result[callerID]
		if !ok {
			calls = findHttpCalls(pool, callerID, snapshotIDs, includeLegacySnapshotsFromCache(cache))
			result[callerID] = calls
		}
		if cache != nil {
			cache.httpCalls[callerID] = calls
		}
	}
	return result
}

// findHttpCalls finds HTTP client calls made by a function
func findHttpCalls(pool Queryer, callerID string, snapshotIDs []int64, includeLegacy bool) []HttpCall {
	query := fmt.Sprintf(`SELECT http_method, url_pattern, line_number, client_type
	          FROM http_client_calls
	          WHERE caller_id = $1
	            AND %s
	          ORDER BY line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, callerID, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var calls []HttpCall
	for rows.Next() {
		var call HttpCall
		rows.Scan(&call.HttpMethod, &call.UrlPattern, &call.LineNumber, &call.ClientType)
		call.HttpMethod = normalizeHttpMethod(call.HttpMethod)
		calls = append(calls, call)
	}
	return calls
}

// endpointMatchLimit caps how many indexed endpoints one HTTP call may match.
// Pattern queries fetch one more row so callers can tell a capped list (shared
// paths such as /health across many services) from a complete one.
const endpointMatchLimit = 50

// capEndpointMatches trims a match list to endpointMatchLimit and records the
// truncation on the trace, so completeness reporting does not claim the list
// of handlers is exhaustive.
func capEndpointMatches(endpoints []MatchedEndpoint, limiter *nodeLimiter) []MatchedEndpoint {
	if len(endpoints) > endpointMatchLimit {
		limiter.markIncomplete()
		return endpoints[:endpointMatchLimit]
	}
	return endpoints
}

func matchEndpointCached(cache *traceCache, pool Queryer, httpMethod, urlPattern, callerRepo string) []MatchedEndpoint {
	method := normalizeHttpMethod(httpMethod)
	if cache != nil {
		key := cacheKey(method, urlPattern, callerRepo)
		if endpoints, ok := cache.endpointMatches[key]; ok {
			return endpoints
		}
	}
	prefixes := getHttpContextPrefixes(cache, callerRepo)
	endpoints := matchEndpoint(pool, method, urlPattern, prefixes, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	if cache != nil {
		key := cacheKey(method, urlPattern, callerRepo)
		cache.endpointMatches[key] = endpoints
	}
	return endpoints
}

func matchGatewayRoutesCached(cache *traceCache, pool Queryer, httpMethod, urlPattern, callerRepo string) []MatchedGatewayRoute {
	method := normalizeHttpMethod(httpMethod)
	if cache != nil {
		key := cacheKey(method, urlPattern, callerRepo)
		if routes, ok := cache.gatewayMatches[key]; ok {
			return routes
		}
	}
	prefixes := getHttpContextPrefixes(cache, callerRepo)
	routes := matchGatewayRoutes(pool, method, urlPattern, prefixes, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	if cache != nil {
		key := cacheKey(method, urlPattern, callerRepo)
		cache.gatewayMatches[key] = routes
	}
	return routes
}

// MatchEndpoints exposes endpoint matching for other packages.
func MatchEndpoints(pool Queryer, httpMethod, urlPattern, callerRepo string) []MatchedEndpoint {
	prefixes := getHttpContextPrefixes(nil, callerRepo)
	return matchEndpoint(pool, httpMethod, urlPattern, prefixes, nil, false)
}

func MatchEndpointsForSnapshots(pool Queryer, httpMethod, urlPattern, callerRepo string, snapshotIDs []int64) []MatchedEndpoint {
	return MatchEndpointsForSnapshotFilter(pool, httpMethod, urlPattern, callerRepo, snapshotIDs, false)
}

func MatchEndpointsForSnapshotFilter(pool Queryer, httpMethod, urlPattern, callerRepo string, snapshotIDs []int64, includeLegacy bool) []MatchedEndpoint {
	prefixes := getHttpContextPrefixes(nil, callerRepo)
	return matchEndpoint(pool, httpMethod, urlPattern, prefixes, snapshotIDs, includeLegacy)
}

// MatchEndpointsChecked preserves query failures for evidence consumers such as Contracts.
func MatchEndpointsChecked(ctx context.Context, pool Queryer, httpMethod, urlPattern, callerRepo string, snapshotIDs []int64, includeLegacy bool) ([]MatchedEndpoint, error) {
	return matchEndpointChecked(ctx, pool, httpMethod, urlPattern, getHttpContextPrefixes(nil, callerRepo), snapshotIDs, includeLegacy)
}

func attachHTTPResolutionTargets(pool Queryer, cache *traceCache, callerID string, httpNode *TreeNode, depth, maxDepth int, limiter *nodeLimiter) bool {
	endpointSeen := make(map[string]bool)
	endpoints := capEndpointMatches(matchEndpointCached(cache, pool, httpNode.HttpMethod, httpNode.HttpTarget, extractRepo(callerID)), limiter)
	for _, ep := range endpoints {
		if !limiter.allow() {
			return true
		}
		endpointNode := &TreeNode{
			Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
			Repo:       ep.Repo,
			File:       ep.File,
			Line:       ep.LineNumber,
			Depth:      depth + 2,
			EdgeType:   edgeTypeHTTP,
			Confidence: confidenceMedium,
			Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
		}
		handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
		if handlerID != "" {
			endpointNode.CallerID = handlerID
		}
		httpNode.Children = appendUniqueNode(httpNode.Children, endpointNode, endpointSeen)
	}
	if len(httpNode.Children) > 0 {
		return false
	}

	gatewaySeen := make(map[string]bool)
	routes := matchGatewayRoutesCached(cache, pool, httpNode.HttpMethod, httpNode.HttpTarget, extractRepo(callerID))
	for _, route := range routes {
		if !limiter.allow() {
			return true
		}
		gatewayNode := &TreeNode{
			Name:       formatGatewayNodeName(route),
			Repo:       route.Repo,
			File:       route.File,
			Line:       route.LineNumber,
			Depth:      depth + 2,
			EdgeType:   edgeTypeHTTP,
			Confidence: confidenceMedium,
			Evidence:   newEvidence(evidenceSourceGatewayRoute, route.GatewayType),
		}
		if hitLimit := attachGatewayBackendTargets(pool, cache, gatewayNode, route, depth+2, maxDepth, limiter); hitLimit {
			return true
		}
		httpNode.Children = appendUniqueNode(httpNode.Children, gatewayNode, gatewaySeen)
	}
	return false
}

func formatGatewayNodeName(route MatchedGatewayRoute) string {
	name := strings.Trim(strings.TrimSpace(route.GatewayType+" "+route.APIName+"/"+route.OperationName), "/")
	if route.OperationName == "" {
		name = strings.Trim(strings.TrimSpace(route.GatewayType+" "+route.APIName), "/")
	}
	if name == "" {
		name = "gateway"
	}
	return "→ " + name + " [" + route.Repo + "]"
}

func attachGatewayBackendTargets(pool Queryer, cache *traceCache, gatewayNode *TreeNode, route MatchedGatewayRoute, depth, maxDepth int, limiter *nodeLimiter) bool {
	endpointSeen := make(map[string]bool)
	for _, candidate := range gatewayBackendRouteCandidates(route) {
		endpoints := matchEndpoint(pool, firstNonEmpty(route.BackendMethod, route.PublicMethod), candidate, nil, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
		for _, ep := range endpoints {
			if !limiter.allow() {
				return true
			}
			endpointNode := &TreeNode{
				Name:       fmt.Sprintf("→ %s [%s]", ep.Handler, ep.Repo),
				Repo:       ep.Repo,
				File:       ep.File,
				Line:       ep.LineNumber,
				Depth:      depth + 1,
				EdgeType:   edgeTypeHTTP,
				Confidence: confidenceMedium,
				Evidence:   newEvidence(evidenceSourceHttpEndpoint, ""),
			}
			handlerID := findEndpointHandler(pool, cache, ep.Repo, ep.File, ep.Handler)
			if handlerID != "" {
				endpointNode.CallerID = handlerID
			}
			gatewayNode.Children = appendUniqueNode(gatewayNode.Children, endpointNode, endpointSeen)
		}
	}
	if len(gatewayNode.Children) == 0 {
		if target := firstNonEmpty(combineGatewayBackendPath(route), route.BackendURL, route.BackendID); target != "" {
			gatewayNode.Children = append(gatewayNode.Children, &TreeNode{
				Name:       "→ backend " + target,
				Depth:      depth + 1,
				EdgeType:   edgeTypeHTTP,
				Confidence: confidenceLow,
				Evidence:   newEvidence(evidenceSourceGatewayRoute, target),
				HttpTarget: target,
			})
		}
	}
	return false
}

func gatewayBackendRouteCandidates(route MatchedGatewayRoute) []string {
	seen := make(map[string]bool)
	var candidates []string
	add := func(value string) {
		value = normalizeEndpointPattern(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		candidates = append(candidates, value)
	}

	if combined := combineGatewayBackendPath(route); combined != "" {
		add(combined)
	}
	add(route.BackendPath)
	if parsed, err := url.Parse(strings.TrimSpace(route.BackendURL)); err == nil {
		add(parsed.Path)
	}
	return candidates
}

func combineGatewayBackendPath(route MatchedGatewayRoute) string {
	basePath := ""
	if parsed, err := url.Parse(strings.TrimSpace(route.BackendURL)); err == nil {
		basePath = parsed.Path
	}
	rewritePath := strings.TrimSpace(route.BackendPath)
	switch {
	case basePath == "" && rewritePath == "":
		return ""
	case basePath == "":
		return rewritePath
	case rewritePath == "":
		return basePath
	default:
		return normalizeEndpointPattern(strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(rewritePath, "/"))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func matchGatewayRoutes(pool Queryer, httpMethod, urlPattern string, prefixes []string, snapshotIDs []int64, includeLegacy bool) []MatchedGatewayRoute {
	method := normalizeHttpMethod(httpMethod)
	if strings.HasPrefix(strings.TrimSpace(urlPattern), "(") {
		return nil
	}
	normalized := normalizeEndpointPattern(urlPattern)
	if normalized == "" {
		return nil
	}

	routes := matchGatewayRoutesWithPattern(pool, method, normalized, snapshotIDs, includeLegacy)
	if len(routes) > 0 {
		return routes
	}

	if normalized != "/" {
		if strings.HasSuffix(normalized, "/") {
			routes = matchGatewayRoutesWithPattern(pool, method, strings.TrimSuffix(normalized, "/"), snapshotIDs, includeLegacy)
		} else {
			routes = matchGatewayRoutesWithPattern(pool, method, normalized+"/", snapshotIDs, includeLegacy)
		}
		if len(routes) > 0 {
			return routes
		}
	}

	for _, variant := range endpointMatchVariants(normalized, prefixes) {
		routes = matchGatewayRoutesWithPattern(pool, method, variant, snapshotIDs, includeLegacy)
		if len(routes) > 0 {
			return routes
		}
	}

	return nil
}

func matchGatewayRoutesWithPattern(pool Queryer, httpMethod, urlPattern string, snapshotIDs []int64, includeLegacy bool) []MatchedGatewayRoute {
	canonicalPattern := canonicalEndpointIdentityPath(urlPattern)
	if canonicalPattern == "" {
		return nil
	}

	queryMatch := func(method, pattern string, exact bool) []MatchedGatewayRoute {
		canonicalMethod := canonicalEndpointIdentityMethod(method)
		if canonicalMethod == "" {
			return nil
		}
		var query string
		var args []interface{}
		if exact {
			query = fmt.Sprintf(`SELECT r.name, f.path, g.gateway_type, g.api_name, g.operation_name,
			                g.public_method, g.public_path, g.backend_method, g.backend_url,
			                g.backend_path, g.backend_id, g.line_number
			         FROM gateway_routes g
			         JOIN files f ON f.id = g.file_id
			         JOIN repositories r ON r.id = g.repo_id
			         WHERE ($1 = 'ANY' OR UPPER(g.public_method) = $1)
			           AND lower(g.public_path) = $2
			           AND f.path NOT LIKE '.codebase-snapshots/%%'
			           AND %s
			         ORDER BY r.name, f.path, g.api_name, g.operation_name
			         LIMIT %d`, traceSnapshotClause("f.snapshot_id", 3, includeLegacy), traceLookupMatchLimit)
			args = []interface{}{canonicalMethod, pattern, snapshotIDs}
		} else {
			likePattern := buildEndpointLikePattern(pattern)
			query = fmt.Sprintf(`SELECT r.name, f.path, g.gateway_type, g.api_name, g.operation_name,
			                g.public_method, g.public_path, g.backend_method, g.backend_url,
			                g.backend_path, g.backend_id, g.line_number
			         FROM gateway_routes g
			         JOIN files f ON f.id = g.file_id
			         JOIN repositories r ON r.id = g.repo_id
			         WHERE ($1 = 'ANY' OR UPPER(g.public_method) = $1)
			           AND (lower(g.public_path) LIKE $2
			                OR lower(g.public_path) LIKE $4
			                OR $3 LIKE %s || '%%')
			           AND f.path NOT LIKE '.codebase-snapshots/%%'
			           AND %s
			         ORDER BY r.name, f.path, g.api_name, g.operation_name
			         LIMIT %d`, sqlEscapeLikeExpr("lower(g.public_path)"), traceSnapshotClause("f.snapshot_id", 5, includeLegacy), traceLookupMatchLimit)
			args = []interface{}{canonicalMethod, strings.ToLower(likePattern), strings.ToLower(pattern), escapeLikeLiteral(strings.ToLower(pattern)), snapshotIDs}
		}
		rows, err := pool.Query(queryContext(pool), query, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()

		var routes []MatchedGatewayRoute
		for rows.Next() {
			var route MatchedGatewayRoute
			var lineNum *int
			rows.Scan(&route.Repo, &route.File, &route.GatewayType, &route.APIName, &route.OperationName,
				&route.PublicMethod, &route.PublicPath, &route.BackendMethod, &route.BackendURL,
				&route.BackendPath, &route.BackendID, &lineNum)
			if lineNum != nil {
				route.LineNumber = *lineNum
			}
			routes = append(routes, route)
		}
		return dedupeMatchedGatewayRoutes(routes)
	}

	routes := queryMatch(httpMethod, canonicalPattern, true)
	if len(routes) > 0 {
		return routes
	}
	if method := normalizeHttpMethod(httpMethod); !httpMethodMatchesAny(method) {
		routes = queryMatch("REQUEST", canonicalPattern, true)
		if len(routes) > 0 {
			return routes
		}
	}

	routes = queryMatch(httpMethod, canonicalPattern, false)
	if len(routes) > 0 {
		return routes
	}
	if method := normalizeHttpMethod(httpMethod); !httpMethodMatchesAny(method) {
		routes = queryMatch("REQUEST", canonicalPattern, false)
		if len(routes) > 0 {
			return routes
		}
	}
	return nil
}

// commonEndpointSegments are path segments too generic to be useful for fuzzy matching.
// A duplicate lives in internal/api/handlers/function_integrations.go (integrationCommonSegments).
// Keep both in sync when adding entries.
var commonEndpointSegments = map[string]bool{
	"action": true, "admin": true, "api": true, "controller": true,
	"create": true, "data": true, "delete": true, "endpoint": true,
	"external": true, "health": true, "index": true, "internal": true,
	"items": true, "list": true, "private": true, "proxy": true,
	"public": true, "query": true, "report": true, "reports": true,
	"resource": true, "resources": true,
	"service": true, "services": true, "status": true, "swagger": true,
	"update": true, "user": true, "users": true, "value": true, "handler": true,
}

// extractMeaningfulSegments returns normalized static path segments that are
// specific enough to identify an endpoint. Parameters, short segments (<=4 chars),
// and common generic words are excluded.
func extractMeaningfulSegments(pattern string) []string {
	parts := strings.Split(pattern, "/")
	seen := make(map[string]bool)
	var segments []string
	for _, part := range parts {
		if part == "" || part == "*" {
			continue
		}
		// skip parameters (:id, {id})
		if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "{") {
			continue
		}
		// normalize: lowercase, strip hyphens and underscores
		norm := strings.ToLower(part)
		norm = strings.ReplaceAll(norm, "-", "")
		norm = strings.ReplaceAll(norm, "_", "")
		if len(norm) <= 4 {
			continue
		}
		if commonEndpointSegments[norm] {
			continue
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		segments = append(segments, norm)
	}
	return segments
}

// matchEndpointBySegmentSimilarity is a last-resort fallback that matches HTTP
// client calls to endpoints by comparing normalized path segments. This handles
// cases where an API gateway rewrites the URL structure (e.g., APIM).
// It returns a match only when exactly one unambiguous candidate is found.
func matchEndpointBySegmentSimilarity(pool Queryer, httpMethod, urlPattern string, snapshotIDs []int64, includeLegacy bool) []MatchedEndpoint {
	matches, _ := matchEndpointBySegmentSimilarityChecked(queryContext(pool), pool, httpMethod, urlPattern, snapshotIDs, includeLegacy)
	return matches
}

func matchEndpointBySegmentSimilarityChecked(ctx context.Context, pool Queryer, httpMethod, urlPattern string, snapshotIDs []int64, includeLegacy bool) ([]MatchedEndpoint, error) {
	normalized := normalizeEndpointPattern(urlPattern)
	if normalized == "" {
		return nil, nil
	}
	segments := extractMeaningfulSegments(normalized)
	if len(segments) == 0 {
		return nil, nil
	}

	method := canonicalEndpointIdentityMethod(normalizeHttpMethod(httpMethod))
	if method == "" {
		return nil, nil
	}

	// Build WHERE conditions: path_canonical must contain at least one meaningful segment
	var conditions []string
	args := []interface{}{method}
	for i, seg := range segments {
		argIdx := i + 2
		conditions = append(conditions, fmt.Sprintf(
			"replace(replace(lower(COALESCE(NULLIF(e.path_canonical, ''), e.path)), '-', ''), '_', '') LIKE '%%' || $%d || '%%'", argIdx))
		args = append(args, escapeLikeLiteral(seg))
	}

	snapshotArg := len(args) + 1
	args = append(args, snapshotIDs)

	query := fmt.Sprintf(`SELECT r.name as repo, f.path as file,
		COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) as path,
		COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) as method,
		e.line_number,
		COALESCE(func.name, '') as handler
	FROM endpoints e
	JOIN files f ON e.file_id = f.id
	JOIN repositories r ON f.repo_id = r.id
	LEFT JOIN functions func ON e.handler_function_id = func.id
	WHERE COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) IN ($1, 'REQUEST')
	  AND (%s)
	  AND COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) <> '/'
	  AND f.path NOT LIKE '.codebase-snapshots/%%'
	  AND %s
	LIMIT 50`, strings.Join(conditions, " OR "), traceSnapshotClause("f.snapshot_id", snapshotArg, includeLegacy))

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scoredEndpoint struct {
		ep    MatchedEndpoint
		score int
	}
	segmentSet := make(map[string]bool, len(segments))
	for _, s := range segments {
		segmentSet[s] = true
	}

	var candidates []scoredEndpoint
	for rows.Next() {
		var ep MatchedEndpoint
		var lineNum *int
		if err := rows.Scan(&ep.Repo, &ep.File, &ep.Path, &ep.Method, &lineNum, &ep.Handler); err != nil {
			return nil, err
		}
		if lineNum != nil {
			ep.LineNumber = *lineNum
		}
		// Strict score: count endpoint segments that EXACTLY equal (after normalization)
		// one of the client's meaningful segments. Substring matches are rejected
		// to avoid false positives (e.g., "exportauditdata" matching "exportaudit").
		epSegments := extractMeaningfulSegments(ep.Path)
		score := 0
		for _, seg := range epSegments {
			if segmentSet[seg] {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, scoredEndpoint{ep: ep, score: score})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Find max score and collect best matches
	maxScore := 0
	for _, c := range candidates {
		if c.score > maxScore {
			maxScore = c.score
		}
	}
	var best []MatchedEndpoint
	for _, c := range candidates {
		if c.score == maxScore {
			best = append(best, c.ep)
		}
	}

	best = dedupeMatchedEndpointsCanonical(best)

	// Only return if unambiguous (exactly 1 unique endpoint)
	if len(best) == 1 {
		return best, nil
	}
	// Multiple methods for the same path is fine (e.g., GET+POST)
	// Check if all results share the same canonical path
	firstPath := strings.ToLower(best[0].Path)
	allSamePath := true
	for _, ep := range best[1:] {
		if strings.ToLower(ep.Path) != firstPath {
			allSamePath = false
			break
		}
	}
	if allSamePath {
		return best, nil
	}

	return nil, nil
}

// matchEndpoint finds endpoints that match an HTTP call's URL pattern
func matchEndpoint(pool Queryer, httpMethod, urlPattern string, prefixes []string, snapshotIDs []int64, includeLegacy bool) []MatchedEndpoint {
	matches, _ := matchEndpointChecked(queryContext(pool), pool, httpMethod, urlPattern, prefixes, snapshotIDs, includeLegacy)
	return matches
}

func matchEndpointChecked(ctx context.Context, pool Queryer, httpMethod, urlPattern string, prefixes []string, snapshotIDs []int64, includeLegacy bool) ([]MatchedEndpoint, error) {
	method := normalizeHttpMethod(httpMethod)
	if strings.HasPrefix(strings.TrimSpace(urlPattern), "(") {
		return nil, nil
	}
	normalized := normalizeEndpointPattern(urlPattern)
	if normalized == "" {
		return nil, nil
	}

	endpoints, err := matchEndpointWithPatternChecked(ctx, pool, method, normalized, snapshotIDs, includeLegacy)
	if err != nil || len(endpoints) > 0 {
		return endpoints, err
	}

	if normalized != "/" {
		if strings.HasSuffix(normalized, "/") {
			trimmed := strings.TrimSuffix(normalized, "/")
			endpoints, err = matchEndpointWithPatternChecked(ctx, pool, method, trimmed, snapshotIDs, includeLegacy)
		} else {
			endpoints, err = matchEndpointWithPatternChecked(ctx, pool, method, normalized+"/", snapshotIDs, includeLegacy)
		}
		if err != nil || len(endpoints) > 0 {
			return endpoints, err
		}
	}

	for _, variant := range endpointMatchVariants(normalized, prefixes) {
		endpoints, err = matchEndpointWithPatternChecked(ctx, pool, method, variant, snapshotIDs, includeLegacy)
		if err != nil || len(endpoints) > 0 {
			return endpoints, err
		}
	}

	// Last resort: fuzzy segment similarity for gateway-rewritten URLs
	endpoints, err = matchEndpointBySegmentSimilarityChecked(ctx, pool, method, normalized, snapshotIDs, includeLegacy)
	if err != nil || len(endpoints) > 0 {
		return endpoints, err
	}

	return nil, nil
}

func matchEndpointWithPattern(pool Queryer, httpMethod, urlPattern string, snapshotIDs []int64, includeLegacy bool) []MatchedEndpoint {
	matches, _ := matchEndpointWithPatternChecked(queryContext(pool), pool, httpMethod, urlPattern, snapshotIDs, includeLegacy)
	return matches
}

func matchEndpointWithPatternChecked(ctx context.Context, pool Queryer, httpMethod, urlPattern string, snapshotIDs []int64, includeLegacy bool) ([]MatchedEndpoint, error) {
	canonicalPattern := canonicalEndpointIdentityPath(urlPattern)
	if canonicalPattern == "" {
		return nil, nil
	}

	queryMatch := func(method, pattern string, exact bool) ([]MatchedEndpoint, error) {
		canonicalMethod := canonicalEndpointIdentityMethod(method)
		if canonicalMethod == "" {
			return nil, nil
		}
		var query string
		var args []interface{}
		if exact {
			query = fmt.Sprintf(`SELECT r.name as repo, f.path as file,
			                COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) as path,
			                COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) as method,
			                e.line_number,
			                COALESCE(func.name, '') as handler
			         FROM endpoints e
			         JOIN files f ON e.file_id = f.id
			         JOIN repositories r ON f.repo_id = r.id
			         LEFT JOIN functions func ON e.handler_function_id = func.id
			         WHERE ($1 = 'ANY' OR COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) = $1)
			           AND COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) = $2
			           AND (COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) <> '/' OR $2 = '/')
			           AND f.path NOT LIKE '.codebase-snapshots/%%'
			           AND %s
			         ORDER BY r.name, f.path, method, path, COALESCE(e.line_number, 0), handler
			         LIMIT %d`, traceSnapshotClause("f.snapshot_id", 3, includeLegacy), endpointMatchLimit+1)
			args = []interface{}{canonicalMethod, pattern, snapshotIDs}
		} else {
			likePattern := buildEndpointLikePattern(pattern)
			query = fmt.Sprintf(`SELECT r.name as repo, f.path as file,
			                COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) as path,
			                COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) as method,
			                e.line_number,
			                COALESCE(func.name, '') as handler
			         FROM endpoints e
			         JOIN files f ON e.file_id = f.id
			         JOIN repositories r ON f.repo_id = r.id
			         LEFT JOIN functions func ON e.handler_function_id = func.id
			         WHERE ($1 = 'ANY' OR COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) = $1)
			           AND (COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) LIKE $2
			                OR COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) LIKE $4
				                OR $3 LIKE %s || '%%')
			           AND (COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) <> '/' OR $3 = '/')
			           AND f.path NOT LIKE '.codebase-snapshots/%%'
			           AND %s
			         ORDER BY r.name, f.path, method, path, COALESCE(e.line_number, 0), handler
			         LIMIT %d`, sqlEscapeLikeExpr("COALESCE(NULLIF(e.path_canonical, ''), lower(e.path))"), traceSnapshotClause("f.snapshot_id", 5, includeLegacy), endpointMatchLimit+1)
			args = []interface{}{canonicalMethod, strings.ToLower(likePattern), strings.ToLower(pattern), escapeLikeLiteral(strings.ToLower(pattern)), snapshotIDs}
		}
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var endpoints []MatchedEndpoint
		for rows.Next() {
			var ep MatchedEndpoint
			var lineNum *int
			if err := rows.Scan(&ep.Repo, &ep.File, &ep.Path, &ep.Method, &lineNum, &ep.Handler); err != nil {
				return nil, err
			}
			if lineNum != nil {
				ep.LineNumber = *lineNum
			}
			endpoints = append(endpoints, ep)
		}
		return dedupeMatchedEndpointsCanonical(endpoints), rows.Err()
	}

	method := normalizeHttpMethod(httpMethod)
	endpoints, err := queryMatch(method, canonicalPattern, true)
	if err != nil || len(endpoints) > 0 {
		return endpoints, err
	}

	if !httpMethodMatchesAny(method) {
		endpoints, err = queryMatch("REQUEST", canonicalPattern, true)
		if err != nil || len(endpoints) > 0 {
			return endpoints, err
		}
	}

	endpoints, err = queryMatch(method, canonicalPattern, false)
	if err != nil || len(endpoints) > 0 || httpMethodMatchesAny(method) {
		return endpoints, err
	}

	endpoints, err = queryMatch("REQUEST", canonicalPattern, false)
	if err != nil || len(endpoints) > 0 {
		return endpoints, err
	}

	return nil, nil
}

func canonicalEndpointIdentityMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

func canonicalEndpointIdentityPath(path string) string {
	path = normalizeEndpointPattern(path)
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return strings.ToLower(path)
}

func normalizeSnapshotFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	const snapshotPrefix = ".codebase-snapshots/"
	if !strings.HasPrefix(path, snapshotPrefix) {
		return path
	}
	rest := strings.TrimPrefix(path, snapshotPrefix)
	if idx := strings.Index(rest, "/"); idx >= 0 && idx+1 < len(rest) {
		return rest[idx+1:]
	}
	return path
}

func isSnapshotFilePath(path string) bool {
	return strings.HasPrefix(strings.TrimSpace(path), ".codebase-snapshots/")
}

func matchedEndpointCanonicalKey(ep MatchedEndpoint) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(ep.Repo)),
		strings.ToLower(strings.TrimSpace(normalizeSnapshotFilePath(ep.File))),
		strings.ToLower(strings.TrimSpace(ep.Handler)),
		canonicalEndpointIdentityMethod(ep.Method),
		canonicalEndpointIdentityPath(ep.Path),
	}, "|")
}

func choosePreferredMatchedEndpoint(existing, candidate MatchedEndpoint) MatchedEndpoint {
	existingSnapshot := isSnapshotFilePath(existing.File)
	candidateSnapshot := isSnapshotFilePath(candidate.File)
	if existingSnapshot && !candidateSnapshot {
		return candidate
	}
	if !existingSnapshot && candidateSnapshot {
		return existing
	}
	if existing.LineNumber <= 0 && candidate.LineNumber > 0 {
		return candidate
	}
	if candidate.LineNumber > 0 && existing.LineNumber > 0 && candidate.LineNumber < existing.LineNumber {
		return candidate
	}
	if existing.File == "" && candidate.File != "" {
		return candidate
	}
	return existing
}

func dedupeMatchedEndpointsCanonical(endpoints []MatchedEndpoint) []MatchedEndpoint {
	if len(endpoints) <= 1 {
		return endpoints
	}
	indexByKey := make(map[string]int, len(endpoints))
	out := make([]MatchedEndpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		key := matchedEndpointCanonicalKey(ep)
		if idx, ok := indexByKey[key]; ok {
			out[idx] = choosePreferredMatchedEndpoint(out[idx], ep)
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, ep)
	}
	return out
}

func matchedGatewayRouteCanonicalKey(route MatchedGatewayRoute) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(route.Repo)),
		strings.ToLower(strings.TrimSpace(normalizeSnapshotFilePath(route.File))),
		strings.ToLower(strings.TrimSpace(route.GatewayType)),
		strings.ToLower(strings.TrimSpace(route.APIName)),
		strings.ToLower(strings.TrimSpace(route.OperationName)),
		canonicalEndpointIdentityMethod(route.PublicMethod),
		canonicalEndpointIdentityPath(route.PublicPath),
	}, "|")
}

func dedupeMatchedGatewayRoutes(routes []MatchedGatewayRoute) []MatchedGatewayRoute {
	if len(routes) <= 1 {
		return routes
	}
	indexByKey := make(map[string]int, len(routes))
	out := make([]MatchedGatewayRoute, 0, len(routes))
	for _, route := range routes {
		key := matchedGatewayRouteCanonicalKey(route)
		if idx, ok := indexByKey[key]; ok {
			if out[idx].LineNumber <= 0 && route.LineNumber > 0 {
				out[idx] = route
			}
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, route)
	}
	return out
}

// buildEndpointLikePattern turns an indexed route into a LIKE pattern in which
// each ":param" or "{param}" segment becomes "%". Every other character of the
// route is literal, so "_" and "%" in a path never act as wildcards. Callers use
// the default LIKE escape character (backslash).
func buildEndpointLikePattern(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; {
		case c == ':':
			end := i + 1
			for end < len(pattern) && pattern[end] != '/' {
				end++
			}
			b.WriteByte('%')
			i = end
		case c == '{':
			end := strings.IndexByte(pattern[i:], '}')
			if end < 0 {
				b.WriteString(escapeLikeLiteral(pattern[i:]))
				return b.String()
			}
			b.WriteByte('%')
			i += end + 1
		default:
			j := i
			for j < len(pattern) && pattern[j] != ':' && pattern[j] != '{' {
				j++
			}
			b.WriteString(escapeLikeLiteral(pattern[i:j]))
			i = j
		}
	}
	return b.String()
}

// sqlEscapeLikeExpr wraps a SQL text expression so its LIKE metacharacters are
// literal when the value is used as the pattern side of a LIKE.
func sqlEscapeLikeExpr(expr string) string {
	return `replace(replace(replace(` + expr + `, '\', '\\'), '%', '\%'), '_', '\_')`
}

func escapeLikeLiteral(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func normalizeEndpointPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}

	if strings.HasPrefix(pattern, "http://") || strings.HasPrefix(pattern, "https://") {
		if parsed, err := url.Parse(pattern); err == nil {
			if parsed.Path != "" {
				pattern = parsed.Path
			}
		}
	}

	if idx := strings.IndexAny(pattern, "?#"); idx >= 0 {
		pattern = pattern[:idx]
	}
	if pattern == "" {
		return ""
	}
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	for strings.Contains(pattern, "//") {
		pattern = strings.ReplaceAll(pattern, "//", "/")
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	pattern = stripLeadingEndpointBasePrefix(pattern)
	if len(pattern) > 1 && strings.HasSuffix(pattern, "/") {
		pattern = strings.TrimSuffix(pattern, "/")
	}
	return pattern
}

func stripLeadingEndpointBasePrefix(pattern string) string {
	for pattern != "" && pattern != "/" {
		if !strings.HasPrefix(pattern, "/:") && !strings.HasPrefix(pattern, "/{") {
			break
		}
		nextSlash := strings.Index(pattern[1:], "/")
		if nextSlash < 0 {
			break
		}
		pattern = pattern[nextSlash+1:]
		if !strings.HasPrefix(pattern, "/") {
			pattern = "/" + pattern
		}
	}
	if pattern == "" {
		return "/"
	}
	return pattern
}

var endpointBraceParamRe = regexp.MustCompile(`\{([^/{}]+)\}`)
var endpointColonParamRe = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

func endpointPatternToColonParams(pattern string) string {
	if pattern == "" {
		return ""
	}
	return endpointBraceParamRe.ReplaceAllString(pattern, ":$1")
}

func endpointPatternToBraceParams(pattern string) string {
	if pattern == "" {
		return ""
	}
	return endpointColonParamRe.ReplaceAllString(pattern, "{$1}")
}

func endpointMatchVariants(pattern string, prefixes []string) []string {
	if pattern == "" {
		return nil
	}
	lower := strings.ToLower(pattern)
	seen := make(map[string]bool)
	var variants []string

	addRaw := func(value string) {
		if value == "" {
			return
		}
		if !seen[value] {
			seen[value] = true
			variants = append(variants, value)
		}
	}

	add := func(value string) {
		if value == "" {
			return
		}
		addRaw(value)
		colon := endpointPatternToColonParams(value)
		if colon != value {
			addRaw(colon)
		}
		braces := endpointPatternToBraceParams(value)
		if braces != value {
			addRaw(braces)
		}
	}

	add(pattern)

	for _, prefix := range prefixes {
		prefixLower := strings.ToLower(prefix)
		if prefixLower == "" {
			continue
		}
		if strings.HasPrefix(lower, prefixLower+"/") || lower == prefixLower {
			variant := pattern[len(prefix):]
			if variant == "" {
				variant = "/"
			}
			if !strings.HasPrefix(variant, "/") {
				variant = "/" + variant
			}
			add(variant)
		} else {
			if strings.HasSuffix(prefix, "/") {
				add(prefix + strings.TrimPrefix(pattern, "/"))
			} else {
				add(prefix + pattern)
			}
		}
	}

	return variants
}

// findEndpointHandler finds the caller_id for an endpoint handler to continue tracing
func findEndpointHandler(pool Queryer, cache *traceCache, repo, file, handler string) string {
	if handler == "" {
		return ""
	}
	if callerID := findCallerIDByRepoFileFuncCached(pool, cache, repo, file, handler); callerID != "" {
		return callerID
	}
	return findPendingCallerID(pool, cache, repo, file, handler)
}

func findPendingCallerID(pool Queryer, cache *traceCache, repo, file, handler string) string {
	id := buildCallerID(repo, file, handler)
	if id == "" || pool == nil {
		return ""
	}
	query := fmt.Sprintf(`SELECT caller_id FROM pending_calls_edges
		WHERE caller_id = $1 AND %s
		LIMIT 1`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
	var callerID string
	if err := pool.QueryRow(queryContext(pool), query, id, snapshotIDsFromCache(cache)).Scan(&callerID); err != nil {
		return ""
	}
	return callerID
}

func findSqsProducerCallsCached(cache *traceCache, pool Queryer, callerID string) []SqsProducerCall {
	if cache != nil {
		if calls, ok := cache.sqsProducers[callerID]; ok {
			return calls
		}
	}
	calls := findSqsProducerCalls(pool, cache, callerID, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	if cache != nil {
		cache.sqsProducers[callerID] = calls
	}
	return calls
}

func findSqsProducerCallsBatchCached(cache *traceCache, pool Queryer, callerIDs []string) map[string][]SqsProducerCall {
	result := make(map[string][]SqsProducerCall, len(callerIDs))
	if len(callerIDs) == 0 {
		return result
	}
	var missing []string
	if cache != nil {
		for _, callerID := range callerIDs {
			if calls, ok := cache.sqsProducers[callerID]; ok {
				result[callerID] = calls
			} else {
				missing = append(missing, callerID)
			}
		}
	} else {
		missing = callerIDs
	}

	if len(missing) == 0 {
		return result
	}

	snapshotIDs := snapshotIDsFromCache(cache)
	query := fmt.Sprintf(`SELECT caller_id, queue_name, line_number
	          FROM sqs_producers
	          WHERE caller_id = ANY($1)
	            AND %s
	          ORDER BY caller_id, line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
	rows, err := pool.Query(queryContext(pool), query, missing, snapshotIDs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var callerID string
			var call SqsProducerCall
			var lineNum *int
			if err := rows.Scan(&callerID, &call.QueueName, &lineNum); err != nil {
				continue
			}
			if lineNum != nil {
				call.LineNumber = *lineNum
			}
			if normalized := normalizeQueueName(call.QueueName); normalized != "" {
				call.QueueName = normalized
			}
			repo := extractRepo(callerID)
			if prefixes := getSqsQueuePrefixes(cache, repo); len(prefixes) > 0 {
				lower := strings.ToLower(call.QueueName)
				for _, prefix := range prefixes {
					if strings.HasPrefix(lower, prefix) && len(call.QueueName) > len(prefix) {
						call.QueueName = call.QueueName[len(prefix):]
						break
					}
				}
			}
			result[callerID] = append(result[callerID], call)
		}
	}

	for _, callerID := range missing {
		calls := result[callerID]
		if cache != nil {
			cache.sqsProducers[callerID] = calls
		}
		if _, ok := result[callerID]; !ok {
			result[callerID] = nil
		}
	}
	return result
}

// findSqsProducerCalls finds SQS sendMessage calls made by a function
func findSqsProducerCalls(pool Queryer, cache *traceCache, callerID string, snapshotIDs []int64, includeLegacy bool) []SqsProducerCall {
	query := fmt.Sprintf(`SELECT queue_name, line_number
	          FROM sqs_producers
	          WHERE caller_id = $1
	            AND %s
	          ORDER BY line_number`, traceSnapshotClause("snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, callerID, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var calls []SqsProducerCall
	repo := extractRepo(callerID)
	for rows.Next() {
		var call SqsProducerCall
		var lineNum *int
		rows.Scan(&call.QueueName, &lineNum)
		if lineNum != nil {
			call.LineNumber = *lineNum
		}
		if normalized := normalizeQueueName(call.QueueName); normalized != "" {
			call.QueueName = normalized
		}
		if prefixes := getSqsQueuePrefixes(cache, repo); len(prefixes) > 0 {
			lower := strings.ToLower(call.QueueName)
			for _, prefix := range prefixes {
				if strings.HasPrefix(lower, prefix) && len(call.QueueName) > len(prefix) {
					call.QueueName = call.QueueName[len(prefix):]
					break
				}
			}
		}
		calls = append(calls, call)
	}
	return calls
}

func findSqsConsumersCached(cache *traceCache, pool Queryer, queueName, producerRepo string) []SqsConsumer {
	if cache != nil {
		key := cacheKey(queueName, producerRepo)
		if consumers, ok := cache.sqsConsumers[key]; ok {
			return consumers
		}
	}
	prefixes := getSqsQueuePrefixes(cache, producerRepo)
	consumers := findSqsConsumers(pool, queueName, prefixes, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	if cache != nil {
		key := cacheKey(queueName, producerRepo)
		cache.sqsConsumers[key] = consumers
	}
	return consumers
}

// findSqsConsumers finds classes that consume messages from a queue
func findSqsConsumers(pool Queryer, queueName string, prefixes []string, snapshotIDs []int64, includeLegacy bool) []SqsConsumer {
	variants := queueNameVariants(queueName, prefixes)
	if len(variants) == 0 {
		return nil
	}
	variants = expandQueueVariantsWithResourceAliases(pool, variants, prefixes, snapshotIDs, includeLegacy)
	query := fmt.Sprintf(`
		SELECT consumer_id, queue_name, handler_method, trigger_type FROM (
			SELECT consumer_id, queue_name, handler_method, '' AS trigger_type
			FROM sqs_consumers
			WHERE LOWER(queue_name) = ANY($1)
			  AND %s

			UNION ALL

			SELECT
				r.name || ':' || f.path || ':' || t.function_name AS consumer_id,
				COALESCE(t.resource_name, '') AS queue_name,
				'' AS handler_method,
				t.trigger_type
			FROM azure_function_triggers t
			JOIN files f ON f.id = t.file_id
			JOIN repositories r ON r.id = t.repo_id
			WHERE t.trigger_type IN ('queueTrigger', 'serviceBusTrigger')
			  AND (
				LOWER(COALESCE(t.resource_name, '')) = ANY($1)
				OR LOWER(TRIM(BOTH '%%' FROM COALESCE(t.resource_name, ''))) = ANY($1)
			  )
			  AND %s

			UNION ALL

			SELECT
				da.caller_id AS consumer_id,
				REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i') AS queue_name,
				split_part(da.caller_id, ':', 3) AS handler_method,
				'' AS trigger_type
			FROM data_accesses da
			WHERE LOWER(da.access) = 'read'
			  AND (
				LOWER(TRIM(BOTH '%%' FROM COALESCE(da.entity_name, ''))) = ANY($1)
				OR LOWER(TRIM(BOTH '%%' FROM REGEXP_REPLACE(COALESCE(da.entity_name, ''), '^queue:', '', 'i'))) = ANY($1)
			  )
			  AND %s
		) consumers`,
		traceSnapshotClause("snapshot_id", 2, includeLegacy),
		traceSnapshotClause("f.snapshot_id", 2, includeLegacy),
		traceSnapshotClause("da.snapshot_id", 2, includeLegacy),
	)
	rows, err := pool.Query(queryContext(pool), query, variants, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var consumers []SqsConsumer
	for rows.Next() {
		var c SqsConsumer
		rows.Scan(&c.ConsumerID, &c.QueueName, &c.HandlerMethod, &c.TriggerType)
		// Parse consumer_id: repo:file:class
		parts := strings.Split(c.ConsumerID, ":")
		if len(parts) >= 3 {
			c.Repo = parts[0]
			c.File = parts[1]
			c.Class = parts[2]
		}
		consumers = append(consumers, c)
	}
	return consumers
}

func expandQueueVariantsWithResourceAliases(pool Queryer, variants []string, prefixes []string, snapshotIDs []int64, includeLegacy bool) []string {
	if pool == nil || len(variants) == 0 {
		return variants
	}
	query := fmt.Sprintf(`
		WITH input_variants AS (
			SELECT unnest($1::text[]) AS queue_name
		),
		direct_aliases AS (
			SELECT alias_key, alias_value
			FROM resource_aliases
			WHERE (
				LOWER(alias_key) IN (SELECT queue_name FROM input_variants)
				OR LOWER(alias_value) IN (SELECT queue_name FROM input_variants)
				OR LOWER(TRIM(BOTH '%%' FROM alias_key)) IN (SELECT queue_name FROM input_variants)
				OR LOWER(TRIM(BOTH '%%' FROM alias_value)) IN (SELECT queue_name FROM input_variants)
			)
			  AND %s
		)
		SELECT alias_key, alias_value
		FROM resource_aliases
		WHERE (
			LOWER(alias_key) IN (SELECT queue_name FROM input_variants)
			OR LOWER(alias_value) IN (SELECT queue_name FROM input_variants)
			OR LOWER(TRIM(BOTH '%%' FROM alias_key)) IN (SELECT queue_name FROM input_variants)
			OR LOWER(TRIM(BOTH '%%' FROM alias_value)) IN (SELECT queue_name FROM input_variants)
			OR LOWER(TRIM(BOTH '%%' FROM alias_value)) IN (
				SELECT LOWER(TRIM(BOTH '%%' FROM alias_value)) FROM direct_aliases
			)
		)
		  AND %s
	`, traceSnapshotClause("snapshot_id", 2, includeLegacy), traceSnapshotClause("snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, variants, snapshotIDs)
	if err != nil {
		return variants
	}
	defer rows.Close()

	seen := make(map[string]bool, len(variants))
	for _, variant := range variants {
		if trimmed := strings.TrimSpace(variant); trimmed != "" {
			seen[strings.ToLower(trimmed)] = true
		}
	}
	addVariants := func(value string) {
		for _, variant := range queueNameVariants(value, prefixes) {
			if strings.TrimSpace(variant) != "" {
				seen[strings.ToLower(variant)] = true
			}
		}
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		addVariants(key)
		addVariants(value)
	}

	out := make([]string, 0, len(seen))
	for variant := range seen {
		out = append(out, variant)
	}
	sort.Strings(out)
	return out
}

// findConsumerHandler finds the caller_id for a consumer's handler method
func findConsumerHandler(pool Queryer, cache *traceCache, consumer SqsConsumer) string {
	if consumer.ConsumerID != "" && consumer.TriggerType != "" {
		if pool == nil {
			return ""
		}
		if callerID := resolveAzureTriggerConsumerHandler(pool, cache, consumer); callerID != "" {
			return callerID
		}
		return ""
	}

	var candidates []string
	if consumer.HandlerMethod != "" {
		if consumer.Class == consumer.HandlerMethod || strings.HasSuffix(consumer.Class, "."+consumer.HandlerMethod) {
			candidates = append(candidates, consumer.Class)
		} else if consumer.Class != "" && !strings.HasPrefix(consumer.HandlerMethod, consumer.Class+".") {
			candidates = append(candidates, consumer.Class+"."+consumer.HandlerMethod)
		}
		candidates = append(candidates, consumer.HandlerMethod)
	} else if consumer.Class != "" {
		candidates = append(candidates, consumer.Class)
	}

	for _, candidate := range candidates {
		if callerID := findCallerIDByRepoFileFuncCached(pool, cache, consumer.Repo, consumer.File, candidate); callerID != "" {
			return callerID
		}
		if callerID := findPendingCallerID(pool, cache, consumer.Repo, consumer.File, candidate); callerID != "" {
			return callerID
		}
	}
	return ""
}

func formatQueueConsumerNodeName(consumer SqsConsumer) string {
	if consumer.TriggerType != "" {
		if consumer.Class != "" {
			return fmt.Sprintf("→ %s [%s]", consumer.Class, consumer.Repo)
		}
		return fmt.Sprintf("→ queue consumer [%s]", consumer.Repo)
	}
	return fmt.Sprintf("→ %s.%s [%s]", consumer.Class, consumer.HandlerMethod, consumer.Repo)
}

// EventBridgeSchedule represents a parsed EventBridge schedule rule.
type EventBridgeSchedule struct {
	RuleName           string
	ScheduleExpression string
	TargetType         string
	TargetName         string
	State              string
	Source             string
}

type AzureTimerTrigger struct {
	FunctionName       string
	ScheduleExpression string
	Source             string
	ScriptFile         string
	LineNumber         int
}

func findScheduleTriggersForCaller(pool Queryer, callerID string, depth int, snapshotIDs []int64, includeLegacy bool) []*TreeNode {
	nodes := findEventBridgeTriggersForConsumer(pool, callerID, depth, snapshotIDs, includeLegacy)
	nodes = append(nodes, findAzureTimerTriggersForHandler(pool, callerID, depth, snapshotIDs, includeLegacy)...)
	return nodes
}

func findAzureTimerTriggersForHandler(pool Queryer, callerID string, depth int, snapshotIDs []int64, includeLegacy bool) []*TreeNode {
	repo := extractRepo(callerID)
	if repo == "" || extractFile(callerID) == "" || extractMethod(callerID) == "" {
		return nil
	}

	query := fmt.Sprintf(`
		SELECT t.function_name,
		       COALESCE(t.schedule_expression, ''),
		       f.path,
		       COALESCE(t.script_file, ''),
		       COALESCE(t.line_number, 0)
		FROM azure_function_triggers t
		JOIN files f ON f.id = t.file_id
		JOIN repositories r ON r.id = t.repo_id
		WHERE r.name = $1
		  AND LOWER(t.trigger_type) = 'timertrigger'
		  AND f.path NOT LIKE '.codebase-snapshots/%%'
		  AND %s
		ORDER BY f.path, t.function_name, COALESCE(t.line_number, 0)
	`, traceSnapshotClause("f.snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, repo, snapshotIDs)
	if err != nil {
		if isMissingRelation(err, "azure_function_triggers") {
			return nil
		}
		return nil
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var nodes []*TreeNode
	for rows.Next() {
		var trigger AzureTimerTrigger
		if err := rows.Scan(&trigger.FunctionName, &trigger.ScheduleExpression, &trigger.Source, &trigger.ScriptFile, &trigger.LineNumber); err != nil {
			continue
		}
		matchesTarget := false
		for _, sourceFile := range azureTriggerSourcePathCandidates(trigger.Source, trigger.ScriptFile) {
			for _, handler := range azureTriggerHandlerNameCandidates("timerTrigger", trigger.FunctionName) {
				resolved := findCallerIDByRepoFileFuncForSnapshotFilter(pool, repo, sourceFile, handler, snapshotIDs, includeLegacy)
				if resolved == callerID {
					matchesTarget = true
					break
				}
			}
			if matchesTarget {
				break
			}
		}
		if !matchesTarget {
			continue
		}
		key := trigger.Source + ":" + trigger.FunctionName + ":" + trigger.ScheduleExpression
		if seen[key] {
			continue
		}
		seen[key] = true

		name := fmt.Sprintf("[Azure Timer → %s]", trigger.FunctionName)
		if strings.TrimSpace(trigger.ScheduleExpression) != "" {
			name = fmt.Sprintf("%s %s", name, trigger.ScheduleExpression)
		}
		nodes = append(nodes, &TreeNode{
			Name:           name,
			File:           trigger.Source,
			Repo:           repo,
			Line:           trigger.LineNumber,
			Depth:          depth + 1,
			EdgeType:       edgeTypeAzureTimer,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			Evidence:       newEvidence(evidenceSourceAzureTimer, trigger.Source),
			Source:         "azure_function_triggers",
		})
	}
	return nodes
}

// findEventBridgeTriggersForConsumer checks if a callerID is an SQS consumer
// and returns EventBridge schedule nodes that target its queue.
func findEventBridgeTriggersForConsumer(pool Queryer, callerID string, depth int, snapshotIDs []int64, includeLegacy bool) []*TreeNode {
	// consumer_id in sqs_consumers is repo:file:ClassName (no method),
	// but callerID from trace is repo:file:ClassName.method — try both.
	classID := callerID
	if idx := strings.LastIndex(extractMethod(callerID), "."); idx >= 0 {
		// Strip method from the function part: "Class.method" -> "Class"
		parts := strings.SplitN(callerID, ":", 3)
		if len(parts) == 3 {
			funcPart := parts[2]
			if dotIdx := strings.LastIndex(funcPart, "."); dotIdx >= 0 {
				classID = parts[0] + ":" + parts[1] + ":" + funcPart[:dotIdx]
			}
		}
	}
	query := fmt.Sprintf(`SELECT queue_name FROM sqs_consumers
	          WHERE (consumer_id = $1 OR consumer_id = $2)
	            AND %s`, traceSnapshotClause("snapshot_id", 3, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, callerID, classID, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var queueNames []string
	for rows.Next() {
		var q string
		rows.Scan(&q)
		queueNames = append(queueNames, q)
	}
	if len(queueNames) == 0 {
		return nil
	}

	// Query EventBridge schedules targeting these queues
	ebQuery := `SELECT rule_name, schedule_expression, target_type, target_name, state, source
	            FROM eventbridge_schedules
	            WHERE target_type = 'sqs' AND LOWER(target_name) = ANY($1)`
	variants := make([]string, len(queueNames))
	for i, q := range queueNames {
		variants[i] = strings.ToLower(q)
	}
	ebRows, err := pool.Query(queryContext(pool), ebQuery, variants)
	if err != nil {
		return nil
	}
	defer ebRows.Close()

	var nodes []*TreeNode
	for ebRows.Next() {
		var eb EventBridgeSchedule
		ebRows.Scan(&eb.RuleName, &eb.ScheduleExpression, &eb.TargetType, &eb.TargetName, &eb.State, &eb.Source)
		node := &TreeNode{
			Name:           fmt.Sprintf("[EventBridge → %s] %s %s", eb.RuleName, eb.ScheduleExpression, eb.State),
			Depth:          depth + 1,
			EdgeType:       edgeTypeEventBridge,
			Confidence:     confidenceHigh,
			IsCrossService: true,
			Evidence:       newEvidence(evidenceSourceEventBridge, eb.Source),
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// FindEventBridgeSchedulesForQueue returns EventBridge schedules targeting a queue.
func FindEventBridgeSchedulesForQueue(pool Queryer, queueName string) []EventBridgeSchedule {
	query := `SELECT rule_name, schedule_expression, target_type, target_name, state, source
	          FROM eventbridge_schedules
	          WHERE target_type = 'sqs' AND LOWER(target_name) = LOWER($1)`
	rows, err := pool.Query(queryContext(pool), query, queueName)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var schedules []EventBridgeSchedule
	for rows.Next() {
		var s EventBridgeSchedule
		rows.Scan(&s.RuleName, &s.ScheduleExpression, &s.TargetType, &s.TargetName, &s.State, &s.Source)
		schedules = append(schedules, s)
	}
	return schedules
}

// findInterfaceImplementations finds all classes that implement a given interface
func findInterfaceImplementations(pool Queryer, interfaceName string) []InterfaceImpl {
	return findInterfaceImplementationsForSnapshots(pool, interfaceName, nil)
}

func findInterfaceImplementationsForSnapshots(pool Queryer, interfaceName string, snapshotIDs []int64) []InterfaceImpl {
	return findInterfaceImplementationsForSnapshotFilter(pool, interfaceName, snapshotIDs, false)
}

func findInterfaceImplementationsForSnapshotFilter(pool Queryer, interfaceName string, snapshotIDs []int64, includeLegacy bool) []InterfaceImpl {
	interfaceName = normalizeTypeName(interfaceName)
	if interfaceName == "" {
		return nil
	}
	if traceInterfaceImplsEnabled() {
		if impls, err := findInterfaceImplementationsTraceForSnapshotFilter(pool, interfaceName, snapshotIDs, includeLegacy); err == nil && len(impls) > 0 {
			return normalizeInterfaceImpls(impls)
		} else if err != nil && isMissingRelation(err, "trace_interface_impls") {
			disableTraceInterfaceImpls()
		}
	}

	// Use recursive CTEs to find all transitive implementations
	// This handles:
	// 1. Interface inheritance: interface A extends B, class C implements A -> find C for B
	// 2. Class inheritance: class A implements I, class B extends A -> find B for I
	query := fmt.Sprintf(`
		WITH RECURSIVE
		scoped_files AS (
			SELECT id FROM files WHERE %s
		),
		scoped_interfaces AS (
			SELECT i.* FROM interfaces i JOIN scoped_files f ON f.id = i.file_id
		),
		scoped_classes AS (
			SELECT c.* FROM classes c JOIN scoped_files f ON f.id = c.file_id
		),
		-- Find all interfaces in the hierarchy (interfaces extending our target)
		interface_hierarchy AS (
			SELECT id, name FROM scoped_interfaces WHERE name = $1
			UNION
			SELECT i.id, i.name
			FROM scoped_interfaces i
			JOIN interface_hierarchy ih ON i.extends_interfaces ? ih.name
		),
		-- Find all direct implementors of any interface in the hierarchy
		direct_impls AS (
			SELECT DISTINCT c.id, c.name
			FROM implementations impl
			JOIN scoped_classes c ON impl.class_id = c.id
			WHERE impl.interface_id IN (SELECT id FROM interface_hierarchy)
			   OR impl.interface_name IN (SELECT name FROM interface_hierarchy)
		),
		-- Find all subclasses of the direct implementors (class inheritance)
		class_hierarchy AS (
			SELECT id, name FROM direct_impls
			UNION
			SELECT c.id, c.name
			FROM scoped_classes c
			JOIN class_hierarchy ch ON c.extends_class = ch.name
		)
			SELECT DISTINCT c.name, c.id, r.name as repo, f.path as file
			FROM class_hierarchy ch
			JOIN classes c ON ch.id = c.id
			JOIN files f ON c.file_id = f.id
			JOIN repositories r ON f.repo_id = r.id
			ORDER BY r.name, f.path, c.name, c.id
			LIMIT %d`, traceSnapshotClause("snapshot_id", 2, includeLegacy), traceLookupMatchLimit)

	rows, err := pool.Query(queryContext(pool), query, interfaceName, snapshotIDs)
	if err != nil {
		// Fallback to simple query if recursive CTE fails
		return findInterfaceImplementationsSimpleForSnapshotFilter(pool, interfaceName, snapshotIDs, includeLegacy)
	}
	defer rows.Close()

	var impls []InterfaceImpl
	for rows.Next() {
		var impl InterfaceImpl
		rows.Scan(&impl.ClassName, &impl.ClassID, &impl.Repo, &impl.File)
		impls = append(impls, impl)
	}
	return normalizeInterfaceImpls(impls)
}

func findInterfaceImplementationsTrace(pool Queryer, interfaceName string) ([]InterfaceImpl, error) {
	return findInterfaceImplementationsTraceForSnapshots(pool, interfaceName, nil)
}

func findInterfaceImplementationsTraceForSnapshots(pool Queryer, interfaceName string, snapshotIDs []int64) ([]InterfaceImpl, error) {
	return findInterfaceImplementationsTraceForSnapshotFilter(pool, interfaceName, snapshotIDs, false)
}

func findInterfaceImplementationsTraceForSnapshotFilter(pool Queryer, interfaceName string, snapshotIDs []int64, includeLegacy bool) ([]InterfaceImpl, error) {
	query := fmt.Sprintf(`
		SELECT class_name, class_id, class_repo, class_file
		FROM trace_interface_impls
		WHERE interface_name = $1
		  AND %s
		ORDER BY class_repo, class_file, class_name, class_id
		LIMIT %d`, traceSnapshotClause("snapshot_id", 2, includeLegacy), traceLookupMatchLimit)
	rows, err := pool.Query(queryContext(pool), query, interfaceName, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var impls []InterfaceImpl
	for rows.Next() {
		var impl InterfaceImpl
		var classID sql.NullInt64
		if err := rows.Scan(&impl.ClassName, &classID, &impl.Repo, &impl.File); err != nil {
			continue
		}
		if classID.Valid {
			impl.ClassID = classID.Int64
		}
		impls = append(impls, impl)
	}
	return normalizeInterfaceImpls(impls), nil
}

func findInterfaceImplementationsCached(pool Queryer, cache *traceCache, interfaceName string) []InterfaceImpl {
	if cache == nil {
		return findInterfaceImplementations(pool, interfaceName)
	}
	key := strings.ToLower(interfaceName)
	if impls, ok := cache.interfaceImpls[key]; ok {
		return impls
	}
	scoped := hasSnapshotScope(cache)
	if !scoped {
		if cached, ok := globalInterfaceImplsLRU.get(key); ok {
			if impls, ok := cached.([]InterfaceImpl); ok {
				cache.interfaceImpls[key] = impls
				return impls
			}
		}
	}
	impls := findInterfaceImplementationsForSnapshotFilter(pool, interfaceName, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.interfaceImpls[key] = impls
	if !scoped {
		globalInterfaceImplsLRU.set(key, impls)
	}
	return impls
}

func findInterfaceImplementationsBatch(pool Queryer, cache *traceCache, interfaceNames []string) map[string][]InterfaceImpl {
	result := make(map[string][]InterfaceImpl, len(interfaceNames))
	if len(interfaceNames) == 0 {
		return result
	}

	seen := make(map[string]bool, len(interfaceNames))
	var pending []string
	for _, name := range interfaceNames {
		normalized := normalizeTypeName(name)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if seen[key] {
			continue
		}
		seen[key] = true
		if cache != nil {
			if impls, ok := cache.interfaceImpls[key]; ok {
				result[key] = impls
				continue
			}
		}
		if !hasSnapshotScope(cache) {
			if cached, ok := globalInterfaceImplsLRU.get(key); ok {
				if impls, ok := cached.([]InterfaceImpl); ok {
					result[key] = impls
					if cache != nil {
						cache.interfaceImpls[key] = impls
					}
					continue
				}
			}
		}
		pending = append(pending, normalized)
	}

	if len(pending) == 0 {
		return result
	}

	if traceInterfaceImplsEnabled() {
		query := fmt.Sprintf(`
			SELECT interface_name, class_name, class_id, class_repo, class_file
			FROM trace_interface_impls
			WHERE interface_name = ANY($1)
			  AND %s`, traceSnapshotClause("snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, pending, snapshotIDsFromCache(cache))
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var iface, className, classRepo, classFile string
				var classID sql.NullInt64
				if err := rows.Scan(&iface, &className, &classID, &classRepo, &classFile); err != nil {
					continue
				}
				impl := InterfaceImpl{
					ClassName: className,
					Repo:      classRepo,
					File:      classFile,
				}
				if classID.Valid {
					impl.ClassID = classID.Int64
				}
				key := strings.ToLower(iface)
				result[key] = append(result[key], impl)
			}
		} else if isMissingRelation(err, "trace_interface_impls") {
			disableTraceInterfaceImpls()
		}
	}

	var missing []string
	for _, name := range pending {
		key := strings.ToLower(name)
		if _, ok := result[key]; !ok {
			missing = append(missing, name)
		}
	}
	for _, name := range missing {
		key := strings.ToLower(name)
		impls := findInterfaceImplementationsCached(pool, cache, name)
		result[key] = impls
	}

	if cache != nil {
		for key, impls := range result {
			normalized := normalizeInterfaceImpls(impls)
			result[key] = normalized
			cache.interfaceImpls[key] = normalized
		}
	} else {
		for key, impls := range result {
			result[key] = normalizeInterfaceImpls(impls)
		}
	}
	return result
}

// findInterfaceImplementationsSimple is a fallback without recursive CTE
func findInterfaceImplementationsSimple(pool Queryer, interfaceName string, snapshotIDs []int64) []InterfaceImpl {
	return findInterfaceImplementationsSimpleForSnapshotFilter(pool, interfaceName, snapshotIDs, false)
}

func findInterfaceImplementationsSimpleForSnapshotFilter(pool Queryer, interfaceName string, snapshotIDs []int64, includeLegacy bool) []InterfaceImpl {
	query := fmt.Sprintf(`
		SELECT c.name, c.id, r.name as repo, f.path as file
		FROM implementations i
		JOIN classes c ON i.class_id = c.id
		JOIN files f ON c.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
			WHERE (i.interface_name = $1
			   OR i.interface_id IN (
			       SELECT id FROM interfaces WHERE name = $1
			   ))
			  AND %s
			ORDER BY r.name, f.path, c.name, c.id
			LIMIT %d`, traceSnapshotClause("f.snapshot_id", 2, includeLegacy), traceLookupMatchLimit)

	rows, err := pool.Query(queryContext(pool), query, interfaceName, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var impls []InterfaceImpl
	for rows.Next() {
		var impl InterfaceImpl
		rows.Scan(&impl.ClassName, &impl.ClassID, &impl.Repo, &impl.File)
		impls = append(impls, impl)
	}
	return normalizeInterfaceImpls(impls)
}

// findHttpInterfaceMethod finds HTTP method details for a Retrofit/Feign interface method
func findHttpInterfaceMethod(pool Queryer, interfaceName, methodName string) *HttpInterfaceMethod {
	query := `
		SELECT r.name, f.path, i.name, h.method_name, h.http_method, h.url_pattern, COALESCE(h.line_number, 0)
		FROM http_interface_methods h
		JOIN interfaces i ON h.interface_id = i.id
		JOIN files f ON i.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE i.name = $1 AND h.method_name = $2
		ORDER BY r.name, f.path, h.line_number, h.id
		LIMIT 1`

	var method HttpInterfaceMethod
	err := pool.QueryRow(queryContext(pool), query, interfaceName, methodName).Scan(
		&method.Repo, &method.File, &method.InterfaceName, &method.MethodName, &method.HttpMethod, &method.UrlPattern, &method.LineNumber)
	if err != nil {
		return nil
	}
	return &method
}

// resolveInterfaceMethodCall resolves a method call that might be on an interface type
// Returns the implementing classes' caller_ids for tracing
func resolveInterfaceMethodCall(pool Queryer, calleeName string) []string {
	// Parse callee name: might be "InterfaceName.methodName" or just "methodName"
	parts := strings.Split(calleeName, ".")
	var typeName, methodName string

	if len(parts) >= 2 {
		typeName = normalizeTypeName(parts[len(parts)-2])
		methodName = parts[len(parts)-1]
	} else {
		methodName = calleeName
	}

	// Check if this is an HTTP interface method first
	if typeName != "" {
		httpMethod := findHttpInterfaceMethod(pool, typeName, methodName)
		if httpMethod != nil {
			// This is an HTTP interface method - return empty so we can handle it specially
			return nil
		}
	}

	// Check if typeName is an interface
	if typeName != "" {
		impls := findInterfaceImplementations(pool, typeName)
		if len(impls) > 0 {
			var callerIDs []string
			for _, impl := range impls {
				// Find the caller_id for this implementation's method
				implMethodName := impl.ClassName + "." + methodName
				callerID := findCallerIDByRepoFileFunc(pool, impl.Repo, impl.File, implMethodName, nil)
				if callerID == "" {
					callerID = findUniqueCallerIDByFunctionName(pool, implMethodName)
				}
				if callerID != "" {
					callerIDs = append(callerIDs, callerID)
				}
			}
			return normalizeCallerIDs(callerIDs)
		}
	}

	return nil
}

func resolveInterfaceMethodCallCached(pool Queryer, cache *traceCache, calleeName string) []string {
	if cache == nil {
		return resolveInterfaceMethodCall(pool, calleeName)
	}
	stats := cache.resolveStats
	key := strings.ToLower(calleeName)
	if ids, ok := cache.interfaceMethodCalls[key]; ok {
		if stats != nil {
			stats.interfaceCacheHits++
			stats.interfaceResolvedEdges += len(ids)
		}
		return ids
	}
	var start time.Time
	if stats != nil {
		stats.interfaceRequests++
		start = time.Now()
	}

	parts := strings.Split(calleeName, ".")
	var typeName, methodName string
	if len(parts) >= 2 {
		typeName = normalizeTypeName(parts[len(parts)-2])
		methodName = parts[len(parts)-1]
	} else {
		methodName = calleeName
	}

	// If this is an HTTP interface method, let the HTTP path handle it.
	if typeName != "" {
		if len(cache.httpInterfaceByFull[strings.ToLower(typeName+"."+methodName)]) > 0 {
			cache.interfaceMethodCalls[key] = nil
			if stats != nil {
				stats.interfaceDuration += time.Since(start)
			}
			return nil
		}
	}

	if typeName != "" {
		impls := findInterfaceImplementationsCached(pool, cache, typeName)
		if len(impls) > 0 {
			var callerIDs []string
			for _, impl := range impls {
				implMethodName := impl.ClassName + "." + methodName
				callerID := findCallerIDByRepoFileFuncCached(pool, cache, impl.Repo, impl.File, implMethodName)
				if callerID == "" {
					callerID = findUniqueCallerIDByFunctionNameCached(pool, cache, implMethodName)
				}
				if callerID != "" {
					callerIDs = append(callerIDs, callerID)
				}
			}
			callerIDs = normalizeCallerIDs(callerIDs)
			cache.interfaceMethodCalls[key] = callerIDs
			if stats != nil {
				stats.interfaceResolvedEdges += len(callerIDs)
				stats.interfaceDuration += time.Since(start)
			}
			return callerIDs
		}
	}

	cache.interfaceMethodCalls[key] = nil
	if stats != nil {
		stats.interfaceDuration += time.Since(start)
	}
	return nil
}

func resolveInterfaceMethodCallBatchCached(pool Queryer, cache *traceCache, calleeNames []string) map[string][]string {
	result := make(map[string][]string, len(calleeNames))
	if len(calleeNames) == 0 {
		return result
	}

	type calleeInfo struct {
		typeKey    string
		typeName   string
		methodName string
	}

	var pending []string
	infoByCallee := make(map[string]calleeInfo)
	typeNamesSet := make(map[string]string)
	var stats *resolveStats
	if cache != nil {
		stats = cache.resolveStats
	}

	for _, calleeName := range calleeNames {
		if calleeName == "" {
			continue
		}
		key := strings.ToLower(calleeName)
		if cache != nil {
			if ids, ok := cache.interfaceMethodCalls[key]; ok {
				result[calleeName] = ids
				if stats != nil {
					stats.interfaceCacheHits++
					stats.interfaceResolvedEdges += len(ids)
				}
				continue
			}
		}
		parts := strings.Split(calleeName, ".")
		if len(parts) < 2 {
			if cache != nil {
				cache.interfaceMethodCalls[key] = nil
			}
			result[calleeName] = nil
			continue
		}
		typeName := normalizeTypeName(parts[len(parts)-2])
		methodName := parts[len(parts)-1]
		if typeName == "" || methodName == "" {
			if cache != nil {
				cache.interfaceMethodCalls[key] = nil
			}
			result[calleeName] = nil
			continue
		}
		if cache != nil {
			if len(cache.httpInterfaceByFull[strings.ToLower(typeName+"."+methodName)]) > 0 {
				cache.interfaceMethodCalls[key] = nil
				result[calleeName] = nil
				continue
			}
		}
		typeKey := strings.ToLower(typeName)
		infoByCallee[calleeName] = calleeInfo{
			typeKey:    typeKey,
			typeName:   typeName,
			methodName: methodName,
		}
		typeNamesSet[typeKey] = typeName
		pending = append(pending, calleeName)
	}

	if len(pending) == 0 {
		return result
	}
	var start time.Time
	if stats != nil {
		stats.interfaceBatchRequests += len(pending)
		start = time.Now()
	}

	var typeNames []string
	for _, typeName := range typeNamesSet {
		typeNames = append(typeNames, typeName)
	}
	implsByType := findInterfaceImplementationsBatch(pool, cache, typeNames)

	keysByCallee := make(map[string][]repoFileFuncKey)
	var allKeys []repoFileFuncKey
	for _, calleeName := range pending {
		info, ok := infoByCallee[calleeName]
		if !ok {
			continue
		}
		impls := implsByType[info.typeKey]
		if len(impls) == 0 {
			result[calleeName] = nil
			continue
		}
		for _, impl := range impls {
			key := repoFileFuncKey{
				Repo: impl.Repo,
				File: impl.File,
				Name: impl.ClassName + "." + info.methodName,
			}
			keysByCallee[calleeName] = append(keysByCallee[calleeName], key)
			allKeys = append(allKeys, key)
		}
	}

	callerIDsByKey := findCallerIDsByRepoFileFuncBatchCached(pool, cache, allKeys)

	for _, calleeName := range pending {
		var ids []string
		for _, key := range keysByCallee[calleeName] {
			cacheKeyValue := key.cacheKey()
			callerID := callerIDsByKey[cacheKeyValue]
			if callerID == "" {
				callerID = findUniqueCallerIDByFunctionNameCached(pool, cache, key.Name)
			}
			if callerID != "" {
				ids = append(ids, callerID)
			}
		}
		ids = normalizeCallerIDs(ids)
		result[calleeName] = ids
		if cache != nil {
			cache.interfaceMethodCalls[strings.ToLower(calleeName)] = ids
		}
		if stats != nil {
			stats.interfaceBatchResolved += len(ids)
		}
	}
	if stats != nil {
		stats.interfaceBatchDuration += time.Since(start)
	}

	return result
}

// isInterfaceType checks if a type name is an interface
func isInterfaceType(pool Queryer, typeName string) bool {
	return isInterfaceTypeForSnapshotFilter(pool, typeName, nil, false)
}

func isInterfaceTypeForSnapshotFilter(pool Queryer, typeName string, snapshotIDs []int64, includeLegacy bool) bool {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM interfaces i
		JOIN files f ON f.id = i.file_id
		WHERE i.name = $1 AND %s`, traceSnapshotClause("f.snapshot_id", 2, includeLegacy))
	var count int
	pool.QueryRow(queryContext(pool), query, typeName, snapshotIDs).Scan(&count)
	return count > 0
}

func normalizeTypeName(typeName string) string {
	name := strings.TrimSpace(typeName)
	if name == "" {
		return ""
	}
	if idx := strings.Index(name, "<"); idx > 0 {
		name = name[:idx]
	}
	for strings.HasSuffix(name, "[]") {
		name = strings.TrimSuffix(name, "[]")
	}
	if strings.HasSuffix(name, "...") {
		name = strings.TrimSuffix(name, "...")
	}
	if idx := strings.LastIndex(name, "."); idx != -1 {
		name = name[idx+1:]
	}
	return strings.TrimSpace(name)
}

func resolveAzureTriggerConsumerHandler(pool Queryer, cache *traceCache, consumer SqsConsumer) string {
	if pool == nil || consumer.Repo == "" || consumer.File == "" || consumer.Class == "" || consumer.TriggerType == "" {
		return ""
	}

	var scriptFile string
	query := fmt.Sprintf(`
		SELECT COALESCE(t.script_file, '')
		FROM azure_function_triggers t
		JOIN repositories r ON r.id = t.repo_id
		JOIN files f ON f.id = t.file_id
		WHERE r.name = $1
		  AND f.path = $2
		  AND t.function_name = $3
		  AND t.trigger_type = $4
		  AND %s
		ORDER BY t.id
		LIMIT 1
	`, traceSnapshotClause("f.snapshot_id", 5, includeLegacySnapshotsFromCache(cache)))
	err := pool.QueryRow(queryContext(pool), query, consumer.Repo, consumer.File, consumer.Class, consumer.TriggerType, snapshotIDsFromCache(cache)).Scan(&scriptFile)
	if err != nil {
		return ""
	}

	sourceCandidates := azureTriggerSourcePathCandidates(consumer.File, scriptFile)
	handlerCandidates := azureTriggerHandlerNameCandidates(consumer.TriggerType, consumer.Class)
	for _, file := range sourceCandidates {
		for _, handler := range handlerCandidates {
			if callerID := findCallerIDByRepoFileFuncCached(pool, cache, consumer.Repo, file, handler); callerID != "" {
				return callerID
			}
		}
	}
	return ""
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

func findClassesByName(pool Queryer, className string) []InterfaceImpl {
	return findClassesByNameForSnapshotFilter(pool, className, nil, false)
}

func findClassesByNameForSnapshotFilter(pool Queryer, className string, snapshotIDs []int64, includeLegacy bool) []InterfaceImpl {
	className = normalizeTypeName(className)
	if className == "" {
		return nil
	}
	query := fmt.Sprintf(`
		SELECT c.name, c.id, r.name as repo, f.path as file
		FROM classes c
		JOIN files f ON c.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE c.name = $1 AND %s`, traceSnapshotClause("f.snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, className, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []InterfaceImpl
	for rows.Next() {
		var impl InterfaceImpl
		if err := rows.Scan(&impl.ClassName, &impl.ClassID, &impl.Repo, &impl.File); err != nil {
			continue
		}
		out = append(out, impl)
	}
	return out
}

func findClassesByNameCached(pool Queryer, cache *traceCache, className string) []InterfaceImpl {
	if cache == nil {
		return findClassesByName(pool, className)
	}
	key := strings.ToLower(className)
	if classes, ok := cache.classByName[key]; ok {
		return classes
	}
	classes := findClassesByNameForSnapshotFilter(pool, className, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.classByName[key] = classes
	return classes
}

func isPrimaryClassCached(pool Queryer, cache *traceCache, classID int64) bool {
	if classID == 0 {
		return false
	}
	if cache != nil {
		if isPrimary, ok := cache.primaryClass[classID]; ok {
			return isPrimary
		}
	}
	query := `
		SELECT 1 FROM annotations a
		WHERE a.entity_type = 'class' AND a.entity_id = $1 AND a.name = 'Primary'
		LIMIT 1`
	var found int
	isPrimary := pool.QueryRow(queryContext(pool), query, classID).Scan(&found) == nil
	if cache != nil {
		cache.primaryClass[classID] = isPrimary
	}
	return isPrimary
}

func isPrimaryBeanDefinitionCached(pool Queryer, cache *traceCache, className string) bool {
	className = normalizeTypeName(className)
	if className == "" {
		return false
	}
	key := strings.ToLower(className)
	if cache != nil {
		if isPrimary, ok := cache.primaryBeanByClass[key]; ok {
			return isPrimary
		}
	}
	query := fmt.Sprintf(`
		SELECT 1 FROM bean_definitions b
		JOIN classes c ON c.id = b.config_class_id
		JOIN files f ON f.id = c.file_id
		WHERE LOWER(b.bean_type) = LOWER($1) AND b.is_primary = true
		  AND %s
		LIMIT 1`, traceSnapshotClause("f.snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
	var found int
	isPrimary := pool.QueryRow(queryContext(pool), query, className, snapshotIDsFromCache(cache)).Scan(&found) == nil
	if cache != nil {
		cache.primaryBeanByClass[key] = isPrimary
	}
	return isPrimary
}

func findPrimaryImplementation(pool Queryer, cache *traceCache, impls []InterfaceImpl) *InterfaceImpl {
	var primary *InterfaceImpl
	for i, impl := range impls {
		if isPrimaryClassCached(pool, cache, impl.ClassID) || isPrimaryBeanDefinitionCached(pool, cache, impl.ClassName) {
			if primary != nil {
				return nil
			}
			primary = &impls[i]
		}
	}
	return primary
}

// InjectedField represents a field with dependency injection
type InjectedField struct {
	FieldName     string
	FieldType     string
	IsInjected    bool
	InjectionType string
	ClassName     string
	ClassID       int64
	FieldID       int64
	Qualifier     string // @Qualifier("name") value for disambiguation
}

// findInjectedFieldsForClass finds all @Autowired/@Inject fields for a class
func findInjectedFieldsForClass(pool Queryer, className string) []InjectedField {
	query := `
		SELECT f.name, f.field_type, f.is_injected, COALESCE(f.injection_type, ''), c.name, c.id
		FROM fields f
		JOIN classes c ON f.class_id = c.id
		WHERE c.name = $1 AND f.is_injected = true`

	rows, err := pool.Query(queryContext(pool), query, className)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var fields []InjectedField
	for rows.Next() {
		var f InjectedField
		rows.Scan(&f.FieldName, &f.FieldType, &f.IsInjected, &f.InjectionType, &f.ClassName, &f.ClassID)
		fields = append(fields, f)
	}
	return fields
}

// findFieldType finds the type of a field in a class
func findFieldType(pool Queryer, className, fieldName string) string {
	info := findFieldInfo(pool, className, fieldName)
	return info.FieldType
}

func findFieldInfoCached(pool Queryer, cache *traceCache, className, fieldName string) InjectedField {
	if cache == nil {
		return findFieldInfo(pool, className, fieldName)
	}
	key := cacheKey(className, fieldName)
	if info, ok := cache.fieldInfo[key]; ok {
		return info
	}
	info := findFieldInfoForSnapshotFilter(pool, className, fieldName, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.fieldInfo[key] = info
	return info
}

func getInjectedFieldNamesCached(pool Queryer, cache *traceCache, className string) map[string]bool {
	if cache == nil {
		return getInjectedFieldNames(pool, className)
	}
	key := strings.ToLower(strings.TrimSpace(className))
	if names, ok := cache.injectedFieldNames[key]; ok {
		return names
	}
	names := getInjectedFieldNamesForSnapshotFilter(pool, className, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.injectedFieldNames[key] = names
	return names
}

func getInjectedFieldNames(pool Queryer, className string) map[string]bool {
	return getInjectedFieldNamesForSnapshotFilter(pool, className, nil, false)
}

func getInjectedFieldNamesForSnapshotFilter(pool Queryer, className string, snapshotIDs []int64, includeLegacy bool) map[string]bool {
	names := make(map[string]bool)
	if className == "" {
		return names
	}
	query := fmt.Sprintf(`
		SELECT f.name
		FROM fields f
		JOIN classes c ON f.class_id = c.id
		JOIN files source ON source.id = c.file_id
		WHERE c.name = $1 AND COALESCE(f.is_injected, false) = true
		  AND %s`, traceSnapshotClause("source.snapshot_id", 2, includeLegacy))
	rows, err := pool.Query(queryContext(pool), query, className, snapshotIDs)
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil && name != "" {
				names[name] = true
			}
		}
		rows.Close()
	}

	query = fmt.Sprintf(`
		SELECT cp.param_name
		FROM constructor_params cp
		JOIN classes c ON cp.class_id = c.id
		JOIN files source ON source.id = c.file_id
		WHERE c.name = $1 AND COALESCE(cp.is_injected, false) = true
		  AND %s`, traceSnapshotClause("source.snapshot_id", 2, includeLegacy))
	rows, err = pool.Query(queryContext(pool), query, className, snapshotIDs)
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil && name != "" {
				names[name] = true
			}
		}
		rows.Close()
	}
	return names
}

// findFieldInfo finds detailed info about a field including @Qualifier annotation
func findFieldInfo(pool Queryer, className, fieldName string) InjectedField {
	return findFieldInfoForSnapshotFilter(pool, className, fieldName, nil, false)
}

func findFieldInfoForSnapshotFilter(pool Queryer, className, fieldName string, snapshotIDs []int64, includeLegacy bool) InjectedField {
	query := fmt.Sprintf(`
		SELECT f.id, f.field_type, COALESCE(f.is_injected, false), COALESCE(f.injection_type, '')
		FROM fields f
		JOIN classes c ON f.class_id = c.id
		JOIN files source ON source.id = c.file_id
		WHERE c.name = $1 AND f.name = $2
		  AND %s
		ORDER BY c.id, f.id
		LIMIT 1`, traceSnapshotClause("source.snapshot_id", 3, includeLegacy))

	var info InjectedField
	info.FieldName = fieldName
	info.ClassName = className

	var fieldID int64
	err := pool.QueryRow(queryContext(pool), query, className, fieldName, snapshotIDs).Scan(
		&fieldID, &info.FieldType, &info.IsInjected, &info.InjectionType)
	if err != nil {
		return info
	}
	info.FieldID = fieldID

	// Look up @Qualifier/@Named/@Resource annotation on this field
	qualifierQuery := `
		SELECT COALESCE(a.values->>'value', a.values->>'name')
		FROM annotations a
		WHERE a.entity_type = 'field' AND a.entity_id = $1
		  AND a.name IN ('Qualifier', 'Named', 'Resource')
		ORDER BY CASE a.name WHEN 'Qualifier' THEN 1 WHEN 'Named' THEN 2 ELSE 3 END
		LIMIT 1`
	pool.QueryRow(queryContext(pool), qualifierQuery, fieldID).Scan(&info.Qualifier)
	if info.Qualifier == "" && strings.EqualFold(info.InjectionType, "resource") {
		info.Qualifier = fieldName
	}

	return info
}

// findImplementationByQualifier finds an implementation class matching the qualifier
// Matches by: class name (lowercase first char), @Named annotation, or @Component("name")
func findImplementationByQualifier(pool Queryer, impls []InterfaceImpl, qualifier string) *InterfaceImpl {
	qualifier = strings.TrimSpace(qualifier)
	if qualifier == "" {
		return nil
	}
	lowerQualifier := strings.ToLower(qualifier)

	for i, impl := range impls {
		// Match by class name convention: "stripeGateway" matches "StripeGateway"
		if len(impl.ClassName) > 0 {
			lowerName := strings.ToLower(impl.ClassName[:1]) + impl.ClassName[1:]
			if strings.ToLower(lowerName) == lowerQualifier {
				return &impls[i]
			}
		}

		// Match by @Named/@Qualifier/@Component annotation value
		annotQuery := `
			SELECT 1 FROM annotations a
			WHERE a.entity_type = 'class' AND a.entity_id = $1
			AND a.name IN ('Named', 'Qualifier', 'Component', 'Service', 'Repository')
			AND LOWER(COALESCE(a.values->>'value', a.values->>'name')) = LOWER($2)
			LIMIT 1`
		var found int
		if pool.QueryRow(queryContext(pool), annotQuery, impl.ClassID, qualifier).Scan(&found) == nil {
			return &impls[i]
		}

		// Match by @Bean name or qualifier
		beanQuery := `
			SELECT 1 FROM bean_definitions b
			WHERE LOWER(b.bean_type) = LOWER($1)
			  AND (LOWER(b.bean_name) = LOWER($2)
			       OR (b.qualifiers IS NOT NULL AND b.qualifiers ? $2))
			LIMIT 1`
		if pool.QueryRow(queryContext(pool), beanQuery, impl.ClassName, qualifier).Scan(&found) == nil {
			return &impls[i]
		}
	}
	return nil
}

func findImplementationByQualifierCached(pool Queryer, cache *traceCache, impls []InterfaceImpl, qualifier string) *InterfaceImpl {
	qualifier = strings.TrimSpace(qualifier)
	if qualifier == "" {
		return nil
	}
	if cache == nil {
		return findImplementationByQualifier(pool, impls, qualifier)
	}
	lowerQualifier := strings.ToLower(qualifier)

	for i, impl := range impls {
		if len(impl.ClassName) > 0 {
			lowerName := strings.ToLower(impl.ClassName[:1]) + impl.ClassName[1:]
			if strings.ToLower(lowerName) == lowerQualifier {
				return &impls[i]
			}
		}

		key := fmt.Sprintf("%d|%s", impl.ClassID, qualifier)
		if matched, ok := cache.qualifierCache[key]; ok {
			if matched {
				return &impls[i]
			}
			continue
		}

		annotQuery := `
			SELECT 1 FROM annotations a
			WHERE a.entity_type = 'class' AND a.entity_id = $1
			AND a.name IN ('Named', 'Qualifier', 'Component', 'Service', 'Repository')
			AND LOWER(COALESCE(a.values->>'value', a.values->>'name')) = LOWER($2)
			LIMIT 1`
		var found int
		if pool.QueryRow(queryContext(pool), annotQuery, impl.ClassID, qualifier).Scan(&found) == nil {
			cache.qualifierCache[key] = true
			return &impls[i]
		}
		beanQuery := fmt.Sprintf(`
			SELECT 1 FROM bean_definitions b
			JOIN classes c ON c.id = b.config_class_id
			JOIN files f ON f.id = c.file_id
			WHERE LOWER(b.bean_type) = LOWER($1)
			  AND (LOWER(b.bean_name) = LOWER($2)
			       OR (b.qualifiers IS NOT NULL AND b.qualifiers ? $2))
			  AND %s
			LIMIT 1`, traceSnapshotClause("f.snapshot_id", 3, includeLegacySnapshotsFromCache(cache)))
		if pool.QueryRow(queryContext(pool), beanQuery, impl.ClassName, qualifier, snapshotIDsFromCache(cache)).Scan(&found) == nil {
			cache.qualifierCache[key] = true
			return &impls[i]
		}
		cache.qualifierCache[key] = false
	}
	return nil
}

// findConstructorInjectedParams finds constructor parameters with injection annotations
func findConstructorInjectedParams(pool Queryer, className string) []InjectedField {
	return findConstructorInjectedParamsForSnapshotFilter(pool, className, nil, false)
}

func findConstructorInjectedParamsForSnapshotFilter(pool Queryer, className string, snapshotIDs []int64, includeLegacy bool) []InjectedField {
	query := fmt.Sprintf(`
		SELECT cp.param_name, cp.param_type, cp.is_injected, COALESCE(cp.annotation, ''),
		       COALESCE(cp.annotation_value, ''), c.name, c.id,
		       COALESCE((
		           SELECT COALESCE(a.values->>'value', a.values->>'name')
		           FROM annotations a
		           WHERE a.entity_type = 'parameter' AND a.entity_id = cp.id
		             AND a.name IN ('Qualifier', 'Named', 'Resource')
		           ORDER BY CASE a.name WHEN 'Qualifier' THEN 1 WHEN 'Named' THEN 2 ELSE 3 END
		           LIMIT 1
		       ), '')
		FROM constructor_params cp
		JOIN classes c ON cp.class_id = c.id
		JOIN files source ON source.id = c.file_id
		WHERE c.name = $1 AND cp.is_injected = true
		  AND %s
		ORDER BY c.id, cp.id`, traceSnapshotClause("source.snapshot_id", 2, includeLegacy))

	rows, err := pool.Query(queryContext(pool), query, className, snapshotIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var params []InjectedField
	for rows.Next() {
		var p InjectedField
		var annotValue, qualifier string
		if err := rows.Scan(&p.FieldName, &p.FieldType, &p.IsInjected, &p.InjectionType,
			&annotValue, &p.ClassName, &p.ClassID, &qualifier); err != nil {
			return nil
		}

		// If annotation is Qualifier/Named/Resource, use the annotation_value directly
		if strings.EqualFold(p.InjectionType, "Qualifier") ||
			strings.EqualFold(p.InjectionType, "Named") ||
			strings.EqualFold(p.InjectionType, "Resource") {
			p.Qualifier = annotValue
		}
		if p.Qualifier == "" {
			p.Qualifier = qualifier
		}
		if p.Qualifier == "" && strings.EqualFold(p.InjectionType, "Resource") {
			p.Qualifier = p.FieldName
		}
		params = append(params, p)
	}
	if rows.Err() != nil {
		return nil
	}
	return params
}

func findConstructorInjectedParamsCached(pool Queryer, cache *traceCache, className string) []InjectedField {
	if cache == nil {
		return findConstructorInjectedParams(pool, className)
	}
	key := strings.ToLower(className)
	if params, ok := cache.ctorParams[key]; ok {
		return params
	}
	params := findConstructorInjectedParamsForSnapshotFilter(pool, className, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache))
	cache.ctorParams[key] = params
	return params
}

// resolveInjectedFieldCall resolves a method call on an injected field
// Given a class and a method call like "gateway.charge()", looks up the field type
// and resolves to implementations if it's an interface
// Uses @Qualifier annotation to disambiguate when multiple implementations exist
func resolveInjectedFieldCall(pool Queryer, className, calleeName string) []string {
	parts := strings.Split(calleeName, ".")
	if len(parts) < 2 {
		return nil
	}

	fieldName := parts[0]
	methodName := parts[len(parts)-1]

	// Look up the field info including @Qualifier
	fieldInfo := findFieldInfo(pool, className, fieldName)
	fieldType := normalizeTypeName(fieldInfo.FieldType)
	qualifier := fieldInfo.Qualifier
	isInjected := fieldInfo.IsInjected
	injectionType := strings.ToLower(fieldInfo.InjectionType)

	if injectionType == "resource" && qualifier == "" {
		qualifier = fieldName
	}
	if injectionType == "value" {
		return nil
	}

	if fieldType == "" || !isInjected {
		// Try constructor params (injected only)
		ctorParams := findConstructorInjectedParams(pool, className)
		for _, p := range ctorParams {
			if p.FieldName == fieldName {
				fieldType = normalizeTypeName(p.FieldType)
				qualifier = p.Qualifier
				isInjected = p.IsInjected
				injectionType = strings.ToLower(p.InjectionType)
				if injectionType == "resource" && qualifier == "" {
					qualifier = p.FieldName
				}
				break
			}
		}
	}

	if fieldType == "" || !isInjected {
		return nil
	}
	if injectionType == "value" {
		return nil
	}

	var impls []InterfaceImpl
	if isInterfaceType(pool, fieldType) {
		// Check if the field type is an interface and resolve to implementations
		impls = findInterfaceImplementations(pool, fieldType)
	} else {
		classes := findClassesByName(pool, fieldType)
		if len(classes) != 1 {
			return nil
		}
		impls = classes
	}
	if len(impls) == 0 {
		return nil
	}

	// If there's a qualifier and multiple implementations, filter by qualifier
	if qualifier != "" && len(impls) > 1 {
		if matchedImpl := findImplementationByQualifier(pool, impls, qualifier); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else {
			return nil
		}
	} else if qualifier == "" && len(impls) > 1 {
		// Spring fallback: bean name defaults to field name when no explicit qualifier.
		if matchedImpl := findImplementationByQualifier(pool, impls, fieldName); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else if matchedImpl := findPrimaryImplementation(pool, nil, impls); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else {
			return nil
		}
	}

	var callerIDs []string
	for _, impl := range impls {
		implMethodName := impl.ClassName + "." + methodName
		callerID := findCallerIDByRepoFileFunc(pool, impl.Repo, impl.File, implMethodName, nil)
		if callerID == "" {
			callerID = findUniqueCallerIDByFunctionName(pool, implMethodName)
		}
		if callerID != "" {
			callerIDs = append(callerIDs, callerID)
		}
	}

	return normalizeCallerIDs(callerIDs)
}

func resolveInjectedFieldCallCached(pool Queryer, cache *traceCache, className, calleeName string) []string {
	if cache == nil {
		return resolveInjectedFieldCall(pool, className, calleeName)
	}
	stats := cache.resolveStats
	key := cacheKey(className, calleeName)
	if ids, ok := cache.injectedFieldCalls[key]; ok {
		if stats != nil {
			stats.injectedCacheHits++
			stats.injectedResolvedEdges += len(ids)
		}
		return ids
	}
	var start time.Time
	if stats != nil {
		stats.injectedRequests++
		start = time.Now()
	}

	parts := strings.Split(calleeName, ".")
	if len(parts) < 2 {
		cache.injectedFieldCalls[key] = nil
		if stats != nil {
			stats.injectedDuration += time.Since(start)
		}
		return nil
	}

	fieldName := parts[0]
	methodName := parts[len(parts)-1]

	if cache != nil {
		injectedNames := getInjectedFieldNamesCached(pool, cache, className)
		if len(injectedNames) == 0 || !injectedNames[fieldName] {
			cache.injectedFieldCalls[key] = nil
			if stats != nil {
				stats.injectedDuration += time.Since(start)
			}
			return nil
		}
	}

	fieldInfo := findFieldInfoCached(pool, cache, className, fieldName)
	fieldType := normalizeTypeName(fieldInfo.FieldType)
	qualifier := fieldInfo.Qualifier
	isInjected := fieldInfo.IsInjected
	injectionType := strings.ToLower(fieldInfo.InjectionType)

	if injectionType == "resource" && qualifier == "" {
		qualifier = fieldName
	}
	if injectionType == "value" {
		cache.injectedFieldCalls[key] = nil
		if stats != nil {
			stats.injectedDuration += time.Since(start)
		}
		return nil
	}

	if fieldType == "" || !isInjected {
		ctorParams := findConstructorInjectedParamsCached(pool, cache, className)
		for _, p := range ctorParams {
			if p.FieldName == fieldName {
				fieldType = normalizeTypeName(p.FieldType)
				qualifier = p.Qualifier
				isInjected = p.IsInjected
				injectionType = strings.ToLower(p.InjectionType)
				if injectionType == "resource" && qualifier == "" {
					qualifier = p.FieldName
				}
				break
			}
		}
	}

	if fieldType == "" || !isInjected {
		cache.injectedFieldCalls[key] = nil
		if stats != nil {
			stats.injectedDuration += time.Since(start)
		}
		return nil
	}
	if injectionType == "value" {
		cache.injectedFieldCalls[key] = nil
		if stats != nil {
			stats.injectedDuration += time.Since(start)
		}
		return nil
	}

	var impls []InterfaceImpl
	if isInterfaceTypeForSnapshotFilter(pool, fieldType, snapshotIDsFromCache(cache), includeLegacySnapshotsFromCache(cache)) {
		impls = findInterfaceImplementationsCached(pool, cache, fieldType)
	} else {
		classes := findClassesByNameCached(pool, cache, fieldType)
		if len(classes) != 1 {
			cache.injectedFieldCalls[key] = nil
			if stats != nil {
				stats.injectedDuration += time.Since(start)
			}
			return nil
		}
		impls = classes
	}
	if len(impls) == 0 {
		cache.injectedFieldCalls[key] = nil
		if stats != nil {
			stats.injectedDuration += time.Since(start)
		}
		return nil
	}

	if qualifier != "" && len(impls) > 1 {
		if matchedImpl := findImplementationByQualifierCached(pool, cache, impls, qualifier); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else {
			cache.injectedFieldCalls[key] = nil
			if stats != nil {
				stats.injectedDuration += time.Since(start)
			}
			return nil
		}
	} else if qualifier == "" && len(impls) > 1 {
		if matchedImpl := findImplementationByQualifierCached(pool, cache, impls, fieldName); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else if matchedImpl := findPrimaryImplementation(pool, cache, impls); matchedImpl != nil {
			impls = []InterfaceImpl{*matchedImpl}
		} else {
			cache.injectedFieldCalls[key] = nil
			if stats != nil {
				stats.injectedDuration += time.Since(start)
			}
			return nil
		}
	}

	var callerIDs []string
	for _, impl := range impls {
		implMethodName := impl.ClassName + "." + methodName
		callerID := findCallerIDByRepoFileFuncCached(pool, cache, impl.Repo, impl.File, implMethodName)
		if callerID == "" {
			callerID = findUniqueCallerIDByFunctionNameCached(pool, cache, implMethodName)
		}
		if callerID != "" {
			callerIDs = append(callerIDs, callerID)
		}
	}
	callerIDs = normalizeCallerIDs(callerIDs)

	cache.injectedFieldCalls[key] = callerIDs
	if stats != nil {
		stats.injectedResolvedEdges += len(callerIDs)
		stats.injectedDuration += time.Since(start)
	}
	return callerIDs
}

type injectedCallRequest struct {
	className  string
	calleeName string
	fieldName  string
	methodName string
	key        string
}

func resolveInjectedFieldCallBatchCached(pool Queryer, cache *traceCache, requests []injectedCallRequest) map[string][]string {
	result := make(map[string][]string, len(requests))
	if len(requests) == 0 {
		return result
	}

	seen := make(map[string]bool, len(requests))
	var pending []injectedCallRequest
	type classField struct {
		className string
		fieldName string
	}
	classFieldKeys := make(map[string]classField)
	var stats *resolveStats
	if cache != nil {
		stats = cache.resolveStats
	}

	for _, req := range requests {
		if req.className == "" || req.calleeName == "" {
			continue
		}
		key := cacheKey(req.className, req.calleeName)
		if seen[key] {
			continue
		}
		seen[key] = true
		if cache != nil {
			if ids, ok := cache.injectedFieldCalls[key]; ok {
				result[key] = ids
				if stats != nil {
					stats.injectedCacheHits++
					stats.injectedResolvedEdges += len(ids)
				}
				continue
			}
		}
		parts := strings.Split(req.calleeName, ".")
		if len(parts) < 2 {
			if cache != nil {
				cache.injectedFieldCalls[key] = nil
			}
			result[key] = nil
			continue
		}
		fieldName := parts[0]
		methodName := parts[len(parts)-1]
		if fieldName == "" || methodName == "" {
			if cache != nil {
				cache.injectedFieldCalls[key] = nil
			}
			result[key] = nil
			continue
		}
		pending = append(pending, injectedCallRequest{
			className:  req.className,
			calleeName: req.calleeName,
			fieldName:  fieldName,
			methodName: methodName,
			key:        key,
		})
		classFieldKeys[cacheKey(req.className, fieldName)] = classField{
			className: req.className,
			fieldName: fieldName,
		}
	}

	if len(pending) == 0 {
		return result
	}
	var start time.Time
	if stats != nil {
		stats.injectedBatchRequests += len(pending)
		start = time.Now()
	}

	var classNames []string
	var fieldNames []string
	for _, cf := range classFieldKeys {
		classNames = append(classNames, cf.className)
		fieldNames = append(fieldNames, cf.fieldName)
	}

	fieldInfoByKey := make(map[string]InjectedField)
	if len(classNames) > 0 {
		query := fmt.Sprintf(`
			WITH input AS (
				SELECT * FROM unnest($1::text[], $2::text[])
					AS t(class_name, field_name)
			)
			SELECT t.class_name,
			       t.field_name,
			       f.id,
			       f.field_type,
			       COALESCE(f.is_injected, false),
			       COALESCE(f.injection_type, ''),
			       COALESCE((
			           SELECT COALESCE(a.values->>'value', a.values->>'name')
			           FROM annotations a
			           WHERE a.entity_type = 'field'
			             AND a.entity_id = f.id
			             AND a.name IN ('Qualifier', 'Named', 'Resource')
			           ORDER BY CASE a.name WHEN 'Qualifier' THEN 1 WHEN 'Named' THEN 2 ELSE 3 END
			           LIMIT 1
			       ), '')
				FROM input t
				JOIN classes c ON c.name = t.class_name
				JOIN fields f ON f.class_id = c.id AND f.name = t.field_name
				JOIN files source ON source.id = c.file_id
				WHERE %s
				ORDER BY c.id, f.id`, traceSnapshotClause("source.snapshot_id", 3, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, classNames, fieldNames, snapshotIDsFromCache(cache))
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var className, fieldName string
				var fieldID int64
				var fieldType, injectionType, qualifier string
				var isInjected bool
				if err := rows.Scan(&className, &fieldName, &fieldID, &fieldType, &isInjected, &injectionType, &qualifier); err != nil {
					continue
				}
				info := InjectedField{
					FieldName:     fieldName,
					FieldType:     fieldType,
					IsInjected:    isInjected,
					InjectionType: injectionType,
					ClassName:     className,
					FieldID:       fieldID,
					Qualifier:     qualifier,
				}
				fieldInfoByKey[cacheKey(className, fieldName)] = info
				if cache != nil {
					cache.fieldInfo[cacheKey(className, fieldName)] = info
				}
			}
		}
	}

	ctorInfoByKey := make(map[string]InjectedField)
	if len(classNames) > 0 {
		query := fmt.Sprintf(`
			WITH input AS (
				SELECT * FROM unnest($1::text[], $2::text[])
					AS t(class_name, field_name)
			)
			SELECT t.class_name,
			       t.field_name,
			       cp.id,
			       cp.param_type,
			       cp.is_injected,
			       COALESCE(cp.annotation, ''),
			       COALESCE(cp.annotation_value, ''),
			       COALESCE((
			           SELECT COALESCE(a.values->>'value', a.values->>'name')
			           FROM annotations a
			           WHERE a.entity_type = 'parameter'
			             AND a.entity_id = cp.id
			             AND a.name IN ('Qualifier', 'Named', 'Resource')
			           ORDER BY CASE a.name WHEN 'Qualifier' THEN 1 WHEN 'Named' THEN 2 ELSE 3 END
			           LIMIT 1
			       ), '')
			FROM input t
			JOIN classes c ON c.name = t.class_name
				JOIN constructor_params cp
				  ON cp.class_id = c.id
				 AND cp.param_name = t.field_name
				 AND COALESCE(cp.is_injected, false) = true
				JOIN files source ON source.id = c.file_id
				WHERE %s
				ORDER BY c.id, cp.id`, traceSnapshotClause("source.snapshot_id", 3, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, classNames, fieldNames, snapshotIDsFromCache(cache))
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var className, fieldName string
				var paramID int64
				var fieldType, injectionType, annotValue, qualifier string
				var isInjected bool
				if err := rows.Scan(&className, &fieldName, &paramID, &fieldType, &isInjected, &injectionType, &annotValue, &qualifier); err != nil {
					continue
				}
				info := InjectedField{
					FieldName:     fieldName,
					FieldType:     fieldType,
					IsInjected:    isInjected,
					InjectionType: injectionType,
					ClassName:     className,
					FieldID:       paramID,
				}
				if strings.EqualFold(injectionType, "Qualifier") ||
					strings.EqualFold(injectionType, "Named") ||
					strings.EqualFold(injectionType, "Resource") {
					info.Qualifier = annotValue
				}
				if info.Qualifier == "" {
					info.Qualifier = qualifier
				}
				if info.Qualifier == "" && strings.EqualFold(injectionType, "Resource") {
					info.Qualifier = fieldName
				}
				ctorInfoByKey[cacheKey(className, fieldName)] = info
			}
		}
	}

	interfaceTypes := make(map[string]string)
	typeByReq := make(map[string]string)
	qualifierByReq := make(map[string]string)
	injectionTypeByReq := make(map[string]string)
	injectedOK := make(map[string]bool)

	for _, req := range pending {
		fieldKey := cacheKey(req.className, req.fieldName)
		fieldInfo := fieldInfoByKey[fieldKey]
		fieldType := normalizeTypeName(fieldInfo.FieldType)
		qualifier := fieldInfo.Qualifier
		isInjected := fieldInfo.IsInjected
		injectionType := strings.ToLower(fieldInfo.InjectionType)

		if injectionType == "resource" && qualifier == "" {
			qualifier = req.fieldName
		}
		if injectionType == "value" {
			result[req.key] = nil
			if cache != nil {
				cache.injectedFieldCalls[req.key] = nil
			}
			continue
		}

		if fieldType == "" || !isInjected {
			if ctorInfo, ok := ctorInfoByKey[fieldKey]; ok {
				fieldType = normalizeTypeName(ctorInfo.FieldType)
				qualifier = ctorInfo.Qualifier
				isInjected = ctorInfo.IsInjected
				injectionType = strings.ToLower(ctorInfo.InjectionType)
				if injectionType == "resource" && qualifier == "" {
					qualifier = ctorInfo.FieldName
				}
			}
		}

		if fieldType == "" || !isInjected || injectionType == "value" {
			result[req.key] = nil
			if cache != nil {
				cache.injectedFieldCalls[req.key] = nil
			}
			continue
		}

		lowerType := strings.ToLower(fieldType)
		typeByReq[req.key] = fieldType
		qualifierByReq[req.key] = qualifier
		injectionTypeByReq[req.key] = injectionType
		interfaceTypes[lowerType] = fieldType
		injectedOK[req.key] = true
	}

	var interfaceNames []string
	for _, name := range interfaceTypes {
		interfaceNames = append(interfaceNames, name)
	}

	interfaceNameSet := make(map[string]bool)
	if len(interfaceNames) > 0 {
		query := fmt.Sprintf(`SELECT i.name FROM interfaces i
			JOIN files f ON f.id = i.file_id
			WHERE i.name = ANY($1) AND %s`, traceSnapshotClause("f.snapshot_id", 2, includeLegacySnapshotsFromCache(cache)))
		rows, err := pool.Query(queryContext(pool), query, interfaceNames, snapshotIDsFromCache(cache))
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil && name != "" {
					interfaceNameSet[strings.ToLower(name)] = true
				}
			}
		}
	}

	implsByInterface := findInterfaceImplementationsBatch(pool, cache, interfaceNames)

	keysByReq := make(map[string][]repoFileFuncKey)
	var allKeys []repoFileFuncKey

	for _, req := range pending {
		if !injectedOK[req.key] {
			continue
		}
		fieldType := typeByReq[req.key]
		if fieldType == "" {
			result[req.key] = nil
			continue
		}
		lowerType := strings.ToLower(fieldType)
		var impls []InterfaceImpl
		if interfaceNameSet[lowerType] {
			impls = implsByInterface[lowerType]
		} else {
			classes := findClassesByNameCached(pool, cache, fieldType)
			if len(classes) != 1 {
				result[req.key] = nil
				continue
			}
			impls = classes
		}

		if len(impls) == 0 {
			result[req.key] = nil
			continue
		}

		qualifier := qualifierByReq[req.key]
		if qualifier != "" && len(impls) > 1 {
			if matchedImpl := findImplementationByQualifierCached(pool, cache, impls, qualifier); matchedImpl != nil {
				impls = []InterfaceImpl{*matchedImpl}
			} else {
				result[req.key] = nil
				continue
			}
		} else if qualifier == "" && len(impls) > 1 {
			if matchedImpl := findImplementationByQualifierCached(pool, cache, impls, req.fieldName); matchedImpl != nil {
				impls = []InterfaceImpl{*matchedImpl}
			} else if matchedImpl := findPrimaryImplementation(pool, cache, impls); matchedImpl != nil {
				impls = []InterfaceImpl{*matchedImpl}
			} else {
				result[req.key] = nil
				continue
			}
		}

		for _, impl := range impls {
			key := repoFileFuncKey{
				Repo: impl.Repo,
				File: impl.File,
				Name: impl.ClassName + "." + req.methodName,
			}
			keysByReq[req.key] = append(keysByReq[req.key], key)
			allKeys = append(allKeys, key)
		}
	}

	callerIDsByKey := findCallerIDsByRepoFileFuncBatchCached(pool, cache, allKeys)

	for _, req := range pending {
		if _, ok := result[req.key]; ok {
			continue
		}
		var ids []string
		for _, key := range keysByReq[req.key] {
			cacheKeyValue := key.cacheKey()
			callerID := callerIDsByKey[cacheKeyValue]
			if callerID == "" {
				callerID = findUniqueCallerIDByFunctionNameCached(pool, cache, key.Name)
			}
			if callerID != "" {
				ids = append(ids, callerID)
			}
		}
		ids = normalizeCallerIDs(ids)
		result[req.key] = ids
		if cache != nil {
			cache.injectedFieldCalls[req.key] = ids
		}
		if stats != nil {
			stats.injectedBatchResolved += len(ids)
		}
	}
	if stats != nil {
		stats.injectedBatchDuration += time.Since(start)
	}

	return result
}
