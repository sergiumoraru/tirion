package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sergiumoraru/tirion/internal/diffparse"
	"github.com/sergiumoraru/tirion/internal/trace"
)

type EstateVerifyResult struct {
	Verdict          string                     `json:"verdict"`
	Mode             string                     `json:"mode"`
	BreakingChanges  []VerifyBreakingChange     `json:"breakingChanges,omitempty"`
	ExposedSurface   EstateVerifyExposedSurface `json:"exposedSurface"`
	ChangeValidation *AgentChangeValidation     `json:"changeValidation,omitempty"`
	AgentWork        *AgentWorkValidation       `json:"agentWork,omitempty"`
	BlastSummary     ImpactSummary              `json:"blastSummary"`
	Completeness     ImpactCompleteness         `json:"completeness"`
	Reasons          []string                   `json:"reasons,omitempty"`
	QueryErrors      []string                   `json:"queryErrors,omitempty"`
}

type AgentWorkSubmission struct {
	BaseSHA              string              `json:"baseSha,omitempty"`
	RootCause            string              `json:"rootCause"`
	Task                 string              `json:"task"`
	RequirementsComplete bool                `json:"requirementsComplete"`
	RootCauseEvidence    []AgentWorkEvidence `json:"rootCauseEvidence"`
	Claims               []AgentWorkClaim    `json:"claims"`
}

type AgentWorkClaim struct {
	ID          string              `json:"id"`
	Requirement string              `json:"requirement"`
	Status      string              `json:"status"`
	Rationale   string              `json:"rationale,omitempty"`
	Evidence    []AgentWorkEvidence `json:"evidence,omitempty"`
}

type AgentWorkEvidence struct {
	Kind   string `json:"kind"`
	Repo   string `json:"repo,omitempty"`
	File   string `json:"file"`
	Symbol string `json:"symbol,omitempty"`
	Line   int    `json:"line,omitempty"`
}

type AgentWorkValidation struct {
	Status               string                        `json:"status"`
	BaseSHA              string                        `json:"baseSha,omitempty"`
	IndexSHA             string                        `json:"indexSha,omitempty"`
	DiffAligned          bool                          `json:"diffAligned"`
	AlignmentErrors      []string                      `json:"alignmentErrors,omitempty"`
	TaskPresent          bool                          `json:"taskPresent"`
	RootCauseGrounded    bool                          `json:"rootCauseGrounded"`
	RootCauseConnected   bool                          `json:"rootCauseConnected"`
	RequirementsComplete bool                          `json:"requirementsComplete"`
	Claims               []AgentWorkClaimValidation    `json:"claims,omitempty"`
	Evidence             []AgentWorkEvidenceValidation `json:"evidence,omitempty"`
	ChangedFiles         []AgentWorkChangedFile        `json:"changedFiles,omitempty"`
	UnaccountedChanges   []AgentWorkChangedFile        `json:"unaccountedChanges,omitempty"`
	Reasons              []string                      `json:"reasons,omitempty"`
}

type AgentWorkClaimValidation struct {
	ID          string   `json:"id"`
	Requirement string   `json:"requirement"`
	Status      string   `json:"status"`
	Verdict     string   `json:"verdict"`
	Reasons     []string `json:"reasons,omitempty"`
}

type AgentWorkEvidenceValidation struct {
	ClaimID  string `json:"claimId,omitempty"`
	Role     string `json:"role"`
	Kind     string `json:"kind,omitempty"`
	Repo     string `json:"repo"`
	File     string `json:"file"`
	Symbol   string `json:"symbol,omitempty"`
	CallerID string `json:"callerId,omitempty"`
	Line     int    `json:"line,omitempty"`
	Resolved bool   `json:"resolved"`
	Changed  bool   `json:"changed"`
	Detail   string `json:"detail"`
}

type AgentWorkChangedFile struct {
	Repo string `json:"repo"`
	File string `json:"file"`
}

type EstateVerifyExposedSurface struct {
	Endpoints []VerifyEndpointExposure `json:"endpoints,omitempty"`
	Queues    []VerifyQueueExposure    `json:"queues,omitempty"`
	Entities  []VerifyEntityExposure   `json:"entities,omitempty"`
}

type VerifyEndpointExposure struct {
	Endpoint      string                  `json:"endpoint"`
	Method        string                  `json:"method"`
	Path          string                  `json:"path"`
	Repo          string                  `json:"repo"`
	Handler       string                  `json:"handler"`
	File          string                  `json:"file"`
	Line          int                     `json:"line,omitempty"`
	Consumers     []VerifySurfaceConsumer `json:"consumers,omitempty"`
	ConsumerRepos int                     `json:"consumerRepos"`
}

