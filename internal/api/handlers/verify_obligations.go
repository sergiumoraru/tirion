package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sergiumoraru/tirion/internal/trace"
	workspacefs "github.com/sergiumoraru/tirion/internal/workspace"
)

type VerifyAnchorInput struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Repo string `json:"repo,omitempty"`
}

type VerifyWaiverInput struct {
	Class         string `json:"class,omitempty"`
	Subject       string `json:"subject"`
	Justification string `json:"justification"`
}

type VerifyPreconditions struct {
	DiffAligned     bool     `json:"diffAligned"`
	IndexSHA        string   `json:"indexSha,omitempty"`
	AlignmentErrors []string `json:"alignmentErrors,omitempty"`
}

type VerifyResolvedAnchor struct {
	Kind      string                     `json:"kind"`
	Ref       string                     `json:"ref"`
	Repo      string                     `json:"repo,omitempty"`
	Status    string                     `json:"status"`
	Reason    string                     `json:"reason,omitempty"`
	Evidence  []VerifyObligationEvidence `json:"evidence,omitempty"`
	CallerIDs []string                   `json:"callerIds,omitempty"`
}

type VerifyObligationEvidence struct {
	Kind     string `json:"kind"`
	Repo     string `json:"repo,omitempty"`
	File     string `json:"file,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	CallerID string `json:"callerId,omitempty"`
	Line     int    `json:"line,omitempty"`
}

type VerifyObligation struct {
	ID            string                     `json:"id"`
	Class         string                     `json:"class"`
	Subject       string                     `json:"subject"`
	Status        string                     `json:"status"`
	Evidence      []VerifyObligationEvidence `json:"evidence,omitempty"`
	Resolution    string                     `json:"resolution,omitempty"`
	Justification string                     `json:"justification,omitempty"`
}

type VerifyConnection struct {
	Anchored                 bool     `json:"anchored"`
	ChangedPathsToBoundary   int      `json:"changedPathsToBoundary"`
	UntouchedPathsToBoundary int      `json:"untouchedPathsToBoundary"`
	ChangedCallerIDs         []string `json:"changedCallerIds,omitempty"`
	FrontierCallerIDs        []string `json:"frontierCallerIds,omitempty"`
}

type AgentChangeValidation struct {
	Mode            string                `json:"mode"`
	FixValidation   string                `json:"fixValidation"`
	Anchor          *VerifyResolvedAnchor `json:"anchor,omitempty"`
	Preconditions   VerifyPreconditions   `json:"preconditions"`
	Connection      *VerifyConnection     `json:"connection,omitempty"`
	Obligations     []VerifyObligation    `json:"obligations,omitempty"`
	FrontierClipped bool                  `json:"frontierClipped,omitempty"`
	QueryErrors     []string              `json:"queryErrors,omitempty"`
	Reasons         []string              `json:"reasons,omitempty"`
}

type verifyFrontierNode struct {
	CallerID string
	Kind     string
	Repo     string
	File     string
	Symbol   string
	Line     int
}

type verifyAnchorResolution struct {
	anchor       VerifyResolvedAnchor
	boundaryIDs  []string
	predecessors []verifyFrontierNode
	frontier     []verifyFrontierNode
	clipped      bool
	queryErrors  []string
}

func (h *Handlers) validateAgentChange(ctx context.Context, req ImpactRequest, changedCallerIDs []string, scope searchWorkspaceScope) *AgentChangeValidation {
	aligned, indexSHA, alignmentErrors := h.validateVerifyDiffBase(req, scope)
	validation := &AgentChangeValidation{
		Mode:          "contracts_only",
		FixValidation: "not_performed",
		Preconditions: VerifyPreconditions{
			DiffAligned:     aligned,
			IndexSHA:        indexSHA,
			AlignmentErrors: alignmentErrors,
		},
	}
	if req.Anchor == nil {
		if req.RequireAnchor {
			validation.FixValidation = "not_verified"
			validation.Reasons = append(validation.Reasons, "fix validation requires an anchor")
		}
		return validation
	}

	validation.Mode = "fix_validation"
	validation.FixValidation = "performed"
	if !aligned {
		validation.FixValidation = "not_verified"
		validation.Reasons = append(validation.Reasons, "diff does not align with the active workspace snapshot")
		validation.Anchor = &VerifyResolvedAnchor{
			Kind:   normalizeVerifyAnchorKind(req.Anchor.Kind),
			Ref:    strings.TrimSpace(req.Anchor.Ref),
			Repo:   strings.TrimSpace(req.Anchor.Repo),
			Status: "not_verified",
			Reason: "diff preconditions failed",
		}
		validation.Obligations = notVerifiedFixObligations("diff preconditions failed")
		return validation
	}

	depth := req.Depth
	if depth <= 0 {
		depth = 4
	}
	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 2000
	}
	resolution := h.resolveVerifyAnchor(ctx, *req.Anchor, depth, maxNodes, scope)
	validation.Anchor = &resolution.anchor
	validation.FrontierClipped = resolution.clipped
	validation.QueryErrors = uniqueNonEmptyStrings(resolution.queryErrors)
	if len(validation.QueryErrors) > 0 {
		validation.Reasons = append(validation.Reasons, "anchor frontier lookup was incomplete")
	}

	changedSet := stringSet(changedCallerIDs)
	anchorChanged := false
	for _, callerID := range resolution.boundaryIDs {
		if changedSet[callerID] {
			anchorChanged = true
			break
		}
	}
	if resolution.anchor.Status != "resolved" || anchorChanged {
		validation.FixValidation = "not_verified"
		if anchorChanged {
			validation.Anchor.Status = "invalid"
			validation.Anchor.Reason = "anchor resolves inside the diff's changed symbol set"
			validation.Reasons = append(validation.Reasons, "anchor is part of the change and cannot independently validate it")
		} else if resolution.anchor.Reason != "" {
			validation.Reasons = append(validation.Reasons, resolution.anchor.Reason)
		}
		validation.Obligations = notVerifiedFixObligations(validation.Anchor.Reason)
		return validation
	}

	frontierSet := map[string]verifyFrontierNode{}
	for _, node := range resolution.frontier {
		if node.CallerID != "" {
			frontierSet[node.CallerID] = node
		}
	}
	var connected []verifyFrontierNode
	for _, callerID := range changedCallerIDs {
		if node, ok := frontierSet[callerID]; ok {
			connected = append(connected, node)
		}
	}
	sortVerifyFrontierNodes(connected)

	connection := &VerifyConnection{
		Anchored:               true,
		ChangedPathsToBoundary: len(connected),
		ChangedCallerIDs:       append([]string{}, changedCallerIDs...),
	}
	for _, node := range resolution.frontier {
		connection.FrontierCallerIDs = append(connection.FrontierCallerIDs, node.CallerID)
	}
	connection.FrontierCallerIDs = uniqueNonEmptyStrings(connection.FrontierCallerIDs)
	validation.Connection = connection

	if resolution.clipped {
		validation.FixValidation = "not_verified"
		validation.Reasons = append(validation.Reasons, "anchor frontier was clipped by the configured graph budget")
		validation.Obligations = notVerifiedFixObligations("anchor frontier clipped")
		return validation
	}
	if len(validation.QueryErrors) > 0 {
		validation.FixValidation = "not_verified"
		validation.Obligations = notVerifiedFixObligations("anchor frontier lookup failed")
		return validation
	}

	anchorSubject := verifyAnchorSubject(resolution.anchor)
	if len(connected) == 0 {
		validation.Obligations = append(validation.Obligations, VerifyObligation{
			ID:         "O1",
			Class:      "fix_connects_to_boundary",
			Subject:    anchorSubject,
			Status:     "contradicted",
			Evidence:   resolution.anchor.Evidence,
			Resolution: "change code on an indexed path to the observed boundary",
		})
	} else {
		validation.Obligations = append(validation.Obligations, VerifyObligation{
			ID:       "O1",
			Class:    "fix_connects_to_boundary",
			Subject:  anchorSubject,
			Status:   "satisfied",
			Evidence: frontierEvidence(connected),
		})
	}

	predecessors := append([]verifyFrontierNode{}, resolution.predecessors...)
	sortVerifyFrontierNodes(predecessors)
	predecessorObligations, untouched := buildEquivalentPredecessorObligations(predecessors, changedSet, req.Waivers)
	validation.Obligations = append(validation.Obligations, predecessorObligations...)
	connection.UntouchedPathsToBoundary = untouched

	if len(connected) == 0 {
		validation.Obligations = append(validation.Obligations, VerifyObligation{
			ID:         "O5",
			Class:      "expected_boundary_coverage",
			Subject:    anchorSubject,
			Status:     "missing",
			Evidence:   resolution.anchor.Evidence,
			Resolution: "cover the supplied boundary from the changed execution path",
		})
	} else {
		validation.Obligations = append(validation.Obligations, VerifyObligation{
			ID:       "O5",
			Class:    "expected_boundary_coverage",
			Subject:  anchorSubject,
			Status:   "satisfied",
			Evidence: frontierEvidence(connected),
		})
	}
	validation.FixValidation = summarizeFixValidation(validation.Obligations)
	return validation
}

func buildEquivalentPredecessorObligations(predecessors []verifyFrontierNode, changedSet map[string]bool, waivers []VerifyWaiverInput) ([]VerifyObligation, int) {
	obligations := make([]VerifyObligation, 0, len(predecessors))
	untouched := 0
	for index, predecessor := range predecessors {
		status := "missing"
		resolutionText := "change this predecessor or attach an explicit waiver"
		justification := ""
		changed := changedSet[predecessor.CallerID]
		if !changed {
			untouched++
		}
		if changed {
			status = "satisfied"
			resolutionText = ""
		} else if waiver, ok := findVerifyWaiver(waivers, "equivalent_predecessor", predecessor.CallerID); ok {
			status = "waived"
			resolutionText = "waiver recorded; deterministic coverage was not proven"
			justification = strings.TrimSpace(waiver.Justification)
		}
		obligations = append(obligations, VerifyObligation{
			ID:            fmt.Sprintf("O2-%d", index+1),
			Class:         "equivalent_predecessor",
			Subject:       predecessor.CallerID,
			Status:        status,
			Evidence:      frontierEvidence([]verifyFrontierNode{predecessor}),
			Resolution:    resolutionText,
			Justification: justification,
		})
	}
	return obligations, untouched
}

func summarizeFixValidation(obligations []VerifyObligation) string {
	status := "verified"
	for _, obligation := range obligations {
		switch obligation.Status {
		case "missing", "contradicted":
			return "failed"
		case "not_verified":
			status = "not_verified"
		case "waived":
			if status == "verified" {
				status = "verified_with_waivers"
			}
		}
	}
	return status
}

const minBaseSHAPrefix = 7

// baseSHAMatches compares a supplied diff base with the indexed commit. Either
// side may be abbreviated, but an abbreviation must be at least 7 hex digits so
// a short or empty-looking value cannot match every commit; case is ignored.
func baseSHAMatches(indexSHA, baseSHA string) (bool, string) {
	index := strings.ToLower(strings.TrimSpace(indexSHA))
	base := strings.ToLower(strings.TrimSpace(baseSHA))
	if !isHexString(base) || len(base) < minBaseSHAPrefix {
		return false, fmt.Sprintf("diff base %q is not a commit id: use at least %d hex characters", baseSHA, minBaseSHAPrefix)
	}
	if strings.HasPrefix(index, base) || (len(index) >= minBaseSHAPrefix && isHexString(index) && strings.HasPrefix(base, index)) {
		return true, ""
	}
	return false, fmt.Sprintf("diff base %s does not match indexed workspace SHA %s", baseSHA, indexSHA)
}

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (h *Handlers) validateVerifyDiffBase(req ImpactRequest, scope searchWorkspaceScope) (bool, string, []string) {
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		return false, "", []string{"repo is required to align a diff with an indexed workspace"}
	}
	workspaceRepo, err := h.storage.GetWorkspaceRepo(scope.Workspace.ID, repo)
	if err != nil {
		return false, "", []string{"workspace repo lookup failed: " + err.Error()}
	}
	indexSHA := ""
	for _, snapshot := range scope.RepoContext {
		if snapshot.RepoName == repo && scope.ActiveSnapshots[snapshot.SnapshotID] {
			indexSHA = strings.TrimSpace(snapshot.SHA)
			break
		}
	}
	if indexSHA == "" {
		return false, "", []string{"repo has no active indexed snapshot for diff alignment"}
	}
	baseSHA := strings.TrimSpace(req.BaseSHA)
	if baseSHA == "" && req.AgentWork != nil {
		baseSHA = strings.TrimSpace(req.AgentWork.BaseSHA)
	}
	var alignmentErrors []string
	worktreeStatus, statusErr := workspacefs.InspectRepo(workspaceRepo.WorktreePath)
	if statusErr != nil {
		alignmentErrors = append(alignmentErrors, "cannot inspect active workspace worktree: "+statusErr.Error())
	} else {
		worktreeSHA := strings.TrimSpace(worktreeStatus.HeadSHA)
		if indexSHA != "" && worktreeSHA != "" && !strings.EqualFold(indexSHA, worktreeSHA) {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("active workspace worktree HEAD %s does not match indexed workspace SHA %s", worktreeSHA, indexSHA))
		}
		if worktreeStatus.Dirty {
			alignmentErrors = append(alignmentErrors, "active workspace worktree is dirty and is not an immutable indexed preimage")
		}
	}
	if baseSHA != "" && indexSHA != "" {
		if matched, reason := baseSHAMatches(indexSHA, baseSHA); !matched {
			alignmentErrors = append(alignmentErrors, reason)
		}
	}
	preimageErrors, checkedLines := verifyDiffPreimage(req.Diff, workspaceRepo.WorktreePath)
	alignmentErrors = append(alignmentErrors, preimageErrors...)
	if baseSHA == "" && checkedLines == 0 {
		alignmentErrors = append(alignmentErrors, "diff has no verifiable preimage context and baseSha was not supplied")
	}
	alignmentErrors = uniqueNonEmptyStrings(alignmentErrors)
	return len(alignmentErrors) == 0, indexSHA, alignmentErrors
}

func (h *Handlers) resolveVerifyAnchor(ctx context.Context, input VerifyAnchorInput, depth, maxNodes int, scope searchWorkspaceScope) verifyAnchorResolution {
	kind := normalizeVerifyAnchorKind(input.Kind)
	resolution := verifyAnchorResolution{anchor: VerifyResolvedAnchor{
		Kind: kind,
		Ref:  strings.TrimSpace(input.Ref),
		Repo: strings.TrimSpace(input.Repo),
	}}
	if resolution.anchor.Ref == "" || kind == "" {
		resolution.anchor.Status = "invalid"
		resolution.anchor.Reason = "anchor requires kind and ref"
		return resolution
	}

	var err error
	switch kind {
	case "symbol":
		resolution.boundaryIDs, resolution.anchor.Evidence, err = h.resolveVerifySymbolAnchor(ctx, resolution.anchor, scope)
	case "endpoint":
		resolution.boundaryIDs, resolution.anchor.Evidence, err = h.resolveVerifyEndpointAnchor(ctx, resolution.anchor, scope, maxNodes)
	case "entity":
		resolution.predecessors, err = h.resolveVerifyEntityPredecessors(ctx, resolution.anchor, scope, maxNodes)
	case "queue":
		resolution.predecessors, err = h.resolveVerifyQueuePredecessors(ctx, resolution.anchor, scope, maxNodes)
	default:
		resolution.anchor.Status = "invalid"
		resolution.anchor.Reason = "unsupported anchor kind: " + kind
		return resolution
	}
	if err != nil {
		resolution.clipped = errors.Is(err, errVerifyFrontierClipped)
		resolution.anchor.Status = "not_verified"
		resolution.anchor.Reason = err.Error()
		resolution.queryErrors = append(resolution.queryErrors, err.Error())
		return resolution
	}

	if kind == "symbol" || kind == "endpoint" {
		if len(resolution.boundaryIDs) != 1 {
			resolution.anchor.Status = "invalid"
			if len(resolution.boundaryIDs) == 0 {
				resolution.anchor.Reason = "anchor does not resolve in the active workspace"
			} else {
				resolution.anchor.Reason = "anchor is ambiguous in the active workspace"
			}
			return resolution
		}
		resolution.anchor.CallerIDs = append([]string{}, resolution.boundaryIDs...)
		resolution.predecessors, resolution.frontier, resolution.clipped, err = h.symbolBoundaryFrontier(ctx, resolution.boundaryIDs[0], depth, maxNodes, scope)
		if err != nil {
			resolution.queryErrors = append(resolution.queryErrors, err.Error())
		}
	} else {
		if len(resolution.predecessors) == 0 {
			resolution.anchor.Status = "invalid"
			resolution.anchor.Reason = "anchor has no indexed typed predecessors in the active workspace"
			return resolution
		}
		resolution.anchor.Evidence = frontierEvidence(resolution.predecessors)
		resolution.frontier, resolution.clipped, resolution.queryErrors = h.predecessorFrontier(ctx, resolution.predecessors, depth, maxNodes, scope)
	}
	resolution.anchor.Status = "resolved"
	return resolution
}

func (h *Handlers) resolveVerifySymbolAnchor(ctx context.Context, anchor VerifyResolvedAnchor, scope searchWorkspaceScope) ([]string, []VerifyObligationEvidence, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	ref := strings.TrimSpace(anchor.Ref)
	repo := strings.TrimSpace(anchor.Repo)
	file := ""
	name := ref
	if parsedRepo, parsedFile, parsedName := trace.ParseCallerID(ref); parsedRepo != "" && parsedFile != "" && parsedName != "" {
		repo, file, name = parsedRepo, parsedFile, parsedName
	}
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, fi.path, f.name, f.start_line
		FROM functions f
		JOIN files fi ON fi.id = f.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE ($1 = '' OR r.name = $1)
		  AND ($2 = '' OR fi.path = $2)
		  AND (LOWER(f.name) = LOWER($3) OR LOWER(COALESCE(f.simple_name, '')) = LOWER($3))
		  AND ($4::bigint[] IS NULL OR fi.snapshot_id = ANY($4))
		ORDER BY r.name, fi.path, f.start_line
		LIMIT 20`, repo, file, name, snapshotIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("symbol anchor lookup failed: %w", err)
	}
	defer rows.Close()
	var ids []string
	var evidence []VerifyObligationEvidence
	for rows.Next() {
		var foundRepo, foundFile, foundName string
		var line int
		if err := rows.Scan(&foundRepo, &foundFile, &foundName, &line); err != nil {
			return nil, nil, fmt.Errorf("symbol anchor scan failed: %w", err)
		}
		callerID := trace.BuildCallerID(foundRepo, foundFile, foundName)
		ids = append(ids, callerID)
		evidence = append(evidence, VerifyObligationEvidence{Kind: "symbol", Repo: foundRepo, File: foundFile, Symbol: foundName, CallerID: callerID, Line: line})
	}
	return uniqueNonEmptyStrings(ids), evidence, rows.Err()
}

