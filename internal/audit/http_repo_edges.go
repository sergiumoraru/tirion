package audit

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
)

type HTTPRepoEdge struct {
	SourceRepoID int
	TargetRepoID int
	Count        int
}

type RepoDependencyEdge struct {
	SourceRepoID int
	TargetRepoID int
	EdgeType     string
	QueueName    string
	Count        int
}

type httpRepoCall struct {
	RepoID     int
	RepoName   string
	ClientType string
	Method     string
	Path       string
}

type httpRepoEndpoint struct {
	RepoID int
	Method string
	Path   string
}

func CollectHTTPRepoEdges(ctx context.Context, pool *pgxpool.Pool) ([]HTTPRepoEdge, error) {
	calls, err := loadHTTPRepoCalls(ctx, pool)
	if err != nil {
		return nil, err
	}
	endpoints, err := loadHTTPRepoEndpoints(ctx, pool)
	if err != nil {
		return nil, err
	}

	counts := make(map[[2]int]int)
	for _, call := range calls {
		path := normalizeAuditHTTPPath(call.Path)
		method := strings.ToUpper(strings.TrimSpace(call.Method))
		if !auditHTTPRouteLike(method, call.ClientType, path) {
			continue
		}
		prefixes := config.GetEffectiveTraceConfig().HTTPForRepo(call.RepoName).ContextPathPrefixes
		targetRepos := matchHTTPRepoEndpoints(method, path, endpoints, prefixes...)
		for targetRepoID := range targetRepos {
			if targetRepoID == call.RepoID {
				continue
			}
			counts[[2]int{call.RepoID, targetRepoID}]++
		}
	}

	edges := make([]HTTPRepoEdge, 0, len(counts))
	for key, count := range counts {
		edges = append(edges, HTTPRepoEdge{
			SourceRepoID: key[0],
			TargetRepoID: key[1],
			Count:        count,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Count == edges[j].Count {
			if edges[i].SourceRepoID == edges[j].SourceRepoID {
				return edges[i].TargetRepoID < edges[j].TargetRepoID
			}
			return edges[i].SourceRepoID < edges[j].SourceRepoID
		}
		return edges[i].Count > edges[j].Count
	})
	return edges, nil
}

func CollectRepoDependencyEdges(ctx context.Context, pool *pgxpool.Pool) ([]RepoDependencyEdge, error) {
	httpEdges, err := CollectHTTPRepoEdges(ctx, pool)
	if err != nil {
		return nil, err
	}
	sqsEdges, err := loadSQSRepoEdges(ctx, pool)
	if err != nil {
		return nil, err
	}

	edges := make([]RepoDependencyEdge, 0, len(httpEdges)+len(sqsEdges))
	for _, edge := range httpEdges {
		edges = append(edges, RepoDependencyEdge{
			SourceRepoID: edge.SourceRepoID,
			TargetRepoID: edge.TargetRepoID,
			EdgeType:     "HTTP_CALLS",
			Count:        edge.Count,
		})
	}
	edges = append(edges, sqsEdges...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].EdgeType == edges[j].EdgeType {
			if edges[i].Count == edges[j].Count {
				if edges[i].SourceRepoID == edges[j].SourceRepoID {
					if edges[i].TargetRepoID == edges[j].TargetRepoID {
						return edges[i].QueueName < edges[j].QueueName
					}
					return edges[i].TargetRepoID < edges[j].TargetRepoID
				}
				return edges[i].SourceRepoID < edges[j].SourceRepoID
			}
			return edges[i].Count > edges[j].Count
		}
		return edges[i].EdgeType < edges[j].EdgeType
	})
	return edges, nil
}

func RefreshRepoDependencySnapshot(ctx context.Context, pool *pgxpool.Pool) error {
	edges, err := CollectRepoDependencyEdges(ctx, pool)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, `DELETE FROM repo_dependency_edges`); err != nil {
		return err
	}

	computedAt := time.Now().UTC()
	for _, edge := range edges {
		if _, err := tx.Exec(ctx, `
			INSERT INTO repo_dependency_edges (
				source_repo_id,
				target_repo_id,
				edge_type,
				queue_name,
				count,
				computed_at
			) VALUES ($1, $2, $3, $4, $5, $6)
		`, edge.SourceRepoID, edge.TargetRepoID, edge.EdgeType, edge.QueueName, edge.Count, computedAt); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func loadHTTPRepoCalls(ctx context.Context, pool *pgxpool.Pool) ([]httpRepoCall, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			h.repo_id,
			r.name,
			COALESCE(h.client_type, ''),
			UPPER(h.http_method),
			COALESCE(h.url_pattern, '')
		FROM http_client_calls h
		JOIN repositories r ON r.id = h.repo_id
		WHERE h.url_pattern LIKE '/%%'
		  AND split_part(h.caller_id, ':', 2) NOT LIKE '.codebase-snapshots/%%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	calls := make([]httpRepoCall, 0)
	for rows.Next() {
		var call httpRepoCall
		if err := rows.Scan(&call.RepoID, &call.RepoName, &call.ClientType, &call.Method, &call.Path); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return calls, nil
}

func loadSQSRepoEdges(ctx context.Context, pool *pgxpool.Pool) ([]RepoDependencyEdge, error) {
	rows, err := pool.Query(ctx, `
		SELECT
		  p.repo_id as producer_repo_id,
		  c.repo_id as consumer_repo_id,
		  p.queue_name,
		  COUNT(*) as connection_count
		FROM sqs_producers p
		JOIN sqs_consumers c ON p.queue_name = c.queue_name
		WHERE p.repo_id <> c.repo_id
		GROUP BY p.repo_id, c.repo_id, p.queue_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	edges := make([]RepoDependencyEdge, 0)
	for rows.Next() {
		var edge RepoDependencyEdge
		edge.EdgeType = "SQS"
		if err := rows.Scan(&edge.SourceRepoID, &edge.TargetRepoID, &edge.QueueName, &edge.Count); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edges, nil
}

func loadHTTPRepoEndpoints(ctx context.Context, pool *pgxpool.Pool) ([]httpRepoEndpoint, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT
			e.repo_id,
			COALESCE(NULLIF(e.method_canonical, ''), UPPER(e.method)) AS http_method,
			COALESCE(NULLIF(e.path_canonical, ''), LOWER(e.path)) AS path
		FROM endpoints e
		JOIN files f ON f.id = e.file_id
		WHERE f.path NOT LIKE '.codebase-snapshots/%%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	endpoints := make([]httpRepoEndpoint, 0)
	for rows.Next() {
		var ep httpRepoEndpoint
		if err := rows.Scan(&ep.RepoID, &ep.Method, &ep.Path); err != nil {
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

func matchHTTPRepoEndpoints(callMethod, callPath string, endpoints []httpRepoEndpoint, prefixes ...string) map[int]struct{} {
	targetRepos := make(map[int]struct{})
	for _, ep := range endpoints {
		if !auditHTTPPathMatches(callMethod, callPath, ep.Method, ep.Path, prefixes...) {
			continue
		}
		if ep.Method == callMethod || ep.Method == "REQUEST" || callMethod == "REQUEST" || callMethod == "ANY" {
			targetRepos[ep.RepoID] = struct{}{}
		}
	}
	return targetRepos
}
