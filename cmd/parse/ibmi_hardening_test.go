package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sergiumoraru/tirion/internal/parser"
)

func TestApplyRPGCopyAliasExpansion_RewritesImportedPrototypeCalls(t *testing.T) {
	repoRoot := t.TempDir()
	sharedDir := filepath.Join(repoRoot, "shared")
	srcDir := filepath.Join(repoRoot, "worker")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	copyPath := filepath.Join(sharedDir, "SHARED.rpgleinc")
	if err := os.WriteFile(copyPath, []byte(`**free
dcl-pr AuditEvent extproc('LOGEVENT');
  Domain char(12) const;
  RefId char(12) const;
end-pr;

dcl-pr TransformValue extproc('TRANSFORM');
  InputValue packed(11:2) const;
  OutputValue packed(11:2);
end-pr;
`), 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	content := []byte(`**free
/copy ../shared/SHARED.rpgleinc

dcl-proc WORKER export;
  dcl-pi *n end-pi;
  TransformValue(100: OutputValue);
  AuditEvent('WORKER': 'R1');
end-proc;
`)

	result := parser.NewRPGParser().ParseFile("worker/WORKER.RPGLE", content)
	applyRPGCopyAliasExpansion("worker/WORKER.RPGLE", &result, buildIBMiIncludeResolver(repoRoot))

	if !hasCallName(result.FunctionCalls["WORKER"], "TRANSFORM") {
		t.Fatalf("expected imported TransformValue to normalize to TRANSFORM, got %#v", result.FunctionCalls["WORKER"])
	}
	if !hasCallName(result.FunctionCalls["WORKER"], "LOGEVENT") {
		t.Fatalf("expected imported AuditEvent to normalize to LOGEVENT, got %#v", result.FunctionCalls["WORKER"])
	}
}

func TestIBMiIncludeResolver_ResolvesSourcefileMemberNotation(t *testing.T) {
	repoRoot := t.TempDir()
	memberDir := filepath.Join(repoRoot, "QRPGLESRC")
	if err := os.MkdirAll(memberDir, 0o755); err != nil {
		t.Fatalf("mkdir member dir: %v", err)
	}
	memberPath := filepath.Join(memberDir, "COMMON.RPGLEINC")
	if err := os.WriteFile(memberPath, []byte("**free\n"), 0o644); err != nil {
		t.Fatalf("write member: %v", err)
	}

	resolver := buildIBMiIncludeResolver(repoRoot)
	resolved, ok := resolver.Resolve("src/FOO.RPGLE", "qrpglesrc,common")
	if !ok {
		t.Fatal("expected resolver to find source member import")
	}
	if resolved != memberPath {
		t.Fatalf("unexpected resolved path: got=%s want=%s", resolved, memberPath)
	}
}

func TestIBMiIncludeResolver_ResolvesCrossRepoRelativePath(t *testing.T) {
	workspaceRoot := t.TempDir()
	repoRoot := filepath.Join(workspaceRoot, "resource-worker")
	sharedRoot := filepath.Join(workspaceRoot, "shared-library")
	if err := os.MkdirAll(filepath.Join(repoRoot, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sharedRoot, "copy"), 0o755); err != nil {
		t.Fatalf("mkdir shared copy: %v", err)
	}

	includePath := filepath.Join(sharedRoot, "copy", "SHARED.rpgleinc")
	if err := os.WriteFile(includePath, []byte("**free\n"), 0o644); err != nil {
		t.Fatalf("write include: %v", err)
	}

	resolver := buildIBMiIncludeResolver(repoRoot)
	resolved, ok := resolver.Resolve("src/WORKER.RPGLE", "../shared-library/copy/SHARED.rpgleinc")
	if !ok {
		t.Fatal("expected resolver to find sibling-repo copy member")
	}
	if resolved != includePath {
		t.Fatalf("unexpected resolved path: got=%s want=%s", resolved, includePath)
	}
}

func TestIBMiIncludeResolver_DoesNotWalkUntilFirstLookup(t *testing.T) {
	repoRoot := t.TempDir()
	// A missing root would make an eager walk record an error.
	resolver := buildIBMiIncludeResolver(filepath.Join(repoRoot, "does-not-exist"))
	if resolver.indexed || len(resolver.byRel) != 0 || resolver.err != nil {
		t.Fatalf("building a resolver must not touch the filesystem: %+v", resolver)
	}
	if _, ok := (*ibmiIncludeResolver)(nil).Resolve("a", "b"); ok {
		t.Fatal("nil resolver must not resolve")
	}
}

func TestIBMiIncludeResolver_IndexSkipsDependencyAndBuildDirectories(t *testing.T) {
	repoRoot := t.TempDir()
	write := func(rel string) string {
		full := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("**free\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return full
	}
	kept := write("QRPGLESRC/KEPT.RPGLEINC")
	external := write("external/EXTCOPY.RPGLEINC")
	write("node_modules/dep/NMCOPY.RPGLEINC")
	write(".hg/store/HGCOPY.RPGLEINC")
	write("vendor/VENDORCOPY.RPGLEINC")
	write("package.json")
	write("dist/DISTCOPY.RPGLEINC")

	cases := []struct {
		name   string
		member string
		want   string
	}{
		{"source member is indexed", "qrpglesrc,kept", kept},
		{"external is ordinary source", "extcopy", external},
		{"node_modules is not indexed", "nmcopy", ""},
		{"VCS metadata is not indexed", "hgcopy", ""},
		{"vendor is not indexed", "vendorcopy", ""},
		{"build output beside package.json is not indexed", "distcopy", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := buildIBMiIncludeResolver(repoRoot)
			got, ok := resolver.Resolve("src/FOO.RPGLE", tc.member)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.member, got, ok, tc.want)
			}
			if resolver.err != nil {
				t.Fatalf("unexpected error: %v", resolver.err)
			}
		})
	}
}

func TestIBMiIncludeResolver_WalkErrorsAreRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission errors cannot be provoked as root")
	}
	repoRoot := t.TempDir()
	locked := filepath.Join(repoRoot, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	resolver := buildIBMiIncludeResolver(repoRoot)
	resolver.Resolve("src/FOO.RPGLE", "missing")
	if resolver.err == nil {
		t.Fatal("an unreadable directory must surface as a resolver error instead of a silently partial index")
	}
}

func hasCallName(calls []parser.ParsedFunctionCall, name string) bool {
	for _, call := range calls {
		if call.CalleeName == name {
			return true
		}
	}
	return false
}