func (h *Handlers) resolveVerifyEndpointAnchor(ctx context.Context, anchor VerifyResolvedAnchor, scope searchWorkspaceScope, maxNodes int) ([]string, []VerifyObligationEvidence, error) {
	method, path, ok := parseFlowStart(anchor.Ref)
	if !ok {
		path = strings.TrimSpace(anchor.Ref)
		method = ""
	}
	path = canonicalFlowEndpointPath(path)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, fi.path, fn.name, COALESCE(e.line_number, fn.start_line)
		FROM endpoints e JOIN functions fn ON fn.id = e.handler_function_id
		JOIN files fi ON fi.id = fn.file_id JOIN repositories r ON r.id = fi.repo_id
		WHERE ($1 = '' OR r.name = $1)
		  AND lower('/' || trim(both '/' FROM regexp_replace(replace(trim(e.path), chr(92), '/'), '/+', '/', 'g'))) = $2
		  AND ($3 = '' OR upper(e.method) = upper($3))
		  AND ($4::bigint[] IS NULL OR fi.snapshot_id = ANY($4))
		ORDER BY r.name, fi.path, fn.name LIMIT $5`, anchor.Repo, path, method, activeSnapshotIDs(scope), maxNodes+1)
	if err != nil {
		return nil, nil, fmt.Errorf("endpoint anchor lookup failed: %w", err)
	}
	defer rows.Close()
	var ids []string
	var evidence []VerifyObligationEvidence
	for rows.Next() {
		var repo, file, name string
		var line int
		if err := rows.Scan(&repo, &file, &name, &line); err != nil {
			return nil, nil, err
		}
		callerID := trace.BuildCallerID(repo, file, name)
		ids = append(ids, callerID)
		evidence = append(evidence, VerifyObligationEvidence{Kind: "endpoint", Repo: repo, File: file, Symbol: name, CallerID: callerID, Line: line})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(ids) > maxNodes {
		return nil, nil, errVerifyFrontierClipped
	}
	return uniqueNonEmptyStrings(ids), evidence, nil
}

func (h *Handlers) resolveVerifyEntityPredecessors(ctx context.Context, anchor VerifyResolvedAnchor, scope searchWorkspaceScope, maxNodes int) ([]verifyFrontierNode, error) {
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, da.caller_id, COALESCE(MIN(da.line_number), 0)
		FROM data_accesses da
		JOIN repositories r ON r.id = da.repo_id
		WHERE LOWER(da.entity_name) = LOWER($1)
		  AND LOWER(da.access) IN ('write','insert','update','delete','merge','save')
		  AND ($2 = '' OR r.name = $2)
		  AND ($3::bigint[] IS NULL OR da.snapshot_id = ANY($3))
		GROUP BY r.name, da.caller_id
		ORDER BY r.name, da.caller_id
		LIMIT $4`, anchor.Ref, anchor.Repo, activeSnapshotIDs(scope), maxNodes+1)
	if err != nil {
		return nil, fmt.Errorf("entity predecessor lookup failed: %w", err)
	}
	defer rows.Close()
	var out []verifyFrontierNode
	for rows.Next() {
		var repo, callerID string
		var line int
		if err := rows.Scan(&repo, &callerID, &line); err != nil {
			return nil, fmt.Errorf("entity predecessor scan failed: %w", err)
		}
		out = append(out, frontierNodeFromCallerID(callerID, "entity_write", line))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxNodes {
		return nil, errVerifyFrontierClipped
	}
	return uniqueVerifyFrontierNodes(out), nil
}

