package handlers

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func findEndpointDiscoveryCandidates(ctx context.Context, pool *pgxpool.Pool, entity, excludeRepo string, snapshotIDs []int64, includeLegacy bool, confirmed []FlowEndpoint) ([]FlowDiscoveryCandidate, bool, error) {
	var truncated bool
	var candidates []FlowDiscoveryCandidate
	if len(confirmed) == 0 {
		endpoints, clipped, err := findEndpointsByPath(ctx, pool, entity, excludeRepo, snapshotIDs, includeLegacy)
		if err != nil {
			return nil, false, err
		}
		truncated = clipped
		for _, endpoint := range endpoints {
			candidates = append(candidates, FlowDiscoveryCandidate{Endpoint: endpoint, Reason: "Route and repository name match; reader connection unconfirmed"})
		}
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT repository_name FROM repository_entities
		WHERE lower(entity_name) = lower($1)
		  AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2) OR ($3 AND snapshot_id IS NULL))
		ORDER BY repository_name`, entity, snapshotIDs, includeLegacy)
	if err != nil {
		return candidates, truncated, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return candidates, truncated, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return candidates, truncated, err
	}
	patterns := repositoryReceiverPatterns(names)
	if len(patterns) == 0 {
		patterns = repositoryReceiverPatternsForEntity(entity)
	}
	readers, err := findDataReaderCallers(ctx, pool, patterns, snapshotIDs, includeLegacy)
	if err != nil {
		return candidates, truncated, err
	}
	inferred, err := findEndpointsForReaders(ctx, pool, readers, excludeRepo, snapshotIDs, includeLegacy)
	if err != nil {
		return candidates, truncated, err
	}
	known := make(map[FlowEndpoint]bool, len(confirmed))
	for _, endpoint := range confirmed {
		known[endpoint] = true
	}
	for _, endpoint := range inferred {
		if !known[endpoint] {
			candidates = append(candidates, FlowDiscoveryCandidate{Endpoint: endpoint, Reason: "Repository receiver and read-method name match; entity access unconfirmed"})
		}
	}
	return candidates, truncated, nil
}

// Name matches are discovery results, never inputs to graph traversal.
func findEndpointsByPath(ctx context.Context, pool *pgxpool.Pool, entity, excludeRepo string, snapshotIDs []int64, includeLegacy bool) ([]FlowEndpoint, bool, error) {
	variants := entityNameVariants(strings.ToLower(strings.TrimSpace(entity)))
	if len(variants) == 0 {
		return nil, false, nil
	}
	escaped := make([]string, 0, len(variants))
	for _, variant := range variants {
		if variant == "" {
			continue
		}
		escaped = append(escaped, regexp.QuoteMeta(variant))
	}
	pattern := `(^|/)(` + strings.Join(escaped, "|") + `)($|/)`
	const limit = 50
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name, f.path, e.path, e.method,
		       e.line_number, COALESCE(fn.name, '')
		FROM endpoints e
		JOIN files f ON f.id = e.file_id
		JOIN repositories r ON r.id = f.repo_id
		LEFT JOIN functions fn ON fn.id = e.handler_function_id
		WHERE COALESCE(NULLIF(e.path_canonical, ''), e.path) ~* $1
		  AND ($2 = '' OR r.name <> $2)
		  AND EXISTS (SELECT 1 FROM unnest($3::text[]) AS v(term)
		              WHERE v.term <> '' AND strpos(lower(r.name), v.term) > 0)
		  AND ($4::bigint[] IS NULL OR f.snapshot_id = ANY($4)
		       OR ($5 AND f.snapshot_id IS NULL))
		ORDER BY r.name, f.path, e.path, e.method, e.line_number, COALESCE(fn.name, '')
		LIMIT $6`, pattern, excludeRepo, variants, snapshotIDs, includeLegacy, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var endpoints []FlowEndpoint
	for rows.Next() {
		var endpoint FlowEndpoint
		if err := rows.Scan(&endpoint.Repo, &endpoint.File, &endpoint.Path, &endpoint.Method, &endpoint.Line, &endpoint.Handler); err != nil {
			return nil, false, err
		}
		endpoints = append(endpoints, endpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(endpoints) > limit {
		return endpoints[:limit], true, nil
	}
	return endpoints, false, nil
}