type VerifySurfaceConsumer struct {
	Repo     string `json:"repo"`
	CallerID string `json:"callerId,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

type VerifyQueueExposure struct {
	Queue             string   `json:"queue"`
	Direction         string   `json:"direction"`
	Repo              string   `json:"repo"`
	CallerID          string   `json:"callerId,omitempty"`
	Line              int      `json:"line,omitempty"`
	CounterpartyRepos []string `json:"counterpartyRepos,omitempty"`
}

type VerifyEntityExposure struct {
	Entity       string                  `json:"entity"`
	Access       string                  `json:"access"`
	Repo         string                  `json:"repo"`
	CallerID     string                  `json:"callerId,omitempty"`
	Line         int                     `json:"line,omitempty"`
	OtherReaders []VerifySurfaceConsumer `json:"otherReaders,omitempty"`
	OtherWriters []VerifySurfaceConsumer `json:"otherWriters,omitempty"`
}

type VerifyBreakingChange struct {
	Endpoint      string              `json:"endpoint"`
	Handler       string              `json:"handler"`
	Repo          string              `json:"repo"`
	File          string              `json:"file"`
	ChangeType    string              `json:"changeType"`
	RemovedParams []string            `json:"removedParams"`
	AddedParams   []string            `json:"addedParams,omitempty"`
	ChangedParams []VerifyParamChange `json:"changedParams,omitempty"`
	Severity      string              `json:"severity"`
	Detail        string              `json:"detail"`
	Evidence      VerifyLineEvidence  `json:"evidence"`
}

type VerifyParamChange struct {
	Name    string `json:"name"`
	OldType string `json:"oldType"`
	NewType string `json:"newType"`
}

type VerifyLineEvidence struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
}

type VerifyRunRecord struct {
	ID                     int64     `json:"id"`
	WorkspaceID            string    `json:"workspaceId"`
	Repo                   string    `json:"repo"`
	Verdict                string    `json:"verdict"`
	Mode                   string    `json:"mode"`
	WorkStatus             string    `json:"workStatus,omitempty"`
	ClaimCount             int       `json:"claimCount"`
	UnaccountedChangeCount int       `json:"unaccountedChangeCount"`
	BreakingCount          int       `json:"breakingCount"`
	EndpointCount          int       `json:"endpointCount"`
	QueueCount             int       `json:"queueCount"`
	EntityCount            int       `json:"entityCount"`
	ConsumerRepoCount      int       `json:"consumerRepoCount"`
	QueryErrorCount        int       `json:"queryErrorCount"`
	ChangeTypes            []string  `json:"changeTypes"`
	Reasons                []string  `json:"reasons"`
	DurationMS             int64     `json:"durationMs"`
	CreatedAt              time.Time `json:"createdAt"`
}

type VerifyRunsResponse struct {
	Workspace ResponseWorkspace `json:"workspace"`
	Runs      []VerifyRunRecord `json:"runs"`
}

func buildEmptyEstateVerify(maxNodes int, warnings []string, reason string) *EstateVerifyResult {
	reasons := append([]string{}, warnings...)
	if strings.TrimSpace(reason) != "" {
		reasons = append(reasons, reason)
	}
	return &EstateVerifyResult{
		Verdict: "warn",
		Mode:    "contracts_only",
		Completeness: ImpactCompleteness{
			AppliedMaxNodes: maxNodes,
		},
		Reasons: uniqueNonEmptyStrings(reasons),
	}
}

func (h *Handlers) buildEstateVerify(ctx context.Context, req ImpactRequest, callerIDs []string, changedEndpoints []ImpactEndpoint, summary ImpactSummary, report ImpactReport, completeness ImpactCompleteness, scope searchWorkspaceScope) *EstateVerifyResult {
	result := &EstateVerifyResult{
		Verdict:      "pass",
		Mode:         "contracts_only",
		BlastSummary: summary,
		Completeness: completeness,
	}
	var queryErrors []string
	result.ExposedSurface.Endpoints, queryErrors = h.verifyEndpointExposures(ctx, changedEndpoints, scope)
	var queueErrors []string
	result.ExposedSurface.Queues, queueErrors = h.verifyQueueExposures(ctx, callerIDs, scope)
	queryErrors = append(queryErrors, queueErrors...)
	var entityErrors []string
	result.ExposedSurface.Entities, entityErrors = h.verifyEntityExposures(ctx, callerIDs, scope)
	queryErrors = append(queryErrors, entityErrors...)
	if strings.TrimSpace(req.Diff) != "" {
		result.BreakingChanges = detectVerifyEndpointBreakingChanges(req.Diff, changedEndpoints)
	}
	result.AgentWork = h.validateAgentWork(ctx, req, callerIDs, report, scope)
	result.ChangeValidation = h.validateAgentChange(ctx, req, callerIDs, scope)
	if result.ChangeValidation != nil {
		result.Mode = result.ChangeValidation.Mode
		queryErrors = append(queryErrors, result.ChangeValidation.QueryErrors...)
	}

	var reasons []string
	for _, change := range result.BreakingChanges {
		exposure := findEndpointExposure(result.ExposedSurface.Endpoints, change.Repo, change.Endpoint)
		if exposure.ConsumerRepos > 0 {
			result.Verdict = "fail"
			reasons = append(reasons, describeVerifyBreakingReason(change, exposure.ConsumerRepos, true))
		} else if result.Verdict != "fail" {
			result.Verdict = "warn"
			reasons = append(reasons, describeVerifyBreakingReason(change, 0, false))
		}
	}
	if result.Verdict != "fail" {
		for _, endpoint := range result.ExposedSurface.Endpoints {
			markVerifyExposure(result)
			if endpoint.ConsumerRepos > 0 {
				reasons = append(reasons, fmt.Sprintf("%s has %d external consumer repo(s)", endpoint.Endpoint, endpoint.ConsumerRepos))
			} else {
				reasons = append(reasons, fmt.Sprintf("%s is an exposed endpoint changed by this diff; no indexed external consumer was found", endpoint.Endpoint))
			}
		}
		for _, queue := range result.ExposedSurface.Queues {
			if len(queue.CounterpartyRepos) > 0 {
				markVerifyExposure(result)
				reasons = append(reasons, fmt.Sprintf("%s queue %s has counterparties in %s", queue.Direction, queue.Queue, strings.Join(queue.CounterpartyRepos, ", ")))
			}
		}
		for _, entity := range result.ExposedSurface.Entities {
			if len(entity.OtherReaders)+len(entity.OtherWriters) > 0 {
				markVerifyExposure(result)
				reasons = append(reasons, fmt.Sprintf("%s %s is shared with other repo accessors", entity.Access, entity.Entity))
			}
		}
	}
	applyAgentChangeVerdict(result, &reasons)
	// The v2 claims gate remains available only for legacy requests. Once an
	// anchor is supplied, the graph-derived obligation ledger owns the verdict.
	if req.Anchor == nil && req.AgentWork != nil {
		applyAgentWorkVerdict(result, &reasons)
	}
	if len(req.diffCoverage) > 0 {
		markVerifyIncomplete(result)
		reasons = append(reasons, req.diffCoverage...)
	}
	// Informational only: never changes the verdict.
	reasons = append(reasons, req.diffNotes...)
	if completeness.Truncated {
		markVerifyIncomplete(result)
		reasons = append(reasons, "impact graph was truncated; verify result may be incomplete")
	}
	for _, queryError := range queryErrors {
		queryError = strings.TrimSpace(queryError)
		if queryError == "" {
			continue
		}
		markVerifyIncomplete(result)
		reasons = append(reasons, "verify incomplete: "+queryError)
	}
	result.QueryErrors = uniqueNonEmptyStrings(queryErrors)
	result.Reasons = uniqueNonEmptyStrings(reasons)
	return result
}

func applyAgentChangeVerdict(result *EstateVerifyResult, reasons *[]string) {
	if result == nil || result.ChangeValidation == nil {
		return
	}
	validation := result.ChangeValidation
	*reasons = append(*reasons, validation.Reasons...)
	if !validation.Preconditions.DiffAligned {
		markVerifyIncomplete(result)
		*reasons = append(*reasons, validation.Preconditions.AlignmentErrors...)
	}
	if len(validation.QueryErrors) > 0 || validation.FrontierClipped || validation.FixValidation == "not_verified" {
		markVerifyIncomplete(result)
	}
	hasWaiver := false
	for _, obligation := range validation.Obligations {
		switch obligation.Status {
		case "contradicted", "missing":
			result.Verdict = "fail"
			*reasons = append(*reasons, fmt.Sprintf("%s %s: %s", obligation.ID, obligation.Status, obligation.Subject))
		case "not_verified":
			markVerifyIncomplete(result)
		case "waived":
			hasWaiver = true
			*reasons = append(*reasons, fmt.Sprintf("%s waived: %s", obligation.ID, obligation.Subject))
		}
	}
	if hasWaiver && result.Verdict == "pass" {
		result.Verdict = "info"
	}
}

func applyAgentWorkVerdict(result *EstateVerifyResult, reasons *[]string) {
	if result == nil {
		return
	}
	if result.AgentWork == nil {
		markVerifyIncomplete(result)
		*reasons = append(*reasons, "agent-work proof was not supplied; only estate exposure was checked")
		return
	}
	*reasons = append(*reasons, result.AgentWork.Reasons...)
	switch result.AgentWork.Status {
	case "contradicted":
		result.Verdict = "fail"
	case "incomplete":
		markVerifyIncomplete(result)
	}
}

func (h *Handlers) validateAgentWork(ctx context.Context, req ImpactRequest, changedCallerIDs []string, report ImpactReport, scope searchWorkspaceScope) *AgentWorkValidation {
	if req.AgentWork == nil {
		return nil
	}
	work := req.AgentWork
	validation := &AgentWorkValidation{
		Status:               "verified",
		TaskPresent:          strings.TrimSpace(work.Task) != "",
		RequirementsComplete: work.RequirementsComplete,
	}
	changedRanges := mergeImpactRanges(parseUnifiedDiffRanges(req.Diff, req.Repo))
	newFiles := agentWorkAddedFiles(req.Diff, req.Repo)
	validation.ChangedFiles = agentWorkChangedFiles(changedRanges, false)
	validation.BaseSHA = strings.TrimSpace(work.BaseSHA)
	validation.DiffAligned, validation.IndexSHA, validation.AlignmentErrors = h.validateAgentWorkDiffBase(req, scope)
	if !validation.DiffAligned {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "diff does not align with the active workspace snapshot; source evidence cannot be trusted")
	}

	if !validation.TaskPresent {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "agent work is missing the task being implemented")
	}
	if strings.TrimSpace(work.RootCause) == "" {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "agent work is missing the diagnosed root-cause statement")
	}
	if !validation.RequirementsComplete {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "agent did not attest that every task requirement is represented by a claim")
	}

	var rootCauseCallerIDs []string
	for _, evidence := range work.RootCauseEvidence {
		checked, queryErr := h.validateAgentWorkEvidence(ctx, evidence, "root_cause", "", req.Repo, changedRanges, newFiles, scope)
		validation.Evidence = append(validation.Evidence, checked)
		if queryErr != nil {
			validation.Status = "incomplete"
			validation.Reasons = append(validation.Reasons, "root-cause evidence lookup failed: "+queryErr.Error())
		}
		if checked.Resolved {
			validation.RootCauseGrounded = true
			if checked.CallerID != "" {
				rootCauseCallerIDs = append(rootCauseCallerIDs, checked.CallerID)
			}
		}
	}
	if len(work.RootCauseEvidence) == 0 {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "no root-cause evidence was supplied")
	} else if !validation.RootCauseGrounded {
		validation.Status = "contradicted"
		validation.Reasons = append(validation.Reasons, "none of the submitted root-cause evidence resolves in the active workspace")
	}
	if validation.RootCauseGrounded {
		if !agentWorkEvidenceConnected(changedCallerIDs, rootCauseCallerIDs, report) {
			if validation.Status == "verified" {
				validation.Status = "incomplete"
			}
			validation.Reasons = append(validation.Reasons, "root-cause evidence resolves but is not connected to the changed execution path in the active graph")
		} else {
			validation.RootCauseConnected = true
		}
	}

	accounted := map[string]bool{}
	if len(work.Claims) == 0 {
		validation.Status = "incomplete"
		validation.Reasons = append(validation.Reasons, "no requirement claims were supplied")
	}
	seenClaimIDs := map[string]bool{}
	for index, claim := range work.Claims {
		claimID := strings.TrimSpace(claim.ID)
		if claimID == "" {
			claimID = fmt.Sprintf("claim-%d", index+1)
		}
		claimValidation := AgentWorkClaimValidation{
			ID:          claimID,
			Requirement: strings.TrimSpace(claim.Requirement),
			Status:      strings.ToLower(strings.TrimSpace(claim.Status)),
			Verdict:     "grounded",
		}
		if seenClaimIDs[claimID] {
			claimValidation.Verdict = "contradicted"
			claimValidation.Reasons = append(claimValidation.Reasons, "claim id is duplicated")
		}
		seenClaimIDs[claimID] = true
		if claimValidation.Requirement == "" {
			claimValidation.Verdict = "incomplete"
			claimValidation.Reasons = append(claimValidation.Reasons, "requirement text is missing")
		}

		resolvedCount := 0
		changedCount := 0
		changedImplementationCount := 0
		for _, evidence := range claim.Evidence {
			checked, queryErr := h.validateAgentWorkEvidence(ctx, evidence, "claim", claimID, req.Repo, changedRanges, newFiles, scope)
			validation.Evidence = append(validation.Evidence, checked)
			if queryErr != nil {
				claimValidation.Verdict = "incomplete"
				claimValidation.Reasons = append(claimValidation.Reasons, "evidence lookup failed: "+queryErr.Error())
				continue
			}
			if !checked.Resolved {
				claimValidation.Verdict = "contradicted"
				claimValidation.Reasons = append(claimValidation.Reasons, fmt.Sprintf("evidence does not resolve: %s:%s:%s", checked.Repo, checked.File, checked.Symbol))
				continue
			}
			resolvedCount++
			if checked.Changed {
				changedCount++
				accounted[agentWorkFileKey(checked.Repo, checked.File)] = true
				if strings.EqualFold(strings.TrimSpace(evidence.Kind), "implementation") {
					changedImplementationCount++
				}
			}
		}

		switch claimValidation.Status {
		case "covered":
			if resolvedCount == 0 || changedImplementationCount == 0 {
				claimValidation.Verdict = "contradicted"
				claimValidation.Reasons = append(claimValidation.Reasons, "covered claim must cite at least one changed, source-resolved evidence item with kind=implementation")
			}
		case "preserved":
			if resolvedCount == 0 {
				claimValidation.Verdict = "contradicted"
				claimValidation.Reasons = append(claimValidation.Reasons, "preserved claim must cite at least one source-resolved path")
			}
			if changedCount > 0 {
				claimValidation.Verdict = "contradicted"
				claimValidation.Reasons = append(claimValidation.Reasons, "preserved claim cites code changed by this diff")
			}
		case "not_applicable":
			if strings.TrimSpace(claim.Rationale) == "" {
				claimValidation.Verdict = "incomplete"
				claimValidation.Reasons = append(claimValidation.Reasons, "not_applicable claim requires a rationale")
			}
		default:
			claimValidation.Verdict = "contradicted"
			claimValidation.Reasons = append(claimValidation.Reasons, "status must be covered, preserved, or not_applicable")
		}

		validation.Claims = append(validation.Claims, claimValidation)
		switch claimValidation.Verdict {
		case "contradicted":
			validation.Status = "contradicted"
		case "incomplete":
			if validation.Status == "verified" {
				validation.Status = "incomplete"
			}
		}
	}

	for _, changed := range validation.ChangedFiles {
		if accounted[agentWorkFileKey(changed.Repo, changed.File)] {
			continue
		}
		validation.UnaccountedChanges = append(validation.UnaccountedChanges, changed)
	}
	if len(validation.UnaccountedChanges) > 0 {
		if validation.Status == "verified" {
			validation.Status = "incomplete"
		}
		validation.Reasons = append(validation.Reasons, fmt.Sprintf("%d changed production file(s) are not cited by a covered claim", len(validation.UnaccountedChanges)))
	}
	if !validation.DiffAligned {
		validation.RootCauseConnected = false
		for i := range validation.Claims {
			if validation.Claims[i].Verdict == "grounded" {
				validation.Claims[i].Verdict = "untrusted"
				validation.Claims[i].Reasons = append(validation.Claims[i].Reasons, "diff preimage does not match the active workspace")
			}
		}
	}
	validation.Reasons = uniqueNonEmptyStrings(validation.Reasons)
	return validation
}

func (h *Handlers) validateAgentWorkEvidence(ctx context.Context, evidence AgentWorkEvidence, role, claimID, defaultRepo string, changedRanges []ImpactRange, newFiles map[string]string, scope searchWorkspaceScope) (AgentWorkEvidenceValidation, error) {
	repo := strings.TrimSpace(evidence.Repo)
	if repo == "" {
		repo = strings.TrimSpace(defaultRepo)
	}
	file := normalizeDiffPath(evidence.File)
	checked := AgentWorkEvidenceValidation{
		ClaimID: claimID,
		Role:    role,
		Kind:    strings.TrimSpace(evidence.Kind),
		Repo:    repo,
		File:    file,
		Symbol:  strings.TrimSpace(evidence.Symbol),
		Line:    evidence.Line,
	}
	if repo == "" || file == "" {
		checked.Detail = "repo and file are required"
		return checked, nil
	}
	if addedContent, added := newFiles[agentWorkFileKey(repo, file)]; added {
		if role == "root_cause" {
			checked.Detail = "root-cause evidence must exist before the change; this file is newly added"
			return checked, nil
		}
		if checked.Symbol != "" && !diffContentContainsSymbol(addedContent, checked.Symbol) {
			checked.Detail = "new file is present in the diff, but the cited symbol is not present in its added source"
			return checked, nil
		}
		checked.Resolved = true
		checked.Changed = true
		checked.Detail = "new file is grounded by added diff source"
		return checked, nil
	}

	snapshotIDs := activeSnapshotIDs(scope)
	if checked.Symbol == "" {
		var exists bool
		err := h.storage.Pool().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM files fi
				JOIN repositories r ON r.id = fi.repo_id
				WHERE r.name = $1 AND fi.path = $2
				  AND `+integrationSnapshotClause("fi.snapshot_id", 3, workspaceIncludesLegacy(scope))+`
			)
		`, repo, file, snapshotIDs).Scan(&exists)
		if err != nil {
			return checked, err
		}
		checked.Resolved = exists
		checked.Changed = agentWorkEvidenceChanged(repo, file, evidence.Line, 0, 0, changedRanges)
		if exists {
			checked.Detail = "file resolves in the active workspace"
		} else {
			checked.Detail = "file does not resolve in the active workspace"
		}
		return checked, nil
	}

	var resolvedName string
	var startLine, endLine int
	err := h.storage.Pool().QueryRow(ctx, `
		SELECT fn.name, fn.start_line, fn.end_line
		FROM functions fn
		JOIN files fi ON fi.id = fn.file_id
		JOIN repositories r ON r.id = fi.repo_id
		WHERE r.name = $1
		  AND fi.path = $2
		  AND (LOWER(fn.name) = LOWER($3) OR LOWER(fn.simple_name) = LOWER($3) OR LOWER(fn.name) LIKE '%.' || LOWER($3))
		  AND `+integrationSnapshotClause("fi.snapshot_id", 4, workspaceIncludesLegacy(scope))+`
		ORDER BY CASE WHEN LOWER(fn.name) = LOWER($3) THEN 0 ELSE 1 END, fn.start_line
		LIMIT 1
	`, repo, file, checked.Symbol, snapshotIDs).Scan(&resolvedName, &startLine, &endLine)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			checked.Detail = "symbol does not resolve in the active workspace"
			return checked, nil
		}
		return checked, err
	}
	checked.Resolved = true
	checked.Symbol = resolvedName
	checked.CallerID = trace.BuildCallerID(repo, file, resolvedName)
	checked.Changed = agentWorkEvidenceChanged(repo, file, 0, startLine, endLine, changedRanges)
	checked.Detail = fmt.Sprintf("symbol resolves at lines %d-%d", startLine, endLine)
	if evidence.Line > 0 && (evidence.Line < startLine || evidence.Line > endLine) {
		checked.Resolved = false
		checked.Detail = fmt.Sprintf("cited line %d is outside resolved symbol lines %d-%d", evidence.Line, startLine, endLine)
	}
	return checked, nil
}

