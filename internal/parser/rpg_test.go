package parser

import "testing"

func TestRPGParser_FreeFormSQLRPGLE(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
/copy qrpglesrc,common
ctl-opt dftactgrp(*no);

dcl-f CUSTF keyed;

begsr Cleanup;
  *inlr = *on;
endsr;

ProcessCustomer();
exsr Cleanup;

dcl-proc ProcessCustomer;
  dcl-pi *n end-pi;
  chain CustNo CUSTF;
  callp SendAudit();
  exec sql
    select name
      into :CustomerName
      from customers
     where custno = :CustNo;
end-proc;

dcl-proc SendAudit;
  dcl-pi *n end-pi;
  update CUSTF;
end-proc;
`)

	result := p.ParseFile("customer.sqlrpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "sqlrpgle" {
		t.Fatalf("expected language sqlrpgle, got %q", result.Language)
	}

	for _, name := range []string{"_MAIN", "Cleanup", "ProcessCustomer", "SendAudit"} {
		if !hasFunction(result.Functions, name) {
			t.Fatalf("expected function %q, got %#v", name, result.Functions)
		}
	}

	if !hasImport(result.Imports, "qrpglesrc,common") {
		t.Fatalf("expected /copy import, got %#v", result.Imports)
	}
	if !hasImport(result.Imports, "CUSTF") {
		t.Fatalf("expected dcl-f import CUSTF, got %#v", result.Imports)
	}

	if !hasCallee(result.FunctionCalls["_MAIN"], "ProcessCustomer") {
		t.Fatalf("expected _MAIN to call ProcessCustomer, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasCallee(result.FunctionCalls["_MAIN"], "Cleanup") {
		t.Fatalf("expected _MAIN to call Cleanup, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasCallee(result.FunctionCalls["ProcessCustomer"], "SendAudit") {
		t.Fatalf("expected ProcessCustomer to call SendAudit, got %#v", result.FunctionCalls["ProcessCustomer"])
	}

	if !hasDataAccess(result.DataAccesses["ProcessCustomer"], "CUSTF", "read") {
		t.Fatalf("expected ProcessCustomer read access to CUSTF, got %#v", result.DataAccesses["ProcessCustomer"])
	}
	if !hasDataAccess(result.DataAccesses["ProcessCustomer"], "CUSTOMERS", "read") {
		t.Fatalf("expected ProcessCustomer SQL read access to CUSTOMERS, got %#v", result.DataAccesses["ProcessCustomer"])
	}
	if !hasDataAccess(result.DataAccesses["SendAudit"], "CUSTF", "write") {
		t.Fatalf("expected SendAudit write access to CUSTF, got %#v", result.DataAccesses["SendAudit"])
	}
}

func TestRPGParser_FixedFormRPG(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`     H DFTACTGRP(*NO)
     FITEMF     IF   E           K DISK

     C                   EXSR      INIT
     C                   CALL      'RUNBATCH'
     C                   SETON                     LR

     C     INIT          BEGSR
     C     ITEMKEY       CHAIN     ITEMF
     C                   EXSR      POST
     C                   ENDSR

     P RUNWORK         B
     C                   READ      ITEMF
     C                   CALLP     POST
     P RUNWORK         E

     C     POST          BEGSR
     C                   UPDATE    ITEMF
     C                   ENDSR
`)

	result := p.ParseFile("legacy.rpg", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if result.Language != "rpg" {
		t.Fatalf("expected language rpg, got %q", result.Language)
	}

	for _, name := range []string{"_MAIN", "INIT", "RUNWORK", "POST"} {
		if !hasFunction(result.Functions, name) {
			t.Fatalf("expected function %q, got %#v", name, result.Functions)
		}
	}

	if !hasCallee(result.FunctionCalls["_MAIN"], "INIT") {
		t.Fatalf("expected _MAIN to call INIT, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasCallee(result.FunctionCalls["_MAIN"], "RUNBATCH") {
		t.Fatalf("expected _MAIN to call RUNBATCH, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasCallee(result.FunctionCalls["INIT"], "POST") {
		t.Fatalf("expected INIT to call POST, got %#v", result.FunctionCalls["INIT"])
	}
	if !hasCallee(result.FunctionCalls["RUNWORK"], "POST") {
		t.Fatalf("expected RUNWORK to call POST, got %#v", result.FunctionCalls["RUNWORK"])
	}

	if !hasDataAccess(result.DataAccesses["INIT"], "ITEMF", "read") {
		t.Fatalf("expected INIT read access to ITEMF, got %#v", result.DataAccesses["INIT"])
	}
	if !hasDataAccess(result.DataAccesses["RUNWORK"], "ITEMF", "read") {
		t.Fatalf("expected RUNWORK read access to ITEMF, got %#v", result.DataAccesses["RUNWORK"])
	}
	if !hasDataAccess(result.DataAccesses["POST"], "ITEMF", "write") {
		t.Fatalf("expected POST write access to ITEMF, got %#v", result.DataAccesses["POST"])
	}
}

func TestRPGParser_IgnoresDeclarationPseudoCalls(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`     H NOMAIN
     D ReadValue       PR            64A   varying
     D                                     ExtProc('READVALUE')
     D   InputKey                     8a   const
     D SomeConst        C                   CONST('abc')

     P real_proc        B
     D real_proc        PI            10I 0
     C                   callp     SetError('boom')
     P                 E
`)

	result := p.ParseFile("decls.rpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if hasCallee(result.FunctionCalls["_MAIN"], "ExtProc") || hasCallee(result.FunctionCalls["_MAIN"], "const") || hasCallee(result.FunctionCalls["_MAIN"], "options") {
		t.Fatalf("did not expect declaration pseudo-calls in _MAIN, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasCallee(result.FunctionCalls["real_proc"], "SetError") {
		t.Fatalf("expected real call SetError in procedure body, got %#v", result.FunctionCalls["real_proc"])
	}
}

func TestRPGParser_IgnoresFixedFormContinuationPseudoCall(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`     P TESTPROC        B
     D TESTPROC        PI            10I 0
     C                   if        valueCount >= 0 and
     C                             (inputPtr<>*null or outputPtr<>*null)
     C                   callp     RealProc()
     P                 E
`)

	result := p.ParseFile("continuation.rpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if hasCallee(result.FunctionCalls["TESTPROC"], "c") {
		t.Fatalf("did not expect continuation line to produce callee c, got %#v", result.FunctionCalls["TESTPROC"])
	}
	if !hasCallee(result.FunctionCalls["TESTPROC"], "RealProc") {
		t.Fatalf("expected RealProc call in TESTPROC, got %#v", result.FunctionCalls["TESTPROC"])
	}
}

func TestRPGParser_NormalizesExternalAliasesAndOpcodeExtenders(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
dcl-f CUSTF keyed;

dcl-pr CmdRunner extpgm('QCMDEXC');
  Cmd char(512);
  Lgth packed(15:5) const;
end-pr;

dcl-s Cmd char(512);
Cmd = 'DLTOVR FILE(CUSTF)';
CmdRunner(Cmd: 512);
callp(e) CmdRunner(Cmd: 512);
chain(e) CustNo CUSTF;
`)

	result := p.ParseFile("aliases.rpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasCallee(result.FunctionCalls["_MAIN"], "QCMDEXC") {
		t.Fatalf("expected _MAIN to normalize external program alias to QCMDEXC, got %#v", result.FunctionCalls["_MAIN"])
	}
	if hasCallee(result.FunctionCalls["_MAIN"], "CmdRunner") {
		t.Fatalf("did not expect local prototype alias CmdRunner in _MAIN calls, got %#v", result.FunctionCalls["_MAIN"])
	}
	if !hasDataAccess(result.DataAccesses["_MAIN"], "CUSTF", "read") {
		t.Fatalf("expected CHAIN(E) to produce read access to CUSTF, got %#v", result.DataAccesses["_MAIN"])
	}
	if !hasImport(result.Imports, "CUSTF") {
		t.Fatalf("expected dcl-f import CUSTF, got %#v", result.Imports)
	}
}

