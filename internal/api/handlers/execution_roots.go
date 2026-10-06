package handlers

import (
	"context"
	"strings"

	"github.com/sergiumoraru/tirion/internal/trace"
)

type executionRoot struct {
	Endpoint FlowEndpoint
	CallerID string
}

type executionRootResolution struct {
	Roots          []executionRoot
	TraceMatchPath string
}

func (r executionRootResolution) endpoints() []FlowEndpoint {
	out := make([]FlowEndpoint, 0, len(r.Roots))
	for _, root := range r.Roots {
		out = append(out, root.Endpoint)
	}
	return out
}

func (r executionRootResolution) callerIDs() []string {
	out := make([]string, 0, len(r.Roots))
	for _, root := range r.Roots {
		out = append(out, root.CallerID)
	}
	return out
}

func resolveExecutionRoots(ctx context.Context, h *Handlers, rawStart string, scope searchWorkspaceScope) executionRootResolution {
	rawStart = strings.TrimSpace(rawStart)
	if rawStart == "" {
		return executionRootResolution{}
	}

	forwardedStartPath := extractForwardedPathFromQuery(rawStart)
	startMode, start := parseFlowStartMode(rawStart)
	startMethod, startPath, hasPath := parseFlowStart(start)
	traceMatchPath := ""
	if hasPath {
		if forwardedStartPath != "" {
			startPath = forwardedStartPath
		} else if forwarded := extractForwardedPathFromQuery(startPath); forwarded != "" {
			startPath = forwarded
		}
		startPath = strings.TrimSpace(startPath)
		traceMatchPath = startPath
		if idx := strings.Index(traceMatchPath, "?"); idx >= 0 {
			traceMatchPath = traceMatchPath[:idx]
		}
		traceMatchPath = strings.TrimSpace(traceMatchPath)
		if len(traceMatchPath) > 1 {
			traceMatchPath = strings.TrimSuffix(traceMatchPath, "/")
		}
	}

	roots := make([]executionRoot, 0)
	seen := make(map[string]bool)
	appendRoots := func(endpoints []FlowEndpoint, callerIDs []string) {
		for i, callerID := range callerIDs {
			if callerID == "" {
				continue
			}
			ep := FlowEndpoint{}
			if i < len(endpoints) {
				ep = endpoints[i]
			}
			key := callerID + "|" + flowEndpointCanonicalIdentityKey(ep)
			if seen[key] {
				continue
			}
			seen[key] = true
			roots = append(roots, executionRoot{
				Endpoint: ep,
				CallerID: callerID,
			})
		}
	}

	switch startMode {
	case "queue":
		r, ids := findQueueRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	case "job":
		r, ids := findConsumerRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	case "scheduled":
		r, ids := findScheduledRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	case "eventbridge":
		r, ids := findEventBridgeRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	case "graphql":
		r, ids := findGraphQLOperationRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	case "azure":
		r, ids := findAzureFunctionRoots(ctx, h.storage.Pool(), start, scope)
		appendRoots(r, ids)
	}

	if len(roots) == 0 && hasPath {
		for _, pathVariant := range flowStartPathVariants(startPath) {
			r, ids := findEndpointRoots(ctx, h.storage.Pool(), startMethod, pathVariant, scope)
			appendRoots(r, ids)
		}
	}
	if len(roots) == 0 && hasPath {
		variants := flowStartPathVariants(startPath)
		if traceMatchPath != "" && traceMatchPath != startPath {
			variants = append(variants, traceMatchPath)
		}
		for _, pathVariant := range variants {
			r, ids := findEndpointRootsWithTraceMatch(trace.WithContext(ctx, h.storage.Pool()), startMethod, pathVariant, scope)
			appendRoots(r, ids)
		}
	}
	if len(roots) == 0 && hasPath {
		variants := flowStartPathVariants(startPath)
		if traceMatchPath != "" && traceMatchPath != startPath {
			variants = append(variants, traceMatchPath)
		}
		for _, pathVariant := range variants {
			r, ids := findAzureFunctionRoots(ctx, h.storage.Pool(), pathVariant, scope)
			appendRoots(r, ids)
		}
	}
	if len(roots) == 0 {
		for _, callerID := range trace.FindCallerIDsForSnapshots(trace.WithContext(ctx, h.storage.Pool()), start, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope)) {
			repo, file, handler := trace.ParseCallerID(callerID)
			if repo == "" || file == "" || handler == "" {
				continue
			}
			appendRoots([]FlowEndpoint{{
				Repo:    repo,
				Handler: handler,
				File:    file,
			}}, []string{callerID})
		}
	}
	if len(roots) == 0 && startMode == "" {
		if r, ids := findConsumerRoots(ctx, h.storage.Pool(), start, scope); len(ids) > 0 {
			appendRoots(r, ids)
		}
	}
	if len(roots) == 0 && startMode == "" {
		if r, ids := findScheduledRoots(ctx, h.storage.Pool(), start, scope); len(ids) > 0 {
			appendRoots(r, ids)
		}
	}

	return executionRootResolution{
		Roots:          roots,
		TraceMatchPath: traceMatchPath,
	}
}