func (h *Handlers) resolveVerifyQueuePredecessors(ctx context.Context, anchor VerifyResolvedAnchor, scope searchWorkspaceScope, maxNodes int) ([]verifyFrontierNode, error) {
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, q.caller_id, COALESCE(MIN(q.line_number), 0)
		FROM sqs_producers q
		JOIN repositories r ON r.id = q.repo_id
		WHERE LOWER(TRIM(BOTH '%' FROM q.queue_name)) = LOWER(TRIM(BOTH '%' FROM $1))
		  AND ($2 = '' OR r.name = $2)
		  AND ($3::bigint[] IS NULL OR q.snapshot_id = ANY($3))
		GROUP BY r.name, q.caller_id
		ORDER BY r.name, q.caller_id
		LIMIT $4`, anchor.Ref, anchor.Repo, activeSnapshotIDs(scope), maxNodes+1)
	if err != nil {
		return nil, fmt.Errorf("queue predecessor lookup failed: %w", err)
	}
	defer rows.Close()
	var out []verifyFrontierNode
	for rows.Next() {
		var repo, callerID string
		var line int
		if err := rows.Scan(&repo, &callerID, &line); err != nil {
			return nil, fmt.Errorf("queue predecessor scan failed: %w", err)
		}
		out = append(out, frontierNodeFromCallerID(callerID, "queue_producer", line))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxNodes {
		return nil, errVerifyFrontierClipped
	}
	return uniqueVerifyFrontierNodes(out), nil
}

func frontierNodeFromCallerID(callerID, kind string, line int) verifyFrontierNode {
	repo, file, symbol := trace.ParseCallerID(callerID)
	return verifyFrontierNode{CallerID: callerID, Kind: kind, Repo: repo, File: file, Symbol: symbol, Line: line}
}

func frontierEvidence(nodes []verifyFrontierNode) []VerifyObligationEvidence {
	out := make([]VerifyObligationEvidence, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, VerifyObligationEvidence{Kind: node.Kind, Repo: node.Repo, File: node.File, Symbol: node.Symbol, CallerID: node.CallerID, Line: node.Line})
	}
	return out
}

func uniqueVerifyFrontierNodes(nodes []verifyFrontierNode) []verifyFrontierNode {
	seen := map[string]bool{}
	var out []verifyFrontierNode
	for _, node := range nodes {
		key := node.Kind + "\x00" + node.CallerID
		if node.CallerID == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, node)
	}
	sortVerifyFrontierNodes(out)
	return out
}

func sortVerifyFrontierNodes(nodes []verifyFrontierNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].CallerID == nodes[j].CallerID {
			return nodes[i].Kind < nodes[j].Kind
		}
		return nodes[i].CallerID < nodes[j].CallerID
	})
}

func normalizeVerifyAnchorKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "function", "caller", "symbol":
		return "symbol"
	case "endpoint", "route":
		return "endpoint"
	case "entity", "table":
		return "entity"
	case "queue", "topic":
		return "queue"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

func notVerifiedFixObligations(reason string) []VerifyObligation {
	return []VerifyObligation{
		{ID: "O1", Class: "fix_connects_to_boundary", Subject: "observed boundary", Status: "not_verified", Resolution: reason},
		{ID: "O2", Class: "equivalent_predecessor", Subject: "boundary predecessors", Status: "not_verified", Resolution: reason},
		{ID: "O5", Class: "expected_boundary_coverage", Subject: "observed boundary", Status: "not_verified", Resolution: reason},
	}
}

func verifyAnchorSubject(anchor VerifyResolvedAnchor) string {
	if anchor.Repo != "" {
		return anchor.Kind + ":" + anchor.Repo + ":" + anchor.Ref
	}
	return anchor.Kind + ":" + anchor.Ref
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = true
		}
	}
	return out
}

func findVerifyWaiver(waivers []VerifyWaiverInput, class, subject string) (VerifyWaiverInput, bool) {
	for _, waiver := range waivers {
		if strings.TrimSpace(waiver.Subject) != subject {
			continue
		}
		if waiver.Class != "" && !strings.EqualFold(strings.TrimSpace(waiver.Class), class) {
			continue
		}
		if strings.TrimSpace(waiver.Justification) == "" {
			continue
		}
		return waiver, true
	}
	return VerifyWaiverInput{}, false
}
