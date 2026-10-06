package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWorktreeAtCommitPreservesNonemptyTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "worktrees", "repo")
	runGit(t, root, "init", source)
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")
	commit := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))

	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "partial.txt"), []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureWorktreeAtCommit(source, target, commit); err == nil {
		t.Fatal("expected nonempty target to be rejected")
	}
	data, err := os.ReadFile(filepath.Join(target, "partial.txt"))
	if err != nil || string(data) != "broken\n" {
		t.Fatalf("existing target content changed: %q, %v", data, err)
	}
	fresh := filepath.Join(root, "worktrees", "fresh")
	if err := EnsureWorktreeAtCommit(source, fresh, commit); err != nil {
		t.Fatalf("creating fresh worktree: %v", err)
	}
	status, err := InspectRepo(fresh)
	if err != nil {
		t.Fatalf("InspectRepo() error = %v", err)
	}
	if status.HeadSHA != commit {
		t.Fatalf("worktree HEAD = %s, want %s", status.HeadSHA, commit)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