func agentWorkEvidenceConnected(changedCallerIDs, evidenceCallerIDs []string, report ImpactReport) bool {
	changedCallerIDs = uniqueNonEmptyStrings(changedCallerIDs)
	evidenceCallerIDs = uniqueNonEmptyStrings(evidenceCallerIDs)
	if len(changedCallerIDs) == 0 || len(evidenceCallerIDs) == 0 {
		return false
	}
	reachable := map[string]bool{}
	for _, changed := range changedCallerIDs {
		reachable[changed] = true
	}
	for _, node := range report.Nodes {
		if node.Type == "function" && strings.HasPrefix(node.ID, "func:") {
			reachable[strings.TrimPrefix(node.ID, "func:")] = true
		}
	}
	for _, evidence := range evidenceCallerIDs {
		if reachable[evidence] {
			return true
		}
	}
	return false
}

func agentWorkEvidenceChanged(repo, file string, line, startLine, endLine int, ranges []ImpactRange) bool {
	for _, changed := range ranges {
		if strings.TrimSpace(changed.Repo) != strings.TrimSpace(repo) || normalizeDiffPath(changed.Path) != normalizeDiffPath(file) {
			continue
		}
		if line > 0 {
			if line >= changed.StartLine && line <= changed.EndLine {
				return true
			}
			continue
		}
		if startLine > 0 && endLine >= startLine {
			if startLine <= changed.EndLine && endLine >= changed.StartLine {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func agentWorkChangedFiles(ranges []ImpactRange, includeTests bool) []AgentWorkChangedFile {
	seen := map[string]bool{}
	var out []AgentWorkChangedFile
	for _, changed := range ranges {
		file := normalizeDiffPath(changed.Path)
		if !includeTests && isTestLikePath(file) {
			continue
		}
		key := agentWorkFileKey(changed.Repo, file)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, AgentWorkChangedFile{Repo: strings.TrimSpace(changed.Repo), File: file})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].File < out[j].File
	})
	return out
}

func agentWorkFileKey(repo, file string) string {
	return strings.TrimSpace(repo) + "\x00" + normalizeDiffPath(file)
}

func agentWorkAddedFiles(diff, defaultRepo string) map[string]string {
	out := map[string]string{}
	files, err := diffparse.Parse(diff)
	if err != nil {
		return out
	}
	for _, file := range files {
		if !file.New || file.NewPath == "" {
			continue
		}
		var added []string
		for _, hunk := range file.Hunks {
			for _, l := range hunk.Lines {
				if l.Op == '+' {
					added = append(added, l.Text)
				}
			}
		}
		out[agentWorkFileKey(defaultRepo, file.NewPath)] = strings.Join(added, "\n")
	}
	return out
}

func diffContentContainsSymbol(content, symbol string) bool {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return true
	}
	candidates := []string{symbol}
	if index := strings.LastIndexAny(symbol, ".#:"); index >= 0 && index+1 < len(symbol) {
		candidates = append(candidates, symbol[index+1:])
	}
	for _, candidate := range candidates {
		if regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(candidate) + `([^A-Za-z0-9_]|$)`).MatchString(content) {
			return true
		}
	}
	return false
}

