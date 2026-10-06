package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/audit"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	dbURL := flag.String("db", "", "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	maxDupEndpoints := flag.Int("max-duplicate-endpoints", 0, "Fail when duplicate endpoint groups exceed this value")
	maxUnresolvedHTTP := flag.Int("max-unresolved-http", -1, "Fail when unresolved internal HTTP calls exceed this value (disabled if <0)")
	minQueueMatchRate := flag.Float64("min-queue-match-rate", -1, "Fail when queue producer->consumer match rate is below this percent (disabled if <0)")
	minDataCoverage := flag.Float64("min-cross-repo-data-coverage", -1, "Fail when cross-repo data-link coverage is below this percent (disabled if <0)")
	timeout := flag.Duration("timeout", 60*time.Second, "Audit execution timeout (e.g. 60s, 3m)")
	flag.Parse()
	if *dbURL == "" {
		*dbURL = databaseURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *dbURL == "" {
		log.Fatal("database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)")
	}

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	metrics, err := audit.Collect(ctx, pool)
	if err != nil {
		log.Fatalf("collect metrics: %v", err)
	}

	fmt.Println("Index Audit")
	fmt.Println("===========")
	fmt.Printf("duplicate_endpoint_groups=%d\n", metrics.DuplicateEndpointGroups)
	fmt.Printf("unresolved_internal_http_calls=%d\n", metrics.UnresolvedHTTPCalls)
	fmt.Printf("unresolved_http_path_unmatched=%d\n", metrics.HTTPPathUnmatched)
	fmt.Printf("unresolved_http_method_mismatched=%d\n", metrics.HTTPMethodMismatched)
	fmt.Printf("http_non_route_like=%d\n", metrics.HTTPNonRouteLike)
	fmt.Printf("http_invalid_method_token=%d\n", metrics.HTTPInvalidMethodToken)
	fmt.Printf("queue_match_rate=%.2f%% (%d/%d)\n", metrics.QueueMatchRate, metrics.QueueMatchedCount, metrics.QueueProducerCount)
	fmt.Printf("queue_producer_total=%d\n", metrics.QueueProducerTotal)
	fmt.Printf("queue_exact_match=%d\n", metrics.QueueExactMatched)
	fmt.Printf("queue_normalized_match_only=%d\n", metrics.QueueNormalizedMatched)
	fmt.Printf("queue_unmatched=%d\n", metrics.QueueUnmatched)
	fmt.Printf("queue_external_or_single_repo_no_consumer=%d\n", metrics.QueueExternalNoConsumer)
	fmt.Printf("cross_repo_data_link_coverage=%.2f%% (%d/%d)\n", metrics.DataCoverageRate, metrics.DataCrossRepoCount, metrics.DataSharedEntityCount)
	fmt.Printf("data_write_entities_total=%d\n", metrics.DataWriteEntityCount)
	fmt.Printf("data_shared_entities=%d\n", metrics.DataSharedEntityCount)
	fmt.Printf("data_no_reader=%d\n", metrics.DataNoReaderCount)
	fmt.Printf("data_same_repo_only=%d\n", metrics.DataSameRepoOnlyCount)
	fmt.Printf("data_single_repo_entities=%d\n", metrics.DataSingleRepoCount)

	var failures []string
	if metrics.DuplicateEndpointGroups > *maxDupEndpoints {
		failures = append(failures, fmt.Sprintf("duplicate endpoint groups %d > allowed %d", metrics.DuplicateEndpointGroups, *maxDupEndpoints))
	}
	if *maxUnresolvedHTTP >= 0 && metrics.UnresolvedHTTPCalls > *maxUnresolvedHTTP {
		failures = append(failures, fmt.Sprintf("unresolved internal HTTP calls %d > allowed %d", metrics.UnresolvedHTTPCalls, *maxUnresolvedHTTP))
	}
	if *minQueueMatchRate >= 0 && metrics.QueueMatchRate < *minQueueMatchRate {
		failures = append(failures, fmt.Sprintf("queue match rate %.2f%% < required %.2f%%", metrics.QueueMatchRate, *minQueueMatchRate))
	}
	if *minDataCoverage >= 0 && metrics.DataCoverageRate < *minDataCoverage {
		failures = append(failures, fmt.Sprintf("cross-repo data coverage %.2f%% < required %.2f%%", metrics.DataCoverageRate, *minDataCoverage))
	}

	if len(failures) == 0 {
		fmt.Println("status=PASS")
		return
	}

	fmt.Println("status=FAIL")
	for _, f := range failures {
		fmt.Printf("- %s\n", f)
	}
	os.Exit(2)
}
