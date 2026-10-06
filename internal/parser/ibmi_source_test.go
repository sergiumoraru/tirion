package parser

import "testing"

func TestIBMISourceMemberDetection(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/tmp/LIB/QRPGLESRC/CBIBILLP", want: "rpgle"},
		{path: "/tmp/LIB/QRPGSRC/LEGACYPGM.MBR", want: "rpg"},
		{path: "/tmp/LIB/QSQLRPGLESRC/ORDSQL.MBR", want: "sqlrpgle"},
		{path: "/tmp/LIB/QCLSRC/BILLDRV.MBR", want: "cl"},
		{path: "/tmp/LIB/QCLLESRC/BILLDRV", want: "clle"},
		{path: "/tmp/LIB/QCPYSRC/CBI_SHARED", want: "rpgleinc"},
		{path: "/tmp/LIB/QDDSSRC/ORDHDR", want: "dds"},
		{path: "/tmp/LIB/QDSPSRC/ORDERDSP.MBR", want: "dspf"},
		{path: "/tmp/LIB/QPRTSRC/INVPRT.TXT", want: "prtf"},
	}

	for _, tt := range tests {
		got, ok := detectIBMISourceMemberKind(tt.path)
		if !ok {
			t.Fatalf("expected %s to be detected", tt.path)
		}
		if got != tt.want {
			t.Fatalf("unexpected kind for %s: got=%q want=%q", tt.path, got, tt.want)
		}
	}

	if got, ok := detectIBMISourceMemberKind("/tmp/LIB/QRPGLESRC/CBIBILLP.java"); ok || got != "" {
		t.Fatalf("did not expect normal non-member extension to be treated as IBM i source member, got=%q ok=%v", got, ok)
	}
}

func TestRPGParser_ParsesIBMISourceMemberPaths(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
dcl-proc SubmitOrder export;
  dcl-pi *n end-pi;
end-proc;`)

	if !p.CanParse("/tmp/LIB/QRPGLESRC/CBIORD100.MBR") {
		t.Fatalf("expected QRPGLESRC member path to parse as RPG")
	}
	result := p.ParseFile("/tmp/LIB/QRPGLESRC/CBIORD100.MBR", content)
	if result.Language != "rpgle" {
		t.Fatalf("expected rpgle language for member path, got %q", result.Language)
	}
	if !hasFunction(result.Functions, "SubmitOrder") {
		t.Fatalf("expected SubmitOrder in parsed member path, got %#v", result.Functions)
	}
}

func TestRPGParser_UsesMemberNameForIBMIMainline(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
SubmitOrder();
return;

dcl-proc SubmitOrder export;
  dcl-pi *n end-pi;
end-proc;`)

	result := p.ParseFile("/tmp/LIB/QRPGLESRC/CBIORD100.MBR", content)
	if !hasFunction(result.Functions, "CBIORD100") {
		t.Fatalf("expected source-member mainline to use member name, got %#v", result.Functions)
	}
	if hasFunction(result.Functions, "_MAIN") {
		t.Fatalf("did not expect generic _MAIN for IBM i source member, got %#v", result.Functions)
	}
	for _, fn := range result.Functions {
		if fn.Name == "CBIORD100" {
			if fn.EndLine != 3 {
				t.Fatalf("expected member mainline to include trailing return, got end line %d with source %#v", fn.EndLine, fn.SourceCode)
			}
		}
	}
}

func TestCLParser_ParsesIBMISourceMemberPaths(t *testing.T) {
	p := NewCLParser()
	content := []byte(`PGM
             CALL PGM(CBIBILLP)
ENDPGM`)

	if !p.CanParse("/tmp/LIB/QCLSRC/CBIBILLDRV") {
		t.Fatalf("expected QCLSRC member path to parse as CL")
	}
	result := p.ParseFile("/tmp/LIB/QCLSRC/CBIBILLDRV", content)
	if result.Language != "cl" {
		t.Fatalf("expected cl language for member path, got %q", result.Language)
	}
	if !hasFunction(result.Functions, "CBIBILLDRV") {
		t.Fatalf("expected program entry from member path, got %#v", result.Functions)
	}
}

func TestDDSParser_ParsesIBMISourceMemberPaths(t *testing.T) {
	p := NewDDSParser()
	content := []byte(`A          R ORENTRY
A            ORDERID       12A`)

	if !p.CanParse("/tmp/LIB/QDSPSRC/ORDERDSP.MBR") {
		t.Fatalf("expected QDSPSRC member path to parse as DDS")
	}
	result := p.ParseFile("/tmp/LIB/QDSPSRC/ORDERDSP.MBR", content)
	if result.Language != "dspf" {
		t.Fatalf("expected dspf language for member path, got %q", result.Language)
	}
	if !hasClass(result.Classes, "ORDERDSP.ORENTRY") {
		t.Fatalf("expected record format class from member path, got %#v", result.Classes)
	}
}
