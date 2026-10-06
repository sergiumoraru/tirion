package parser

import "testing"

func TestParserFixtureCorpus_AzureHostDefaultPrefix(t *testing.T) {
	content := []byte(`{
  "version": "2.0"
}`)

	result := NewAzureHostParser().ParseFile("host.json", content)

	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.AzureHostConfigs) != 1 {
		t.Fatalf("expected one host config, got %#v", result.AzureHostConfigs)
	}
	if result.AzureHostConfigs[0].RoutePrefix != "api" {
		t.Fatalf("expected default route prefix api, got %#v", result.AzureHostConfigs[0])
	}
}

func TestParserFixtureCorpus_AzureHostCustomPrefix(t *testing.T) {
	content := []byte(`{
  "version": "2.0",
  "extensions": {
    "http": {
      "routePrefix": ""
    }
  }
}`)

	result := NewAzureHostParser().ParseFile("host.json", content)

	if len(result.AzureHostConfigs) != 1 {
		t.Fatalf("expected one host config, got %#v", result.AzureHostConfigs)
	}
	if result.AzureHostConfigs[0].RoutePrefix != "" {
		t.Fatalf("expected empty route prefix, got %#v", result.AzureHostConfigs[0])
	}
}
