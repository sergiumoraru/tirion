package parser

import "testing"

func TestParserFixtureCorpus_JavaScriptGraphQLOperationUsage(t *testing.T) {
	content := []byte(`
import CreateResourceMutationQuery from '@/shared/graphql/mutation/CreateResource.gql';

export function useCreateResourceFields() {
  return useMutation(CreateResourceMutationQuery);
}
`)

	result := NewJavaScriptParser().ParseFile("useCreateResourceFields.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLOperationUsages) == 0 {
		t.Fatalf("expected GraphQL operation usages, got none")
	}

	found := false
	for _, usage := range result.GraphQLOperationUsages {
		if usage.ImportPath == "@/shared/graphql/mutation/CreateResource.gql" &&
			usage.ImportedAs == "CreateResourceMutationQuery" &&
			usage.FunctionName == "useCreateResourceFields" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CreateResourceMutationQuery usage in useCreateResourceFields, got %#v", result.GraphQLOperationUsages)
	}
}

func TestParserFixtureCorpus_VueTemplateOnlyGraphQLOperationUsage(t *testing.T) {
	content := []byte(`
<template>
  <resource-list
    :query="allResourcesQuery"
    :count-query="resourceCountQuery"
  />
</template>

<script setup lang="ts">
import allResourcesQuery from './query/AllResources.gql';
import resourceCountQuery from './query/ResourceCount.gql';
</script>
`)

	result := NewJavaScriptParser().ParseFile("src/ResourceList.vue", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	want := map[string]string{
		"allResourcesQuery":  "./query/AllResources.gql",
		"resourceCountQuery": "./query/ResourceCount.gql",
	}
	for importedAs, importPath := range want {
		found := false
		for _, usage := range result.GraphQLOperationUsages {
			if usage.ImportedAs == importedAs &&
				usage.ImportPath == importPath &&
				usage.FunctionName == "_template_" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected template usage %s from %s, got %#v", importedAs, importPath, result.GraphQLOperationUsages)
		}
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLBackendEntrypoint(t *testing.T) {
	content := []byte(`
const registry = require('../registry.js')

export async function handler(context, req) {
  const resolvers = registry.register({
    controllersPath: path.join(__dirname, 'controllers')
  })
  return resolvers
}
`)

	result := graphqlRegistrationFixtureParser("controllersPath").ParseFile("api/server.js", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLBackendEntrypoints) != 1 {
		t.Fatalf("expected one backend entrypoint, got %#v", result.GraphQLBackendEntrypoints)
	}
	entry := result.GraphQLBackendEntrypoints[0]
	if entry.RegistrationKind != "registry.register" {
		t.Fatalf("unexpected registration kind: %#v", entry)
	}
	if entry.HandlerName != "handler" {
		t.Fatalf("expected handler as containing function, got %#v", entry)
	}
	if entry.ControllersPath == "" {
		t.Fatalf("expected controllers path to be captured, got %#v", entry)
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLBackendEntrypointPositionalPath(t *testing.T) {
	content := []byte(`
const registry = require('../registry.js')
const path = require('path')

const resolvers = registry.register(path.join(__dirname, 'controllers'));
`)

	result := graphqlRegistrationFixtureParser("").ParseFile("public/server.js", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLBackendEntrypoints) != 1 {
		t.Fatalf("expected one backend entrypoint, got %#v", result.GraphQLBackendEntrypoints)
	}
	entry := result.GraphQLBackendEntrypoints[0]
	if entry.ControllersPath != "path.join(__dirname, 'controllers')" {
		t.Fatalf("expected positional controllers path, got %#v", entry)
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLOperationResolvers(t *testing.T) {
	content := []byte(`
module.exports.queries = [
  'resources',
  'resourceGroups'
]

module.exports.mutations = [
  'createResource'
]

module.exports.resources = async (_parent, args, context, info) => {
  await context.db.resource.findMany()
  return []
}

module.exports.resourceGroups = (parent, args, context, info) => {
  return []
}

module.exports.createResource = auth(async (parent, args, context, info) => {
  return {}
})

module.exports.router = express.Router()
module.exports.db = knex(config)
module.exports[types.root] = {}
`)

	result := NewJavaScriptParser().ParseFile("resolvers/resources.js", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	want := map[string]string{
		"query:resources":         "resources",
		"query:resourceGroups":    "resourceGroups",
		"mutation:createResource": "createResource",
	}
	if len(result.GraphQLOperationResolvers) != len(want) {
		t.Fatalf("resolvers = %#v, want %d", result.GraphQLOperationResolvers, len(want))
	}
	for _, resolver := range result.GraphQLOperationResolvers {
		key := resolver.OperationType + ":" + resolver.OperationName
		if want[key] != resolver.ResolverName {
			t.Fatalf("resolver %s = %#v, want resolver name %q", key, resolver, want[key])
		}
	}
	if !parsedFunctionsContain(result.Functions, "resources") {
		t.Fatalf("expected resources export to be indexed as a function, got %#v", result.Functions)
	}
	if parsedFunctionsContain(result.Functions, "router") || parsedFunctionsContain(result.Functions, "db") {
		t.Fatalf("expected non-resolver module exports to be ignored, got %#v", result.Functions)
	}
	if calls := result.FunctionCalls["resources"]; len(calls) == 0 {
		t.Fatalf("expected calls inside resources to be attributed to resolver function, got %#v", result.FunctionCalls)
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLPermissionRulesAreNotResolvers(t *testing.T) {
	content := []byte(`
import { allow, or, rule } from 'graphql-shield'

const canReadResources = rule()(async (_parent, args, context) => {
  return context.user.admin
})

export default {
  Query: {
    allResources: or(allow, canReadResources),
    resource: allow,
  },
  Mutation: {
    updateResource: canReadResources,
  },
}
`)

	result := NewJavaScriptParser().ParseFile("permissions.js", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLOperationResolvers) != 0 {
		t.Fatalf("permission rules must not be indexed as resolvers: %#v", result.GraphQLOperationResolvers)
	}
	want := map[string]string{
		"query:allResources":      "or(allow, canReadResources)",
		"query:resource":          "allow",
		"mutation:updateResource": "canReadResources",
	}
	if len(result.GraphQLOperationPermissions) != len(want) {
		t.Fatalf("permissions = %#v, want %d", result.GraphQLOperationPermissions, len(want))
	}
	for _, permission := range result.GraphQLOperationPermissions {
		key := permission.OperationType + ":" + permission.OperationName
		if want[key] != permission.RuleExpression {
			t.Fatalf("permission %s = %#v, want rule %q", key, permission, want[key])
		}
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLRegistryUsage(t *testing.T) {
	content := []byte(`
import getQuery from '@/config/graphql.queries';

export function useResources() {
  return getQuery('resources', 'GetResources');
}
`)

	result := NewJavaScriptParser().ParseFile("src/composables/useResources.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLOperationUsages) != 1 {
		t.Fatalf("expected one GraphQL usage, got %#v", result.GraphQLOperationUsages)
	}
	usage := result.GraphQLOperationUsages[0]
	if usage.ImportPath != "__graphql_operation__:GetResources" || usage.ImportedAs != "GetResources" {
		t.Fatalf("unexpected usage payload: %#v", usage)
	}
	if usage.FunctionName != "useResources" {
		t.Fatalf("expected useResources as caller, got %#v", usage)
	}
}

func TestParserFixtureCorpus_JavaScriptGraphQLClientRequestUsage(t *testing.T) {
	content := []byte(
		"const createResource = `\n" +
			"  mutation {\n" +
			"    createResource(data: { name: \"Acme\" }) {\n" +
			"      id\n" +
			"    }\n" +
			"  }\n" +
			"`\n\n" +
			"export async function createResourceRequest(graphQLClient) {\n" +
			"  return graphQLClient.request(createResource)\n" +
			"}\n\n" +
			"export async function loadPermissions(graphQLClient) {\n" +
			"  return graphQLClient.request(`query GetPermissions { permissions { id } }`)\n" +
			"}\n")

	result := NewJavaScriptParser().ParseFile("src/createResourceRequest.js", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	want := map[string]string{
		"createResourceRequest": "createResource",
		"loadPermissions":       "GetPermissions",
	}
	for functionName, operationName := range want {
		found := false
		for _, usage := range result.GraphQLOperationUsages {
			if usage.FunctionName == functionName &&
				usage.ImportedAs == operationName &&
				usage.ImportPath == "__graphql_operation__:"+operationName {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %s to use GraphQL operation %s, got %#v", functionName, operationName, result.GraphQLOperationUsages)
		}
	}
}

func TestParserFixtureCorpus_JavaScriptAxiosGraphQLBodyUsage(t *testing.T) {
	content := []byte(`
export async function callResourceApi(token) {
  return axios({
    url: this.environment.graphqlEndpoint,
    method: 'post',
    headers: { Authorization: token },
    data: {
      query: ` + "`" + `{
        resourceLabels { references labels }
      }` + "`" + `,
    },
  })
}

export async function callWithOptions(token) {
  const options = {
    url: this.environment.graphqlEndpoint,
    method: 'post',
    data: {
      query: ` + "`" + `mutation UpdateRole { updateRole(id: "1") { id } }` + "`" + `,
    },
  }
  return axios(options)
}
`)

	result := NewJavaScriptParser().ParseFile("src/services/resources.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	want := map[string]string{
		"callResourceApi": "resourceLabels",
		"callWithOptions": "UpdateRole",
	}
	for functionName, operationName := range want {
		found := false
		for _, usage := range result.GraphQLOperationUsages {
			if usage.FunctionName == functionName &&
				usage.ImportedAs == operationName &&
				usage.ImportPath == "__graphql_operation__:"+operationName {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %s to use GraphQL operation %s, got %#v", functionName, operationName, result.GraphQLOperationUsages)
		}
	}
}

func TestParserFixtureCorpus_JavaScriptEmbeddedGraphQLOperations(t *testing.T) {
	content := []byte(`
export const queries = [
  {
    query: 'GetResources',
    graphql: 'query GetResources($where: ResourceWhereInput!, $take: Int) {\n  resources(where: $where, take: $take) {\n    id\n  }\n}\n',
  },
];
`)

	result := NewJavaScriptParser().ParseFile("src/config/graphql.queries.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.GraphQLOperations) != 1 {
		t.Fatalf("expected one embedded operation, got %#v", result.GraphQLOperations)
	}
	op := result.GraphQLOperations[0]
	if op.Name != "GetResources" || op.OperationType != "query" {
		t.Fatalf("unexpected embedded operation: %#v", op)
	}
}

func TestParserFixtureCorpus_JavaScriptClassFieldArrowMethodsBecomeFunctions(t *testing.T) {
	content := []byte(`
export default class ResourcePublisher {
  constructor() {}

  processActive = async (): Promise<any> => {
    return await storeMessages();
  };

  publish = async (): Promise<void> => {
    await updateResources();
  };
}
`)

	result := NewJavaScriptParser().ParseFile("src/resource-publisher.ts", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	var names []string
	for _, fn := range result.Functions {
		names = append(names, fn.Name)
	}

	expected := map[string]bool{
		"ResourcePublisher.constructor":   false,
		"ResourcePublisher.processActive": false,
		"ResourcePublisher.publish":       false,
	}
	for _, name := range names {
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Fatalf("expected %s to be extracted as a function, got %#v", name, names)
		}
	}
}

func parsedFunctionsContain(functions []ParsedFunction, name string) bool {
	for _, fn := range functions {
		if fn.Name == name {
			return true
		}
	}
	return false
}
