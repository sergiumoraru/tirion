package audit

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/graph"
)

type Metrics struct {
	DuplicateEndpointGroups int     `json:"duplicateEndpointGroups"`
	UnresolvedHTTPCalls     int     `json:"unresolvedHTTPCalls"`
	HTTPPathUnmatched       int     `json:"httpPathUnmatched"`
	HTTPMethodMismatched    int     `json:"httpMethodMismatched"`
	HTTPNonRouteLike        int     `json:"httpNonRouteLike"`
	HTTPInvalidMethodToken  int     `json:"httpInvalidMethodToken"`
	QueueProducerTotal      int     `json:"queueProducerTotal"`
	QueueProducerCount      int     `json:"queueProducerCount"`
	QueueMatchedCount       int     `json:"queueMatchedCount"`
	QueueMatchRate          float64 `json:"queueMatchRate"`
	QueueExactMatched       int     `json:"queueExactMatched"`
	QueueNormalizedMatched  int     `json:"queueNormalizedMatched"`
	QueueUnmatched          int     `json:"queueUnmatched"`
	QueueExternalNoConsumer int     `json:"queueExternalNoConsumer"`
	DataWriteEntityCount    int     `json:"dataWriteEntityCount"`
	DataSharedEntityCount   int     `json:"dataSharedEntityCount"`
	DataCrossRepoCount      int     `json:"dataCrossRepoCount"`
	DataCoverageRate        float64 `json:"dataCoverageRate"`
	DataNoReaderCount       int     `json:"dataNoReaderCount"`
	DataSameRepoOnlyCount   int     `json:"dataSameRepoOnlyCount"`
	DataSingleRepoCount     int     `json:"dataSingleRepoCount"`
}

func Collect(ctx context.Context, pool *pgxpool.Pool) (Metrics, error) {
	m, err := collectMetricsWithoutHTTPClassifications(ctx, pool)
	if err != nil {
		return Metrics{}, err
	}

	classifications, err := collectHTTPClassifications(ctx, pool)
	if err != nil {
		return Metrics{}, err
	}
	applyHTTPClassificationsToMetrics(&m, classifications)
	Finalize(&m)
	return m, nil
}

