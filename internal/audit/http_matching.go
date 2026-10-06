package audit

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/graph"
)

type auditHTTPCall struct {
	RepoName   string
	CallerID   string
	LineNumber int
	ClientType string
	Method     string
	Path       string
}

type auditHTTPEndpoint struct {
	Method string
	Path   string
}

type auditHTTPClassification struct {
	call             auditHTTPCall
	routeLike        bool
	methodIsHTTP     bool
	hasPathMatch     bool
	hasMethodMatch   bool
	availableMethods []string
}

var (
	auditHTTPRouteLikeRe   = regexp.MustCompile(`^/(?:[a-z0-9._-]+|:[a-z0-9._-]+|\{[a-z0-9._-]+\})(?:/(?:[a-z0-9._-]+|:[a-z0-9._-]+|\{[a-z0-9._-]+\}))*$`)
	auditHTTPBraceParamRe  = regexp.MustCompile(`\{([^/{}]+)\}`)
	auditHTTPColonParamRe  = regexp.MustCompile(`:([a-z0-9._-]+)`)
	auditHTTPBareAPIRe     = regexp.MustCompile(`^/api[^/]*$`)
	auditHTTPSingleParamRe = regexp.MustCompile(`^/:[^/]+$`)
)

func collectHTTPClassifications(ctx context.Context, pool *pgxpool.Pool, filters ...graph.SnapshotFilter) ([]auditHTTPClassification, error) {
	calls, err := loadAuditHTTPCalls(ctx, pool, filters...)
	if err != nil {
		return nil, err
	}
	endpoints, err := loadAuditHTTPEndpoints(ctx, pool, filters...)
	if err != nil {
		return nil, err
	}

	classified := make([]auditHTTPClassification, 0, len(calls))
	for _, call := range calls {
		classified = append(classified, classifyAuditHTTPCall(call, endpoints))
	}
	return classified, nil
}

