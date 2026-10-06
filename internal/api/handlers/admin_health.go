package handlers

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/audit"
	"github.com/sergiumoraru/tirion/internal/buildinfo"
	"github.com/sergiumoraru/tirion/internal/graph"
)

const (
	recentRepoWindow       = 24 * time.Hour
	staleRepoWindow        = 7 * 24 * time.Hour
	adminAuditRetryBackoff = 2 * time.Minute
)

type adminHealthOverview struct {
	Status          string `json:"status"`
	ServerStatus    string `json:"serverStatus"`
	Repos           int    `json:"repos"`
	Files           int    `json:"files"`
	Functions       int    `json:"functions"`
	Classes         int    `json:"classes"`
	Endpoints       int    `json:"endpoints"`
	RecentRepos     int    `json:"recentRepos"`
	AgingRepos      int    `json:"agingRepos"`
	StaleRepos      int    `json:"staleRepos"`
	LatestIndexedAt string `json:"latestIndexedAt,omitempty"`
	OldestIndexedAt string `json:"oldestIndexedAt,omitempty"`
}

type adminHealthAudit struct {
	Status         string                        `json:"status"`
	Warnings       []string                      `json:"warnings"`
	Metrics        audit.Metrics                 `json:"metrics"`
	UnresolvedHTTP audit.UnresolvedHTTPDrilldown `json:"unresolvedHTTP"`
}

type adminHealthRepo struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Path            string  `json:"path"`
	UpdatedAt       string  `json:"updatedAt"`
	AgeHours        float64 `json:"ageHours"`
	FreshnessStatus string  `json:"freshnessStatus"`
	FileCount       int     `json:"fileCount"`
	FunctionCount   int     `json:"functionCount"`
	ClassCount      int     `json:"classCount"`
	EndpointCount   int     `json:"endpointCount"`
	PrimaryLanguage string  `json:"primaryLanguage"`
}