func TestRPGParser_CapturesDisplayFileInteractions(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
dcl-f ORDERDSP workstn;

dcl-proc SubmitOrder export;
  dcl-pi *n end-pi;

  exfmt ORENTRY;
end-proc;
`)

	result := p.ParseFile("display.rpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if !hasImport(result.Imports, "ORDERDSP") {
		t.Fatalf("expected workstn file ORDERDSP to be captured as import, got %#v", result.Imports)
	}
	if !hasDataAccess(result.DataAccesses["SubmitOrder"], "ORENTRY", "display") {
		t.Fatalf("expected EXFMT ORENTRY to produce display record access, got %#v", result.DataAccesses["SubmitOrder"])
	}
	if !hasDataAccess(result.DataAccesses["SubmitOrder"], "ORDERDSP", "display") {
		t.Fatalf("expected EXFMT ORENTRY to produce display file access ORDERDSP, got %#v", result.DataAccesses["SubmitOrder"])
	}
}

func TestRPGParser_NoMainDoesNotCreateSyntheticMainline(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
ctl-opt nomain;

dcl-pr QcmdExc extpgm('QCMDEXC');
  cmd varchar(512) const;
  len packed(15:5) const;
end-pr;

dcl-proc RunClCmd export;
  dcl-pi *n int(10);
    cmdln varchar(512) const;
  end-pi;

  QcmdExc(%trim(cmdln): %len(cmdln));
  return 0;
end-proc;
`)

	result := p.ParseFile("nomain.rpgle", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if hasFunction(result.Functions, "_MAIN") {
		t.Fatalf("did not expect synthetic _MAIN in NOMAIN source, got %#v", result.Functions)
	}
	if !hasFunction(result.Functions, "RunClCmd") {
		t.Fatalf("expected RunClCmd function, got %#v", result.Functions)
	}
	if !hasCallee(result.FunctionCalls["RunClCmd"], "QCMDEXC") {
		t.Fatalf("expected RunClCmd to call QCMDEXC, got %#v", result.FunctionCalls["RunClCmd"])
	}
}

