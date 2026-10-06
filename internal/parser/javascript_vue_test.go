package parser

import "testing"

func TestJavaScriptVueExportDefaultMethodsExtraction(t *testing.T) {
	content := []byte(`
<template><div /></template>
<script>
export default {
  name: 'GenerateReport',
  methods: {
    async generateReport() {
      return this.sendPayloadToReportsApi()
    },
    async sendPayloadToReportsApi() {
      return fetch('/reports/export', { method: 'POST' })
    }
  }
}
</script>
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("generate-report.vue", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	foundGenerate := false
	foundSend := false
	for _, fn := range result.Functions {
		switch fn.Name {
		case "GenerateReport.methods.generateReport":
			foundGenerate = true
		case "GenerateReport.methods.sendPayloadToReportsApi":
			foundSend = true
		}
	}
	if !foundGenerate || !foundSend {
		t.Fatalf("expected Vue export-default methods to be extracted, got %#v", result.Functions)
	}

	calls := result.FunctionCalls["GenerateReport.methods.generateReport"]
	if len(calls) == 0 {
		t.Fatalf("expected calls for GenerateReport.methods.generateReport, got %#v", result.FunctionCalls)
	}
	foundCall := false
	for _, call := range calls {
		if call.CalleeName == "GenerateReport.methods.sendPayloadToReportsApi" || call.CalleeName == "sendPayloadToReportsApi" {
			foundCall = true
			break
		}
	}
	if !foundCall {
		t.Fatalf("expected generateReport method to call sendPayloadToReportsApi, got %#v", calls)
	}

	httpCalls := result.HttpCalls["GenerateReport.methods.sendPayloadToReportsApi"]
	if len(httpCalls) != 1 {
		t.Fatalf("expected one HTTP call for GenerateReport.methods.sendPayloadToReportsApi, got %#v", result.HttpCalls)
	}
	if httpCalls[0].UrlPattern != "/reports/export" || httpCalls[0].HttpMethod != "POST" {
		t.Fatalf("expected POST /reports/export, got %#v", httpCalls[0])
	}
}

func TestJavaScriptAliasesGenericEntryFunctionNameFromIndexFile(t *testing.T) {
	content := []byte(`
const httpTrigger = async (context, req) => {
  return getDocumentInfo(req.body.sessionId)
}

function getDocumentInfo(sessionId) {
  return sessionId
}

export default httpTrigger
`)

	parser := NewJavaScriptParser()
	result := parser.ParseFile("src/GetSessionInfo/index.ts", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	foundAliased := false
	foundGeneric := false
	for _, fn := range result.Functions {
		switch fn.Name {
		case "GetSessionInfo":
			foundAliased = true
		case "httpTrigger":
			foundGeneric = true
		}
	}
	if !foundAliased {
		t.Fatalf("expected generic entry function to be aliased to GetSessionInfo, got %#v", result.Functions)
	}
	if foundGeneric {
		t.Fatalf("expected generic httpTrigger name to be removed after aliasing, got %#v", result.Functions)
	}
}
