package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadIntegrationSourceUsesIndexedBytes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	const file = "routes.txt"
	const indexed = "RESOURCE_PATH=/api/resources\n"
	const changed = "RESOURCE_PATH=/api/resources/v2\n"
	path := filepath.Join(repo, file)
	writeWorkspaceTestFile(t, path, indexed)
	git("add", file)
	git("commit", "-m", "Synthetic source")
	commit := git("rev-parse", "HEAD")
	hash := integrationTestHash(indexed)
	read := func(t *testing.T, revision string) {
		t.Helper()
		got, err := readIntegrationSource(context.Background(), repo, file, hash, revision)
		if err != nil || string(got) != indexed {
			t.Fatalf("read indexed source = %q, %v; want %q", got, err, indexed)
		}
	}
	t.Run("matching checkout needs no Git revision", func(t *testing.T) {
		read(t, "")
	})
	writeWorkspaceTestFile(t, path, changed)
	t.Run("modified checkout falls back to indexed commit", func(t *testing.T) {
		read(t, commit)
	})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	t.Run("missing checkout falls back to indexed commit", func(t *testing.T) {
		read(t, commit)
	})
	t.Run("revision bytes must match indexed hash", func(t *testing.T) {
		got, err := readIntegrationSource(context.Background(), repo, file, integrationTestHash(changed), commit)
		if err == nil || got != nil {
			t.Fatalf("mismatched revision returned %q, %v", got, err)
		}
	})
	for _, revision := range []string{"", "main", strings.Repeat("z", 40), strings.Repeat("0", 40)} {
		t.Run("unavailable revision "+revision, func(t *testing.T) {
			got, err := readIntegrationSource(context.Background(), repo, file, hash, revision)
			if err == nil || got != nil {
				t.Fatalf("unavailable revision returned %q, %v", got, err)
			}
		})
	}
}

func TestReadIntegrationSourceRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "outside repository\n"
	outside := filepath.Join(root, "outside.txt")
	writeWorkspaceTestFile(t, outside, content)
	for _, path := range []string{"../outside.txt", outside} {
		got, err := readIntegrationSource(context.Background(), repo, path, integrationTestHash(content), "")
		if err == nil || got != nil {
			t.Fatalf("escaping path %q returned %q, %v", path, got, err)
		}
	}
	t.Run("symlink outside repository", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(repo, "link.txt")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got, err := readIntegrationSource(context.Background(), repo, "link.txt", integrationTestHash(content), "")
		if err == nil || got != nil {
			t.Fatalf("escaping symlink returned %q, %v", got, err)
		}
	})
}

func TestReadIntegrationSourceHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := readIntegrationSource(ctx, t.TempDir(), "routes.txt", integrationTestHash(""), "")
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancelled read returned %q, %v", got, err)
	}
}

func integrationTestHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
