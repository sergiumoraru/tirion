package handlers

import (
	"context"
	"strings"
	"testing"
)

func TestParseFlowStart_PathAndMethod(t *testing.T) {
	t.Parallel()

	method, path, ok := parseFlowStart("/Resource")
	if !ok || method != "" || path != "/Resource" {
		t.Fatalf("expected path-only parse, got ok=%v method=%q path=%q", ok, method, path)
	}

	method, path, ok = parseFlowStart("POST /action/Resource/save")
	if !ok || method != "POST" || path != "/action/Resource/save" {
		t.Fatalf("expected method+path parse, got ok=%v method=%q path=%q", ok, method, path)
	}
}

func TestBuildEndpointPathCandidates_ActionAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "already action path",
			in:   "/action/Resource",
			want: []string{"/action/Resource"},
		},
		{
			name: "single segment alias",
			in:   "/Resource",
			want: []string{"/Resource"},
		},
		{
			name: "nested path alias",
			in:   "/resources/save",
			want: []string{"/resources/save"},
		},
		{
			name: "forwarding iframe forwarded path",
			in:   "/proxy?path=%2Fresources",
			want: []string{
				"/proxy",
				"/resources",
			},
		},
		{
			name: "forwarding iframe forwarded path unescaped",
			in:   "/proxy?path=/resources",
			want: []string{
				"/proxy",
				"/resources",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildEndpointPathCandidates(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d candidates, got %d: %#v", len(tt.want), len(got), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("candidate[%d] mismatch: got %q want %q (all=%#v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestFlowStartPathVariants_UsesForwardedPathQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "encoded",
			in:   "/proxy?path=%2Fresources",
			want: []string{
				"/proxy?path=%2Fresources",
				"/resources",
			},
		},
		{
			name: "unescaped",
			in:   "/proxy?path=/resources",
			want: []string{
				"/proxy?path=/resources",
				"/resources",
			},
		},
	}

	for _, tt := range tests {
		got := flowStartPathVariants(tt.in)
		if len(got) != len(tt.want) {
			t.Fatalf("%s: expected %d variants, got %d: %#v", tt.name, len(tt.want), len(got), got)
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Fatalf("%s: variant[%d] mismatch: got %q want %q (all=%#v)", tt.name, i, got[i], tt.want[i], got)
			}
		}
	}
}

func TestParseFlowStartMode_TreatsKafkaAndTopicAsQueueStarts(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"kafka:RecordTopic", "topic:record-events"} {
		mode, value := parseFlowStartMode(raw)
		if mode != "queue" || value == "" {
			t.Fatalf("parseFlowStartMode(%q) = mode %q value %q, want queue with value", raw, mode, value)
		}
	}
}

func TestFlowQueueNameVariants_NormalizesTransportNames(t *testing.T) {
	t.Parallel()

	got := flowQueueNameVariants("arn:aws:sqs:us-east-1:123:test_records.fifo")
	want := map[string]bool{
		"arn:aws:sqs:us-east-1:123:test_records.fifo": true,
		"test_records.fifo":                           true,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected queue aliases: %v", got)
	}
	for _, item := range got {
		delete(want, item)
	}
	if len(want) != 0 {
		t.Fatalf("missing queue variants %#v from got %#v", want, got)
	}
}

func TestHttpCallMatchesEndpoints_RejectsPlaceholderOnlyPatterns(t *testing.T) {
	t.Parallel()

	endpoints := []FlowEndpoint{
		{Method: "REQUEST", Path: "/resources"},
	}

	if httpCallMatchesEndpoints("REQUEST", "/:url", endpoints) {
		t.Fatalf("expected placeholder-only pattern to be rejected")
	}
	if httpCallMatchesEndpoints("GET", "/{id}", endpoints) {
		t.Fatalf("expected placeholder-only brace pattern to be rejected")
	}
	if !httpCallMatchesEndpoints("GET", "/resources/:resourceId", []FlowEndpoint{{Method: "REQUEST", Path: "/resources/:id"}}) {
		t.Fatalf("expected literal+placeholder pattern to match")
	}
	if !httpCallMatchesEndpoints("ANY", "/resources/:resourceId", []FlowEndpoint{{Method: "POST", Path: "/resources/:id"}}) {
		t.Fatalf("expected ANY client method to match concrete endpoint method")
	}
}

func TestPathHasLiteralSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{in: "/:url", want: false},
		{in: "/{id}", want: false},
		{in: "/**", want: false},
		{in: "/resources", want: true},
		{in: "/resources/:resourceId", want: true},
	}
	for _, tt := range tests {
		if got := pathHasLiteralSegment(tt.in); got != tt.want {
			t.Fatalf("pathHasLiteralSegment(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestShouldSkipLowSignalInternalCall(t *testing.T) {
	t.Parallel()

	if !shouldSkipLowSignalInternalCall(
		FlowEndpoint{Handler: "_module_"},
		FlowEndpoint{Handler: "NgModule"},
	) {
		t.Fatalf("expected _module_ -> NgModule to be skipped")
	}

	if shouldSkipLowSignalInternalCall(
		FlowEndpoint{Handler: "_module_"},
		FlowEndpoint{Handler: "bootstrapApp"},
	) {
		t.Fatalf("did not expect lowercase module call to be skipped")
	}

	if shouldSkipLowSignalInternalCall(
		FlowEndpoint{Handler: "ResourceController.save"},
		FlowEndpoint{Handler: "NgModule"},
	) {
		t.Fatalf("did not expect non-module caller to be skipped")
	}

	if !shouldSkipLowSignalInternalCall(
		FlowEndpoint{
			Handler: "ResourceController.load",
			Repo:    "resource-api",
			File:    "Controllers/ResourceController.cs",
		},
		FlowEndpoint{
			Handler: "ResourceController.Exception",
			Repo:    "resource-api",
			File:    "Controllers/ResourceController.cs",
		},
	) {
		t.Fatalf("expected same-file type pseudo-call to be skipped")
	}

	if shouldSkipLowSignalInternalCall(
		FlowEndpoint{
			Handler: "ResourceController.load",
			Repo:    "resource-api",
			File:    "Controllers/ResourceController.cs",
		},
		FlowEndpoint{
			Handler: "ResourceRepository.GetById",
			Repo:    "resource-api",
			File:    "Controllers/ResourceController.cs",
		},
	) {
		t.Fatalf("did not expect business repository call to be skipped")
	}
}

func TestShouldIncludeFlowEntity(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"page":     true,
		"pages":    true,
		"pagelink": true,
	}

	if !shouldIncludeFlowEntity("page", allowed) {
		t.Fatalf("expected direct primary entity to be included")
	}
	if !shouldIncludeFlowEntity("Pages", allowed) {
		t.Fatalf("expected canonicalized entity to be included")
	}
	if shouldIncludeFlowEntity("agreement", allowed) {
		t.Fatalf("did not expect unrelated entity to be included")
	}
	if !shouldIncludeFlowEntity("agreement", nil) {
		t.Fatalf("expected empty allowlist to include all entities")
	}
}

func TestSelectEndpointRoots_PreservesDistinctCallerRoots(t *testing.T) {
	t.Parallel()

	candidates := []endpointRootCandidate{
		{
			endpoint: FlowEndpoint{
				Repo:    "admin-app",
				Handler: "ResourceAction.save",
				File:    "src/ResourceAction.java",
				Method:  "POST",
				Path:    "/action/Resource/save",
			},
			callerID:      "admin-app:src/ResourceAction.java:ResourceAction.save",
			candidateRank: 0,
			callerCount:   2,
		},
		{
			endpoint: FlowEndpoint{
				Repo:    "web-app",
				Handler: "ResourceAction.save",
				File:    "src/ResourceAction.java",
				Method:  "POST",
				Path:    "/action/Resource/save",
			},
			callerID:      "web-app:src/ResourceAction.java:ResourceAction.save",
			candidateRank: 0,
			callerCount:   5,
		},
		{
			endpoint: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceController.save",
				File:    "src/ResourceController.java",
				Method:  "POST",
				Path:    "/resources/save",
			},
			callerID:      "resource-api:src/ResourceController.java:ResourceController.save",
			candidateRank: 0,
			callerCount:   1,
		},
		{
			endpoint: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceController.update",
				File:    "src/ResourceController.java",
				Method:  "PUT",
				Path:    "/resources/save",
			},
			callerID:      "resource-api:src/ResourceController.java:ResourceController.update",
			candidateRank: 0,
			callerCount:   1,
		},
	}

	roots, callerIDs := selectEndpointRoots(candidates)
	if len(roots) != 4 || len(callerIDs) != 4 {
		t.Fatalf("expected all 4 distinct caller roots, got roots=%d callerIDs=%d", len(roots), len(callerIDs))
	}

	expected := make(map[string]FlowEndpoint, len(candidates))
	for _, candidate := range candidates {
		expected[candidate.callerID] = candidate.endpoint
	}
	for i, callerID := range callerIDs {
		endpoint, ok := expected[callerID]
		if !ok {
			t.Fatalf("unexpected or duplicate caller root %q", callerID)
		}
		if roots[i].Repo != endpoint.Repo || roots[i].File != endpoint.File || roots[i].Handler != endpoint.Handler || roots[i].Method != endpoint.Method || roots[i].Path != endpoint.Path {
			t.Fatalf("caller %q lost its endpoint identity: %#v", callerID, roots[i])
		}
		delete(expected, callerID)
	}
	if len(expected) != 0 {
		t.Fatalf("missing caller roots: %#v", expected)
	}
}

