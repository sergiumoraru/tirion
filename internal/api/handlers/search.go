package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/graph"
)

type FunctionResult struct {
	ID           int64                 `json:"id"`
	Name         string                `json:"name"`
	File         string                `json:"file"`
	Repo         string                `json:"repo"`
	StartLine    int                   `json:"startLine"`
	EndLine      int                   `json:"endLine"`
	Source       string                `json:"source,omitempty"`
	Score        float64               `json:"score,omitempty"`
	Richness     int                   `json:"richness"`
	CallerID     string                `json:"callerId,omitempty"`
	Tags         []string              `json:"tags,omitempty"`
	Integrations []FunctionIntegration `json:"integrations,omitempty"`
	SnapshotID   *int64                `json:"snapshotId,omitempty"`
}

type ClassResult struct {
	ID               int64                  `json:"id"`
	Name             string                 `json:"name"`
	File             string                 `json:"file"`
	Repo             string                 `json:"repo"`
	StartLine        int                    `json:"startLine"`
	EndLine          int                    `json:"endLine"`
	Score            float64                `json:"score,omitempty"`
	Integrations     []ClassIntegration     `json:"integrations,omitempty"`
	HandledEndpoints []ClassHandledEndpoint `json:"handledEndpoints,omitempty"`
	SnapshotID       *int64                 `json:"snapshotId,omitempty"`
}

type EndpointResult struct {
	ID         int64  `json:"id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Handler    string `json:"handler,omitempty"`
	File       string `json:"file"`
	Repo       string `json:"repo"`
	Line       int    `json:"line"`
	Richness   int    `json:"richness"`
	SnapshotID *int64 `json:"snapshotId,omitempty"`
}

type DataEntityResult struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	File       string  `json:"file"`
	Repo       string  `json:"repo"`
	Line       int     `json:"line"`
	Access     string  `json:"access"`
	Caller     string  `json:"caller,omitempty"`
	CallerID   string  `json:"callerId,omitempty"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score,omitempty"`
	SnapshotID *int64  `json:"snapshotId,omitempty"`
}

type ExternalSymbolResult struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	File       string  `json:"file"`
	Repo       string  `json:"repo"`
	Line       int     `json:"line"`
	Caller     string  `json:"caller,omitempty"`
	CallerID   string  `json:"callerId,omitempty"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score,omitempty"`
	SnapshotID *int64  `json:"snapshotId,omitempty"`
}

type ScheduleResult struct {
	ID                 int64  `json:"id"`
	RuleName           string `json:"ruleName"`
	ScheduleExpression string `json:"scheduleExpression"`
	TargetType         string `json:"targetType"`
	TargetName         string `json:"targetName"`
	State              string `json:"state"`
	Source             string `json:"source"`
	SnapshotID         *int64 `json:"snapshotId,omitempty"`
}

type GraphQLOperationResult struct {
	ID            int64   `json:"id"`
	Kind          string  `json:"kind,omitempty"`
	Name          string  `json:"name"`
	OperationType string  `json:"operationType"`
	ResolverName  string  `json:"resolverName,omitempty"`
	File          string  `json:"file"`
	Repo          string  `json:"repo"`
	SnapshotID    *int64  `json:"snapshotId,omitempty"`
	Line          int     `json:"line"`
	UsageCount    int     `json:"usageCount"`
	Score         float64 `json:"score,omitempty"`
}

type AzureTriggerResult struct {
	ID           int64   `json:"id"`
	FunctionName string  `json:"functionName"`
	TriggerType  string  `json:"triggerType"`
	Route        string  `json:"route,omitempty"`
	ResourceName string  `json:"resourceName,omitempty"`
	File         string  `json:"file"`
	Repo         string  `json:"repo"`
	SnapshotID   *int64  `json:"snapshotId,omitempty"`
	Line         int     `json:"line"`
	Score        float64 `json:"score,omitempty"`
}

type QueueHitResult struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	File       string  `json:"file"`
	Repo       string  `json:"repo"`
	StartLine  int     `json:"startLine"`
	EndLine    int     `json:"endLine"`
	Queue      string  `json:"queue"`
	Role       string  `json:"role"`
	CallerID   string  `json:"callerId,omitempty"`
	Score      float64 `json:"score,omitempty"`
	SnapshotID *int64  `json:"snapshotId,omitempty"`
}

type TypeSymbolResult struct {
	ID         int64                  `json:"id"`
	Name       string                 `json:"name"`
	Kind       string                 `json:"kind"`
	File       string                 `json:"file"`
	Repo       string                 `json:"repo"`
	SnapshotID *int64                 `json:"snapshotId,omitempty"`
	Line       int                    `json:"line"`
	Score      float64                `json:"score,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

type SearchResults struct {
	Functions         []FunctionResult         `json:"functions"`
	Classes           []ClassResult            `json:"classes"`
	TypeSymbols       []TypeSymbolResult       `json:"typeSymbols,omitempty"`
	Endpoints         []EndpointResult         `json:"endpoints"`
	DataEntities      []DataEntityResult       `json:"dataEntities,omitempty"`
	ExternalSymbols   []ExternalSymbolResult   `json:"externalSymbols,omitempty"`
	Schedules         []ScheduleResult         `json:"schedules,omitempty"`
	GraphQLOperations []GraphQLOperationResult `json:"graphqlOperations,omitempty"`
	AzureTriggers     []AzureTriggerResult     `json:"azureTriggers,omitempty"`
	QueueHits         []QueueHitResult         `json:"queueHits,omitempty"`
}

type SearchBucketStats struct {
	Returned   int  `json:"returned"`
	Total      int  `json:"total"`
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	HasMore    bool `json:"hasMore"`
	Truncated  bool `json:"truncated"`
	ExactTotal bool `json:"exactTotal"`
	// Capped reports that the source query stopped at its fetch cap, so Total is
	// a lower bound and later pages may be missing.
	Capped bool `json:"capped,omitempty"`
	// FilteredCount is how many matches noNoise removed from this bucket.
	FilteredCount int `json:"filteredCount,omitempty"`
}

type SearchBucketBreakdown struct {
	Functions         SearchBucketStats `json:"functions"`
	Classes           SearchBucketStats `json:"classes"`
	TypeSymbols       SearchBucketStats `json:"typeSymbols"`
	Endpoints         SearchBucketStats `json:"endpoints"`
	DataEntities      SearchBucketStats `json:"dataEntities"`
	ExternalSymbols   SearchBucketStats `json:"externalSymbols"`
	Schedules         SearchBucketStats `json:"schedules"`
	GraphQLOperations SearchBucketStats `json:"graphqlOperations"`
	AzureTriggers     SearchBucketStats `json:"azureTriggers"`
	QueueHits         SearchBucketStats `json:"queueHits"`
}

type SearchStats struct {
	TotalResults    int                   `json:"totalResults"`
	ReturnedResults int                   `json:"returnedResults"`
	SearchTime      string                `json:"searchTime"`
	Limit           int                   `json:"limit"`
	HasMore         bool                  `json:"hasMore"`
	Truncated       bool                  `json:"truncated"`
	ExactTotal      bool                  `json:"exactTotal"`
	Buckets         SearchBucketBreakdown `json:"buckets"`
}

type WorkspaceHintMatch struct {
	WorkspaceID string `json:"workspaceId"`
	Repo        string `json:"repo"`
	Branch      string `json:"branch"`
	SHA         string `json:"sha"`
	File        string `json:"file,omitempty"`
	Symbol      string `json:"symbol,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

type WorkspaceHint struct {
	Kind            string               `json:"kind"`
	Query           string               `json:"query"`
	ActiveWorkspace string               `json:"activeWorkspace"`
	FoundIn         []WorkspaceHintMatch `json:"foundIn"`
}

type SearchResponse struct {
	Query          string                `json:"query"`
	RequestedMode  string                `json:"requestedMode"`
	Mode           string                `json:"mode"`
	Workspace      ResponseWorkspace     `json:"workspace"`
	RepoContext    []ResponseRepoContext `json:"repoContext,omitempty"`
	Results        SearchResults         `json:"results"`
	Stats          SearchStats           `json:"stats"`
	WorkspaceHints []WorkspaceHint       `json:"workspaceHints,omitempty"`
	Warnings       []string              `json:"warnings,omitempty"`
}

type SearchBucketOffsets struct {
	Functions         int
	Classes           int
	TypeSymbols       int
	Endpoints         int
	DataEntities      int
	ExternalSymbols   int
	Schedules         int
	GraphQLOperations int
	AzureTriggers     int
	QueueHits         int
}

type SearchOptions struct {
	Mode       string
	Limit      int
	Offsets    SearchBucketOffsets
	RepoFilter string
	Workspace  string
	SortBy     string
	NoNoise    bool
}

type searchBucketOffsets struct {
	Functions         int
	Classes           int
	TypeSymbols       int
	Endpoints         int
	DataEntities      int
	ExternalSymbols   int
	Schedules         int
	GraphQLOperations int
	AzureTriggers     int
	QueueHits         int
}

type searchExecution struct {
	Results  SearchResults
	Stats    SearchBucketBreakdown
	Hints    []WorkspaceHint
	Warnings []string
}

// markCapped records that a bucket was fetched up to its cap: the total is only a
// lower bound, so callers must not treat it as exact.
func markCapped(stats SearchBucketStats, capped bool) SearchBucketStats {
	if !capped {
		return stats
	}
	stats.Capped = true
	stats.ExactTotal = false
	stats.Truncated = true
	stats.HasMore = true
	return stats
}

func cappedWarning(buckets ...string) string {
	return fmt.Sprintf("%s matched more than %d rows and were capped; totals are lower bounds, narrow the query or repo filter", strings.Join(buckets, ", "), graph.SearchFetchCap)
}

func noNoiseWarning(functions, endpoints int) string {
	if functions == 0 && endpoints == 0 {
		return ""
	}
	return fmt.Sprintf("noNoise hid %d function(s) and %d endpoint(s); pass noNoise=false to include them", functions, endpoints)
}

type searchWorkspaceScope struct {
	Workspace        ResponseWorkspace
	RepoContext      []ResponseRepoContext
	IsDefault        bool
	EnforceSnapshots bool
	ActiveSnapshots  map[int64]bool
}

func normalizeSearchOptions(opts SearchOptions) SearchOptions {
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		mode = "keyword"
	}
	opts.Mode = mode

	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Limit > 50 {
		opts.Limit = 50
	}
	if opts.SortBy == "" {
		opts.SortBy = "richness"
	}
	return opts
}

