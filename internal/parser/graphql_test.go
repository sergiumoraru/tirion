package parser

import "testing"

func TestParserFixtureCorpus_GraphQL(t *testing.T) {
	content := []byte(`#import "../fragments/Foo.gql"

query ResourceById($id: String!) {
  resources(where: { id: { equals: $id } }) {
    id
  }
}

mutation CreateResource($data: ResourceCreateInput!) {
  createResource(data: $data) {
    id
  }
}`)

	result := NewGraphQLParser().ParseFile("queries/ResourceById.gql", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "graphql" {
		t.Fatalf("expected graphql language, got %q", result.Language)
	}
	if len(result.Imports) != 1 || result.Imports[0].Path != "../fragments/Foo.gql" {
		t.Fatalf("unexpected imports: %#v", result.Imports)
	}
	if len(result.GraphQLOperations) != 2 {
		t.Fatalf("expected 2 operations, got %#v", result.GraphQLOperations)
	}
	if result.GraphQLOperations[0].Name != "ResourceById" || result.GraphQLOperations[0].OperationType != "query" || result.GraphQLOperations[0].LineNumber != 3 {
		t.Fatalf("unexpected first operation: %#v", result.GraphQLOperations[0])
	}
	if result.GraphQLOperations[1].Name != "CreateResource" || result.GraphQLOperations[1].OperationType != "mutation" || result.GraphQLOperations[1].LineNumber != 9 {
		t.Fatalf("unexpected second operation: %#v", result.GraphQLOperations[1])
	}
}

func TestParserFixtureCorpus_GraphQLSchemaOnly(t *testing.T) {
	content := []byte(`type Query {
  user(id: ID!): User
}

type User {
  id: ID!
}`)

	result := NewGraphQLParser().ParseFile("schema.graphql", content)

	if len(result.GraphQLOperations) != 0 {
		t.Fatalf("expected no operations for schema-only file, got %#v", result.GraphQLOperations)
	}
}
