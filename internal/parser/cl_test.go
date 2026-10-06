package parser

import "testing"

func TestCLParser_ProgramCallsAndDataAccess(t *testing.T) {
	p := NewCLParser()
	content := []byte(`PGM        PARM(&MODE)
             DCLF       FILE(RECORDS)
             CALL       PGM(*LIBL/WORKER) PARM(&MODE)
             SBMJOB     CMD(CALL PGM(UPDATEJOB)) JOB(NIGHTLY)
             CALLPRC    PRC(LOGEVENT)
ENDPGM`)

	result := p.ParseFile("DRIVER.CLLE", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "clle" {
		t.Fatalf("expected language clle, got %q", result.Language)
	}
	if !hasFunction(result.Functions, "DRIVER") {
		t.Fatalf("expected program entry function DRIVER, got %#v", result.Functions)
	}
	if !hasCallee(result.FunctionCalls["DRIVER"], "WORKER") {
		t.Fatalf("expected DRIVER to call WORKER, got %#v", result.FunctionCalls["DRIVER"])
	}
	if !hasCallee(result.FunctionCalls["DRIVER"], "UPDATEJOB") {
		t.Fatalf("expected DRIVER to call UPDATEJOB via SBMJOB CMD(CALL PGM(...)), got %#v", result.FunctionCalls["DRIVER"])
	}
	if !hasCallee(result.FunctionCalls["DRIVER"], "LOGEVENT") {
		t.Fatalf("expected DRIVER to call LOGEVENT via CALLPRC, got %#v", result.FunctionCalls["DRIVER"])
	}
	if !hasDataAccess(result.DataAccesses["DRIVER"], "RECORDS", "read") {
		t.Fatalf("expected DCLF FILE(RECORDS) to produce read access, got %#v", result.DataAccesses["DRIVER"])
	}
	if !hasImport(result.Imports, "RECORDS") {
		t.Fatalf("expected DCLF FILE(RECORDS) to produce DDS import, got %#v", result.Imports)
	}
}

func TestCLParser_SubroutinesAndLabels(t *testing.T) {
	p := NewCLParser()
	content := []byte(`PGM
             MONMSG     MSGID(CPF0000) EXEC(GOTO CMDLBL(ERROR))
             CALLSUBR   SUBR(CLEANUP)
             RETURN
             SUBR       SUBR(CLEANUP)
             CALL       PGM(QCMDEXC) PARM(&CMD 0000000010)
             ENDSUBR
ERROR:       SNDPGMMSG  MSG('Failed')
ENDPGM`)

	result := p.ParseFile("ERRHANDL.CLP", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "clp" {
		t.Fatalf("expected language clp, got %q", result.Language)
	}
	for _, name := range []string{"ERRHANDL", "CLEANUP"} {
		if !hasFunction(result.Functions, name) {
			t.Fatalf("expected function %q, got %#v", name, result.Functions)
		}
	}
	if !hasCallee(result.FunctionCalls["ERRHANDL"], "CLEANUP") {
		t.Fatalf("expected ERRHANDL to call CLEANUP, got %#v", result.FunctionCalls["ERRHANDL"])
	}
	if !hasCallee(result.FunctionCalls["CLEANUP"], "QCMDEXC") {
		t.Fatalf("expected CLEANUP to call QCMDEXC, got %#v", result.FunctionCalls["CLEANUP"])
	}
	if hasCallee(result.FunctionCalls["ERRHANDL"], "ERROR") {
		t.Fatalf("did not expect label ERROR to be treated as call, got %#v", result.FunctionCalls["ERRHANDL"])
	}
}

func TestCLParser_CapturesDisplayInteractions(t *testing.T) {
	p := NewCLParser()
	content := []byte(`PGM
             DCLF       FILE(SCREEN)
             SNDRCVF    RCDFMT(EDITOR)
ENDPGM`)

	result := p.ParseFile("DISPLAY.CLLE", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if !hasDataAccess(result.DataAccesses["DISPLAY"], "EDITOR", "display") {
		t.Fatalf("expected SNDRCVF RCDFMT(EDITOR) to produce display record access, got %#v", result.DataAccesses["DISPLAY"])
	}
	if !hasDataAccess(result.DataAccesses["DISPLAY"], "SCREEN", "display") {
		t.Fatalf("expected SNDRCVF to produce display file access SCREEN, got %#v", result.DataAccesses["DISPLAY"])
	}
}

func TestCLParser_CapturesIBMiBindings(t *testing.T) {
	p := NewCLParser()
	content := []byte(`PGM
             ADDBNDDIRE BNDDIR(TESTLIB/SHARED) +
                          OBJ((TESTLIB/SHARED *SRVPGM *IMMED))
             CRTSRVPGM  SRVPGM(TESTLIB/SHARED) +
                          SRCMBR(EXPORTS)
ENDPGM`)

	result := p.ParseFile("SRCBLDC.CLP", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if !hasIBMiBinding(result.IBMiBindings, "SRCBLDC", "SHARED", "bnddir_entry", "SHARED") {
		t.Fatalf("expected binding-directory entry metadata, got %#v", result.IBMiBindings)
	}
	if !hasIBMiBinding(result.IBMiBindings, "SHARED", "EXPORTS", "binder_source", "SHARED") {
		t.Fatalf("expected binder source metadata, got %#v", result.IBMiBindings)
	}
	if !hasIBMiExport(result.IBMiExports, "SRCBLDC", "SRCBLDC", "SRCBLDC", "program_entry") {
		t.Fatalf("expected program entry export metadata, got %#v", result.IBMiExports)
	}
}