func (h *Handlers) validateAgentWorkDiffBase(req ImpactRequest, scope searchWorkspaceScope) (bool, string, []string) {
	if req.AgentWork == nil {
		return false, "", []string{"agent work proof is missing"}
	}
	return h.validateVerifyDiffBase(req, scope)
}

func verifyDiffPreimage(diff, worktreePath string) ([]string, int) {
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return []string{"active workspace has no worktree for preimage validation"}, 0
	}
	root, err := os.OpenRoot(worktreePath)
	if err != nil {
		return []string{"cannot open indexed preimage: " + err.Error()}, 0
	}
	defer root.Close()
	files, err := diffparse.Parse(diff)
	if err != nil {
		return []string{"diff cannot be parsed: " + err.Error()}, 0
	}
	var alignmentErrors []string
	totalChecked := 0
	loadPreimage := func(path string) ([]string, bool) {
		clean := filepath.Clean(path)
		if path == "" || clean == "." || !filepath.IsLocal(clean) {
			alignmentErrors = append(alignmentErrors, "unsafe diff path: "+path)
			return nil, false
		}
		info, err := root.Lstat(clean)
		if err != nil {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("cannot read indexed preimage %s: %v", path, err))
			return nil, false
		}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			// The diff records the link target, not the content of the target file.
			var target string
			target, err = root.Readlink(clean)
			data = []byte(target)
		} else if info.Mode().IsRegular() {
			data, err = root.ReadFile(clean)
		} else {
			err = fmt.Errorf("not a regular file or symbolic link")
		}
		if err != nil {
			alignmentErrors = append(alignmentErrors, fmt.Sprintf("cannot read indexed preimage %s: %v", path, err))
			return nil, false
		}
		return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), true
	}
	for _, file := range files {
		path := file.Path()
		if file.New {
			if path != "" {
				clean := filepath.Clean(path)
				if clean == "." || !filepath.IsLocal(clean) {
					alignmentErrors = append(alignmentErrors, "unsafe diff path: "+path)
				} else if _, err := root.Lstat(clean); err == nil {
					alignmentErrors = append(alignmentErrors, "new-file diff targets a file already present in the indexed preimage: "+path)
				} else if !os.IsNotExist(err) {
					alignmentErrors = append(alignmentErrors, fmt.Sprintf("cannot validate new-file preimage %s: %v", path, err))
				}
			}
			continue
		}
		if len(file.Hunks) == 0 {
			continue
		}
		lines, ok := loadPreimage(path)
		for _, hunk := range file.Hunks {
			oldLine := hunk.OldStart
			for _, l := range hunk.Lines {
				if l.Op == '+' {
					continue
				}
				if !ok {
					oldLine++
					continue
				}
				index := oldLine - 1
				if index < 0 || index >= len(lines) {
					alignmentErrors = append(alignmentErrors, fmt.Sprintf("%s:%d is outside the indexed preimage", path, oldLine))
				} else if lines[index] != l.Text {
					alignmentErrors = append(alignmentErrors, fmt.Sprintf("%s:%d does not match the indexed preimage", path, oldLine))
				} else {
					totalChecked++
				}
				oldLine++
			}
		}
	}
	return uniqueNonEmptyStrings(alignmentErrors), totalChecked
}

