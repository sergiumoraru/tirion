package trace

import "testing"

func TestNormalizeEndpointPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "full url with query and fragment",
			in:   "https://api.example.com/v1/users/42?expand=true#section",
			want: "/v1/users/42",
		},
		{
			name: "trim trailing slash and leading whitespace",
			in:   "  users/  ",
			want: "/users",
		},
		{
			name: "empty after query stripped",
			in:   "?x=1",
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeEndpointPattern(tt.in)
			if got != tt.want {
				t.Fatalf("normalizeEndpointPattern(%q): got %q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseEndpointStartForTraceLookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		wantMethod string
		wantPath   string
		wantOK     bool
	}{
		{name: "method path", in: "POST /api/CreateResource", wantMethod: "POST", wantPath: "/api/CreateResource", wantOK: true},
		{name: "bare path", in: "/api/CreateResource", wantMethod: "REQUEST", wantPath: "/api/CreateResource", wantOK: true},
		{name: "full url", in: "https://gateway.example.com/reports/export", wantMethod: "REQUEST", wantPath: "https://gateway.example.com/reports/export", wantOK: true},
		{name: "relative path", in: "reports/export", wantMethod: "REQUEST", wantPath: "reports/export", wantOK: true},
		{name: "function name", in: "CreateResource", wantOK: false},
		{name: "unknown method", in: "TRACE /api/CreateResource", wantOK: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotMethod, gotPath, gotOK := parseEndpointStartForTraceLookup(tt.in)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotMethod, gotPath, tt.wantMethod, tt.wantPath)
			}
		})
	}
}

func TestGatewayBackendRouteCandidates(t *testing.T) {
	t.Parallel()

	route := MatchedGatewayRoute{
		BackendURL:  "https://functions.example.com/api",
		BackendPath: "/TransformData/{id}",
	}
	candidates := gatewayBackendRouteCandidates(route)
	set := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		set[candidate] = true
	}

	if !set["/api/TransformData/{id}"] {
		t.Fatalf("expected combined backend route candidate, got %v", candidates)
	}
	if !set["/TransformData/{id}"] {
		t.Fatalf("expected rewrite-path route candidate, got %v", candidates)
	}
	if !set["/api"] {
		t.Fatalf("expected backend URL path candidate, got %v", candidates)
	}
}

func TestEndpointMatchVariants_PreserveParamFormsWithoutInventingAliases(t *testing.T) {
	t.Parallel()

	variants := endpointMatchVariants("/resources/{resourceId}/versions/:version", nil)
	set := make(map[string]bool, len(variants))
	for _, v := range variants {
		set[v] = true
	}

	expected := []string{
		"/resources/{resourceId}/versions/:version",
		"/resources/:resourceId/versions/:version",
		"/resources/{resourceId}/versions/{version}",
	}
	for _, want := range expected {
		if !set[want] {
			t.Fatalf("expected variant %q, got variants=%v", want, variants)
		}
	}
	if len(set) != len(expected) {
		t.Fatalf("unexpected route aliases: %v", variants)
	}
}

func TestEndpointMatchVariants_PreservesActionPrefix(t *testing.T) {
	t.Parallel()

	variants := endpointMatchVariants("/action/Resource/save", nil)
	set := make(map[string]bool, len(variants))
	for _, v := range variants {
		set[v] = true
	}

	if len(set) != 1 || !set["/action/Resource/save"] {
		t.Fatalf("route prefix or case changed: %v", variants)
	}
}

func TestEndpointMatchVariants_ContextAndVersionVariants(t *testing.T) {
	t.Parallel()

	variants := endpointMatchVariants("/v2/api/users/:id", []string{"/api"})
	set := make(map[string]bool, len(variants))
	for _, v := range variants {
		set[v] = true
	}

	expected := []string{
		"/v2/api/users/:id",
		"/v2/api/users/{id}",
		"/api/v2/api/users/:id", // explicit configured prefix
		"/api/v2/api/users/{id}",
	}
	for _, want := range expected {
		if !set[want] {
			t.Fatalf("expected variant %q, got variants=%v", want, variants)
		}
	}
}

func TestCanonicalEndpointIdentityPath(t *testing.T) {
	t.Parallel()

	got := canonicalEndpointIdentityPath("Resources//Save/")
	want := "/resources/save"
	if got != want {
		t.Fatalf("canonicalEndpointIdentityPath: got %q want %q", got, want)
	}
}

func TestDedupeMatchedEndpointsCanonical(t *testing.T) {
	t.Parallel()

	in := []MatchedEndpoint{
		{
			Repo:       "resource-api",
			File:       ".codebase-snapshots/run-123/src/ResourceController.java",
			Handler:    "ResourceController.save",
			Method:     "post",
			Path:       "Resources//Save/",
			LineNumber: 0,
		},
		{
			Repo:       "resource-api",
			File:       "src/ResourceController.java",
			Handler:    "ResourceController.save",
			Method:     "POST",
			Path:       "/resources/save",
			LineNumber: 216,
		},
	}

	out := dedupeMatchedEndpointsCanonical(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 canonical endpoint, got %d", len(out))
	}
	if out[0].LineNumber != 216 {
		t.Fatalf("expected non-snapshot line evidence to win, got %d", out[0].LineNumber)
	}
	if out[0].File != "src/ResourceController.java" {
		t.Fatalf("expected non-snapshot endpoint to win, got %q", out[0].File)
	}
}

func TestExtractMeaningfulSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "APIM rewritten URL extracts transform-data",
			in:   "/:apimUrl.value/service/:resourceType.value/transform-data/:id",
			want: []string{"transformdata"},
		},
		{
			name: "azure function endpoint extracts transformdata",
			in:   "/api/TransformData/{id}",
			want: []string{"transformdata"},
		},
		{
			name: "all params or short segments returns empty",
			in:   "/:id/:name/api/v2",
			want: nil,
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
		{
			name: "hyphens and underscores normalized",
			in:   "/api/user-management/export_data",
			want: []string{"usermanagement", "exportdata"},
		},
		{
			name: "common words filtered",
			in:   "/api/service/handler/create",
			want: nil,
		},
		{
			name: "mixed params and static segments",
			in:   "/notifications/:product/queue",
			want: []string{"notifications", "queue"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractMeaningfulSegments(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("extractMeaningfulSegments(%q): got %v want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("extractMeaningfulSegments(%q)[%d]: got %q want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}