func TestNormalizeFlowHops_DedupAndStableOrder(t *testing.T) {
	t.Parallel()

	hops := []FlowHop{
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "r2", Handler: "B"},
			Via:   FlowVia{Type: "data", Entity: "Users", Access: "publish"},
			To:    FlowEndpoint{Repo: "user-api", Method: "GET", Path: "/users"},
		},
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "r1", Handler: "A"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "r1", Handler: "A.child"},
		},
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "r1", Handler: "A"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "r1", Handler: "A.child"},
		},
	}

	got := normalizeFlowHops(hops)
	if len(got) != 2 {
		t.Fatalf("expected 2 hops after dedupe, got %d", len(got))
	}
	if got[0].Depth != 0 || got[0].Via.Type != "call" {
		t.Fatalf("expected call hop first after sort, got depth=%d type=%q", got[0].Depth, got[0].Via.Type)
	}
	if got[1].Depth != 1 || got[1].Via.Type != "data" {
		t.Fatalf("expected data hop second after sort, got depth=%d type=%q", got[1].Depth, got[1].Via.Type)
	}
}

func TestBuildFlowCompleteness_TruncatedByMaxHopsAndSoftLimit(t *testing.T) {
	t.Parallel()

	got := buildFlowCompleteness(100, 6, 24, flowBuildStats{
		MaxHopsReached:           true,
		InternalCallsSoftLimited: true,
	})

	if !got.Truncated {
		t.Fatalf("expected flow completeness to be truncated")
	}
	if got.ExactAvailableHops {
		t.Fatalf("expected exactAvailableHops=false when limits were hit")
	}
	if got.ReturnedHops != 6 || got.AvailableHops != 24 {
		t.Fatalf("unexpected counts: %#v", got)
	}
	if len(got.TruncationReasons) != 2 {
		t.Fatalf("expected two truncation reasons, got %#v", got.TruncationReasons)
	}
}

func TestBuildFlowCompleteness_ExactWhenNotLimited(t *testing.T) {
	t.Parallel()

	got := buildFlowCompleteness(100, 6, 24, flowBuildStats{})

	if got.Truncated {
		t.Fatalf("expected non-truncated completeness, got %#v", got)
	}
	if !got.ExactAvailableHops {
		t.Fatalf("expected exact available hops")
	}
	if len(got.TruncationReasons) != 0 {
		t.Fatalf("did not expect truncation reasons, got %#v", got.TruncationReasons)
	}
}