func toInternalOffsets(offsets SearchBucketOffsets) searchBucketOffsets {
	return searchBucketOffsets{
		Functions:         offsets.Functions,
		Classes:           offsets.Classes,
		TypeSymbols:       offsets.TypeSymbols,
		Endpoints:         offsets.Endpoints,
		DataEntities:      offsets.DataEntities,
		ExternalSymbols:   offsets.ExternalSymbols,
		Schedules:         offsets.Schedules,
		GraphQLOperations: offsets.GraphQLOperations,
		AzureTriggers:     offsets.AzureTriggers,
		QueueHits:         offsets.QueueHits,
	}
}

func buildSearchStats(buckets SearchBucketBreakdown, limit int, searchTime string) SearchStats {
	return SearchStats{
		TotalResults: buckets.Functions.Total + buckets.Classes.Total + buckets.TypeSymbols.Total + buckets.Endpoints.Total +
			buckets.DataEntities.Total + buckets.ExternalSymbols.Total + buckets.Schedules.Total +
			buckets.GraphQLOperations.Total + buckets.AzureTriggers.Total + buckets.QueueHits.Total,
		ReturnedResults: buckets.Functions.Returned + buckets.Classes.Returned + buckets.TypeSymbols.Returned + buckets.Endpoints.Returned +
			buckets.DataEntities.Returned + buckets.ExternalSymbols.Returned + buckets.Schedules.Returned +
			buckets.GraphQLOperations.Returned + buckets.AzureTriggers.Returned + buckets.QueueHits.Returned,
		SearchTime: searchTime,
		Limit:      limit,
		HasMore: buckets.Functions.HasMore || buckets.Classes.HasMore || buckets.Endpoints.HasMore ||
			buckets.TypeSymbols.HasMore || buckets.DataEntities.HasMore || buckets.ExternalSymbols.HasMore || buckets.Schedules.HasMore ||
			buckets.GraphQLOperations.HasMore || buckets.AzureTriggers.HasMore || buckets.QueueHits.HasMore,
		Truncated: buckets.Functions.Truncated || buckets.Classes.Truncated || buckets.Endpoints.Truncated ||
			buckets.TypeSymbols.Truncated || buckets.DataEntities.Truncated || buckets.ExternalSymbols.Truncated || buckets.Schedules.Truncated ||
			buckets.GraphQLOperations.Truncated || buckets.AzureTriggers.Truncated || buckets.QueueHits.Truncated,
		ExactTotal: buckets.Functions.ExactTotal && buckets.Classes.ExactTotal && buckets.Endpoints.ExactTotal &&
			buckets.TypeSymbols.ExactTotal && buckets.DataEntities.ExactTotal && buckets.ExternalSymbols.ExactTotal && buckets.Schedules.ExactTotal &&
			buckets.GraphQLOperations.ExactTotal && buckets.AzureTriggers.ExactTotal && buckets.QueueHits.ExactTotal,
		Buckets: buckets,
	}
}

func (h *Handlers) ExecuteSearch(ctx context.Context, query string, opts SearchOptions) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResponse{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts = normalizeSearchOptions(opts)
	if !isValidMode(opts.Mode) {
		return SearchResponse{}, fmt.Errorf("invalid mode %q", opts.Mode)
	}

	start := time.Now()
	workspace, err := h.storage.WithQueryContext(ctx).ResolveWorkspace(opts.Workspace)
	if err != nil {
		return SearchResponse{}, err
	}
	scope, err := h.searchWorkspaceScope(workspace, ctx)
	if err != nil {
		return SearchResponse{}, err
	}
	if err := h.requireRepo(ctx, opts.RepoFilter, scope); err != nil {
		return SearchResponse{}, err
	}
	searchExec, actualMode, warnings, err := h.runSearch(
		ctx,
		query,
		opts.Mode,
		opts.Limit,
		toInternalOffsets(opts.Offsets),
		strings.TrimSpace(opts.RepoFilter),
		scope,
		strings.TrimSpace(opts.SortBy),
		opts.NoNoise,
	)
	if err != nil {
		return SearchResponse{}, err
	}

	if len(searchExec.Results.Functions) > 0 {
		callerIDs := make([]string, len(searchExec.Results.Functions))
		for i, f := range searchExec.Results.Functions {
			callerIDs[i] = f.CallerID
		}
		tagMap := FetchArchitectureTagsForSnapshots(h.storage.Pool(), callerIDs, activeSnapshotIDs(scope))
		for i := range searchExec.Results.Functions {
			if t, ok := tagMap[searchExec.Results.Functions[i].CallerID]; ok {
				searchExec.Results.Functions[i].Tags = t
			}
		}
	}
	if scope.EnforceSnapshots && searchResultsHaveLegacyUnscopedRows(searchExec.Results) {
		warnings = mergeUniqueStrings(warnings, []string{
			"Some returned results are legacy unscoped rows without branch/SHA provenance; reparse this workspace before trusting exact file and line targets.",
		})
	}

	if err := ctx.Err(); err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Query:          query,
		RequestedMode:  opts.Mode,
		Mode:           actualMode,
		Workspace:      scope.Workspace,
		RepoContext:    scope.RepoContext,
		Results:        searchExec.Results,
		Stats:          buildSearchStats(searchExec.Stats, opts.Limit, time.Since(start).String()),
		WorkspaceHints: searchExec.Hints,
		Warnings:       warnings,
	}, nil
}

func (h *Handlers) searchWorkspaceScope(workspace *graph.Workspace, contexts ...context.Context) (searchWorkspaceScope, error) {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	scope := searchWorkspaceScope{
		Workspace:        responseWorkspace(workspace),
		EnforceSnapshots: workspace != nil,
		ActiveSnapshots:  map[int64]bool{},
	}
	if workspace == nil {
		scope.IsDefault = true
		return scope, nil
	}
	scope.IsDefault = workspace.Slug == graph.DefaultWorkspaceSlug
	snapshots, err := h.storage.WithQueryContext(ctx).ActiveSnapshotsForWorkspace(workspace.Slug)
	if err != nil {
		return searchWorkspaceScope{}, fmt.Errorf("load active snapshots for workspace %q: %w", workspace.Slug, err)
	}
	for _, snapshot := range snapshots {
		scope.ActiveSnapshots[snapshot.SnapshotID] = true
		scope.RepoContext = append(scope.RepoContext, snapshot)
	}
	return scope, nil
}

func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing query parameter 'q'", nil)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "keyword"
	}
	if !isValidMode(mode) {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid mode", map[string]any{
			"mode":  mode,
			"valid": []string{"keyword", "trigram"},
		})
		return
	}
	limit, globalOffset, ok := parsePage(w, r, 10, 50)
	if !ok {
		return
	}
	var offsets searchBucketOffsets
	for _, bucket := range []struct {
		key string
		dst *int
	}{
		{"functionsOffset", &offsets.Functions},
		{"classesOffset", &offsets.Classes},
		{"typeSymbolsOffset", &offsets.TypeSymbols},
		{"endpointsOffset", &offsets.Endpoints},
		{"dataEntitiesOffset", &offsets.DataEntities},
		{"externalSymbolsOffset", &offsets.ExternalSymbols},
		{"schedulesOffset", &offsets.Schedules},
		{"graphqlOperationsOffset", &offsets.GraphQLOperations},
		{"azureTriggersOffset", &offsets.AzureTriggers},
		{"queueHitsOffset", &offsets.QueueHits},
	} {
		value, err := queryInt(r, bucket.key, globalOffset, 0)
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), map[string]any{"parameter": bucket.key})
			return
		}
		*bucket.dst = value
	}

	repoFilter := strings.TrimSpace(r.URL.Query().Get("repo"))
	noNoise := true
	if v := r.URL.Query().Get("noNoise"); v == "false" || v == "0" {
		noNoise = false
	}
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	resp, err := h.ExecuteSearch(r.Context(), query, SearchOptions{
		Mode:       mode,
		Limit:      limit,
		Offsets:    SearchBucketOffsets(offsets),
		RepoFilter: repoFilter,
		Workspace:  workspaceIDFromRequest(r),
		SortBy:     sortBy,
		NoNoise:    noNoise,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, graph.ErrInvalidWorkspace) {
			writeLookupError(w, err)
			return
		}
		status := http.StatusServiceUnavailable
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, "SEARCH_INCOMPLETE", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) runSearch(ctx context.Context, query, mode string, limit int, offsets searchBucketOffsets, repoFilter string, scope searchWorkspaceScope, sortBy string, noNoise bool) (searchExecution, string, []string, error) {
	switch mode {
	case "keyword":
		results, err := h.keywordSearch(ctx, query, limit, offsets, repoFilter, scope, sortBy, noNoise)
		if err != nil || searchExecutionTotal(results) > 0 {
			if err == nil {
				h.attachOtherWorkspaceHints(ctx, query, scope, &results)
			}
			return results, mode, results.Warnings, err
		}
		trigramResults, trigramErr := h.trigramSearch(ctx, query, limit, offsets, repoFilter, scope, noNoise)
		if trigramErr == nil && searchExecutionTotal(trigramResults) > 0 {
			h.attachOtherWorkspaceHints(ctx, query, scope, &trigramResults)
			return trigramResults, "trigram", mergeUniqueStrings([]string{"exact match returned nothing; showing trigram results"}, trigramResults.Warnings), nil
		}
		if trigramErr != nil {
			return results, mode, nil, trigramErr
		}
		h.attachOtherWorkspaceHints(ctx, query, scope, &results)
		return results, mode, mergeUniqueStrings(results.Warnings, trigramResults.Warnings), nil
	case "trigram":
		results, err := h.trigramSearch(ctx, query, limit, offsets, repoFilter, scope, noNoise)
		if err == nil {
			h.attachOtherWorkspaceHints(ctx, query, scope, &results)
		}
		return results, mode, results.Warnings, err
	default:
		return searchExecution{}, mode, nil, nil
	}
}