func collectMetricsWithoutHTTPClassifications(ctx context.Context, pool *pgxpool.Pool, filters ...graph.SnapshotFilter) (Metrics, error) {
	var m Metrics
	endpointScope, args := auditSnapshotPredicate("f.snapshot_id", filters)
	factScope, _ := auditSnapshotPredicate("snapshot_id", filters)

	if err := pool.QueryRow(ctx, strings.ReplaceAll(`
		SELECT COUNT(*)
		FROM (
			SELECT
				e.repo_id,
				COALESCE(NULLIF(e.path_canonical, ''), lower(e.path)) AS path_key,
				COALESCE(NULLIF(e.method_canonical, ''), upper(e.method)) AS method_key,
				COALESCE(e.handler_function_id, 0) AS handler_id,
				COALESCE(e.file_id, 0) AS file_id,
				COALESCE(e.line_number, -1) AS line_number,
				COUNT(*) AS cnt
			FROM endpoints e
			JOIN files f ON f.id = e.file_id
			WHERE f.path NOT LIKE '.codebase-snapshots/%' AND /* scope */
			GROUP BY e.repo_id, path_key, method_key, handler_id, file_id, line_number
			HAVING COUNT(*) > 1
		) d
	`, "/* scope */", endpointScope), args...).Scan(&m.DuplicateEndpointGroups); err != nil {
		return m, err
	}

	if err := pool.QueryRow(ctx, strings.ReplaceAll(`
		WITH producers AS (
			SELECT
				queue_name,
				COUNT(DISTINCT repo_id) AS producer_repo_count,
				lower(queue_name) AS queue_exact,
				lower(regexp_replace(
					regexp_replace(
						regexp_replace(
							regexp_replace(queue_name, '^arn:[^:]+:sqs:[^:]+:[^:]+:', ''),
							'^https?://[^/]+/[^/]+/', ''
						),
						'^.*/', ''
					),
					'\\.fifo$', ''
				)) AS queue_norm
			FROM sqs_producers
			WHERE queue_name IS NOT NULL AND queue_name <> '' AND /* scope */
			GROUP BY queue_name
		),
		consumers AS (
			SELECT DISTINCT
				queue_name,
				lower(queue_name) AS queue_exact,
				lower(regexp_replace(
					regexp_replace(
						regexp_replace(
							regexp_replace(queue_name, '^arn:[^:]+:sqs:[^:]+:[^:]+:', ''),
							'^https?://[^/]+/[^/]+/', ''
						),
						'^.*/', ''
					),
					'\\.fifo$', ''
				)) AS queue_norm
			FROM sqs_consumers
			WHERE queue_name IS NOT NULL AND queue_name <> '' AND /* scope */
		),
		reasons AS (
			SELECT
				p.queue_name,
				p.producer_repo_count,
				CASE
					WHEN EXISTS (SELECT 1 FROM consumers c WHERE c.queue_exact = p.queue_exact) THEN 'exact_match'
					WHEN EXISTS (SELECT 1 FROM consumers c WHERE c.queue_norm = p.queue_norm) THEN 'normalized_match_only'
					WHEN p.producer_repo_count > 1 THEN 'no_consumer_match_internal'
					ELSE 'no_consumer_match_single_repo'
				END AS reason
			FROM producers p
		)
		SELECT
			(SELECT COUNT(*) FROM producers) AS producer_total,
			COUNT(*) FILTER (
				WHERE reason IN ('exact_match', 'normalized_match_only', 'no_consumer_match_internal')
			) AS producer_count,
			COUNT(*) FILTER (WHERE reason = 'exact_match') AS exact_match,
			COUNT(*) FILTER (WHERE reason = 'normalized_match_only') AS normalized_only_match,
			COUNT(*) FILTER (WHERE reason = 'no_consumer_match_internal') AS no_consumer_match,
			COUNT(*) FILTER (WHERE reason = 'no_consumer_match_single_repo') AS no_consumer_match_single_repo
		FROM reasons
	`, "/* scope */", factScope), args...).Scan(&m.QueueProducerTotal, &m.QueueProducerCount, &m.QueueExactMatched, &m.QueueNormalizedMatched, &m.QueueUnmatched, &m.QueueExternalNoConsumer); err != nil {
		return m, err
	}

	if err := pool.QueryRow(ctx, strings.ReplaceAll(`
		WITH selected_data_accesses AS (SELECT * FROM data_accesses WHERE /* scope */), writes AS (
			SELECT DISTINCT lower(entity_name) AS entity_name, repo_id
			FROM selected_data_accesses
			WHERE lower(access) = 'write'
			  AND entity_name <> ''
		),
		write_entities AS (
			SELECT DISTINCT entity_name
			FROM writes
		),
		entity_resolution AS (
			SELECT
				e.entity_name,
				(SELECT COUNT(DISTINCT w.repo_id) FROM writes w WHERE w.entity_name = e.entity_name) AS write_repo_count,
				(
					SELECT COUNT(DISTINCT r.repo_id)
					FROM selected_data_accesses r
					WHERE lower(r.access) = 'read'
					  AND lower(r.entity_name) = e.entity_name
				) AS read_repo_count,
				EXISTS (
					SELECT 1
					FROM selected_data_accesses r
					WHERE lower(r.access) = 'read'
					  AND lower(r.entity_name) = e.entity_name
				) AS has_any_reader,
				EXISTS (
					SELECT 1
					FROM writes w
					JOIN selected_data_accesses r ON lower(r.entity_name) = w.entity_name
					WHERE lower(r.access) = 'read'
					  AND r.repo_id <> w.repo_id
					  AND w.entity_name = e.entity_name
				) AS has_cross_repo_reader
			FROM write_entities e
		)
		SELECT
			COUNT(*) AS write_entities,
			COUNT(*) FILTER (WHERE write_repo_count > 1 OR read_repo_count > 1) AS shared_entities,
			COUNT(*) FILTER (
				WHERE (write_repo_count > 1 OR read_repo_count > 1) AND has_cross_repo_reader
			) AS cross_entities,
			COUNT(*) FILTER (WHERE NOT has_any_reader) AS no_reader,
			COUNT(*) FILTER (WHERE has_any_reader AND NOT has_cross_repo_reader) AS same_repo_only,
			COUNT(*) FILTER (WHERE NOT (write_repo_count > 1 OR read_repo_count > 1)) AS single_repo_only
		FROM entity_resolution
	`, "/* scope */", factScope), args...).Scan(&m.DataWriteEntityCount, &m.DataSharedEntityCount, &m.DataCrossRepoCount, &m.DataNoReaderCount, &m.DataSameRepoOnlyCount, &m.DataSingleRepoCount); err != nil {
		return m, err
	}
	return m, nil
}

func applyHTTPClassificationsToMetrics(m *Metrics, classifications []auditHTTPClassification) {
	if m == nil {
		return
	}
	for _, classification := range classifications {
		switch {
		case !classification.routeLike:
			m.HTTPNonRouteLike++
		case !classification.hasPathMatch:
			m.HTTPPathUnmatched++
		case classification.methodIsHTTP && !classification.hasMethodMatch:
			m.HTTPMethodMismatched++
		case !classification.methodIsHTTP:
			m.HTTPInvalidMethodToken++
		}
	}
}

