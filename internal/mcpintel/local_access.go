package mcpintel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
)

func openLocalRoots() ([]*os.Root, error) {
	var roots []*os.Root
	for _, path := range []string{os.Getenv("TIRION_REPOS_ROOT"), getEnvFirst("TIRION_WORKDIR", "CODEBASE_INTEL_WORKDIR")} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			for _, root := range roots {
				root.Close()
			}
			return nil, fmt.Errorf("MCP local roots must be absolute")
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err == nil && filepath.Dir(canonical) == canonical {
			err = fmt.Errorf("MCP local root cannot be a filesystem root")
		}
		if err != nil {
			for _, root := range roots {
				root.Close()
			}
			return nil, err
		}
		root, err := os.OpenRoot(canonical)
		if err != nil {
			for _, root := range roots {
				root.Close()
			}
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// localPath matches lexical paths to roots opened at startup. os.Root performs
// traversal-resistant opens, including validation of symlink destinations.
func (s *mcpServer) localPath(path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		if s.workdir == "" {
			return nil, "", fmt.Errorf("relative paths require an explicit TIRION_WORKDIR")
		}
		path = filepath.Join(s.workdir, path)
	}
	path = filepath.Clean(path)
	for _, root := range s.localRoots {
		relative, err := filepath.Rel(root.Name(), path)
		if err == nil && filepath.IsLocal(relative) {
			return root, relative, nil
		}
	}
	// Accept an operator's symlink spelling only when its resolved target remains
	// within an allowed root; the final read still goes through os.Root.
	if canonical, err := filepath.EvalSymlinks(path); err == nil && canonical != path {
		for _, root := range s.localRoots {
			relative, err := filepath.Rel(root.Name(), canonical)
			if err == nil && filepath.IsLocal(relative) {
				return root, relative, nil
			}
		}
	}

	return nil, "", fmt.Errorf("path is outside the configured MCP roots (TIRION_REPOS_ROOT / TIRION_WORKDIR)")
}

func (s *mcpServer) gitRepository(path string) (string, error) {
	root, relative, err := s.localPath(path)
	if err != nil {
		return "", err
	}
	dir, err := root.OpenRoot(relative)
	if err != nil {
		return "", err
	}
	dir.Close()
	// Git needs a real path; verify both its canonical location and actual top
	// level. Never allow Git's implicit no-index mode or parent-repository fallback.
	canonical, err := filepath.EvalSymlinks(filepath.Join(root.Name(), relative))
	if err != nil {
		return "", err
	}
	if _, _, err = s.localPath(canonical); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := localGitCommand(ctx, canonical, "rev-parse", "--show-toplevel")
	var output limitedOutput
	output.limit = 4096
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("repoPath must be a Git worktree root: %w", err)
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(output.buffer.String()))
	if err != nil || top != canonical {
		return "", fmt.Errorf("repoPath must be the Git worktree root")
	}
	return canonical, nil
}

func localGitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	prefix := []string{"--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-C", repo}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.Env = runtimeconfig.GitEnvironment()
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// LocalGitDiffFromEnv supports native clients using the same explicit local trust
// configuration as mcp-intel. The working directory is never inferred from CWD.
func LocalGitDiffFromEnv() (string, error) {
	roots, err := openLocalRoots()
	if err != nil {
		return "", err
	}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	server := mcpServer{localRoots: roots, workdir: getEnvFirst("TIRION_WORKDIR", "CODEBASE_INTEL_WORKDIR")}
	return server.localGitDiff()
}
