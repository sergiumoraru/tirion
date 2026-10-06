package trace

import (
	"strings"
	"testing"
)

func TestAzureTriggerSourcePathCandidatesIncludesDefaultIndexWhenScriptFileBlank(t *testing.T) {
	candidates := azureTriggerSourcePathCandidates("audit-functions/ExportAudit/function.json", "")
	if len(candidates) == 0 {
		t.Fatal("expected default source candidates when script_file is blank")
	}

	foundLocalIndex := false
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate, "/ExportAudit/index.ts") || strings.HasSuffix(candidate, "/ExportAudit/index.js") {
			foundLocalIndex = true
			break
		}
	}
	if !foundLocalIndex {
		t.Fatalf("expected default index candidate in %#v", candidates)
	}
}
