package parser

import "testing"

func TestResourceConfigParserExtractsConfiguredQueueAliases(t *testing.T) {
	content := []byte(`{
  "RESOURCE_EVENTS_QUEUE": "resource-events",
  "RESOURCE_UPDATES_QUEUE": "resource-updates",
  "SERVICE_BUS_ACCESS_KEY": "do-not-index-secret",
  "nested": {
    "RESOURCE_TASKS_QUEUE": "resource-tasks"
  }
}`)

	result := NewResourceConfigParser().ParseFile("src/core/config.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_EVENTS_QUEUE", "resource-events", "config")
	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_UPDATES_QUEUE", "resource-updates", "config")
	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_TASKS_QUEUE", "resource-tasks", "config")
	assertNoResourceAlias(t, result.ResourceAliases, "SERVICE_BUS_ACCESS_KEY")
}

func TestResourceConfigParserExtractsLocalSettingsAliases(t *testing.T) {
	content := []byte(`{
  "IsEncrypted": false,
  "Values": {
    "AzureWebJobsStorage": "UseDevelopmentStorage=true",
    "RESOURCE_EVENTS_QUEUE": "resource-events",
    "RESOURCE_UPDATES_QUEUE": "resource-updates"
  }
}`)

	result := NewResourceConfigParser().ParseFile("local.settings.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_EVENTS_QUEUE", "resource-events", "config")
	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_UPDATES_QUEUE", "resource-updates", "config")
	assertNoResourceAlias(t, result.ResourceAliases, "AzureWebJobsStorage")
}

func TestResourceConfigParserExtractsLogicAppParameterAliases(t *testing.T) {
	parser := NewResourceConfigParser()
	if !parser.CanParse("templates/workflow.parameters.json") {
		t.Fatal("expected Logic App parameters file to be parsed for resource aliases")
	}

	content := []byte(`{
  "parameters": {
    "logicAppName": {
      "value": "resource-worker"
    },
    "triggerQueueName": {
      "value": "resource-events"
    },
    "serviceBusConnectionString": {
      "value": "Endpoint=sb://example.servicebus.windows.net/;SharedAccessKey=secret"
    }
  }
}`)

	result := parser.ParseFile("templates/workflow.parameters.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertResourceAlias(t, result.ResourceAliases, "triggerQueueName", "resource-events", "config")
	assertNoResourceAlias(t, result.ResourceAliases, "serviceBusConnectionString")
}

func TestResourceConfigParserExtractsLogicAppDefaultValueAliases(t *testing.T) {
	content := []byte(`{
  "parameters": {
    "serviceBusQueueName": {
      "type": "string",
      "defaultValue": "resource-events"
    }
  }
}`)

	result := NewResourceConfigParser().ParseFile("config/workflow.json", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	assertResourceAlias(t, result.ResourceAliases, "serviceBusQueueName", "resource-events", "config")
}

func TestResourceConfigParserExtractsEnvAliases(t *testing.T) {
	content := []byte(`
export RESOURCE_EVENTS_QUEUE=resource-events
RESOURCE_TASKS_QUEUE='resource-tasks'
SERVICE_BUS_ACCESS_KEY=secret-value
`)

	result := NewResourceConfigParser().ParseFile(".env.local", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "env-config" {
		t.Fatalf("language = %q, want env-config", result.Language)
	}

	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_EVENTS_QUEUE", "resource-events", "env")
	assertResourceAlias(t, result.ResourceAliases, "RESOURCE_TASKS_QUEUE", "resource-tasks", "env")
	assertNoResourceAlias(t, result.ResourceAliases, "SERVICE_BUS_ACCESS_KEY")
}

func assertResourceAlias(t *testing.T, aliases []ParsedResourceAlias, key, value, kind string) {
	t.Helper()
	for _, alias := range aliases {
		if alias.Alias == key && alias.Value == value && alias.Kind == kind {
			return
		}
	}
	t.Fatalf("missing resource alias %s=%s kind=%s in %#v", key, value, kind, aliases)
}

func assertNoResourceAlias(t *testing.T, aliases []ParsedResourceAlias, key string) {
	t.Helper()
	for _, alias := range aliases {
		if alias.Alias == key {
			t.Fatalf("unexpected resource alias for %s in %#v", key, aliases)
		}
	}
}