func TestBuildFlowWarnings_OnlyForTruncation(t *testing.T) {
	t.Parallel()

	if got := buildFlowWarnings(FlowCompleteness{}, FlowAssumptions{}); len(got) != 0 {
		t.Fatalf("expected no warnings without truncation, got %#v", got)
	}

	got := buildFlowWarnings(FlowCompleteness{
		ReturnedHops:       10,
		AvailableHops:      20,
		ExactAvailableHops: true,
		Truncated:          true,
	}, FlowAssumptions{})
	if len(got) != 1 {
		t.Fatalf("expected one warning for truncated flow, got %#v", got)
	}
}

func TestFlowEndpointDedupKey_CanonicalizesMethodPathAndSnapshot(t *testing.T) {
	t.Parallel()

	a := FlowEndpoint{
		Repo:    "resource-api",
		Method:  "post",
		Path:    "Resources//Save/",
		Handler: "ResourceController.save",
		File:    ".codebase-snapshots/run-1/src/ResourceController.java",
	}
	b := FlowEndpoint{
		Repo:    "resource-api",
		Method:  "POST",
		Path:    "/resources/save",
		Handler: "ResourceController.save",
		File:    "src/ResourceController.java",
	}

	if flowEndpointDedupKey(a) != flowEndpointDedupKey(b) {
		t.Fatalf("expected canonical dedupe keys to match, got %q vs %q", flowEndpointDedupKey(a), flowEndpointDedupKey(b))
	}
}

func TestDedupeFlowEndpoints_CanonicalIdentity(t *testing.T) {
	t.Parallel()

	in := []FlowEndpoint{
		{
			Repo:    "resource-api",
			Method:  "post",
			Path:    "Resources//Save/",
			Handler: "ResourceController.save",
			File:    ".codebase-snapshots/run-1/src/ResourceController.java",
			Line:    0,
		},
		{
			Repo:    "resource-api",
			Method:  "POST",
			Path:    "/resources/save",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
			Line:    216,
		},
	}

	out := dedupeFlowEndpoints(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 endpoint after canonical dedupe, got %d", len(out))
	}
	if out[0].Line != 216 {
		t.Fatalf("expected non-snapshot endpoint with line evidence to win, got line=%d", out[0].Line)
	}
	if out[0].File != "src/ResourceController.java" {
		t.Fatalf("expected non-snapshot file to win, got %q", out[0].File)
	}
}

func TestApplyStrictFlowMode_PicksDeterministicCoreChain(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{Repo: "r1", Handler: "Root.run", File: "a.go"},
	}
	hops := []FlowHop{
		// Root fans out to two internal calls.
		{Depth: 0, From: FlowEndpoint{Repo: "r1", Handler: "Root.run", File: "a.go"}, Via: FlowVia{Type: "call"}, To: FlowEndpoint{Repo: "r1", Handler: "Root.alpha"}},
		{Depth: 0, From: FlowEndpoint{Repo: "r1", Handler: "Root.run", File: "a.go"}, Via: FlowVia{Type: "call"}, To: FlowEndpoint{Repo: "r1", Handler: "Root.beta"}},
		// Only beta has an explicit cross-service hop.
		{Depth: 1, From: FlowEndpoint{Repo: "r1", Handler: "Root.beta"}, Via: FlowVia{Type: "sqs", Queue: "Q1"}, To: FlowEndpoint{Repo: "r2", Handler: "Consumer.sync"}},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 4)
	if len(got) != 2 {
		t.Fatalf("expected strict chain of 2 hops, got %d", len(got))
	}
	if got[0].Via.Type != "call" || got[0].To.Handler != "Root.beta" {
		t.Fatalf("expected first strict hop to Root.beta call, got type=%q to=%q", got[0].Via.Type, got[0].To.Handler)
	}
	if got[1].Via.Type != "sqs" || got[1].To.Repo != "r2" {
		t.Fatalf("expected second strict hop to be sqs cross-service, got type=%q repo=%q", got[1].Via.Type, got[1].To.Repo)
	}
}