// attachOtherWorkspaceHints adds "exists elsewhere" hints from a small, separate
// query over snapshots that other workspaces have active. A failure only costs
// the hint, so it is logged and never fails the search.
func (h *Handlers) attachOtherWorkspaceHints(ctx context.Context, query string, scope searchWorkspaceScope, exec *searchExecution) {
	if !scope.EnforceSnapshots || len(exec.Hints) > 0 {
		return
	}
	exec.Hints = h.otherWorkspaceHints(ctx, query, scope)
}

// otherWorkspaceHints looks for the query's terms in snapshots that are active in
// other workspaces. These rows never take part in ordinary searches, so they
// cannot consume a result cap.
func (h *Handlers) otherWorkspaceHints(ctx context.Context, query string, scope searchWorkspaceScope) []WorkspaceHint {
	terms := splitSearchTerms(query)
	if len(terms) > 3 {
		terms = terms[:3]
	}
	var found []graph.SearchResult
	for _, term := range terms {
		matches, err := h.storage.WithQueryContext(ctx).DiscoverOtherWorkspaceHits(ctx, term, snapshotFilterForScope(scope), 4)
		if err != nil {
			log.Printf("WARN: other-workspace hint discovery failed for %q: %v", term, err)
			return nil
		}
		found = append(found, matches...)
	}
	return h.workspaceHintsForResults(query, scope, found)
}

func searchExecutionTotal(exec searchExecution) int {
	return exec.Stats.Functions.Total +
		exec.Stats.Classes.Total +
		exec.Stats.TypeSymbols.Total +
		exec.Stats.Endpoints.Total +
		exec.Stats.DataEntities.Total +
		exec.Stats.ExternalSymbols.Total +
		exec.Stats.Schedules.Total +
		exec.Stats.GraphQLOperations.Total +
		exec.Stats.AzureTriggers.Total +
		exec.Stats.QueueHits.Total
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isValidMode(mode string) bool {
	switch mode {
	case "keyword", "trigram":
		return true
	default:
		return false
	}
}

func (h *Handlers) keywordSearch(ctx context.Context, query string, limit int, offsets searchBucketOffsets, repoFilter string, scope searchWorkspaceScope, sortBy string, noNoise bool) (searchExecution, error) {
	terms := splitSearchTerms(query)
	var filters []graph.SnapshotFilter
	if scope.EnforceSnapshots {
		filters = append(filters, snapshotFilterForScope(scope))
	}

	var funcs []graph.SearchResult
	var classes []graph.SearchResult
	var typeSymbols []graph.SearchResult
	var endpoints []graph.SearchResult
	var dataEntities []graph.SearchResult
	var externalSymbols []graph.SearchResult
	var schedules []ScheduleResult
	var graphqlOperations []graph.SearchResult
	var azureTriggers []graph.SearchResult
	var queueHits []graph.SearchResult
	var typeSymbolsCapped, queueHitsCapped, schedulesCapped bool
	const bucketConcurrency = 4

	for _, term := range terms {
		if err := ctx.Err(); err != nil {
			return searchExecution{}, err
		}
		var tFuncs, tClasses, tTypeSymbols, tEndpoints, tDataEntities, tExternalSymbols, tGraphqlOps, tAzureTriggers []graph.SearchResult
		var tQueueHits []graph.SearchResult
		var tSchedules []ScheduleResult
		var bucketWg sync.WaitGroup
		var bucketMu sync.Mutex
		var bucketErrors []error
		recordError := func(name string, err error) {
			if err != nil {
				bucketMu.Lock()
				bucketErrors = append(bucketErrors, fmt.Errorf("%s search: %w", name, err))
				bucketMu.Unlock()
			}
		}
		bucketSem := make(chan struct{}, bucketConcurrency)
		runBucket := func(fn func()) {
			bucketWg.Add(1)
			go func() {
				defer bucketWg.Done()
				select {
				case bucketSem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-bucketSem }()
				if ctx.Err() != nil {
					return
				}
				fn()
			}()
		}

		storage := h.storage.WithQueryContext(ctx)
		runBucket(func() {
			var err error
			if sortBy == "" || sortBy == "richness" || sortBy == "discovery" {
				tFuncs, err = storage.SearchFunctionsRanked(term, 0, filters...)
			} else {
				tFuncs, err = storage.SearchFunctionsAll(term, filters...)
			}
			recordError("functions", err)
		})
		runBucket(func() {
			var err error
			tClasses, err = storage.SearchClassesAll(term, filters...)
			recordError("classes", err)
		})
		runBucket(func() {
			var err error
			var capped bool
			tTypeSymbols, capped, err = h.searchTypeSymbols(ctx, term, scopeForRepoFilter(scope, repoFilter))
			recordError("type symbols", err)
			if capped {
				bucketMu.Lock()
				typeSymbolsCapped = true
				bucketMu.Unlock()
			}
		})
		runBucket(func() {
			var err error
			tEndpoints, err = storage.SearchEndpointsAll(term, filters...)
			recordError("endpoints", err)
		})
		runBucket(func() {
			var err error
			tDataEntities, err = storage.SearchDataAccessesAll(term, filters...)
			recordError("data entities", err)
		})
		runBucket(func() {
			var err error
			tExternalSymbols, err = storage.SearchExternalSymbolsAll(term, filters...)
			recordError("external symbols", err)
		})
		runBucket(func() {
			var err error
			tSchedules, err = h.searchSchedulesChecked(ctx, term, graph.SearchFetchCap+1, scope)
			recordError("schedules", err)
			if len(tSchedules) > graph.SearchFetchCap {
				tSchedules = tSchedules[:graph.SearchFetchCap]
				bucketMu.Lock()
				schedulesCapped = true
				bucketMu.Unlock()
			}
		})
		runBucket(func() {
			var err error
			tGraphqlOps, err = storage.SearchGraphQLOperationsAll(term, filters...)
			recordError("GraphQL", err)
		})
		runBucket(func() {
			var err error
			tAzureTriggers, err = storage.SearchAzureTriggersAll(term, filters...)
			recordError("Azure triggers", err)
		})
		runBucket(func() {
			var err error
			var capped bool
			tQueueHits, capped, err = h.searchQueueHits(ctx, term, repoFilter, scope)
			recordError("queues", err)
			if capped {
				bucketMu.Lock()
				queueHitsCapped = true
				bucketMu.Unlock()
			}
		})

		bucketWg.Wait()
		if len(bucketErrors) > 0 {
			return searchExecution{}, errors.Join(bucketErrors...)
		}
		if err := ctx.Err(); err != nil {
			return searchExecution{}, err
		}
		funcs = append(funcs, tFuncs...)
		classes = append(classes, tClasses...)
		typeSymbols = append(typeSymbols, tTypeSymbols...)
		endpoints = append(endpoints, tEndpoints...)
		dataEntities = append(dataEntities, tDataEntities...)
		externalSymbols = append(externalSymbols, tExternalSymbols...)
		schedules = append(schedules, tSchedules...)
		graphqlOperations = append(graphqlOperations, tGraphqlOps...)
		azureTriggers = append(azureTriggers, tAzureTriggers...)
		queueHits = append(queueHits, tQueueHits...)
	}

	if len(terms) > 1 {
		funcs = boostMultiTermMatches(terms, funcs)
		classes = boostMultiTermMatchesGeneric(terms, classes)
		typeSymbols = boostMultiTermMatchesGeneric(terms, typeSymbols)
		endpoints = boostMultiTermMatchesGeneric(terms, endpoints)
		dataEntities = boostMultiTermMatchesGeneric(terms, dataEntities)
		externalSymbols = boostMultiTermMatchesGeneric(terms, externalSymbols)
		graphqlOperations = boostMultiTermMatchesGeneric(terms, graphqlOperations)
		azureTriggers = boostMultiTermMatchesGeneric(terms, azureTriggers)
		queueHits = boostMultiTermMatchesGeneric(terms, queueHits)
	}

	funcs = deduplicateResults(funcs)
	classes = deduplicateResults(classes)
	typeSymbols = deduplicateResults(typeSymbols)
	endpoints = deduplicateResults(endpoints)
	dataEntities = deduplicateResults(dataEntities)
	externalSymbols = deduplicateResults(externalSymbols)
	schedules = deduplicateSchedules(schedules)
	graphqlOperations = deduplicateResults(graphqlOperations)
	azureTriggers = deduplicateResults(azureTriggers)
	queueHits = deduplicateResults(queueHits)

	funcs = filterRepo(funcs, repoFilter)
	classes = filterRepo(classes, repoFilter)
	typeSymbols = filterRepo(typeSymbols, repoFilter)
	endpoints = filterRepo(endpoints, repoFilter)
	dataEntities = filterRepo(dataEntities, repoFilter)
	externalSymbols = filterRepo(externalSymbols, repoFilter)
	graphqlOperations = filterRepo(graphqlOperations, repoFilter)
	azureTriggers = filterRepo(azureTriggers, repoFilter)
	queueHits = filterRepo(queueHits, repoFilter)
	var workspaceExcluded []graph.SearchResult
	funcs, workspaceExcluded = partitionWorkspaceResults(funcs, scope, workspaceExcluded)
	classes, workspaceExcluded = partitionWorkspaceResults(classes, scope, workspaceExcluded)
	typeSymbols, workspaceExcluded = partitionWorkspaceResults(typeSymbols, scope, workspaceExcluded)
	endpoints, workspaceExcluded = partitionWorkspaceResults(endpoints, scope, workspaceExcluded)
	dataEntities, workspaceExcluded = partitionWorkspaceResults(dataEntities, scope, workspaceExcluded)
	externalSymbols, workspaceExcluded = partitionWorkspaceResults(externalSymbols, scope, workspaceExcluded)
	graphqlOperations, workspaceExcluded = partitionWorkspaceResults(graphqlOperations, scope, workspaceExcluded)
	azureTriggers, workspaceExcluded = partitionWorkspaceResults(azureTriggers, scope, workspaceExcluded)
	queueHits, workspaceExcluded = partitionWorkspaceResults(queueHits, scope, workspaceExcluded)
	endpointHandlerHits := findEndpointHandlerHits(endpoints)

	var noiseFunctions, noiseEndpoints int
	if noNoise {
		before := len(funcs)
		funcs = filterNoise(funcs)
		noiseFunctions = before - len(funcs)
		before = len(endpoints)
		endpoints = filterNoiseEndpoints(endpoints)
		noiseEndpoints = before - len(endpoints)
	}

	// For multi-term queries, boost functions whose direct callees match missing terms.
	var calleeTermHits map[string]bool
	if len(terms) > 1 {
		calleeTermHits = h.findCalleeTermHits(ctx, terms, funcs, scope)
	}
	funcs = rerankKeywordFunctions(query, funcs, h.getRepoFanIn(ctx, scope), calleeTermHits, endpointHandlerHits)

	functionPage, functionStats := paginateSearchResults(funcs, offsets.Functions, limit)
	classPage, classStats := paginateSearchResults(classes, offsets.Classes, limit)
	typeSymbolPage, typeSymbolStats := paginateSearchResults(typeSymbols, offsets.TypeSymbols, limit)
	endpointPage, endpointStats := paginateSearchResults(endpoints, offsets.Endpoints, limit)
	dataEntityPage, dataEntityStats := paginateSearchResults(dataEntities, offsets.DataEntities, limit)
	externalSymbolPage, externalSymbolStats := paginateSearchResults(externalSymbols, offsets.ExternalSymbols, limit)
	schedulePage, scheduleStats := paginateScheduleResults(schedules, offsets.Schedules, limit)
	graphqlOperationPage, graphqlOperationStats := paginateSearchResults(graphqlOperations, offsets.GraphQLOperations, limit)
	azureTriggerPage, azureTriggerStats := paginateSearchResults(azureTriggers, offsets.AzureTriggers, limit)
	queueHitPage, queueHitStats := paginateSearchResults(queueHits, offsets.QueueHits, limit)
	functionStats.FilteredCount = noiseFunctions
	endpointStats.FilteredCount = noiseEndpoints
	typeSymbolStats = markCapped(typeSymbolStats, typeSymbolsCapped)
	queueHitStats = markCapped(queueHitStats, queueHitsCapped)
	scheduleStats = markCapped(scheduleStats, schedulesCapped)
	var warnings []string
	if w := noNoiseWarning(noiseFunctions, noiseEndpoints); w != "" {
		warnings = append(warnings, w)
	}
	warnings = appendCappedWarning(warnings, typeSymbolsCapped, queueHitsCapped, schedulesCapped)

	return searchExecution{
		Warnings: warnings,
		Results: SearchResults{
			Functions:         mapFunctions(functionPage, limit),
			Classes:           mapClasses(classPage, limit),
			TypeSymbols:       mapTypeSymbols(typeSymbolPage, limit),
			Endpoints:         mapEndpoints(endpointPage, limit),
			DataEntities:      mapDataEntities(dataEntityPage, limit),
			ExternalSymbols:   mapExternalSymbols(externalSymbolPage, limit),
			Schedules:         schedulePage,
			GraphQLOperations: mapGraphQLOperations(graphqlOperationPage, limit),
			AzureTriggers:     mapAzureTriggers(azureTriggerPage, limit),
			QueueHits:         mapQueueHits(queueHitPage, limit),
		},
		Stats: SearchBucketBreakdown{
			Functions:         functionStats,
			Classes:           classStats,
			TypeSymbols:       typeSymbolStats,
			Endpoints:         endpointStats,
			DataEntities:      dataEntityStats,
			ExternalSymbols:   externalSymbolStats,
			Schedules:         scheduleStats,
			GraphQLOperations: graphqlOperationStats,
			AzureTriggers:     azureTriggerStats,
			QueueHits:         queueHitStats,
		},
		Hints: h.workspaceHintsForResults(query, scope, workspaceExcluded),
	}, nil
}

func appendCappedWarning(warnings []string, typeSymbols, queueHits, schedules bool) []string {
	var buckets []string
	if typeSymbols {
		buckets = append(buckets, "type symbols")
	}
	if queueHits {
		buckets = append(buckets, "queue hits")
	}
	if schedules {
		buckets = append(buckets, "schedules")
	}
	if len(buckets) == 0 {
		return warnings
	}
	return append(warnings, cappedWarning(buckets...))
}

// findCalleeTermHits finds direct callees matching query terms in the active
// snapshot set. One batch query covers all supplied callers and terms.
func (h *Handlers) findCalleeTermHits(ctx context.Context, terms []string, funcs []graph.SearchResult, scope searchWorkspaceScope) map[string]bool {
	hits := make(map[string]bool)
	if len(terms) < 2 || len(funcs) == 0 {
		return hits
	}

	// Build caller_ids for all function results
	callerIDs := make([]string, 0, len(funcs))
	for _, f := range funcs {
		if f.Type != "function" {
			continue
		}
		callerIDs = append(callerIDs, f.RepoName+":"+f.FilePath+":"+f.Name)
	}
	if len(callerIDs) == 0 {
		return hits
	}

	patterns := make([]string, 0, len(terms))
	for _, term := range terms {
		patterns = append(patterns, "%"+graph.EscapeLike(strings.ToLower(term))+"%")
	}
	query := fmt.Sprintf(`
		SELECT DISTINCT caller_id FROM trace_call_edges
		WHERE caller_id = ANY($1)
		  AND LOWER(callee_name) LIKE ANY($2::text[])
		  AND %s`, integrationSnapshotClause("snapshot_id", 3, workspaceIncludesLegacy(scope)))
	rows, err := h.storage.Pool().Query(ctx, query, callerIDs, patterns, activeSnapshotIDs(scope))
	if err != nil {
		log.Printf("WARN: search callee-term ranking skipped: %v", err)
		return hits
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			log.Printf("WARN: search callee-term ranking scan failed: %v", err)
			continue
		}
		hits[cid] = true
	}
	if err := rows.Err(); err != nil {
		log.Printf("WARN: search callee-term ranking incomplete: %v", err)
	}
	return hits
}

