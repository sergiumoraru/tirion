package parser

import (
	"strings"
	"testing"
)

func TestAPIMParserCanParseOnlyAPIMLookingJSONPaths(t *testing.T) {
	p := NewAPIMParser()
	if !p.CanParse("infra/resource-apim.json") {
		t.Fatal("expected APIM-looking template path to be parsed")
	}
	if !p.CanParse("infra/logic/workflow.json") {
		t.Fatal("expected Logic App workflow path to be parsed")
	}
	if p.CanParse("local.settings.json") {
		t.Fatal("expected ordinary settings json to skip APIM parser")
	}
	if p.CanParse("package.json") {
		t.Fatal("expected generic json file to skip APIM parser")
	}
}

func TestParserFixtureCorpus_APIMGatewayRoutes(t *testing.T) {
	content := []byte(`{
  // APIs
  "resources": [
    {
      "type": "Microsoft.ApiManagement/service/apis",
      "name": "[concat(parameters('gatewayName'), '/resources')]",
      "properties": {
        "path": "resources"
      }
    },
    {
      "type": "Microsoft.ApiManagement/service/apis/operations",
      "name": "[concat(parameters('gatewayName'), '/resources/create-resource')]",
      "properties": {
        "method": "POST",
        "urlTemplate": "/create-resource/{type}/{id}"
      }
    },
    {
      "type": "Microsoft.ApiManagement/service/apis/operations/policies",
      "name": "[concat(parameters('gatewayName'), '/resources/create-resource/policy')]",
      "properties": {
        "value": "<policies><inbound><base /><set-backend-service base-url=\"https://resource-functions-{{environment}}.azurewebsites.net/api/CreateResource\" /><set-query-parameter name=\"code\" exists-action=\"override\"><value>{{create-resource-access-code}}</value></set-query-parameter><rewrite-uri template=\"/{type}/{id}\" copy-unmatched-params=\"true\" /></inbound><backend><base /></backend><outbound><base /></outbound><on-error><base /></on-error></policies>"
      }
    }
  ]
}`)

	result := NewAPIMParser().ParseFile("resource-apim.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse failure: %#v", result.ParseDiagnostics)
	}
	if len(result.GatewayRoutes) != 1 {
		t.Fatalf("expected 1 gateway route, got %#v", result.GatewayRoutes)
	}
	route := result.GatewayRoutes[0]
	if route.GatewayType != "azure_apim" || route.APIName != "resources" || route.OperationName != "create-resource" {
		t.Fatalf("unexpected route identity: %#v", route)
	}
	if route.PublicMethod != "POST" || route.PublicPath != "/resources/create-resource/{type}/{id}" {
		t.Fatalf("unexpected public route: %#v", route)
	}
	if route.BackendURL != "https://resource-functions-{{environment}}.azurewebsites.net/api/CreateResource" {
		t.Fatalf("unexpected backend url: %#v", route)
	}
	if route.BackendPath != "/{type}/{id}" {
		t.Fatalf("unexpected backend path: %#v", route)
	}
}

func TestParserFixtureCorpus_APIMSkipsNonTemplates(t *testing.T) {
	content := []byte(`{"name":"plain-json","value":1}`)
	result := NewAPIMParser().ParseFile("sample.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse failure: %#v", result.ParseDiagnostics)
	}
	if len(result.GatewayRoutes) != 0 {
		t.Fatalf("expected no gateway routes, got %#v", result.GatewayRoutes)
	}
}

func TestParserFixtureCorpus_LogicAppWorkflowDefinitionTriggers(t *testing.T) {
	content := []byte(`{
  "resources": [
    {
      "type": "Microsoft.Logic/workflows",
      "name": "processResources",
      "properties": {
        "definition": {
          "triggers": {
            "Recurrence": {
              "type": "Recurrence",
              "recurrence": {
                "frequency": "Day",
                "interval": 1,
                "schedule": {
                  "hours": [2],
                  "minutes": [30]
                }
              }
            }
          }
        }
      }
    }
  ]
}`)

	result := NewAPIMParser().ParseFile("infra/logic/workflow.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse failure: %#v", result.ParseDiagnostics)
	}
	if len(result.AzureTriggers) != 1 {
		t.Fatalf("expected one Logic App trigger, got %#v", result.AzureTriggers)
	}
	trigger := result.AzureTriggers[0]
	if trigger.FunctionName != "processResources/Recurrence" ||
		trigger.TriggerType != "timerTrigger" ||
		trigger.BindingName != "Recurrence" ||
		trigger.ResourceName != "processResources" {
		t.Fatalf("unexpected Logic App trigger identity: %#v", trigger)
	}
	for _, want := range []string{"frequency=Day", "interval=1", `"hours":[2]`, `"minutes":[30]`} {
		if !strings.Contains(trigger.Schedule, want) {
			t.Fatalf("schedule %q missing %q", trigger.Schedule, want)
		}
	}
}

func TestParserFixtureCorpus_LogicAppServiceBusTrigger(t *testing.T) {
	content := []byte(`{
  "resources": [
    {
      "type": "Microsoft.Logic/workflows",
      "name": "resourceConsumer",
      "properties": {
        "definition": {
          "triggers": {
            "When_messages_are_available_in_a_queue": {
              "type": "ApiConnection",
              "inputs": {
                "host": {
                  "connection": {
                    "name": "@parameters('$connections')['servicebus']['connectionId']"
                  }
                },
                "path": "/@{encodeURIComponent('resource-events')}/messages/head/peek",
                "queries": {
                  "queueName": "resource-events"
                }
              }
            }
          }
        }
      }
    }
  ]
}`)

	result := NewAPIMParser().ParseFile("infra/logic/resourceConsumer.LogicApp.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse failure: %#v", result.ParseDiagnostics)
	}
	if len(result.AzureTriggers) != 1 {
		t.Fatalf("expected one Logic App Service Bus trigger, got %#v", result.AzureTriggers)
	}
	trigger := result.AzureTriggers[0]
	if trigger.FunctionName != "resourceConsumer/When_messages_are_available_in_a_queue" ||
		trigger.TriggerType != "serviceBusTrigger" ||
		trigger.ResourceName != "resource-events" ||
		trigger.BindingName != "When_messages_are_available_in_a_queue" {
		t.Fatalf("unexpected Logic App Service Bus trigger: %#v", trigger)
	}
}

func TestParserFixtureCorpus_LogicAppTriggerResource(t *testing.T) {
	content := []byte(`{
  "resources": [
    {
      "type": "Microsoft.Logic/workflows/triggers",
      "name": "cleanupWorkflow/nightlyCleanup",
      "properties": {
        "recurrence": {
          "frequency": "Hour",
          "interval": 12
        }
      }
    }
  ]
}`)

	result := NewAPIMParser().ParseFile("infra/workflows/nightly-cleanup.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse failure: %#v", result.ParseDiagnostics)
	}
	if len(result.AzureTriggers) != 1 {
		t.Fatalf("expected one Logic App trigger resource, got %#v", result.AzureTriggers)
	}
	trigger := result.AzureTriggers[0]
	if trigger.FunctionName != "cleanupWorkflow/nightlyCleanup" ||
		trigger.TriggerType != "timerTrigger" ||
		trigger.BindingName != "nightlyCleanup" ||
		trigger.ResourceName != "cleanupWorkflow" ||
		trigger.Schedule != "frequency=Hour interval=12" {
		t.Fatalf("unexpected Logic App trigger resource: %#v", trigger)
	}
}