type adminHealthRefresh struct {
	Status        string `json:"status"`
	Workspace     string `json:"workspace,omitempty"`
	Attempt       int    `json:"attempt"`
	MaxAttempts   int    `json:"maxAttempts"`
	StartedAt     string `json:"startedAt,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorSummary  string `json:"errorSummary,omitempty"`
	Superseded    bool   `json:"superseded,omitempty"`
}

type adminHealthResponse struct {
	GeneratedAt string              `json:"generatedAt"`
	Version     string              `json:"version"`
	Overview    adminHealthOverview `json:"overview"`
	Audit       adminHealthAudit    `json:"audit"`
	Refresh     adminHealthRefresh  `json:"refresh"`
	Warnings    []string            `json:"warnings"`
	Repos       []adminHealthRepo   `json:"repos"`
}

func emptyUnresolvedHTTPDrilldown() audit.UnresolvedHTTPDrilldown {
	return audit.UnresolvedHTTPDrilldown{
		TopRepos: make([]audit.UnresolvedHTTPRepoSummary, 0),
		Samples:  make([]audit.UnresolvedHTTPSample, 0),
	}
}

func (h *Handlers) AdminHealth(w http.ResponseWriter, r *http.Request) {
	_, scope, err := h.resolveWorkspaceScope(graph.DefaultWorkspaceSlug)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	stats, err := h.storage.GetStats(snapshotFilterForScope(scope))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	repoStateRows, err := h.listAdminRepoRows(graph.DefaultWorkspaceSlug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		return
	}

	now := time.Now().UTC()
	repoRows, latestIndexedAt, oldestIndexedAt, recentRepos, agingRepos, staleRepos := buildAdminHealthRepos(repoStateRows, now)
	auditSnapshot, auditReady := h.adminAuditSnapshot(now)
	refreshSnapshot := loadAdminRefreshStatus()
	refreshSnapshot = finalizeAdminRefreshStatus(refreshSnapshot, latestIndexedAt)
	if !auditReady {
		auditSnapshot = adminHealthAudit{
			Status:         "warn",
			Warnings:       []string{"Audit metrics are still computing in the background. This page will populate once the startup audit completes."},
			Metrics:        audit.Metrics{},
			UnresolvedHTTP: emptyUnresolvedHTTPDrilldown(),
		}
	}
	overviewStatus := "ok"
	warnings := make([]string, 0, len(auditSnapshot.Warnings)+1)

	if staleRepos > 0 {
		overviewStatus = "warn"
		warnings = append(warnings, "Some repos have unindexed git changes and should be parsed before relying on cross-repo results.")
	}
	if auditSnapshot.Status == "warn" {
		overviewStatus = "warn"
		warnings = append(warnings, auditSnapshot.Warnings...)
	}
	if refreshSnapshot.Status == "failed" && !refreshSnapshot.Superseded {
		overviewStatus = "warn"
		if refreshSnapshot.Attempt > 0 && refreshSnapshot.MaxAttempts > 0 {
			warnings = append(warnings, "Last refresh failed after "+strconv.Itoa(refreshSnapshot.Attempt)+"/"+strconv.Itoa(refreshSnapshot.MaxAttempts)+" attempts: "+refreshFailureMessage(refreshSnapshot))
		} else if refreshSnapshot.ErrorSummary != "" {
			warnings = append(warnings, "Last refresh failed: "+refreshSnapshot.ErrorSummary)
		} else {
			warnings = append(warnings, "Last refresh failed. Indexed state may be behind the workspace state.")
		}
	}

	response := adminHealthResponse{
		GeneratedAt: now.Format(time.RFC3339),
		Version:     buildinfo.Version,
		Overview: adminHealthOverview{
			Status:       overviewStatus,
			ServerStatus: "ok",
			Repos:        stats.Repos,
			Files:        stats.Files,
			Functions:    stats.Functions,
			Classes:      stats.Classes,
			Endpoints:    stats.Endpoints,
			RecentRepos:  recentRepos,
			AgingRepos:   agingRepos,
			StaleRepos:   staleRepos,
		},
		Audit:    auditSnapshot,
		Refresh:  refreshSnapshot,
		Warnings: warnings,
		Repos:    repoRows,
	}

	if !latestIndexedAt.IsZero() {
		response.Overview.LatestIndexedAt = latestIndexedAt.Format(time.RFC3339)
	}
	if !oldestIndexedAt.IsZero() {
		response.Overview.OldestIndexedAt = oldestIndexedAt.Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, response)
}

func loadAdminRefreshStatus() adminHealthRefresh {
	path := strings.TrimSpace(os.Getenv("TIRION_REFRESH_STATUS_FILE"))
	if path == "" {
		path = "/var/log/tirion/refresh-status.env"
	}

	file, err := os.Open(path)
	if err != nil {
		return adminHealthRefresh{Status: "unknown"}
	}
	defer file.Close()

	status := adminHealthRefresh{Status: "unknown"}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "STATUS":
			status.Status = value
		case "WORKSPACE":
			status.Workspace = value
		case "ATTEMPT":
			status.Attempt, _ = strconv.Atoi(value)
		case "MAX_ATTEMPTS":
			status.MaxAttempts, _ = strconv.Atoi(value)
		case "STARTED_AT":
			status.StartedAt = value
		case "UPDATED_AT":
			status.UpdatedAt = value
		case "LAST_SUCCESS_AT":
			status.LastSuccessAt = value
		case "ERROR":
			status.Error = value
		}
	}

	if status.Status == "" {
		status.Status = "unknown"
	}
	return status
}

func finalizeAdminRefreshStatus(status adminHealthRefresh, latestIndexedAt time.Time) adminHealthRefresh {
	status.ErrorSummary = summarizeRefreshError(status.Error)
	if status.Status != "failed" || latestIndexedAt.IsZero() {
		return status
	}

	reference := parseRefreshTimestamp(status.UpdatedAt)
	if reference.IsZero() {
		reference = parseRefreshTimestamp(status.StartedAt)
	}
	if reference.IsZero() {
		return status
	}

	if latestIndexedAt.After(reference) {
		status.Superseded = true
		if status.LastSuccessAt == "" {
			status.LastSuccessAt = latestIndexedAt.Format(time.RFC3339)
		}
	}

	return status
}

func parseRefreshTimestamp(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func summarizeRefreshError(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	normalized := strings.Join(strings.Fields(trimmed), " ")
	for _, marker := range []string{
		"extract-spring failed:",
		"extract-java-calls failed:",
		"extract-java-intel failed:",
		"extract-sqs failed:",
		"extract-http failed:",
		"index failed:",
	} {
		if idx := strings.LastIndex(normalized, marker); idx >= 0 {
			return clampRefreshSummary(normalized[idx:])
		}
	}

	return clampRefreshSummary(normalized)
}

func clampRefreshSummary(raw string) string {
	const maxLen = 180
	if len(raw) <= maxLen {
		return raw
	}
	return strings.TrimSpace(raw[:maxLen-1]) + "…"
}

func refreshFailureMessage(status adminHealthRefresh) string {
	if status.ErrorSummary != "" {
		return status.ErrorSummary
	}
	if status.Error != "" {
		return summarizeRefreshError(status.Error)
	}
	return "Indexed state may be behind the workspace state."
}

func buildAdminHealthRepos(repos []adminRepoRow, now time.Time) ([]adminHealthRepo, time.Time, time.Time, int, int, int) {
	rows := make([]adminHealthRepo, 0, len(repos))
	var latestIndexedAt time.Time
	var oldestIndexedAt time.Time
	var recentRepos int
	var agingRepos int
	var staleRepos int

	for _, repo := range repos {
		indexedAt := parseAdminRepoTimestamp(repo.UpdatedAt)
		age := time.Duration(0)
		if !indexedAt.IsZero() {
			age = now.Sub(indexedAt.UTC())
		}
		status := adminHealthRepoFreshnessStatus(repo, age)
		switch status {
		case "recent":
			recentRepos++
		case "aging":
			agingRepos++
		default:
			staleRepos++
		}

		if !indexedAt.IsZero() && (latestIndexedAt.IsZero() || indexedAt.After(latestIndexedAt)) {
			latestIndexedAt = indexedAt
		}
		if !indexedAt.IsZero() && (oldestIndexedAt.IsZero() || indexedAt.Before(oldestIndexedAt)) {
			oldestIndexedAt = indexedAt
		}

		rows = append(rows, adminHealthRepo{
			ID:              repo.ID,
			Name:            repo.Name,
			Path:            repo.Path,
			UpdatedAt:       repo.UpdatedAt,
			AgeHours:        age.Hours(),
			FreshnessStatus: status,
			FileCount:       repo.FileCount,
			FunctionCount:   repo.FunctionCount,
			ClassCount:      repo.ClassCount,
			EndpointCount:   repo.EndpointCount,
			PrimaryLanguage: repo.PrimaryLanguage,
		})
	}

	return rows, latestIndexedAt.UTC(), oldestIndexedAt.UTC(), recentRepos, agingRepos, staleRepos
}

func adminHealthRepoFreshnessStatus(repo adminRepoRow, age time.Duration) string {
	if repo.ReparseNeeded || repo.BranchDrift || strings.TrimSpace(repo.DriftStatus) == "drift" {
		return "stale"
	}
	status := strings.TrimSpace(repo.FreshnessStatus)
	if status == "" {
		return repoFreshnessStatus(age)
	}
	return status
}

func parseAdminRepoTimestamp(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func repoFreshnessStatus(age time.Duration) string {
	if age <= recentRepoWindow {
		return "recent"
	}
	if age <= staleRepoWindow {
		return "aging"
	}
	return "stale"
}

func buildAdminAuditStatus(metrics audit.Metrics) (string, []string) {
	var warnings []string
	if metrics.DuplicateEndpointGroups > 0 {
		warnings = append(warnings, "Duplicate endpoint groups are present and can create misleading HTTP surface results.")
	}
	if metrics.UnresolvedHTTPCalls > 0 {
		warnings = append(warnings, "Some internal HTTP client calls still have unresolved route matches against indexed endpoints.")
	}
	if metrics.QueueProducerCount > 0 && metrics.QueueMatchRate < 90 {
		warnings = append(warnings, "Queue producer to consumer match coverage is below 90%.")
	}
	if metrics.DataSharedEntityCount > 0 && metrics.DataCoverageRate < 60 {
		warnings = append(warnings, "Cross-repo data access coverage is below 60% for shared entities.")
	}
	if len(warnings) == 0 {
		return "ok", nil
	}
	return "warn", warnings
}

func (h *Handlers) adminAuditSnapshot(now time.Time) (adminHealthAudit, bool) {
	h.adminAuditMu.RLock()
	snapshot := h.adminAuditCache
	failed := h.adminAuditFailed
	retryAfter := h.adminAuditRetryAfter
	busy := h.adminAuditBusy
	h.adminAuditMu.RUnlock()

	if snapshot.Status != "" {
		if failed && !busy && (retryAfter.IsZero() || !now.Before(retryAfter)) {
			h.queueAdminAuditRefresh()
		}
		return snapshot, true
	}

	if !busy {
		h.queueAdminAuditRefresh()
	}
	return adminHealthAudit{}, false
}

func (h *Handlers) queueAdminAuditRefresh() {
	h.adminAuditMu.Lock()
	if h.adminAuditBusy {
		h.adminAuditMu.Unlock()
		return
	}
	h.adminAuditBusy = true
	h.adminAuditMu.Unlock()

	go h.refreshAdminAuditCache()
}

func (h *Handlers) refreshAdminAuditCache() {
	snapshot, err := h.collectAdminAuditSnapshot(context.Background(), 10*time.Minute)
	if err != nil {
		log.Printf("WARN: refresh admin audit cache: %v", err)
		h.adminAuditMu.Lock()
		if h.adminAuditCache.Status == "" {
			message := "Audit metrics refresh failed."
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
				message = "Audit metrics refresh timed out on this dataset. Increase the audit timeout or retry after indexing settles."
			}
			h.adminAuditCache = adminHealthAudit{
				Status:         "warn",
				Warnings:       []string{message},
				Metrics:        audit.Metrics{},
				UnresolvedHTTP: emptyUnresolvedHTTPDrilldown(),
			}
		}
		h.adminAuditFailed = true
		h.adminAuditRetryAfter = time.Now().Add(adminAuditRetryBackoff)
		h.adminAuditBusy = false
		h.adminAuditMu.Unlock()
		return
	}

	h.adminAuditMu.Lock()
	h.adminAuditCache = snapshot
	h.adminAuditFailed = false
	h.adminAuditRetryAfter = time.Time{}
	h.adminAuditBusy = false
	h.adminAuditMu.Unlock()
}

func (h *Handlers) collectAdminAuditSnapshot(parent context.Context, timeout time.Duration) (adminHealthAudit, error) {
	auditCtx, auditCancel := context.WithTimeout(parent, timeout)
	defer auditCancel()

	_, scope, err := h.resolveWorkspaceScope(graph.DefaultWorkspaceSlug)
	if err != nil {
		return adminHealthAudit{}, err
	}
	metrics, drilldown, err := audit.CollectHTTPAudit(auditCtx, h.storage.Pool(), 8, 12, snapshotFilterForScope(scope))
	if err != nil {
		return adminHealthAudit{}, err
	}

	status, warnings := buildAdminAuditStatus(metrics)
	return adminHealthAudit{
		Status:         status,
		Warnings:       warnings,
		Metrics:        metrics,
		UnresolvedHTTP: drilldown,
	}, nil
}
