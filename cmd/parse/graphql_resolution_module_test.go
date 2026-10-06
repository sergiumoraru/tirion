package main

import (
	"reflect"
	"testing"
)

func TestCandidateGraphQLImportPaths_Relative(t *testing.T) {
	got := candidateGraphQLImportPaths("src/views/foo/useThing.ts", "../graphql/query/GetThing.gql")
	want := []string{"src/views/graphql/query/GetThing.gql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got=%v want=%v", got, want)
	}
}

func TestCandidateGraphQLImportPaths_Alias(t *testing.T) {
	got := candidateGraphQLImportPaths("src/composables/useThing.ts", "@/shared/graphql/mutation/CreateThing.gql")
	want := []string{
		"shared/graphql/mutation/CreateThing.gql",
		"src/shared/graphql/mutation/CreateThing.gql",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got=%v want=%v", got, want)
	}
}

func TestCandidateGraphQLImportPaths_AddsMissingExtensions(t *testing.T) {
	got := candidateGraphQLImportPaths("src/composables/useThing.ts", "./graphql/query/GetThing")
	want := []string{
		"src/composables/graphql/query/GetThing",
		"src/composables/graphql/query/GetThing.gql",
		"src/composables/graphql/query/GetThing.graphql",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got=%v want=%v", got, want)
	}
}

func TestCandidateGraphQLImportPaths_GenericAlias(t *testing.T) {
	got := candidateGraphQLImportPaths("src/helpers/populateConfig.js", "@apollo/query/GetConfig.gql")
	want := []string{
		"query/GetConfig.gql",
		"src/query/GetConfig.gql",
		"apollo/query/GetConfig.gql",
		"src/apollo/query/GetConfig.gql",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected candidates: got=%v want=%v", got, want)
	}
}

func TestDirectGraphQLOperationName(t *testing.T) {
	got, ok := directGraphQLOperationName("__graphql_operation__:GetResource", "GetResource")
	if !ok {
		t.Fatalf("expected direct operation name match")
	}
	if got != "GetResource" {
		t.Fatalf("unexpected direct operation name: %q", got)
	}
}
