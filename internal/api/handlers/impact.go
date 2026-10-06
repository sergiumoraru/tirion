package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/diffparse"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type ImpactFile struct {
	Repo   string        `json:"repo,omitempty"`
	Path   string        `json:"path"`
	Ranges []ImpactRange `json:"ranges,omitempty"`
}

type ImpactRange struct {
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Start     int    `json:"start,omitempty"`
	End       int    `json:"end,omitempty"`
}

type ImpactRequest struct {
	diffCoverage  []string
	diffNotes     []string
	diffOnlyAdded bool
	Functions     []string             `json:"functions"`
	CallerIDs     []string             `json:"callerIds"`
	FunctionIDs   []int64              `json:"functionIds"`
	Files         []ImpactFile         `json:"files"`
	Ranges        []ImpactRange        `json:"ranges"`
	Diff          string               `json:"diff,omitempty"`
	Repo          string               `json:"repo,omitempty"`
	BaseSHA       string               `json:"baseSha,omitempty"`
	Anchor        *VerifyAnchorInput   `json:"anchor,omitempty"`
	Waivers       []VerifyWaiverInput  `json:"waivers,omitempty"`
	RequireAnchor bool                 `json:"requireAnchor,omitempty"`
	WorkspaceID   string               `json:"workspaceId,omitempty"`
	Profile       string               `json:"profile,omitempty"`
	Depth         int                  `json:"depth"`
	NoTests       bool                 `json:"noTests"`
	Resolve       *bool                `json:"resolve"`
	Exclude       []string             `json:"exclude"`
	IncludeRepos  []string             `json:"includeRepos"`
	ExcludeRepos  []string             `json:"excludeRepos"`
	MaxNodes      int                  `json:"maxNodes"`
	IncludeTrace  bool                 `json:"includeTrace"`
	Verify        bool                 `json:"verify,omitempty"`
	AgentWork     *AgentWorkSubmission `json:"agentWork,omitempty"`
}

var impactHunkRegex = regexp.MustCompile(`@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type ImpactStats struct {
	Roots           int    `json:"roots"`
	DownstreamNodes int    `json:"downstreamNodes"`
	UpstreamNodes   int    `json:"upstreamNodes"`
	TotalNodes      int    `json:"totalNodes"`
	TotalEdges      int    `json:"totalEdges"`
	ImpactTime      string `json:"impactTime"`
}

type ImpactDirectionCompleteness = TraceDirectionCompleteness

type ImpactCompleteness struct {
	AppliedMaxNodes   int                         `json:"appliedMaxNodes"`
	Truncated         bool                        `json:"truncated"`
	TruncationReasons []string                    `json:"truncationReasons,omitempty"`
	Downstream        ImpactDirectionCompleteness `json:"downstream"`
	Upstream          ImpactDirectionCompleteness `json:"upstream"`
}

type ImpactEndpoint struct {
	Method           string   `json:"method"`
	Path             string   `json:"path"`
	Handler          string   `json:"handler"`
	Repo             string   `json:"repo"`
	File             string   `json:"file"`
	Line             int      `json:"line"`
	MinDepth         int      `json:"minDepth"`
	DirectlyAffected bool     `json:"directlyAffected"`
	Fanout           int      `json:"fanout"`
	Score            float64  `json:"score"`
	Owners           []string `json:"owners,omitempty"`
	OwnerSource      string   `json:"ownerSource,omitempty"`
}

type ImpactHttpCall struct {
	Method           string            `json:"method"`
	Path             string            `json:"path"`
	MinDepth         int               `json:"minDepth"`
	DirectlyAffected bool              `json:"directlyAffected"`
	Fanout           int               `json:"fanout"`
	Score            float64           `json:"score"`
	Matches          []ImpactHttpMatch `json:"matches,omitempty"`
}

type ImpactHttpMatch struct {
	Repo    string `json:"repo"`
	Handler string `json:"handler"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
}

type ImpactQueue struct {
	Name             string   `json:"name"`
	MinDepth         int      `json:"minDepth"`
	DirectlyAffected bool     `json:"directlyAffected"`
	Fanout           int      `json:"fanout"`
	Score            float64  `json:"score"`
	Consumers        []string `json:"consumers,omitempty"`
}

type ImpactRepo struct {
	Name             string  `json:"name"`
	MinDepth         int     `json:"minDepth"`
	DirectlyAffected bool    `json:"directlyAffected"`
	Fanout           int     `json:"fanout"`
	Score            float64 `json:"score"`
}

type ImpactSchedule struct {
	Name             string  `json:"name"`
	Schedule         string  `json:"schedule"`
	State            string  `json:"state"`
	MinDepth         int     `json:"minDepth"`
	DirectlyAffected bool    `json:"directlyAffected"`
	Fanout           int     `json:"fanout"`
	Score            float64 `json:"score"`
}

type ImpactDataAccess struct {
	Entity           string   `json:"entity"`
	Access           string   `json:"access"`
	MinDepth         int      `json:"minDepth"`
	DirectlyAffected bool     `json:"directlyAffected"`
	Fanout           int      `json:"fanout"`
	Score            float64  `json:"score"`
	Callers          []string `json:"callers,omitempty"`
	Files            []string `json:"files,omitempty"`
}

type ImpactSummary struct {
	Entrypoints          []ImpactEndpoint   `json:"entrypoints"`
	HttpCalls            []ImpactHttpCall   `json:"httpCalls"`
	Queues               []ImpactQueue      `json:"queues"`
	DataAccesses         []ImpactDataAccess `json:"dataAccesses,omitempty"`
	EventBridgeSchedules []ImpactSchedule   `json:"eventBridgeSchedules,omitempty"`
	AzureTimerSchedules  []ImpactSchedule   `json:"azureTimerSchedules,omitempty"`
	Repos                []ImpactRepo       `json:"repos"`
}

type ImpactNode struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Queue  string `json:"queue,omitempty"`
}

type ImpactEdge struct {
	Source     string              `json:"source"`
	Target     string              `json:"target"`
	Type       string              `json:"type"`
	Confidence string              `json:"confidence,omitempty"`
	Evidence   *trace.EdgeEvidence `json:"evidence,omitempty"`
}

type ImpactReport struct {
	Nodes []ImpactNode `json:"nodes"`
	Edges []ImpactEdge `json:"edges"`
}

type ImpactRoot struct {
	CallerID string `json:"callerId"`
	Repo     string `json:"repo,omitempty"`
	File     string `json:"file,omitempty"`
	Name     string `json:"name,omitempty"`
}

