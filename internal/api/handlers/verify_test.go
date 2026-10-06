package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractParamNamesByLanguage(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ext  string
		want []string
	}{
		{
			name: "csharp attributes",
			raw:  "[FromBody] ResourceRequest request, [FromUri] int resourceId",
			ext:  ".cs",
			want: []string{"request", "resourceId"},
		},
		{
			name: "typescript optional",
			raw:  "userId: string, count?: number",
			ext:  ".ts",
			want: []string{"userId", "count"},
		},
		{
			name: "go",
			raw:  "ctx context.Context, userID int64",
			ext:  ".go",
			want: []string{"ctx", "userID"},
		},
		{
			name: "java validation annotation",
			raw:  "@Valid SearchRequest request, String status",
			ext:  ".java",
			want: []string{"request", "status"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractParamNames(tc.raw, tc.ext)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDetectVerifyEndpointBreakingChangesParameterRemoved(t *testing.T) {
	diff := `diff --git a/Controllers/ResourceController.cs b/Controllers/ResourceController.cs
--- a/Controllers/ResourceController.cs
+++ b/Controllers/ResourceController.cs
@@ -122,1 +122,1 @@
-public IHttpActionResult Create([FromBody] ResourceRequest request)
+public IHttpActionResult Create()
`
	endpoints := []ImpactEndpoint{{
		Method:  "POST",
		Path:    "/api/resources",
		Handler: "ResourceController.Create",
		Repo:    "resource-api",
		File:    "Controllers/ResourceController.cs",
		Line:    122,
	}}

	got := detectVerifyEndpointBreakingChanges(diff, endpoints)
	if len(got) != 1 {
		t.Fatalf("got %d breaking changes, want 1: %#v", len(got), got)
	}
	if got[0].ChangeType != "parameter_removed" || got[0].Endpoint != "POST /api/resources" {
		t.Fatalf("unexpected breaking change: %#v", got[0])
	}
	if len(got[0].RemovedParams) != 1 || got[0].RemovedParams[0] != "request" {
		t.Fatalf("unexpected removed params: %#v", got[0].RemovedParams)
	}
	if got[0].Evidence.File != "Controllers/ResourceController.cs" || got[0].Evidence.Line != 122 {
		t.Fatalf("unexpected evidence: %#v", got[0].Evidence)
	}
}

func TestDetectVerifyEndpointBreakingChangesEndpointRemoved(t *testing.T) {
	diff := `diff --git a/Controllers/ResourceController.cs b/Controllers/ResourceController.cs
--- a/Controllers/ResourceController.cs
+++ b/Controllers/ResourceController.cs
@@ -122,1 +122,0 @@
-public IHttpActionResult Create([FromBody] ResourceRequest request)
`
	endpoints := []ImpactEndpoint{{
		Method:  "POST",
		Path:    "/api/resources",
		Handler: "ResourceController.Create",
		Repo:    "resource-api",
		File:    "Controllers/ResourceController.cs",
		Line:    122,
	}}

	got := detectVerifyEndpointBreakingChanges(diff, endpoints)
	if len(got) != 1 {
		t.Fatalf("got %d breaking changes, want 1: %#v", len(got), got)
	}
	if got[0].ChangeType != "endpoint_removed" {
		t.Fatalf("unexpected breaking change: %#v", got[0])
	}
	if got[0].Severity != "critical" {
		t.Fatalf("endpoint removal should be critical: %#v", got[0])
	}
}

func TestDetectVerifyEndpointBreakingChangesParameterTypeChanged(t *testing.T) {
	diff := `diff --git a/Controllers/ResourceController.cs b/Controllers/ResourceController.cs
--- a/Controllers/ResourceController.cs
+++ b/Controllers/ResourceController.cs
@@ -122,1 +122,1 @@
-public IHttpActionResult Create([FromBody] ResourceRequest request)
+public IHttpActionResult Create([FromBody] ResourceRequestV2 request)
`
	endpoints := []ImpactEndpoint{{
		Method:  "POST",
		Path:    "/api/resources",
		Handler: "ResourceController.Create",
		Repo:    "resource-api",
		File:    "Controllers/ResourceController.cs",
		Line:    122,
	}}

	got := detectVerifyEndpointBreakingChanges(diff, endpoints)
	if len(got) != 1 {
		t.Fatalf("got %d breaking changes, want 1: %#v", len(got), got)
	}
	if got[0].ChangeType != "parameter_type_changed" {
		t.Fatalf("unexpected breaking change: %#v", got[0])
	}
	if len(got[0].ChangedParams) != 1 {
		t.Fatalf("unexpected changed params: %#v", got[0].ChangedParams)
	}
	change := got[0].ChangedParams[0]
	if change.Name != "request" || change.OldType != "ResourceRequest" || change.NewType != "ResourceRequestV2" {
		t.Fatalf("unexpected parameter type change: %#v", change)
	}
}

func TestDetectVerifyEndpointBreakingChangesQualifiedHandlerName(t *testing.T) {
	diff := `diff --git a/src/main/java/com/example/UserController.java b/src/main/java/com/example/UserController.java
--- a/src/main/java/com/example/UserController.java
+++ b/src/main/java/com/example/UserController.java
@@ -42,1 +42,1 @@
-public Response updateUser(@Valid UserRequest request, String status) {
+public Response updateUser(@Valid UserRequest request) {
`
	endpoints := []ImpactEndpoint{{
		Method:  "PUT",
		Path:    "/users/{id}",
		Handler: "UserController.updateUser",
		Repo:    "user-api",
		File:    "src/main/java/com/example/UserController.java",
		Line:    42,
	}}

	got := detectVerifyEndpointBreakingChanges(diff, endpoints)
	if len(got) != 1 {
		t.Fatalf("got %d breaking changes, want 1: %#v", len(got), got)
	}
	if got[0].Severity != "high" {
		t.Fatalf("unchanged @Valid on a different parameter must not escalate removal severity: %#v", got[0])
	}
	if len(got[0].RemovedParams) != 1 || got[0].RemovedParams[0] != "status" {
		t.Fatalf("unexpected removed params: %#v", got[0].RemovedParams)
	}
}

func TestDetectVerifyEndpointBreakingChangesParamAddedIsNotBreaking(t *testing.T) {
	diff := `diff --git a/controller.ts b/controller.ts
--- a/controller.ts
+++ b/controller.ts
@@ -10,1 +10,1 @@
-function updateUser(userId: string) {}
+function updateUser(userId: string, status: string) {}
`
	endpoints := []ImpactEndpoint{{
		Method:  "POST",
		Path:    "/users/{userId}",
		Handler: "updateUser",
		Repo:    "api",
		File:    "controller.ts",
		Line:    10,
	}}
	if got := detectVerifyEndpointBreakingChanges(diff, endpoints); len(got) != 0 {
		t.Fatalf("param addition should not be breaking, got %#v", got)
	}
}

func TestDetectVerifyEndpointBreakingChangesNoEndpoints(t *testing.T) {
	diff := `diff --git a/controller.ts b/controller.ts
--- a/controller.ts
+++ b/controller.ts
@@ -10,1 +10,1 @@
-function updateUser(userId: string, status: string) {}
+function updateUser(userId: string) {}
`
	if got := detectVerifyEndpointBreakingChanges(diff, nil); len(got) != 0 {
		t.Fatalf("no endpoints should produce no breaking changes, got %#v", got)
	}
}

func TestVerifyVerdictTiering(t *testing.T) {
	result := &EstateVerifyResult{Verdict: "pass"}
	markVerifyExposure(result)
	if result.Verdict != "info" {
		t.Fatalf("exposure-only should promote pass to info, got %q", result.Verdict)
	}
	markVerifyExposure(result)
	if result.Verdict != "info" {
		t.Fatalf("exposure should not promote info beyond info, got %q", result.Verdict)
	}
	markVerifyIncomplete(result)
	if result.Verdict != "warn" {
		t.Fatalf("incomplete verification should promote info to warn, got %q", result.Verdict)
	}

	result.Verdict = "fail"
	markVerifyExposure(result)
	markVerifyIncomplete(result)
	if result.Verdict != "fail" {
		t.Fatalf("tiering must not downgrade fail, got %q", result.Verdict)
	}
}

func TestExtractParamsFromSignature(t *testing.T) {
	got := extractParamsFromSignature("public IHttpActionResult Create([FromBody] ResourceRequest request) {", "Create")
	want := "[FromBody] ResourceRequest request"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = extractParamsFromSignature("function updateUser(userId: string, meta: Record<string, string>) {", "updateUser")
	want = "userId: string, meta: Record<string, string>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVerifyDiffPreimage(t *testing.T) {
	worktree := t.TempDir()
	file := filepath.Join(worktree, "controller.ts")
	if err := os.WriteFile(file, []byte("line one\nline two\nline three\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	aligned := `diff --git a/controller.ts b/controller.ts
--- a/controller.ts
+++ b/controller.ts
@@ -1,3 +1,4 @@
 line one
+inserted
 line two
 line three
`
	errors, checked := verifyDiffPreimage(aligned, worktree)
	if len(errors) != 0 || checked != 3 {
		t.Fatalf("aligned preimage errors=%v checked=%d", errors, checked)
	}

	mismatched := `diff --git a/controller.ts b/controller.ts
--- a/controller.ts
+++ b/controller.ts
@@ -1,1 +1,1 @@
-different
+replacement
`
	errors, _ = verifyDiffPreimage(mismatched, worktree)
	if len(errors) == 0 {
		t.Fatal("mismatched preimage should be rejected")
	}
}

func TestAgentWorkEvidenceConnectedUsesImpactReport(t *testing.T) {
	report := ImpactReport{Nodes: []ImpactNode{{ID: "func:api:controller.ts:Controller.save", Type: "function"}}}
	if !agentWorkEvidenceConnected(
		[]string{"web:client.ts:submit"},
		[]string{"api:controller.ts:Controller.save"},
		report,
	) {
		t.Fatal("resolved impact function should connect evidence to the changed path")
	}
	if agentWorkEvidenceConnected(
		[]string{"web:client.ts:submit"},
		[]string{"other:worker.ts:run"},
		report,
	) {
		t.Fatal("unrelated evidence should not be connected")
	}
}

func TestAgentWorkAddedFilesGroundsNewSource(t *testing.T) {
	diff := `diff --git a/src/NewResolver.cs b/src/NewResolver.cs
new file mode 100644
--- /dev/null
+++ b/src/NewResolver.cs
@@ -0,0 +1,4 @@
+public class NewResolver
+{
+    public int Resolve() => 42;
+}
`
	files := agentWorkAddedFiles(diff, "api")
	content, ok := files[agentWorkFileKey("api", "src/NewResolver.cs")]
	if !ok {
		t.Fatalf("new file not captured: %#v", files)
	}
	if !diffContentContainsSymbol(content, "NewResolver.Resolve") {
		t.Fatalf("added source should contain Resolve symbol: %q", content)
	}
	if diffContentContainsSymbol(content, "NewResolver.Missing") {
		t.Fatal("missing symbol should not resolve from added source")
	}
}

func TestEquivalentPredecessorObligations(t *testing.T) {
	predecessors := []verifyFrontierNode{
		{CallerID: "repo:a.cs:Service.first", Kind: "call", Repo: "repo", File: "a.cs", Symbol: "Service.first", Line: 10},
		{CallerID: "repo:b.cs:Service.second", Kind: "call", Repo: "repo", File: "b.cs", Symbol: "Service.second", Line: 20},
		{CallerID: "repo:c.cs:Service.third", Kind: "call", Repo: "repo", File: "c.cs", Symbol: "Service.third", Line: 30},
	}

	t.Run("one changed leaves two missing", func(t *testing.T) {
		obligations, untouched := buildEquivalentPredecessorObligations(predecessors, map[string]bool{predecessors[0].CallerID: true}, nil)
		if untouched != 2 {
			t.Fatalf("untouched=%d, want 2", untouched)
		}
		want := []string{"satisfied", "missing", "missing"}
		for i := range want {
			if obligations[i].Status != want[i] {
				t.Fatalf("obligation %d status=%q, want %q", i, obligations[i].Status, want[i])
			}
			if len(obligations[i].Evidence) != 1 || obligations[i].Evidence[0].Line != predecessors[i].Line {
				t.Fatalf("obligation %d lost file:line evidence: %#v", i, obligations[i].Evidence)
			}
		}
		if got := summarizeFixValidation(obligations); got != "failed" {
			t.Fatalf("fix validation=%q, want failed", got)
		}
	})

	t.Run("waiver records but does not cover untouched path", func(t *testing.T) {
		changed := map[string]bool{predecessors[0].CallerID: true, predecessors[1].CallerID: true}
		waivers := []VerifyWaiverInput{{
			Class:         "equivalent_predecessor",
			Subject:       predecessors[2].CallerID,
			Justification: "separate path",
		}}
		obligations, untouched := buildEquivalentPredecessorObligations(predecessors, changed, waivers)
		if untouched != 1 {
			t.Fatalf("untouched=%d, want 1", untouched)
		}
		if obligations[2].Status != "waived" || obligations[2].Justification != "separate path" {
			t.Fatalf("waiver not preserved: %#v", obligations[2])
		}
		if got := summarizeFixValidation(obligations); got != "verified_with_waivers" {
			t.Fatalf("fix validation=%q, want verified_with_waivers", got)
		}
	})

	t.Run("all changed is verified", func(t *testing.T) {
		changed := map[string]bool{}
		for _, predecessor := range predecessors {
			changed[predecessor.CallerID] = true
		}
		obligations, untouched := buildEquivalentPredecessorObligations(predecessors, changed, nil)
		if untouched != 0 {
			t.Fatalf("untouched=%d, want 0", untouched)
		}
		if got := summarizeFixValidation(obligations); got != "verified" {
			t.Fatalf("fix validation=%q, want verified", got)
		}
	})
}