func TestApplyStrictFlowMode_ContinuesPastQueueConsumerIntoLongerExecutionPath(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{Repo: "publish", Handler: "httpTrigger", File: "src/producer/index.ts"},
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "publish", Handler: "httpTrigger", File: "src/producer/index.ts"},
			Via:   FlowVia{Type: "sqs", Queue: "RESOURCE_EVENTS_QUEUE"},
			To:    FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.processActive"},
		},
		{
			Depth: 2,
			From:  FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.processActive"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.publish"},
		},
		{
			Depth: 3,
			From:  FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.publish"},
			Via:   FlowVia{Type: "http", Method: "POST", Path: "/resources/:id/publish"},
			To:    FlowEndpoint{Repo: "remote-api", Handler: "publishEntry"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
			Via:   FlowVia{Type: "http", Method: "POST", Path: "/resources/cleanup"},
			To:    FlowEndpoint{Repo: "publish", Handler: "cleanup"},
		},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 5)
	if len(got) != 4 {
		t.Fatalf("expected strict path to keep the longer publish chain (4 hops), got %d", len(got))
	}
	if got[0].Via.Type != "sqs" || got[0].To.Handler != "serviceBusTrigger" {
		t.Fatalf("expected first hop to queue into the consumer, got %#v", got[0])
	}
	if got[len(got)-1].Via.Type != "http" || got[len(got)-1].To.Handler != "publishEntry" {
		t.Fatalf("expected strict path to end at the publish HTTP action, got %#v", got[len(got)-1])
	}
}

func TestApplyStrictFlowMode_PrefersBusinessPublishBranchOverReadOnlySibling(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{Repo: "publish", Handler: "httpTrigger", File: "src/producer/index.ts"},
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "publish", Handler: "httpTrigger", File: "src/producer/index.ts"},
			Via:   FlowVia{Type: "sqs", Queue: "RESOURCE_EVENTS_QUEUE"},
			To:    FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "loadReferences", File: "src/consumer/references.ts"},
		},
		{
			Depth: 2,
			From:  FlowEndpoint{Repo: "publish", Handler: "loadReferences", File: "src/consumer/references.ts"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "ReferenceReader.getReferences", File: "src/references.ts"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "publish", Handler: "serviceBusTrigger", File: "src/consumer/index.ts"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.processActive", File: "src/consumer/publisher.ts"},
		},
		{
			Depth: 2,
			From:  FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.processActive", File: "src/consumer/publisher.ts"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.publish", File: "src/consumer/publisher.ts"},
		},
		{
			Depth: 3,
			From:  FlowEndpoint{Repo: "publish", Handler: "ResourcePublisher.publish", File: "src/consumer/publisher.ts"},
			Via:   FlowVia{Type: "http", Method: "POST", Path: "/resources/:id/publish"},
			To:    FlowEndpoint{Repo: "remote-api", Handler: "publishEntry"},
		},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 5)
	if len(got) != 4 {
		t.Fatalf("expected strict path to keep the publish branch (4 hops), got %d", len(got))
	}
	joined := ""
	for _, hop := range got {
		joined += hop.To.Handler + " -> "
	}
	if strings.Contains(joined, "loadReferences") || strings.Contains(joined, "ReferenceReader.getReferences") {
		t.Fatalf("expected strict mode to demote read-only sibling branch, got %q", joined)
	}
	if !strings.Contains(joined, "ResourcePublisher.processActive") || !strings.Contains(joined, "ResourcePublisher.publish") || !strings.Contains(joined, "publishEntry") {
		t.Fatalf("expected strict mode to keep the business publish branch, got %q", joined)
	}
}

