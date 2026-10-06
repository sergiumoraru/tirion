package mcpintel

import (
	"encoding/json"
	"net/http"
	"testing"
)

func writeTestJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func findTool(t *testing.T, tools []toolSchema, name string) toolSchema {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return toolSchema{}
}

func assertToolProperty(t *testing.T, tool toolSchema, name string) {
	t.Helper()
	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %s has invalid properties schema", tool.Name)
	}
	if _, ok := properties[name]; !ok {
		t.Fatalf("tool %s missing property %s", tool.Name, name)
	}
}
