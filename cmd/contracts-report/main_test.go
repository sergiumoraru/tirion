package main

import (
	"strings"
	"testing"
)

func TestContractsURLIncludesWorkspaceAndEscapesRepo(t *testing.T) {
	got := contractsURL("http://localhost:18080/", "Resource API/Worker", 50, "team-release")
	want := "http://localhost:18080/api/contracts/Resource%20API%2FWorker?limit=50&workspaceId=team-release"
	if got != want {
		t.Fatalf("contractsURL = %q, want %q", got, want)
	}
}

func TestSummaryReportIncludesGraphQLDataAndAzureTimers(t *testing.T) {
	report := buildMarkdownReport(contractsResponse{
		Services: []contractSummary{{
			Repo:               "resource-worker",
			EndpointCount:      1,
			HttpCallCount:      2,
			GraphQLOperations:  3,
			GraphQLResolvers:   4,
			DataAccessCount:    5,
			QueuesProduced:     6,
			QueuesConsumed:     7,
			AzureTimerTriggers: 8,
		}},
	}, "")

	for _, want := range []string{
		"resource-worker",
		"graphql 7",
		"data 5",
		"queues 6/7",
		"azure timers 8",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("summary report missing %q:\n%s", want, report)
		}
	}
}

func TestServiceReportIncludesGraphQLDataAndSchedules(t *testing.T) {
	report := buildMarkdownReport(contractsResponse{
		Service: &contractDetail{
			Repo: "resource-api",
			GraphQLOperations: []contractGraphQLOperation{{
				Name: "AllResources",
				Type: "query",
				File: "query/AllResources.gql",
				Line: 1,
			}},
			GraphQLUsages: []contractGraphQLUsage{{
				ImportedAs: "allResourcesQuery",
				ImportPath: "./query/AllResources.gql",
				Caller:     "ResourceList",
				File:       "ResourceList.vue",
				Line:       21,
			}},
			GraphQLResolvers: []contractGraphQLResolver{{
				OperationName: "allResources",
				OperationType: "query",
				Resolver:      "allResources",
				File:          "resolvers/resources.js",
				Line:          38,
			}},
			DataAccesses: []contractDataAccess{{
				Entity: "blob:resource",
				Access: "write",
				Caller: "writeResource",
				File:   "WriteResource/index.js",
				Line:   118,
			}},
			AzureTimerTriggers: []contractSchedule{{
				FunctionName:       "CleanupResources",
				ScheduleExpression: "0 0 0 * * *",
				State:              "ENABLED",
				File:               "CleanupResources/function.json",
				Line:               5,
			}},
		},
	}, "resource-api")

	for _, want := range []string{
		"## GraphQL Operations",
		"QUERY AllResources",
		"ResourceList uses allResourcesQuery",
		"QUERY allResources",
		"write blob:resource by writeResource",
		"## Azure Timer Triggers",
		"CleanupResources",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("service report missing %q:\n%s", want, report)
		}
	}
}
