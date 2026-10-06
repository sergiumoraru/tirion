package handlers

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FetchArchitectureTags returns architecture tags for a set of caller IDs.
// Tags are derived from existing DB tables: endpoints, sqs_consumers, sqs_producers,
// http_client_calls, and eventbridge_schedules.
func FetchArchitectureTags(pool *pgxpool.Pool, callerIDs []string) map[string][]string {
	return FetchArchitectureTagsForSnapshots(pool, callerIDs, nil)
}

func FetchArchitectureTagsForSnapshots(pool *pgxpool.Pool, callerIDs []string, snapshotIDs []int64) map[string][]string {
	if len(callerIDs) == 0 {
		return nil
	}
	tags := make(map[string][]string)
	ctx := context.Background()

	// 1. api_endpoint — functions that are endpoint handlers
	tagEndpointHandlers(ctx, pool, callerIDs, snapshotIDs, tags)

	// 2. sqs_consumer — consumer_id is class-level (repo:file:Class), caller_ids are
	//    method-level (repo:file:Class.method), so we need prefix matching
	tagSqsConsumers(ctx, pool, callerIDs, snapshotIDs, tags)

	// 3. sqs_producer
	tagFromTable(ctx, pool, callerIDs, tags, "sqs_producer",
		`SELECT DISTINCT caller_id FROM sqs_producers WHERE caller_id = ANY($1) AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2))`, snapshotIDs)

	// 4. http_client
	tagFromTable(ctx, pool, callerIDs, tags, "http_client",
		`SELECT DISTINCT caller_id FROM http_client_calls WHERE caller_id = ANY($1) AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2))`, snapshotIDs)

	// 5. eventbridge_target — SQS consumers whose queue is an EventBridge target
	tagEventBridgeTargets(ctx, pool, callerIDs, snapshotIDs, tags)

	return tags
}

func tagEndpointHandlers(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, snapshotIDs []int64, tags map[string][]string) {
	// Build caller_ids for endpoint handler functions and check if they match
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name || ':' || fi.path || ':' || fn.name AS caller_id
		FROM endpoints e
		JOIN functions fn ON e.handler_function_id = fn.id
		JOIN files fi ON fn.file_id = fi.id
		JOIN repositories r ON fi.repo_id = r.id
		WHERE (r.name || ':' || fi.path || ':' || fn.name) = ANY($1)
		  AND ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2))
	`, callerIDs, snapshotIDs)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err == nil {
			tags[cid] = appendTag(tags[cid], "api_endpoint")
		}
	}
}

func tagFromTable(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, tags map[string][]string, tag, query string, snapshotIDs []int64) {
	rows, err := pool.Query(ctx, query, callerIDs, snapshotIDs)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err == nil {
			tags[cid] = appendTag(tags[cid], tag)
		}
	}
}

func tagSqsConsumers(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, snapshotIDs []int64, tags map[string][]string) {
	// sqs_consumers.consumer_id is class-level: "repo:file:ClassName"
	// Our callerIDs are method-level: "repo:file:ClassName.methodName"
	// Extract class-level prefixes to match against consumer_id
	classPrefixes := extractClassPrefixes(callerIDs)
	if len(classPrefixes) == 0 {
		return
	}

	rows, err := pool.Query(ctx,
		`SELECT consumer_id FROM sqs_consumers WHERE consumer_id = ANY($1) AND ($2::bigint[] IS NULL OR snapshot_id = ANY($2))`, classPrefixes, snapshotIDs)
	if err != nil {
		return
	}
	defer rows.Close()

	matchedPrefixes := make(map[string]bool)
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err == nil {
			matchedPrefixes[cid] = true
		}
	}

	// Map back to the original method-level callerIDs
	for _, cid := range callerIDs {
		prefix := classPrefix(cid)
		if matchedPrefixes[prefix] {
			tags[cid] = appendTag(tags[cid], "sqs_consumer")
		}
	}
}

func tagEventBridgeTargets(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, snapshotIDs []int64, tags map[string][]string) {
	// Same prefix matching needed as sqs_consumer
	classPrefixes := extractClassPrefixes(callerIDs)
	if len(classPrefixes) == 0 {
		return
	}

	rows, err := pool.Query(ctx, `
		SELECT DISTINCT sc.consumer_id
		FROM sqs_consumers sc
		JOIN eventbridge_schedules eb ON
			eb.target_type = 'sqs'
			AND lower(eb.target_name) = lower(sc.queue_name)
		WHERE sc.consumer_id = ANY($1)
		  AND ($2::bigint[] IS NULL OR sc.snapshot_id = ANY($2))
	`, classPrefixes, snapshotIDs)
	if err != nil {
		return
	}
	defer rows.Close()

	matchedPrefixes := make(map[string]bool)
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err == nil {
			matchedPrefixes[cid] = true
		}
	}

	for _, cid := range callerIDs {
		prefix := classPrefix(cid)
		if matchedPrefixes[prefix] {
			tags[cid] = appendTag(tags[cid], "eventbridge_target")
		}
	}
}

// classPrefix extracts "repo:file:ClassName" from "repo:file:ClassName.methodName"
func classPrefix(callerID string) string {
	// Find the last colon to get the function part
	lastColon := strings.LastIndex(callerID, ":")
	if lastColon < 0 {
		return callerID
	}
	funcPart := callerID[lastColon+1:]
	// Strip method name: "ClassName.method" → "ClassName"
	if dot := strings.Index(funcPart, "."); dot > 0 {
		return callerID[:lastColon+1] + funcPart[:dot]
	}
	return callerID
}

// extractClassPrefixes returns unique class-level prefixes from method-level callerIDs
func extractClassPrefixes(callerIDs []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, cid := range callerIDs {
		prefix := classPrefix(cid)
		if prefix != "" && !seen[prefix] {
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out
}

func appendTag(existing []string, tag string) []string {
	for _, t := range existing {
		if t == tag {
			return existing
		}
	}
	return append(existing, tag)
}
