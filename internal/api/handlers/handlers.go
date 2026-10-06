package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/graph"
	"github.com/sergiumoraru/tirion/internal/owners"
)

type Handlers struct {
	storage              *graph.Storage
	dbURL                string
	traceExpandCache     *traceExpandCache
	ownerResolver        *owners.Resolver
	adminAuditCache      adminHealthAudit
	adminAuditFailed     bool
	adminAuditRetryAfter time.Time
	adminAuditBusy       bool
	adminAuditMu         sync.RWMutex
	repoDepsCache        GraphResponse
	repoDepsCacheAt      time.Time
	repoDepsMu           sync.RWMutex
	repoOpMu             sync.Mutex
	repoOpActive         *repoOperationState
	workspaceBulkIndexMu sync.Mutex
	workspaceBulkIndex   *workspaceBulkIndexJob
}

func New(storage *graph.Storage, dbURL string) *Handlers {
	h := &Handlers{
		storage:          storage,
		dbURL:            dbURL,
		traceExpandCache: newTraceExpandCacheFromEnv(),
		ownerResolver:    owners.NewResolver(storage),
	}
	h.queueAdminAuditRefresh()
	return h
}

func (h *Handlers) Close() {
	// No long-running owned resources currently require shutdown.
}

// Fan-in is scoped to the captured selection; a process-lifetime global cache
// would mix workspaces and retain rankings from superseded graph generations.
func (h *Handlers) getRepoFanIn(ctx context.Context, scope searchWorkspaceScope) map[string]int {
	return computeRepoFanIn(ctx, h.storage.Pool(), scope)
}

func computeRepoFanIn(ctx context.Context, pool *pgxpool.Pool, scope searchWorkspaceScope) map[string]int {
	fanIn := make(map[string]int)
	// Filter and deduplicate before looking up targets: fan-in counts source
	// repositories, so repeated calls to the same target add no new evidence.
	// Keep both source and target visibility checks for archived/legacy graphs.
	rows, err := pool.Query(ctx, `WITH selected AS MATERIALIZED (
 SELECT DISTINCT te.callee_repo,te.repo_id,te.callee_function_id
 FROM trace_call_edges te
 WHERE te.callee_repo IS NOT NULL AND te.callee_repo<>'' AND split_part(te.caller_id,':',1)<>te.callee_repo
 AND ($1::bigint[] IS NULL OR te.snapshot_id=ANY($1) OR ($2 AND te.snapshot_id IS NULL))
 )
 SELECT te.callee_repo,COUNT(DISTINCT te.repo_id)
 FROM selected te JOIN functions target ON target.id=te.callee_function_id JOIN files tf ON tf.id=target.file_id
 WHERE ($1::bigint[] IS NULL OR tf.snapshot_id=ANY($1) OR ($2 AND tf.snapshot_id IS NULL))
 GROUP BY te.callee_repo`, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if err != nil {
		log.Printf("WARN: computeRepoFanIn query failed: %v", err)
		return fanIn
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		var count int
		if err := rows.Scan(&repo, &count); err != nil {
			return map[string]int{}
		}
		fanIn[repo] = count
	}
	if err := rows.Err(); err != nil {
		log.Printf("WARN: computeRepoFanIn rows failed: %v", err)
		return map[string]int{}
	}
	return fanIn
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}