func TestApplyStrictFlowMode_PrefersSubmitEndpointForDataHop(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{Repo: "resource-api", Handler: "ResourceController.save", File: "resources.go"},
	}
	hops := []FlowHop{
		{Depth: 0, From: FlowEndpoint{Repo: "resource-api", Handler: "ResourceController.save", File: "resources.go"}, Via: FlowVia{Type: "call"}, To: FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"}},
		{Depth: 1, From: FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"}, Via: FlowVia{Type: "data", Entity: "Resources", Access: "publish"}, To: FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id"}},
		{Depth: 1, From: FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"}, Via: FlowVia{Type: "data", Entity: "Resources", Access: "publish"}, To: FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id/save"}},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 4)
	if len(got) != 2 {
		t.Fatalf("expected strict chain of 2 hops, got %d", len(got))
	}
	last := got[len(got)-1]
	if last.Via.Type != "data" {
		t.Fatalf("expected data hop as strict cross-service hop, got %q", last.Via.Type)
	}
	if last.To.Path != "/resource/:id/save" {
		t.Fatalf("expected strict mode to prefer submit endpoint, got %q", last.To.Path)
	}
}

func TestApplyStrictFlowMode_EndpointRootMatchesInternalHopsWithoutMethodPath(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{
			Repo:    "resource-api",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
			Method:  "POST",
			Path:    "/resources/save",
			Line:    216,
		},
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceController.save", File: "src/ResourceController.java"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"},
			Via:   FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To:    FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id/save"},
		},
		// Noise that should not be returned when strict mode is working.
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "shared-api", Handler: "HeaderUtility.authorizeHeaders"},
		},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 4)
	if len(got) == 0 {
		t.Fatalf("expected strict mode to keep a core chain, got 0 hops")
	}
	if len(got) > 2 {
		t.Fatalf("expected strict endpoint chain to stay concise (<=2 hops), got %d", len(got))
	}
	if got[len(got)-1].Via.Type != "data" {
		t.Fatalf("expected strict chain to end on explicit data hop, got %q", got[len(got)-1].Via.Type)
	}
}

func TestApplyStrictFlowMode_FallsBackToRootAnchoredCallChainWithoutCrossServiceHop(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{Repo: "web-app", Handler: "ResourceAction.save"},
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "web-app", Handler: "ResourceAction.save"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "web-app", Handler: "ResourceService.update"},
		},
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "web-app", Handler: "ResourceService.update"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "core-app", Handler: "ResourceClient.post"},
		},
		// Unrelated noise that must not be returned for this root.
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "background-worker", Handler: "Worker.run"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "core-app", Handler: "LogEntity.insert"},
		},
	}

	got := applyStrictFlowMode(roots, normalizeFlowHops(hops), 8)
	if len(got) != 2 {
		t.Fatalf("expected strict fallback to keep only root-anchored call chain (2 hops), got %d", len(got))
	}
	if got[0].From.Handler != "ResourceAction.save" || got[0].To.Handler != "ResourceService.update" {
		t.Fatalf("unexpected first hop: %#v", got[0])
	}
	if got[1].From.Handler != "ResourceService.update" || got[1].To.Handler != "ResourceClient.post" {
		t.Fatalf("unexpected second hop: %#v", got[1])
	}
}

func TestBuildFlowNarratives_UsesRootSpecificCallChain(t *testing.T) {
	t.Parallel()

	roots := []FlowEndpoint{
		{
			Repo:    "resource-api",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
			Method:  "POST",
			Path:    "/resources/save",
			Line:    216,
		},
		{
			Repo:    "resource-api",
			Handler: "ResourceController.update",
			File:    "src/ResourceController.java",
			Method:  "PUT",
			Path:    "/resources/save",
			Line:    261,
		},
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceController.save", File: "src/ResourceController.java"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.create", File: "src/ResourceService.java"},
		},
		{
			Depth: 0,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceController.update", File: "src/ResourceController.java"},
			Via:   FlowVia{Type: "call"},
			To:    FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update", File: "src/ResourceService.java"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.create", File: "src/ResourceService.java"},
			Via:   FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To:    FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id/save"},
		},
		{
			Depth: 1,
			From:  FlowEndpoint{Repo: "resource-api", Handler: "ResourceService.update", File: "src/ResourceService.java"},
			Via:   FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To:    FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id/save"},
		},
	}

	narratives := buildFlowNarratives(context.Background(), nil, roots, normalizeFlowHops(hops), nil, nil, false, nil)
	if len(narratives) != 2 {
		t.Fatalf("expected 2 narratives, got %d", len(narratives))
	}

	byMethod := map[string]FlowNarrative{}
	for _, n := range narratives {
		byMethod[n.Root.Method] = n
	}

	post, ok := byMethod["POST"]
	if !ok {
		t.Fatalf("missing POST narrative")
	}
	put, ok := byMethod["PUT"]
	if !ok {
		t.Fatalf("missing PUT narrative")
	}

	if len(post.Steps) == 0 || post.Steps[0].To.Handler != "ResourceService.create" {
		t.Fatalf("expected POST root to follow saveNewPage, got %#v", post.Steps)
	}
	if len(put.Steps) == 0 || put.Steps[0].To.Handler != "ResourceService.update" {
		t.Fatalf("expected PUT root to follow saveExistingPage, got %#v", put.Steps)
	}
}