func loadAuditHTTPCalls(ctx context.Context, pool *pgxpool.Pool, filters ...graph.SnapshotFilter) ([]auditHTTPCall, error) {
	scope, args := auditSnapshotPredicate("h.snapshot_id", filters)
	rows, err := pool.Query(ctx, strings.ReplaceAll(`
		SELECT
			r.name,
			h.caller_id,
			COALESCE(h.line_number, 0),
			COALESCE(h.client_type, ''),
			UPPER(h.http_method),
			COALESCE(h.url_pattern, '')
		FROM http_client_calls h
		JOIN repositories r ON r.id = h.repo_id
		WHERE h.url_pattern LIKE '/%%'
		  AND split_part(h.caller_id, ':', 2) NOT LIKE '.codebase-snapshots/%%'
 AND /* scope */
	`, "/* scope */", scope), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	calls := make([]auditHTTPCall, 0)
	for rows.Next() {
		var call auditHTTPCall
		if err := rows.Scan(&call.RepoName, &call.CallerID, &call.LineNumber, &call.ClientType, &call.Method, &call.Path); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return calls, nil
}

func loadAuditHTTPEndpoints(ctx context.Context, pool *pgxpool.Pool, filters ...graph.SnapshotFilter) ([]auditHTTPEndpoint, error) {
	scope, args := auditSnapshotPredicate("f.snapshot_id", filters)
	rows, err := pool.Query(ctx, strings.ReplaceAll(`
		SELECT DISTINCT
			COALESCE(NULLIF(e.method_canonical, ''), UPPER(e.method)) AS http_method,
			COALESCE(NULLIF(e.path_canonical, ''), LOWER(e.path)) AS path
		FROM endpoints e
		JOIN files f ON f.id = e.file_id
		WHERE f.path NOT LIKE '.codebase-snapshots/%%'
 AND /* scope */
	`, "/* scope */", scope), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	endpoints := make([]auditHTTPEndpoint, 0)
	for rows.Next() {
		var ep auditHTTPEndpoint
		if err := rows.Scan(&ep.Method, &ep.Path); err != nil {
			return nil, err
		}
		ep.Method = strings.ToUpper(strings.TrimSpace(ep.Method))
		ep.Path = normalizeAuditHTTPPath(ep.Path)
		endpoints = append(endpoints, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return endpoints, nil
}

func classifyAuditHTTPCall(call auditHTTPCall, endpoints []auditHTTPEndpoint) auditHTTPClassification {
	path := normalizeAuditHTTPPath(call.Path)
	method := strings.ToUpper(strings.TrimSpace(call.Method))
	methodIsHTTP := isAuditHTTPMethod(method)
	routeLike := auditHTTPRouteLike(method, call.ClientType, path)

	if !routeLike {
		return auditHTTPClassification{
			call:         call,
			routeLike:    false,
			methodIsHTTP: methodIsHTTP,
		}
	}

	var availableMethods []string
	methodMatched := false
	prefixes := config.GetEffectiveTraceConfig().HTTPForRepo(call.RepoName).ContextPathPrefixes
	for _, ep := range endpoints {
		if !auditHTTPPathMatches(method, path, ep.Method, ep.Path, prefixes...) {
			continue
		}
		availableMethods = append(availableMethods, ep.Method)
		if ep.Method == method || ep.Method == "REQUEST" || method == "REQUEST" || method == "ANY" {
			methodMatched = true
		}
	}
	availableMethods = uniqueSortedStrings(availableMethods)

	return auditHTTPClassification{
		call:             call,
		routeLike:        true,
		methodIsHTTP:     methodIsHTTP,
		hasPathMatch:     len(availableMethods) > 0,
		hasMethodMatch:   methodMatched,
		availableMethods: availableMethods,
	}
}

func normalizeAuditHTTPPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		if parsed, err := url.Parse(path); err == nil && parsed.Path != "" {
			path = parsed.Path
		}
	}
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
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

func auditHTTPRouteLike(method, clientType, path string) bool {
	if strings.EqualFold(strings.TrimSpace(clientType), "navigation") {
		return false
	}
	if path == "/" || !auditHTTPRouteLikeRe.MatchString(path) {
		return false
	}
	if (method == "REQUEST" || method == "ANY") && auditHTTPBareAPIRe.MatchString(path) {
		return false
	}
	if (method == "REQUEST" || method == "ANY") && auditHTTPSingleParamRe.MatchString(path) {
		return false
	}
	return true
}

func isAuditHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func auditHTTPPathMatches(callMethod, callPath, endpointMethod, endpointPath string, prefixes ...string) bool {
	callVariants := auditHTTPPathVariants(callPath, prefixes)
	endpointVariants := auditHTTPPathVariants(endpointPath, prefixes)
	for _, left := range callVariants {
		leftSegs := auditHTTPPathSegments(left)
		for _, right := range endpointVariants {
			if strings.EqualFold(left, right) || auditHTTPSegmentsEquivalent(leftSegs, auditHTTPPathSegments(right)) {
				return true
			}
		}
	}
	return false
}

func auditHTTPPathVariants(path string, prefixes []string) []string {
	normalized := normalizeAuditHTTPPath(path)
	if normalized == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		value = normalizeAuditHTTPPath(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		out = append(out, value)

		colon := auditHTTPBraceParamRe.ReplaceAllString(value, ":$1")
		if colon != value && !seen[colon] {
			seen[colon] = true
			out = append(out, colon)
		}
		braces := auditHTTPColonParamRe.ReplaceAllString(value, "{$1}")
		if braces != value && !seen[braces] {
			seen[braces] = true
			out = append(out, braces)
		}
	}

	add(normalized)
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(normalizeAuditHTTPPath(prefix), "/")
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(normalized, prefix+"/") {
			add(strings.TrimPrefix(normalized, prefix))
		} else if normalized != prefix {
			add(prefix + normalized)
		}
	}
	return out
}

func auditHTTPPathSegments(path string) []string {
	path = normalizeAuditHTTPPath(path)
	if path == "/" {
		return nil
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
	return segments
}

func auditHTTPSegmentsEquivalent(left, right []string) bool {
	if len(left) == len(right) {
		return auditHTTPSegmentsEqualLength(left, right)
	}
	if len(left) < len(right) {
		return auditHTTPMatchesWithTrailingWildcards(left, right)
	}
	if len(right) < len(left) {
		return auditHTTPMatchesWithTrailingWildcards(right, left)
	}
	return false
}

func auditHTTPSegmentsEqualLength(left, right []string) bool {
	for i := range left {
		if left[i] == right[i] {
			continue
		}
		if auditHTTPIsWildcardSegment(left[i]) || auditHTTPIsWildcardSegment(right[i]) {
			continue
		}
		return false
	}
	return len(left) > 0
}

func auditHTTPMatchesWithTrailingWildcards(shorter, longer []string) bool {
	if len(shorter) == 0 || len(longer) == 0 || len(shorter) >= len(longer) {
		return false
	}
	for i := range shorter {
		if shorter[i] == longer[i] {
			continue
		}
		if auditHTTPIsWildcardSegment(shorter[i]) || auditHTTPIsWildcardSegment(longer[i]) {
			continue
		}
		return false
	}
	for _, segment := range longer[len(shorter):] {
		if !auditHTTPIsWildcardSegment(segment) {
			return false
		}
	}
	return true
}

func auditHTTPIsWildcardSegment(segment string) bool {
	return strings.HasPrefix(segment, ":") || (strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"))
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToUpper(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

// Callers that supply a captured workspace selection exclude unpublished and
// archived generations from both sides of matching. Unscoped offline audits
// retain their database-wide behavior.
func auditSnapshotPredicate(column string, filters []graph.SnapshotFilter) (string, []any) {
	if len(filters) == 0 {
		return "TRUE", nil
	}
	filter := filters[0]
	return "(" + column + "=ANY($1::bigint[]) OR ($2::boolean AND " + column + " IS NULL))", []any{filter.SnapshotIDs, filter.IncludeLegacy}
}
