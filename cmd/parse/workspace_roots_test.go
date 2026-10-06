package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredWorkspaceRootDirsFromWorkspaceManifests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
  "workspaces": ["libs/*", "apps/demo", "!ignored/*"]
}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(`packages:
  - "packages/*"
  - tools/*
`), 0o644); err != nil {
		t.Fatalf("write pnpm-workspace.yaml: %v", err)
	}

	got := declaredWorkspaceRootDirs(dir)
	for _, want := range []string{"libs", "apps", "packages", "tools"} {
		if !got[want] {
			t.Fatalf("expected workspace root %q in %#v", want, got)
		}
	}
	if got["ignored"] {
		t.Fatalf("negated workspace pattern should not be included: %#v", got)
	}
}

func TestWorkspacePatternRootDirRejectsGlobbedFirstSegment(t *testing.T) {
	if got := workspacePatternRootDir("*/src"); got != "" {
		t.Fatalf("workspacePatternRootDir() = %q, want empty", got)
	}
	if got := workspacePatternRootDir("./libs/@scope/pkg"); got != "libs" {
		t.Fatalf("workspacePatternRootDir() = %q, want libs", got)
	}
}