func TestSameEndpoint_ToleratesMissingMethodAndPathOnHopNodes(t *testing.T) {
	t.Parallel()

	root := FlowEndpoint{
		Repo:    "web-app",
		Handler: "ResourceAction.save",
		File:    "src/ResourceAction.java",
		Method:  "REQUEST",
		Path:    "/action/Resource/save",
	}
	hopFrom := FlowEndpoint{
		Repo:    "web-app",
		Handler: "ResourceAction.save",
		File:    "src/ResourceAction.java",
	}
	if !sameEndpoint(root, hopFrom) {
		t.Fatalf("expected sameEndpoint to match when method/path are omitted on hop node")
	}
}

func TestExtractBranchConditionFromSource_FindsNearestIf(t *testing.T) {
	t.Parallel()

	source := `public void saveResource() {
  if (resource == null) {
    return;
  }
  if (existing != null) {
    doExisting();
  } else {
    doNew();
  }
}`
	condition := extractBranchConditionFromSource(source, 100, 106)
	if condition == "" {
		t.Fatalf("expected a condition, got empty")
	}
	if condition != "if (existing != null)" {
		t.Fatalf("unexpected condition: %q", condition)
	}
}

func TestBuildNarrativeForRoot_AddsBranchStepWithUnknownWhenConditionMissing(t *testing.T) {
	t.Parallel()

	root := FlowEndpoint{
		Repo:    "resource-api",
		Handler: "ResourceController.save",
		File:    "src/ResourceController.java",
		Method:  "POST",
		Path:    "/resources/save",
		Line:    216,
	}
	hops := []FlowHop{
		{
			Depth: 0,
			From:  root,
			Via:   FlowVia{Type: "call"},
			To: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.create",
				File:    "src/ResourceService.java",
				Line:    34,
			},
		},
		{
			Depth: 0,
			From:  root,
			Via:   FlowVia{Type: "call"},
			To: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.update",
				File:    "src/ResourceService.java",
				Line:    78,
			},
		},
		{
			Depth: 1,
			From: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.create",
				File:    "src/ResourceService.java",
				Line:    34,
			},
			Via: FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To: FlowEndpoint{
				Repo:    "resource-ui",
				Method:  "REQUEST",
				Path:    "/resource/:id/save",
				Handler: "ResourceForm.submit",
				File:    "src/ResourceForm.java",
				Line:    695,
			},
		},
	}

	steps := buildNarrativeForRoot(context.Background(), nil, root, normalizeFlowHops(hops), nil, nil, false, nil)
	if len(steps) < 3 {
		t.Fatalf("expected at least 3 steps (branch + call + data hop), got %d", len(steps))
	}
	if steps[0].Kind != "branch" {
		t.Fatalf("expected first step to be branch, got %q", steps[0].Kind)
	}
	if steps[0].Condition != "unknown" {
		t.Fatalf("expected unknown branch condition when source evidence unavailable, got %q", steps[0].Condition)
	}
	if steps[0].Confidence != "unknown" {
		t.Fatalf("expected unknown confidence for unresolved branch condition, got %q", steps[0].Confidence)
	}
	if len(steps[0].Evidence) == 0 {
		t.Fatalf("expected branch step to include endpoint evidence")
	}
}