func markVerifyExposure(result *EstateVerifyResult) {
	if result != nil && result.Verdict == "pass" {
		result.Verdict = "info"
	}
}

func markVerifyIncomplete(result *EstateVerifyResult) {
	if result == nil {
		return
	}
	if result.Verdict == "pass" || result.Verdict == "info" {
		result.Verdict = "warn"
	}
}

func (h *Handlers) recordEstateVerifyRun(ctx context.Context, req ImpactRequest, scope searchWorkspaceScope, result *EstateVerifyResult, duration time.Duration) {
	if result == nil {
		return
	}
	changeTypes := verifyChangeTypes(result.BreakingChanges)
	reasons := uniqueNonEmptyStrings(result.Reasons)
	changeTypesJSON, err := json.Marshal(changeTypes)
	if err != nil {
		changeTypesJSON = []byte("[]")
	}
	reasonsJSON, err := json.Marshal(reasons)
	if err != nil {
		reasonsJSON = []byte("[]")
	}
	workStatus := ""
	claimCount := 0
	unaccountedCount := 0
	if result.AgentWork != nil {
		workStatus = result.AgentWork.Status
		claimCount = len(result.AgentWork.Claims)
		unaccountedCount = len(result.AgentWork.UnaccountedChanges)
	}
	_, err = h.storage.Pool().Exec(ctx, `
		INSERT INTO verify_runs (
			workspace_id,
			repo,
			verdict,
			mode,
			work_status,
			claim_count,
			unaccounted_change_count,
			breaking_count,
			endpoint_count,
			queue_count,
			entity_count,
			consumer_repo_count,
			query_error_count,
			change_types,
			reasons,
			duration_ms
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15::jsonb,$16)
	`,
		scope.Workspace.ID,
		strings.TrimSpace(req.Repo),
		result.Verdict,
		result.Mode,
		workStatus,
		claimCount,
		unaccountedCount,
		len(result.BreakingChanges),
		len(result.ExposedSurface.Endpoints),
		len(result.ExposedSurface.Queues),
		len(result.ExposedSurface.Entities),
		countVerifyConsumerRepos(result.ExposedSurface),
		len(result.QueryErrors),
		string(changeTypesJSON),
		string(reasonsJSON),
		duration.Milliseconds(),
	)
	if err != nil {
		log.Printf("WARN: verify telemetry insert failed: %v", err)
	}
}

func verifyChangeTypes(changes []VerifyBreakingChange) []string {
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		out = append(out, change.ChangeType)
	}
	return uniqueNonEmptyStrings(out)
}

func countVerifyConsumerRepos(surface EstateVerifyExposedSurface) int {
	seen := map[string]bool{}
	for _, endpoint := range surface.Endpoints {
		for _, consumer := range endpoint.Consumers {
			if consumer.Repo != "" {
				seen[consumer.Repo] = true
			}
		}
	}
	for _, queue := range surface.Queues {
		for _, repo := range queue.CounterpartyRepos {
			if repo != "" {
				seen[repo] = true
			}
		}
	}
	for _, entity := range surface.Entities {
		for _, reader := range entity.OtherReaders {
			if reader.Repo != "" {
				seen[reader.Repo] = true
			}
		}
		for _, writer := range entity.OtherWriters {
			if writer.Repo != "" {
				seen[writer.Repo] = true
			}
		}
	}
	return len(seen)
}

func (h *Handlers) ListVerifyRuns(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimitOnly(w, r, 50, 200)
	if !ok {
		return
	}
	// Same workspace selection as every other route: ?workspaceId, then the
	// X-Tirion-Workspace header, then the default workspace. Unknown -> 404.
	_, scope, err := h.resolveWorkspaceScope(workspaceIDFromRequest(r), r.Context())
	if err != nil {
		writeLookupError(w, err)
		return
	}
	workspaceID := scope.Workspace.ID
	rows, err := h.storage.Pool().Query(r.Context(), `
		SELECT
			id,
			workspace_id,
			repo,
			verdict,
			mode,
			work_status,
			claim_count,
			unaccounted_change_count,
			breaking_count,
			endpoint_count,
			queue_count,
			entity_count,
			consumer_repo_count,
			query_error_count,
			change_types,
			reasons,
			duration_ms,
			created_at
		FROM verify_runs
		WHERE ($1 = '' OR workspace_id = $1)
		ORDER BY id DESC
		LIMIT $2
	`, workspaceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_ERROR", err.Error(), nil)
		return
	}
	defer rows.Close()

	runs := []VerifyRunRecord{}
	for rows.Next() {
		var run VerifyRunRecord
		var changeTypesJSON []byte
		var reasonsJSON []byte
		if err := rows.Scan(
			&run.ID,
			&run.WorkspaceID,
			&run.Repo,
			&run.Verdict,
			&run.Mode,
			&run.WorkStatus,
			&run.ClaimCount,
			&run.UnaccountedChangeCount,
			&run.BreakingCount,
			&run.EndpointCount,
			&run.QueueCount,
			&run.EntityCount,
			&run.ConsumerRepoCount,
			&run.QueryErrorCount,
			&changeTypesJSON,
			&reasonsJSON,
			&run.DurationMS,
			&run.CreatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "QUERY_ERROR", err.Error(), nil)
			return
		}
		_ = json.Unmarshal(changeTypesJSON, &run.ChangeTypes)
		_ = json.Unmarshal(reasonsJSON, &run.Reasons)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_ERROR", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, VerifyRunsResponse{Workspace: scope.Workspace, Runs: runs})
}

func describeVerifyBreakingReason(change VerifyBreakingChange, consumerRepos int, hasConsumers bool) string {
	suffix := "but no external consumer was found in the active graph"
	if hasConsumers {
		suffix = fmt.Sprintf("and has %d external consumer repo(s)", consumerRepos)
	}
	switch change.ChangeType {
	case "endpoint_removed":
		return fmt.Sprintf("%s endpoint handler %s was removed %s", change.Endpoint, change.Handler, suffix)
	case "parameter_type_changed":
		return fmt.Sprintf("%s changed parameter type(s) %s %s", change.Endpoint, describeVerifyParamChanges(change.ChangedParams), suffix)
	default:
		return fmt.Sprintf("%s removed %s %s", change.Endpoint, strings.Join(quoteStrings(change.RemovedParams), ", "), suffix)
	}
}

