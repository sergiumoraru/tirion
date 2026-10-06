package indexer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sergiumoraru/tirion/internal/graph"
)

// Uses real native parsing/enrichment and a dedicated database. Build cmd/* into
// TIRION_TEST_BIN and explicitly opt in; ordinary contributor tests need no DB.
func TestPipelineRetriesFailedSameHEADWithLastGoodSnapshot(t *testing.T) {
	dbURL, bin := liveEnvironment(t, "TIRION_PIPELINE_LIVE_TESTS", true)
	if runtime.GOOS == "windows" {
		t.Skip("helper failure injection uses a POSIX shell")
	}
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	helperDir := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	for _, dir := range []string{repo, helperDir, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, helper := range []string{"extract-http", "extract-java-intel", "extract-java-calls", "extract-spring", "extract-ts-intel"} {
		if err := os.Symlink(filepath.Join(bin, helper), filepath.Join(helperDir, helper)); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "fail-helper")
	useHelperDirectory(t, helperDir)
	// Helpers run with a filtered environment, so the paths are baked in.
	wrapper := fmt.Sprintf("#!/bin/sh\nif [ -e %q ]; then echo synthetic-helper-failure >&2; exit 71; fi\nexec %q \"$@\"\n", marker, filepath.Join(bin, "extract-sqs"))
	if err := os.WriteFile(filepath.Join(helperDir, "extract-sqs"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.js"), []byte("function caller() { target(); }\nfunction target() { return 1; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "app.js"}, {"-c", "user.name=Synthetic Validation", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "Synthetic source"}} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %v\n%s", err, out)
		}
	}
	name := fmt.Sprintf("pipeline-retry-%d", time.Now().UnixNano())
	storage, err := graph.NewStorage(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	// Only the default workspace is created on demand; others must exist.
	if _, err := storage.EnsureWorkspace(name, name, "", "", false); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := storage.Pool().Exec(ctx, `DELETE FROM workspaces WHERE slug=$1`, name); err != nil {
			t.Errorf("cleanup workspace: %v", err)
		}
		if _, err := storage.Pool().Exec(ctx, `DELETE FROM repositories WHERE name=$1`, name); err != nil {
			t.Errorf("cleanup repository: %v", err)
		}
	}()
	opts := PipelineOptions{DBURL: dbURL, Workspace: name, ParseBinary: filepath.Join(bin, "parse"), Repos: []RepoEntry{{Name: name, Path: repo}}, SkipTests: true, SkipUnchanged: true, Resolve: true}
	run := func() BatchResult {
		t.Helper()
		result, err := RunRepositories(opts)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	selection := func() int64 {
		t.Helper()
		refs, err := storage.ActiveSnapshotsForWorkspace(name)
		if err != nil || len(refs) != 1 {
			t.Fatalf("active selection: %+v, %v", refs, err)
		}
		return refs[0].SnapshotID
	}
	if result := run(); result.Parsed != 1 {
		t.Fatalf("initial run: %+v", result)
	}
	before := selection()
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts.SkipUnchanged = false
	if _, err := RunRepositories(opts); err == nil {
		t.Fatal("injected helper failure reported success")
	}
	if got := selection(); got != before {
		t.Fatalf("failed attempt replaced last good snapshot: %d -> %d", before, got)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	opts.SkipUnchanged = true
	if result := run(); result.Parsed != 1 || result.Skipped != 0 {
		t.Fatalf("same-HEAD failed attempt was not retried: %+v", result)
	}
	if selection() == before {
		t.Fatal("successful retry did not publish a new generation")
	}
	if result := run(); result.Skipped != 1 || result.Parsed != 0 {
		t.Fatalf("completed retry did not regain normal freshness: %+v", result)
	}
}
