package main

import (
	"reflect"
	"testing"
)

func TestCandidateGraphQLControllerDirs_PathJoin(t *testing.T) {
	got := candidateGraphQLControllerDirs("catalog/server.js", "path.join(__dirname, 'controllers')")
	want := []string{"catalog/controllers", "controllers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected controller dirs: got=%v want=%v", got, want)
	}
}

func TestCandidateGraphQLControllerDirs_NestedPathJoin(t *testing.T) {
	got := candidateGraphQLControllerDirs("templates/product/server.js", "path.join(__dirname, 'controllers', 'shared')")
	want := []string{"templates/product/controllers/shared", "controllers/shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected controller dirs: got=%v want=%v", got, want)
	}
}

func TestGraphQLControllerLinkConfidence(t *testing.T) {
	if got := graphqlControllerLinkConfidence("catalog/controllers/StatusController.js", []string{"catalog/controllers"}); got != "high" {
		t.Fatalf("unexpected confidence: %q", got)
	}
	if got := graphqlControllerLinkConfidence("catalog/server.js", []string{"catalog/controllers"}); got != "" {
		t.Fatalf("expected no confidence, got %q", got)
	}
}
