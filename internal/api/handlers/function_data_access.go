package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type FunctionDataAccess struct {
	CallerID   string `json:"callerId"`
	EntityName string `json:"entityName"`
	Access     string `json:"access"`
	LineNumber int    `json:"lineNumber"`
}

type FunctionDataAccessResponse struct {
	Workspace   ResponseWorkspace     `json:"workspace"`
	RepoContext []ResponseRepoContext `json:"repoContext,omitempty"`
	CallerID    string                `json:"callerId"`
	Accesses    []FunctionDataAccess  `json:"accesses"`
}

func (h *Handlers) GetFunctionDataAccess(w http.ResponseWriter, r *http.Request) {
	callerID := strings.TrimSpace(r.URL.Query().Get("callerId"))
	if callerID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing callerId", nil)
		return
	}

	_, workspaceScope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if err := h.requireCaller(r.Context(), callerID, workspaceScope); err != nil {
		writeLookupError(w, err)
		return
	}
	query := `
		SELECT caller_id, entity_name, access, COALESCE(line_number, 0)
		FROM data_accesses
		WHERE caller_id = $1
		ORDER BY entity_name, access, line_number
	`
	args := []any{callerID}
	if workspaceScope.EnforceSnapshots {
		ids := activeSnapshotIDs(workspaceScope)
		includeLegacy := workspaceIncludesLegacy(workspaceScope)
		switch {
		case len(ids) > 0 && includeLegacy:
			query = strings.Replace(query, "ORDER BY", "AND (snapshot_id = ANY($2) OR snapshot_id IS NULL)\n\t\tORDER BY", 1)
			args = append(args, ids)
		case len(ids) > 0:
			query = strings.Replace(query, "ORDER BY", "AND snapshot_id = ANY($2)\n\t\tORDER BY", 1)
			args = append(args, ids)
		case includeLegacy:
			query = strings.Replace(query, "ORDER BY", "AND snapshot_id IS NULL\n\t\tORDER BY", 1)
		default:
			query = strings.Replace(query, "ORDER BY", "AND FALSE\n\t\tORDER BY", 1)
		}
	}

	rows, err := h.storage.Pool().Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load function data access", nil)
		return
	}
	defer rows.Close()

	accesses := make([]FunctionDataAccess, 0, 16)
	for rows.Next() {
		var item FunctionDataAccess
		if err := rows.Scan(&item.CallerID, &item.EntityName, &item.Access, &item.LineNumber); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read function data access", nil)
			return
		}
		accesses = append(accesses, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read function data access", nil)
		return
	}

	sort.Slice(accesses, func(i, j int) bool {
		if accesses[i].EntityName == accesses[j].EntityName {
			if accesses[i].Access == accesses[j].Access {
				return accesses[i].LineNumber < accesses[j].LineNumber
			}
			return accesses[i].Access < accesses[j].Access
		}
		return accesses[i].EntityName < accesses[j].EntityName
	})

	writeJSON(w, http.StatusOK, FunctionDataAccessResponse{
		Workspace:   workspaceScope.Workspace,
		RepoContext: workspaceScope.RepoContext,
		CallerID:    callerID,
		Accesses:    accesses,
	})
}

// requireCaller returns pgx.ErrNoRows (-> 404) when callerID names nothing the
// selected workspace knows about: no indexed function or GraphQL usage, and no
// recorded data access or HTTP call made by that id.
func (h *Handlers) requireCaller(ctx context.Context, callerID string, scope searchWorkspaceScope) error {
	var exists bool
	err := h.storage.Pool().QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM functions fn JOIN files f ON f.id=fn.file_id JOIN repositories r ON r.id=f.repo_id
        WHERE r.name=split_part($1,':',1) AND (r.name||':'||f.path||':'||fn.name)=$1
          AND ($2::bigint[] IS NULL OR f.snapshot_id=ANY($2) OR ($3::boolean AND f.snapshot_id IS NULL)))
 OR EXISTS(SELECT 1 FROM graphql_operation_usages u JOIN files f ON f.id=u.file_id JOIN repositories r ON r.id=f.repo_id
        WHERE r.name=split_part($1,':',1) AND (r.name||':'||f.path||':'||u.caller_function)=$1
          AND ($2::bigint[] IS NULL OR f.snapshot_id=ANY($2) OR ($3::boolean AND f.snapshot_id IS NULL)))
 OR EXISTS(SELECT 1 FROM data_accesses d WHERE d.caller_id=$1
          AND ($2::bigint[] IS NULL OR d.snapshot_id=ANY($2) OR ($3::boolean AND d.snapshot_id IS NULL)))
 OR EXISTS(SELECT 1 FROM http_client_calls c WHERE c.caller_id=$1
          AND ($2::bigint[] IS NULL OR c.snapshot_id=ANY($2) OR ($3::boolean AND c.snapshot_id IS NULL)))`,
		callerID, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope)).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("function %q not found in workspace %q: %w", callerID, scope.Workspace.ID, pgx.ErrNoRows)
	}
	return nil
}