func findEndpointHandlerHits(endpoints []graph.SearchResult) map[string]bool {
	hits := make(map[string]bool)
	for _, endpoint := range endpoints {
		if endpoint.Extra == nil {
			continue
		}
		handler, ok := endpoint.Extra["handler"].(string)
		if !ok || strings.TrimSpace(handler) == "" {
			continue
		}
		hits[endpoint.RepoName+":"+endpoint.FilePath+":"+handler] = true
	}
	return hits
}

func partitionWorkspaceResults(results []graph.SearchResult, scope searchWorkspaceScope, excluded []graph.SearchResult) ([]graph.SearchResult, []graph.SearchResult) {
	if !scope.EnforceSnapshots {
		return results, excluded
	}
	out := results[:0]
	for _, result := range results {
		if result.SnapshotID == nil {
			if workspaceIncludesLegacy(scope) {
				out = append(out, result)
			}
			continue
		}
		if scope.ActiveSnapshots[*result.SnapshotID] {
			out = append(out, result)
			continue
		}
		excluded = append(excluded, result)
	}
	return out, excluded
}

func searchResultsHaveLegacyUnscopedRows(results SearchResults) bool {
	for _, result := range results.Functions {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.Classes {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.TypeSymbols {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.Endpoints {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.DataEntities {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.ExternalSymbols {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.GraphQLOperations {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.AzureTriggers {
		if result.SnapshotID == nil {
			return true
		}
	}
	for _, result := range results.QueueHits {
		if result.SnapshotID == nil {
			return true
		}
	}
	return false
}

func (h *Handlers) workspaceHintsForResults(query string, scope searchWorkspaceScope, excluded []graph.SearchResult) []WorkspaceHint {
	if len(excluded) == 0 {
		return nil
	}
	snapshotSeen := make(map[int64]bool)
	var snapshotIDs []int64
	for _, result := range excluded {
		if result.SnapshotID == nil || snapshotSeen[*result.SnapshotID] {
			continue
		}
		snapshotSeen[*result.SnapshotID] = true
		snapshotIDs = append(snapshotIDs, *result.SnapshotID)
	}
	if len(snapshotIDs) == 0 {
		return nil
	}
	refsBySnapshot, err := h.storage.WorkspaceRefsForSnapshots(snapshotIDs)
	if err != nil {
		log.Printf("WARN: workspace hint lookup failed: %v", err)
		return nil
	}

	found := make([]WorkspaceHintMatch, 0, 8)
	seen := make(map[string]bool)
	for _, result := range excluded {
		if result.SnapshotID == nil {
			continue
		}
		for _, ref := range refsBySnapshot[*result.SnapshotID] {
			if ref.Workspace == scope.Workspace.ID {
				continue
			}
			match := WorkspaceHintMatch{
				WorkspaceID: ref.Workspace,
				Repo:        ref.RepoName,
				Branch:      ref.Branch,
				SHA:         ref.SHA,
				File:        result.FilePath,
				Symbol:      result.Name,
				Kind:        result.Type,
			}
			key := strings.Join([]string{match.WorkspaceID, match.Repo, match.Branch, match.SHA, match.File, match.Symbol, match.Kind}, "|")
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, match)
			if len(found) >= 8 {
				break
			}
		}
		if len(found) >= 8 {
			break
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []WorkspaceHint{{
		Kind:            "exists_elsewhere",
		Query:           query,
		ActiveWorkspace: scope.Workspace.ID,
		FoundIn:         found,
	}}
}

func (h *Handlers) workspaceHintsForQuery(ctx context.Context, query string, scope searchWorkspaceScope) []WorkspaceHint {
	query = strings.TrimSpace(query)
	if query == "" || !scope.EnforceSnapshots {
		return nil
	}
	return h.otherWorkspaceHints(ctx, query, scope)
}

func (h *Handlers) trigramSearch(ctx context.Context, query string, limit int, offsets searchBucketOffsets, repoFilter string, scope searchWorkspaceScope, noNoise bool) (searchExecution, error) {
	if err := ctx.Err(); err != nil {
		return searchExecution{}, err
	}
	var filters []graph.SnapshotFilter
	if scope.EnforceSnapshots {
		filters = append(filters, snapshotFilterForScope(scope))
	}
	fuzzy, err := h.storage.WithQueryContext(ctx).TrigramSearch(query, 0, filters...)
	if err != nil {
		return searchExecution{}, err
	}
	fuzzy = filterRepo(fuzzy, repoFilter)
	var workspaceExcluded []graph.SearchResult
	fuzzy, workspaceExcluded = partitionWorkspaceResults(fuzzy, scope, workspaceExcluded)

	var funcs []graph.SearchResult
	var classes []graph.SearchResult
	var dataEntities []graph.SearchResult
	var externalSymbols []graph.SearchResult
	var graphqlOperations []graph.SearchResult
	var azureTriggers []graph.SearchResult
	for _, r := range fuzzy {
		switch r.Type {
		case "function":
			funcs = append(funcs, r)
		case "class":
			classes = append(classes, r)
		case "data_entity":
			dataEntities = append(dataEntities, r)
		case "external_symbol":
			externalSymbols = append(externalSymbols, r)
		case "graphql_operation", "graphql_resolver":
			graphqlOperations = append(graphqlOperations, r)
		case "azure_trigger":
			azureTriggers = append(azureTriggers, r)
		}
	}

	var noiseFunctions, noiseEndpoints int
	if noNoise {
		before := len(funcs)
		funcs = filterNoise(funcs)
		noiseFunctions = before - len(funcs)
	}

	endpoints, err := h.storage.WithQueryContext(ctx).SearchEndpointsAll(query, filters...)
	if err != nil {
		return searchExecution{}, err
	}
	endpoints = filterRepo(endpoints, repoFilter)
	endpoints, workspaceExcluded = partitionWorkspaceResults(endpoints, scope, workspaceExcluded)
	if noNoise {
		before := len(endpoints)
		endpoints = filterNoiseEndpoints(endpoints)
		noiseEndpoints = before - len(endpoints)
	}
	typeSymbols, typeSymbolsCapped, err := h.searchTypeSymbols(ctx, query, scopeForRepoFilter(scope, repoFilter))
	if err != nil {
		return searchExecution{}, err
	}
	typeSymbols = filterRepo(typeSymbols, repoFilter)
	typeSymbols, workspaceExcluded = partitionWorkspaceResults(typeSymbols, scope, workspaceExcluded)
	queueHits, queueHitsCapped, err := h.searchQueueHits(ctx, query, repoFilter, scope)
	if err != nil {
		return searchExecution{}, err
	}
	queueHits = filterRepo(queueHits, repoFilter)
	queueHits, workspaceExcluded = partitionWorkspaceResults(queueHits, scope, workspaceExcluded)
	schedules, err := h.searchSchedulesChecked(ctx, query, graph.SearchFetchCap+1, scope)
	if err != nil {
		return searchExecution{}, err
	}
	schedulesCapped := len(schedules) > graph.SearchFetchCap
	if schedulesCapped {
		schedules = schedules[:graph.SearchFetchCap]
	}
	schedules = deduplicateSchedules(schedules)

	functionPage, functionStats := paginateSearchResults(funcs, offsets.Functions, limit)
	classPage, classStats := paginateSearchResults(classes, offsets.Classes, limit)
	typeSymbolPage, typeSymbolStats := paginateSearchResults(typeSymbols, offsets.TypeSymbols, limit)
	endpointPage, endpointStats := paginateSearchResults(endpoints, offsets.Endpoints, limit)
	dataEntityPage, dataEntityStats := paginateSearchResults(dataEntities, offsets.DataEntities, limit)
	externalSymbolPage, externalSymbolStats := paginateSearchResults(externalSymbols, offsets.ExternalSymbols, limit)
	graphqlOperationPage, graphqlOperationStats := paginateSearchResults(graphqlOperations, offsets.GraphQLOperations, limit)
	azureTriggerPage, azureTriggerStats := paginateSearchResults(azureTriggers, offsets.AzureTriggers, limit)
	queueHitPage, queueHitStats := paginateSearchResults(queueHits, offsets.QueueHits, limit)
	schedulePage, scheduleStats := paginateScheduleResults(schedules, offsets.Schedules, limit)
	functionStats.FilteredCount = noiseFunctions
	endpointStats.FilteredCount = noiseEndpoints
	typeSymbolStats = markCapped(typeSymbolStats, typeSymbolsCapped)
	queueHitStats = markCapped(queueHitStats, queueHitsCapped)
	scheduleStats = markCapped(scheduleStats, schedulesCapped)
	var warnings []string
	if w := noNoiseWarning(noiseFunctions, noiseEndpoints); w != "" {
		warnings = append(warnings, w)
	}
	warnings = appendCappedWarning(warnings, typeSymbolsCapped, queueHitsCapped, schedulesCapped)

	return searchExecution{
		Warnings: warnings,
		Results: SearchResults{
			Functions:         mapFunctions(functionPage, limit),
			Classes:           mapClasses(classPage, limit),
			TypeSymbols:       mapTypeSymbols(typeSymbolPage, limit),
			Endpoints:         mapEndpoints(endpointPage, limit),
			DataEntities:      mapDataEntities(dataEntityPage, limit),
			ExternalSymbols:   mapExternalSymbols(externalSymbolPage, limit),
			Schedules:         schedulePage,
			GraphQLOperations: mapGraphQLOperations(graphqlOperationPage, limit),
			AzureTriggers:     mapAzureTriggers(azureTriggerPage, limit),
			QueueHits:         mapQueueHits(queueHitPage, limit),
		},
		Stats: SearchBucketBreakdown{
			Functions:         functionStats,
			Classes:           classStats,
			TypeSymbols:       typeSymbolStats,
			Endpoints:         endpointStats,
			DataEntities:      dataEntityStats,
			ExternalSymbols:   externalSymbolStats,
			Schedules:         scheduleStats,
			GraphQLOperations: graphqlOperationStats,
			AzureTriggers:     azureTriggerStats,
			QueueHits:         queueHitStats,
		},
		Hints: h.workspaceHintsForResults(query, scope, workspaceExcluded),
	}, nil
}

// filterRepo applies the same matcher as requireRepo and the SQL filters:
// case-insensitive exact name, or prefix when the filter ends in "*".
func filterRepo(results []graph.SearchResult, repoFilter string) []graph.SearchResult {
	repoFilter = strings.TrimSpace(repoFilter)
	if repoFilter == "" {
		return results
	}
	var out []graph.SearchResult
	for _, r := range results {
		if graph.RepoFilterMatches(r.RepoName, repoFilter) {
			out = append(out, r)
		}
	}
	return out
}

func rerankKeywordFunctions(query string, results []graph.SearchResult, repoFanIn map[string]int, calleeTermHits map[string]bool, endpointHandlerHits map[string]bool) []graph.SearchResult {
	if len(results) < 2 {
		return results
	}
	query = strings.TrimSpace(strings.ToLower(query))
	ranked := append([]graph.SearchResult(nil), results...)
	sort.SliceStable(ranked, func(i, j int) bool {
		si := keywordDiscoveryScore(query, ranked[i], repoFanIn, calleeTermHits, endpointHandlerHits)
		sj := keywordDiscoveryScore(query, ranked[j], repoFanIn, calleeTermHits, endpointHandlerHits)
		if si != sj {
			return si > sj
		}
		ri := richnessFromResult(ranked[i])
		rj := richnessFromResult(ranked[j])
		if ri != rj {
			return ri > rj
		}
		return strings.ToLower(ranked[i].Name) < strings.ToLower(ranked[j].Name)
	})
	return ranked
}

func keywordDiscoveryScore(queryLower string, r graph.SearchResult, repoFanIn map[string]int, calleeTermHits map[string]bool, endpointHandlerHits map[string]bool) int {
	nameLower := strings.ToLower(r.Name)
	pathLower := strings.ToLower(r.FilePath)
	repoLower := strings.ToLower(r.RepoName)
	methodLower := leafIdentifier(nameLower)
	sourceLower := ""
	if r.SourceCode != "" {
		sourceLower = strings.ToLower(r.SourceCode)
	}
	score := 0

	if queryLower != "" {
		queryTerms := strings.Fields(queryLower)
		exactLeafMatch := false
		prefixLeafVariant := false
		for _, qt := range queryTerms {
			if methodLower == qt {
				exactLeafMatch = true
				continue
			}
			if strings.HasPrefix(methodLower, qt) {
				prefixLeafVariant = true
			}
		}

		if strings.Contains(nameLower, queryLower) {
			score += 60
		}

		// Multi-term: name matches are strong signal, repo-only matches are weaker
		if len(queryTerms) > 1 {
			nameMatched := 0
			repoMatched := 0
			sourceMatched := 0
			allCovered := 0
			for _, qt := range queryTerms {
				inName := strings.Contains(nameLower, qt)
				inRepo := strings.Contains(repoLower, qt)
				inSource := sourceLower != "" && strings.Contains(sourceLower, qt)
				if inName {
					nameMatched++
					allCovered++
					score += 30
				} else if inRepo {
					repoMatched++
					allCovered++
					score += 10 // repo-only match is weaker signal
				} else if inSource {
					sourceMatched++
					allCovered++
					score += 8 // source/body matches help recover conceptually relevant functions
				}
			}
			if nameMatched >= len(queryTerms) {
				score += 50 // all terms in function name — strong
			} else if allCovered >= len(queryTerms) {
				score += 15 // all terms covered across name+repo+source — weaker than pure name match
			}
			if exactLeafMatch && allCovered >= len(queryTerms) {
				score += 55
			}
			if prefixLeafVariant && !exactLeafMatch {
				score -= 30
			}

			// If the name covers the core action and the missing term is only in the body,
			// prefer that over generic single-term matches.
			if nameMatched > 0 && sourceMatched > 0 {
				score += 28
			}
			if nameMatched > 0 && repoMatched > 0 {
				score += 10
			}

			// Callee-term boost: if this function's direct callees contain the missing terms
			callerID := r.RepoName + ":" + r.FilePath + ":" + r.Name
			if calleeTermHits[callerID] {
				score += 40
			}
		} else {
			if hasIdentifierTokenMatch(r.Name, queryLower) {
				score += 35
			}
			if exactLeafMatch {
				score += 55
			}
		}

		// Prefix/dot match — per-term for multi-term queries
		queryTerms2 := strings.Fields(queryLower)
		for _, qt := range queryTerms2 {
			if strings.HasPrefix(nameLower, qt+".") || strings.Contains(nameLower, "."+qt) {
				score += 15
				break
			}
		}
	}

	// Richness stays relevant, but is not allowed to dominate relevance and signal quality.
	score += minInt(richnessFromResult(r), 200) / 8

	if strings.Contains(nameLower, "controller.") {
		score += 25
	}
	if strings.Contains(nameLower, "service.") {
		score += 20
	}
	if strings.Contains(nameLower, "job") || strings.Contains(nameLower, "consumer") {
		score += 12
	}
	if strings.Contains(pathLower, "/src/main/java/") {
		score += 8
	}
	if endpointHandlerHits[r.RepoName+":"+r.FilePath+":"+r.Name] {
		score += 150
	}

	if isAccessorName(r.Name) {
		score -= 30
	}
	if strings.Contains(pathLower, "/src/test/") || strings.Contains(pathLower, "/test/") {
		score -= 35
	}
	if isFrontendAsset(pathLower) {
		score -= 25
	}
	if looksLikeSyntheticCallback(nameLower) {
		score -= 80
	}
	// Deprioritize infrastructure/glue code — DAO impls, filters, mappers, validators, converters
	if strings.Contains(nameLower, "daoimpl.") || strings.Contains(nameLower, "dao.") ||
		strings.HasSuffix(nameLower, "daoimpl") {
		score -= 25
	}
	if strings.Contains(nameLower, "filter.") || strings.Contains(nameLower, "mapper.") ||
		strings.Contains(nameLower, "converter.") || strings.Contains(nameLower, "interceptor.") {
		score -= 15
	}
	// Deprioritize pure data retrieval methods (get*Dtos*, retrieve*Dtos*)
	dotIdx := strings.LastIndex(nameLower, ".")
	if dotIdx >= 0 {
		methodLower := nameLower[dotIdx+1:]
		if (strings.HasPrefix(methodLower, "get") || strings.HasPrefix(methodLower, "retrieve")) &&
			strings.Contains(methodLower, "dto") {
			score -= 15
		}
	}
	// Deprioritize HTTP client wrappers — they're transport, not logic
	if strings.Contains(nameLower, "httpclient.") {
		score -= 20
	}
	// Deprioritize generic REST methods (.get, .post, .put, .delete) on service/API classes
	if dotIdx >= 0 {
		methodLower := nameLower[dotIdx+1:]
		if methodLower == "get" || methodLower == "post" || methodLower == "put" || methodLower == "delete" {
			score -= 20
		}
	}

	// Boost functions from repos that many other repos depend on (core business logic).
	// A repo with fan-in 20 (depended on by 20 repos) gets +20; fan-in 2 gets +2. Capped at 25.
	if repoFanIn != nil {
		fi := repoFanIn[r.RepoName]
		score += minInt(fi, 25)
	}
	return score
}

func leafIdentifier(nameLower string) string {
	if idx := strings.LastIndex(nameLower, "."); idx >= 0 && idx+1 < len(nameLower) {
		return nameLower[idx+1:]
	}
	return nameLower
}

func hasIdentifierTokenMatch(identifier, queryLower string) bool {
	queryTokens := splitIdentifierTokens(queryLower)
	if len(queryTokens) == 0 {
		return false
	}
	identTokens := splitIdentifierTokens(identifier)
	if len(identTokens) == 0 {
		return false
	}
	tokenSet := make(map[string]struct{}, len(identTokens))
	for _, tok := range identTokens {
		tokenSet[tok] = struct{}{}
	}
	for _, qt := range queryTokens {
		if _, ok := tokenSet[qt]; ok {
			return true
		}
	}
	return false
}

func splitIdentifierTokens(value string) []string {
	if value == "" {
		return nil
	}
	var b strings.Builder
	runes := []rune(value)
	for i, r := range runes {
		if isIdentifierSep(r) {
			b.WriteRune(' ')
			continue
		}
		if i > 0 {
			prev := runes[i-1]
			if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Fields(b.String())
}

func isIdentifierSep(r rune) bool {
	switch r {
	case '.', '_', '-', '/', '\\', ':', '(', ')', '[', ']', '{', '}', ',', ';', ' ':
		return true
	default:
		return false
	}
}

func richnessFromResult(r graph.SearchResult) int {
	if r.Extra == nil {
		return 0
	}
	switch v := r.Extra["richness"].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mapFunctions(results []graph.SearchResult, limit int) []FunctionResult {
	var out []FunctionResult
	for _, r := range results {
		richness := 0
		if r.Extra != nil {
			if v, ok := r.Extra["richness"].(int); ok {
				richness = v
			}
		}
		out = append(out, FunctionResult{
			ID:         r.ID,
			Name:       r.Name,
			File:       r.FilePath,
			Repo:       r.RepoName,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			Source:     r.SourceCode,
			Score:      scoreFromExtra(r.Extra),
			Richness:   richness,
			CallerID:   r.RepoName + ":" + r.FilePath + ":" + r.Name,
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapQueueHits(results []graph.SearchResult, limit int) []QueueHitResult {
	var out []QueueHitResult
	for _, r := range results {
		queue := ""
		role := ""
		if r.Extra != nil {
			if v, ok := r.Extra["queue"].(string); ok {
				queue = v
			}
			if producer, ok := r.Extra["producer"].(bool); ok && producer {
				role = "producer"
			}
			if consumer, ok := r.Extra["consumer"].(bool); ok && consumer {
				role = "consumer"
			}
		}
		if queue == "" {
			continue
		}
		out = append(out, QueueHitResult{
			ID:         r.ID,
			Name:       r.Name,
			File:       r.FilePath,
			Repo:       r.RepoName,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			Queue:      queue,
			Role:       role,
			CallerID:   r.RepoName + ":" + r.FilePath + ":" + r.Name,
			Score:      scoreFromExtra(r.Extra),
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapClasses(results []graph.SearchResult, limit int) []ClassResult {
	var out []ClassResult
	for _, r := range results {
		out = append(out, ClassResult{
			ID:         r.ID,
			Name:       r.Name,
			File:       r.FilePath,
			Repo:       r.RepoName,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			Score:      scoreFromExtra(r.Extra),
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapEndpoints(results []graph.SearchResult, limit int) []EndpointResult {
	var out []EndpointResult
	for _, r := range results {
		method := ""
		handler := ""
		if r.Extra != nil {
			if v, ok := r.Extra["method"].(string); ok {
				method = v
			}
			if v, ok := r.Extra["handler"].(string); ok {
				handler = v
			}
		}
		out = append(out, EndpointResult{
			ID:         r.ID,
			Method:     method,
			Path:       r.Name,
			Handler:    handler,
			File:       r.FilePath,
			Repo:       r.RepoName,
			Line:       r.StartLine,
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapDataEntities(results []graph.SearchResult, limit int) []DataEntityResult {
	var out []DataEntityResult
	for _, r := range results {
		access := ""
		caller := ""
		callerID := ""
		if r.Extra != nil {
			if v, ok := r.Extra["access"].(string); ok {
				access = v
			}
			if v, ok := r.Extra["caller"].(string); ok {
				caller = v
			}
			if v, ok := r.Extra["caller_id"].(string); ok {
				callerID = v
			}
		}
		out = append(out, DataEntityResult{
			ID:         r.ID,
			Name:       r.Name,
			File:       r.FilePath,
			Repo:       r.RepoName,
			Line:       r.StartLine,
			Access:     access,
			Caller:     caller,
			CallerID:   callerID,
			Source:     r.SourceCode,
			Score:      scoreFromExtra(r.Extra),
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapExternalSymbols(results []graph.SearchResult, limit int) []ExternalSymbolResult {
	var out []ExternalSymbolResult
	for _, r := range results {
		caller := ""
		callerID := ""
		if r.Extra != nil {
			if v, ok := r.Extra["caller"].(string); ok {
				caller = v
			}
			if v, ok := r.Extra["caller_id"].(string); ok {
				callerID = v
			}
		}
		out = append(out, ExternalSymbolResult{
			ID:         r.ID,
			Name:       r.Name,
			File:       r.FilePath,
			Repo:       r.RepoName,
			Line:       r.StartLine,
			Caller:     caller,
			CallerID:   callerID,
			Source:     r.SourceCode,
			Score:      scoreFromExtra(r.Extra),
			SnapshotID: r.SnapshotID,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// searchTypeSymbols gathers the capped type-symbol sources. Each source fetches
// at most graph.SearchFetchCap+1 rows; capped reports that any source overflowed
// (the overflow row is dropped), so the combined total is only a lower bound.
func (h *Handlers) searchTypeSymbols(ctx context.Context, query string, scopes ...searchWorkspaceScope) ([]graph.SearchResult, bool, error) {
	var filters []graph.SnapshotFilter
	if len(scopes) > 0 && scopes[0].EnforceSnapshots {
		filters = append(filters, snapshotFilterForScope(scopes[0]))
	}
	storage := h.storage.WithQueryContext(ctx)
	sources := []func() ([]graph.SearchResult, error){
		func() ([]graph.SearchResult, error) { return storage.SearchInterfaces(query, filters...) },
		func() ([]graph.SearchResult, error) { return storage.SearchTypeAliases(query, filters...) },
		func() ([]graph.SearchResult, error) { return storage.SearchHookCalls(query, filters...) },
		func() ([]graph.SearchResult, error) { return storage.SearchVueComponentContracts(query, filters...) },
		func() ([]graph.SearchResult, error) { return storage.SearchPiniaStores(query, filters...) },
		func() ([]graph.SearchResult, error) { return storage.SearchEnumConstants(query, filters...) },
	}
	var out []graph.SearchResult
	capped := false
	for _, source := range sources {
		part, err := source()
		if err != nil {
			return nil, false, err
		}
		part, over := capSearchResults(part)
		capped = capped || over
		out = append(out, part...)
	}
	return deduplicateResults(out), capped, nil
}

// scopeForRepoFilter narrows an enforced workspace scope to the snapshots of the
// repositories a filter selects, so per-source capped queries are not exhausted
// by other repositories before filterRepo runs.
func scopeForRepoFilter(scope searchWorkspaceScope, repoFilter string) searchWorkspaceScope {
	if strings.TrimSpace(repoFilter) == "" || !scope.EnforceSnapshots {
		return scope
	}
	narrowed := scope
	narrowed.RepoContext = nil
	narrowed.ActiveSnapshots = map[int64]bool{}
	for _, ref := range scope.RepoContext {
		if graph.RepoFilterMatches(ref.RepoName, repoFilter) {
			narrowed.RepoContext = append(narrowed.RepoContext, ref)
			narrowed.ActiveSnapshots[ref.SnapshotID] = true
		}
	}
	return narrowed
}

// capSearchResults trims a source fetched with graph.SearchFetchCap+1 rows.
func capSearchResults(results []graph.SearchResult) ([]graph.SearchResult, bool) {
	if len(results) > graph.SearchFetchCap {
		return results[:graph.SearchFetchCap], true
	}
	return results, false
}

func (h *Handlers) searchQueueHits(ctx context.Context, query string, repoFilter string, scope searchWorkspaceScope) ([]graph.SearchResult, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false, nil
	}
	patterns := []string{"%" + graph.EscapeLike(strings.ToLower(query)) + "%"}
	var includedRepos []string
	if strings.TrimSpace(repoFilter) != "" {
		includedRepos = []string{strings.TrimSpace(repoFilter)}
	}
	var filters []graph.SnapshotFilter
	if scope.EnforceSnapshots {
		filters = append(filters, snapshotFilterForScope(scope))
	}
	var out []graph.SearchResult
	capped := false
	producers, err := h.storage.SearchQueueProducers(ctx, patterns, graph.SearchFetchCap+1, includedRepos, filters...)
	if err != nil {
		return nil, false, err
	}
	producers, over := capSearchResults(producers)
	capped = capped || over
	out = append(out, producers...)
	consumers, err := h.storage.SearchQueueConsumers(ctx, patterns, graph.SearchFetchCap+1, includedRepos, filters...)
	if err != nil {
		return nil, false, err
	}
	consumers, over = capSearchResults(consumers)
	capped = capped || over
	out = append(out, consumers...)
	return deduplicateResults(out), capped, nil
}

func mapTypeSymbols(results []graph.SearchResult, limit int) []TypeSymbolResult {
	var out []TypeSymbolResult
	for _, r := range results {
		out = append(out, TypeSymbolResult{
			ID:         r.ID,
			Name:       r.Name,
			Kind:       r.Type,
			File:       r.FilePath,
			Repo:       r.RepoName,
			SnapshotID: r.SnapshotID,
			Line:       r.StartLine,
			Score:      scoreFromExtra(r.Extra),
			Extra:      r.Extra,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapGraphQLOperations(results []graph.SearchResult, limit int) []GraphQLOperationResult {
	var out []GraphQLOperationResult
	for _, r := range results {
		operationType := ""
		resolverName := ""
		usageCount := 0
		if r.Extra != nil {
			if v, ok := r.Extra["operation_type"].(string); ok {
				operationType = v
			}
			if v, ok := r.Extra["resolver_name"].(string); ok {
				resolverName = v
			}
			if v, ok := r.Extra["usage_count"].(int); ok {
				usageCount = v
			}
		}
		out = append(out, GraphQLOperationResult{
			ID:            r.ID,
			Kind:          r.Type,
			Name:          r.Name,
			OperationType: operationType,
			ResolverName:  resolverName,
			File:          r.FilePath,
			Repo:          r.RepoName,
			SnapshotID:    r.SnapshotID,
			Line:          r.StartLine,
			UsageCount:    usageCount,
			Score:         scoreFromExtra(r.Extra),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func mapAzureTriggers(results []graph.SearchResult, limit int) []AzureTriggerResult {
	var out []AzureTriggerResult
	for _, r := range results {
		triggerType := ""
		route := ""
		resourceName := ""
		if r.Extra != nil {
			if v, ok := r.Extra["trigger_type"].(string); ok {
				triggerType = v
			}
			if v, ok := r.Extra["route"].(string); ok {
				route = v
			}
			if v, ok := r.Extra["resource_name"].(string); ok {
				resourceName = v
			}
		}
		out = append(out, AzureTriggerResult{
			ID:           r.ID,
			FunctionName: r.Name,
			TriggerType:  triggerType,
			Route:        route,
			ResourceName: resourceName,
			File:         r.FilePath,
			Repo:         r.RepoName,
			SnapshotID:   r.SnapshotID,
			Line:         r.StartLine,
			Score:        scoreFromExtra(r.Extra),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func scoreFromExtra(extra map[string]interface{}) float64 {
	if extra == nil {
		return 0
	}
	if v, ok := extra["score"]; ok {
		if score, ok := v.(float64); ok {
			return score
		}
	}
	if v, ok := extra["similarity"]; ok {
		if score, ok := v.(float64); ok {
			return score
		}
	}
	return 0
}

func (h *Handlers) searchSchedules(query string, limit int, scope searchWorkspaceScope) []ScheduleResult {
	out, _ := h.searchSchedulesChecked(context.Background(), query, limit, scope)
	return out
}
func (h *Handlers) searchSchedulesChecked(ctx context.Context, query string, limit int, scope searchWorkspaceScope) ([]ScheduleResult, error) {
	pattern := "%" + graph.EscapeLike(query) + "%"
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT id, rule_name, schedule_expression, target_type, target_name,
		       COALESCE(state, 'ENABLED'), COALESCE(source, 'eventbridge')
		FROM eventbridge_schedules
		WHERE rule_name ILIKE $1
		   OR target_name ILIKE $1
		   OR schedule_expression ILIKE $1
		ORDER BY rule_name
		LIMIT NULLIF($2, 0)
	`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScheduleResult
	for rows.Next() {
		var s ScheduleResult
		if err := rows.Scan(&s.ID, &s.RuleName, &s.ScheduleExpression, &s.TargetType, &s.TargetName, &s.State, &s.Source); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	remaining := 0
	if limit > 0 {
		remaining = limit - len(out)
		if remaining <= 0 {
			return out, nil
		}
	}
	timers, err := h.searchAzureTimerSchedulesChecked(ctx, query, remaining, scope)
	if err != nil {
		return nil, err
	}
	return append(out, timers...), nil
}

func (h *Handlers) searchAzureTimerSchedules(query string, limit int, scope searchWorkspaceScope) []ScheduleResult {
	out, _ := h.searchAzureTimerSchedulesChecked(context.Background(), query, limit, scope)
	return out
}
func (h *Handlers) searchAzureTimerSchedulesChecked(ctx context.Context, query string, limit int, scope searchWorkspaceScope) ([]ScheduleResult, error) {
	pattern := "%" + graph.EscapeLike(query) + "%"
	snapshotIDs := activeSnapshotIDs(scope)
	includeLegacy := workspaceIncludesLegacy(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT t.id,
		       t.function_name,
		       COALESCE(t.schedule_expression, ''),
		       COALESCE(t.resource_name, ''),
		       COALESCE(f.path, ''),
		       f.snapshot_id
		FROM azure_function_triggers t
		JOIN files f ON f.id = t.file_id
		WHERE LOWER(t.trigger_type) = 'timertrigger'
		  AND f.path NOT LIKE '.codebase-snapshots/%'
		  AND ($3::bigint[] IS NULL OR f.snapshot_id = ANY($3) OR ($4::boolean AND f.snapshot_id IS NULL))
		  AND (
		    t.function_name ILIKE $1
		    OR COALESCE(t.schedule_expression, '') ILIKE $1
		    OR COALESCE(t.resource_name, '') ILIKE $1
		  )
		ORDER BY t.function_name, f.path
		LIMIT NULLIF($2, 0)
	`, pattern, limit, snapshotIDs, includeLegacy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScheduleResult
	for rows.Next() {
		var id int64
		var functionName, schedule, resourceName, file string
		var snapshotID *int64
		if err := rows.Scan(&id, &functionName, &schedule, &resourceName, &file, &snapshotID); err != nil {
			return nil, err
		}
		targetName := strings.TrimSpace(resourceName)
		if targetName == "" {
			targetName = functionName
		}
		out = append(out, ScheduleResult{
			ID:                 id,
			RuleName:           functionName,
			ScheduleExpression: schedule,
			TargetType:         "azure_timer",
			TargetName:         targetName,
			State:              "ENABLED",
			Source:             firstNonEmptyString(file, "azure_timer_trigger"),
			SnapshotID:         snapshotID,
		})
	}
	return out, rows.Err()
}

func paginateSearchResults(results []graph.SearchResult, offset, limit int) ([]graph.SearchResult, SearchBucketStats) {
	total := len(results)
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset >= total {
		return nil, SearchBucketStats{
			Returned:   0,
			Total:      total,
			Offset:     offset,
			Limit:      limit,
			HasMore:    false,
			Truncated:  false,
			ExactTotal: true,
		}
	}
	end := total
	if limit > 0 && limit < total-offset {
		end = offset + limit
	}
	return results[offset:end], SearchBucketStats{
		Returned:   end - offset,
		Total:      total,
		Offset:     offset,
		Limit:      limit,
		HasMore:    end < total,
		Truncated:  end < total,
		ExactTotal: true,
	}
}

func paginateScheduleResults(results []ScheduleResult, offset, limit int) ([]ScheduleResult, SearchBucketStats) {
	total := len(results)
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset >= total {
		return nil, SearchBucketStats{
			Returned:   0,
			Total:      total,
			Offset:     offset,
			Limit:      limit,
			HasMore:    false,
			Truncated:  false,
			ExactTotal: true,
		}
	}
	end := total
	if limit > 0 && limit < total-offset {
		end = offset + limit
	}
	return results[offset:end], SearchBucketStats{
		Returned:   end - offset,
		Total:      total,
		Offset:     offset,
		Limit:      limit,
		HasMore:    end < total,
		Truncated:  end < total,
		ExactTotal: true,
	}
}

// ---------------------------------------------------------------------------
// Noise filtering
// ---------------------------------------------------------------------------

var noiseExactNames = map[string]bool{
	"hashCode":  true,
	"equals":    true,
	"toString":  true,
	"compareTo": true,
	"clone":     true,
}

func isNoiseFunction(r graph.SearchResult) bool {
	name := r.Name
	// Strip class prefix: "Foo.hashCode" → "hashCode"
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	nameLower := strings.ToLower(r.Name)
	repoLower := strings.ToLower(r.RepoName)

	// Test repos
	if strings.HasSuffix(repoLower, "-test") || strings.HasSuffix(repoLower, "-tests") {
		return true
	}

	// Exact noise names
	if noiseExactNames[name] {
		return true
	}

	// Simple getters/setters with short bodies
	if isSimpleGetterSetter(name, r.SourceCode) {
		return true
	}

	// Generated file paths
	fp := strings.ToLower(r.FilePath)
	if strings.Contains(fp, "/generated/") ||
		strings.Contains(fp, "/target/generated-sources/") ||
		strings.Contains(fp, "objectfactory") {
		return true
	}
	if looksLikeSyntheticCallback(nameLower) {
		return true
	}
	if isFrontendAsset(fp) && strings.Contains(nameLower, "jquery.") {
		return true
	}

	return false
}

func isSimpleGetterSetter(name string, source string) bool {
	if !isAccessorName(name) {
		return false
	}

	// Short body (<=3 significant lines of code) is treated as low-signal accessor noise.
	if source == "" {
		return false
	}
	return countSignificantCodeLines(source) <= 3
}

func isAccessorName(name string) bool {
	segment := name
	if idx := strings.LastIndex(segment, "."); idx >= 0 {
		segment = segment[idx+1:]
	}
	if len(segment) < 3 {
		return false
	}
	switch {
	case strings.HasPrefix(segment, "get") && len(segment) > 3:
		return isUpperASCII(segment[3])
	case strings.HasPrefix(segment, "set") && len(segment) > 3:
		return isUpperASCII(segment[3])
	case strings.HasPrefix(segment, "is") && len(segment) > 2:
		return isUpperASCII(segment[2])
	case strings.HasPrefix(segment, "has") && len(segment) > 3:
		return isUpperASCII(segment[3])
	default:
		return false
	}
}

func isUpperASCII(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

func countSignificantCodeLines(source string) int {
	lines := strings.Split(source, "\n")
	count := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || t == "{" || t == "}" {
			continue
		}
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "@") {
			continue
		}
		count++
	}
	return count
}

func looksLikeSyntheticCallback(nameLower string) bool {
	if strings.Contains(nameLower, "jquery.") || strings.Contains(nameLower, ".jquery") {
		return true
	}
	if strings.Contains(nameLower, "anonymous") {
		return true
	}
	if strings.Contains(nameLower, "__") && hasGeneratedLineSuffix(nameLower) {
		return true
	}
	return hasGeneratedLineSuffix(nameLower)
}

func hasGeneratedLineSuffix(nameLower string) bool {
	idx := strings.LastIndex(nameLower, "_l")
	if idx < 0 || idx+2 >= len(nameLower) {
		return false
	}
	for _, r := range nameLower[idx+2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isFrontendAsset(pathLower string) bool {
	return strings.HasSuffix(pathLower, ".js") ||
		strings.HasSuffix(pathLower, ".jsx") ||
		strings.HasSuffix(pathLower, ".ts") ||
		strings.HasSuffix(pathLower, ".tsx") ||
		strings.Contains(pathLower, "/src/main/webapp/")
}

// splitSearchTerms splits a multi-word query into individual search terms,
// filtering out common stop words. If the query is a single term or looks like
// a qualified name (contains a dot), it's returned as-is.
func splitSearchTerms(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	// Qualified names like "AuthController.login" should not be split
	if strings.Contains(query, ".") {
		return []string{query}
	}
	words := strings.Fields(strings.ToLower(query))
	if len(words) <= 1 {
		return []string{query}
	}

	stopWords := map[string]bool{
		"what": true, "where": true, "how": true, "does": true, "is": true,
		"which": true, "when": true, "why": true, "who": true,
		"the": true, "a": true, "an": true, "in": true, "of": true,
		"to": true, "for": true, "and": true, "or": true, "this": true,
		"that": true, "it": true, "with": true, "from": true, "by": true,
		"can": true, "you": true, "me": true, "show": true, "find": true,
		"tell": true, "about": true, "work": true, "works": true,
		"are": true, "was": true, "were": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "having": true,
		"do": true, "did": true, "doing": true,
	}

	var terms []string
	seen := make(map[string]bool)
	for _, w := range words {
		w = strings.Trim(w, "?.,!\"'")
		if len(w) < 2 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
	}
	if len(terms) == 0 {
		return []string{query}
	}
	return terms
}

func deduplicateResults(results []graph.SearchResult) []graph.SearchResult {
	type resultKey struct {
		ID                 int64
		Kind, Queue        string
		Producer, Consumer bool
	}
	seen := make(map[resultKey]bool)
	var out []graph.SearchResult
	for _, r := range results {
		key := resultKey{ID: r.ID, Kind: r.Type}
		if r.Extra != nil {
			key.Queue, _ = r.Extra["queue"].(string)
			key.Producer, _ = r.Extra["producer"].(bool)
			key.Consumer, _ = r.Extra["consumer"].(bool)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func deduplicateSchedules(schedules []ScheduleResult) []ScheduleResult {
	type scheduleKey struct {
		ID                 int64
		TargetType, Source string
	}
	seen := make(map[scheduleKey]bool)
	var out []ScheduleResult
	for _, s := range schedules {
		key := scheduleKey{ID: s.ID, TargetType: s.TargetType, Source: s.Source}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// boostMultiTermMatches sorts function results so that items matching more
// search terms appear first. Within the same match count, original order
// (richness-based) is preserved.
func boostMultiTermMatches(terms []string, results []graph.SearchResult) []graph.SearchResult {
	type scored struct {
		result    graph.SearchResult
		termHits  int
		origIndex int
	}
	var items []scored
	for i, r := range results {
		nameLower := strings.ToLower(r.Name)
		repoLower := strings.ToLower(r.RepoName)
		hits := 0
		for _, t := range terms {
			tl := strings.ToLower(t)
			if strings.Contains(nameLower, tl) || strings.Contains(repoLower, tl) {
				hits++
			}
		}
		items = append(items, scored{result: r, termHits: hits, origIndex: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].termHits > items[j].termHits
	})
	out := make([]graph.SearchResult, len(items))
	for i, it := range items {
		out[i] = it.result
	}
	return out
}

// boostMultiTermMatchesGeneric does the same for classes/endpoints.
func boostMultiTermMatchesGeneric(terms []string, results []graph.SearchResult) []graph.SearchResult {
	return boostMultiTermMatches(terms, results)
}

func filterNoise(results []graph.SearchResult) []graph.SearchResult {
	var out []graph.SearchResult
	for _, r := range results {
		if r.Type == "function" && isNoiseFunction(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func filterNoiseEndpoints(results []graph.SearchResult) []graph.SearchResult {
	var out []graph.SearchResult
	for _, r := range results {
		method := ""
		if r.Extra != nil {
			if v, ok := r.Extra["method"].(string); ok {
				method = v
			}
		}
		// File-based legacy routes (.asp, .php) are noise; framework routes and
		// Azure default endpoints can also use REQUEST and should stay visible.
		if strings.EqualFold(method, "REQUEST") && isLegacyFileRoutePath(r.Name) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func isLegacyFileRoutePath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	for _, suffix := range []string{".asp", ".aspx", ".php", ".jsp", ".cfm"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
		if strings.Contains(lower, suffix+"/") {
			return true
		}
	}
	return false
}

func mergeUniqueStrings(base []string, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, item := range append(base, extra...) {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
