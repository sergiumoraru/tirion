package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/sergiumoraru/tirion/internal/graph"
)

type ClassIntegration struct {
	SourceFunction string `json:"sourceFunction"`
	CallerID       string `json:"callerId"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	ClientType     string `json:"clientType,omitempty"`
	TargetRepo     string `json:"targetRepo"`
	TargetFile     string `json:"targetFile"`
	TargetHandler  string `json:"targetHandler,omitempty"`
	LineNumber     int    `json:"lineNumber"`
	Resolution     string `json:"resolution"`
}

type ClassHandledEndpoint struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Handler string `json:"handler,omitempty"`
}

type ClassIntegrationsResponse struct {
	Workspace        ResponseWorkspace      `json:"workspace"`
	RepoContext      []ResponseRepoContext  `json:"repoContext,omitempty"`
	ClassID          int64                  `json:"classId"`
	ClassName        string                 `json:"className"`
	Integrations     []ClassIntegration     `json:"integrations"`
	HandledEndpoints []ClassHandledEndpoint `json:"handledEndpoints"`
	MethodCount      int                    `json:"methodCount"`
}

func (h *Handlers) GetClassIntegrations(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing class id", nil)
		return
	}
	classID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || classID <= 0 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid class id", nil)
		return
	}

	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if len(h.filterClassIDsByWorkspace(r.Context(), []int64{classID}, workspaceScope)) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "class not found in active workspace", nil)
		return
	}

	resp, err := h.loadClassIntegrationsResponse(r.Context(), classID, workspaceScope)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "class not found", nil)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) loadClassIntegrationsResponse(ctx context.Context, classID int64, scopes ...searchWorkspaceScope) (ClassIntegrationsResponse, error) {
	var scope searchWorkspaceScope
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	var className string
	var fileID int64
	var startLine, endLine int
	if err := h.storage.Pool().QueryRow(ctx, `
		SELECT c.name, c.file_id, c.start_line, c.end_line
		FROM classes c
		JOIN files f ON f.id = c.file_id
		WHERE c.id = $1
		  AND ($2::bigint[] IS NULL OR f.snapshot_id = ANY($2) OR ($3 AND f.snapshot_id IS NULL))
	`, classID, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope)).Scan(&className, &fileID, &startLine, &endLine); err != nil {
		return ClassIntegrationsResponse{}, err
	}

	rows, err := h.storage.Pool().Query(ctx, `
		SELECT fn.id, fn.name, COALESCE(r.name || ':' || f.path || ':' || fn.name, '')
		FROM functions fn
		JOIN files f ON fn.file_id = f.id
		JOIN repositories r ON f.repo_id = r.id
		WHERE fn.file_id = $1
		  AND fn.start_line >= $2
		  AND fn.end_line <= $3
		  AND fn.name LIKE $4
		ORDER BY fn.start_line
	`, fileID, startLine, endLine, graph.EscapeLike(className)+".%")
	if err != nil {
		return ClassIntegrationsResponse{}, err
	}
	defer rows.Close()

	type classMethod struct {
		ID       int64
		Name     string
		CallerID string
	}
	methods := make([]classMethod, 0, 16)
	for rows.Next() {
		var item classMethod
		if err := rows.Scan(&item.ID, &item.Name, &item.CallerID); err != nil {
			return ClassIntegrationsResponse{}, err
		}
		if item.CallerID == "" {
			continue
		}
		methods = append(methods, item)
	}
	if err := rows.Err(); err != nil {
		return ClassIntegrationsResponse{}, err
	}
	rows.Close()

	integrations := make([]ClassIntegration, 0, 16)
	seen := make(map[string]struct{})
	for _, method := range methods {
		items, err := h.loadFunctionIntegrationsScoped(ctx, method.CallerID, true, scope)
		if err != nil {
			return ClassIntegrationsResponse{}, err
		}
		for _, item := range items {
			key := strings.Join([]string{method.Name, item.Method, item.Path, item.TargetRepo, item.TargetHandler, item.Resolution}, "|")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			integrations = append(integrations, ClassIntegration{
				SourceFunction: method.Name,
				CallerID:       item.CallerID,
				Method:         item.Method,
				Path:           item.Path,
				ClientType:     item.ClientType,
				TargetRepo:     item.TargetRepo,
				TargetFile:     item.TargetFile,
				TargetHandler:  item.TargetHandler,
				LineNumber:     item.LineNumber,
				Resolution:     item.Resolution,
			})
		}
	}

	sort.Slice(integrations, func(i, j int) bool {
		if integrations[i].TargetRepo == integrations[j].TargetRepo {
			if integrations[i].Path == integrations[j].Path {
				return integrations[i].SourceFunction < integrations[j].SourceFunction
			}
			return integrations[i].Path < integrations[j].Path
		}
		return integrations[i].TargetRepo < integrations[j].TargetRepo
	})

	methodIDs := make([]int64, 0, len(methods))
	for _, method := range methods {
		methodIDs = append(methodIDs, method.ID)
	}

	handledEndpoints := make([]ClassHandledEndpoint, 0, 16)
	if len(methodIDs) > 0 {
		endpointRows, err := h.storage.Pool().Query(ctx, `
			SELECT
				COALESCE(NULLIF(e.method_canonical, ''), e.method) AS method,
				COALESCE(NULLIF(e.path_canonical, ''), e.path) AS path,
				COALESCE(fn.name, '') AS handler
			FROM endpoints e
			LEFT JOIN functions fn ON e.handler_function_id = fn.id
			WHERE e.handler_function_id = ANY($1)
			ORDER BY path, method
		`, methodIDs)
		if err != nil {
			return ClassIntegrationsResponse{}, err
		}
		{
			defer endpointRows.Close()
			seenEndpoints := make(map[string]struct{}, 16)
			for endpointRows.Next() {
				var item ClassHandledEndpoint
				if err := endpointRows.Scan(&item.Method, &item.Path, &item.Handler); err != nil {
					return ClassIntegrationsResponse{}, err
				}
				key := strings.Join([]string{item.Method, item.Path, item.Handler}, "|")
				if _, ok := seenEndpoints[key]; ok {
					continue
				}
				seenEndpoints[key] = struct{}{}
				handledEndpoints = append(handledEndpoints, item)
			}
			if err := endpointRows.Err(); err != nil {
				return ClassIntegrationsResponse{}, err
			}
		}
	}

	return ClassIntegrationsResponse{
		Workspace:        scope.Workspace,
		RepoContext:      scope.RepoContext,
		ClassID:          classID,
		ClassName:        className,
		Integrations:     coalesceClassIntegrations(integrations),
		HandledEndpoints: coalesceClassHandledEndpoints(handledEndpoints),
		MethodCount:      len(methods),
	}, nil
}

func (h *Handlers) filterClassIDsByWorkspace(ctx context.Context, classIDs []int64, scope searchWorkspaceScope) []int64 {
	classIDs = uniqueInt64sPreserveOrder(classIDs)
	if len(classIDs) == 0 || !scope.EnforceSnapshots {
		return classIDs
	}
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT c.id, f.snapshot_id
		FROM classes c
		JOIN files f ON f.id = c.file_id
		WHERE c.id = ANY($1)
	`, classIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()

	allowed := make(map[int64]bool, len(classIDs))
	for rows.Next() {
		var classID int64
		var snapshotID *int64
		if err := rows.Scan(&classID, &snapshotID); err != nil {
			continue
		}
		if workspaceAllowsSnapshot(snapshotID, scope) {
			allowed[classID] = true
		}
	}
	out := make([]int64, 0, len(classIDs))
	for _, classID := range classIDs {
		if allowed[classID] {
			out = append(out, classID)
		}
	}
	return out
}

func coalesceClassIntegrations(items []ClassIntegration) []ClassIntegration {
	if items == nil {
		return []ClassIntegration{}
	}
	return items
}

func coalesceClassHandledEndpoints(items []ClassHandledEndpoint) []ClassHandledEndpoint {
	if items == nil {
		return []ClassHandledEndpoint{}
	}
	return items
}

func uniqueInt64sPreserveOrder(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
