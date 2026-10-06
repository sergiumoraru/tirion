package main

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestGraphQLDocumentsModulePersistExtrasQueuesOperations(t *testing.T) {
	module := newGraphQLDocumentsModule(parser.NewGraphQLParser())
	batch := &pgx.Batch{}

	err := module.PersistExtras(&modulePersistContext{
		repoID: 1,
		fileID: 2,
		batch:  batch,
	}, parser.ParsedFile{
		GraphQLOperations: []parser.ParsedGraphQLOperation{
			{Name: "CreateResource", OperationType: "mutation", LineNumber: 1},
			{Name: "GetResource", OperationType: "query", LineNumber: 10},
		},
	})
	if err != nil {
		t.Fatalf("unexpected persist error: %v", err)
	}
	if batch.Len() != 2 {
		t.Fatalf("expected 2 queued statements, got %d", batch.Len())
	}
}

func TestAzureFunctionsModulePersistExtrasQueuesTriggers(t *testing.T) {
	module := newAzureFunctionsModule(parser.NewAzureFunctionsParser())
	batch := &pgx.Batch{}

	err := module.PersistExtras(&modulePersistContext{
		repoID: 1,
		fileID: 2,
		batch:  batch,
	}, parser.ParsedFile{
		AzureTriggers: []parser.ParsedAzureTrigger{
			{FunctionName: "GetRoles", TriggerType: "httpTrigger", Direction: "in", BindingName: "req", Methods: []string{"GET"}, LineNumber: 5},
			{FunctionName: "Cleanup", TriggerType: "timerTrigger", Direction: "in", BindingName: "cleanupTimer", Schedule: "0 0 0 * * *", LineNumber: 12},
		},
	})
	if err != nil {
		t.Fatalf("unexpected persist error: %v", err)
	}
	if batch.Len() != 2 {
		t.Fatalf("expected 2 queued statements, got %d", batch.Len())
	}
}

func TestJavaScriptModulePersistExtrasQueuesGraphQLUsageAndEntrypoint(t *testing.T) {
	module := newJavaScriptParserModule(parser.NewJavaScriptParser(), func(relPath, absPath string, content []byte) parser.ParsedFile {
		return parser.ParsedFile{}
	})
	batch := &pgx.Batch{}

	err := module.PersistExtras(&modulePersistContext{
		repoID: 1,
		fileID: 2,
		batch:  batch,
	}, parser.ParsedFile{
		GraphQLOperationUsages: []parser.ParsedGraphQLOperationUsage{
			{ImportPath: "@/shared/graphql/query/Foo.gql", ImportedAs: "FooQuery", FunctionName: "loadFoo", LineNumber: 4},
		},
		GraphQLBackendEntrypoints: []parser.ParsedGraphQLBackendEntrypoint{
			{HandlerName: "_module_", RegistrationKind: "ApolloServer", ControllersPath: "path.join(__dirname, 'controllers')", LineNumber: 12},
		},
	})
	if err != nil {
		t.Fatalf("unexpected persist error: %v", err)
	}
	if batch.Len() != 2 {
		t.Fatalf("expected 2 queued statements, got %d", batch.Len())
	}
}

func TestAPIMModulePersistExtrasQueuesGatewayRoutes(t *testing.T) {
	module := newAPIMModule(parser.NewAPIMParser())
	batch := &pgx.Batch{}

	err := module.PersistExtras(&modulePersistContext{
		repoID: 1,
		fileID: 2,
		batch:  batch,
	}, parser.ParsedFile{
		GatewayRoutes: []parser.ParsedGatewayRoute{
			{
				GatewayType:   "azure_apim",
				APIName:       "resources",
				OperationName: "create",
				PublicMethod:  "POST",
				PublicPath:    "/resources/{type}/{id}",
				BackendMethod: "POST",
				BackendURL:    "https://resource-functions-{{environment}}.azurewebsites.net/api/CreateResource",
				BackendPath:   "/{type}/{id}",
				LineNumber:    10,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected persist error: %v", err)
	}
	if batch.Len() != 1 {
		t.Fatalf("expected 1 queued statement, got %d", batch.Len())
	}
}