type ImpactResponse struct {
	Workspace      ResponseWorkspace     `json:"workspace"`
	RepoContext    []ResponseRepoContext `json:"repoContext,omitempty"`
	Roots          []string              `json:"roots"`
	RootDetails    []ImpactRoot          `json:"rootDetails"`
	Downstream     []*trace.TreeNode     `json:"downstream,omitempty"`
	Upstream       []*trace.TreeNode     `json:"upstream,omitempty"`
	Summary        ImpactSummary         `json:"summary"`
	Report         ImpactReport          `json:"report"`
	Stats          ImpactStats           `json:"stats"`
	Completeness   ImpactCompleteness    `json:"completeness"`
	Verify         *EstateVerifyResult   `json:"verify,omitempty"`
	WorkspaceHints []WorkspaceHint       `json:"workspaceHints,omitempty"`
	Warnings       []string              `json:"warnings,omitempty"`
}

func (h *Handlers) Impact(w http.ResponseWriter, r *http.Request) {
	var req ImpactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body", nil)
		return
	}
	if r.URL.Path == "/api/verify" {
		req.Verify = true
		if strings.TrimSpace(req.Repo) == "" || strings.TrimSpace(req.Diff) == "" {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "repo and diff are required for change verification", nil)
			return
		}
		// Impact hints are not proof that the diff changed these symbols.
		req.Functions, req.CallerIDs, req.FunctionIDs = nil, nil, nil
		req.Files, req.Ranges = nil, nil
	} else {
		req.Verify = false
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		req.WorkspaceID = workspaceIDFromRequest(r)
	}
	_, workspaceScope, err := h.resolveWorkspaceScope(req.WorkspaceID, r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	cfg := config.GetEffectiveTraceConfig()
	profile, _ := cfg.ResolveProfile(req.Profile)

	if req.Depth > trace.MaxDepth {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "depth must not exceed 32", nil)
		return
	}
	depth := req.Depth
	if depth <= 0 {
		if profile.Depth != nil && *profile.Depth > 0 {
			depth = *profile.Depth
		} else {
			depth = 4
		}
	}

	depth = min(depth, trace.MaxDepth)
	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		if profile.MaxNodes != nil && *profile.MaxNodes > 0 {
			maxNodes = *profile.MaxNodes
		} else {
			maxNodes = 2000
		}
	}
	if maxNodes > 10000 {
		maxNodes = 10000
	}
	req.Depth = depth
	req.MaxNodes = maxNodes

	resolve := false
	if profile.Resolve != nil {
		resolve = *profile.Resolve
	}
	if req.Resolve != nil {
		resolve = *req.Resolve
	}
	tuning := trace.NewTraceTuning(profile, depth)
	tuning.SnapshotIDs = activeSnapshotIDs(workspaceScope)
	tuning.IncludeLegacySnapshots = workspaceIncludesLegacy(workspaceScope)
	diffWarnings := []string{}
	// Diff, file, and range mapping compare the stored repository name exactly, so
	// a case variant must be canonicalized here rather than only validated.
	if strings.TrimSpace(req.Repo) != "" {
		canonical, err := h.resolveRepoName(r.Context(), req.Repo, workspaceScope)
		if err != nil {
			writeLookupError(w, err)
			return
		}
		req.Repo = canonical
	}
	if !req.Verify && len(req.Functions) == 0 && len(req.CallerIDs) == 0 && len(req.FunctionIDs) == 0 &&
		len(req.Files) == 0 && len(req.Ranges) == 0 && strings.TrimSpace(req.Diff) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "missing impact targets", nil)
		return
	}

	var callerIDs []string
	if req.Verify {
		resolution, err := h.resolveDiffChangedSymbols(r.Context(), req, workspaceScope)
		if err != nil {
			writeDiffResolutionError(w, "VERIFY_INCOMPLETE", err)
			return
		}
		callerIDs, req.diffCoverage, req.diffNotes, req.diffOnlyAdded = resolution.CallerIDs, resolution.Gaps, resolution.Notes, resolution.OnlyAdded
		for _, file := range resolution.AddedUnchecked {
			req.diffCoverage = append(req.diffCoverage, "added file "+file+" is not indexed yet; its code was not checked")
		}
		diffWarnings = append(diffWarnings, req.diffCoverage...)
		diffWarnings = append(diffWarnings, req.diffNotes...)
	} else {
		var err error
		callerIDs, err = resolveImpactCallerIDs(r.Context(), h, req, workspaceScope)
		if err != nil {
			writeLookupError(w, err)
			return
		}
		if strings.TrimSpace(req.Diff) != "" {
			resolution, err := h.resolveDiffChangedSymbols(r.Context(), req, workspaceScope)
			if err != nil {
				writeDiffResolutionError(w, "IMPACT_INCOMPLETE", err)
				return
			}
			callerIDs = uniqueNonEmptyStrings(append(callerIDs, resolution.CallerIDs...))
			diffWarnings = append(diffWarnings, resolution.Gaps...)
			diffWarnings = append(diffWarnings, resolution.Notes...)
			for _, file := range resolution.AddedUnchecked {
				diffWarnings = append(diffWarnings, "added file "+file+" not indexed yet; it has no indexed consumers (informational)")
			}
		}
		callerIDs = h.filterCallerIDsByWorkspace(r.Context(), callerIDs, workspaceScope)
	}
	if len(callerIDs) == 0 {
		var verify *EstateVerifyResult
		if req.Verify {
			verifyStart := time.Now()
			verify = buildEmptyEstateVerify(maxNodes, diffWarnings, "no matching functions for impact targets in active workspace")
			if req.diffOnlyAdded && len(req.diffCoverage) == 0 {
				// Newly created files have no indexed preimage and no consumers yet;
				// that alone is not an incomplete check; the note stays informational.
				verify.Verdict = "pass"
				verify.Reasons = uniqueNonEmptyStrings(append([]string{}, req.diffNotes...))
			}
			verify.AgentWork = h.validateAgentWork(r.Context(), req, nil, ImpactReport{}, workspaceScope)
			verify.ChangeValidation = h.validateAgentChange(r.Context(), req, nil, workspaceScope)
			if verify.ChangeValidation != nil {
				verify.Mode = verify.ChangeValidation.Mode
				reasons := append([]string{}, verify.Reasons...)
				applyAgentChangeVerdict(verify, &reasons)
				if req.Anchor == nil && verify.AgentWork != nil {
					applyAgentWorkVerdict(verify, &reasons)
				}
				verify.Reasons = uniqueNonEmptyStrings(reasons)
			}
			h.recordEstateVerifyRun(r.Context(), req, workspaceScope, verify, time.Since(verifyStart))
		}
		writeJSON(w, http.StatusOK, ImpactResponse{
			Workspace:      workspaceScope.Workspace,
			RepoContext:    workspaceScope.RepoContext,
			Roots:          []string{},
			RootDetails:    []ImpactRoot{},
			Summary:        ImpactSummary{},
			Report:         ImpactReport{},
			WorkspaceHints: impactWorkspaceHints(r.Context(), h, req, workspaceScope),
			Warnings:       mergeUniqueStrings([]string{"no matching functions for impact targets in active workspace"}, diffWarnings),
			Verify:         verify,
			Completeness: ImpactCompleteness{
				AppliedMaxNodes: maxNodes,
			},
		})
		return
	}

	start := time.Now()
	filters := trace.BuildFilters(req.NoTests, req.Exclude, req.IncludeRepos, req.ExcludeRepos)

	downstream, downstreamLimit := trace.TraceDownstreamWithLimitAndResolveTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), callerIDs, depth, maxNodes, resolve, tuning)
	downstream = trace.FilterTree(downstream, filters)
	downstream = h.filterTraceTreeByWorkspace(r.Context(), downstream, workspaceScope)
	downstream, downstreamPrune := pruneTreeWithStats(downstream, maxNodes)

	upstream, _, upstreamLimit := trace.TraceUpstreamWithModeAndLimitTuningStats(trace.WithContext(r.Context(), h.storage.Pool()), "", callerIDs, depth, maxNodes, tuning)
	upstream = trace.FilterTree(upstream, filters)
	upstream = h.filterTraceTreeByWorkspace(r.Context(), upstream, workspaceScope)
	upstream, upstreamPrune := pruneTreeWithStats(upstream, maxNodes)

	if r.Context().Err() != nil {
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Impact time limit exceeded", nil)
		return
	}
	builder := newImpactBuilder()
	builder.ingestTrees(downstream, "downstream")
	builder.ingestTrees(upstream, "upstream")
	entrypoints, err := builder.resolveEntrypoints(r.Context(), h.storage.Pool(), workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "IMPACT_INCOMPLETE", "could not resolve affected entrypoints", nil)
		return
	}
	rootEntrypoints, err := resolveRootEntrypoints(r.Context(), h.storage.Pool(), callerIDs, workspaceScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "IMPACT_INCOMPLETE", "could not resolve root entrypoints", nil)
		return
	}
	entrypoints = mergeEntrypoints(entrypoints, rootEntrypoints)

	summary := builder.buildSummary(trace.WithContext(r.Context(), h.storage.Pool()), entrypoints, activeSnapshotIDs(workspaceScope), workspaceIncludesLegacy(workspaceScope))
	if err := r.Context().Err(); err != nil {
		writeError(w, http.StatusGatewayTimeout, "IMPACT_INCOMPLETE", err.Error(), nil)
		return
	}
	report := builder.buildReport()
	stats := ImpactStats{
		Roots:           len(callerIDs),
		DownstreamNodes: trace.CountNodes(downstream),
		UpstreamNodes:   trace.CountNodes(upstream),
		TotalNodes:      len(report.Nodes),
		TotalEdges:      len(report.Edges),
		ImpactTime:      time.Since(start).String(),
	}
	completeness := buildImpactCompleteness(maxNodes, downstreamLimit, downstreamPrune, upstreamLimit, upstreamPrune)

	resp := ImpactResponse{
		Workspace:      workspaceScope.Workspace,
		RepoContext:    workspaceScope.RepoContext,
		Roots:          callerIDs,
		RootDetails:    impactRootsFromCallerIDs(callerIDs),
		Summary:        summary,
		Report:         report,
		Stats:          stats,
		Completeness:   completeness,
		WorkspaceHints: impactWorkspaceHints(r.Context(), h, req, workspaceScope),
		Warnings:       mergeUniqueStrings(buildImpactWarnings(completeness), diffWarnings),
	}
	if req.Verify {
		verifyStart := time.Now()
		resp.Verify = h.buildEstateVerify(r.Context(), req, callerIDs, rootEntrypoints, summary, report, completeness, workspaceScope)
		h.recordEstateVerifyRun(r.Context(), req, workspaceScope, resp.Verify, time.Since(verifyStart))
	}
	if req.IncludeTrace {
		resp.Downstream = downstream
		resp.Upstream = upstream
	}
	writeJSON(w, http.StatusOK, resp)
}

