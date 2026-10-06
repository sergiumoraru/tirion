package parser

import "testing"

func TestPowerShellParserExtractsFunctionsCmdletsAndHTTPCalls(t *testing.T) {
	content := []byte(`
param([string]$BaseUrl)

function Deploy-Reports {
  $secret = Get-AzKeyVaultSecret -VaultName "reports" -Name "token"
  Invoke-RestMethod -Method Post -Uri "/api/reports/deploy" -Body "{}"
  New-AzResourceGroupDeployment -Name "reports"
}

Invoke-WebRequest -Uri "https://example.com/health"
`)

	result := NewPowerShellParser().ParseFile("deploy.ps1", content)
	if result.Language != "powershell" {
		t.Fatalf("expected powershell language, got %q", result.Language)
	}
	if !hasPowerShellFunction(result.Functions, "Deploy-Reports") {
		t.Fatalf("expected Deploy-Reports function, got %#v", result.Functions)
	}
	for _, name := range []string{"Get-AzKeyVaultSecret", "Invoke-RestMethod", "New-AzResourceGroupDeployment"} {
		if !hasPowerShellCall(result.FunctionCalls["Deploy-Reports"], name) {
			t.Fatalf("expected %s call in Deploy-Reports, got %#v", name, result.FunctionCalls)
		}
	}
	if !hasPowerShellCall(result.FunctionCalls["_module_"], "Invoke-WebRequest") {
		t.Fatalf("expected module-level Invoke-WebRequest call, got %#v", result.FunctionCalls)
	}
	if calls := result.HttpCalls["Deploy-Reports"]; len(calls) != 1 || calls[0].HttpMethod != "POST" || calls[0].UrlPattern != "/api/reports/deploy" {
		t.Fatalf("unexpected Deploy-Reports HTTP calls: %#v", calls)
	}
	if calls := result.HttpCalls["_module_"]; len(calls) != 1 || calls[0].UrlPattern != "https://example.com/health" {
		t.Fatalf("unexpected module HTTP calls: %#v", calls)
	}
}

func hasPowerShellFunction(functions []ParsedFunction, name string) bool {
	for _, fn := range functions {
		if fn.Name == name {
			return true
		}
	}
	return false
}

func hasPowerShellCall(calls []ParsedFunctionCall, name string) bool {
	for _, call := range calls {
		if call.CalleeName == name {
			return true
		}
	}
	return false
}
