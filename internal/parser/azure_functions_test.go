package parser

import "testing"

func TestParserFixtureCorpus_AzureFunctionsHTTP(t *testing.T) {
	content := []byte(`{
  "disabled": false,
  "bindings": [
    {
      "authLevel": "anonymous",
      "type": "httpTrigger",
      "direction": "in",
      "name": "req",
      "methods": ["get", "post"],
      "route": "graphql/resources"
    },
    {
      "type": "http",
      "direction": "out",
      "name": "$return"
    }
  ],
  "scriptFile": "../dist/index.js"
}`)

	result := NewAzureFunctionsParser().ParseFile("resources/function.json", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "azure-functions" {
		t.Fatalf("expected azure-functions language, got %q", result.Language)
	}
	if len(result.AzureTriggers) != 1 {
		t.Fatalf("expected 1 trigger binding, got %#v", result.AzureTriggers)
	}
	if result.AzureTriggers[0].FunctionName != "resources" || result.AzureTriggers[0].TriggerType != "httpTrigger" {
		t.Fatalf("unexpected first trigger: %#v", result.AzureTriggers[0])
	}
	if result.AzureTriggers[0].Route != "/api/graphql/resources" {
		t.Fatalf("expected normalized trigger route, got %#v", result.AzureTriggers[0])
	}
	if len(result.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %#v", result.Endpoints)
	}
	if result.Endpoints[0].Path != "/api/graphql/resources" || result.Endpoints[0].Method != "GET" {
		t.Fatalf("unexpected first endpoint: %#v", result.Endpoints[0])
	}
	if result.Endpoints[1].Path != "/api/graphql/resources" || result.Endpoints[1].Method != "POST" {
		t.Fatalf("unexpected second endpoint: %#v", result.Endpoints[1])
	}
}

func TestParserFixtureCorpus_AzureFunctionsTriggers(t *testing.T) {
	content := []byte(`{
  "bindings": [
    {
      "name": "cleanupTimer",
      "type": "timerTrigger",
      "direction": "in",
      "schedule": "0 0 0 * * *"
    },
    {
      "queueName": "%RESOURCE_EVENTS_QUEUE%",
      "name": "queueItem",
      "type": "serviceBusTrigger",
      "direction": "in"
    }
  ],
  "scriptFile": "../dist/Cleanup/index.js"
}`)

	result := NewAzureFunctionsParser().ParseFile("CleanupResources/function.json", content)

	if len(result.Endpoints) != 0 {
		t.Fatalf("expected no HTTP endpoints, got %#v", result.Endpoints)
	}
	if len(result.AzureTriggers) != 2 {
		t.Fatalf("expected 2 triggers, got %#v", result.AzureTriggers)
	}
	if result.AzureTriggers[0].TriggerType != "timerTrigger" || result.AzureTriggers[0].Schedule != "0 0 0 * * *" {
		t.Fatalf("unexpected timer trigger: %#v", result.AzureTriggers[0])
	}
	if result.AzureTriggers[1].TriggerType != "serviceBusTrigger" || result.AzureTriggers[1].ResourceName != "%RESOURCE_EVENTS_QUEUE%" {
		t.Fatalf("unexpected service bus trigger: %#v", result.AzureTriggers[1])
	}
}

func TestParserFixtureCorpus_AzureFunctionsServiceBusSubscriptionResource(t *testing.T) {
	content := []byte(`{
  "bindings": [
    {
      "topicName": "resource-events",
      "subscriptionName": "resource-worker",
      "name": "queueItem",
      "type": "serviceBusTrigger",
      "direction": "in"
    }
  ]
}`)

	result := NewAzureFunctionsParser().ParseFile("ProcessResources/function.json", content)

	if len(result.AzureTriggers) != 1 {
		t.Fatalf("expected 1 trigger, got %#v", result.AzureTriggers)
	}
	if result.AzureTriggers[0].ResourceName != "resource-events/subscriptions/resource-worker" {
		t.Fatalf("unexpected service bus subscription resource: %#v", result.AzureTriggers[0])
	}
}

func TestParserFixtureCorpus_AzureFunctionsDefaultRoute(t *testing.T) {
	content := []byte(`{
  "bindings": [
    {
      "type": "httpTrigger",
      "direction": "in",
      "name": "req"
    }
  ],
  "scriptFile": "../dist/index.js"
}`)

	result := NewAzureFunctionsParser().ParseFile("GetRoles/function.json", content)

	if len(result.Endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %#v", result.Endpoints)
	}
	if result.Endpoints[0].Path != "/api/GetRoles" || result.Endpoints[0].Method != "REQUEST" {
		t.Fatalf("unexpected default route endpoint: %#v", result.Endpoints[0])
	}
}