func TestEnrichNarrativeStepEvidence_UnknownWhenEndpointLocationMissing(t *testing.T) {
	t.Parallel()

	step := FlowNarrativeStep{
		Kind: "hop",
		From: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
		},
		Via: &FlowVia{Type: "call"},
		To: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceService.create",
			File:    "src/ResourceService.java",
			Line:    34,
		},
	}

	enrichNarrativeStepEvidence(&step, nil)
	if step.Confidence != "unknown" {
		t.Fatalf("expected unknown confidence when endpoint location evidence is incomplete, got %q", step.Confidence)
	}
}

func TestEnforceNarrativeStepEvidenceConfidence_DowngradesExactWhenLocationMissing(t *testing.T) {
	t.Parallel()

	step := FlowNarrativeStep{
		Kind:       "hop",
		Confidence: "exact",
		From: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
			Line:    216,
		},
		To: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceService.create",
			File:    "src/ResourceService.java",
		},
	}

	enforceNarrativeStepEvidenceConfidence(&step)
	if step.Confidence != "unknown" {
		t.Fatalf("expected exact confidence to be downgraded when endpoint evidence is incomplete, got %q", step.Confidence)
	}
}

func TestEnrichNarrativeStepEvidence_ExactWhenBothEndpointLocationsPresent(t *testing.T) {
	t.Parallel()

	step := FlowNarrativeStep{
		Kind: "hop",
		From: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceController.save",
			File:    "src/ResourceController.java",
			Line:    216,
		},
		To: FlowEndpoint{
			Repo:    "resource-api",
			Handler: "ResourceService.create",
			File:    "src/ResourceService.java",
			Line:    34,
		},
	}

	enrichNarrativeStepEvidence(&step, nil)
	if step.Confidence != "exact" {
		t.Fatalf("expected exact confidence when both endpoint locations are present, got %q", step.Confidence)
	}
	if len(step.Evidence) != 2 {
		t.Fatalf("expected endpoint evidence refs for both sides, got %v", step.Evidence)
	}
	evidence := make(map[string]bool, len(step.Evidence))
	for _, ref := range step.Evidence {
		evidence[ref] = true
	}
	if !evidence["resource-api/src/ResourceController.java:216"] {
		t.Fatalf("expected from endpoint line evidence, got %v", step.Evidence)
	}
	if !evidence["resource-api/src/ResourceService.java:34"] {
		t.Fatalf("expected to endpoint line evidence, got %v", step.Evidence)
	}
}

func TestFindBestRelatedDataHop_UsesDetailedContextWhenRootChainMissing(t *testing.T) {
	t.Parallel()

	root := FlowEndpoint{
		Repo:    "resource-api",
		Handler: "ResourceController.save",
		File:    "src/ResourceController.java",
	}
	focus := FlowEndpoint{
		Repo:    "resource-api",
		Handler: "ResourceController.save",
		File:    "src/ResourceController.java",
	}
	detailed := []FlowNarrativeStep{
		{
			Kind: "hop",
			From: root,
			Via:  &FlowVia{Type: "call"},
			To: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.create",
				File:    "src/ResourceService.java",
			},
		},
	}
	hops := []FlowHop{
		{
			Depth: 1,
			From: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.create",
				File:    "src/ResourceService.java",
			},
			Via: FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To:  FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id"},
		},
		{
			Depth: 1,
			From: FlowEndpoint{
				Repo:    "resource-api",
				Handler: "ResourceService.create",
				File:    "src/ResourceService.java",
			},
			Via: FlowVia{Type: "data", Entity: "Resources", Access: "publish"},
			To:  FlowEndpoint{Repo: "resource-ui", Method: "REQUEST", Path: "/resource/:id/save"},
		},
	}

	best := findBestRelatedDataHop(nil, hops, root, &focus, detailed)
	if best == nil {
		t.Fatalf("expected related data hop, got nil")
	}
	if best.To.Path != "/resource/:id/save" {
		t.Fatalf("expected submit endpoint data hop, got %q", best.To.Path)
	}
}