func impactRootsFromCallerIDs(callerIDs []string) []ImpactRoot {
	if len(callerIDs) == 0 {
		return []ImpactRoot{}
	}
	out := make([]ImpactRoot, 0, len(callerIDs))
	for _, callerID := range callerIDs {
		callerID = strings.TrimSpace(callerID)
		if callerID == "" {
			continue
		}
		repo, file, name := trace.ParseCallerID(callerID)
		out = append(out, ImpactRoot{
			CallerID: callerID,
			Repo:     repo,
			File:     file,
			Name:     name,
		})
	}
	return out
}

// resolveImpactCallerIDs maps explicit targets to caller IDs. File and range
// entries default to the request's (already canonical) top-level repo; an entry's
// own repo is resolved to its stored name the same way. Without any repo, a path
// matches every repository in the workspace that indexes it.
func resolveImpactCallerIDs(ctx context.Context, h *Handlers, req ImpactRequest, scope searchWorkspaceScope) ([]string, error) {
	canonical := map[string]string{}
	repoFor := func(entryRepo string) (string, error) {
		entryRepo = strings.TrimSpace(entryRepo)
		if entryRepo == "" {
			return req.Repo, nil
		}
		if name, ok := canonical[entryRepo]; ok {
			return name, nil
		}
		name, err := h.resolveRepoName(ctx, entryRepo, scope)
		if err != nil {
			return "", err
		}
		canonical[entryRepo] = name
		return name, nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range req.CallerIDs {
		add(id)
	}
	for _, id := range trace.FindCallerIDsByFunctionIDs(trace.WithContext(ctx, h.storage.Pool()), req.FunctionIDs) {
		add(id)
	}
	for _, name := range req.Functions {
		ids := trace.FindCallerIDsForSnapshots(trace.WithContext(ctx, h.storage.Pool()), name, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
		if len(ids) == 0 {
			if method, path, ok := parseFlowStart(name); ok {
				if idx := strings.Index(path, "?"); idx >= 0 {
					path = path[:idx]
				}
				path = strings.TrimSpace(path)
				if len(path) > 1 {
					path = strings.TrimSuffix(path, "/")
				}
				_, endpointCallerIDs := findEndpointRoots(ctx, h.storage.Pool(), method, path, scope)
				ids = endpointCallerIDs
			}
		}
		for _, id := range ids {
			add(id)
		}
	}
	addRange := func(r ImpactRange) error {
		repo, err := repoFor(r.Repo)
		if err != nil {
			return err
		}
		funcs, err := h.storage.FunctionsByFileRange(repo, r.Path, r.StartLine, r.EndLine, snapshotFilterForScope(scope))
		if err != nil {
			return fmt.Errorf("functions for %s:%d-%d: %w", r.Path, r.StartLine, r.EndLine, err)
		}
		for _, fn := range funcs {
			add(trace.BuildCallerID(fn.RepoName, fn.FilePath, fn.Name))
		}
		return nil
	}
	for _, file := range req.Files {
		if len(file.Ranges) > 0 {
			for _, r := range file.Ranges {
				if err := addRange(normalizeImpactRange(r, file.Repo, file.Path)); err != nil {
					return nil, err
				}
			}
			continue
		}
		repo, err := repoFor(file.Repo)
		if err != nil {
			return nil, err
		}
		funcs, err := h.storage.FunctionsByFilePath(repo, file.Path, snapshotFilterForScope(scope))
		if err != nil {
			return nil, fmt.Errorf("functions for %s: %w", file.Path, err)
		}
		for _, fn := range funcs {
			add(trace.BuildCallerID(fn.RepoName, fn.FilePath, fn.Name))
		}
	}
	for _, r := range req.Ranges {
		if err := addRange(normalizeImpactRange(r, "", "")); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func normalizeImpactRange(r ImpactRange, parentRepo, parentPath string) ImpactRange {
	if strings.TrimSpace(r.Repo) == "" {
		r.Repo = parentRepo
	}
	if strings.TrimSpace(r.Path) == "" {
		r.Path = parentPath
	}
	if r.StartLine <= 0 {
		r.StartLine = r.Start
	}
	if r.EndLine <= 0 {
		r.EndLine = r.End
	}
	return r
}

// parseUnifiedDiffRanges returns the edited line ranges of a diff in pre-image
// coordinates, which is what the index holds. Unchanged context is excluded and
// header-looking added content ("+++ x") can never switch files. An unparseable
// diff yields no ranges; request handlers reject such diffs before this runs.
func parseUnifiedDiffRanges(diff, repo string) []ImpactRange {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	files, err := diffparse.Parse(diff)
	if err != nil {
		return nil
	}
	repo = strings.TrimSpace(repo)
	var ranges []ImpactRange
	for _, file := range files {
		if file.New {
			continue
		}
		for _, edit := range file.Edits() {
			start := edit.Start
			if start < 1 {
				start = 1
			}
			ranges = append(ranges, ImpactRange{Repo: repo, Path: edit.Path, StartLine: start, EndLine: max(edit.End, start)})
		}
	}
	return ranges
}

func parseDiffFilePath(line string) string { return diffparse.GitHeaderPath(line) }

func normalizeDiffPath(path string) string { return diffparse.NormalizePath(path) }

func mergeImpactRanges(in []ImpactRange) []ImpactRange {
	if len(in) == 0 {
		return nil
	}
	grouped := make(map[string][]ImpactRange)
	for _, r := range in {
		if r.Path == "" {
			continue
		}
		if r.StartLine <= 0 {
			continue
		}
		if r.EndLine < r.StartLine {
			r.EndLine = r.StartLine
		}
		key := strings.TrimSpace(r.Repo) + "|" + r.Path
		grouped[key] = append(grouped[key], r)
	}

	var out []ImpactRange
	for _, ranges := range grouped {
		sort.Slice(ranges, func(i, j int) bool {
			if ranges[i].StartLine != ranges[j].StartLine {
				return ranges[i].StartLine < ranges[j].StartLine
			}
			return ranges[i].EndLine < ranges[j].EndLine
		})

		current := ranges[0]
		for _, next := range ranges[1:] {
			if next.StartLine <= current.EndLine+1 {
				if next.EndLine > current.EndLine {
					current.EndLine = next.EndLine
				}
				continue
			}
			out = append(out, current)
			current = next
		}
		out = append(out, current)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].EndLine < out[j].EndLine
	})
	return out
}

func filterOutTestLikeRanges(in []ImpactRange) []ImpactRange {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImpactRange, 0, len(in))
	for _, r := range in {
		if isTestLikePath(r.Path) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func isTestLikePath(path string) bool { return sourcepath.IsTest(path) }

func impactWorkspaceHints(ctx context.Context, h *Handlers, req ImpactRequest, scope searchWorkspaceScope) []WorkspaceHint {
	for _, value := range req.Functions {
		if hints := h.workspaceHintsForQuery(ctx, value, scope); len(hints) > 0 {
			return hints
		}
	}
	for _, value := range req.CallerIDs {
		if hints := h.workspaceHintsForQuery(ctx, value, scope); len(hints) > 0 {
			return hints
		}
	}
	for _, value := range req.Files {
		if hints := h.workspaceHintsForQuery(ctx, filepath.Base(value.Path), scope); len(hints) > 0 {
			return hints
		}
	}
	return nil
}

type impactCount struct {
	MinDepth int
	Count    int
}

type impactBuilder struct {
	nodes                map[string]ImpactNode
	edges                map[string]ImpactEdge
	httpCalls            map[string]*impactCount
	httpMatches          map[string]map[string]ImpactHttpMatch
	httpNodeKey          map[string]string
	queues               map[string]*impactCount
	queueConsumers       map[string]map[string]bool
	queueNodeKey         map[string]string
	eventBridgeSchedules map[string]*impactCount
	azureTimerSchedules  map[string]*impactCount
	callerDepths         map[string]int
	repos                map[string]*impactCount
	upstreamFuncDepth    map[string]int
	upstreamFuncCount    map[string]int
}

func newImpactBuilder() *impactBuilder {
	return &impactBuilder{
		nodes:                make(map[string]ImpactNode),
		edges:                make(map[string]ImpactEdge),
		httpCalls:            make(map[string]*impactCount),
		httpMatches:          make(map[string]map[string]ImpactHttpMatch),
		httpNodeKey:          make(map[string]string),
		queues:               make(map[string]*impactCount),
		queueConsumers:       make(map[string]map[string]bool),
		queueNodeKey:         make(map[string]string),
		eventBridgeSchedules: make(map[string]*impactCount),
		azureTimerSchedules:  make(map[string]*impactCount),
		callerDepths:         make(map[string]int),
		repos:                make(map[string]*impactCount),
		upstreamFuncDepth:    make(map[string]int),
		upstreamFuncCount:    make(map[string]int),
	}
}

func (b *impactBuilder) ingestTrees(nodes []*trace.TreeNode, direction string) {
	var walk func(parentID string, node *trace.TreeNode)
	walk = func(parentID string, node *trace.TreeNode) {
		if node == nil {
			return
		}
		nodeID, impactNode, ok := impactNodeFromTree(parentID, node)
		if ok {
			if _, exists := b.nodes[nodeID]; !exists {
				b.nodes[nodeID] = impactNode
			}
			if impactNode.Type == "http_call" {
				key := httpCallKey(node.HttpMethod, node.HttpTarget)
				if key != "" {
					b.httpNodeKey[nodeID] = key
				}
			}
			if impactNode.Type == "sqs" && impactNode.Queue != "" {
				b.queueNodeKey[nodeID] = impactNode.Queue
			}
		}
		if key, ok := b.httpNodeKey[parentID]; ok {
			if match, ok := httpMatchFromNode(node); ok {
				addHttpMatch(b.httpMatches, key, match)
			}
		}
		if queue, ok := b.queueNodeKey[parentID]; ok {
			addImpactQueueConsumer(b.queueConsumers, queue, impactQueueConsumerName(node))
		}
		if node.CallerID != "" && direction == "upstream" {
			if depth, ok := b.upstreamFuncDepth[node.CallerID]; !ok || node.Depth < depth {
				b.upstreamFuncDepth[node.CallerID] = node.Depth
			}
			b.upstreamFuncCount[node.CallerID]++
		}
		if node.CallerID != "" {
			if depth, ok := b.callerDepths[node.CallerID]; !ok || node.Depth < depth {
				b.callerDepths[node.CallerID] = node.Depth
			}
		}
		if node.Repo != "" {
			updateImpactCount(b.repos, node.Repo, node.Depth)
		}
		if node.HttpMethod != "" || node.EdgeType == "http" {
			method := node.HttpMethod
			target := node.HttpTarget
			if target == "" && node.Name != "" {
				// Extract target from node name like "[GET /path] via Handler"
				// or use the name directly if no URL pattern was parsed.
				if len(node.Name) > 1 && node.Name[0] == '[' {
					if end := strings.Index(node.Name, "]"); end > 0 {
						inner := strings.TrimSpace(node.Name[1:end])
						parts := strings.SplitN(inner, " ", 2)
						if len(parts) == 2 && parts[1] != "" {
							method = parts[0]
							target = parts[1]
						} else if method == "" {
							method = inner
						}
					}
				}
				if target == "" {
					target = "(external API)"
				}
			}
			key := fmt.Sprintf("%s %s", method, target)
			updateImpactCount(b.httpCalls, key, node.Depth)
		}
		if node.QueueTarget != "" || node.IsSqs {
			queue := node.QueueTarget
			if queue == "" {
				queue = node.Name
			}
			updateImpactCount(b.queues, queue, node.Depth)
		}
		if node.EdgeType == "eventbridge" {
			updateImpactCount(b.eventBridgeSchedules, node.Name, node.Depth)
		}
		if node.EdgeType == "azure_timer" {
			updateImpactCount(b.azureTimerSchedules, node.Name, node.Depth)
		}

		if parentID != "" && nodeID != "" {
			source := parentID
			target := nodeID
			if direction == "upstream" {
				source, target = nodeID, parentID
			}
			edgeKey := fmt.Sprintf("%s->%s:%s", source, target, node.EdgeType)
			if _, exists := b.edges[edgeKey]; !exists {
				b.edges[edgeKey] = ImpactEdge{
					Source:     source,
					Target:     target,
					Type:       node.EdgeType,
					Confidence: node.Confidence,
					Evidence:   node.Evidence,
				}
			}
		}

		nextParent := parentID
		if nodeID != "" {
			nextParent = nodeID
		}
		for _, child := range node.Children {
			walk(nextParent, child)
		}
	}

	for _, root := range nodes {
		walk("", root)
	}
}

func (b *impactBuilder) resolveEntrypoints(ctx context.Context, pool *pgxpool.Pool, scope searchWorkspaceScope) ([]ImpactEndpoint, error) {
	if len(b.upstreamFuncDepth) == 0 {
		return []ImpactEndpoint{}, nil
	}
	var callerIDs []string
	for callerID := range b.upstreamFuncDepth {
		callerIDs = append(callerIDs, callerID)
	}
	idByCaller := trace.FindFunctionIDsByCallerIDsForSnapshotFilter(trace.WithContext(ctx, pool), callerIDs, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if len(idByCaller) == 0 {
		return []ImpactEndpoint{}, nil
	}
	callerByID := make(map[int64]string, len(idByCaller))
	var funcIDs []int64
	for callerID, id := range idByCaller {
		funcIDs = append(funcIDs, id)
		callerByID[id] = callerID
	}

	query := `
		SELECT e.method, e.path, e.line_number, fn.name, fi.path, r.name, fn.id
		FROM endpoints e
		JOIN functions fn ON e.handler_function_id = fn.id
		JOIN files fi ON fn.file_id = fi.id
		JOIN repositories r ON fi.repo_id = r.id
		WHERE e.handler_function_id = ANY($1)`
	rows, err := pool.Query(ctx, query, funcIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	endpointMap := make(map[string]ImpactEndpoint)
	for rows.Next() {
		var method, path, handler, file, repo string
		var lineNum *int
		var fnID int64
		if err := rows.Scan(&method, &path, &lineNum, &handler, &file, &repo, &fnID); err != nil {
			return nil, err
		}
		callerID := callerByID[fnID]
		if callerID == "" {
			callerID = trace.BuildCallerID(repo, file, handler)
		}
		minDepth, ok := b.upstreamFuncDepth[callerID]
		if !ok {
			minDepth = 0
		}
		fanout := b.upstreamFuncCount[callerID]
		if fanout == 0 {
			fanout = 1
		}
		line := 0
		if lineNum != nil {
			line = *lineNum
		}
		key := fmt.Sprintf("%s %s %s", repo, method, path)
		entry := endpointMap[key]
		if entry.Method == "" {
			entry = ImpactEndpoint{
				Method:           method,
				Path:             path,
				Handler:          handler,
				Repo:             repo,
				File:             file,
				Line:             line,
				MinDepth:         minDepth,
				DirectlyAffected: directlyAffectedImpactDepth(minDepth),
				Fanout:           fanout,
				Score:            impactScore(minDepth, fanout),
			}
		} else {
			if minDepth < entry.MinDepth {
				entry.MinDepth = minDepth
				entry.DirectlyAffected = directlyAffectedImpactDepth(entry.MinDepth)
			}
			entry.Fanout += fanout
			entry.Score = impactScore(entry.MinDepth, entry.Fanout)
		}
		endpointMap[key] = entry
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(endpointMap) == 0 {
		return []ImpactEndpoint{}, nil
	}

	var out []ImpactEndpoint
	for _, entry := range endpointMap {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Repo == out[j].Repo {
				return out[i].Path < out[j].Path
			}
			return out[i].Repo < out[j].Repo
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

func resolveRootEntrypoints(ctx context.Context, pool *pgxpool.Pool, callerIDs []string, scope searchWorkspaceScope) ([]ImpactEndpoint, error) {
	if len(callerIDs) == 0 {
		return []ImpactEndpoint{}, nil
	}
	idByCaller := trace.FindFunctionIDsByCallerIDsForSnapshotFilter(trace.WithContext(ctx, pool), callerIDs, activeSnapshotIDs(scope), workspaceIncludesLegacy(scope))
	if len(idByCaller) == 0 {
		return []ImpactEndpoint{}, nil
	}
	var funcIDs []int64
	for _, id := range idByCaller {
		funcIDs = append(funcIDs, id)
	}

	query := `
		SELECT e.method, e.path, e.line_number, fn.name, fi.path, r.name
		FROM endpoints e
		JOIN functions fn ON e.handler_function_id = fn.id
		JOIN files fi ON fn.file_id = fi.id
		JOIN repositories r ON fi.repo_id = r.id
		WHERE e.handler_function_id = ANY($1)`
	rows, err := pool.Query(ctx, query, funcIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	endpointMap := make(map[string]ImpactEndpoint)
	for rows.Next() {
		var method, path, handler, file, repo string
		var lineNum *int
		if err := rows.Scan(&method, &path, &lineNum, &handler, &file, &repo); err != nil {
			return nil, err
		}
		line := 0
		if lineNum != nil {
			line = *lineNum
		}
		key := fmt.Sprintf("%s %s %s", repo, method, path)
		entry := endpointMap[key]
		if entry.Method == "" {
			entry = ImpactEndpoint{
				Method:           method,
				Path:             path,
				Handler:          handler,
				Repo:             repo,
				File:             file,
				Line:             line,
				MinDepth:         0,
				DirectlyAffected: true,
				Fanout:           1,
				Score:            impactScore(0, 1),
			}
		} else {
			entry.Fanout++
			entry.Score = impactScore(entry.MinDepth, entry.Fanout)
		}
		endpointMap[key] = entry
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(endpointMap) == 0 {
		return []ImpactEndpoint{}, nil
	}
	var out []ImpactEndpoint
	for _, entry := range endpointMap {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Repo == out[j].Repo {
				return out[i].Path < out[j].Path
			}
			return out[i].Repo < out[j].Repo
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

func mergeEntrypoints(primary, secondary []ImpactEndpoint) []ImpactEndpoint {
	if len(primary) == 0 && len(secondary) == 0 {
		return []ImpactEndpoint{}
	}
	merged := make(map[string]ImpactEndpoint)
	add := func(entry ImpactEndpoint) {
		key := fmt.Sprintf("%s %s %s", entry.Repo, entry.Method, entry.Path)
		if existing, ok := merged[key]; ok {
			if entry.MinDepth < existing.MinDepth {
				existing.MinDepth = entry.MinDepth
				existing.DirectlyAffected = directlyAffectedImpactDepth(existing.MinDepth)
			}
			existing.Fanout += entry.Fanout
			existing.Score = impactScore(existing.MinDepth, existing.Fanout)
			if existing.Handler == "" {
				existing.Handler = entry.Handler
				existing.File = entry.File
				existing.Line = entry.Line
			}
			merged[key] = existing
			return
		}
		merged[key] = entry
	}
	for _, entry := range primary {
		add(entry)
	}
	for _, entry := range secondary {
		add(entry)
	}
	out := make([]ImpactEndpoint, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Repo == out[j].Repo {
				return out[i].Path < out[j].Path
			}
			return out[i].Repo < out[j].Repo
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func (b *impactBuilder) buildSummary(pool trace.Queryer, entrypoints []ImpactEndpoint, snapshotIDs []int64, includeLegacy bool) ImpactSummary {
	if entrypoints == nil {
		entrypoints = []ImpactEndpoint{}
	}
	httpCalls := buildImpactHttpCalls(b.httpCalls, b.httpMatches)
	queues := buildImpactQueues(b.queues, b.queueConsumers)
	dataAccesses := buildImpactDataAccesses(pool, b.callerDepths, snapshotIDs, includeLegacy)
	ebSchedules := buildImpactEventBridgeSchedules(pool, b.eventBridgeSchedules)
	azureSchedules := buildImpactAzureTimerSchedules(pool, b.azureTimerSchedules, snapshotIDs, includeLegacy)
	repos := buildImpactRepos(b.repos)
	summary := ImpactSummary{
		Entrypoints:  entrypoints,
		HttpCalls:    httpCalls,
		Queues:       queues,
		DataAccesses: dataAccesses,
		Repos:        repos,
	}
	if len(ebSchedules) > 0 {
		summary.EventBridgeSchedules = ebSchedules
	}
	if len(azureSchedules) > 0 {
		summary.AzureTimerSchedules = azureSchedules
	}
	return summary
}

func (b *impactBuilder) buildReport() ImpactReport {
	nodes := make([]ImpactNode, 0, len(b.nodes))
	for _, node := range b.nodes {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})
	edges := make([]ImpactEdge, 0, len(b.edges))
	for _, edge := range b.edges {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source == edges[j].Source {
			if edges[i].Target == edges[j].Target {
				return edges[i].Type < edges[j].Type
			}
			return edges[i].Target < edges[j].Target
		}
		return edges[i].Source < edges[j].Source
	})
	return ImpactReport{
		Nodes: nodes,
		Edges: edges,
	}
}

func impactNodeFromTree(parentID string, node *trace.TreeNode) (string, ImpactNode, bool) {
	if node == nil {
		return "", ImpactNode{}, false
	}
	if node.CallerID != "" {
		id := "func:" + node.CallerID
		return id, ImpactNode{
			ID:   id,
			Type: "function",
			Name: node.Name,
			Repo: node.Repo,
			File: node.File,
			Line: node.Line,
		}, true
	}
	if node.HttpMethod != "" && node.HttpTarget != "" {
		id := fmt.Sprintf("http:%s:%s:from:%s", node.HttpMethod, node.HttpTarget, parentID)
		return id, ImpactNode{
			ID:     id,
			Type:   "http_call",
			Name:   fmt.Sprintf("%s %s", node.HttpMethod, node.HttpTarget),
			Method: node.HttpMethod,
			Path:   node.HttpTarget,
			Line:   node.Line,
		}, true
	}
	if node.QueueTarget != "" || node.IsSqs {
		queue := node.QueueTarget
		if queue == "" {
			queue = node.Name
		}
		id := fmt.Sprintf("sqs:%s:from:%s", queue, parentID)
		return id, ImpactNode{
			ID:    id,
			Type:  "sqs",
			Name:  queue,
			Queue: queue,
			Line:  node.Line,
		}, true
	}
	if node.EdgeType == "azure_timer" {
		id := fmt.Sprintf("azure_timer:%s:from:%s", node.Name, parentID)
		return id, ImpactNode{
			ID:   id,
			Type: "azure_timer",
			Name: node.Name,
			Repo: node.Repo,
			File: node.File,
			Line: node.Line,
		}, true
	}
	return "", ImpactNode{}, false
}

func updateImpactCount(target map[string]*impactCount, key string, depth int) {
	if key == "" {
		return
	}
	entry, ok := target[key]
	if !ok {
		entry = &impactCount{MinDepth: depth}
		target[key] = entry
	}
	if depth < entry.MinDepth {
		entry.MinDepth = depth
	}
	entry.Count++
}

func impactScore(depth, fanout int) float64 {
	if depth < 0 {
		depth = 0
	}
	if fanout < 0 {
		fanout = 0
	}
	return (1.0 / float64(depth+1)) * (1.0 + math.Log1p(float64(fanout)))
}

func directlyAffectedImpactDepth(depth int) bool {
	return depth == 0
}

func buildImpactCompleteness(maxNodes int, downstreamLimit trace.LimitStats, downstream tracePruneStats, upstreamLimit trace.LimitStats, upstream tracePruneStats) ImpactCompleteness {
	downstreamCompleteness, downstreamReason := buildTraceDirectionCompleteness("downstream", downstreamLimit, downstream)
	upstreamCompleteness, upstreamReason := buildTraceDirectionCompleteness("upstream", upstreamLimit, upstream)
	truncationReasons := make([]string, 0, 2)
	if downstreamReason != "" {
		truncationReasons = append(truncationReasons, downstreamReason)
	}
	if upstreamReason != "" {
		truncationReasons = append(truncationReasons, upstreamReason)
	}
	return ImpactCompleteness{
		AppliedMaxNodes:   maxNodes,
		Truncated:         downstreamCompleteness.Truncated || upstreamCompleteness.Truncated,
		TruncationReasons: truncationReasons,
		Downstream:        downstreamCompleteness,
		Upstream:          upstreamCompleteness,
	}
}

func buildImpactWarnings(completeness ImpactCompleteness) []string {
	if !completeness.Truncated {
		return nil
	}
	warnings := make([]string, 0, 2)
	if completeness.Downstream.Truncated {
		warnings = append(warnings, buildImpactDirectionWarning("Downstream", completeness.Downstream))
	}
	if completeness.Upstream.Truncated {
		warnings = append(warnings, buildImpactDirectionWarning("Upstream", completeness.Upstream))
	}
	return warnings
}

func buildImpactDirectionWarning(direction string, completeness ImpactDirectionCompleteness) string {
	if completeness.ExactAvailableNodes {
		return fmt.Sprintf("%s impact graph is clipped. Showing %d of %d available nodes. Increase maxNodes to see the full blast radius.", direction, completeness.ReturnedNodes, completeness.AvailableNodes)
	}
	return fmt.Sprintf("%s impact graph is clipped. Showing %d nodes; more are available, but the exact total is unknown at this maxNodes setting.", direction, completeness.ReturnedNodes)
}

func buildImpactHttpCalls(counts map[string]*impactCount, matches map[string]map[string]ImpactHttpMatch) []ImpactHttpCall {
	if len(counts) == 0 {
		return []ImpactHttpCall{}
	}
	var out []ImpactHttpCall
	for key, entry := range counts {
		parts := strings.SplitN(key, " ", 2)
		method := key
		path := ""
		if len(parts) == 2 {
			method = parts[0]
			path = parts[1]
		}
		out = append(out, ImpactHttpCall{
			Method:           method,
			Path:             path,
			MinDepth:         entry.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(entry.MinDepth),
			Fanout:           entry.Count,
			Score:            impactScore(entry.MinDepth, entry.Count),
			Matches:          flattenHttpMatches(matches[key]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Method < out[j].Method
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func buildImpactQueues(counts map[string]*impactCount, consumers map[string]map[string]bool) []ImpactQueue {
	if len(counts) == 0 {
		return []ImpactQueue{}
	}
	var out []ImpactQueue
	for name, entry := range counts {
		out = append(out, ImpactQueue{
			Name:             name,
			MinDepth:         entry.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(entry.MinDepth),
			Fanout:           entry.Count,
			Score:            impactScore(entry.MinDepth, entry.Count),
			Consumers:        sortedImpactQueueConsumers(consumers[name]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func buildImpactDataAccesses(pool trace.Queryer, callerDepths map[string]int, snapshotIDs []int64, includeLegacy bool) []ImpactDataAccess {
	if pool == nil || len(callerDepths) == 0 {
		return nil
	}
	callerIDs := make([]string, 0, len(callerDepths))
	for callerID := range callerDepths {
		if strings.TrimSpace(callerID) != "" {
			callerIDs = append(callerIDs, callerID)
		}
	}
	if len(callerIDs) == 0 {
		return nil
	}
	sort.Strings(callerIDs)

	args := []interface{}{callerIDs, snapshotIDs}
	snapshotClause := integrationSnapshotClause("da.snapshot_id", 2, includeLegacy)
	query := fmt.Sprintf(`
		SELECT da.caller_id,
		       da.entity_name,
		       da.access,
		       split_part(da.caller_id, ':', 2) AS file_path
		FROM data_accesses da
		WHERE da.caller_id = ANY($1)
		  AND COALESCE(da.entity_name, '') <> ''
		  AND COALESCE(da.access, '') <> ''
		  AND %s
		ORDER BY da.entity_name, da.access, da.caller_id
		LIMIT 1000`, snapshotClause)
	rows, err := pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type impactDataAccessAgg struct {
		entity  string
		access  string
		count   impactCount
		callers map[string]bool
		files   map[string]bool
	}
	byKey := make(map[string]*impactDataAccessAgg)
	seenRows := make(map[string]bool)
	for rows.Next() {
		var callerID, entity, access, file string
		if err := rows.Scan(&callerID, &entity, &access, &file); err != nil {
			continue
		}
		entity = strings.TrimSpace(entity)
		access = strings.TrimSpace(strings.ToLower(access))
		callerID = strings.TrimSpace(callerID)
		if entity == "" || access == "" || callerID == "" {
			continue
		}
		depth := callerDepths[callerID]
		rowKey := strings.ToLower(entity) + "|" + access + "|" + callerID + "|" + file
		if seenRows[rowKey] {
			continue
		}
		seenRows[rowKey] = true
		key := strings.ToLower(entity) + "|" + access
		agg := byKey[key]
		if agg == nil {
			agg = &impactDataAccessAgg{
				entity:  entity,
				access:  access,
				count:   impactCount{MinDepth: depth},
				callers: make(map[string]bool),
				files:   make(map[string]bool),
			}
			byKey[key] = agg
		}
		if depth < agg.count.MinDepth {
			agg.count.MinDepth = depth
		}
		agg.count.Count++
		agg.callers[callerID] = true
		if strings.TrimSpace(file) != "" {
			agg.files[file] = true
		}
	}

	out := make([]ImpactDataAccess, 0, len(byKey))
	for _, agg := range byKey {
		out = append(out, ImpactDataAccess{
			Entity:           agg.entity,
			Access:           agg.access,
			MinDepth:         agg.count.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(agg.count.MinDepth),
			Fanout:           agg.count.Count,
			Score:            impactScore(agg.count.MinDepth, agg.count.Count),
			Callers:          sortedLimitStrings(agg.callers, 6),
			Files:            sortedLimitStrings(agg.files, 6),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if out[i].Entity == out[j].Entity {
				return out[i].Access < out[j].Access
			}
			return out[i].Entity < out[j].Entity
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func addImpactQueueConsumer(target map[string]map[string]bool, queue, consumer string) {
	queue = strings.TrimSpace(queue)
	consumer = strings.TrimSpace(consumer)
	if queue == "" || consumer == "" {
		return
	}
	if target[queue] == nil {
		target[queue] = make(map[string]bool)
	}
	target[queue][consumer] = true
}

func impactQueueConsumerName(node *trace.TreeNode) string {
	if node == nil {
		return ""
	}
	if strings.TrimSpace(node.CallerID) != "" {
		return node.CallerID
	}
	name := strings.TrimSpace(strings.TrimPrefix(node.Name, "→"))
	if node.Repo != "" && name != "" && !strings.Contains(name, "["+node.Repo+"]") {
		return fmt.Sprintf("%s [%s]", name, node.Repo)
	}
	if name != "" {
		return name
	}
	return strings.TrimSpace(node.Repo)
}

func sortedImpactQueueConsumers(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedLimitStrings(values map[string]bool, limit int) []string {
	if len(values) == 0 || limit == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func buildImpactEventBridgeSchedules(pool trace.Queryer, counts map[string]*impactCount) []ImpactSchedule {
	if len(counts) == 0 {
		return nil
	}

	// Extract rule names from formatted node names like "[EventBridge → ruleName] cron(...) STATE"
	ruleNames := make([]string, 0, len(counts))
	for name := range counts {
		if ruleName := parseEventBridgeRuleName(name); ruleName != "" {
			ruleNames = append(ruleNames, ruleName)
		}
	}

	// Batch lookup schedule details from DB
	scheduleMap := make(map[string]trace.EventBridgeSchedule)
	if len(ruleNames) > 0 && pool != nil {
		rows, err := pool.Query(context.Background(), `
			SELECT rule_name, schedule_expression, state
			FROM eventbridge_schedules
			WHERE rule_name = ANY($1)
		`, ruleNames)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var ruleName, expr, state string
				if rows.Scan(&ruleName, &expr, &state) == nil {
					scheduleMap[ruleName] = trace.EventBridgeSchedule{
						RuleName:           ruleName,
						ScheduleExpression: expr,
						State:              state,
					}
				}
			}
		}
	}

	var out []ImpactSchedule
	for name, entry := range counts {
		sched := ImpactSchedule{
			Name:             name,
			MinDepth:         entry.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(entry.MinDepth),
			Fanout:           entry.Count,
			Score:            impactScore(entry.MinDepth, entry.Count),
		}
		if ruleName := parseEventBridgeRuleName(name); ruleName != "" {
			if eb, ok := scheduleMap[ruleName]; ok {
				sched.Name = ruleName
				sched.Schedule = eb.ScheduleExpression
				sched.State = eb.State
			}
		}
		out = append(out, sched)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func buildImpactAzureTimerSchedules(pool trace.Queryer, counts map[string]*impactCount, snapshotIDs []int64, includeLegacy bool) []ImpactSchedule {
	if len(counts) == 0 {
		return nil
	}

	functionNames := make([]string, 0, len(counts))
	for name := range counts {
		if functionName := parseAzureTimerFunctionName(name); functionName != "" {
			functionNames = append(functionNames, functionName)
		}
	}

	scheduleMap := make(map[string]string)
	if len(functionNames) > 0 && pool != nil {
		query := fmt.Sprintf(`
			SELECT aft.function_name, COALESCE(aft.schedule_expression, '')
			FROM azure_function_triggers aft
			JOIN files fi ON fi.id = aft.file_id
			WHERE LOWER(aft.trigger_type) = 'timertrigger'
			  AND aft.function_name = ANY($1)
			  AND %s
		`, integrationSnapshotClause("fi.snapshot_id", 2, includeLegacy))
		rows, err := pool.Query(context.Background(), query, functionNames, snapshotIDs)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var functionName, expr string
				if rows.Scan(&functionName, &expr) == nil {
					scheduleMap[functionName] = expr
				}
			}
		}
	}

	var out []ImpactSchedule
	for name, entry := range counts {
		sched := ImpactSchedule{
			Name:             name,
			MinDepth:         entry.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(entry.MinDepth),
			Fanout:           entry.Count,
			Score:            impactScore(entry.MinDepth, entry.Count),
		}
		if functionName := parseAzureTimerFunctionName(name); functionName != "" {
			sched.Name = functionName
			sched.Schedule = scheduleMap[functionName]
			sched.State = "ENABLED"
		}
		out = append(out, sched)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// parseEventBridgeRuleName extracts the rule name from a formatted node name
// like "[EventBridge → scheduledTask] cron(30 11 ? * * *) ENABLED"
func parseEventBridgeRuleName(name string) string {
	const prefix = "[EventBridge → "
	idx := strings.Index(name, prefix)
	if idx < 0 {
		return ""
	}
	rest := name[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func parseAzureTimerFunctionName(name string) string {
	const prefix = "[Azure Timer → "
	idx := strings.Index(name, prefix)
	if idx < 0 {
		return ""
	}
	rest := name[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func buildImpactRepos(counts map[string]*impactCount) []ImpactRepo {
	if len(counts) == 0 {
		return []ImpactRepo{}
	}
	var out []ImpactRepo
	for name, entry := range counts {
		out = append(out, ImpactRepo{
			Name:             name,
			MinDepth:         entry.MinDepth,
			DirectlyAffected: directlyAffectedImpactDepth(entry.MinDepth),
			Fanout:           entry.Count,
			Score:            impactScore(entry.MinDepth, entry.Count),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Name < out[j].Name
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func httpCallKey(method, path string) string {
	if method == "" || path == "" {
		return ""
	}
	return fmt.Sprintf("%s %s", method, path)
}

func httpMatchFromNode(node *trace.TreeNode) (ImpactHttpMatch, bool) {
	if node == nil {
		return ImpactHttpMatch{}, false
	}
	repo, handler := "", ""
	if node.CallerID != "" {
		repo, _, handler = trace.ParseCallerID(node.CallerID)
	}
	if repo == "" || handler == "" {
		name := strings.TrimSpace(node.Name)
		if strings.HasPrefix(name, "→") {
			name = strings.TrimSpace(strings.TrimPrefix(name, "→"))
			if idx := strings.LastIndex(name, "["); idx != -1 && strings.HasSuffix(name, "]") {
				handler = strings.TrimSpace(name[:idx])
				repo = strings.TrimSuffix(strings.TrimSpace(name[idx+1:]), "]")
			}
		}
	}
	if repo == "" || handler == "" {
		return ImpactHttpMatch{}, false
	}
	return ImpactHttpMatch{
		Repo:    repo,
		Handler: handler,
		File:    node.File,
		Line:    node.Line,
	}, true
}

func addHttpMatch(target map[string]map[string]ImpactHttpMatch, key string, match ImpactHttpMatch) {
	if key == "" {
		return
	}
	if target[key] == nil {
		target[key] = make(map[string]ImpactHttpMatch)
	}
	inner := target[key]
	innerKey := fmt.Sprintf("%s:%s:%s", match.Repo, match.Handler, match.File)
	if _, ok := inner[innerKey]; ok {
		return
	}
	inner[innerKey] = match
}

func flattenHttpMatches(matches map[string]ImpactHttpMatch) []ImpactHttpMatch {
	if len(matches) == 0 {
		return nil
	}
	out := make([]ImpactHttpMatch, 0, len(matches))
	for _, match := range matches {
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo == out[j].Repo {
			return out[i].Handler < out[j].Handler
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}