func describeVerifyParamChanges(changes []VerifyParamChange) string {
	if len(changes) == 0 {
		return ""
	}
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		out = append(out, fmt.Sprintf("%s %s->%s", change.Name, change.OldType, change.NewType))
	}
	return strings.Join(out, ", ")
}

func findEndpointExposure(exposures []VerifyEndpointExposure, repo, endpoint string) VerifyEndpointExposure {
	for _, exposure := range exposures {
		if exposure.Repo == repo && exposure.Endpoint == endpoint {
			return exposure
		}
	}
	return VerifyEndpointExposure{}
}

func (h *Handlers) verifyEndpointExposures(ctx context.Context, endpoints []ImpactEndpoint, scope searchWorkspaceScope) ([]VerifyEndpointExposure, []string) {
	if len(endpoints) == 0 {
		return nil, nil
	}
	out := make([]VerifyEndpointExposure, 0, len(endpoints))
	var queryErrors []string
	for _, endpoint := range endpoints {
		exposure := VerifyEndpointExposure{
			Endpoint: strings.TrimSpace(endpoint.Method + " " + endpoint.Path),
			Method:   endpoint.Method,
			Path:     endpoint.Path,
			Repo:     endpoint.Repo,
			Handler:  endpoint.Handler,
			File:     endpoint.File,
			Line:     endpoint.Line,
		}
		consumers, err := h.fetchVerifyEndpointConsumers(ctx, endpoint, scope)
		if err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("endpoint consumer lookup failed for %s: %v", exposure.Endpoint, err))
		}
		exposure.Consumers = consumers
		exposure.ConsumerRepos = countUniqueConsumerRepos(exposure.Consumers)
		out = append(out, exposure)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConsumerRepos != out[j].ConsumerRepos {
			return out[i].ConsumerRepos > out[j].ConsumerRepos
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Endpoint < out[j].Endpoint
	})
	return out, queryErrors
}

func (h *Handlers) fetchVerifyEndpointConsumers(ctx context.Context, endpoint ImpactEndpoint, scope searchWorkspaceScope) ([]VerifySurfaceConsumer, error) {
	return h.fetchVerifyEndpointCallers(ctx, endpoint, scope, true)
}

func (h *Handlers) fetchVerifyEndpointCallers(ctx context.Context, endpoint ImpactEndpoint, scope searchWorkspaceScope, externalOnly bool, limits ...int) ([]VerifySurfaceConsumer, error) {
	limit := 100
	if len(limits) > 0 {
		limit = limits[0]
	}
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		WITH target_endpoint AS (
			SELECT
			       upper(COALESCE(NULLIF($1, ''), 'REQUEST')) AS method,
			       lower(regexp_replace(regexp_replace(COALESCE(NULLIF($2, ''), ''), '[?#].*$', ''), '/+', '/', 'g')) AS path
		),
		http_map AS (
			SELECT hr.name AS caller_repo,
			       h.caller_id,
			       h.line_number,
			       upper(COALESCE(NULLIF(h.http_method, ''), 'REQUEST')) AS method,
			       lower(regexp_replace(
			         regexp_replace(
			           regexp_replace(
			             regexp_replace(h.url_pattern, '^[A-Za-z][A-Za-z0-9+.-]*://[^/]*', ''),
			             '[?#].*$', ''
			           ),
			           '/+', '/', 'g'
			         ),
			         '^/((:[^/]+|[{][^/]+[}])/)+', '/'
			       )) AS path
			FROM http_client_calls h
			JOIN repositories hr ON h.repo_id = hr.id
			WHERE (NOT $5 OR hr.name <> $3)
			  AND length(h.url_pattern) > 3
			  AND `+integrationSnapshotClause("h.snapshot_id", 4, workspaceIncludesLegacy(scope))+`
		)
		SELECT h.caller_repo, h.caller_id, COALESCE(MIN(h.line_number), 0)
		FROM http_map h
		JOIN target_endpoint endpoint ON (endpoint.method = h.method OR endpoint.method = 'REQUEST' OR h.method IN ('REQUEST', 'ANY'))
		                          AND trim(both '/' FROM endpoint.path) <> ''
		                          AND trim(both '/' FROM h.path) <> ''
		                          AND cardinality(string_to_array(trim(both '/' FROM endpoint.path), '/')) = cardinality(string_to_array(trim(both '/' FROM h.path), '/'))
		                          AND NOT EXISTS (
		                            SELECT 1
		                            FROM generate_subscripts(string_to_array(trim(both '/' FROM endpoint.path), '/'), 1) AS idx(i)
		                            WHERE NOT (
		                              (string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] = (string_to_array(trim(both '/' FROM h.path), '/'))[i]
		                              OR left((string_to_array(trim(both '/' FROM endpoint.path), '/'))[i], 1) = ':'
		                              OR ((string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] LIKE '{%' AND (string_to_array(trim(both '/' FROM endpoint.path), '/'))[i] LIKE '%}')
		                              OR left((string_to_array(trim(both '/' FROM h.path), '/'))[i], 1) = ':'
		                              OR ((string_to_array(trim(both '/' FROM h.path), '/'))[i] LIKE '{%' AND (string_to_array(trim(both '/' FROM h.path), '/'))[i] LIKE '%}')
		                            )
		                          )
		GROUP BY h.caller_repo, h.caller_id
		ORDER BY h.caller_repo, h.caller_id
		LIMIT $6
	`, endpoint.Method, endpoint.Path, endpoint.Repo, snapshotIDs, externalOnly, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifySurfaceConsumer
	for rows.Next() {
		var consumer VerifySurfaceConsumer
		if err := rows.Scan(&consumer.Repo, &consumer.CallerID, &consumer.Line); err != nil {
			return out, err
		}
		_, file, _ := trace.ParseCallerID(consumer.CallerID)
		consumer.File = file
		out = append(out, consumer)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out) > limit {
		return out[:limit], errVerifyFrontierClipped
	}
	return out, nil
}

func countUniqueConsumerRepos(consumers []VerifySurfaceConsumer) int {
	seen := map[string]bool{}
	for _, consumer := range consumers {
		if consumer.Repo != "" {
			seen[consumer.Repo] = true
		}
	}
	return len(seen)
}

func (h *Handlers) verifyQueueExposures(ctx context.Context, callerIDs []string, scope searchWorkspaceScope) ([]VerifyQueueExposure, []string) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, q.caller_id, q.queue_name, q.line_number, 'produces' AS direction
		FROM sqs_producers q
		JOIN repositories r ON q.repo_id = r.id
		WHERE q.caller_id = ANY($1)
		  AND `+integrationSnapshotClause("q.snapshot_id", 2, workspaceIncludesLegacy(scope))+`

		UNION ALL

		SELECT r.name, q.consumer_id, q.queue_name, 0 AS line_number, 'consumes' AS direction
		FROM sqs_consumers q
		JOIN repositories r ON q.repo_id = r.id
		WHERE (q.consumer_id = ANY($1) OR EXISTS (
		    SELECT 1 FROM unnest($1::text[]) cid WHERE cid LIKE q.consumer_id || ':%'
		))
		  AND `+integrationSnapshotClause("q.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		ORDER BY queue_name, direction, caller_id
	`, callerIDs, snapshotIDs)
	if err != nil {
		return nil, []string{fmt.Sprintf("queue exposure lookup failed: %v", err)}
	}
	defer rows.Close()
	var out []VerifyQueueExposure
	var queryErrors []string
	for rows.Next() {
		var exposure VerifyQueueExposure
		if err := rows.Scan(&exposure.Repo, &exposure.CallerID, &exposure.Queue, &exposure.Line, &exposure.Direction); err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("queue exposure row scan failed: %v", err))
			continue
		}
		out = append(out, exposure)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		queryErrors = append(queryErrors, fmt.Sprintf("queue exposure rows failed: %v", err))
	}
	for i := range out {
		exposure := &out[i]
		var err error
		if exposure.Direction == "produces" {
			exposure.CounterpartyRepos, err = h.fetchQueueConsumerCounterparties(ctx, exposure.Repo, exposure.Queue, scope)
		} else {
			exposure.CounterpartyRepos, err = h.fetchQueueProducerCounterparties(ctx, exposure.Repo, exposure.Queue, scope)
		}
		if err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("queue counterparty lookup failed for %s: %v", exposure.Queue, err))
		}
	}
	return out, queryErrors
}

func (h *Handlers) verifyEntityExposures(ctx context.Context, callerIDs []string, scope searchWorkspaceScope) ([]VerifyEntityExposure, []string) {
	snapshotIDs := activeSnapshotIDs(scope)
	rows, err := h.storage.Pool().Query(ctx, `
		SELECT r.name, da.caller_id, da.entity_name, da.access, da.line_number
		FROM data_accesses da
		JOIN repositories r ON da.repo_id = r.id
		WHERE da.caller_id = ANY($1)
		  AND COALESCE(da.entity_name, '') <> ''
		  AND `+integrationSnapshotClause("da.snapshot_id", 2, workspaceIncludesLegacy(scope))+`
		ORDER BY da.entity_name, da.access, da.caller_id
	`, callerIDs, snapshotIDs)
	if err != nil {
		return nil, []string{fmt.Sprintf("entity exposure lookup failed: %v", err)}
	}
	defer rows.Close()
	var out []VerifyEntityExposure
	var queryErrors []string
	for rows.Next() {
		var exposure VerifyEntityExposure
		if err := rows.Scan(&exposure.Repo, &exposure.CallerID, &exposure.Entity, &exposure.Access, &exposure.Line); err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("entity exposure row scan failed: %v", err))
			continue
		}
		if !isWriteAccess(exposure.Access) {
			continue
		}
		out = append(out, exposure)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		queryErrors = append(queryErrors, fmt.Sprintf("entity exposure rows failed: %v", err))
	}
	for i := range out {
		exposure := &out[i]
		readers, err := h.fetchEntityAccessors(ctx, exposure.Entity, exposure.Repo, true, scope)
		if err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("entity reader lookup failed for %s: %v", exposure.Entity, err))
		}
		writers, err := h.fetchEntityAccessors(ctx, exposure.Entity, exposure.Repo, false, scope)
		if err != nil {
			queryErrors = append(queryErrors, fmt.Sprintf("entity writer lookup failed for %s: %v", exposure.Entity, err))
		}
		exposure.OtherReaders = readers
		exposure.OtherWriters = writers
	}
	return out, queryErrors
}

func isWriteAccess(access string) bool {
	access = strings.ToLower(strings.TrimSpace(access))
	return access == "write" || access == "insert" || access == "update" || access == "delete" || access == "merge" || access == "save"
}

func (h *Handlers) fetchEntityAccessors(ctx context.Context, entity, sourceRepo string, readers bool, scope searchWorkspaceScope) ([]VerifySurfaceConsumer, error) {
	snapshotIDs := activeSnapshotIDs(scope)
	accessPredicate := "LOWER(da.access) = 'read'"
	if !readers {
		accessPredicate = "LOWER(da.access) <> 'read'"
	}
	rows, err := h.storage.Pool().Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT r.name, da.caller_id, da.line_number
		FROM data_accesses da
		JOIN repositories r ON da.repo_id = r.id
		WHERE LOWER(da.entity_name) = LOWER($1)
		  AND r.name <> $2
		  AND %s
		  AND %s
		ORDER BY r.name, da.caller_id, da.line_number
		LIMIT 101
	`, accessPredicate, integrationSnapshotClause("da.snapshot_id", 3, workspaceIncludesLegacy(scope))), entity, sourceRepo, snapshotIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifySurfaceConsumer
	for rows.Next() {
		var consumer VerifySurfaceConsumer
		if err := rows.Scan(&consumer.Repo, &consumer.CallerID, &consumer.Line); err != nil {
			return out, err
		}
		_, file, _ := trace.ParseCallerID(consumer.CallerID)
		consumer.File = file
		out = append(out, consumer)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out) > 100 {
		return out[:100], errVerifyFrontierClipped
	}
	return out, nil
}