func TestRPGParser_PrototypeHeaderDoesNotCreateSyntheticMainline(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
dcl-ds *n psds;
  progid *proc;
end-ds;

dcl-pr QcmdExc extpgm('QCMDEXC');
  cmd varchar(512) const;
  len packed(15:5) const;
end-pr;

dcl-pr RunClCmd int(10);
  cmdln varchar(512) const;
end-pr;
`)

	result := p.ParseFile("WORKERH.RPGLE", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}

	if hasFunction(result.Functions, "_MAIN") {
		t.Fatalf("did not expect synthetic _MAIN in prototype header, got %#v", result.Functions)
	}
	if len(result.Functions) != 0 {
		t.Fatalf("expected no executable functions in prototype header, got %#v", result.Functions)
	}
}

func TestRPGParser_CapturesIBMiBindingsAndExports(t *testing.T) {
	p := NewRPGParser()
	content := []byte(`**free
ctl-opt nomain bnddir('TESTLIB/SHARED':'QC2LE');

dcl-proc RunClCmd export;
  dcl-pi *n int(10);
  end-pi;
  return 0;
end-proc;
`)

	result := p.ParseFile("WORKER.RPGLE", content)
	if result.ParseDiagnostics.Failed() {
		t.Fatalf("unexpected parse diagnostics: %#v", result.ParseDiagnostics)
	}
	if len(result.IBMiBindings) < 2 {
		t.Fatalf("expected IBM i binding refs, got %#v", result.IBMiBindings)
	}
	if !hasIBMiBinding(result.IBMiBindings, "WORKER", "SHARED", "bnddir_ref", "") {
		t.Fatalf("expected SHARED binding ref, got %#v", result.IBMiBindings)
	}
	if !hasIBMiBinding(result.IBMiBindings, "WORKER", "QC2LE", "bnddir_ref", "") {
		t.Fatalf("expected QC2LE binding ref, got %#v", result.IBMiBindings)
	}
	if !hasIBMiExport(result.IBMiExports, "RunClCmd", "RUNCLCMD", "WORKER", "procedure_export") {
		t.Fatalf("expected exported procedure metadata, got %#v", result.IBMiExports)
	}
}

func hasDataAccess(accesses []ParsedDataAccess, entity string, access string) bool {
	for _, item := range accesses {
		if item.EntityName == entity && item.Access == access {
			return true
		}
	}
	return false
}

func hasImport(imports []ParsedImport, path string) bool {
	for _, item := range imports {
		if item.Path == path {
			return true
		}
	}
	return false
}

func hasIBMiBinding(bindings []ParsedIBMiBinding, ownerObject, bindingName, bindingType, targetObject string) bool {
	for _, item := range bindings {
		if item.OwnerObject == ownerObject && item.BindingName == bindingName && item.BindingType == bindingType && item.TargetObject == targetObject {
			return true
		}
	}
	return false
}

func hasIBMiExport(exports []ParsedIBMiExport, functionName, exportName, objectName, sourceType string) bool {
	for _, item := range exports {
		if item.FunctionName == functionName && item.ExportName == exportName && item.ObjectName == objectName && item.SourceType == sourceType {
			return true
		}
	}
	return false
}
