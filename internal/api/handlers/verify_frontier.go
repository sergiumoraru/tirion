package handlers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sergiumoraru/tirion/internal/trace"
)

var errVerifyFrontierClipped = errors.New("verification predecessor lookup exceeded the graph budget")

func (h *Handlers) symbolBoundaryFrontier(ctx context.Context, callerID string, depth, maxNodes int, scope searchWorkspaceScope) ([]verifyFrontierNode, []verifyFrontierNode, bool, error) {
	// Direct predecessors must not depend on recursive traversal order.
	predecessors, err := h.verifySymbolPredecessors(ctx, callerID, maxNodes, scope)
	if err != nil {
		return nil, nil, errors.Is(err, errVerifyFrontierClipped), err
	}
	frontier, clipped, errs := h.predecessorFrontier(ctx, predecessors, depth, maxNodes, scope, callerID)
	if len(errs) > 0 {
		return predecessors, frontier, clipped, fmt.Errorf("frontier lookup: %s", errs[0])
	}
	return predecessors, frontier, clipped, nil
}

func (h *Handlers) predecessorFrontier(ctx context.Context, predecessors []verifyFrontierNode, depth, maxNodes int, scope searchWorkspaceScope, boundaries ...string) ([]verifyFrontierNode, bool, []string) {
	type pending struct {
		node  verifyFrontierNode
		depth int
	}
	var queue []pending
	var frontier []verifyFrontierNode
	seen := map[string]bool{}
	for _, boundary := range boundaries {
		seen[boundary] = true
	}
	add := func(node verifyFrontierNode, level int) bool {
		if seen[node.CallerID] {
			return true
		}
		if len(frontier) >= maxNodes {
			return false
		}
		seen[node.CallerID] = true
		frontier = append(frontier, node)
		queue = append(queue, pending{node, level})
		return true
	}
	for _, node := range predecessors {
		if !add(node, 1) {
			return frontier, true, nil
		}
	}
	for index := 0; index < len(queue); index++ {
		current := queue[index]
		if current.node.Kind == "schedule" {
			continue
		}
		nodes, err := h.verifySymbolPredecessors(ctx, current.node.CallerID, maxNodes, scope)
		if err != nil {
			return frontier, errors.Is(err, errVerifyFrontierClipped), []string{err.Error()}
		}
		for _, node := range nodes {
			if seen[node.CallerID] {
				continue
			}
			if current.depth >= depth || !add(node, current.depth+1) {
				return frontier, true, nil
			}
		}
	}
	sortVerifyFrontierNodes(frontier)
	return frontier, false, nil
}