type verifyEndpointInfo struct {
	method  string
	path    string
	handler string
	repo    string
	file    string
	line    int
}

func detectVerifyEndpointBreakingChanges(diff string, changedEndpoints []ImpactEndpoint) []VerifyBreakingChange {
	if len(changedEndpoints) == 0 || strings.TrimSpace(diff) == "" {
		return nil
	}
	fileEndpoints := make(map[string][]verifyEndpointInfo)
	for _, ep := range changedEndpoints {
		fileEndpoints[ep.File] = append(fileEndpoints[ep.File], verifyEndpointInfo{
			method: ep.Method, path: ep.Path, handler: ep.Handler, repo: ep.Repo, file: ep.File, line: ep.Line,
		})
	}

	var breaking []VerifyBreakingChange
	currentFile := ""
	currentExt := ""
	var removedLines []string
	var addedLines []string
	var contextLines []string

	flushBlock := func() {
		if len(removedLines) == 0 {
			removedLines = nil
			addedLines = nil
			return
		}
		eps, ok := fileEndpoints[currentFile]
		if !ok {
			removedLines = nil
			addedLines = nil
			return
		}
		removedText := strings.Join(removedLines, " ")
		addedText := strings.Join(addedLines, " ")
		signatureContext := strings.Join(contextLines, " ")
		oldSignatureText := strings.TrimSpace(signatureContext + " " + removedText)
		newSignatureText := strings.TrimSpace(signatureContext + " " + addedText)
		for _, ep := range eps {
			if ep.handler == "" || ep.handler == "handler" {
				continue
			}
			candidates := handlerMatchCandidates(ep.handler)
			oldSignatureFound := signatureContainsCandidate(oldSignatureText, candidates)
			newSignatureFound := signatureContainsCandidate(newSignatureText, candidates)
			if !oldSignatureFound && !newSignatureFound {
				continue
			}
			endpoint := strings.TrimSpace(ep.method + " " + ep.path)
			if oldSignatureFound && !newSignatureFound {
				breaking = append(breaking, VerifyBreakingChange{
					Endpoint:   endpoint,
					Handler:    ep.handler,
					Repo:       ep.repo,
					File:       ep.file,
					ChangeType: "endpoint_removed",
					Severity:   "critical",
					Detail:     fmt.Sprintf("Endpoint handler %s (%s) was removed.", ep.handler, endpoint),
					Evidence:   VerifyLineEvidence{File: ep.file, Line: ep.line},
				})
				continue
			}
			oldParams := extractParamsFromSignatureWithCandidates(oldSignatureText, candidates)
			newParams := extractParamsFromSignatureWithCandidates(newSignatureText, candidates)
			if oldParams == "" && newParams == "" {
				continue
			}
			oldParsed := extractParams(oldParams, currentExt)
			newParsed := extractParams(newParams, currentExt)
			oldNames := verifyParamNames(oldParsed)
			newNames := verifyParamNames(newParsed)
			removed := stringDiff(oldNames, newNames)
			added := stringDiff(newNames, oldNames)
			typeChanges := verifyParamTypeChanges(oldParsed, newParsed)
			if len(removed) > 0 {
				detail := fmt.Sprintf("Parameter(s) %s removed from %s (%s).", strings.Join(quoteStrings(removed), ", "), ep.handler, endpoint)
				breaking = append(breaking, VerifyBreakingChange{
					Endpoint:      endpoint,
					Handler:       ep.handler,
					Repo:          ep.repo,
					File:          ep.file,
					ChangeType:    "parameter_removed",
					RemovedParams: removed,
					AddedParams:   added,
					Severity:      "high",
					Detail:        detail,
					Evidence:      VerifyLineEvidence{File: ep.file, Line: ep.line},
				})
			}
			if len(typeChanges) > 0 {
				breaking = append(breaking, VerifyBreakingChange{
					Endpoint:      endpoint,
					Handler:       ep.handler,
					Repo:          ep.repo,
					File:          ep.file,
					ChangeType:    "parameter_type_changed",
					ChangedParams: typeChanges,
					Severity:      "high",
					Detail:        fmt.Sprintf("Parameter type(s) %s changed on %s (%s).", describeVerifyParamChanges(typeChanges), ep.handler, endpoint),
					Evidence:      VerifyLineEvidence{File: ep.file, Line: ep.line},
				})
			}
		}
		removedLines = nil
		addedLines = nil
	}

	diffFiles, err := diffparse.Parse(diff)
	if err != nil {
		// Callers reject unparseable diffs before verification; nothing to report here.
		return nil
	}
	for _, file := range diffFiles {
		flushBlock()
		currentFile = file.NewPath
		if currentFile == "" {
			currentFile = file.OldPath
		}
		currentExt = strings.ToLower(path.Ext(currentFile))
		contextLines = nil
		for _, hunk := range file.Hunks {
			flushBlock()
			contextLines = nil
			for _, l := range hunk.Lines {
				switch l.Op {
				case '-':
					if len(addedLines) > 0 && len(removedLines) > 0 {
						flushBlock()
					}
					removedLines = append(removedLines, strings.TrimSpace(l.Text))
				case '+':
					addedLines = append(addedLines, strings.TrimSpace(l.Text))
				case ' ':
					flushBlock()
					contextLines = append(contextLines, strings.TrimSpace(l.Text))
				}
			}
		}
	}
	flushBlock()
	sort.Slice(breaking, func(i, j int) bool {
		if breaking[i].Severity != breaking[j].Severity {
			return breaking[i].Severity == "critical"
		}
		return breaking[i].Endpoint < breaking[j].Endpoint
	})
	return breaking
}

