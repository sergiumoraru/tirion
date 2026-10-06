package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/graph"
)

func writeLookupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAmbiguousRepo):
		writeError(w, http.StatusBadRequest, "AMBIGUOUS_REPO", err.Error(), nil)
	case errors.Is(err, pgx.ErrNoRows):
		// Drop the driver's bare "no rows in result set" tail; the wrapped text says what was missing.
		message := strings.TrimSuffix(err.Error(), ": "+pgx.ErrNoRows.Error())
		if message == pgx.ErrNoRows.Error() {
			message = "the requested resource was not found"
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", message, nil)
	case errors.Is(err, graph.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "INVALID_WORKSPACE", err.Error(), nil)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		log.Printf("WARN: lookup did not complete: %v", err)
		writeError(w, http.StatusGatewayTimeout, "LOOKUP_TIMEOUT", "Lookup did not complete", nil)
	default:
		log.Printf("ERROR: lookup failed: %v", err)
		writeError(w, http.StatusServiceUnavailable, "LOOKUP_FAILED", "Could not load the requested resource", nil)
	}
}

// matchingRepos lists the repository names in the selected workspace that match
// a repo filter. The matcher is shared with search and the SQL list filters
// (graph.RepoFilterPattern): case-insensitive exact name, or prefix with a
// trailing "*".
func (h *Handlers) matchingRepos(ctx context.Context, filter string, scope searchWorkspaceScope) ([]string, error) {
	rows, err := h.storage.Pool().Query(ctx, `SELECT r.name FROM repositories r WHERE r.name ILIKE $1 AND (
 NOT $2 OR EXISTS (SELECT 1 FROM workspace_repos wr JOIN workspaces w ON w.id=wr.workspace_id WHERE w.slug=$3 AND wr.repo_name=r.name)
 OR ($4 AND EXISTS(SELECT 1 FROM files f WHERE f.repo_id=r.id AND f.snapshot_id IS NULL)))
 ORDER BY r.name LIMIT 50`, graph.RepoFilterPattern(filter), scope.EnforceSnapshots, scope.Workspace.ID, workspaceIncludesLegacy(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// A named repository filter identifies a resource in the selected workspace.
// An existing repository with no matches still returns an empty successful search.
func (h *Handlers) requireRepo(ctx context.Context, name string, scope searchWorkspaceScope) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	names, err := h.matchingRepos(ctx, name, scope)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("repository %q not found in workspace %q: %w", name, scope.Workspace.ID, pgx.ErrNoRows)
	}
	return nil
}

var errAmbiguousRepo = errors.New("repository name is ambiguous")

// resolveRepoName maps a path-style repo identifier to the stored repository
// name. Matching is case-insensitive; if several repositories differ only by
// case the exact spelling wins, otherwise the caller gets errAmbiguousRepo.
func (h *Handlers) resolveRepoName(ctx context.Context, name string, scope searchWorkspaceScope) (string, error) {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, "*") {
		return "", fmt.Errorf("repository %q: wildcards are not valid here: %w", name, pgx.ErrNoRows)
	}
	names, err := h.matchingRepos(ctx, name, scope)
	if err != nil {
		return "", err
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("repository %q not found in workspace %q: %w", name, scope.Workspace.ID, pgx.ErrNoRows)
	case 1:
		return names[0], nil
	}
	for _, candidate := range names {
		if candidate == name {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("repository %q matches %s: %w", name, strings.Join(names, ", "), errAmbiguousRepo)
}