func (h *Handlers) verifySymbolPredecessors(ctx context.Context, callerID string, limit int, scope searchWorkspaceScope) ([]verifyFrontierNode, error) {
	repo, file, symbol := trace.ParseCallerID(callerID)
	// A resolved identity alone does not prove invocation. Speculative callback
	// arguments are discovery; supported API callback relationships remain eligible.
	rows, err := h.storage.Pool().Query(ctx, `
		WITH target AS (
			SELECT fn.*, fi.snapshot_id, fi.path, r.name AS repo
			FROM functions fn JOIN files fi ON fi.id = fn.file_id
			JOIN repositories r ON r.id = fi.repo_id
			WHERE r.name = $4 AND fi.path = $5 AND fn.name = $6
			  AND ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2))
		), edges AS (
			SELECT r.name || ':' || fi.path || ':' || fn.name AS caller_id,
			       'call' AS kind, COALESCE(fc.line_number, fn.start_line) AS line
			FROM target t JOIN function_calls fc ON fc.callee_function_id = t.id
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files fi ON fi.id = fn.file_id JOIN repositories r ON r.id = fi.repo_id
			WHERE ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2))
			  AND NOT fc.is_callback_argument
			UNION ALL
			SELECT t.repo || ':' || t.path || ':@schedule.' || sm.method_name, 'schedule', COALESCE(sm.line_number, t.start_line)
			FROM target t JOIN scheduled_methods sm ON sm.method_id = t.id
			UNION ALL
			SELECT r.name || ':' || fi.path || ':' || fn.name, 'di_impl', fn.start_line
			FROM target t
			JOIN classes c ON c.file_id = t.file_id AND t.start_line >= c.start_line AND t.end_line <= c.end_line
			JOIN implementations impl ON impl.class_id = c.id
			JOIN interfaces iface ON iface.id = impl.interface_id
			JOIN functions fn ON fn.file_id = iface.file_id AND fn.start_line >= iface.start_line AND fn.end_line <= iface.end_line
			JOIN files fi ON fi.id = fn.file_id JOIN repositories r ON r.id = fi.repo_id
			WHERE COALESCE(NULLIF(fn.simple_name, ''), regexp_replace(fn.name, '^.*[.]', '')) = COALESCE(NULLIF(t.simple_name, ''), regexp_replace(t.name, '^.*[.]', ''))
			  AND ($2::bigint[] IS NULL OR fi.snapshot_id = ANY($2))
			UNION ALL
			SELECT p.caller_id, 'queue_producer', COALESCE(p.line_number, 0)
			FROM sqs_consumers c JOIN sqs_producers p
			  ON lower(trim(both '%' FROM p.queue_name)) = lower(trim(both '%' FROM c.queue_name))
			WHERE (c.consumer_id = $1 OR c.consumer_id || '.' || c.handler_method = $1
			       OR split_part(c.consumer_id, ':', 1) || ':' || split_part(c.consumer_id, ':', 2) || ':' || c.handler_method = $1)
			  AND ($2::bigint[] IS NULL OR (c.snapshot_id = ANY($2) AND p.snapshot_id = ANY($2)))
			UNION ALL
			SELECT '@eventbridge:' || COALESCE(NULLIF(s.source, ''), 'eventbridge') || ':' || s.rule_name,
			       'schedule', 0
			FROM sqs_consumers c JOIN eventbridge_schedules s
			  ON lower(s.target_type) = 'sqs' AND lower(s.target_name) = lower(c.queue_name)
			WHERE (c.consumer_id = $1 OR c.consumer_id || '.' || c.handler_method = $1
			       OR split_part(c.consumer_id, ':', 1) || ':' || split_part(c.consumer_id, ':', 2) || ':' || c.handler_method = $1)
			  AND ($2::bigint[] IS NULL OR c.snapshot_id = ANY($2))
		)
		SELECT caller_id, kind, MIN(line) FROM edges
		WHERE caller_id <> $1
		GROUP BY caller_id, kind ORDER BY caller_id, kind LIMIT $3`, callerID, activeSnapshotIDs(scope), limit+1, repo, file, symbol)
	if err != nil {
		return nil, fmt.Errorf("predecessors of %s: %w", callerID, err)
	}
	var nodes []verifyFrontierNode
	for rows.Next() {
		var id, kind string
		var line int
		if err := rows.Scan(&id, &kind, &line); err != nil {
			rows.Close()
			return nil, err
		}
		nodes = append(nodes, frontierNodeFromCallerID(id, kind, line))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(nodes) > limit {
		return nil, errVerifyFrontierClipped
	}
	azure, err := h.verifyAzurePredecessors(ctx, callerID, limit, scope)
	if err != nil {
		return nil, err
	}
	nodes = uniqueVerifyFrontierNodes(append(nodes, azure...))
	if len(nodes) > limit {
		return nil, errVerifyFrontierClipped
	}

	endpoints, err := h.verifySymbolEndpoints(ctx, callerID, limit, scope)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range endpoints {
		callers, err := h.fetchVerifyEndpointCallers(ctx, endpoint, scope, false, limit)
		if err != nil {
			return nil, err
		}
		for _, caller := range callers {
			nodes = append(nodes, frontierNodeFromCallerID(caller.CallerID, "http_client", caller.Line))
		}
		nodes = uniqueVerifyFrontierNodes(nodes)
		if len(nodes) > limit {
			return nil, errVerifyFrontierClipped
		}
	}
	return uniqueVerifyFrontierNodes(nodes), nil
}

func (h *Handlers) verifySymbolEndpoints(ctx context.Context, callerID string, limit int, scope searchWorkspaceScope) ([]ImpactEndpoint, error) {
	repo, file, symbol := trace.ParseCallerID(callerID)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT e.method, e.path, fn.name, r.name, fi.path, COALESCE(e.line_number, fn.start_line)
		FROM endpoints e JOIN functions fn ON fn.id = e.handler_function_id
		JOIN files fi ON fi.id = fn.file_id JOIN repositories r ON r.id = fi.repo_id
		WHERE r.name = $1 AND fi.path = $2 AND fn.name = $3
		  AND ($4::bigint[] IS NULL OR fi.snapshot_id = ANY($4))
		ORDER BY e.method, e.path LIMIT $5`, repo, file, symbol, activeSnapshotIDs(scope), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var endpoints []ImpactEndpoint
	for rows.Next() {
		var endpoint ImpactEndpoint
		if err := rows.Scan(&endpoint.Method, &endpoint.Path, &endpoint.Handler, &endpoint.Repo, &endpoint.File, &endpoint.Line); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(endpoints) > limit {
		return nil, errVerifyFrontierClipped
	}
	return endpoints, nil
}

func (h *Handlers) verifyAzurePredecessors(ctx context.Context, callerID string, limit int, scope searchWorkspaceScope) ([]verifyFrontierNode, error) {
	repo, file, symbol := trace.ParseCallerID(callerID)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT fi.path, COALESCE(t.script_file, ''), t.function_name, t.trigger_type,
		       COALESCE(t.resource_name, ''), COALESCE(t.line_number, 0)
		FROM azure_function_triggers t JOIN files fi ON fi.id = t.file_id
		JOIN repositories r ON r.id = t.repo_id
		WHERE r.name = $1 AND (t.function_name = $2 OR t.trigger_type = $2)
		  AND ($3::bigint[] IS NULL OR fi.snapshot_id = ANY($3))
		ORDER BY fi.path, t.id LIMIT $4`, repo, symbol, activeSnapshotIDs(scope), limit+1)
	if err != nil {
		return nil, err
	}
	type trigger struct {
		file, script, name, kind, resource string
		line                               int
	}
	var triggers []trigger
	for rows.Next() {
		var t trigger
		if err := rows.Scan(&t.file, &t.script, &t.name, &t.kind, &t.resource, &t.line); err != nil {
			rows.Close()
			return nil, err
		}
		triggers = append(triggers, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(triggers) > limit {
		return nil, errVerifyFrontierClipped
	}
	var nodes []verifyFrontierNode
	for _, t := range triggers {
		declaredSource := t.file
		if t.script != "" {
			declaredSource = filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(t.file), t.script)))
		}
		if declaredSource != file {
			continue
		}
		switch strings.ToLower(t.kind) {
		case "timertrigger":
			nodes = append(nodes, verifyFrontierNode{CallerID: trace.BuildCallerID(repo, t.file, "@schedule."+t.name), Kind: "schedule", Repo: repo, File: t.file, Symbol: t.name, Line: t.line})
		case "queuetrigger", "servicebustrigger":
			if t.resource == "" {
				continue
			}
			producers, err := h.resolveVerifyQueuePredecessors(ctx, VerifyResolvedAnchor{Ref: t.resource}, scope, limit)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, producers...)
		}
	}
	return uniqueVerifyFrontierNodes(nodes), nil
}
