package main

import (
	"reflect"
	"testing"
)

func TestSemanticRegression_GraphQLOperationResolutionCandidates(t *testing.T) {
	t.Parallel()

	got := candidateGraphQLImportPaths("src/composables/useThing.ts", "@/shared/graphql/query/GetThing.gql")
	want := []string{
		"shared/graphql/query/GetThing.gql",
		"src/shared/graphql/query/GetThing.gql",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected GraphQL resolution candidates: got=%v want=%v", got, want)
	}

	name, ok := directGraphQLOperationName("__graphql_operation__:GetResource", "GetResource")
	if !ok || name != "GetResource" {
		t.Fatalf("expected direct GraphQL operation identity, got name=%q ok=%v", name, ok)
	}
}