func BuildUnresolvedHTTPDrilldown(classifications []auditHTTPClassification, repoLimit, sampleLimit int) UnresolvedHTTPDrilldown {
	drilldown := UnresolvedHTTPDrilldown{
		TopRepos: make([]UnresolvedHTTPRepoSummary, 0),
		Samples:  make([]UnresolvedHTTPSample, 0),
	}

	repoStats := make(map[string]*UnresolvedHTTPRepoSummary)
	samples := make([]UnresolvedHTTPSample, 0, sampleLimit)
	for _, classification := range classifications {
		if !classification.routeLike {
			continue
		}
		reason := ""
		switch {
		case !classification.hasPathMatch:
			reason = "path_unmatched"
		case classification.methodIsHTTP && !classification.hasMethodMatch:
			reason = "method_mismatched"
		default:
			continue
		}

		repoSummary, ok := repoStats[classification.call.RepoName]
		if !ok {
			repoSummary = &UnresolvedHTTPRepoSummary{Repo: classification.call.RepoName}
			repoStats[classification.call.RepoName] = repoSummary
		}
		repoSummary.Count++
		if reason == "path_unmatched" {
			repoSummary.PathUnmatched++
		} else {
			repoSummary.MethodMismatch++
		}

		samples = append(samples, UnresolvedHTTPSample{
			Repo:             classification.call.RepoName,
			CallerID:         classification.call.CallerID,
			Method:           classification.call.Method,
			Path:             classification.call.Path,
			LineNumber:       classification.call.LineNumber,
			ClientType:       classification.call.ClientType,
			Reason:           reason,
			AvailableMethods: classification.availableMethods,
		})
	}

	drilldown.TopRepos = make([]UnresolvedHTTPRepoSummary, 0, len(repoStats))
	for _, repoSummary := range repoStats {
		drilldown.TopRepos = append(drilldown.TopRepos, *repoSummary)
	}
	sort.Slice(drilldown.TopRepos, func(i, j int) bool {
		if drilldown.TopRepos[i].Count == drilldown.TopRepos[j].Count {
			return drilldown.TopRepos[i].Repo < drilldown.TopRepos[j].Repo
		}
		return drilldown.TopRepos[i].Count > drilldown.TopRepos[j].Count
	})
	if repoLimit > 0 && len(drilldown.TopRepos) > repoLimit {
		drilldown.TopRepos = drilldown.TopRepos[:repoLimit]
	}

	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Reason != samples[j].Reason {
			return samples[i].Reason == "method_mismatched"
		}
		if samples[i].Repo != samples[j].Repo {
			return samples[i].Repo < samples[j].Repo
		}
		if samples[i].Path != samples[j].Path {
			return samples[i].Path < samples[j].Path
		}
		return samples[i].CallerID < samples[j].CallerID
	})
	if sampleLimit > 0 && len(samples) > sampleLimit {
		samples = samples[:sampleLimit]
	}
	drilldown.Samples = samples

	return drilldown
}

func CollectHTTPAudit(ctx context.Context, pool *pgxpool.Pool, repoLimit, sampleLimit int, filters ...graph.SnapshotFilter) (Metrics, UnresolvedHTTPDrilldown, error) {
	metrics, err := collectMetricsWithoutHTTPClassifications(ctx, pool, filters...)
	if err != nil {
		return Metrics{}, UnresolvedHTTPDrilldown{}, err
	}

	classifications, err := collectHTTPClassifications(ctx, pool, filters...)
	if err != nil {
		return Metrics{}, UnresolvedHTTPDrilldown{}, err
	}

	applyHTTPClassificationsToMetrics(&metrics, classifications)
	Finalize(&metrics)

	return metrics, BuildUnresolvedHTTPDrilldown(classifications, repoLimit, sampleLimit), nil
}

func Finalize(m *Metrics) {
	if m == nil {
		return
	}
	m.UnresolvedHTTPCalls = m.HTTPPathUnmatched + m.HTTPMethodMismatched
	m.QueueMatchedCount = m.QueueExactMatched + m.QueueNormalizedMatched
	if m.QueueProducerCount == 0 {
		m.QueueMatchRate = 100.0
	} else {
		m.QueueMatchRate = float64(m.QueueMatchedCount) * 100.0 / float64(m.QueueProducerCount)
	}
	if m.DataSharedEntityCount > 0 {
		m.DataCoverageRate = float64(m.DataCrossRepoCount) * 100.0 / float64(m.DataSharedEntityCount)
	} else if m.DataWriteEntityCount == 0 {
		m.DataCoverageRate = 100.0
	} else {
		m.DataCoverageRate = 0.0
	}
}