func extractParamNames(rawParams, fileExt string) []string {
	return verifyParamNames(extractParams(rawParams, fileExt))
}

type verifyParam struct {
	Name string
	Type string
}

func extractParams(rawParams, fileExt string) []verifyParam {
	rawParams = strings.TrimSpace(rawParams)
	if rawParams == "" {
		return nil
	}
	ext := strings.ToLower(fileExt)
	params := splitParams(rawParams)
	var out []verifyParam
	for _, param := range params {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		if parsed := extractSingleParam(param, ext); parsed.Name != "" {
			out = append(out, parsed)
		}
	}
	return out
}

func verifyParamNames(params []verifyParam) []string {
	if len(params) == 0 {
		return nil
	}
	names := make([]string, 0, len(params))
	for _, param := range params {
		if param.Name != "" {
			names = append(names, param.Name)
		}
	}
	return names
}

func verifyParamTypeChanges(oldParams, newParams []verifyParam) []VerifyParamChange {
	newByName := make(map[string]verifyParam, len(newParams))
	for _, param := range newParams {
		newByName[param.Name] = param
	}
	var out []VerifyParamChange
	for _, oldParam := range oldParams {
		newParam, ok := newByName[oldParam.Name]
		if !ok {
			continue
		}
		oldType := normalizeVerifyParamType(oldParam.Type)
		newType := normalizeVerifyParamType(newParam.Type)
		if oldType == "" || newType == "" || oldType == newType {
			continue
		}
		out = append(out, VerifyParamChange{
			Name:    oldParam.Name,
			OldType: oldType,
			NewType: newType,
		})
	}
	return out
}

func splitParams(raw string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, ch := range raw {
		switch ch {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, raw[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, raw[start:])
	return parts
}

var annotationRegexp = regexp.MustCompile(`(?:\[[\w.]+(?:\([^)]*\))?\]\s*)|(?:@\w+(?:\([^)]*\))?\s*)`)

func extractSingleParam(param, ext string) verifyParam {
	cleaned := annotationRegexp.ReplaceAllString(param, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return verifyParam{}
	}
	if idx := strings.Index(cleaned, "="); idx >= 0 {
		cleaned = strings.TrimSpace(cleaned[:idx])
	}
	cleaned = strings.TrimPrefix(cleaned, "this ")
	cleaned = strings.TrimPrefix(cleaned, "params ")
	for _, kw := range []string{"ref ", "out ", "in "} {
		cleaned = strings.TrimPrefix(cleaned, kw)
	}
	tokens := strings.Fields(cleaned)
	if len(tokens) == 0 {
		return verifyParam{}
	}
	switch ext {
	case ".go":
		return verifyParam{Name: tokens[0], Type: strings.TrimSpace(strings.Join(tokens[1:], " "))}
	case ".ts", ".tsx", ".js", ".jsx":
		if idx := strings.Index(cleaned, ":"); idx >= 0 {
			name := strings.TrimSpace(cleaned[:idx])
			name = strings.TrimRight(name, "?:")
			typ := strings.TrimSpace(cleaned[idx+1:])
			return verifyParam{Name: name, Type: typ}
		}
		name := strings.TrimRight(tokens[0], "?:")
		return verifyParam{Name: name}
	default:
		return verifyParam{
			Name: tokens[len(tokens)-1],
			Type: strings.TrimSpace(strings.Join(tokens[:len(tokens)-1], " ")),
		}
	}
}

func normalizeVerifyParamType(typ string) string {
	typ = strings.TrimSpace(typ)
	typ = strings.TrimPrefix(typ, "readonly ")
	typ = strings.TrimPrefix(typ, "const ")
	return typ
}

func handlerMatchCandidates(handler string) []string {
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return nil
	}
	parts := []string{handler}
	if idx := strings.LastIndex(handler, "."); idx >= 0 && idx < len(handler)-1 {
		parts = append(parts, handler[idx+1:])
	}
	return uniqueNonEmptyStrings(parts)
}

func signatureContainsCandidate(text string, candidates []string) bool {
	for _, candidate := range candidates {
		idx := strings.Index(text, candidate)
		if idx < 0 {
			continue
		}
		rest := text[idx+len(candidate):]
		if strings.Contains(rest, "(") {
			return true
		}
	}
	return false
}

func extractParamsFromSignatureWithCandidates(text string, candidates []string) string {
	for _, candidate := range candidates {
		if params := extractParamsFromSignature(text, candidate); params != "" {
			return params
		}
	}
	return ""
}

func extractParamsFromSignature(text, handler string) string {
	idx := strings.Index(text, handler)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(handler):]
	parenStart := strings.Index(rest, "(")
	if parenStart < 0 {
		return ""
	}
	depth := 0
	for i := parenStart; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(rest[parenStart+1 : i])
			}
		}
	}
	return strings.TrimSpace(rest[parenStart+1:])
}

func uniqueNonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func stringDiff(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, s := range b {
		bSet[s] = true
	}
	var diff []string
	for _, s := range a {
		if !bSet[s] {
			diff = append(diff, s)
		}
	}
	return diff
}

func quoteStrings(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "'" + s + "'"
	}
	return out
}
