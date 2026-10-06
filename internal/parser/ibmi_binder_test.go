package parser

import "testing"

func TestIBMiBinderParser_ExtractsExports(t *testing.T) {
	p := NewIBMiBinderParser()
	content := []byte(`STRPGMEXP  PGMLVL(*CURRENT) SIGNATURE('QSHONISRV')
  EXPORT SYMBOL("QSHEXEC")
  EXPORT SYMBOL("QSHBASH")
  EXPORT SYMBOL("QSHCALL")
ENDPGMEXP`)

	result := p.ParseFile("QSHONISRVD.BNDDIR", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "ibmi_binder" {
		t.Fatalf("expected ibmi_binder, got %q", result.Language)
	}
	if len(result.IBMiExports) != 3 {
		t.Fatalf("expected 3 binder exports, got %#v", result.IBMiExports)
	}
	for _, exportName := range []string{"QSHEXEC", "QSHBASH", "QSHCALL"} {
		found := false
		for _, item := range result.IBMiExports {
			if item.ExportName == exportName && item.ObjectName == "QSHONISRV" && item.SourceType == "binder_export" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing binder export %q in %#v", exportName, result.IBMiExports)
		}
	}
}
